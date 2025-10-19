//go:build integration

package agent

import (
	"context"
	"fmt"
	"math"
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

// TestAverageLatencyMultipleRuns measures average latency across multiple translation runs.
func (suite *TranslationIntegrationTestSuite) TestAverageLatencyMultipleRuns() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	realStage := NewTranslationStage(&TranslationConfig{
		Name:         "latency-test",
		Priority:     30,
		APIKey:       apiKey,
		EnableWarmup: true,
	})
	defer realStage.Disconnect()

	// Wait for warm-up to complete
	time.Sleep(2 * time.Second)

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
		Name:         "length-test",
		Priority:     30,
		APIKey:       apiKey,
		EnableWarmup: true,
	})
	defer realStage.Disconnect()

	// Wait for warm-up to complete
	time.Sleep(2 * time.Second)

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
			Name:         "multi-lang-test",
			Priority:     30,
			APIKey:       apiKey,
			EnableWarmup: true,
		})

		// Wait for warm-up to complete
		time.Sleep(2 * time.Second)

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
	return time.Duration(int64(math.Sqrt(variance))) * time.Millisecond
}

// TestModelPerformanceComparison compares latency across different OpenAI models.
func (suite *TranslationIntegrationTestSuite) TestModelPerformanceComparison() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Models to compare (GPT-4 family only - GPT-5 not production-ready)
	models := []struct {
		name        string
		description string
	}{
		{"gpt-4o-mini", "Baseline (original default)"},
		{"gpt-4.1-nano", "Fastest/cheapest GPT-4.1 (recommended)"},
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
			Name:         "model-comparison-test",
			Priority:     30,
			APIKey:       apiKey,
			Model:        modelInfo.name,
			EnableWarmup: true,
		})

		// Wait for warm-up to complete
		time.Sleep(2 * time.Second)

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

