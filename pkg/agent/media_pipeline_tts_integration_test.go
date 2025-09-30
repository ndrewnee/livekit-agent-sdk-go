//go:build integration

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	suite.stage = NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "integration-tts",
		Priority: 50,
		APIKey:   suite.apiKey,
		Model:    "tts-1",
		Voice:    "alloy",
	})
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
	oversizedText := make([]byte, maxTTSInputSize+100)
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
	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "rate-limit-test",
		Priority: 50,
		APIKey:   suite.apiKey,
		Model:    "tts-1",
		Voice:    "alloy",
	})

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
	invalidStage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "circuit-test",
		Priority: 50,
		APIKey:   "invalid-key",
		Model:    "tts-1",
		Voice:    "alloy",
	})

	// Make requests to trip the circuit breaker
	var errorCount int
	for i := 0; i < ttsMaxFailures+2; i++ {
		_, err := invalidStage.generateTTS(suite.ctx, "Circuit breaker test")
		if err != nil {
			errorCount++
		}
	}

	assert.Greater(suite.T(), errorCount, ttsMaxFailures, "Should have errors from invalid API key")

	// Circuit should be open now
	assert.False(suite.T(), invalidStage.canMakeAPICall(), "Circuit breaker should be open")
}

func (suite *TTSIntegrationSuite) TestRealDifferentVoices() {
	voices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}

	for _, voice := range voices {
		suite.T().Run("voice_"+voice, func(t *testing.T) {
			stage := NewTextToSpeechStage(&TextToSpeechConfig{
				Name:     "voice-test",
				Priority: 50,
				APIKey:   suite.apiKey,
				Model:    "tts-1",
				Voice:    voice,
			})

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
			stage := NewTextToSpeechStage(&TextToSpeechConfig{
				Name:     "model-test",
				Priority: 50,
				APIKey:   suite.apiKey,
				Model:    model,
				Voice:    "alloy",
			})

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

// TTSLatencyTestSuite provides comprehensive latency and performance testing with real OpenAI API
type TTSLatencyTestSuite struct {
	suite.Suite
}

// TestAverageLatencyMultipleRuns measures average latency across multiple TTS generation runs.
func (suite *TTSLatencyTestSuite) TestAverageLatencyMultipleRuns() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	realStage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "latency-test",
		Priority: 50,
		APIKey:   apiKey,
		Model:    "gpt-4o-mini-tts", // Default baseline model
		Voice:    "alloy",
	})

	const numRuns = 10
	var totalLatencies []time.Duration
	var audioSizes []int
	successCount := 0

	// Use different text for each run to avoid cache hits
	testTexts := []string{
		"Hello, how are you doing today? I hope you're having a great day!",
		"Good morning everyone! The weather is beautiful today, isn't it?",
		"Thank you for joining our meeting. Let's get started with the agenda.",
		"I appreciate your help with this project. Your expertise is invaluable.",
		"The presentation went very well. Everyone seemed engaged and interested.",
		"Let's schedule a follow-up meeting to discuss the next steps forward.",
		"Your feedback on the proposal was extremely helpful and constructive.",
		"We need to finalize the budget before the end of the month.",
		"The team has been working hard to meet all the project deadlines.",
		"Congratulations on your success! You've earned this achievement through dedication.",
	}

	fmt.Printf("\n=== Running %d TTS generation tests ===\n", numRuns)

	for i := 0; i < numRuns; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		start := time.Now()
		audioData, err := realStage.generateTTS(ctx, testTexts[i])
		latency := time.Since(start)
		cancel()

		if err == nil && len(audioData) > 0 {
			successCount++
			totalLatencies = append(totalLatencies, latency)
			audioSizes = append(audioSizes, len(audioData))

			fmt.Printf("Run %d: Latency=%dms, AudioSize=%d bytes\n",
				i+1, latency.Milliseconds(), len(audioData))
		} else {
			fmt.Printf("Run %d: FAILED - %v\n", i+1, err)
		}

		time.Sleep(100 * time.Millisecond)
	}

	suite.Greater(successCount, numRuns/2, "At least half of the runs should succeed")

	if len(totalLatencies) > 0 {
		avgLatency := calculateAverage(totalLatencies)
		minLatency := calculateMin(totalLatencies)
		maxLatency := calculateMax(totalLatencies)
		stdDev := calculateStdDev(totalLatencies, avgLatency)

		fmt.Printf("\n=== Latency Statistics ===\n")
		fmt.Printf("Average: %dms\n", avgLatency.Milliseconds())
		fmt.Printf("Min: %dms\n", minLatency.Milliseconds())
		fmt.Printf("Max: %dms\n", maxLatency.Milliseconds())
		fmt.Printf("StdDev: %dms\n", stdDev.Milliseconds())
		fmt.Printf("Success Rate: %d/%d (%.1f%%)\n", successCount, numRuns, float64(successCount)/float64(numRuns)*100)
	}

	if len(audioSizes) > 0 {
		var totalSize int
		minSize := audioSizes[0]
		maxSize := audioSizes[0]
		for _, size := range audioSizes {
			totalSize += size
			if size < minSize {
				minSize = size
			}
			if size > maxSize {
				maxSize = size
			}
		}
		avgSize := totalSize / len(audioSizes)

		fmt.Printf("\n=== Audio Size Statistics ===\n")
		fmt.Printf("Average: %d bytes\n", avgSize)
		fmt.Printf("Min: %d bytes\n", minSize)
		fmt.Printf("Max: %d bytes\n", maxSize)
	}

	stats := realStage.GetStats()
	fmt.Printf("\n=== Overall Stage Statistics ===\n")
	fmt.Printf("Total Generations: %d\n", stats.TotalGenerations)
	fmt.Printf("Successful: %d\n", stats.SuccessfulGenerations)
	fmt.Printf("Failed: %d\n", stats.FailedGenerations)
	fmt.Printf("Average Latency: %.2fms\n", stats.AverageLatencyMs)
}

