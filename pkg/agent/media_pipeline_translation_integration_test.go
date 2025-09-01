//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// TranslationIntegrationTestSuite tests integration with OpenAI API using both mock and real endpoints.
type TranslationIntegrationTestSuite struct {
	suite.Suite
	stage      *TranslationStage
	mockServer *httptest.Server
	responses  map[string]string // Mock responses for different prompts
}

func (suite *TranslationIntegrationTestSuite) SetupTest() {
	// Initialize mock responses
	suite.responses = map[string]string{
		"multi_lang":     `{"es": "Hola mundo", "fr": "Bonjour le monde", "de": "Hallo Welt"}`,
		"single_lang":    `{"es": "Hola mundo"}`,
		"empty_response": `{}`,
	}

	// Create mock OpenAI server
	suite.mockServer = httptest.NewServer(http.HandlerFunc(suite.mockOpenAIHandler))

	// Create translation stage with mock server endpoint
	suite.stage = NewTranslationStageWithEndpoint("integration-test", 30, "test-api-key", suite.mockServer.URL)
	suite.stage.client = &http.Client{Timeout: 5 * time.Second}
}

func (suite *TranslationIntegrationTestSuite) TearDownTest() {
	if suite.mockServer != nil {
		suite.mockServer.Close()
	}
	if suite.stage != nil {
		suite.stage.Disconnect()
	}
}

// mockOpenAIHandler simulates OpenAI Streaming API responses with Server-Sent Events.
func (suite *TranslationIntegrationTestSuite) mockOpenAIHandler(w http.ResponseWriter, r *http.Request) {
	// Verify request format
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check authorization header
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse request body
	var req OpenAIChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Verify streaming is enabled
	if !req.Stream {
		http.Error(w, "Streaming not enabled", http.StatusBadRequest)
		return
	}

	// Set up streaming response headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Determine response based on prompt content
	prompt := req.Messages[0].Content

	switch {
	case strings.Contains(prompt, "error_test"):
		// Simulate streaming API error
		errorChunk := OpenAIStreamChunk{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}{
				Message: "Rate limit exceeded",
				Type:    "insufficient_quota",
			},
		}
		data, _ := json.Marshal(errorChunk)
		w.Write([]byte("data: " + string(data) + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
		return
	case strings.Contains(prompt, "timeout_test"):
		// Simulate timeout by sleeping
		time.Sleep(10 * time.Second)
		return
	case strings.Contains(prompt, "Target languages: es, fr, de"):
		// Multi-language translation - stream the response
		suite.streamResponse(w, suite.responses["multi_lang"])
	case strings.Contains(prompt, "Target languages: es"):
		// Single language translation - stream the response
		suite.streamResponse(w, suite.responses["single_lang"])
	case strings.Contains(prompt, "empty_response"):
		// Empty response test
		suite.streamResponse(w, suite.responses["empty_response"])
	default:
		// Default successful response - stream it
		suite.streamResponse(w, `{"en": "Default translation"}`)
	}
}

// streamResponse simulates streaming a response in chunks
func (suite *TranslationIntegrationTestSuite) streamResponse(w http.ResponseWriter, content string) {
	// Split content into chunks to simulate streaming
	chunkSize := 10
	for i := 0; i < len(content); i += chunkSize {
		end := i + chunkSize
		if end > len(content) {
			end = len(content)
		}

		chunk := content[i:end]
		streamChunk := OpenAIStreamChunk{
			Choices: []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			}{
				{
					Delta: struct {
						Content string `json:"content"`
					}{
						Content: chunk,
					},
				},
			},
		}

		data, _ := json.Marshal(streamChunk)
		w.Write([]byte("data: " + string(data) + "\n\n"))

		// Small delay to simulate streaming
		time.Sleep(1 * time.Millisecond)
	}

	// Send completion chunk
	finishReason := "stop"
	completionChunk := OpenAIStreamChunk{
		Choices: []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		}{
			{
				Delta: struct {
					Content string `json:"content"`
				}{
					Content: "",
				},
				FinishReason: &finishReason,
			},
		},
	}

	data, _ := json.Marshal(completionChunk)
	w.Write([]byte("data: " + string(data) + "\n\n"))
	w.Write([]byte("data: [DONE]\n\n"))
}

// TestSuccessfulMultiLanguageTranslation tests successful API integration.
func (suite *TranslationIntegrationTestSuite) TestSuccessfulMultiLanguageTranslation() {
	// Add callback to inject target languages
	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es", "fr", "de"}
	})

	ctx := context.Background()
	// Test with sample transcription
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "Hello world",
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	// Process should inject target languages via callback and get translations
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)
	suite.Equal(input.Type, output.Type)
	suite.Equal(input.TrackID, output.TrackID)

	// Check that translation was added to transcription event
	transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
	suite.True(ok)
	suite.NotEmpty(transcriptionEvent.Translations)

	// Verify specific translations
	suite.Contains(transcriptionEvent.Translations, "es")
	suite.Contains(transcriptionEvent.Translations, "fr")
	suite.Contains(transcriptionEvent.Translations, "de")
	suite.Equal("Hola mundo", transcriptionEvent.Translations["es"])
	suite.Equal("Bonjour le monde", transcriptionEvent.Translations["fr"])
	suite.Equal("Hallo Welt", transcriptionEvent.Translations["de"])

	// Check metrics
	metrics := suite.stage.GetAPIMetrics()
	suite.Greater(metrics["successful_calls"].(uint64), uint64(0))
	suite.Equal(metrics["failed_calls"].(uint64), uint64(0))
}

