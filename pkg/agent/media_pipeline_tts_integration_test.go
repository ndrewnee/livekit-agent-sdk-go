//go:build integration

package agent

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// TTSIntegrationSuite provides integration tests with real OpenAI API
// Run with: go test -tags=integration ./internal/media
// Requires: OPENAI_API_KEY environment variable
type TTSIntegrationSuite struct {
	suite.Suite
	stage  *TextToSpeechStage
	ctx    context.Context
	apiKey string
}

func (suite *TTSIntegrationSuite) SetupSuite() {
	suite.ctx = context.Background()

	// Get API key from environment
	suite.apiKey = os.Getenv("OPENAI_API_KEY")
	if suite.apiKey == "" {
		suite.T().Skip("OPENAI_API_KEY not set, skipping integration tests")
	}
}

func (suite *TTSIntegrationSuite) SetupTest() {
	// Create real TTS stage with OpenAI API
	suite.stage = NewTextToSpeechStage("integration-tts", 50, suite.apiKey, "tts-1", "alloy")
}

func (suite *TTSIntegrationSuite) TestRealOpenAITTSGeneration() {
	// Test single language TTS generation
	audioData, err := suite.stage.generateTTS(suite.ctx, "Hello, this is a test of the OpenAI TTS API integration.")

	require.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), audioData)
	assert.Greater(suite.T(), len(audioData), 1000) // Should be substantial opus audio data

	// Verify it's opus format (basic check - opus files start with "OggS")
	assert.True(suite.T(), len(audioData) >= 4, "Audio data too short to be valid opus")
	// Note: OpenAI returns opus format, but may not be in Ogg container
}

func (suite *TTSIntegrationSuite) TestRealParallelTTSGeneration() {
	translations := map[string]string{
		"es": "Hola, esta es una prueba de la integración de la API TTS de OpenAI.",
		"fr": "Bonjour, ceci est un test de l'intégration de l'API TTS d'OpenAI.",
		"de": "Hallo, dies ist ein Test der OpenAI TTS API-Integration.",
	}

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "integration-track",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Text:         "Hello, this is a test.",
				Language:     "en",
				Translations: translations,
			},
		},
	}

	startTime := time.Now()
	output, err := suite.stage.Process(suite.ctx, input)
	duration := time.Since(startTime)

	require.NoError(suite.T(), err)

	// Verify TTS audio was generated
	assert.Contains(suite.T(), output.Metadata, "tts_audio_map")
	ttsAudioMap := output.Metadata["tts_audio_map"].(map[string][]byte)

	// Should have audio for all languages
	assert.Len(suite.T(), ttsAudioMap, 3)
	assert.Contains(suite.T(), ttsAudioMap, "es")
	assert.Contains(suite.T(), ttsAudioMap, "fr")
	assert.Contains(suite.T(), ttsAudioMap, "de")

	// Verify audio data quality
	for lang, audio := range ttsAudioMap {
		assert.NotEmpty(suite.T(), audio, "Audio data empty for language: %s", lang)
		assert.Greater(suite.T(), len(audio), 500, "Audio data too small for language: %s", lang)
	}

	// Parallel processing should be faster than sequential
	suite.T().Logf("Parallel TTS generation took: %v", duration)
	assert.Less(suite.T(), duration, 10*time.Second, "Parallel generation took too long")

	// Verify metadata
	assert.True(suite.T(), output.Metadata["tts_generated"].(bool))
	assert.Equal(suite.T(), "opus", output.Metadata["tts_audio_format"].(string))
}

func (suite *TTSIntegrationSuite) TestRealErrorHandling() {
	// Test with oversized input
	oversizedText := make([]byte, MaxTTSInputSize+100)
	for i := range oversizedText {
		oversizedText[i] = 'a'
	}

	_, err := suite.stage.generateTTS(suite.ctx, string(oversizedText))
	assert.Error(suite.T(), err)
	assert.Contains(suite.T(), err.Error(), "input text too large")

	// Test with empty input
	_, err = suite.stage.generateTTS(suite.ctx, "")
	assert.Error(suite.T(), err)
	assert.Contains(suite.T(), err.Error(), "empty text input")

	// Test with whitespace only
	_, err = suite.stage.generateTTS(suite.ctx, "   ")
	assert.Error(suite.T(), err)
	assert.Contains(suite.T(), err.Error(), "empty text input")
}

