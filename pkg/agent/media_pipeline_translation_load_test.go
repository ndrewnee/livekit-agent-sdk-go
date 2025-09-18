package agent

import (
	"context"
	"fmt"
	"runtime"
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
	stage *TranslationStage
}

func (suite *TranslationLoadTestSuite) SetupTest() {
	suite.stage = NewTranslationStage("load-test", 30, "test-api-key", "")
}

func (suite *TranslationLoadTestSuite) TearDownTest() {
	if suite.stage != nil {
		suite.stage.Disconnect()
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

// TestCachePerformanceUnderLoad tests caching behavior with high volume.
func (suite *TranslationLoadTestSuite) TestCachePerformanceUnderLoad() {
	const (
		numGoroutines        = 50
		requestsPerGoroutine = 100
		uniqueTexts          = 20 // Repeated texts to test cache efficiency
	)

	var (
		cacheHits   int64
		cacheMisses int64
	)

	// Pre-populate some cache entries
	baseTexts := make([]string, uniqueTexts)
	for i := 0; i < uniqueTexts; i++ {
		baseTexts[i] = fmt.Sprintf("Test text number %d for caching", i)
	}

	var wg sync.WaitGroup

	// Concurrent cache operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := 0; j < requestsPerGoroutine; j++ {
				text := baseTexts[j%uniqueTexts]
				targetLangs := []string{"es", "fr"}

				// Generate cache key
				cacheKey := suite.stage.generateCacheKey(text, "en", targetLangs)

				// Check cache
				if cached := suite.stage.getCachedTranslation(cacheKey); cached != nil {
					atomic.AddInt64(&cacheHits, 1)
				} else {
					atomic.AddInt64(&cacheMisses, 1)

					// Simulate adding to cache
					mockTranslations := map[string]string{
						"es": fmt.Sprintf("Spanish translation of text %d", j%uniqueTexts),
						"fr": fmt.Sprintf("French translation of text %d", j%uniqueTexts),
					}
					suite.stage.cacheTranslation(cacheKey, mockTranslations)
				}
			}
		}(i)
	}

	wg.Wait()

	suite.T().Logf("Cache performance: %d hits, %d misses (%.2f%% hit rate)",
		cacheHits, cacheMisses, float64(cacheHits)/float64(cacheHits+cacheMisses)*100)

	// Should have good cache hit rate due to repeated texts
	hitRate := float64(cacheHits) / float64(cacheHits+cacheMisses)
	suite.Greater(hitRate, 0.5, "Cache hit rate should be > 50% with repeated texts")
}

// TestCacheEvictionUnderPressure tests cache cleanup behavior.
func (suite *TranslationLoadTestSuite) TestCacheEvictionUnderPressure() {
	// Fill cache to just under capacity first
	for i := 0; i < maxCacheSize-10; i++ {
		text := fmt.Sprintf("Cache test text %d", i)
		targetLangs := []string{"es"}
		cacheKey := suite.stage.generateCacheKey(text, "en", targetLangs)

		translations := map[string]string{"es": fmt.Sprintf("Translation %d", i)}
		suite.stage.cacheTranslation(cacheKey, translations)
	}

	// Add some expired entries manually
	for i := 0; i < 50; i++ {
		text := fmt.Sprintf("Expired text %d", i)
		cacheKey := suite.stage.generateCacheKey(text, "en", []string{"es"})
		suite.stage.cache[cacheKey] = &translationCacheEntry{
			translations: map[string]string{"es": "expired"},
			timestamp:    time.Now().Add(-2 * cacheTTL), // Expired
		}
	}

	// Now add more entries which should trigger cleanup
	for i := 0; i < 20; i++ {
		text := fmt.Sprintf("New cache text %d", i)
		targetLangs := []string{"es"}
		cacheKey := suite.stage.generateCacheKey(text, "en", targetLangs)

		translations := map[string]string{"es": fmt.Sprintf("New translation %d", i)}
		suite.stage.cacheTranslation(cacheKey, translations)
	}

	// Verify cache size is reasonable (cleanup should have occurred)
	suite.stage.cacheMu.RLock()
	cacheSize := len(suite.stage.cache)
	suite.stage.cacheMu.RUnlock()

	suite.LessOrEqual(cacheSize, maxCacheSize+50, "Cache should have cleaned up expired entries")
}

// TestMemoryUsageUnderLoad tests memory consumption patterns.
func (suite *TranslationLoadTestSuite) TestMemoryUsageUnderLoad() {
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Simulate high load
	const iterations = 1000
	for i := 0; i < iterations; i++ {
		// Add cache entries
		text := fmt.Sprintf("Load test text %d", i%100) // Some repetition
		cacheKey := suite.stage.generateCacheKey(text, "en", []string{"es"})
		translations := map[string]string{"es": fmt.Sprintf("Translation %d", i)}
		suite.stage.cacheTranslation(cacheKey, translations)

		// Update metrics
		suite.stage.recordAPILatency(time.Duration(i%1000) * time.Millisecond)

		// Simulate some API calls for metrics
		if i%10 == 0 {
			suite.stage.recordAPIFailure()
		} else {
			suite.stage.recordAPISuccess()
		}
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	var memoryGrowth uint64
	if m2.Alloc > m1.Alloc {
		memoryGrowth = m2.Alloc - m1.Alloc
	} else {
		memoryGrowth = 0 // Memory was freed by GC
	}
	suite.T().Logf("Memory growth after %d iterations: %d bytes", iterations, memoryGrowth)

	// Memory growth should be reasonable (less than 10MB for this test)
	suite.Less(memoryGrowth, uint64(10*1024*1024), "Memory growth should be bounded")
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

// TestCacheCleanupEfficiency tests cache cleanup performance.
func (suite *TranslationLoadTestSuite) TestCacheCleanupEfficiency() {
	// Fill cache with entries of varying ages
	now := time.Now()

	// Add old entries (should be cleaned)
	for i := 0; i < 500; i++ {
		text := fmt.Sprintf("old_text_%d", i)
		cacheKey := suite.stage.generateCacheKey(text, "en", []string{"es"})
		suite.stage.cache[cacheKey] = &translationCacheEntry{
			translations: map[string]string{"es": "old translation"},
			timestamp:    now.Add(-2 * cacheTTL), // Expired
		}
	}

	// Add recent entries (should be kept)
	for i := 0; i < 500; i++ {
		text := fmt.Sprintf("new_text_%d", i)
		cacheKey := suite.stage.generateCacheKey(text, "en", []string{"es"})
		suite.stage.cache[cacheKey] = &translationCacheEntry{
			translations: map[string]string{"es": "new translation"},
			timestamp:    now.Add(-30 * time.Minute), // Valid
		}
	}

	// Measure cleanup performance
	start := time.Now()
	suite.stage.cleanupStaleEntriesLocked()
	cleanupDuration := time.Since(start)

	// Verify cleanup effectiveness
	suite.LessOrEqual(len(suite.stage.cache), 500, "Should have cleaned up expired entries")
	suite.Less(cleanupDuration, 100*time.Millisecond, "Cleanup should be fast")
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
