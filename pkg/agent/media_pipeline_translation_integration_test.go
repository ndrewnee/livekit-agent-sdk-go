//go:build integration

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// TranslationIntegrationTestSuite tests integration with real OpenAI API.
type TranslationIntegrationTestSuite struct {
	suite.Suite
}

// TestRealOpenAIIntegration tests against real OpenAI API if API key is provided.
func (suite *TranslationIntegrationTestSuite) TestRealOpenAIIntegration() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Create stage with real OpenAI endpoint
	realStage := NewTranslationStage(&TranslationConfig{
		Name:     "real-integration-test",
		Priority: 30,
		APIKey:   apiKey,
	})
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

// TestRealOpenAIIntegrationWithCustomModel tests with a custom model.
func (suite *TranslationIntegrationTestSuite) TestRealOpenAIIntegrationWithCustomModel() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Create stage with custom model
	realStage := NewTranslationStage(&TranslationConfig{
		Name:     "real-integration-test",
		Priority: 30,
		APIKey:   apiKey,
		Model:    "gpt-3.5-turbo",
	})
	defer realStage.Disconnect()

	// Verify model is set correctly
	suite.Equal("gpt-3.5-turbo", realStage.config.Model)

	// Add callback to inject target languages
	realStage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "Good morning!",
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	// Process with real API using custom model
	output, err := realStage.Process(ctx, input)
	suite.NoError(err)

	// Check that translation was received
	transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
	suite.True(ok)
	suite.NotEmpty(transcriptionEvent.Translations)

	// Should have Spanish translation
	suite.Contains(transcriptionEvent.Translations, "es")
	suite.NotEmpty(transcriptionEvent.Translations["es"])

	fmt.Printf("Custom model (gpt-3.5-turbo) translation: %+v\n", transcriptionEvent.Translations)
}

// TestAverageLatencyMultipleRuns measures average latency across multiple translation runs.
func (suite *TranslationIntegrationTestSuite) TestAverageLatencyMultipleRuns() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	realStage := NewTranslationStage(&TranslationConfig{
		Name:     "latency-test",
		Priority: 30,
		APIKey:   apiKey,
	})
	defer realStage.Disconnect()

	realStage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es", "fr"}
	})

	const numRuns = 10
	var totalLatencies []time.Duration
	var ttftLatencies []time.Duration
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

	fmt.Printf("\n=== Running %d translation tests ===\n", numRuns)

	for i := 0; i < numRuns; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		input := MediaData{
			Type:    MediaTypeAudio,
			TrackID: fmt.Sprintf("track_%d", i),
			Metadata: map[string]interface{}{
				"transcription_event": TranscriptionEvent{
					Type:     "final",
					Text:     testTexts[i],
					Language: "en",
					IsFinal:  true,
				},
			},
		}

		start := time.Now()
		output, err := realStage.Process(ctx, input)
		latency := time.Since(start)
		cancel()

		if err == nil {
			transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
			if ok && len(transcriptionEvent.Translations) > 0 {
				successCount++
				totalLatencies = append(totalLatencies, latency)

				stats := realStage.GetStats()
				ttftLatencies = append(ttftLatencies, time.Duration(stats.AverageTimeToFirstToken)*time.Millisecond)

				fmt.Printf("Run %d: Total=%dms, TTFT=%.0fms, Translations=%d\n",
					i+1, latency.Milliseconds(), stats.AverageTimeToFirstToken, len(transcriptionEvent.Translations))
			}
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

		fmt.Printf("\n=== Total Latency Statistics ===\n")
		fmt.Printf("Average: %dms\n", avgLatency.Milliseconds())
		fmt.Printf("Min: %dms\n", minLatency.Milliseconds())
		fmt.Printf("Max: %dms\n", maxLatency.Milliseconds())
		fmt.Printf("StdDev: %dms\n", stdDev.Milliseconds())
		fmt.Printf("Success Rate: %d/%d (%.1f%%)\n", successCount, numRuns, float64(successCount)/float64(numRuns)*100)
	}

	if len(ttftLatencies) > 0 {
		avgTTFT := calculateAverage(ttftLatencies)
		minTTFT := calculateMin(ttftLatencies)
		maxTTFT := calculateMax(ttftLatencies)

		fmt.Printf("\n=== Time-to-First-Token Statistics ===\n")
		fmt.Printf("Average: %dms\n", avgTTFT.Milliseconds())
		fmt.Printf("Min: %dms\n", minTTFT.Milliseconds())
		fmt.Printf("Max: %dms\n", maxTTFT.Milliseconds())
	}

	stats := realStage.GetStats()
	fmt.Printf("\n=== Overall Stage Statistics ===\n")
	fmt.Printf("Total Translations: %d\n", stats.TotalTranslations)
	fmt.Printf("Successful: %d\n", stats.SuccessfulTranslations)
	fmt.Printf("Failed: %d\n", stats.FailedTranslations)
	fmt.Printf("Average Latency: %.2fms\n", stats.AverageLatencyMs)
}

