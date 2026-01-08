package main

import (
	"crypto/aes"
	"testing"
)

func TestAV1E2EEEncryptDecryptRoundTrip(t *testing.T) {
	// Minimal AV1 OBU stream with size fields (Chromium RTCEncodedVideoFrame.data format):
	// 1) OBU_TEMPORAL_DELIMITER (type 2), size=0
	// 2) OBU_SEQUENCE_HEADER (type 1), size=1, payload=[0xAA]
	// 3) OBU_FRAME_HEADER (type 3), size=2, payload=[0x00, 0xBB]
	plain := []byte{0x12, 0x00, 0x0a, 0x01, 0xaa, 0x1a, 0x02, 0x00, 0xbb}

	key := make([]byte, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}

	encrypted, err := encryptAV1E2EEOBUStream(plain, block, 1)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	payload, meta, ok := extractAV1E2EEMetadataOBU(encrypted)
	if !ok {
		t.Fatalf("expected AV1 metadata OBU to be present")
	}
	if got, want := len(payload), len(plain); got != want {
		t.Fatalf("payload len=%d want %d", got, want)
	}
	if got, want := meta.keyIndex, byte(1); got != want {
		t.Fatalf("keyIndex=%d want %d", got, want)
	}

	// OBU headers and size fields must remain in the clear.
	mustEq := func(i int, want byte) {
		if got := payload[i]; got != want {
			t.Fatalf("payload[%d]=0x%02x want 0x%02x", i, got, want)
		}
	}
	mustEq(0, 0x12)
	mustEq(1, 0x00)
	mustEq(2, 0x0a)
	mustEq(3, 0x01)
	mustEq(5, 0x1a)
	mustEq(6, 0x02)
	// First payload byte of OBU_FRAME_HEADER is kept clear for SFU keyframe detection.
	mustEq(7, 0x00)

	if !isAV1OBUStreamKeyframe(payload) {
		t.Fatalf("expected encrypted payload to still be detectable as keyframe")
	}

	decrypted, err := decryptAV1E2EEOBUStream(encrypted, block, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(decrypted) != string(plain) {
		t.Fatalf("decrypted does not match plain: got=%x want=%x", decrypted, plain)
	}
	if !isAV1OBUStreamKeyframe(decrypted) {
		t.Fatalf("expected decrypted payload to be detectable as keyframe")
	}
}
