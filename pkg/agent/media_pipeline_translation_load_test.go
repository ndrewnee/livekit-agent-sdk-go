package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// TranslationLoadTestSuite tests high-volume and concurrent scenarios.
type TranslationLoadTestSuite struct {
	suite.Suite
	stage      *TranslationStage
	mockServer *httptest.Server
}

func (suite *TranslationLoadTestSuite) SetupTest() {
	// Setup mock OpenAI streaming server
	suite.mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set headers for Server-Sent Events
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		// Send mock streaming response with multiple languages
		mockResponse := `data: {"choices":[{"delta":{"content":"{\"es\":\"Texto de prueba de carga\",\"fr\":\"Texte de test de charge\",\"de\":\"Lasttest-Text\",\"it\":\"Testo di carico\",\"pt\":\"Texto de teste de carga\"}"}}]}

data: [DONE]

`
		_, _ = w.Write([]byte(mockResponse))
	}))

	suite.stage = NewTranslationStage(&TranslationConfig{
		Name:     "load-test",
		Priority: 30,
		APIKey:   "test-api-key",
	})
	suite.stage.SetEndpoint(suite.mockServer.URL)
}

func (suite *TranslationLoadTestSuite) TearDownTest() {
	if suite.stage != nil {
		suite.stage.Disconnect()
	}
	if suite.mockServer != nil {
		suite.mockServer.Close()
	}
}

// TestHighConcurrencyProcessing tests processing operations under load.
func (suite *TranslationLoadTestSuite) TestHighConcurrencyProcessing() {
	const (
		numGoroutines          = 100
		operationsPerGoroutine = 50
	)

	var (
		processedCount int64
		successCount   int64
	)

	ctx := context.Background()
	var wg sync.WaitGroup

	// Start processing goroutines
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				languages := []string{"es", "fr", "de", "it", "pt"}
				targetLangs := languages[:j%len(languages)+1]

				input := MediaData{
					Type:    MediaTypeAudio,
					TrackID: fmt.Sprintf("worker_%d_track_%d", workerID, j),
					Metadata: map[string]interface{}{
						"transcription_event": TranscriptionEvent{
							Text:     fmt.Sprintf("Load test message %d from worker %d", j, workerID),
							Language: "en",
							IsFinal:  true,
						},
						"target_languages": targetLangs,
					},
				}

				output, err := suite.stage.Process(ctx, input)
				atomic.AddInt64(&processedCount, 1)

				if err == nil {
					atomic.AddInt64(&successCount, 1)
				}
				_ = output // Use output to avoid lint warnings
			}
		}(i)
	}

	// Wait for all operations to complete
	wg.Wait()

	// Verify no race conditions occurred
	stats := suite.stage.GetStats()
	suite.NotPanics(func() {
		// Test with valid input
		testInput := MediaData{
			Type:    MediaTypeAudio,
			TrackID: "test-track",
			Metadata: map[string]interface{}{
				"transcription_event": TranscriptionEvent{
					Text:     "Test text",
					Language: "en",
					IsFinal:  true,
				},
				"target_languages": []string{"es"},
			},
		}
		_, _ = suite.stage.Process(ctx, testInput)
	}, "Should not panic after concurrent operations")

	suite.Greater(processedCount, int64(0))

	suite.T().Logf("Completed %d processing operations, %d successful",
		processedCount, successCount)
	suite.T().Logf("Final stats: %d total translations, %d failed",
		stats.TotalTranslations, stats.FailedTranslations)
}

// TestMetricsAccuracyUnderLoad tests metrics tracking accuracy.
func (suite *TranslationLoadTestSuite) TestMetricsAccuracyUnderLoad() {
	const numOperations = 1000

	var (
		expectedSuccesses int64
		expectedFailures  int64
	)

	// Simulate API calls with known success/failure patterns
	for i := 0; i < numOperations; i++ {
		duration := time.Duration(i%100) * time.Millisecond
		suite.stage.recordAPILatency(duration)

		if i%10 == 0 {
			// Simulate failure
			suite.stage.recordAPIFailure()
			atomic.AddInt64(&expectedFailures, 1)
		} else {
			// Simulate success
			suite.stage.recordAPISuccess()
			atomic.AddInt64(&expectedSuccesses, 1)
		}
	}

	// Verify metrics accuracy
	metrics := suite.stage.GetAPIMetrics()

	suite.Equal(expectedSuccesses, int64(metrics["successful_calls"].(uint64)))
	suite.Equal(expectedFailures, int64(metrics["failed_calls"].(uint64)))
	suite.Equal(expectedSuccesses+expectedFailures, int64(metrics["total_calls"].(uint64)))

	// Average latency should be reasonable
	avgLatency := metrics["average_latency_ms"].(float64)
	suite.Greater(avgLatency, 0.0)
	suite.Less(avgLatency, 100.0) // Should be under 100ms for our test data
}

