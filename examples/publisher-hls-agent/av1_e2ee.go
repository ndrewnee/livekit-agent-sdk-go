package main

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

const (
	av1GCMTagLengthBytes = 16

	av1E2EEMetadataOBUType = 5

	av1E2EEMetadataMagic0  = 0x4c // 'L'
	av1E2EEMetadataMagic1  = 0x4b // 'K'
	av1E2EEMetadataVersion = 1

	av1OBUTypeFrameHeader = 3
	av1OBUTypeFrame       = 6
)

type av1ByteRange struct {
	start int
	end   int
}

type av1EncryptionLayout struct {
	protectedRanges []av1ByteRange
	protectedLength int
}

type av1E2EEMetadata struct {
	keyIndex byte
	iv       []byte
	tag      []byte
}

func isAV1OBUStreamKeyframe(data []byte) bool {
	offset := 0
	for offset < len(data) {
		obuType, ext, hasSizeField, ok := parseAV1OBUHeader(data[offset])
		if !ok {
			return false
		}
		headerLen := 1
		if ext {
			headerLen++
		}
		if offset+headerLen > len(data) {
			return false
		}

		payloadLen := len(data) - (offset + headerLen)
		sizeFieldLen := 0
		if hasSizeField {
			val, n, ok := readLeb128(data, offset+headerLen)
			if !ok {
				return false
			}
			payloadLen = int(val)
			sizeFieldLen = n
			if offset+headerLen+sizeFieldLen+payloadLen > len(data) {
				return false
			}
		}

		payloadStart := offset + headerLen + sizeFieldLen
		payloadEnd := payloadStart + payloadLen

		if (obuType == av1OBUTypeFrameHeader || obuType == av1OBUTypeFrame) && payloadLen > 0 {
			b := data[payloadStart]
			showExistingFrame := (b & 0x01) != 0
			if !showExistingFrame {
				frameType := (b >> 1) & 0x03
				if frameType == 0 {
					return true
				}
			}
		}

		offset = payloadEnd
		if !hasSizeField {
			break
		}
	}
	return false
}

func decryptAV1E2EEOBUStream(encrypted []byte, cipherBlock cipher.Block, sifTrailer []byte) ([]byte, error) {
	if cipherBlock == nil {
		return nil, fmt.Errorf("cipherBlock is required")
	}
	if len(encrypted) == 0 {
		return nil, nil
	}
	if sifTrailer != nil && len(encrypted) >= len(sifTrailer) && bytes.HasSuffix(encrypted, sifTrailer) {
		return nil, nil
	}

	payload, meta, ok := extractAV1E2EEMetadataOBU(encrypted)
	if !ok {
		return nil, fmt.Errorf("missing AV1 E2EE metadata OBU")
	}

	layout, err := computeAV1EncryptionLayout(payload)
	if err != nil {
		return nil, fmt.Errorf("compute layout: %w", err)
	}

	cipherProtected := extractRanges(payload, layout.protectedRanges, layout.protectedLength)
	if len(cipherProtected) != layout.protectedLength {
		return nil, fmt.Errorf("invalid protected length: got %d want %d", len(cipherProtected), layout.protectedLength)
	}

	if len(meta.iv) != 12 {
		return nil, fmt.Errorf("unexpected IV length: %d", len(meta.iv))
	}
	if len(meta.tag) != av1GCMTagLengthBytes {
		return nil, fmt.Errorf("unexpected auth tag length: %d", len(meta.tag))
	}

	cipherProtectedWithTag := make([]byte, len(cipherProtected)+len(meta.tag))
	copy(cipherProtectedWithTag, cipherProtected)
	copy(cipherProtectedWithTag[len(cipherProtected):], meta.tag)

	aesGCM, err := cipher.NewGCMWithNonceSize(cipherBlock, len(meta.iv))
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	plainProtected, err := aesGCM.Open(nil, meta.iv, cipherProtectedWithTag, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt protected bytes: %w", err)
	}
	if len(plainProtected) != layout.protectedLength {
		return nil, fmt.Errorf("unexpected plaintext length: got %d want %d", len(plainProtected), layout.protectedLength)
	}

	out := make([]byte, len(payload))
	copy(out, payload)
	if err := writeRanges(out, layout.protectedRanges, plainProtected, layout.protectedLength); err != nil {
		return nil, err
	}

	return out, nil
}

