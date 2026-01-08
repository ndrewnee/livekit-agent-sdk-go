package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
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
// The passphrase is processed in two ways to support different client configurations:
//
//  1. If the passphrase is a base64 URL-encoded 16-byte key, it is decoded and used
//     with DeriveKeyFromBytes (HKDF derivation). This matches the LiveKit JS SDK
//     behavior when using ExternalE2EEKeyProvider.setKey(key).
//
//  2. Otherwise, DeriveKeyFromString is used (PBKDF2 derivation). This matches the
//     LiveKit JS SDK behavior when using a passphrase string.
//
// The sifTrailer is used to identify Server Injected Frames (non-encrypted
// placeholder frames) which should be dropped during decryption.
//
// Parameters:
//   - passphrase: The shared secret used by all E2EE participants (base64 key or passphrase)
//   - sifTrailer: Server Injected Frame trailer from room.SifTrailer()
//
// Returns an error if key derivation fails.
func NewE2EEContext(passphrase string, sifTrailer []byte) (*E2EEContext, error) {
	var key []byte
	var err error
	var derivationMethod string

	// First, try to decode as base64 URL-encoded key (for ExternalE2EEKeyProvider)
	keyBytes, decodeErr := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(passphrase)
	if decodeErr == nil && len(keyBytes) == 16 {
		// Valid base64-encoded 16-byte key - use HKDF derivation
		key, err = lksdk.DeriveKeyFromBytes(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to derive E2EE key from bytes: %w", err)
		}
		derivationMethod = "DeriveKeyFromBytes (base64 key)"
	} else {
		// Not a valid base64 key - use passphrase with PBKDF2 derivation
		key, err = lksdk.DeriveKeyFromString(passphrase)
		if err != nil {
			return nil, fmt.Errorf("failed to derive E2EE key from string: %w", err)
		}
		derivationMethod = "DeriveKeyFromString (passphrase)"
	}

	cipherBlock, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	log.Printf("[e2ee] initialized E2EE context using %s (sifTrailer len=%d)", derivationMethod, len(sifTrailer))

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
//
// debugPayloadOnce is used to log the first encrypted payload for debugging
var debugPayloadOnce sync.Once
var debugPayloadCount int

func (e *E2EEContext) DecryptAudio(payload []byte) ([]byte, error) {
	if !e.Enabled() {
		return nil, ErrE2EENotEnabled
	}

	e.mu.RLock()
	cipherBlock := e.cipherBlock
	sifTrailer := e.sifTrailer
	e.mu.RUnlock()

	// Debug: log the first few payloads to understand the format
	debugPayloadOnce.Do(func() {
		log.Printf("[e2ee-debug] First audio payload (len=%d):", len(payload))
		if len(payload) >= 20 {
			log.Printf("[e2ee-debug]   First 10 bytes: %x", payload[:10])
			log.Printf("[e2ee-debug]   Last 10 bytes: %x", payload[len(payload)-10:])
			log.Printf("[e2ee-debug]   Last 2 bytes (trailer): ivLen=%d, keyID=%d", payload[len(payload)-2], payload[len(payload)-1])
		}
		log.Printf("[e2ee-debug]   sifTrailer len=%d", len(sifTrailer))
		if len(sifTrailer) > 0 {
			log.Printf("[e2ee-debug]   sifTrailer: %x", sifTrailer)
		}
	})

	decrypted, err := lksdk.DecryptGCMAudioSampleCustomCipher(payload, sifTrailer, cipherBlock)
	if err != nil {
		debugPayloadCount++
		if debugPayloadCount <= 3 {
			log.Printf("[e2ee-debug] Decryption failed for payload %d (len=%d):", debugPayloadCount, len(payload))
			if len(payload) >= 20 {
				log.Printf("[e2ee-debug]   First 10 bytes: %x", payload[:10])
				log.Printf("[e2ee-debug]   Last 10 bytes: %x", payload[len(payload)-10:])
				log.Printf("[e2ee-debug]   Trailer: ivLen=%d, keyID=%d", payload[len(payload)-2], payload[len(payload)-1])
			} else {
				log.Printf("[e2ee-debug]   Full payload: %x", payload)
			}
		}
		return nil, fmt.Errorf("audio decryption failed: %w", err)
	}

	// nil return means this was a Server Injected Frame
	if decrypted == nil {
		debugPayloadCount++
		if debugPayloadCount <= 3 {
			log.Printf("[e2ee-debug] SIF frame detected for payload %d (dropped)", debugPayloadCount)
		}
		return nil, nil
	}

	// Log successful decryption for first few packets
	debugPayloadCount++
	if debugPayloadCount <= 3 {
		log.Printf("[e2ee-debug] Successfully decrypted audio payload %d: encrypted=%d bytes -> decrypted=%d bytes",
			debugPayloadCount, len(payload), len(decrypted))
	}

	return decrypted, nil
}

// DecryptRTPPayload decrypts an E2EE-encrypted RTP payload (works for both audio and video).
//
// LiveKit E2EE uses the same encryption format for all media types:
//   - 1 byte unencrypted header (used as AAD)
//   - encrypted payload with GCM auth tag
//   - IV (typically 12 bytes)
//   - ivLen (1 byte)
//   - keyID (1 byte)
//
// This function uses the SDK's DecryptGCMAudioSampleCustomCipher which works
// for both audio and video RTP payloads.
//
// Returns:
//   - Decrypted payload on success
//   - nil with no error for Server Injected Frames (should be dropped)
//   - Error if decryption fails
func (e *E2EEContext) DecryptRTPPayload(payload []byte) ([]byte, error) {
	if !e.Enabled() {
		return nil, ErrE2EENotEnabled
	}

	e.mu.RLock()
	cipherBlock := e.cipherBlock
	sifTrailer := e.sifTrailer
	e.mu.RUnlock()

	// Use the SDK's audio decryption function - it works for video too
	// since the encryption format is identical
	decrypted, err := lksdk.DecryptGCMAudioSampleCustomCipher(payload, sifTrailer, cipherBlock)
	if err != nil {
		return nil, err
	}

	return decrypted, nil
}

// DecryptVideo decrypts an E2EE-encrypted video RTP payload.
// This is an alias for DecryptRTPPayload for backwards compatibility.
func (e *E2EEContext) DecryptVideo(payload []byte) ([]byte, error) {
	return e.DecryptRTPPayload(payload)
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