// TestTranslationQuality validates translation quality against ground truth.
func (suite *TranslationIntegrationTestSuite) TestTranslationQuality() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Define ground truth test cases with expected translations from speech
	type qualityTestCase struct {
		english         string
		spanishKeywords []string // Keywords that must appear
		frenchKeywords  []string
		germanKeywords  []string
		russianKeywords []string
		validationType  string // "keywords" for speech recognition tolerance
	}

	testCases := []qualityTestCase{
		{
			english:         "um hello everyone can you hear me okay",
			spanishKeywords: []string{"hola", "todos", "escuchar", "bien"},
			frenchKeywords:  []string{"bonjour", "monde", "entendez"},
			germanKeywords:  []string{"hallo", "alle", "hören"},
			russianKeywords: []string{"привет", "всем", "слышите", "меня"},
			validationType:  "keywords", // Speech includes filler words like "um"
		},
		{
			english:         "yeah so the meeting is scheduled for next Tuesday at two pm",
			spanishKeywords: []string{"reunión", "martes", "próximo", "dos"},
			frenchKeywords:  []string{"réunion", "mardi", "prochain"},
			germanKeywords:  []string{"meeting", "besprechung", "dienstag"},
			russianKeywords: []string{"встреча", "вторник", "следующий"},
			validationType:  "keywords",
		},
		{
			english:         "I think we should uh probably discuss this later",
			spanishKeywords: []string{"creo", "deberíamos", "discutir", "tarde"},
			frenchKeywords:  []string{"pense", "devrions", "discuter", "tard"},
			germanKeywords:  []string{"denke", "sollten", "später"},
			russianKeywords: []string{"думаю", "должны", "обсудить", "позже"},
			validationType:  "keywords",
		},
		{
			english:         "okay great thanks for joining today's call",
			spanishKeywords: []string{"gracias", "unirse", "llamada"},
			frenchKeywords:  []string{"merci", "appel", "aujourd'hui"},
			germanKeywords:  []string{"danke", "anruf"},
			russianKeywords: []string{"спасибо", "звонок", "сегодня"},
			validationType:  "keywords",
		},
		{
			english:         "let me share my screen so everyone can see the presentation",
			spanishKeywords: []string{"compartir", "pantalla", "presentación"},
			frenchKeywords:  []string{"partager", "écran", "présentation"},
			germanKeywords:  []string{"bildschirm", "teilen", "präsentation"},
			russianKeywords: []string{"экран", "презентацию", "показать"},
			validationType:  "keywords",
		},
	}

	// Models to test (GPT-4 family only - GPT-5 not production-ready)
	models := []string{
		"gpt-4o-mini",
		"gpt-4.1-nano",
		"gpt-4.1-mini",
	}

	fmt.Printf("\n=== Translation Quality Validation ===\n")
	fmt.Printf("Testing %d models with %d test cases\n\n", len(models), len(testCases))

	type modelQualityResult struct {
		model        string
		correctES    int
		correctFR    int
		correctDE    int
		correctRU    int
		totalTests   int
		qualityScore float64
	}

	results := make([]modelQualityResult, 0, len(models))

	for _, modelName := range models {
		fmt.Printf("Testing %s...\n", modelName)

		stage := NewTranslationStage(&TranslationConfig{
			Name:         "quality-test",
			Priority:     30,
			APIKey:       apiKey,
			Model:        modelName,
			EnableWarmup: true,
		})

		// Wait for warm-up to complete
		time.Sleep(2 * time.Second)

		result := modelQualityResult{
			model:      modelName,
			totalTests: len(testCases) * 4, // 4 languages per test case (es, fr, de, ru)
		}

		for i, tc := range testCases {
			stage.AddBeforeTranslationCallback(func(data *MediaData) {
				if data.Metadata == nil {
					data.Metadata = make(map[string]interface{})
				}
				data.Metadata["target_languages"] = []string{"es", "fr", "de", "ru"}
			})

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			input := MediaData{
				Type:    MediaTypeAudio,
				TrackID: fmt.Sprintf("quality-test-%s-%d", modelName, i),
				Metadata: map[string]interface{}{
					"transcription_event": TranscriptionEvent{
						Type:     "final",
						Text:     tc.english,
						Language: "en",
						IsFinal:  true,
					},
				},
			}

			output, err := stage.Process(ctx, input)
			cancel()

			if err == nil {
				transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
				if ok && len(transcriptionEvent.Translations) > 0 {
					// Validate Spanish
					if esTranslation, exists := transcriptionEvent.Translations["es"]; exists {
						if validateWithKeywords(esTranslation, tc.spanishKeywords) {
							result.correctES++
						}
					}

					// Validate French
					if frTranslation, exists := transcriptionEvent.Translations["fr"]; exists {
						if validateWithKeywords(frTranslation, tc.frenchKeywords) {
							result.correctFR++
						}
					}

					// Validate German
					if deTranslation, exists := transcriptionEvent.Translations["de"]; exists {
						if validateWithKeywords(deTranslation, tc.germanKeywords) {
							result.correctDE++
						}
					}

					// Validate Russian
					if ruTranslation, exists := transcriptionEvent.Translations["ru"]; exists {
						if validateWithKeywords(ruTranslation, tc.russianKeywords) {
							result.correctRU++
						}
					}
				}
			}

			time.Sleep(200 * time.Millisecond)
		}

		// Calculate quality score
		totalCorrect := result.correctES + result.correctFR + result.correctDE + result.correctRU
		result.qualityScore = float64(totalCorrect) / float64(result.totalTests) * 100

		fmt.Printf("  Spanish: %d/%d correct\n", result.correctES, len(testCases))
		fmt.Printf("  French:  %d/%d correct\n", result.correctFR, len(testCases))
		fmt.Printf("  German:  %d/%d correct\n", result.correctDE, len(testCases))
		fmt.Printf("  Russian: %d/%d correct\n", result.correctRU, len(testCases))
		fmt.Printf("  Overall Quality Score: %.1f%%\n\n", result.qualityScore)

		stage.Disconnect()
		results = append(results, result)
	}

	// Print comparison summary
	fmt.Printf("\n=== Quality Comparison Summary ===\n\n")
	fmt.Printf("%-25s %-12s %-12s %-12s %-12s %-15s\n", "Model", "Spanish", "French", "German", "Russian", "Quality Score")
	fmt.Printf("%s\n", strings.Repeat("-", 95))

	for _, result := range results {
		fmt.Printf("%-25s %-12s %-12s %-12s %-12s %.1f%%",
			result.model,
			fmt.Sprintf("%d/%d", result.correctES, len(testCases)),
			fmt.Sprintf("%d/%d", result.correctFR, len(testCases)),
			fmt.Sprintf("%d/%d", result.correctDE, len(testCases)),
			fmt.Sprintf("%d/%d", result.correctRU, len(testCases)),
			result.qualityScore)

		// Show comparison to baseline (gpt-4o-mini)
		if result.model == "gpt-4o-mini" {
			fmt.Printf(" (baseline)")
		} else if len(results) > 0 && results[0].model == "gpt-4o-mini" {
			baseline := results[0].qualityScore
			diff := result.qualityScore - baseline
			if diff > 0 {
				fmt.Printf(" (+%.1f%%)", diff)
			} else if diff < 0 {
				fmt.Printf(" (%.1f%%)", diff)
			}
		}
		fmt.Println()

		// Assert minimum quality threshold
		suite.Greater(result.qualityScore, 70.0, fmt.Sprintf("%s quality score should be above 70%%", result.model))
	}

	fmt.Printf("\n✅ All models meet minimum quality threshold (70%%)\n")
}

