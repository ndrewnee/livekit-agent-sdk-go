package agent

import (
	"encoding/base64"
	"testing"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetEncryptionKey tests SetEncryptionKey validation and state management
func TestSetEncryptionKey(t *testing.T) {
	testCases := []struct {
		name          string
		encodedKey    string
		expectError   bool
		errorContains string
		validateState func(t *testing.T, pipeline *MediaPipeline)
	}{
		{
			name: "valid 16-byte key",
			encodedKey: func() string {
				keyBytes := []byte{
					0x8d, 0x94, 0x22, 0x46, 0x98, 0xa8, 0xc8, 0x6f,
					0x1b, 0xce, 0x9a, 0x1e, 0x8c, 0x4e, 0xe5, 0xc6,
				}
				return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(keyBytes)
			}(),
			expectError: false,
			validateState: func(t *testing.T, pipeline *MediaPipeline) {
				pipeline.mu.RLock()
				defer pipeline.mu.RUnlock()
				assert.NotNil(t, pipeline.encryptionKey)
				assert.Equal(t, 16, len(pipeline.encryptionKey)) // HKDF-derived key is 16 bytes
				assert.NotNil(t, pipeline.encryptionCipher)
			},
		},
		{
			name:          "invalid base64",
			encodedKey:    "not-valid-base64!!!",
			expectError:   true,
			errorContains: "decode encryption key",
		},
		{
			name: "wrong size - 8 bytes",
			encodedKey: func() string {
				keyBytes := make([]byte, 8)
				return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(keyBytes)
			}(),
			expectError:   true,
			errorContains: "invalid key size: expected 16 bytes for AES-128-GCM, got 8",
		},
		{
			name: "wrong size - 32 bytes",
			encodedKey: func() string {
				keyBytes := make([]byte, 32)
				return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(keyBytes)
			}(),
			expectError:   true,
			errorContains: "invalid key size: expected 16 bytes for AES-128-GCM, got 32",
		},
		{
			name: "wrong size - 0 bytes",
			encodedKey: func() string {
				keyBytes := make([]byte, 0)
				return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(keyBytes)
			}(),
			expectError:   true,
			errorContains: "invalid key size: expected 16 bytes for AES-128-GCM, got 0",
		},
		{
			name:          "empty string",
			encodedKey:    "",
			expectError:   true,
			errorContains: "",
		},
		{
			name: "nil room handling",
			encodedKey: func() string {
				keyBytes := make([]byte, 16)
				return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(keyBytes)
			}(),
			expectError: false,
			validateState: func(t *testing.T, pipeline *MediaPipeline) {
				pipeline.mu.RLock()
				defer pipeline.mu.RUnlock()
				assert.NotNil(t, pipeline.encryptionKey)
				assert.NotNil(t, pipeline.encryptionCipher)
				assert.Nil(t, pipeline.room)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := NewMediaPipeline()

			// Always pass nil room for these tests
			err := pipeline.SetEncryptionKey(nil, tc.encodedKey)

			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				require.NoError(t, err)
				if tc.validateState != nil {
					tc.validateState(t, pipeline)
				}
			}
		})
	}
}

// TestSetEncryptionKeyConcurrent tests concurrent key setting for race conditions
func TestSetEncryptionKeyConcurrent(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Generate test keys
	keys := make([]string, 10)
	for i := 0; i < 10; i++ {
		keyBytes := make([]byte, 16)
		keyBytes[0] = byte(i) // Make each key different
		keys[i] = base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(keyBytes)
	}

	// Set keys concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(keyIndex int) {
			err := pipeline.SetEncryptionKey(nil, keys[keyIndex])
			assert.NoError(t, err)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify pipeline has valid encryption key
	pipeline.mu.RLock()
	assert.NotNil(t, pipeline.encryptionKey)
	assert.NotNil(t, pipeline.encryptionCipher)
	pipeline.mu.RUnlock()
}

// TestMediaPipelineE2EEDecryption tests end-to-end encryption/decryption flow
// This integration test verifies that our pipeline correctly:
// 1. Stores encryption key and cipher via SetEncryptionKey
// 2. Uses the cached cipher to decrypt audio packets
func TestMediaPipelineE2EEDecryption(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Original plaintext audio data
	plaintext := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	}

	// Generate encryption key (16 bytes)
	keyBytes := []byte{
		0x8d, 0x94, 0x22, 0x46, 0x98, 0xa8, 0xc8, 0x6f,
		0x1b, 0xce, 0x9a, 0x1e, 0x8c, 0x4e, 0xe5, 0xc6,
	}
	encodedKey := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(keyBytes)

	// Step 1: Set encryption key in pipeline (this is our code)
	err := pipeline.SetEncryptionKey(nil, encodedKey)
	require.NoError(t, err)

	// Step 2: Get the cached cipher from pipeline (testing our cipher caching)
	pipeline.mu.RLock()
	cipherBlock := pipeline.encryptionCipher
	pipeline.mu.RUnlock()
	require.NotNil(t, cipherBlock, "cipher should be cached after SetEncryptionKey")

	// Step 3: Encrypt plaintext (simulating what the client/browser does)
	// We use LiveKit SDK here only to simulate the client side
	encrypted, err := lksdk.EncryptGCMAudioSampleCustomCipher(plaintext, 0, cipherBlock)
	require.NoError(t, err)
	assert.NotEqual(t, plaintext, encrypted, "encrypted data should differ from plaintext")

	// Step 4: Decrypt using our cached cipher (this tests our integration)
	// This mimics what happens in subscribeToTrack when processing RTP packets
	decrypted, err := lksdk.DecryptGCMAudioSampleCustomCipher(encrypted, nil, cipherBlock)
	require.NoError(t, err)

	// Step 5: Verify decrypted matches original plaintext
	assert.Equal(t, plaintext, decrypted, "decrypted data should match original plaintext")
}