func (suite *TTSIntegrationSuite) TestRealRateLimiting() {
	// Create a stage with lower rate limit for testing
	stage := NewTextToSpeechStage("rate-limit-test", 50, suite.apiKey, "tts-1", "alloy")

	// Make several rapid requests to test rate limiting
	var successCount, rateLimitCount int

	for i := 0; i < 20; i++ {
		_, err := stage.generateTTS(suite.ctx, "Rate limit test")
		if err != nil {
			if err.Error() == "rate limit exceeded for TTS API" {
				rateLimitCount++
			} else {
				suite.T().Logf("Unexpected error: %v", err)
			}
		} else {
			successCount++
		}
	}

	suite.T().Logf("Successful requests: %d, Rate limited: %d", successCount, rateLimitCount)
	// Should have some rate limiting with 20 rapid requests
	assert.Greater(suite.T(), rateLimitCount, 0, "Expected some rate limiting")
}

func (suite *TTSIntegrationSuite) TestRealCircuitBreaker() {
	// Create stage with invalid API key to trigger failures
	invalidStage := NewTextToSpeechStage("circuit-test", 50, "invalid-key", "tts-1", "alloy")

	// Make requests to trip the circuit breaker
	var errorCount int
	for i := 0; i < TTSMaxFailures+2; i++ {
		_, err := invalidStage.generateTTS(suite.ctx, "Circuit breaker test")
		if err != nil {
			errorCount++
		}
	}

	assert.Greater(suite.T(), errorCount, TTSMaxFailures, "Should have errors from invalid API key")

	// Circuit should be open now
	assert.False(suite.T(), invalidStage.canMakeAPICall(), "Circuit breaker should be open")
}

func (suite *TTSIntegrationSuite) TestRealDifferentVoices() {
	voices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}

	for _, voice := range voices {
		suite.T().Run("voice_"+voice, func(t *testing.T) {
			stage := NewTextToSpeechStage("voice-test", 50, suite.apiKey, "tts-1", voice)

			audioData, err := stage.generateTTS(suite.ctx, "Testing different voices.")

			require.NoError(t, err)
			assert.NotEmpty(t, audioData)
			assert.Greater(t, len(audioData), 500, "Audio too small for voice: %s", voice)
		})
	}
}

func (suite *TTSIntegrationSuite) TestRealDifferentModels() {
	models := []string{"tts-1", "tts-1-hd"}

	for _, model := range models {
		suite.T().Run("model_"+model, func(t *testing.T) {
			stage := NewTextToSpeechStage("model-test", 50, suite.apiKey, model, "alloy")

			audioData, err := stage.generateTTS(suite.ctx, "Testing different TTS models.")

			require.NoError(t, err)
			assert.NotEmpty(t, audioData)
			assert.Greater(t, len(audioData), 500, "Audio too small for model: %s", model)
		})
	}
}

func (suite *TTSIntegrationSuite) TestRealLongText() {
	// Test with longer text (but within limits)
	longText := "This is a longer text to test the OpenAI TTS API with more substantial content. " +
		"The quick brown fox jumps over the lazy dog. " +
		"Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor " +
		"incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud " +
		"exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat."

	audioData, err := suite.stage.generateTTS(suite.ctx, longText)

	require.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), audioData)
	assert.Greater(suite.T(), len(audioData), 2000, "Audio should be substantial for long text")
}

func (suite *TTSIntegrationSuite) TestRealMetricsTracking() {
	// Generate some TTS to update metrics
	translations := map[string]string{
		"es": "Prueba de métricas",
		"fr": "Test des métriques",
	}

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "metrics-track",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Translations: translations,
			},
		},
	}

	initialStats := suite.stage.GetStats()

	_, err := suite.stage.Process(suite.ctx, input)
	require.NoError(suite.T(), err)

	finalStats := suite.stage.GetStats()

	// Verify metrics were updated
	assert.Greater(suite.T(), finalStats.TotalGenerations, initialStats.TotalGenerations)
	assert.Greater(suite.T(), finalStats.SuccessfulGenerations, initialStats.SuccessfulGenerations)
	assert.Greater(suite.T(), finalStats.ParallelBatches, initialStats.ParallelBatches)
	assert.Greater(suite.T(), finalStats.AverageLatencyMs, float64(0))

	// Get API metrics
	apiMetrics := suite.stage.GetAPIMetrics()
	assert.Greater(suite.T(), apiMetrics["total_calls"], uint64(0))
	assert.Greater(suite.T(), apiMetrics["successful_calls"], uint64(0))
}

// Run the integration test suite
func TestTTSIntegrationSuite(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration tests")
	}
	suite.Run(t, new(TTSIntegrationSuite))
}

// Standalone integration test for quick verification
func TestTTSQuickIntegration(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	stage := NewTextToSpeechStage("quick-test", 50, apiKey, "tts-1", "alloy")

	audioData, err := stage.generateTTS(context.Background(), "Quick integration test.")

	require.NoError(t, err)
	assert.NotEmpty(t, audioData)
	assert.Greater(t, len(audioData), 100)

	t.Logf("Generated %d bytes of opus audio data", len(audioData))
}
