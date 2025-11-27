package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"log"
	"sync"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

// E2EE encryption constants (matching LiveKit client SDK)
const (
	// ivLength is the length of the initialization vector in bytes
	ivLength = 12
	// unencryptedVideoBytes is the number of leading bytes in H264 frames that remain unencrypted
	// This typically corresponds to the NAL unit header (1 byte for type + 1 byte for start code indicator)
	// Based on LiveKit SFrame implementation, this value may vary but we use 1 for compatibility
	unencryptedVideoBytes = 1
)

var (
	// ErrE2EENotEnabled is returned when attempting to decrypt without E2EE configuration
	ErrE2EENotEnabled = errors.New("E2EE decryption not enabled")
	// ErrSIFFrame indicates a Server Injected Frame (non-encrypted placeholder frame)
	ErrSIFFrame = errors.New("server injected frame detected")
	// ErrMalformedPayload indicates the payload is too short to contain valid encrypted data
	ErrMalformedPayload = errors.New("malformed encrypted payload")
)

// E2EEContext holds the encryption key and state for E2EE decryption.
//
// It provides thread-safe decryption of audio and video RTP payloads using
// AES-GCM 128-bit encryption. The context is initialized with a passphrase
// and a Server Injected Frame (SIF) trailer from the LiveKit room.
//
// Usage:
//
//	ctx, err := NewE2EEContext(passphrase, room.SifTrailer())
//	if err != nil {
//	    return err
//	}
//
//	// Decrypt audio payload
//	decrypted, err := ctx.DecryptAudio(rtpPayload)
//
//	// Decrypt video payload
//	decrypted, err := ctx.DecryptVideo(rtpPayload)
type E2EEContext struct {
	mu          sync.RWMutex
	key         []byte
	sifTrailer  []byte
	cipherBlock cipher.Block
	enabled     bool
}

// NewE2EEContext creates a new E2EE decryption context from a passphrase.
//
// The passphrase is used to derive a 128-bit AES key using PBKDF2 with
// the LiveKit standard salt ("LKFrameEncryptionKey"). The sifTrailer is
// used to identify Server Injected Frames (non-encrypted placeholder frames)
// which should be dropped during decryption.
//
// Parameters:
//   - passphrase: The shared secret used by all E2EE participants
//   - sifTrailer: Server Injected Frame trailer from room.SifTrailer()
//
// Returns an error if key derivation fails.
func NewE2EEContext(passphrase string, sifTrailer []byte) (*E2EEContext, error) {
	key, err := lksdk.DeriveKeyFromString(passphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to derive E2EE key: %w", err)
	}

	cipherBlock, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	log.Printf("[e2ee] initialized E2EE context (sifTrailer len=%d)", len(sifTrailer))

	return &E2EEContext{
		key:         key,
		sifTrailer:  sifTrailer,
		cipherBlock: cipherBlock,
		enabled:     true,
	}, nil
}

