package main

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
)

// H264 NAL unit types for RTP
const (
	nalTypeSingleNALUMin = 1
	nalTypeSingleNALUMax = 23
	nalTypeSTAPA         = 24
	nalTypeFUA           = 28

	fuaHeaderSize       = 2
	stapaHeaderSize     = 1
	stapaNALULengthSize = 2

	naluTypeBitmask   = 0x1F
	naluRefIdcBitmask = 0x60
	fuStartBitmask    = 0x80
	fuEndBitmask      = 0x40
)

// EncryptedH264Depacketizer reassembles H264 NAL units from RTP packets
// and decrypts them using E2EE.
//
// Unlike the standard pion H264Packet, this depacketizer is designed to
// work with encrypted payloads where:
//   - The NAL header byte is unencrypted (used for packetization decisions)
//   - The rest of the NAL unit is encrypted with E2EE format
//
// When the SDK encrypts an H264 NAL unit and then packetizes it:
//  1. The encrypted blob is treated as a "NAL unit" for packetization
//  2. FU-A fragmentation splits the encrypted bytes across packets
//  3. FU indicator/header bytes are added by the packetizer (not encrypted)
//
// This depacketizer reassembles the encrypted blob, then decrypts it.
type EncryptedH264Depacketizer struct {
	e2eeCtx *E2EEContext

	// Fragment assembly buffer
	fuaBuffer []byte
	// Track the expected sequence for detecting gaps
	lastSeq     uint16
	hasLastSeq  bool
	assemblyTS  uint32 // Timestamp of current assembly
	hasAssembly bool
}

// NewEncryptedH264Depacketizer creates a new depacketizer for encrypted H264.
func NewEncryptedH264Depacketizer(e2eeCtx *E2EEContext) *EncryptedH264Depacketizer {
	return &EncryptedH264Depacketizer{
		e2eeCtx: e2eeCtx,
	}
}

// ProcessRTP processes an RTP packet containing encrypted H264 data.
// Returns:
//   - Decrypted NAL unit(s) when a complete unit is assembled and decrypted
//   - Empty slice when more fragments are needed
//   - Error if decryption fails
func (d *EncryptedH264Depacketizer) ProcessRTP(packet *rtp.Packet) ([]byte, error) {
	if len(packet.Payload) == 0 {
		return nil, nil
	}

	// Check for sequence gaps (packet loss)
	if d.hasLastSeq {
		expectedSeq := d.lastSeq + 1
		if packet.SequenceNumber != expectedSeq {
			// Gap detected - reset assembly state
			d.resetAssembly()
		}
	}
	d.lastSeq = packet.SequenceNumber
	d.hasLastSeq = true

	// Check for timestamp change (new frame)
	if d.hasAssembly && packet.Timestamp != d.assemblyTS {
		// New frame started - discard incomplete assembly
		d.resetAssembly()
	}

	naluType := packet.Payload[0] & naluTypeBitmask

	switch {
	case naluType >= nalTypeSingleNALUMin && naluType <= nalTypeSingleNALUMax:
		// Single NAL unit - decrypt directly
		return d.decryptNALU(packet.Payload)

	case naluType == nalTypeSTAPA:
		// STAP-A from SFU typically contains SPS/PPS which are NOT encrypted.
		// Pass through unchanged - the RTP layer will handle it.
		return packet.Payload, nil

	case naluType == nalTypeFUA:
		// FU-A: fragmented NAL unit
		return d.processFUA(packet)

	default:
		return nil, fmt.Errorf("unsupported NAL unit type: %d", naluType)
	}
}

