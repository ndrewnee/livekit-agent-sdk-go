//go:build integration

package agent

import (
	"context"
	"fmt"
	"os"
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

func TestTranslationIntegration(t *testing.T) {
	suite.Run(t, new(TranslationIntegrationTestSuite))
}
