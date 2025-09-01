package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// TranslationStageTestSuite tests the TranslationStage functionality.
type TranslationStageTestSuite struct {
	suite.Suite
	stage *TranslationStage
}

func (suite *TranslationStageTestSuite) SetupTest() {
	// Create a translation stage for testing
	suite.stage = NewTranslationStage("test-translation", 30, "test-api-key")
}

func (suite *TranslationStageTestSuite) TearDownTest() {
	if suite.stage != nil {
		suite.stage.Disconnect()
	}
}

// TestProcessWithoutTranscription tests processing without transcription data.
func (suite *TranslationStageTestSuite) TestProcessWithoutTranscription() {
	ctx := context.Background()

	// Test with nil metadata
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
	}
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)
	suite.Equal(input, output)

	// Test with metadata but no transcription
	input.Metadata = map[string]interface{}{
		"other_data": "value",
	}
	output, err = suite.stage.Process(ctx, input)
	suite.NoError(err)
	suite.Equal(input, output)
}

// TestProcessWithoutTargetLanguages tests that processing skips when no target languages.
func (suite *TranslationStageTestSuite) TestProcessWithoutTargetLanguages() {
	ctx := context.Background()

	// Create input with transcription but no target languages
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
			// No target_languages in metadata
		},
	}

	// Process should skip translation
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)
	suite.Equal(input, output)

	// Check stats
	stats := suite.stage.GetStats()
	suite.Equal(uint64(1), stats.SkippedSameLanguage)
	suite.Equal(uint64(0), stats.TotalTranslations)
}

// TestProcessWithTargetLanguages tests that processing detects target languages from metadata.
func (suite *TranslationStageTestSuite) TestProcessWithTargetLanguages() {
	ctx := context.Background()

	// Create input with transcription and target languages
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
			"target_languages": []string{"es", "fr"},
		},
	}

	// Process should attempt translation (will fail with test key, but that's OK)
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)

	// Should have passed through the input since translation failed
	suite.Equal(input, output)

	// Check that translation was attempted (TotalTranslations incremented)
	stats := suite.stage.GetStats()
	suite.Equal(uint64(1), stats.TotalTranslations)
	suite.Equal(uint64(1), stats.FailedTranslations)
}

// TestProcessFiltersSameLanguage tests that source language is filtered from target languages.
func (suite *TranslationStageTestSuite) TestProcessFiltersSameLanguage() {
	ctx := context.Background()

	// Create input where target languages include source language
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
			"target_languages": []string{"en", "es", "fr", "en"}, // Include source and duplicates
		},
	}

	// Process should filter out source language and attempt translation
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)

	// Should have passed through since translation fails with test key
	suite.Equal(input, output)

	// Check that translation was attempted for 2 languages (es, fr)
	stats := suite.stage.GetStats()
	suite.Equal(uint64(1), stats.TotalTranslations)
	suite.Equal(uint64(1), stats.FailedTranslations)
}

// TestCanProcess tests the CanProcess method.
func (suite *TranslationStageTestSuite) TestCanProcess() {
	suite.True(suite.stage.CanProcess(MediaTypeAudio))
	suite.False(suite.stage.CanProcess(MediaTypeVideo))
}

// TestStageProperties tests basic stage properties.
func (suite *TranslationStageTestSuite) TestStageProperties() {
	suite.Equal("test-translation", suite.stage.GetName())
	suite.Equal(30, suite.stage.GetPriority())
}

// TestInputValidation tests input validation behavior.
func (suite *TranslationStageTestSuite) TestInputValidation() {
	ctx := context.Background()

	// Test with very large input
	largeText := strings.Repeat("a", MaxInputTextSize+1)
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Text:     largeText,
				Language: "en",
				IsFinal:  true,
			},
			"target_languages": []string{"es"},
		},
	}

	// Should handle gracefully
	output, err := suite.stage.Process(ctx, input)
	suite.NoError(err)
	suite.Equal(input, output) // Should pass through unchanged when translation fails
}

// TestStatisticsTracking tests that statistics are properly tracked.
func (suite *TranslationStageTestSuite) TestStatisticsTracking() {
	stats := suite.stage.GetStats()
	suite.Equal(uint64(0), stats.TotalTranslations)
	suite.Equal(uint64(0), stats.SuccessfulTranslations)
	suite.Equal(uint64(0), stats.FailedTranslations)
}

// TestTranslationCallbacks tests translation callback functionality.
func (suite *TranslationStageTestSuite) TestTranslationCallbacks() {
	callbackInvoked := false

	suite.stage.AddTranslationCallback(func(event TranslationEvent) {
		callbackInvoked = true
	})

	ctx := context.Background()
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Text:     "Hello world",
				Language: "en",
				IsFinal:  true,
			},
			"target_languages": []string{"es"},
		},
	}

	_, err := suite.stage.Process(ctx, input)
	suite.NoError(err)

	// Give callback time to execute
	time.Sleep(50 * time.Millisecond)

	// Callback should not be invoked because translation failed
	suite.False(callbackInvoked)

	// But stats should show translation was attempted
	stats := suite.stage.GetStats()
	suite.Equal(uint64(1), stats.TotalTranslations)
	suite.Equal(uint64(1), stats.FailedTranslations)
}

// TestBeforeTranslationCallbacks tests before translation callback functionality.
func (suite *TranslationStageTestSuite) TestBeforeTranslationCallbacks() {
	callbackInvoked := false
	var receivedData *MediaData

	suite.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		callbackInvoked = true
		receivedData = data

		// Inject target languages via callback
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		data.Metadata["target_languages"] = []string{"es", "fr"}
	})

	ctx := context.Background()
	input := MediaData{
		Type:    MediaTypeAudio,
		TrackID: "track1",
		Metadata: map[string]interface{}{
			"transcription_event": TranscriptionEvent{
				Text:     "Hello world",
				Language: "en",
				IsFinal:  true,
			},
		},
	}

	_, err := suite.stage.Process(ctx, input)
	suite.NoError(err)

	// Callback should have been invoked
	suite.True(callbackInvoked)
	suite.NotNil(receivedData)

	// Metadata should have been modified by callback
	targetLangs, ok := receivedData.Metadata["target_languages"].([]string)
	suite.True(ok)
	suite.Equal([]string{"es", "fr"}, targetLangs)

	// Translation should have been attempted with the injected languages
	stats := suite.stage.GetStats()
	suite.Equal(uint64(1), stats.TotalTranslations)
	suite.Equal(uint64(1), stats.FailedTranslations) // Fails with test API key
}

// Run tests
func TestTranslationStage(t *testing.T) {
	suite.Run(t, new(TranslationStageTestSuite))
}