// TestSingleLanguageTranslation tests translation to a single language.
func (suite *TranslationIntegrationTestSuite) TestSingleLanguageTranslation() {
	// Add callback to inject single target language
	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	ctx := context.Background()
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "Hello world",
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)

	// Check translation
	transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
	suite.True(ok)
	suite.Len(transcriptionEvent.Translations, 1)
	suite.Equal("Hola mundo", transcriptionEvent.Translations["es"])
}

// TestAPIErrorHandling tests error scenarios.
func (suite *TranslationIntegrationTestSuite) TestAPIErrorHandling() {
	// Add callback to inject target languages
	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	ctx := context.Background()

	// Create input with error trigger
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "error_test text", // This will trigger error in mock
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	// Process should handle error gracefully
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err, "Pipeline should not fail on translation errors")
	suite.Equal(input.Type, output.Type)
	suite.Equal(input.TrackID, output.TrackID)

	// Check metrics show the failure
	metrics := suite.stage.GetAPIMetrics()
	suite.Greater(metrics["failed_calls"].(uint64), uint64(0))
}

// TestContextCancellation tests proper context handling.
func (suite *TranslationIntegrationTestSuite) TestContextCancellation() {
	// Add callback to inject target languages
	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Create input with timeout trigger
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "timeout_test text", // This will trigger timeout in mock
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	// Process should handle context cancellation
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err, "Pipeline should handle context cancellation gracefully")
	suite.Equal(input, output)
}

// TestRateLimitingIntegration tests rate limiting behavior.
func (suite *TranslationIntegrationTestSuite) TestRateLimitingIntegration() {
	// Test rate limiting directly on the rateLimiter
	allowedCount := 0
	deniedCount := 0

	// Make rapid requests to test rate limiting
	for i := 0; i < 30; i++ { // More than burst size (20)
		if suite.stage.rateLimiter.Allow() {
			allowedCount++
		} else {
			deniedCount++
			suite.stage.recordRateLimitExceeded()
		}
	}

	// Should have some rate limited requests
	suite.Greater(deniedCount, 0, "Should encounter rate limiting")
	suite.Greater(allowedCount, 0, "Should have some successful requests")

	// Check metrics
	metrics := suite.stage.GetAPIMetrics()
	suite.Greater(metrics["rate_limit_exceeded"].(uint64), uint64(0))
}

// TestCircuitBreakerIntegration tests circuit breaker functionality.
func (suite *TranslationIntegrationTestSuite) TestCircuitBreakerIntegration() {
	// Add callback to inject target languages
	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	ctx := context.Background()

	// Create input that will cause API errors
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "error_test text", // Triggers error in mock
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	// Make several requests to trigger circuit breaker
	for i := 0; i < MaxConsecutiveFailures+1; i++ {
		suite.stage.Process(ctx, input)
	}

	// Circuit breaker should now be open
	suite.False(suite.stage.canMakeAPICall(), "Circuit breaker should be open after failures")

	// Check metrics
	metrics := suite.stage.GetAPIMetrics()
	suite.Greater(metrics["circuit_breaker_trips"].(uint64), uint64(0))
	suite.Greater(metrics["failed_calls"].(uint64), uint64(0))
}

// TestCachingIntegration tests cache functionality.
func (suite *TranslationIntegrationTestSuite) TestCachingIntegration() {
	// Add callback to inject target languages
	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	ctx := context.Background()
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "Hello world",
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	// First call - should hit API
	output1, err1 := suite.stage.Process(ctx, input)
	suite.NoError(err1)

	metrics1 := suite.stage.GetAPIMetrics()
	initialCalls := metrics1["successful_calls"].(uint64)

	// Second call with same input - should hit cache
	output2, err2 := suite.stage.Process(ctx, input)
	suite.NoError(err2)

	metrics2 := suite.stage.GetAPIMetrics()
	finalCalls := metrics2["successful_calls"].(uint64)

	// Should have same result but no additional API call
	suite.Equal(output1, output2)
	suite.Equal(initialCalls, finalCalls, "Second call should use cache")

	// Cache should have entries
	suite.Greater(metrics2["cache_entries"].(int), 0)
}

// TestRealOpenAIIntegration tests against real OpenAI API if API key is provided.
func (suite *TranslationIntegrationTestSuite) TestRealOpenAIIntegration() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Create stage with real OpenAI endpoint
	realStage := NewTranslationStage("real-integration-test", 30, apiKey)
	defer realStage.Disconnect()

	// Add callback to inject target languages
	realStage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es", "fr"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "Hello, how are you today?",
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	// Process with real API
	output, err := realStage.Process(ctx, input)
	suite.NoError(err)

	// Check that translations were received
	transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
	suite.True(ok)
	suite.NotEmpty(transcriptionEvent.Translations)

	// Should have Spanish and French translations
	suite.Contains(transcriptionEvent.Translations, "es")
	suite.Contains(transcriptionEvent.Translations, "fr")

	// Translations should not be empty
	suite.NotEmpty(transcriptionEvent.Translations["es"])
	suite.NotEmpty(transcriptionEvent.Translations["fr"])

	// Check metrics
	stats := realStage.GetStats()
	suite.Greater(stats.SuccessfulTranslations, uint64(0))
	suite.Greater(stats.AverageTimeToFirstToken, float64(0))

	fmt.Printf("Real API translations: %+v\n", transcriptionEvent.Translations)
	fmt.Printf("Time to first token: %.2fms\n", stats.AverageTimeToFirstToken)
}

func TestTranslationIntegration(t *testing.T) {
	suite.Run(t, new(TranslationIntegrationTestSuite))
}