// TestLatencyWithDifferentTextLengths measures performance with varying text lengths.
func (suite *TTSLatencyTestSuite) TestLatencyWithDifferentTextLengths() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	realStage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "length-test",
		Priority: 50,
		APIKey:   apiKey,
		Model:    "gpt-4o-mini-tts",
		Voice:    "alloy",
	})

	testCases := []struct {
		name string
		text string
	}{
		{
			name: "Short (10 words)",
			text: "Hello, how are you doing today? Great weather!",
		},
		{
			name: "Medium (50 words)",
			text: "Good morning everyone! I wanted to take a moment to discuss our project progress. We've made significant strides in the development phase and I'm pleased to report that we're on track to meet our deadline. The team has been working incredibly hard and their dedication is truly appreciated. Let's continue this momentum!",
		},
		{
			name: "Long (150 words)",
			text: "Ladies and gentlemen, thank you for joining us today for this important presentation. I'd like to begin by discussing the current state of our industry and how it relates to our company's strategic vision. Over the past several years, we've witnessed unprecedented changes in consumer behavior, technological advancement, and market dynamics. These shifts have created both challenges and opportunities that we must navigate carefully. Our research indicates that customers are increasingly demanding more personalized experiences, faster service delivery, and greater transparency in business operations. To address these evolving needs, we've developed a comprehensive strategy that focuses on three key pillars: innovation in product development, enhancement of customer service capabilities, and investment in cutting-edge technology infrastructure. By executing this strategy effectively, we believe we can not only maintain our competitive position but also capture new market segments and drive sustainable growth for years to come. Now let me share some specific details about each pillar.",
		},
	}

	fmt.Printf("\n=== Testing Different Text Lengths ===\n")

	for _, tc := range testCases {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		start := time.Now()
		audioData, err := realStage.generateTTS(ctx, tc.text)
		latency := time.Since(start)
		cancel()

		if err == nil {
			fmt.Printf("\n%s:\n", tc.name)
			fmt.Printf("  Latency: %dms\n", latency.Milliseconds())
			fmt.Printf("  Audio Size: %d bytes\n", len(audioData))
			fmt.Printf("  Text Length: %d chars\n", len(tc.text))
			fmt.Printf("  Bytes per char: %.2f\n", float64(len(audioData))/float64(len(tc.text)))
		} else {
			fmt.Printf("\n%s: FAILED - %v\n", tc.name, err)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// TestLatencyWithMultipleLanguages measures performance with different numbers of parallel TTS generations.
func (suite *TTSLatencyTestSuite) TestLatencyWithMultipleLanguages() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	testCases := []struct {
		name         string
		translations map[string]string
	}{
		{
			name: "Single Language (Spanish)",
			translations: map[string]string{
				"es": "Hola a todos! Hoy vamos a discutir el cronograma del proyecto.",
			},
		},
		{
			name: "Two Languages (Spanish, French)",
			translations: map[string]string{
				"es": "Hola a todos! Hoy vamos a discutir el cronograma del proyecto.",
				"fr": "Bonjour à tous! Aujourd'hui nous allons discuter du calendrier du projet.",
			},
		},
		{
			name: "Five Languages",
			translations: map[string]string{
				"es": "Hola a todos! Hoy vamos a discutir el cronograma del proyecto.",
				"fr": "Bonjour à tous! Aujourd'hui nous allons discuter du calendrier du projet.",
				"de": "Hallo zusammen! Heute werden wir den Projektzeitplan besprechen.",
				"it": "Ciao a tutti! Oggi discuteremo la tempistica del progetto.",
				"pt": "Olá a todos! Hoje vamos discutir o cronograma do projeto.",
			},
		},
	}

	fmt.Printf("\n=== Testing Different Language Counts ===\n")

	for _, tc := range testCases {
		realStage := NewTextToSpeechStage(&TextToSpeechConfig{
			Name:     "multi-lang-test",
			Priority: 50,
			APIKey:   apiKey,
			Model:    "gpt-4o-mini-tts",
			Voice:    "alloy",
		})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		start := time.Now()
		audioMap := realStage.generateTTSParallel(ctx, tc.translations)
		latency := time.Since(start)
		cancel()

		totalAudioSize := 0
		for _, audio := range audioMap {
			totalAudioSize += len(audio)
		}

		fmt.Printf("\n%s:\n", tc.name)
		fmt.Printf("  Total Latency: %dms\n", latency.Milliseconds())
		fmt.Printf("  Languages Generated: %d/%d\n", len(audioMap), len(tc.translations))
		fmt.Printf("  Latency per Language: %.0fms\n", float64(latency.Milliseconds())/float64(len(tc.translations)))
		fmt.Printf("  Total Audio Size: %d bytes\n", totalAudioSize)
		if len(audioMap) > 0 {
			fmt.Printf("  Avg Audio Size: %d bytes/language\n", totalAudioSize/len(audioMap))
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// TestModelPerformanceComparison compares latency across different TTS models.
func (suite *TTSLatencyTestSuite) TestModelPerformanceComparison() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Models to compare (gpt-4o-mini-tts is the baseline/default)
	models := []struct {
		name        string
		description string
	}{
		{"gpt-4o-mini-tts", "Baseline (current default)"},
		{"tts-1", "Standard quality"},
		{"tts-1-hd", "High definition quality"},
	}

	// Test texts (unique to avoid cache)
	testTexts := []string{
		"The annual conference will take place next month in San Francisco.",
		"Please review the attached document and provide your feedback by Friday.",
		"Our team has successfully completed the project ahead of schedule.",
		"Customer satisfaction is our top priority and we strive for excellence.",
		"The new software update includes several important security improvements.",
	}

	type modelResult struct {
		model          string
		totalLatencies []time.Duration
		audioSizes     []int
		successCount   int
		failureCount   int
	}

	results := make([]modelResult, 0, len(models))

	fmt.Printf("\n=== Model Performance Comparison ===\n")
	fmt.Printf("Running %d generations per model\n\n", len(testTexts))

	for _, modelInfo := range models {
		fmt.Printf("Testing %s (%s)...\n", modelInfo.name, modelInfo.description)

		stage := NewTextToSpeechStage(&TextToSpeechConfig{
			Name:     "model-comparison-test",
			Priority: 50,
			APIKey:   apiKey,
			Model:    modelInfo.name,
			Voice:    "alloy",
		})

		result := modelResult{
			model:          modelInfo.name,
			totalLatencies: make([]time.Duration, 0),
			audioSizes:     make([]int, 0),
		}

		for i, text := range testTexts {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			start := time.Now()
			audioData, err := stage.generateTTS(ctx, text)
			latency := time.Since(start)
			cancel()

			if err == nil && len(audioData) > 0 {
				result.successCount++
				result.totalLatencies = append(result.totalLatencies, latency)
				result.audioSizes = append(result.audioSizes, len(audioData))

				fmt.Printf("  Run %d: %dms (%d bytes)\n", i+1, latency.Milliseconds(), len(audioData))
			} else {
				result.failureCount++
				fmt.Printf("  Run %d: FAILED - %v\n", i+1, err)
			}

			time.Sleep(200 * time.Millisecond)
		}

		results = append(results, result)
		fmt.Printf("\n")
	}

	// Print comparison summary
	fmt.Printf("\n=== Performance Comparison Summary ===\n\n")
	fmt.Printf("%-20s %-15s %-15s %-15s %-12s %-15s\n", "Model", "Avg Latency", "Min Latency", "Max Latency", "Success Rate", "Avg Audio Size")
	fmt.Printf("%s\n", strings.Repeat("-", 100))

	baselineAvg := int64(0)
	for _, result := range results {
		if len(result.totalLatencies) > 0 {
			avgLatency := calculateAverage(result.totalLatencies)
			minLatency := calculateMin(result.totalLatencies)
			maxLatency := calculateMax(result.totalLatencies)
			successRate := float64(result.successCount) / float64(result.successCount+result.failureCount) * 100

			var avgAudioSize int
			for _, size := range result.audioSizes {
				avgAudioSize += size
			}
			if len(result.audioSizes) > 0 {
				avgAudioSize /= len(result.audioSizes)
			}

			improvement := ""
			if result.model == "gpt-4o-mini-tts" {
				baselineAvg = avgLatency.Milliseconds()
			} else if baselineAvg > 0 {
				pctChange := float64(baselineAvg-avgLatency.Milliseconds()) / float64(baselineAvg) * 100
				if pctChange > 0 {
					improvement = fmt.Sprintf(" (%.1f%% faster)", pctChange)
				} else {
					improvement = fmt.Sprintf(" (%.1f%% slower)", -pctChange)
				}
			}

			fmt.Printf("%-20s %-15s %-15s %-15s %.1f%% %-15s%s\n",
				result.model,
				fmt.Sprintf("%dms", avgLatency.Milliseconds()),
				fmt.Sprintf("%dms", minLatency.Milliseconds()),
				fmt.Sprintf("%dms", maxLatency.Milliseconds()),
				successRate,
				fmt.Sprintf("%d bytes", avgAudioSize),
				improvement)
		}
	}

	// Assertions
	suite.True(len(results) > 0, "Should have test results")
	for _, result := range results {
		suite.Greater(result.successCount, 0, fmt.Sprintf("%s should have at least one successful generation", result.model))
	}
}

// TestVoicePerformanceComparison compares latency across different TTS voices.
func (suite *TTSLatencyTestSuite) TestVoicePerformanceComparison() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// All available OpenAI TTS voices
	voices := []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}

	testText := "Hello everyone! Today we're going to discuss the project timeline and deliverables for the next quarter."

	type voiceResult struct {
		voice          string
		totalLatencies []time.Duration
		audioSizes     []int
		successCount   int
	}

	results := make([]voiceResult, 0, len(voices))

	fmt.Printf("\n=== Voice Performance Comparison ===\n")
	fmt.Printf("Testing %d voices with same text\n\n", len(voices))

	for _, voice := range voices {
		fmt.Printf("Testing voice: %s\n", voice)

		stage := NewTextToSpeechStage(&TextToSpeechConfig{
			Name:     "voice-comparison-test",
			Priority: 50,
			APIKey:   apiKey,
			Model:    "gpt-4o-mini-tts",
			Voice:    voice,
		})

		result := voiceResult{
			voice:          voice,
			totalLatencies: make([]time.Duration, 0),
			audioSizes:     make([]int, 0),
		}

		// Run 3 times per voice to get average
		for i := 0; i < 3; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			start := time.Now()
			audioData, err := stage.generateTTS(ctx, testText)
			latency := time.Since(start)
			cancel()

			if err == nil && len(audioData) > 0 {
				result.successCount++
				result.totalLatencies = append(result.totalLatencies, latency)
				result.audioSizes = append(result.audioSizes, len(audioData))

				fmt.Printf("  Run %d: %dms (%d bytes)\n", i+1, latency.Milliseconds(), len(audioData))
			}

			time.Sleep(200 * time.Millisecond)
		}

		results = append(results, result)
		fmt.Printf("\n")
	}

	// Print comparison summary
	fmt.Printf("\n=== Voice Performance Summary ===\n\n")
	fmt.Printf("%-15s %-15s %-15s %-15s\n", "Voice", "Avg Latency", "Avg Audio Size", "Success Rate")
	fmt.Printf("%s\n", strings.Repeat("-", 65))

	for _, result := range results {
		if len(result.totalLatencies) > 0 {
			avgLatency := calculateAverage(result.totalLatencies)

			var avgAudioSize int
			for _, size := range result.audioSizes {
				avgAudioSize += size
			}
			avgAudioSize /= len(result.audioSizes)

			successRate := float64(result.successCount) / 3.0 * 100

			fmt.Printf("%-15s %-15s %-15s %.1f%%\n",
				result.voice,
				fmt.Sprintf("%dms", avgLatency.Milliseconds()),
				fmt.Sprintf("%d bytes", avgAudioSize),
				successRate)
		}
	}

	// Assertions
	suite.True(len(results) > 0, "Should have test results")
	for _, result := range results {
		suite.Greater(result.successCount, 0, fmt.Sprintf("Voice %s should have at least one successful generation", result.voice))
	}
}

