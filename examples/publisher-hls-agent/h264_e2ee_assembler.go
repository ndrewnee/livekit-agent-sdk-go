package main

import (
	"crypto/cipher"
	"fmt"
	"log"
	"sync"

	"github.com/pion/rtp"
)

// H264E2EEAssembler handles frame-level E2EE decryption for H264 video.
// LiveKit E2EE encrypts complete NAL units before RTP packetization.
// For large NAL units fragmented into FU-A packets, we must:
// 1. Reassemble all fragments into the complete encrypted frame
// 2. Decrypt the complete frame
// 3. Output decrypted NAL unit(s)
//
// IMPORTANT: For keyframes (IDR, NAL type 5), the browser's EncodedVideoFrame
// includes SPS/PPS NALUs before the IDR slice. The encryption AAD includes
// all of these. We must cache SPS/PPS from STAP-A packets and prepend them
// when decrypting IDR slices.
type H264E2EEAssembler struct {
	mu sync.Mutex

	cipherBlock cipher.Block
	sifTrailer  []byte

	// Cached parameter sets for IDR decryption
	// Chrome's EncodedVideoFrame for keyframes includes: [SPS][PPS][IDR]
	// The encryption AAD covers all of them, so we need to reconstruct
	cachedSPS []byte // Most recent SPS NAL (without start code)
	cachedPPS []byte // Most recent PPS NAL (without start code)

	// Current fragment accumulation
	fragmentBuffer   []byte
	fragmentNRI      byte   // NRI bits from FU indicator
	fragmentType     byte   // Original NAL type from FU header
	fragmentSeqStart uint16 // Starting sequence number
	inFragment       bool

	// Stats
	framesAssembled int
	framesDecrypted int
	decryptErrors   int

	logPrefix string
}

// NewH264E2EEAssembler creates a new assembler for H264 E2EE decryption.
func NewH264E2EEAssembler(cipherBlock cipher.Block, sifTrailer []byte, logPrefix string) *H264E2EEAssembler {
	return &H264E2EEAssembler{
		cipherBlock: cipherBlock,
		sifTrailer:  sifTrailer,
		logPrefix:   logPrefix,
	}
}

// H264NAL represents a decrypted H264 NAL unit
type H264NAL struct {
	Data      []byte // Complete NAL unit including header
	Timestamp uint32
	SeqNum    uint16
}

