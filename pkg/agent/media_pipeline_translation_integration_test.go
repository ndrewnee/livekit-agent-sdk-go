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

func TestTranslationIntegration(t *testing.T) {
	suite.Run(t, new(TranslationIntegrationTestSuite))
}
