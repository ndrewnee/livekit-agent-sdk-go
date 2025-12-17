package main

import (
	"crypto/cipher"
	"fmt"
	"log"
	"sort"
	"sync"

	"github.com/pion/rtp"
)

// fuaState tracks FU-A fragment reassembly state
type fuaState struct {
	buffer  []byte
	nalType byte
	nri     byte
	started bool
}

// FrameAssembler collects RTP packets and assembles them into complete Annex B frames.
// This is needed for E2EE video decryption because:
// 1. LiveKit E2EE encrypts complete Annex B frames (with start codes)
// 2. RTP packetization fragments these encrypted frames
// 3. We must reassemble the encrypted Annex B frame before decryption
type FrameAssembler struct {
	mu sync.Mutex

	// Packets grouped by timestamp (frame)
	pendingPackets map[uint32][]*rtp.Packet

	// FU-A fragment reassembly state
	fuaState *fuaState

	// E2EE decryption context
	cipherBlock cipher.Block
	sifTrailer  []byte

	// Statistics
	framesAssembled   int
	framesDecrypted   int
	decryptErrors     int
	decryptErrorLimit int

	logPrefix string
}

// NewFrameAssembler creates a new frame assembler for E2EE video decryption.
func NewFrameAssembler(cipherBlock cipher.Block, sifTrailer []byte, logPrefix string) *FrameAssembler {
	return &FrameAssembler{
		pendingPackets:    make(map[uint32][]*rtp.Packet),
		cipherBlock:       cipherBlock,
		sifTrailer:        sifTrailer,
		logPrefix:         logPrefix,
		decryptErrorLimit: 10,
	}
}

// DecryptedFrame represents a decrypted video frame in Annex B format.
type DecryptedFrame struct {
	Data       []byte // Decrypted Annex B data (with start codes)
	Timestamp  uint32 // RTP timestamp
	IsKeyframe bool   // True if this frame contains an IDR slice
}