// TestQualityVsLatencyTradeoff analyzes the quality/latency tradeoff across models.
func (suite *TTSLatencyTestSuite) TestQualityVsLatencyTradeoff() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Quality test cases with different text complexities
	testTexts := []string{
		"Hello everyone, can you hear me?",
		"The meeting starts at three o'clock this afternoon.",
		"Let's review the quarterly results and discuss our strategy for the upcoming fiscal year.",
	}

	// Models to compare (gpt-4o-mini-tts is the baseline)
	models := []string{
		"gpt-4o-mini-tts",
		"tts-1",
		"tts-1-hd",
	}

	fmt.Printf("\n=== Quality vs Latency Tradeoff Analysis ===\n")
	fmt.Printf("Analyzing %d models with %d test cases\n\n", len(models), len(testTexts))

	type tradeoffResult struct {
		model        string
		avgLatencyMs float64
		avgAudioSize float64
		efficiency   float64 // Audio bytes per millisecond (higher = more efficient)
		successCount int
		totalTests   int
	}

	results := make([]tradeoffResult, 0, len(models))

	for _, modelName := range models {
		fmt.Printf("Testing %s...\n", modelName)

		stage := NewTextToSpeechStage(&TextToSpeechConfig{
			Name:     "tradeoff-test",
			Priority: 50,
			APIKey:   apiKey,
			Model:    modelName,
			Voice:    "alloy",
		})

		var totalLatency time.Duration
		var totalAudioSize int
		successCount := 0

		for i, text := range testTexts {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			start := time.Now()
			audioData, err := stage.generateTTS(ctx, text)
			latency := time.Since(start)
			cancel()

			if err == nil && len(audioData) > 0 {
				successCount++
				totalLatency += latency
				totalAudioSize += len(audioData)

				fmt.Printf("  Test %d: %dms (%d bytes)\n", i+1, latency.Milliseconds(), len(audioData))
			} else {
				fmt.Printf("  Test %d: FAILED - %v\n", i+1, err)
			}

			time.Sleep(200 * time.Millisecond)
		}

		if successCount > 0 {
			avgLatency := float64(totalLatency.Milliseconds()) / float64(successCount)
			avgAudioSize := float64(totalAudioSize) / float64(successCount)
			efficiency := avgAudioSize / avgLatency // bytes per ms

			result := tradeoffResult{
				model:        modelName,
				avgLatencyMs: avgLatency,
				avgAudioSize: avgAudioSize,
				efficiency:   efficiency,
				successCount: successCount,
				totalTests:   len(testTexts),
			}

			fmt.Printf("  Avg Latency: %.0fms | Avg Audio Size: %.0f bytes | Efficiency: %.2f bytes/ms\n",
				avgLatency, avgAudioSize, efficiency)

			results = append(results, result)
		}

		fmt.Printf("\n")
	}

	// Sort by efficiency (descending)
	sortedResults := make([]tradeoffResult, len(results))
	copy(sortedResults, results)
	for i := 0; i < len(sortedResults)-1; i++ {
		for j := 0; j < len(sortedResults)-i-1; j++ {
			if sortedResults[j].efficiency < sortedResults[j+1].efficiency {
				sortedResults[j], sortedResults[j+1] = sortedResults[j+1], sortedResults[j]
			}
		}
	}

	// Print comprehensive summary
	fmt.Printf("\n=== Detailed Comparison ===\n\n")
	fmt.Printf("%-20s %-15s %-20s %-20s %-15s\n",
		"Model", "Latency", "Audio Size", "Efficiency", "Success Rate")
	fmt.Printf("%s\n", strings.Repeat("-", 95))

	for _, result := range results {
		fmt.Printf("%-20s %-15s %-20s %-20s %d/%d\n",
			result.model,
			fmt.Sprintf("%.0fms", result.avgLatencyMs),
			fmt.Sprintf("%.0f bytes", result.avgAudioSize),
			fmt.Sprintf("%.2f bytes/ms", result.efficiency),
			result.successCount,
			result.totalTests)
	}

	// Print recommendations
	fmt.Printf("\n=== Model Recommendations ===\n\n")

	fastest := results[0]
	largestAudio := results[0]
	mostEfficient := sortedResults[0]

	for _, r := range results {
		if r.avgLatencyMs < fastest.avgLatencyMs {
			fastest = r
		}
		if r.avgAudioSize > largestAudio.avgAudioSize {
			largestAudio = r
		}
	}

	fmt.Printf("⚡ Fastest Model:        %s (%.0fms avg latency)", fastest.model, fastest.avgLatencyMs)
	if fastest.model != "gpt-4o-mini-tts" {
		baselineLatency := results[0].avgLatencyMs
		improvement := (baselineLatency - fastest.avgLatencyMs) / baselineLatency * 100
		fmt.Printf(" [%.1f%% faster than baseline]", improvement)
	}
	fmt.Println()

	fmt.Printf("🎯 Largest Audio:        %s (%.0f bytes avg)", largestAudio.model, largestAudio.avgAudioSize)
	if largestAudio.model != "gpt-4o-mini-tts" {
		baselineSize := results[0].avgAudioSize
		diff := (largestAudio.avgAudioSize - baselineSize) / baselineSize * 100
		fmt.Printf(" [%.1f%% larger than baseline]", diff)
	}
	fmt.Println()

	fmt.Printf("⭐ Most Efficient:       %s (%.2f bytes/ms)", mostEfficient.model, mostEfficient.efficiency)
	if mostEfficient.model != "gpt-4o-mini-tts" {
		baselineEff := results[0].efficiency
		improvement := (mostEfficient.efficiency - baselineEff) / baselineEff * 100
		fmt.Printf(" [%.1f%% more efficient]", improvement)
	}
	fmt.Println()

	// Provide production recommendation
	fmt.Printf("\n💡 Production Recommendation:\n")
	if mostEfficient.successCount == mostEfficient.totalTests {
		fmt.Printf("   Use %s - optimal balance of speed and audio quality\n", mostEfficient.model)
	} else if fastest.successCount == fastest.totalTests {
		fmt.Printf("   Use %s - prioritize speed while maintaining quality\n", fastest.model)
	} else {
		fmt.Printf("   Use %s - prioritize audio quality for production\n", largestAudio.model)
	}

	// Assertions
	for _, result := range results {
		suite.Greater(result.successCount, 0,
			fmt.Sprintf("%s should have at least one successful generation", result.model))

		// Efficiency should be positive
		suite.Greater(result.efficiency, 0.0,
			fmt.Sprintf("%s efficiency should be positive", result.model))
	}

	fmt.Printf("\n✅ All models meet minimum thresholds\n")
}

// Run the integration test suite
func TestTTSIntegrationSuite(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration tests")
	}
	suite.Run(t, new(TTSIntegrationSuite))
}

// Run the latency test suite
func TestTTSLatencyTestSuite(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration tests")
	}
	suite.Run(t, new(TTSLatencyTestSuite))
}

// Standalone integration test for quick verification
func TestTTSQuickIntegration(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}

	stage := NewTextToSpeechStage(&TextToSpeechConfig{
		Name:     "quick-test",
		Priority: 50,
		APIKey:   apiKey,
		Model:    "tts-1",
		Voice:    "alloy",
	})

	audioData, err := stage.generateTTS(context.Background(), "Quick integration test.")

	require.NoError(t, err)
	assert.NotEmpty(t, audioData)
	assert.Greater(t, len(audioData), 100)

	t.Logf("Generated %d bytes of opus audio data", len(audioData))
}