// ProcessPacket processes an RTP packet and returns decrypted NAL unit(s) if available.
// For FU-A fragments, it accumulates until complete then decrypts.
// For single NAL units, it decrypts directly.
// Returns nil if more fragments needed or on error.
func (a *H264E2EEAssembler) ProcessPacket(pkt *rtp.Packet) ([]*H264NAL, error) {
	if len(pkt.Payload) == 0 {
		return nil, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	nalType := pkt.Payload[0] & 0x1F

	switch nalType {
	case 24: // STAP-A - SPS/PPS from SFU, pass through unencrypted
		return a.handleSTAPA(pkt)

	case 28: // FU-A - fragmented NAL unit
		return a.handleFUA(pkt)

	default: // Single NAL unit (types 1-23)
		return a.handleSingleNAL(pkt)
	}
}

// handleSTAPA passes through STAP-A packets (SPS/PPS) without decryption
// Also caches SPS/PPS for IDR decryption - Chrome's EncodedVideoFrame includes
// [SPS][PPS][IDR] for keyframes, and the encryption AAD covers all of them.
func (a *H264E2EEAssembler) handleSTAPA(pkt *rtp.Packet) ([]*H264NAL, error) {
	// STAP-A from SFU contains unencrypted SPS/PPS
	// Extract individual NAL units from STAP-A
	var nals []*H264NAL
	payload := pkt.Payload[1:] // Skip STAP-A header

	for len(payload) > 2 {
		nalSize := int(payload[0])<<8 | int(payload[1])
		payload = payload[2:]

		if nalSize > len(payload) {
			break
		}

		nalData := payload[:nalSize]
		nalType := nalData[0] & 0x1F

		// Cache SPS (type 7) and PPS (type 8) for IDR decryption
		switch nalType {
		case 7: // SPS
			a.cachedSPS = make([]byte, len(nalData))
			copy(a.cachedSPS, nalData)
			log.Printf("[%s] cached SPS: %d bytes", a.logPrefix, len(nalData))
		case 8: // PPS
			a.cachedPPS = make([]byte, len(nalData))
			copy(a.cachedPPS, nalData)
			log.Printf("[%s] cached PPS: %d bytes", a.logPrefix, len(nalData))
		}

		nals = append(nals, &H264NAL{
			Data:      nalData,
			Timestamp: pkt.Timestamp,
			SeqNum:    pkt.SequenceNumber,
		})
		payload = payload[nalSize:]
	}

	return nals, nil
}

// handleFUA handles FU-A fragmented NAL units
func (a *H264E2EEAssembler) handleFUA(pkt *rtp.Packet) ([]*H264NAL, error) {
	if len(pkt.Payload) < 2 {
		return nil, fmt.Errorf("FU-A packet too short")
	}

	fuIndicator := pkt.Payload[0]
	fuHeader := pkt.Payload[1]

	isStart := (fuHeader & 0x80) != 0
	isEnd := (fuHeader & 0x40) != 0
	nalType := fuHeader & 0x1F
	nri := fuIndicator & 0x60

	if isStart {
		// Start of new fragment sequence
		a.fragmentBuffer = nil
		a.fragmentNRI = nri
		a.fragmentType = nalType
		a.fragmentSeqStart = pkt.SequenceNumber
		a.inFragment = true

		// Append payload after FU indicator and FU header
		a.fragmentBuffer = append(a.fragmentBuffer, pkt.Payload[2:]...)
		return nil, nil
	}

	if !a.inFragment {
		// Middle/end fragment without start - skip
		return nil, nil
	}

	// Append fragment payload (skip FU indicator byte only, FU header not present in middle/end)
	// Actually FU header IS present in all FU-A packets
	a.fragmentBuffer = append(a.fragmentBuffer, pkt.Payload[2:]...)

	if isEnd {
		// Complete fragment - now decrypt
		a.inFragment = false
		a.framesAssembled++

		// The assembled buffer contains the encrypted NAL payload WITHOUT the NAL header
		// because FU-A packetization extracts the NAL header into NRI/type bits.
		//
		// LiveKit E2EE format: [NAL header (unencrypted, 1 byte AAD)] + [encrypted payload] + [GCM tag] + [IV] + [ivLen] + [keyID]
		//
		// FU-A packetization of encrypted frame:
		// - Takes encrypted blob: [NAL header] + [encrypted payload + trailer]
		// - Extracts NAL header to create FU indicator (NRI) and FU header (type)
		// - Fragments the REST: [encrypted payload + trailer]
		//
		// So fragmentBuffer contains: [encrypted payload] + [tag] + [IV] + [ivLen] + [keyID]
		// We need to PREPEND the reconstructed NAL header to get the complete encrypted blob

		// Reconstruct the original NAL header byte = NRI | Type
		originalNALHeader := a.fragmentNRI | a.fragmentType

		// Prepend NAL header to create complete encrypted blob for decryption
		completeEncrypted := make([]byte, 1+len(a.fragmentBuffer))
		completeEncrypted[0] = originalNALHeader
		copy(completeEncrypted[1:], a.fragmentBuffer)

		// Try to decrypt the assembled frame
		decrypted, err := a.decryptFrame(completeEncrypted, originalNALHeader)
		if err != nil {
			a.decryptErrors++
			if a.decryptErrors <= 5 {
				log.Printf("[%s] FU-A decrypt error (assembled %d bytes): %v", a.logPrefix, len(a.fragmentBuffer), err)
			}
			return nil, err
		}

		a.framesDecrypted++
		if a.framesDecrypted <= 3 {
			log.Printf("[%s] FU-A decrypted: assembled=%d -> decrypted=%d bytes (NAL type=%d)",
				a.logPrefix, len(a.fragmentBuffer), len(decrypted), a.fragmentType)
		}

		return []*H264NAL{{
			Data:      decrypted,
			Timestamp: pkt.Timestamp,
			SeqNum:    a.fragmentSeqStart,
		}}, nil
	}

	return nil, nil
}

// handleSingleNAL handles single NAL unit packets (not fragmented)
func (a *H264E2EEAssembler) handleSingleNAL(pkt *rtp.Packet) ([]*H264NAL, error) {
	// Single NAL - the entire encrypted frame is in one packet
	// Format: [NAL header (unencrypted)] + [encrypted payload] + [GCM tag] + [IV] + [ivLen] + [keyID]

	// Minimum size check
	const minEncryptedSize = 32 // 1 header + 1 data + 16 tag + 12 IV + 1 ivLen + 1 keyID
	if len(pkt.Payload) < minEncryptedSize {
		// Too small to be encrypted, pass through
		return []*H264NAL{{
			Data:      pkt.Payload,
			Timestamp: pkt.Timestamp,
			SeqNum:    pkt.SequenceNumber,
		}}, nil
	}

	nalHeader := pkt.Payload[0]
	decrypted, err := a.decryptFrame(pkt.Payload, nalHeader)
	if err != nil {
		a.decryptErrors++
		if a.decryptErrors <= 5 {
			log.Printf("[%s] single NAL decrypt error (len=%d, type=%d): %v",
				a.logPrefix, len(pkt.Payload), nalHeader&0x1F, err)
		}
		return nil, err
	}

	a.framesDecrypted++
	if a.framesDecrypted <= 3 {
		log.Printf("[%s] single NAL decrypted: encrypted=%d -> decrypted=%d bytes",
			a.logPrefix, len(pkt.Payload), len(decrypted))
	}

	return []*H264NAL{{
		Data:      decrypted,
		Timestamp: pkt.Timestamp,
		SeqNum:    pkt.SequenceNumber,
	}}, nil
}

// decryptFrame decrypts a complete encrypted H264 NAL unit
// H264 video uses 2 unencrypted bytes as AAD (unlike audio which uses 1)
// The LiveKit JS SDK applies RBSP escaping to H264 encrypted data, so we need
// to reverse that escaping before decryption.
//
// IMPORTANT: WebRTC EncodedVideoFrame uses Annex B format with start codes (00 00 00 01 or 00 00 01).
// The encryption AAD includes these start codes, but RTP packetization strips them.
// We must try decryption with start codes prepended to reconstruct the original AAD.
//
// For IDR keyframes (NAL type 5), Chrome's EncodedVideoFrame includes:
// [start_code][SPS][start_code][PPS][start_code][IDR slice...]
// The encryption AAD covers all of this, so we need to prepend cached SPS/PPS.
func (a *H264E2EEAssembler) decryptFrame(encrypted []byte, expectedNALHeader byte) ([]byte, error) {
	// Check for SIF frame
	if a.sifTrailer != nil && len(encrypted) >= len(a.sifTrailer) {
		possibleTrailer := encrypted[len(encrypted)-len(a.sifTrailer):]
		match := true
		for i := range a.sifTrailer {
			if possibleTrailer[i] != a.sifTrailer[i] {
				match = false
				break
			}
		}
		if match {
			// SIF frame - drop
			return nil, nil
		}
	}

	var lastErr error
	nalType := expectedNALHeader & 0x1F

	// Strategy 0: For IDR frames (type 5), try with SPS/PPS prepended
	// Chrome's EncodedVideoFrame for keyframes includes: [SPS][PPS][IDR]
	// The encryption AAD covers all of this data.
	// Format: [00 00 00 01][SPS][00 00 00 01][PPS][00 00 00 01][IDR...]
	if nalType == 5 && a.cachedSPS != nil && a.cachedPPS != nil {
		decrypted, err := a.tryDecryptIDRWithSPSPPS(encrypted)
		if err == nil {
			return decrypted, nil
		}
		lastErr = err
		if a.decryptErrors < 3 {
			log.Printf("[%s] IDR decryption with SPS/PPS failed: %v, trying other strategies", a.logPrefix, err)
		}
	}

	// Try decryption strategies in order of likelihood:
	// 1. With 4-byte start code prepended (most common for Annex B H264)
	// 2. With 3-byte start code prepended (alternative Annex B format)
	// 3. Without start codes (in case data already has them or different format)

	// Strategy 1: Try with 4-byte start code (00 00 00 01) prepended
	// This is the most common format for WebRTC H264 Annex B
	startCode4 := []byte{0x00, 0x00, 0x00, 0x01}
	withStartCode4 := make([]byte, len(startCode4)+len(encrypted))
	copy(withStartCode4, startCode4)
	copy(withStartCode4[len(startCode4):], encrypted)

	// With 4-byte start code, NAL header is at index 4, so unencrypted = 4 + 2 = 6
	decrypted, err := a.tryDecryptWithStartCode(withStartCode4, 6, "4-byte start code")
	if err == nil {
		return decrypted, nil
	}
	lastErr = err

	// Strategy 2: Try with 3-byte start code (00 00 01) prepended
	startCode3 := []byte{0x00, 0x00, 0x01}
	withStartCode3 := make([]byte, len(startCode3)+len(encrypted))
	copy(withStartCode3, startCode3)
	copy(withStartCode3[len(startCode3):], encrypted)

	// With 3-byte start code, NAL header is at index 3, so unencrypted = 3 + 2 = 5
	decrypted, err = a.tryDecryptWithStartCode(withStartCode3, 5, "3-byte start code")
	if err == nil {
		return decrypted, nil
	}
	lastErr = err

	// Strategy 3: Try without start codes (various unencrypted byte counts)
	// This handles cases where the data format is different
	unencryptedBytesCounts := []int{2, 1, 3, 4, 5, 6, 0}
	for _, unencryptedBytes := range unencryptedBytesCounts {
		decrypted, err := a.tryDecrypt(encrypted, unencryptedBytes)
		if err == nil {
			if a.framesDecrypted <= 3 {
				log.Printf("[%s] video decryption succeeded with %d unencrypted bytes (no start code)", a.logPrefix, unencryptedBytes)
			}
			return decrypted, nil
		}
		lastErr = err
	}

	// Strategy 4: Try with RBSP unescaping (in case escape bytes are present)
	unescaped := removeRBSPEscaping(encrypted)
	if len(unescaped) != len(encrypted) {
		if a.decryptErrors < 3 {
			log.Printf("[%s] RBSP unescape: %d -> %d bytes (removed %d escape bytes)",
				a.logPrefix, len(encrypted), len(unescaped), len(encrypted)-len(unescaped))
		}

		// Try unescaped data with start codes
		withStartCode4Unesc := make([]byte, len(startCode4)+len(unescaped))
		copy(withStartCode4Unesc, startCode4)
		copy(withStartCode4Unesc[len(startCode4):], unescaped)
		decrypted, err = a.tryDecryptWithStartCode(withStartCode4Unesc, 6, "4-byte start code + RBSP")
		if err == nil {
			return decrypted, nil
		}

		// Try unescaped without start codes
		for _, unencryptedBytes := range unencryptedBytesCounts {
			decrypted, err := a.tryDecrypt(unescaped, unencryptedBytes)
			if err == nil {
				if a.framesDecrypted <= 3 {
					log.Printf("[%s] video decryption succeeded with %d unencrypted bytes (after RBSP unescape)", a.logPrefix, unencryptedBytes)
				}
				return decrypted, nil
			}
			lastErr = err
		}
	}

	// Debug: log the encrypted frame details for analysis
	if a.decryptErrors < 3 {
		log.Printf("[%s] video decrypt failed - encrypted len=%d, first 20 bytes: %x",
			a.logPrefix, len(encrypted), safeSlice(encrypted, 0, 20))
		log.Printf("[%s] video decrypt failed - last 20 bytes: %x",
			a.logPrefix, safeSlice(encrypted, len(encrypted)-20, len(encrypted)))
		if len(encrypted) >= 2 {
			ivLen := int(encrypted[len(encrypted)-2])
			keyID := int(encrypted[len(encrypted)-1])
			log.Printf("[%s] video decrypt failed - trailer: ivLen=%d, keyID=%d", a.logPrefix, ivLen, keyID)
		}
	}

	return nil, lastErr
}

// tryDecryptIDRWithSPSPPS attempts decryption of an IDR frame by prepending cached SPS/PPS.
// Chrome's EncodedVideoFrame for keyframes includes: [SPS][PPS][IDR slice]
// The encryption uses all of this as AAD, so we must reconstruct the full format:
// [00 00 00 01][SPS][00 00 00 01][PPS][00 00 00 01][IDR...]
//
// Returns only the decrypted IDR slice (SPS/PPS are output separately from STAP-A).
func (a *H264E2EEAssembler) tryDecryptIDRWithSPSPPS(encryptedIDR []byte) ([]byte, error) {
	startCode := []byte{0x00, 0x00, 0x00, 0x01}

	// Build complete keyframe: [start_code][SPS][start_code][PPS][start_code][IDR encrypted data]
	// Total prefix length = 4 + len(SPS) + 4 + len(PPS) + 4 = len(SPS) + len(PPS) + 12
	prefixLen := len(startCode)*3 + len(a.cachedSPS) + len(a.cachedPPS)
	fullData := make([]byte, prefixLen+len(encryptedIDR))

	offset := 0
	// Start code + SPS
	copy(fullData[offset:], startCode)
	offset += len(startCode)
	copy(fullData[offset:], a.cachedSPS)
	offset += len(a.cachedSPS)
	// Start code + PPS
	copy(fullData[offset:], startCode)
	offset += len(startCode)
	copy(fullData[offset:], a.cachedPPS)
	offset += len(a.cachedPPS)
	// Start code + IDR encrypted data
	copy(fullData[offset:], startCode)
	offset += len(startCode)
	copy(fullData[offset:], encryptedIDR)

	// Unencrypted bytes = all of [start_code][SPS][start_code][PPS][start_code] + 2 bytes of IDR header
	// According to naluUtils.ts: unencrypted = position of first slice NAL + 2
	// Position of IDR = prefixLen, so unencrypted = prefixLen + 2
	unencryptedBytes := prefixLen + 2

	if a.decryptErrors < 5 {
		log.Printf("[%s] trying IDR with SPS/PPS: SPS=%d, PPS=%d, IDR=%d, total=%d, unencrypted=%d",
			a.logPrefix, len(a.cachedSPS), len(a.cachedPPS), len(encryptedIDR), len(fullData), unencryptedBytes)
	}

	decrypted, err := a.tryDecrypt(fullData, unencryptedBytes)
	if err != nil {
		return nil, err
	}

	if a.framesDecrypted <= 3 {
		log.Printf("[%s] IDR decryption with SPS/PPS succeeded: encrypted=%d -> decrypted=%d",
			a.logPrefix, len(encryptedIDR), len(decrypted))
	}

	// Return only the IDR portion (strip SPS/PPS prefix)
	// The decrypted data is: [SPS with start code][PPS with start code][IDR with start code]
	// We only want the IDR NAL (without start code since we stripped it in P-frame decryption too)
	// IDR starts at offset prefixLen in the decrypted result
	if len(decrypted) > prefixLen {
		// Skip the prefix (SPS/PPS/start codes) to get just the IDR NAL
		return decrypted[prefixLen:], nil
	}

	return decrypted, nil
}

// tryDecryptWithStartCode attempts decryption with start code prepended.
// The decrypted result will NOT include the start code (we strip it after decryption).
func (a *H264E2EEAssembler) tryDecryptWithStartCode(dataWithStartCode []byte, unencryptedBytes int, desc string) ([]byte, error) {
	decrypted, err := a.tryDecrypt(dataWithStartCode, unencryptedBytes)
	if err != nil {
		return nil, err
	}

	if a.framesDecrypted <= 3 {
		log.Printf("[%s] video decryption succeeded with %s, %d unencrypted bytes", a.logPrefix, desc, unencryptedBytes)
	}

	// Strip the start code from the decrypted result
	// We only need the NAL data (header + payload), not the start code
	startCodeLen := unencryptedBytes - 2 // unencrypted = startCode + NAL header + 1 byte
	if startCodeLen > 0 && startCodeLen < len(decrypted) {
		return decrypted[startCodeLen:], nil
	}
	return decrypted, nil
}

// tryDecrypt attempts to decrypt with a specific number of unencrypted bytes
func (a *H264E2EEAssembler) tryDecrypt(encrypted []byte, unencryptedBytes int) ([]byte, error) {
	if len(encrypted) < unencryptedBytes+30 { // Need at least header + tag + IV + trailer
		return nil, fmt.Errorf("encrypted data too short: %d bytes", len(encrypted))
	}

	// LiveKit E2EE format:
	// [unencrypted header (N bytes AAD)] + [ciphertext + 16-byte tag] + [IV (12)] + [ivLen (1)] + [keyID (1)]

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

	aesGCM, err := cipher.NewGCMWithNonceSize(a.cipherBlock, ivLength)
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

// safeSlice returns a safe slice of data, handling bounds
func safeSlice(data []byte, start, end int) []byte {
	if start < 0 {
		start = 0
	}
	if end > len(data) {
		end = len(data)
	}
	if start >= end {
		return nil
	}
	return data[start:end]
}

// removeRBSPEscaping removes H264 emulation prevention bytes from the data.
// In H264/H265 bitstreams, 0x00 0x00 0x03 sequences are used to prevent
// accidental start code patterns. This function reverses that escaping.
// Pattern: 00 00 03 XX -> 00 00 XX (where XX is 00, 01, 02, or 03)
func removeRBSPEscaping(data []byte) []byte {
	if len(data) < 3 {
		return data
	}

	result := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		// Check for emulation prevention pattern: 00 00 03
		if i+2 < len(data) && data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x03 {
			// Check that the byte after 03 is valid (00, 01, 02, or 03)
			if i+3 < len(data) && data[i+3] <= 0x03 {
				// Copy the two 00 bytes, skip the 03
				result = append(result, 0x00, 0x00)
				i += 3 // Skip past 00 00 03
				continue
			}
		}
		result = append(result, data[i])
		i++
	}

	return result
}

// Stats returns current statistics
func (a *H264E2EEAssembler) Stats() (assembled, decrypted, errors int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.framesAssembled, a.framesDecrypted, a.decryptErrors
}