// AddPacket adds an RTP packet to the assembler.
// Returns a decrypted frame when a complete frame is ready, nil otherwise.
func (f *FrameAssembler) AddPacket(pkt *rtp.Packet) (*DecryptedFrame, error) {
	if len(pkt.Payload) == 0 {
		return nil, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Add packet to the pending list for this timestamp
	f.pendingPackets[pkt.Timestamp] = append(f.pendingPackets[pkt.Timestamp], pkt)

	// Check if frame is complete (marker bit set)
	if !pkt.Marker {
		return nil, nil
	}

	// Frame is complete - assemble and decrypt
	packets := f.pendingPackets[pkt.Timestamp]
	delete(f.pendingPackets, pkt.Timestamp)

	// Sort by sequence number to handle out-of-order packets
	sort.Slice(packets, func(i, j int) bool {
		return packets[i].SequenceNumber < packets[j].SequenceNumber
	})

	// Assemble RTP packets into Annex B format (with start codes)
	// The NAL type bytes are clear, allowing proper RTP depayloading
	annexBFrame, err := f.assembleAnnexB(packets)
	if err != nil {
		return nil, fmt.Errorf("failed to assemble Annex B frame: %w", err)
	}

	f.framesAssembled++

	// Decrypt the frame
	decrypted, isKeyframe, err := f.decryptVideoFrame(annexBFrame)
	if err != nil {
		f.decryptErrors++
		if f.decryptErrors <= f.decryptErrorLimit {
			log.Printf("[%s] decrypt error (frame %d): %v", f.logPrefix, f.framesAssembled, err)
		}
		return nil, err
	}

	f.framesDecrypted++
	if f.framesDecrypted <= 3 {
		log.Printf("[%s] decrypted frame %d: %d -> %d bytes, keyframe=%v",
			f.logPrefix, f.framesDecrypted, len(annexBFrame), len(decrypted), isKeyframe)
	}

	return &DecryptedFrame{
		Data:       decrypted,
		Timestamp:  pkt.Timestamp,
		IsKeyframe: isKeyframe,
	}, nil
}

// assembleAnnexB converts RTP packets to Annex B format with start codes.
//
// RTP H264 packetization (RFC 6184) uses these formats:
// - Single NAL (type 1-23): Direct NAL unit in payload
// - STAP-A (type 24): Aggregated NAL units with size prefixes
// - FU-A (type 28): Fragmented NAL unit across multiple packets
//
// For E2EE video:
// - The NAL type byte and RTP headers are CLEAR (not encrypted)
// - The NAL RBSP content (after type byte) is ENCRYPTED
// - We parse NAL structure, add start codes, then decrypt the assembled frame
func (f *FrameAssembler) assembleAnnexB(packets []*rtp.Packet) ([]byte, error) {
	var result []byte
	startCode := []byte{0x00, 0x00, 0x00, 0x01}

	// Reset FU-A state at start of new frame
	f.fuaState = nil

	for _, pkt := range packets {
		if len(pkt.Payload) == 0 {
			continue
		}

		nalType := pkt.Payload[0] & 0x1F

		switch {
		case nalType >= 1 && nalType <= 23:
			// Single NAL unit - add start code and append entire payload
			result = append(result, startCode...)
			result = append(result, pkt.Payload...)

		case nalType == nalTypeSTAPA:
			// STAP-A aggregation - extract each NAL unit with size prefix
			nals, err := f.parseSTAPA(pkt.Payload)
			if err != nil {
				return nil, fmt.Errorf("failed to parse STAP-A: %w", err)
			}
			for _, nal := range nals {
				result = append(result, startCode...)
				result = append(result, nal...)
			}

		case nalType == nalTypeFUA:
			// FU-A fragmentation - reassemble fragments
			nal, complete, err := f.processFUA(pkt.Payload)
			if err != nil {
				return nil, fmt.Errorf("failed to process FU-A: %w", err)
			}
			if complete && len(nal) > 0 {
				result = append(result, startCode...)
				result = append(result, nal...)
			}

		default:
			// Unknown NAL type - skip or treat as single NAL
			if len(pkt.Payload) > 1 {
				result = append(result, startCode...)
				result = append(result, pkt.Payload...)
			}
		}
	}

	return result, nil
}

// parseSTAPA extracts individual NAL units from a STAP-A packet.
// STAP-A format: [STAP-A header (1 byte)] + [NAL1 size (2 bytes)] + [NAL1 data] + ...
func (f *FrameAssembler) parseSTAPA(payload []byte) ([][]byte, error) {
	var nals [][]byte
	offset := 1 // Skip STAP-A header byte

	for offset+2 <= len(payload) {
		nalSize := int(payload[offset])<<8 | int(payload[offset+1])
		offset += 2

		if nalSize == 0 {
			continue // Skip empty NALs
		}

		if offset+nalSize > len(payload) {
			return nil, fmt.Errorf("STAP-A NAL size %d exceeds remaining payload %d", nalSize, len(payload)-offset)
		}

		nal := make([]byte, nalSize)
		copy(nal, payload[offset:offset+nalSize])
		nals = append(nals, nal)
		offset += nalSize
	}

	return nals, nil
}

// processFUA handles FU-A fragmented NAL units.
// FU-A format: [FU indicator (1 byte)] + [FU header (1 byte)] + [fragment data]
// Returns the complete NAL when the last fragment (E bit) is received.
func (f *FrameAssembler) processFUA(payload []byte) ([]byte, bool, error) {
	if len(payload) < 2 {
		return nil, false, fmt.Errorf("FU-A payload too short: %d bytes", len(payload))
	}

	fuIndicator := payload[0]
	fuHeader := payload[1]

	isStart := (fuHeader & 0x80) != 0 // S bit
	isEnd := (fuHeader & 0x40) != 0   // E bit
	nalType := fuHeader & 0x1F
	nri := fuIndicator & 0x60

	if isStart {
		// Start of new fragmented NAL - initialize state
		f.fuaState = &fuaState{
			buffer:  append([]byte(nil), payload[2:]...), // Copy fragment data
			nalType: nalType,
			nri:     nri,
			started: true,
		}
		return nil, false, nil
	}

	// Middle or end fragment
	if f.fuaState == nil || !f.fuaState.started {
		// Fragment without start - skip (packet loss)
		return nil, false, nil
	}

	// Append fragment data
	f.fuaState.buffer = append(f.fuaState.buffer, payload[2:]...)

	if isEnd {
		// Last fragment - reconstruct complete NAL
		// NAL header = NRI (from FU indicator) | NAL type (from FU header)
		nalHeader := f.fuaState.nri | f.fuaState.nalType
		nal := make([]byte, 1+len(f.fuaState.buffer))
		nal[0] = nalHeader
		copy(nal[1:], f.fuaState.buffer)

		f.fuaState = nil // Reset state
		return nal, true, nil
	}

	return nil, false, nil
}

// decryptVideoFrame decrypts a complete encrypted video frame.
// Returns the decrypted Annex B data and whether it's a keyframe.
//
// LiveKit E2EE frame format:
// [unencrypted header (N bytes AAD)] + [ciphertext + 16-byte GCM tag] + [IV] + [ivLen (1)] + [keyID (1)]
//
// The unencrypted header contains the Annex B start codes and NAL headers,
// which allows the receiver to calculate AAD size by finding start codes.
func (f *FrameAssembler) decryptVideoFrame(encryptedFrame []byte) ([]byte, bool, error) {
	// Check for SIF frame (Server Injected Frame - signaling frame that should be dropped)
	if f.sifTrailer != nil && len(encryptedFrame) >= len(f.sifTrailer) {
		possibleTrailer := encryptedFrame[len(encryptedFrame)-len(f.sifTrailer):]
		match := true
		for i := range f.sifTrailer {
			if possibleTrailer[i] != f.sifTrailer[i] {
				match = false
				break
			}
		}
		if match {
			return nil, false, nil // SIF frame - drop silently
		}
	}

	// Strategy: Find Annex B start codes in the unencrypted header to calculate AAD size.
	// The start codes (00 00 00 01 or 00 00 01) are left unencrypted in LiveKit E2EE.
	//
	// For H264, unencrypted portion typically includes:
	// - Start code: 3-4 bytes
	// - NAL header: 1 byte
	// - First slice header bytes: 1-10 bytes (varies by NAL type)

	// First, try to find NAL units in the beginning of the frame (unencrypted portion)
	// and calculate AAD size from the Annex B structure
	nalUnits := f.findNALUnits(encryptedFrame)

	// Debug: log frame info for first few frames
	if f.framesAssembled <= 3 {
		log.Printf("[%s] frame %d: len=%d, first16bytes=%x",
			f.logPrefix, f.framesAssembled+1, len(encryptedFrame),
			func() []byte {
				if len(encryptedFrame) >= 16 {
					return encryptedFrame[:16]
				}
				return encryptedFrame
			}())
		log.Printf("[%s] frame %d: found %d NAL units in unencrypted portion",
			f.logPrefix, f.framesAssembled+1, len(nalUnits))
	}

	var unencryptedBytes int
	if len(nalUnits) > 0 {
		// Find the first slice NAL to determine unencrypted header size
		firstSliceIdx := -1
		for i, nal := range nalUnits {
			if len(nal.data) > 0 {
				nalType := nal.data[0] & 0x1F
				// Slice types: 1 (non-IDR), 5 (IDR)
				if nalType >= 1 && nalType <= 5 {
					firstSliceIdx = i
					break
				}
			}
		}

		if firstSliceIdx == -1 {
			// No slice found - use first NAL
			firstSliceIdx = 0
		}

		// Unencrypted bytes = everything up to first slice NAL + 2 bytes of slice header
		// This matches how LiveKit JS SDK calculates unencrypted bytes
		unencryptedBytes = nalUnits[firstSliceIdx].startOffset + 2
	} else {
		// No start codes found - try common fixed sizes
		unencryptedBytes = 10 // Default for H264
	}

	// Try decryption with calculated AAD size first
	decrypted, err := f.decryptWithAAD(encryptedFrame, unencryptedBytes)
	if err == nil {
		isKeyframe := f.detectKeyframeFromAnnexB(decrypted)
		return decrypted, isKeyframe, nil
	}

	// If calculated size failed, try other common AAD sizes
	aadSizes := []int{10, 0, 1, 2, 4, 6, 8, 12, 14, 16}
	var lastErr error = err

	for _, aadSize := range aadSizes {
		if aadSize == unencryptedBytes {
			continue // Already tried this
		}
		if len(encryptedFrame) < aadSize+30 { // Need header + tag(16) + IV(12) + trailer(2)
			continue
		}

		decrypted, lastErr = f.decryptWithAAD(encryptedFrame, aadSize)
		if lastErr == nil {
			isKeyframe := f.detectKeyframeFromAnnexB(decrypted)
			return decrypted, isKeyframe, nil
		}
	}

	// All AAD sizes failed
	return nil, false, fmt.Errorf("decryption failed (tried AAD=%d and fallbacks): %w", unencryptedBytes, lastErr)
}

// detectKeyframeFromAnnexB checks if the decrypted Annex B data contains an IDR slice.
func (f *FrameAssembler) detectKeyframeFromAnnexB(annexB []byte) bool {
	nalUnits := f.findNALUnits(annexB)
	for _, nal := range nalUnits {
		if len(nal.data) > 0 {
			nalType := nal.data[0] & 0x1F
			if nalType == 5 { // IDR slice
				return true
			}
		}
	}
	return false
}

type nalUnit struct {
	data        []byte
	startOffset int // Offset in original buffer where NAL starts (after start code)
}

// findNALUnits finds NAL unit boundaries in Annex B formatted data.
func (f *FrameAssembler) findNALUnits(data []byte) []nalUnit {
	var units []nalUnit
	i := 0

	for i < len(data) {
		// Look for start code (00 00 00 01 or 00 00 01)
		startCodeLen := 0
		if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			startCodeLen = 4
		} else if i+3 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			startCodeLen = 3
		}

		if startCodeLen > 0 {
			nalStart := i + startCodeLen

			// Find end of this NAL (next start code or end of data)
			nalEnd := len(data)
			for j := nalStart; j < len(data)-3; j++ {
				if data[j] == 0 && data[j+1] == 0 {
					if (j+2 < len(data) && data[j+2] == 1) ||
						(j+3 < len(data) && data[j+2] == 0 && data[j+3] == 1) {
						nalEnd = j
						break
					}
				}
			}

			if nalStart < nalEnd {
				units = append(units, nalUnit{
					data:        data[nalStart:nalEnd],
					startOffset: nalStart,
				})
			}

			i = nalEnd
		} else {
			i++
		}
	}

	return units
}