// processFUA handles FU-A fragmented NAL units
func (d *EncryptedH264Depacketizer) processFUA(packet *rtp.Packet) ([]byte, error) {
	payload := packet.Payload
	if len(payload) < fuaHeaderSize {
		return nil, fmt.Errorf("FU-A packet too short: %d bytes", len(payload))
	}

	fuIndicator := payload[0]
	fuHeader := payload[1]
	isStart := (fuHeader & fuStartBitmask) != 0
	isEnd := (fuHeader & fuEndBitmask) != 0

	if isStart {
		// Start of new NAL unit - reset assembly
		d.resetAssembly()
		d.fuaBuffer = []byte{}
		d.assemblyTS = packet.Timestamp
		d.hasAssembly = true
	}

	if !d.hasAssembly {
		// Got middle/end fragment without start - discard
		return nil, nil
	}

	// Append fragment payload (after FU-A header)
	d.fuaBuffer = append(d.fuaBuffer, payload[fuaHeaderSize:]...)

	if isEnd {
		// Complete NAL unit - reconstruct header and decrypt
		// NAL header = (FU indicator & 0x60) | (FU header & 0x1F)
		naluHeader := (fuIndicator & naluRefIdcBitmask) | (fuHeader & naluTypeBitmask)

		// Build complete encrypted NAL unit
		encryptedNALU := make([]byte, 1+len(d.fuaBuffer))
		encryptedNALU[0] = naluHeader
		copy(encryptedNALU[1:], d.fuaBuffer)

		d.resetAssembly()

		return d.decryptNALU(encryptedNALU)
	}

	// More fragments expected
	return nil, nil
}

// processSTAPA handles STAP-A aggregated NAL units
// STAP-A packets from the SFU typically contain SPS/PPS which are NOT encrypted.
// We detect unencrypted NAL units by checking for valid NAL header and length.
func (d *EncryptedH264Depacketizer) processSTAPA(payload []byte) ([]byte, error) {
	if len(payload) < stapaHeaderSize+stapaNALULengthSize {
		return nil, fmt.Errorf("STAP-A packet too short")
	}

	var result []byte
	currOffset := stapaHeaderSize

	for currOffset < len(payload) {
		if currOffset+stapaNALULengthSize > len(payload) {
			break
		}

		naluSize := int(binary.BigEndian.Uint16(payload[currOffset:]))
		currOffset += stapaNALULengthSize

		if currOffset+naluSize > len(payload) {
			return nil, fmt.Errorf("STAP-A NAL unit size exceeds payload")
		}

		naluData := payload[currOffset : currOffset+naluSize]
		currOffset += naluSize

		if len(naluData) == 0 {
			continue
		}

		// Check NAL type - SPS (7) and PPS (8) are usually NOT encrypted
		// They come from the SFU for codec negotiation
		nalType := naluData[0] & naluTypeBitmask
		if nalType == 7 || nalType == 8 {
			// SPS/PPS - pass through without decryption
			result = append(result, 0x00, 0x00, 0x00, 0x01)
			result = append(result, naluData...)
			continue
		}

		// Try to decrypt other NAL types
		decrypted, err := d.decryptNALU(naluData)
		if err != nil {
			// If decryption fails, it might be unencrypted - pass through as-is
			result = append(result, 0x00, 0x00, 0x00, 0x01)
			result = append(result, naluData...)
			continue
		}

		// Append with Annex B start code
		result = append(result, 0x00, 0x00, 0x00, 0x01)
		result = append(result, decrypted...)
	}

	return result, nil
}

// decryptNALU decrypts an assembled encrypted NAL unit
func (d *EncryptedH264Depacketizer) decryptNALU(encryptedNALU []byte) ([]byte, error) {
	if d.e2eeCtx == nil || !d.e2eeCtx.Enabled() {
		// E2EE not enabled - return as-is
		return encryptedNALU, nil
	}

	decrypted, err := d.e2eeCtx.DecryptVideo(encryptedNALU)
	if err != nil {
		return nil, err
	}

	if decrypted == nil {
		// SIF frame - should be dropped
		return nil, nil
	}

	return decrypted, nil
}

// resetAssembly clears the fragment assembly state
func (d *EncryptedH264Depacketizer) resetAssembly() {
	d.fuaBuffer = nil
	d.hasAssembly = false
}

// Reset clears all state including sequence tracking
func (d *EncryptedH264Depacketizer) Reset() {
	d.resetAssembly()
	d.hasLastSeq = false
}