// TestLatencyWithDifferentTextLengths measures performance with varying text lengths.
func (suite *TranslationIntegrationTestSuite) TestLatencyWithDifferentTextLengths() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	realStage := NewTranslationStage(&TranslationConfig{
		Name:     "length-test",
		Priority: 30,
		APIKey:   apiKey,
	})
	defer realStage.Disconnect()

	realStage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
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

		input := MediaData{
			Type:    MediaTypeAudio,
			TrackID: "length-test",
			Metadata: map[string]interface{}{
				"transcription_event": TranscriptionEvent{
					Type:     "final",
					Text:     tc.text,
					Language: "en",
					IsFinal:  true,
				},
			},
		}

		start := time.Now()
		output, err := realStage.Process(ctx, input)
		latency := time.Since(start)
		cancel()

		if err == nil {
			transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
			stats := realStage.GetStats()

			fmt.Printf("\n%s:\n", tc.name)
			fmt.Printf("  Total Latency: %dms\n", latency.Milliseconds())
			fmt.Printf("  TTFT: %.0fms\n", stats.AverageTimeToFirstToken)
			if ok {
				fmt.Printf("  Translation Length: %d chars\n", len(transcriptionEvent.Translations["es"]))
			}
		} else {
			fmt.Printf("\n%s: FAILED - %v\n", tc.name, err)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// TestLatencyWithMultipleLanguages measures performance with different numbers of target languages.
func (suite *TranslationIntegrationTestSuite) TestLatencyWithMultipleLanguages() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	testCases := []struct {
		name      string
		languages []string
	}{
		{
			name:      "Single Language (Spanish)",
			languages: []string{"es"},
		},
		{
			name:      "Two Languages (Spanish, French)",
			languages: []string{"es", "fr"},
		},
		{
			name:      "Five Languages",
			languages: []string{"es", "fr", "de", "it", "pt"},
		},
	}

	testText := "Hello everyone! Today we're going to discuss the project timeline and deliverables."

	fmt.Printf("\n=== Testing Different Language Counts ===\n")

	for _, tc := range testCases {
		realStage := NewTranslationStage(&TranslationConfig{
			Name:     "multi-lang-test",
			Priority: 30,
			APIKey:   apiKey,
		})

		realStage.AddBeforeTranslationCallback(func(data *MediaData) {
			if data.Metadata == nil {
				data.Metadata = make(map[string]interface{})
			}
			data.Metadata["target_languages"] = tc.languages
		})

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

		input := MediaData{
			Type:    MediaTypeAudio,
			TrackID: "multi-lang-test",
			Metadata: map[string]interface{}{
				"transcription_event": TranscriptionEvent{
					Type:     "final",
					Text:     testText,
					Language: "en",
					IsFinal:  true,
				},
			},
		}

		start := time.Now()
		output, err := realStage.Process(ctx, input)
		latency := time.Since(start)
		cancel()

		if err == nil {
			transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
			stats := realStage.GetStats()

			fmt.Printf("\n%s:\n", tc.name)
			fmt.Printf("  Total Latency: %dms\n", latency.Milliseconds())
			fmt.Printf("  TTFT: %.0fms\n", stats.AverageTimeToFirstToken)
			fmt.Printf("  Latency per Language: %.0fms\n", float64(latency.Milliseconds())/float64(len(tc.languages)))
			if ok {
				fmt.Printf("  Languages Received: %d/%d\n", len(transcriptionEvent.Translations), len(tc.languages))
			}
		} else {
			fmt.Printf("\n%s: FAILED - %v\n", tc.name, err)
		}

		realStage.Disconnect()
		time.Sleep(100 * time.Millisecond)
	}
}

// Helper functions for statistics calculation
func calculateAverage(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var total time.Duration
	for _, d := range durations {
		total += d
	}
	return total / time.Duration(len(durations))
}

func calculateMin(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	min := durations[0]
	for _, d := range durations[1:] {
		if d < min {
			min = d
		}
	}
	return min
}

func calculateMax(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	max := durations[0]
	for _, d := range durations[1:] {
		if d > max {
			max = d
		}
	}
	return max
}