// validateWithKeywords checks if translation contains required keywords.
func validateWithKeywords(translation string, keywords []string) bool {
	translation = strings.ToLower(translation)
	// At least 50% of keywords must be present
	matchCount := 0
	for _, keyword := range keywords {
		if strings.Contains(translation, strings.ToLower(keyword)) {
			matchCount++
		}
	}
	return float64(matchCount)/float64(len(keywords)) >= 0.5
}

// TestTranslationQualityRegression tests edge cases to ensure quality hasn't degraded.
func (suite *TranslationIntegrationTestSuite) TestTranslationQualityRegression() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Edge case test scenarios for speech transcription
	type edgeCaseTest struct {
		category      string
		english       string
		requiredTerms map[string][]string // language -> required terms
		description   string
	}

	testCases := []edgeCaseTest{
		{
			category:    "Filler Words and Disfluencies",
			english:     "um so like I was saying uh we need to you know complete this by Friday",
			description: "Speech disfluencies should be handled gracefully",
			requiredTerms: map[string][]string{
				"es": {"necesitamos", "completar", "viernes"},
				"fr": {"devons", "terminer", "vendredi"},
				"ru": {"нужно", "завершить", "пятниц"},
			},
		},
		{
			category:    "Numbers in Speech",
			english:     "the meeting is at three thirty pm on the fifteenth",
			description: "Spoken numbers should be translated correctly",
			requiredTerms: map[string][]string{
				"es": {"reunión", "tres", "treinta", "quince"},
				"fr": {"réunion", "trois", "trente", "quinze"},
				"ru": {"встреча", "три", "тридцать", "пятнадцат"},
			},
		},
		{
			category:    "Incomplete Sentences",
			english:     "so if we could just maybe I don't know start working on",
			description: "Incomplete speech should be translated as-is",
			requiredTerms: map[string][]string{
				"es": {"podríamos", "trabajar"},
				"fr": {"pourrions", "travailler"},
				"ru": {"могли", "работать"},
			},
		},
		{
			category:    "Repetitions and False Starts",
			english:     "we need to we need to schedule a call with the the client",
			description: "Repetitions in speech should be handled",
			requiredTerms: map[string][]string{
				"es": {"necesitamos", "programar", "llamada", "cliente"},
				"fr": {"devons", "planifier", "appel", "client"},
				"ru": {"нужно", "запланировать", "звонок", "клиент"},
			},
		},
		{
			category:    "Casual Speech Contractions",
			english:     "I'm gonna check that we're all set and we'll get back to you",
			description: "Contractions and casual speech should translate naturally",
			requiredTerms: map[string][]string{
				"es": {"voy", "verificar", "comunicar"},
				"fr": {"vais", "vérifier", "recontacterons"},
				"ru": {"проверю", "связь"},
			},
		},
		{
			category:    "Questions with Filler Words",
			english:     "so um can you hear me is everyone on the call",
			description: "Questions with fillers should preserve meaning",
			requiredTerms: map[string][]string{
				"es": {"pueden", "escuchar", "todos", "llamada"},
				"fr": {"pouvez", "entendre", "tout", "appel"},
				"ru": {"слышите", "все", "звонок"},
			},
		},
		{
			category:    "Technical Terms in Speech",
			english:     "let's deploy the API to production and monitor the uh the metrics",
			description: "Technical terms in casual speech should be preserved",
			requiredTerms: map[string][]string{
				"es": {"api", "producción", "monitorear", "métric"},
				"fr": {"api", "production", "surveiller", "métrique"},
				"ru": {"api", "продакшн", "production", "метрик"},
			},
		},
	}

	// Test with baseline model (gpt-4o-mini) and optimized GPT-4.1 models
	models := []string{
		"gpt-4o-mini",  // Baseline (original default)
		"gpt-4.1-nano", // Current optimized model (recommended)
		"gpt-4.1-mini", // Balanced model
	}

	fmt.Printf("\n=== Translation Quality Regression Testing ===\n")
	fmt.Printf("Testing %d edge case categories with %d models\n\n", len(testCases), len(models))

	type regressionResult struct {
		model       string
		passedTests int
		totalTests  int
		failedCases []string
	}

	results := make([]regressionResult, 0, len(models))

	for _, modelName := range models {
		fmt.Printf("Testing %s...\n", modelName)

		stage := NewTranslationStage(&TranslationConfig{
			Name:         "regression-test",
			Priority:     30,
			APIKey:       apiKey,
			Model:        modelName,
			EnableWarmup: true,
		})

		// Wait for warm-up to complete
		time.Sleep(2 * time.Second)

		result := regressionResult{
			model:       modelName,
			totalTests:  len(testCases),
			failedCases: make([]string, 0),
		}

		for _, tc := range testCases {
			targetLangs := []string{"es", "fr", "ru"}

			stage.AddBeforeTranslationCallback(func(data *MediaData) {
				if data.Metadata == nil {
					data.Metadata = make(map[string]interface{})
				}
				data.Metadata["target_languages"] = targetLangs
			})

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			input := MediaData{
				Type:    MediaTypeAudio,
				TrackID: fmt.Sprintf("regression-%s", tc.category),
				Metadata: map[string]interface{}{
					"transcription_event": TranscriptionEvent{
						Type:     "final",
						Text:     tc.english,
						Language: "en",
						IsFinal:  true,
					},
				},
			}

			output, err := stage.Process(ctx, input)
			cancel()

			testPassed := false
			if err == nil {
				transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent)
				if ok && len(transcriptionEvent.Translations) > 0 {
					// Check if all required terms are present for each language
					allLanguagesPassed := true
					for lang, requiredTerms := range tc.requiredTerms {
						if translation, exists := transcriptionEvent.Translations[lang]; exists {
							if !validateWithKeywords(translation, requiredTerms) {
								allLanguagesPassed = false
								break
							}
						} else {
							allLanguagesPassed = false
							break
						}
					}
					testPassed = allLanguagesPassed
				}
			}

			if testPassed {
				result.passedTests++
				fmt.Printf("  ✅ %s: PASS\n", tc.category)
			} else {
				result.failedCases = append(result.failedCases, tc.category)
				fmt.Printf("  ❌ %s: FAIL - %s\n", tc.category, tc.description)
			}

			time.Sleep(200 * time.Millisecond)
		}

		stage.Disconnect()
		results = append(results, result)
		fmt.Println()
	}

	// Print summary
	fmt.Printf("\n=== Regression Test Summary ===\n\n")
	fmt.Printf("%-30s %-15s %-15s\n", "Model", "Passed", "Pass Rate")
	fmt.Printf("%s\n", strings.Repeat("-", 65))

	for i, result := range results {
		passRate := float64(result.passedTests) / float64(result.totalTests) * 100
		fmt.Printf("%-30s %-15s %.1f%%",
			result.model,
			fmt.Sprintf("%d/%d", result.passedTests, result.totalTests),
			passRate)

		// Show comparison to baseline (gpt-4o-mini)
		if result.model == "gpt-4o-mini" {
			fmt.Printf(" (baseline)")
		} else if i > 0 && results[0].model == "gpt-4o-mini" {
			baselineRate := float64(results[0].passedTests) / float64(results[0].totalTests) * 100
			diff := passRate - baselineRate
			if diff > 0 {
				fmt.Printf(" (+%.1f%%)", diff)
			} else if diff < 0 {
				fmt.Printf(" (%.1f%%)", diff)
			}
		}
		fmt.Println()

		if len(result.failedCases) > 0 {
			fmt.Printf("  Failed: %s\n", strings.Join(result.failedCases, ", "))
		}

		// Assert minimum regression threshold (at least 80% pass rate)
		suite.Greater(passRate, 80.0, fmt.Sprintf("%s should pass at least 80%% of regression tests", result.model))
	}

	fmt.Printf("\n✅ All models meet regression quality threshold\n")
}