func encryptAV1E2EEOBUStream(plain []byte, cipherBlock cipher.Block, keyIndex byte) ([]byte, error) {
	if cipherBlock == nil {
		return nil, fmt.Errorf("cipherBlock is required")
	}
	if len(plain) == 0 {
		return nil, nil
	}

	layout, err := computeAV1EncryptionLayout(plain)
	if err != nil {
		return nil, fmt.Errorf("compute layout: %w", err)
	}

	plainProtected := extractRanges(plain, layout.protectedRanges, layout.protectedLength)

	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("generate IV: %w", err)
	}

	aesGCM, err := cipher.NewGCMWithNonceSize(cipherBlock, len(iv))
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	cipherProtectedWithTag := aesGCM.Seal(nil, iv, plainProtected, nil)
	if len(cipherProtectedWithTag) != len(plainProtected)+av1GCMTagLengthBytes {
		return nil, fmt.Errorf("unexpected AES-GCM output length: got %d want %d", len(cipherProtectedWithTag), len(plainProtected)+av1GCMTagLengthBytes)
	}

	cipherProtected := cipherProtectedWithTag[:len(plainProtected)]
	tag := cipherProtectedWithTag[len(plainProtected):]

	metaOBU, err := buildAV1E2EEMetadataOBU(av1E2EEMetadata{
		keyIndex: keyIndex,
		iv:       iv,
		tag:      tag,
	})
	if err != nil {
		return nil, err
	}

	out := make([]byte, len(plain), len(plain)+len(metaOBU))
	copy(out, plain)
	if err := writeRanges(out, layout.protectedRanges, cipherProtected, layout.protectedLength); err != nil {
		return nil, err
	}
	out = append(out, metaOBU...)

	return out, nil
}

func buildAV1E2EEMetadataOBU(meta av1E2EEMetadata) ([]byte, error) {
	if len(meta.iv) != 12 {
		return nil, fmt.Errorf("unexpected IV length: %d", len(meta.iv))
	}
	if len(meta.tag) != av1GCMTagLengthBytes {
		return nil, fmt.Errorf("unexpected auth tag length: %d", len(meta.tag))
	}

	payload := make([]byte, 32)
	payload[0] = av1E2EEMetadataMagic0
	payload[1] = av1E2EEMetadataMagic1
	payload[2] = av1E2EEMetadataVersion
	payload[3] = meta.keyIndex
	copy(payload[4:16], meta.iv)
	copy(payload[16:32], meta.tag)

	obuHeader := byte(av1E2EEMetadataOBUType<<3) | 0x02
	sizeField := writeLeb128(uint32(len(payload)))

	out := make([]byte, 1+len(sizeField)+len(payload))
	out[0] = obuHeader
	copy(out[1:], sizeField)
	copy(out[1+len(sizeField):], payload)
	return out, nil
}

func extractAV1E2EEMetadataOBU(data []byte) ([]byte, av1E2EEMetadata, bool) {
	offset := 0
	for offset < len(data) {
		obuStart := offset

		obuType, ext, hasSizeField, ok := parseAV1OBUHeader(data[offset])
		if !ok {
			return nil, av1E2EEMetadata{}, false
		}

		headerLen := 1
		if ext {
			headerLen++
		}
		if offset+headerLen > len(data) {
			return nil, av1E2EEMetadata{}, false
		}

		if !hasSizeField {
			return nil, av1E2EEMetadata{}, false
		}

		val, n, ok := readLeb128(data, offset+headerLen)
		if !ok {
			return nil, av1E2EEMetadata{}, false
		}

		payloadLen := int(val)
		payloadStart := offset + headerLen + n
		payloadEnd := payloadStart + payloadLen
		if payloadEnd > len(data) {
			return nil, av1E2EEMetadata{}, false
		}

		obuEnd := payloadEnd

		if obuType == av1E2EEMetadataOBUType && obuEnd == len(data) && payloadLen == 32 {
			payload := data[payloadStart:payloadEnd]
			if payload[0] != av1E2EEMetadataMagic0 || payload[1] != av1E2EEMetadataMagic1 || payload[2] != av1E2EEMetadataVersion {
				return nil, av1E2EEMetadata{}, false
			}
			return data[:obuStart], av1E2EEMetadata{
				keyIndex: payload[3],
				iv:       append([]byte(nil), payload[4:16]...),
				tag:      append([]byte(nil), payload[16:32]...),
			}, true
		}

		offset = obuEnd
	}

	return nil, av1E2EEMetadata{}, false
}