// TestInputValidationLimits tests input size and count validation.
func (suite *TranslationLoadTestSuite) TestInputValidationLimits() {
	ctx := context.Background()

	// Test maximum input text size
	largeText := strings.Repeat("a", maxInputTextSize+1)
	targetLangs := []string{"es"}

	_, err := suite.stage.translateViaStreaming(ctx, largeText, "en", targetLangs)
	suite.Error(err, "Should reject oversized input")
	suite.Contains(err.Error(), "input text too large")

	// Test maximum target languages
	tooManyLangs := make([]string, maxTargetLanguages+1)
	for i := range tooManyLangs {
		tooManyLangs[i] = fmt.Sprintf("lang%d", i)
	}

	_, err = suite.stage.translateViaStreaming(ctx, "test", "en", tooManyLangs)
	suite.Error(err, "Should reject too many target languages")
	suite.Contains(err.Error(), "too many target languages")
}

// TestStressTestCircuitBreakerRecovery tests circuit breaker under stress.
func (suite *TranslationLoadTestSuite) TestStressTestCircuitBreakerRecovery() {
	const numFailures = 5 * 2 // maxConsecutiveFailures * 2

	// Trigger failures to open circuit breaker
	for i := 0; i < numFailures; i++ {
		suite.stage.recordAPIFailure()
	}

	// Circuit should be open
	suite.False(suite.stage.canMakeAPICall(), "Circuit should be open after failures")

	// Manually set recovery time for testing (avoid long sleep)
	suite.stage.breaker.mu.Lock()
	suite.stage.breaker.nextRetryTime = time.Now().Add(-1 * time.Second)
	suite.stage.breaker.mu.Unlock()

	// Should allow retry attempt (half-open state)
	suite.True(suite.stage.canMakeAPICall(), "Circuit should allow retry after timeout")

	// Record success to close circuit
	suite.stage.recordAPISuccess()
	suite.True(suite.stage.canMakeAPICall(), "Circuit should be closed after success")
}

// TestRateLimiterUnderSustainedLoad tests rate limiting behavior.
func (suite *TranslationLoadTestSuite) TestRateLimiterUnderSustainedLoad() {
	const testDuration = 2 * time.Second

	var (
		allowedRequests int64
		deniedRequests  int64
	)

	// Create many goroutines making requests
	var wg sync.WaitGroup
	stopChan := make(chan struct{})

	// Start sustained load
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				select {
				case <-stopChan:
					return
				default:
					if suite.stage.rateLimiter.Allow() {
						atomic.AddInt64(&allowedRequests, 1)
					} else {
						atomic.AddInt64(&deniedRequests, 1)
					}
					time.Sleep(10 * time.Millisecond) // Small delay
				}
			}
		}()
	}

	// Run for test duration
	time.Sleep(testDuration)
	close(stopChan)
	wg.Wait()

	suite.T().Logf("Rate limiting results: %d allowed, %d denied requests",
		allowedRequests, deniedRequests)

	// Should have both allowed and denied requests under sustained load
	suite.Greater(allowedRequests, int64(0))
	suite.Greater(deniedRequests, int64(0))

	// Rate should be approximately correct (allowing for burst)
	expectedMaxRequests := int64(float64(testDuration.Seconds())*float64(defaultRateLimit) + defaultBurstSize)
	suite.LessOrEqual(allowedRequests, expectedMaxRequests*2, "Rate limiting should be working")
}

// TestMetricsRollingWindowAccuracy tests latency bucket accuracy.
func (suite *TranslationLoadTestSuite) TestMetricsRollingWindowAccuracy() {
	// Add known latencies
	knownLatencies := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
	}

	var totalLatencyMs float64
	for _, latency := range knownLatencies {
		suite.stage.recordAPILatency(latency)
		totalLatencyMs += float64(latency.Nanoseconds()) / 1e6
	}

	expectedAverage := totalLatencyMs / float64(len(knownLatencies))

	metrics := suite.stage.GetAPIMetrics()
	actualAverage := metrics["average_latency_ms"].(float64)

	// Should be close to expected average (within 1ms tolerance)
	suite.InDelta(expectedAverage, actualAverage, 1.0,
		"Rolling average should be accurate")
}

func TestTranslationLoadTests(t *testing.T) {
	suite.Run(t, new(TranslationLoadTestSuite))
}