func calculateStdDev(durations []time.Duration, avg time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	var sumSquares float64
	avgMs := float64(avg.Milliseconds())
	for _, d := range durations {
		diff := float64(d.Milliseconds()) - avgMs
		sumSquares += diff * diff
	}
	variance := sumSquares / float64(len(durations))
	return time.Duration(int64(variance)) * time.Millisecond
}

// TestModelPerformanceComparison compares latency across different OpenAI models.
func (suite *TranslationIntegrationTestSuite) TestModelPerformanceComparison() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Models to compare
	models := []struct {
		name        string
		description string
	}{
		{"gpt-4o-mini", "Current default (baseline)"},
		{"gpt-4.1-nano", "Fastest/cheapest GPT-4.1"},
		{"gpt-4.1-mini", "Balanced GPT-4.1"},
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
		model              string
		totalLatencies     []time.Duration
		ttftLatencies      []time.Duration
		successCount       int
		failureCount       int
		translationSamples []string
	}

	results := make([]modelResult, 0, len(models))

	fmt.Printf("\n=== Model Performance Comparison ===\n")
	fmt.Printf("Running %d translations per model\n\n", len(testTexts))

	for _, modelInfo := range models {
		fmt.Printf("Testing %s (%s)...\n", modelInfo.name, modelInfo.description)

		stage := NewTranslationStage(&TranslationConfig{
			Name:     "model-comparison-test",
			Priority: 30,
			APIKey:   apiKey,
			Model:    modelInfo.name,
		})

		stage.AddBeforeTranslationCallback(func(data *MediaData) {
			if data.Metadata == nil {
				data.Metadata = make(map[string]interface{})
			}
			data.Metadata["target_languages"] = []string{"es", "fr"}
		})

		result := modelResult{
			model:              modelInfo.name,
			totalLatencies:     make([]time.Duration, 0),
			ttftLatencies:      make([]time.Duration, 0),
			translationSamples: make([]string, 0),
		}

		for i, text := range testTexts {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			input := MediaData{
				Type:    MediaTypeAudio,
				TrackID: fmt.Sprintf("track_%s_%d", modelInfo.name, i),
				Metadata: map[string]interface{}{
					"transcription_event": TranscriptionEvent{
						Type:     "final",
						Text:     text,
						Language: "en",
						IsFinal:  true,
					},
				},
			}

			start := time.Now()
			output, err := stage.Process(ctx, input)
			latency := time.Since(start)
			cancel()

			if err == nil {
				transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
				if ok && len(transcriptionEvent.Translations) > 0 {
					result.successCount++
					result.totalLatencies = append(result.totalLatencies, latency)

					stats := stage.GetStats()
					result.ttftLatencies = append(result.ttftLatencies, time.Duration(stats.AverageTimeToFirstToken)*time.Millisecond)

					// Store first translation sample
					if len(result.translationSamples) == 0 && len(transcriptionEvent.Translations["es"]) > 0 {
						result.translationSamples = append(result.translationSamples, transcriptionEvent.Translations["es"])
					}

					fmt.Printf("  Run %d: %dms (TTFT: %.0fms)\n", i+1, latency.Milliseconds(), stats.AverageTimeToFirstToken)
				} else {
					result.failureCount++
					fmt.Printf("  Run %d: FAILED - no translations\n", i+1)
				}
			} else {
				result.failureCount++
				fmt.Printf("  Run %d: FAILED - %v\n", i+1, err)
			}

			time.Sleep(200 * time.Millisecond)
		}

		stage.Disconnect()
		results = append(results, result)
		fmt.Printf("\n")
	}

	// Print comparison summary
	fmt.Printf("\n=== Performance Comparison Summary ===\n\n")
	fmt.Printf("%-20s %-15s %-15s %-15s %-12s\n", "Model", "Avg Latency", "Min Latency", "Max Latency", "Success Rate")
	fmt.Printf("%s\n", strings.Repeat("-", 80))

	baselineAvg := int64(0)
	for _, result := range results {
		if len(result.totalLatencies) > 0 {
			avgLatency := calculateAverage(result.totalLatencies)
			minLatency := calculateMin(result.totalLatencies)
			maxLatency := calculateMax(result.totalLatencies)
			successRate := float64(result.successCount) / float64(result.successCount+result.failureCount) * 100

			improvement := ""
			if result.model == "gpt-4o-mini" {
				baselineAvg = avgLatency.Milliseconds()
			} else if baselineAvg > 0 {
				pctChange := float64(baselineAvg-avgLatency.Milliseconds()) / float64(baselineAvg) * 100
				if pctChange > 0 {
					improvement = fmt.Sprintf(" (%.1f%% faster)", pctChange)
				} else {
					improvement = fmt.Sprintf(" (%.1f%% slower)", -pctChange)
				}
			}

			fmt.Printf("%-20s %-15s %-15s %-15s %.1f%%%s\n",
				result.model,
				fmt.Sprintf("%dms", avgLatency.Milliseconds()),
				fmt.Sprintf("%dms", minLatency.Milliseconds()),
				fmt.Sprintf("%dms", maxLatency.Milliseconds()),
				successRate,
				improvement)
		}
	}

	fmt.Printf("\n=== TTFT Comparison ===\n\n")
	fmt.Printf("%-20s %-15s %-15s %-15s\n", "Model", "Avg TTFT", "Min TTFT", "Max TTFT")
	fmt.Printf("%s\n", strings.Repeat("-", 65))

	for _, result := range results {
		if len(result.ttftLatencies) > 0 {
			avgTTFT := calculateAverage(result.ttftLatencies)
			minTTFT := calculateMin(result.ttftLatencies)
			maxTTFT := calculateMax(result.ttftLatencies)

			fmt.Printf("%-20s %-15s %-15s %-15s\n",
				result.model,
				fmt.Sprintf("%dms", avgTTFT.Milliseconds()),
				fmt.Sprintf("%dms", minTTFT.Milliseconds()),
				fmt.Sprintf("%dms", maxTTFT.Milliseconds()))
		}
	}

	// Print translation quality samples
	fmt.Printf("\n=== Translation Quality Samples ===\n")
	fmt.Printf("English: \"%s\"\n\n", testTexts[0])
	for _, result := range results {
		if len(result.translationSamples) > 0 {
			fmt.Printf("%-20s: \"%s\"\n", result.model, result.translationSamples[0])
		}
	}

	// Assertions
	suite.True(len(results) > 0, "Should have test results")
	for _, result := range results {
		suite.Greater(result.successCount, 0, fmt.Sprintf("%s should have at least one successful translation", result.model))
	}
}

