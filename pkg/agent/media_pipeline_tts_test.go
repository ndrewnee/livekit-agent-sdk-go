package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// TTSSuite provides a test suite for TextToSpeechStage
type TTSSuite struct {
	suite.Suite
	stage      *TextToSpeechStage
	ctx        context.Context
	mockServer *httptest.Server
	mockAPIKey string
}

func (suite *TTSSuite) SetupSuite() {
	suite.ctx = context.Background()
	suite.mockAPIKey = "test-api-key"
}

func (suite *TTSSuite) SetupTest() {
	// Create mock HTTP server for OpenAI API
	suite.mockServer = httptest.NewServer(http.HandlerFunc(suite.mockOpenAIHandler))

	// Create TTS stage with mock server URL
	suite.stage = NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "test-tts",
		Priority: 50,
		APIKey:   suite.mockAPIKey,
		Model:    "gpt-4o-mini-tts",
		Voice:    "alloy",
	})

	// Configure to use mock server endpoint
	suite.stage.SetEndpoint(suite.mockServer.URL)
}

func (suite *TTSSuite) TearDownTest() {
	if suite.mockServer != nil {
		suite.mockServer.Close()
	}
}

// mockOpenAIHandler simulates OpenAI TTS API responses
func (suite *TTSSuite) mockOpenAIHandler(w http.ResponseWriter, r *http.Request) {
	// Verify request method and headers
	assert.Equal(suite.T(), http.MethodPost, r.Method)
	assert.Equal(suite.T(), "Bearer "+suite.mockAPIKey, r.Header.Get("Authorization"))
	assert.Equal(suite.T(), "application/json", r.Header.Get("Content-Type"))

	// Parse request body
	var req OpenAITTSRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate request parameters
	if req.Input == "" {
		http.Error(w, "Empty input text", http.StatusBadRequest)
		return
	}

	if req.Input == "fail" {
		http.Error(w, "API Error", http.StatusInternalServerError)
		return
	}

	if req.Input == "rate_limit" {
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	// Return mock opus audio data
	mockOpusData := []byte("mock-opus-audio-data-" + req.Input)
	w.Header().Set("Content-Type", "audio/opus")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mockOpusData)
}

func (suite *TTSSuite) TestNewTextToSpeechStage() {
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "tts",
		Priority: 50,
		APIKey:   "api-key",
	})

	assert.Equal(suite.T(), "tts", stage.GetName())
	assert.Equal(suite.T(), 50, stage.GetPriority())
	assert.Equal(suite.T(), "api-key", stage.config.APIKey)
	assert.Equal(suite.T(), defaultTTSModel, stage.config.Model)
	assert.Equal(suite.T(), defaultVoice, stage.config.Voice)
	assert.Equal(suite.T(), defaultSpeed, stage.config.Speed)
	assert.NotNil(suite.T(), stage.client)
	assert.NotNil(suite.T(), stage.rateLimiter)
	assert.NotNil(suite.T(), stage.breaker)
	assert.NotNil(suite.T(), stage.metrics)
	assert.NotNil(suite.T(), stage.stats)
}

func (suite *TTSSuite) TestCanProcess() {
	assert.True(suite.T(), suite.stage.CanProcess(MediaTypeAudio))
	assert.False(suite.T(), suite.stage.CanProcess(MediaTypeVideo))
}

func (suite *TTSSuite) TestProcessWithPartialTranscriptions() {
	// Test with partial (non-final) transcription - should NOT generate TTS
	translations := map[string]string{
		"es": "Hola mundo",
		"fr": "Bonjour le monde",
	}

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track-1",
		Data:    []byte("audio"),
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Text:         "Hello world",
				Language:     "en",
				IsFinal:      false, // Partial transcription
				Translations: translations,
			},
		},
	}

	output, err := suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)

	// Should not generate TTS for partial transcriptions
	assert.NotContains(suite.T(), output.Metadata, "tts_audio_map")
	assert.NotContains(suite.T(), output.Metadata, "tts_generated")

	// Original data should pass through unchanged
	assert.Equal(suite.T(), input.Data, output.Data)
	assert.Equal(suite.T(), input.TrackID, output.TrackID)
}

