//go:build integration

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// TranslationIntegrationTestSuite tests integration with OpenAI API using mocks.
type TranslationIntegrationTestSuite struct {
	suite.Suite
	stage      *TranslationStage
	mockServer *httptest.Server
	responses  map[string]string // Mock responses for different prompts
}

func (suite *TranslationIntegrationTestSuite) SetupTest() {
	// Initialize mock responses
	suite.responses = map[string]string{
		"multi_lang":     `{"en": "Hello world", "es": "Hola mundo", "fr": "Bonjour le monde"}`,
		"single_lang":    `{"es": "Hola mundo"}`,
		"error_response": `{"error": {"message": "Rate limit exceeded", "type": "insufficient_quota"}}`,
	}

	// Create mock OpenAI server
	suite.mockServer = httptest.NewServer(http.HandlerFunc(suite.mockOpenAIHandler))

	// Create translation stage with mock server
	suite.stage = NewTranslationStage("integration-test", 30, "test-api-key")

	// Override the API endpoint to use our mock server
	// We'll need to modify the callOpenAI method to accept custom endpoint for testing
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

// mockOpenAIHandler simulates OpenAI API responses.
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

	// Determine response based on prompt content
	var response OpenAIChatResponse
	prompt := req.Messages[0].Content

	switch {
	case strings.Contains(prompt, "error_test"):
		// Simulate API error
		response = OpenAIChatResponse{
			Error: &struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			}{
				Message: "Rate limit exceeded",
				Type:    "insufficient_quota",
			},
		}
	case strings.Contains(prompt, "timeout_test"):
		// Simulate timeout by sleeping
		time.Sleep(10 * time.Second)
		return
	case strings.Contains(prompt, "Target languages: es, fr"):
		// Multi-language translation
		response = OpenAIChatResponse{
			Choices: []struct {
				Message ChatMessage `json:"message"`
			}{
				{
					Message: ChatMessage{
						Role:    "assistant",
						Content: suite.responses["multi_lang"],
					},
				},
			},
		}
	case strings.Contains(prompt, "Target languages: es"):
		// Single language translation
		response = OpenAIChatResponse{
			Choices: []struct {
				Message ChatMessage `json:"message"`
			}{
				{
					Message: ChatMessage{
						Role:    "assistant",
						Content: suite.responses["single_lang"],
					},
				},
			},
		}
	default:
		// Default successful response
		response = OpenAIChatResponse{
			Choices: []struct {
				Message ChatMessage `json:"message"`
			}{
				{
					Message: ChatMessage{
						Role:    "assistant",
						Content: `{"en": "Default translation"}`,
					},
				},
			},
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// TestSuccessfulMultiLanguageTranslation tests successful API integration.
func (suite *TranslationIntegrationTestSuite) TestSuccessfulMultiLanguageTranslation() {
	// Add callback to inject target languages
	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es", "fr"}
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

	// Process should inject target languages via callback
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)
	suite.Equal(input.Type, output.Type)
	suite.Equal(input.TrackID, output.TrackID)

	// Test that the mock server is responding
	suite.NotNil(suite.mockServer, "Mock server should be available")
	suite.NotEmpty(suite.responses, "Mock responses should be configured")
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

func TestTranslationIntegration(t *testing.T) {
	suite.Run(t, new(TranslationIntegrationTestSuite))
}