// TestWarmupImpact tests the impact of warm-up on first translation latency.
func (suite *TranslationIntegrationTestSuite) TestWarmupImpact() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	fmt.Printf("\n=== Testing Warm-up Impact ===\n\n")

	// Test WITHOUT warm-up (cold start)
	fmt.Printf("Test 1: WITHOUT warm-up (cold start)\n")
	stageNoWarmup := NewTranslationStage(&TranslationConfig{
		Name:         "no-warmup-test",
		Priority:     30,
		APIKey:       apiKey,
		EnableWarmup: false,
	})
	defer stageNoWarmup.Disconnect()

	stageNoWarmup.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	ctx1, cancel1 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel1()

	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "warmup-test",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Type:     "final",
				Text:     "Hello, this is a test message.",
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	start1 := time.Now()
	_, err1 := stageNoWarmup.Process(ctx1, input)
	latency1 := time.Since(start1)

	suite.NoError(err1)
	fmt.Printf("  First translation latency: %dms\n\n", latency1.Milliseconds())

	// Test WITH warm-up
	fmt.Printf("Test 2: WITH warm-up (pre-warmed)\n")
	stageWithWarmup := NewTranslationStage(&TranslationConfig{
		Name:         "with-warmup-test",
		Priority:     30,
		APIKey:       apiKey,
		EnableWarmup: true,
	})
	defer stageWithWarmup.Disconnect()

	stageWithWarmup.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es"}
	})

	// Wait for warm-up to complete
	fmt.Printf("  Waiting for warm-up to complete...\n")
	time.Sleep(2 * time.Second)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()

	start2 := time.Now()
	_, err2 := stageWithWarmup.Process(ctx2, input)
	latency2 := time.Since(start2)

	suite.NoError(err2)
	fmt.Printf("  First translation latency: %dms\n\n", latency2.Milliseconds())

	// Compare results
	improvement := latency1 - latency2
	improvementPct := float64(improvement) / float64(latency1) * 100

	fmt.Printf("=== Warm-up Impact Summary ===\n")
	fmt.Printf("Cold start (no warm-up):  %dms\n", latency1.Milliseconds())
	fmt.Printf("Pre-warmed (with warm-up): %dms\n", latency2.Milliseconds())
	fmt.Printf("Improvement: %dms (%.1f%% faster)\n", improvement.Milliseconds(), improvementPct)

	// Warm-up should provide noticeable improvement
	if improvement > 0 {
		fmt.Printf("\n✅ Warm-up successfully reduced cold start latency!\n")
	} else {
		fmt.Printf("\n⚠️  Warm-up did not reduce latency (may already be warm from previous tests)\n")
	}
}

func TestTranslationIntegration(t *testing.T) {
	suite.Run(t, new(TranslationIntegrationTestSuite))
}