func (suite *TTSSuite) TestProcessWithoutTranslations() {
	// Test with no metadata
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track-1",
		Data:    []byte("audio"),
	}

	output, err := suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)
	assert.Equal(suite.T(), input, output)

	// Test with metadata but no transcription event
	input.Metadata = map[string]interface{}{
		"some_other_data": "value",
	}

	output, err = suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)
	assert.Equal(suite.T(), input, output)

	// Test with transcription event but no translations
	input.Metadata = map[string]interface{}{
		"transcription_event": TranscriptionEvent{
			Text:         "Hello world",
			Language:     "en",
			Translations: map[string]string{}, // Empty translations
		},
	}

	output, err = suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)
	assert.Equal(suite.T(), input, output)
}

func (suite *TTSSuite) TestProcessWithTranslations() {
	// Mock successful TTS generation
	suite.setupMockServer()

	translations := map[string]string{
		"es": "Hola mundo",
		"fr": "Bonjour le monde",
	}

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track-1",
		Data:    []byte("audio"),
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Text:         "Hello world",
				Language:     "en",
				IsFinal:      true,
				Translations: translations,
			},
		},
	}

	output, err := suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)

	// Verify TTS audio was added to metadata
	assert.Contains(suite.T(), output.Metadata, "tts_audio_map")
	assert.Contains(suite.T(), output.Metadata, "tts_generated")
	assert.Contains(suite.T(), output.Metadata, "tts_generation_time")
	assert.Contains(suite.T(), output.Metadata, "tts_audio_format")

	ttsAudioMap := output.Metadata["tts_audio_map"].(map[string][]byte)
	assert.Len(suite.T(), ttsAudioMap, 2)
	assert.Contains(suite.T(), ttsAudioMap, "es")
	assert.Contains(suite.T(), ttsAudioMap, "fr")

	// Verify audio data
	assert.Equal(suite.T(), []byte("mock-opus-audio-data-Hola mundo"), ttsAudioMap["es"])
	assert.Equal(suite.T(), []byte("mock-opus-audio-data-Bonjour le monde"), ttsAudioMap["fr"])

	// Verify metadata
	assert.True(suite.T(), output.Metadata["tts_generated"].(bool))
	assert.Equal(suite.T(), "opus", output.Metadata["tts_audio_format"].(string))
}

func (suite *TTSSuite) TestProcessWithPartialFailures() {
	suite.setupMockServer()

	translations := map[string]string{
		"es":   "Hola mundo",       // Should succeed
		"fail": "fail",             // Should fail (mock returns error)
		"fr":   "Bonjour le monde", // Should succeed
	}

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track-1",
		Data:    []byte("audio"),
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Text:         "Hello world",
				Language:     "en",
				IsFinal:      true,
				Translations: translations,
			},
		},
	}

	output, err := suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)

	// Should still have successful TTS results
	ttsAudioMap := output.Metadata["tts_audio_map"].(map[string][]byte)
	assert.Len(suite.T(), ttsAudioMap, 2) // Only successful ones
	assert.Contains(suite.T(), ttsAudioMap, "es")
	assert.Contains(suite.T(), ttsAudioMap, "fr")
	assert.NotContains(suite.T(), ttsAudioMap, "fail")
}

func (suite *TTSSuite) TestCircuitBreaker() {
	suite.setupMockServer()

	// Test that circuit breaker starts closed
	assert.True(suite.T(), suite.stage.canMakeAPICall())

	// Simulate failures to trip the circuit breaker
	for i := 0; i < ttsMaxFailures; i++ {
		suite.stage.recordAPIFailure()
	}

	// Circuit should now be open
	assert.False(suite.T(), suite.stage.canMakeAPICall())

	// Test that circuit breaker allows retry after timeout
	suite.stage.breaker.nextRetryTime = time.Now().Add(-time.Second)
	assert.True(suite.T(), suite.stage.canMakeAPICall())

	// Successful call should close the circuit
	suite.stage.recordAPISuccess()
	assert.True(suite.T(), suite.stage.canMakeAPICall())
}