// Enabled returns true if E2EE decryption is configured.
func (e *E2EEContext) Enabled() bool {
	if e == nil {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enabled
}

// UpdateSifTrailer updates the Server Injected Frame trailer.
// Called when reconnecting to a room that may have a different trailer.
func (e *E2EEContext) UpdateSifTrailer(sifTrailer []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sifTrailer = sifTrailer
	log.Printf("[e2ee] updated sifTrailer (len=%d)", len(sifTrailer))
}

// DecryptAudio decrypts an E2EE-encrypted audio RTP payload.
//
// This function uses the built-in LiveKit SDK decryption function which
// handles the audio-specific encryption format (1 unencrypted byte).
//
// Returns:
//   - Decrypted payload on success
//   - nil with no error for Server Injected Frames (should be dropped)
//   - Error if decryption fails
func (e *E2EEContext) DecryptAudio(payload []byte) ([]byte, error) {
	if !e.Enabled() {
		return nil, ErrE2EENotEnabled
	}

	e.mu.RLock()
	cipherBlock := e.cipherBlock
	sifTrailer := e.sifTrailer
	e.mu.RUnlock()

	decrypted, err := lksdk.DecryptGCMAudioSampleCustomCipher(payload, sifTrailer, cipherBlock)
	if err != nil {
		return nil, fmt.Errorf("audio decryption failed: %w", err)
	}

	// nil return means this was a Server Injected Frame
	if decrypted == nil {
		return nil, nil
	}

	return decrypted, nil
}

// DecryptVideo decrypts an E2EE-encrypted video RTP payload.
//
// Video frames use the same AES-GCM encryption format as audio, but with
// potentially different unencrypted header bytes depending on the codec.
// For H264, typically 1-2 bytes of NAL unit header remain unencrypted.
//
// Encrypted payload format (same as LiveKit client SDK):
//
//	+---------+-------------------------+---------+----+
//	|frameHdr |     encrypted payload   |   IV    |len |KID|
//	+---------+-------------------------+---------+----+
//
// Where:
//   - frameHdr: Unencrypted frame header (used for authentication)
//   - encrypted payload: AES-GCM encrypted video data
//   - IV: Initialization vector (12 bytes typical)
//   - len: IV length (1 byte)
//   - KID: Key ID (1 byte, ignored - key provided externally)
//
// Returns:
//   - Decrypted payload on success
//   - nil with no error for Server Injected Frames (should be dropped)
//   - Error if decryption fails
func (e *E2EEContext) DecryptVideo(payload []byte) ([]byte, error) {
	if !e.Enabled() {
		return nil, ErrE2EENotEnabled
	}

	e.mu.RLock()
	cipherBlock := e.cipherBlock
	sifTrailer := e.sifTrailer
	e.mu.RUnlock()

	// Check for Server Injected Frame
	if sifTrailer != nil && len(payload) >= len(sifTrailer) {
		possibleTrailer := payload[len(payload)-len(sifTrailer):]
		if bytes.Equal(possibleTrailer, sifTrailer) {
			// This is an unencrypted Server Injected Frame - should be dropped
			return nil, nil
		}
	}

	// Minimum payload size: frameHeader + ciphertext(16 byte auth tag min) + IV + ivLength + KID
	minSize := unencryptedVideoBytes + 16 + ivLength + 2
	if len(payload) < minSize {
		return nil, ErrMalformedPayload
	}

	// Parse encrypted payload structure
	// Last 2 bytes: IV_LENGTH (1 byte) + KID (1 byte)
	frameTrailer := payload[len(payload)-2:]
	ivLen := int(frameTrailer[0])
	// KID := frameTrailer[1] // Key ID - ignored, we use externally provided key

	if ivLen > len(payload)-2-unencryptedVideoBytes {
		return nil, ErrMalformedPayload
	}

	// Extract components
	frameHeader := payload[:unencryptedVideoBytes]
	ivStart := len(payload) - 2 - ivLen
	iv := payload[ivStart : ivStart+ivLen]

	cipherTextStart := unencryptedVideoBytes
	cipherTextEnd := ivStart
	cipherText := payload[cipherTextStart:cipherTextEnd]

	// Create GCM cipher with the IV length from the payload
	aesGCM, err := cipher.NewGCMWithNonceSize(cipherBlock, ivLen)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM cipher: %w", err)
	}

	// Decrypt using authenticated decryption
	// The frame header is used as Additional Authenticated Data (AAD)
	plainText, err := aesGCM.Open(nil, iv, cipherText, frameHeader)
	if err != nil {
		return nil, fmt.Errorf("video decryption failed: %w", err)
	}

	// Reconstruct the decrypted payload: frameHeader + plainText
	result := make([]byte, len(frameHeader)+len(plainText))
	copy(result[:len(frameHeader)], frameHeader)
	copy(result[len(frameHeader):], plainText)

	return result, nil
}

// IsSIFFrame checks if the payload is a Server Injected Frame.
// SIF frames are non-encrypted placeholder frames that should be dropped.
func (e *E2EEContext) IsSIFFrame(payload []byte) bool {
	if !e.Enabled() {
		return false
	}

	e.mu.RLock()
	sifTrailer := e.sifTrailer
	e.mu.RUnlock()

	if sifTrailer == nil || len(payload) < len(sifTrailer) {
		return false
	}

	possibleTrailer := payload[len(payload)-len(sifTrailer):]
	return bytes.Equal(possibleTrailer, sifTrailer)
}