// decryptWithAAD decrypts the frame using the specified number of unencrypted bytes as AAD.
func (f *FrameAssembler) decryptWithAAD(encrypted []byte, unencryptedBytes int) ([]byte, error) {
	if len(encrypted) < unencryptedBytes+30 { // Need header + tag + IV + trailer
		return nil, fmt.Errorf("encrypted data too short: %d bytes", len(encrypted))
	}

	// LiveKit E2EE format:
	// [unencrypted header (N bytes AAD)] + [ciphertext + 16-byte tag] + [IV] + [ivLen (1)] + [keyID (1)]

	frameHeader := encrypted[:unencryptedBytes]
	frameTrailer := encrypted[len(encrypted)-2:]
	ivLength := int(frameTrailer[0])

	if ivLength <= 0 || ivLength > 16 {
		return nil, fmt.Errorf("invalid IV length: %d", ivLength)
	}

	ivStart := len(encrypted) - 2 - ivLength
	if ivStart < unencryptedBytes {
		return nil, fmt.Errorf("IV start position invalid: %d", ivStart)
	}

	iv := make([]byte, ivLength)
	copy(iv, encrypted[ivStart:ivStart+ivLength])

	cipherTextStart := unencryptedBytes
	cipherTextLength := len(encrypted) - 2 - ivLength - unencryptedBytes
	if cipherTextLength <= 0 {
		return nil, fmt.Errorf("ciphertext length invalid: %d", cipherTextLength)
	}

	cipherText := make([]byte, cipherTextLength)
	copy(cipherText, encrypted[cipherTextStart:cipherTextStart+cipherTextLength])

	aesGCM, err := cipher.NewGCMWithNonceSize(f.cipherBlock, ivLength)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	plainText, err := aesGCM.Open(nil, iv, cipherText, frameHeader)
	if err != nil {
		return nil, err
	}

	// Reconstruct: [unencrypted header] + [decrypted payload]
	result := make([]byte, unencryptedBytes+len(plainText))
	copy(result[:unencryptedBytes], frameHeader)
	copy(result[unencryptedBytes:], plainText)

	return result, nil
}

// Stats returns current statistics.
func (f *FrameAssembler) Stats() (assembled, decrypted, errors int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.framesAssembled, f.framesDecrypted, f.decryptErrors
}

// Flush clears any pending packets (e.g., on stream discontinuity).
func (f *FrameAssembler) Flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingPackets = make(map[uint32][]*rtp.Packet)
}