// TestQualityVsLatencyTradeoff analyzes the quality/latency tradeoff across models.
func (suite *TranslationIntegrationTestSuite) TestQualityVsLatencyTradeoff() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	// Quality test cases from speech transcription (fewer for speed)
	type qualityTest struct {
		english         string
		spanishKeywords []string
		frenchKeywords  []string
		russianKeywords []string
	}

	testCases := []qualityTest{
		{
			english:         "um hello everyone can you hear me",
			spanishKeywords: []string{"hola", "todos", "escuchar"},
			frenchKeywords:  []string{"bonjour", "monde", "entendre"},
			russianKeywords: []string{"привет", "всем", "слышите"},
		},
		{
			english:         "the meeting starts at three o'clock this afternoon",
			spanishKeywords: []string{"reunión", "tres", "tarde"},
			frenchKeywords:  []string{"réunion", "trois", "après-midi"},
			russianKeywords: []string{"встреча", "три", "после"},
		},
		{
			english:         "okay so let's uh review the quarterly results",
			spanishKeywords: []string{"revisar", "resultados", "trimestre"},
			frenchKeywords:  []string{"réviser", "résultats", "trimestre"},
			russianKeywords: []string{"рассмотреть", "результат", "квартал"},
		},
	}

	// GPT-4 models to compare (GPT-5 not production-ready)
	models := []string{
		"gpt-4o-mini",
		"gpt-4.1-nano",
		"gpt-4.1-mini",
	}

	fmt.Printf("\n=== Quality vs Latency Tradeoff Analysis ===\n")
	fmt.Printf("Analyzing %d models with %d test cases\n\n", len(models), len(testCases))

	type tradeoffResult struct {
		model               string
		avgLatencyMs        float64
		avgTTFTMs           float64
		qualityScore        float64
		qualityPerMs        float64 // Quality score per millisecond (efficiency metric)
		correctTranslations int
		totalTranslations   int
	}

	results := make([]tradeoffResult, 0, len(models))

	for _, modelName := range models {
		fmt.Printf("Testing %s...\n", modelName)

		stage := NewTranslationStage(&TranslationConfig{
			Name:         "tradeoff-test",
			Priority:     30,
			APIKey:       apiKey,
			Model:        modelName,
			EnableWarmup: true,
		})

		// Wait for warm-up to complete
		time.Sleep(2 * time.Second)

		var totalLatency time.Duration
		correctCount := 0
		totalTranslations := len(testCases) * 3 // Spanish + French + Russian
		testCount := 0

		for i, tc := range testCases {
			stage.AddBeforeTranslationCallback(func(data *MediaData) {
				if data.Metadata == nil {
					data.Metadata = make(map[string]interface{})
				}
				data.Metadata["target_languages"] = []string{"es", "fr", "ru"}
			})

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

			input := MediaData{
				Type:    MediaTypeAudio,
				TrackID: fmt.Sprintf("tradeoff-%s-%d", modelName, i),
				Metadata: map[string]interface{}{
					"transcription_event": TranscriptionEvent{
						Type:     "final",
						Text:     tc.english,
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
					testCount++
					totalLatency += latency

					// Check Spanish quality
					if esTranslation, exists := transcriptionEvent.Translations["es"]; exists {
						if validateWithKeywords(esTranslation, tc.spanishKeywords) {
							correctCount++
						}
					}

					// Check French quality
					if frTranslation, exists := transcriptionEvent.Translations["fr"]; exists {
						if validateWithKeywords(frTranslation, tc.frenchKeywords) {
							correctCount++
						}
					}

					// Check Russian quality
					if ruTranslation, exists := transcriptionEvent.Translations["ru"]; exists {
						if validateWithKeywords(ruTranslation, tc.russianKeywords) {
							correctCount++
						}
					}
				}
			}

			time.Sleep(200 * time.Millisecond)
		}

		// Calculate metrics
		avgLatency := float64(totalLatency.Milliseconds()) / float64(testCount)
		qualityScore := float64(correctCount) / float64(totalTranslations) * 100

		// Get TTFT from stage stats
		stats := stage.GetStats()
		avgTTFT := stats.AverageTimeToFirstToken

		// Calculate efficiency: quality per millisecond
		qualityPerMs := qualityScore / avgLatency

		result := tradeoffResult{
			model:               modelName,
			avgLatencyMs:        avgLatency,
			avgTTFTMs:           avgTTFT,
			qualityScore:        qualityScore,
			qualityPerMs:        qualityPerMs,
			correctTranslations: correctCount,
			totalTranslations:   totalTranslations,
		}

		fmt.Printf("  Latency: %.0fms | Quality: %.1f%% | Efficiency: %.3f\n",
			avgLatency, qualityScore, qualityPerMs)

		stage.Disconnect()
		results = append(results, result)
	}

	// Sort by efficiency (quality per ms) to find optimal model
	sortedResults := make([]tradeoffResult, len(results))
	copy(sortedResults, results)

	// Simple bubble sort by qualityPerMs (descending)
	for i := 0; i < len(sortedResults)-1; i++ {
		for j := 0; j < len(sortedResults)-i-1; j++ {
			if sortedResults[j].qualityPerMs < sortedResults[j+1].qualityPerMs {
				sortedResults[j], sortedResults[j+1] = sortedResults[j+1], sortedResults[j]
			}
		}
	}

	// Print comprehensive summary
	fmt.Printf("\n=== Detailed Comparison ===\n\n")
	fmt.Printf("%-15s %-12s %-12s %-12s %-12s %-15s\n",
		"Model", "Latency", "TTFT", "Quality", "Accuracy", "Efficiency")
	fmt.Printf("%s\n", strings.Repeat("-", 80))

	for _, result := range results {
		fmt.Printf("%-15s %-12s %-12s %-12s %-15s %.4f\n",
			result.model,
			fmt.Sprintf("%.0fms", result.avgLatencyMs),
			fmt.Sprintf("%.0fms", result.avgTTFTMs),
			fmt.Sprintf("%.1f%%", result.qualityScore),
			fmt.Sprintf("%d/%d", result.correctTranslations, result.totalTranslations),
			result.qualityPerMs)
	}

	// Print recommendations
	fmt.Printf("\n=== Model Recommendations ===\n\n")

	fastest := results[0]
	bestQuality := results[0]
	mostEfficient := sortedResults[0]

	for _, r := range results {
		if r.avgLatencyMs < fastest.avgLatencyMs {
			fastest = r
		}
		if r.qualityScore > bestQuality.qualityScore {
			bestQuality = r
		}
	}

	fmt.Printf("⚡ Fastest Model:        %s (%.0fms avg latency)", fastest.model, fastest.avgLatencyMs)
	if fastest.model != "gpt-4o-mini" && results[0].model == "gpt-4o-mini" {
		improvement := (results[0].avgLatencyMs - fastest.avgLatencyMs) / results[0].avgLatencyMs * 100
		fmt.Printf(" [%.1f%% faster than baseline]", improvement)
	}
	fmt.Println()

	fmt.Printf("🎯 Best Quality:         %s (%.1f%% quality score)", bestQuality.model, bestQuality.qualityScore)
	if bestQuality.model != "gpt-4o-mini" && results[0].model == "gpt-4o-mini" {
		diff := bestQuality.qualityScore - results[0].qualityScore
		if diff > 0 {
			fmt.Printf(" [+%.1f%% vs baseline]", diff)
		}
	}
	fmt.Println()

	fmt.Printf("⭐ Most Efficient:       %s (%.4f quality/ms)", mostEfficient.model, mostEfficient.qualityPerMs)
	if mostEfficient.model != "gpt-4o-mini" && results[0].model == "gpt-4o-mini" {
		baselineEff := results[0].qualityPerMs
		improvement := (mostEfficient.qualityPerMs - baselineEff) / baselineEff * 100
		fmt.Printf(" [%.1f%% more efficient]", improvement)
	}
	fmt.Println()

	// Provide production recommendation
	fmt.Printf("\n💡 Production Recommendation:\n")
	if mostEfficient.qualityScore >= 80.0 {
		fmt.Printf("   Use %s - optimal balance of speed and quality\n", mostEfficient.model)
	} else if fastest.qualityScore >= 80.0 {
		fmt.Printf("   Use %s - prioritize speed while maintaining quality\n", fastest.model)
	} else {
		fmt.Printf("   Use %s - prioritize quality for production\n", bestQuality.model)
	}

	// Assertions
	for _, result := range results {
		// All models should meet minimum quality threshold
		suite.Greater(result.qualityScore, 60.0,
			fmt.Sprintf("%s should maintain at least 60%% quality", result.model))

		// Efficiency should be reasonable (quality per ms should be positive)
		suite.Greater(result.qualityPerMs, 0.0,
			fmt.Sprintf("%s efficiency should be positive", result.model))
	}

	fmt.Printf("\n✅ All models meet minimum thresholds\n")
}

func TestTranslationIntegration(t *testing.T) {
	suite.Run(t, new(TranslationIntegrationTestSuite))
}