func computeAV1EncryptionLayout(data []byte) (*av1EncryptionLayout, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty frame")
	}

	var protectedRanges []av1ByteRange
	protectedLength := 0

	offset := 0
	for offset < len(data) {
		obuType, ext, hasSizeField, ok := parseAV1OBUHeader(data[offset])
		if !ok {
			return nil, fmt.Errorf("invalid OBU header at offset %d", offset)
		}
		headerLen := 1
		if ext {
			headerLen++
		}
		if offset+headerLen > len(data) {
			return nil, fmt.Errorf("truncated OBU header at offset %d", offset)
		}

		var payloadLen int
		sizeFieldLen := 0
		if hasSizeField {
			val, n, ok := readLeb128(data, offset+headerLen)
			if !ok {
				return nil, fmt.Errorf("invalid leb128 at offset %d", offset+headerLen)
			}
			payloadLen = int(val)
			sizeFieldLen = n
			if offset+headerLen+sizeFieldLen+payloadLen > len(data) {
				return nil, fmt.Errorf("OBU payload extends past end at offset %d", offset)
			}
		} else {
			payloadLen = len(data) - (offset + headerLen)
		}

		payloadStart := offset + headerLen + sizeFieldLen
		payloadEnd := payloadStart + payloadLen

		clearPayloadPrefixLen := 0
		if obuType == av1OBUTypeFrameHeader || obuType == av1OBUTypeFrame {
			if payloadLen > 0 {
				clearPayloadPrefixLen = 1
			}
		}

		protectedStart := payloadStart + clearPayloadPrefixLen
		if protectedStart < payloadEnd {
			protectedRanges = append(protectedRanges, av1ByteRange{start: protectedStart, end: payloadEnd})
			protectedLength += payloadEnd - protectedStart
		}

		offset = payloadEnd
		if !hasSizeField {
			break
		}
	}

	return &av1EncryptionLayout{
		protectedRanges: protectedRanges,
		protectedLength: protectedLength,
	}, nil
}

func parseAV1OBUHeader(b byte) (obuType byte, extensionFlag bool, hasSizeField bool, ok bool) {
	if (b & 0x80) != 0 {
		return 0, false, false, false
	}
	if (b & 0x01) != 0 {
		return 0, false, false, false
	}
	return (b & 0x78) >> 3, (b & 0x04) != 0, (b & 0x02) != 0, true
}

func readLeb128(data []byte, offset int) (uint32, int, bool) {
	var value uint32
	shift := 0
	length := 0

	for offset+length < len(data) {
		b := data[offset+length]
		value += uint32(b&0x7f) << shift
		length++
		if (b & 0x80) == 0 {
			return value, length, true
		}
		shift += 7
		if length >= 5 {
			return 0, 0, false
		}
	}

	return 0, 0, false
}

func writeLeb128(value uint32) []byte {
	var out []byte
	v := value
	for v >= 0x80 {
		out = append(out, byte(v&0x7f)|0x80)
		v >>= 7
	}
	out = append(out, byte(v&0x7f))
	return out
}

func extractRanges(data []byte, ranges []av1ByteRange, totalLength int) []byte {
	out := make([]byte, 0, totalLength)
	for _, r := range ranges {
		if r.start < 0 || r.end < r.start || r.end > len(data) {
			continue
		}
		out = append(out, data[r.start:r.end]...)
	}
	return out
}

func writeRanges(target []byte, ranges []av1ByteRange, source []byte, totalLength int) error {
	if len(source) != totalLength {
		return fmt.Errorf("unexpected protected bytes length: %d, expected %d", len(source), totalLength)
	}
	readOffset := 0
	for _, r := range ranges {
		if r.start < 0 || r.end < r.start || r.end > len(target) {
			return fmt.Errorf("invalid protected range: %d..%d (len=%d)", r.start, r.end, len(target))
		}
		n := r.end - r.start
		copy(target[r.start:r.end], source[readOffset:readOffset+n])
		readOffset += n
	}
	return nil
}