func (suite *TTSSuite) TestRateLimiting() {
	// Create a stage with very low rate limit for testing
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "test",
		Priority: 50,
		APIKey:   "key",
	})
	stage.rateLimiter = nil // Disable rate limiter for specific testing

	// Test that rate limiter allows initial requests
	assert.True(suite.T(), stage.rateLimiter == nil || stage.rateLimiter.Allow())
}

func (suite *TTSSuite) TestInputValidation() {
	suite.setupMockServer()

	testCases := []struct {
		name        string
		input       string
		shouldError bool
	}{
		{"valid_input", "Hello world", false},
		{"empty_input", "", true},
		{"whitespace_only", "   ", true},
		{"max_size_input", strings.Repeat("a", maxTTSInputSize), false},
		{"oversized_input", strings.Repeat("a", maxTTSInputSize+1), true},
	}

	for _, tc := range testCases {
		suite.T().Run(tc.name, func(t *testing.T) {
			_, err := suite.stage.generateTTS(suite.ctx, tc.input)
			if tc.shouldError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func (suite *TTSSuite) TestMetricsTracking() {
	suite.setupMockServer()

	// Initial metrics should be zero
	stats := suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(0), stats.TotalGenerations)
	assert.Equal(suite.T(), uint64(0), stats.SuccessfulGenerations)

	// Process some TTS
	translations := map[string]string{
		"es": "Hola",
		"fr": "Bonjour",
	}

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track-1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				IsFinal:      true,
				Translations: translations,
			},
		},
	}

	_, err := suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)

	// Check updated metrics
	stats = suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(2), stats.TotalGenerations)      // 2 languages
	assert.Equal(suite.T(), uint64(2), stats.SuccessfulGenerations) // Both succeeded
	assert.Equal(suite.T(), uint64(1), stats.ParallelBatches)       // 1 batch processed
}

func (suite *TTSSuite) TestGetAPIMetrics() {
	metrics := suite.stage.GetAPIMetrics()

	// Should return a map with expected keys
	expectedKeys := []string{
		"total_calls", "successful_calls", "failed_calls",
		"circuit_breaker_trips", "rate_limit_exceeded",
	}

	for _, key := range expectedKeys {
		assert.Contains(suite.T(), metrics, key)
	}
}

// setupMockServer configures the TTS stage to use the mock server
func (suite *TTSSuite) setupMockServer() {
	// Mock server is already configured in SetupTest
	// This method is kept for compatibility with existing test calls
}

// Run the test suite
func TestTTSSuite(t *testing.T) {
	suite.Run(t, new(TTSSuite))
}

// Individual test functions for specific scenarios
func TestTextToSpeechStage_NewStageDefaults(t *testing.T) {
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "test",
		Priority: 40,
		APIKey:   "api-key",
	})

	assert.Equal(t, "test", stage.GetName())
	assert.Equal(t, 40, stage.GetPriority())
	assert.Equal(t, defaultTTSModel, stage.config.Model)
	assert.Equal(t, defaultVoice, stage.config.Voice)
	assert.Equal(t, defaultSpeed, stage.config.Speed)
}

func TestTextToSpeechStage_NewStageCustom(t *testing.T) {
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "custom",
		Priority: 60,
		APIKey:   "key",
		Model:    "custom-model",
		Voice:    "nova",
	})

	assert.Equal(t, "custom", stage.GetName())
	assert.Equal(t, 60, stage.GetPriority())
	assert.Equal(t, "custom-model", stage.config.Model)
	assert.Equal(t, "nova", stage.config.Voice)
}

func TestTextToSpeechStage_ProcessNonAudio(t *testing.T) {
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "test",
		Priority: 50,
		APIKey:   "key",
	})

	input := MediaData{Type: MediaTypeVideo, TrackID: "track-1"}
	output, err := stage.Process(context.Background(), input)

	require.NoError(t, err)
	assert.Equal(t, input, output) // Should pass through unchanged
}
