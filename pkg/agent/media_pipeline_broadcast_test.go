package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// BroadcastStageTestSuite tests the BroadcastStage
type BroadcastStageTestSuite struct {
	suite.Suite
	stage    *BroadcastStage
	mockRoom *MockRoom
	ctx      context.Context
}

func (suite *BroadcastStageTestSuite) SetupTest() {
	suite.ctx = context.Background()
	suite.mockRoom = NewMockRoom()

	suite.stage = NewBroadcastStage("test-broadcast", 20)
}

func (suite *BroadcastStageTestSuite) TestNewBroadcastStage() {
	stage := NewBroadcastStage("broadcast", 20)

	assert.Equal(suite.T(), "broadcast", stage.GetName())
	assert.Equal(suite.T(), 20, stage.GetPriority())
	assert.True(suite.T(), stage.CanProcess(MediaTypeAudio))
	assert.False(suite.T(), stage.CanProcess(MediaTypeVideo))
}

func (suite *BroadcastStageTestSuite) TestAddBroadcastCallback() {
	// Test adding callbacks
	called := false
	callback := func(ctx context.Context, event TranscriptionEvent, participantMetadata, trackID string) error {
		called = true
		return nil
	}

	suite.stage.AddBroadcastCallback(callback)

	// We can't directly test if the callback was added without making fields public
	// But we can test that it gets called during processing
	transcriptionEvent := TranscriptionEvent{
		Type:      "final",
		Text:      "Hello world",
		Language:  "en",
		Timestamp: time.Now(),
		IsFinal:   true,
	}

	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio data"),
		Metadata: map[string]interface{}{
			"transcription_event": transcriptionEvent,
		},
	}

	// Process should call the callback
	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), called, "Callback should have been called")
	assert.True(suite.T(), output.Metadata["broadcast_completed"].(bool))
}

func (suite *BroadcastStageTestSuite) TestProcessWithoutTranscription() {
	// Test with no metadata
	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio data"),
	}

	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), input.Data, output.Data)
	assert.Nil(suite.T(), output.Metadata)

	// Test with metadata but no transcription
	input.Metadata = map[string]interface{}{
		"other_key": "other_value",
	}

	output, err = suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "other_value", output.Metadata["other_key"])
	assert.Nil(suite.T(), output.Metadata["broadcast_completed"])
}

func (suite *BroadcastStageTestSuite) TestProcessWithTranscriptionButNoCallbacks() {
	// Set up transcription metadata
	transcriptionEvent := TranscriptionEvent{
		Type:      "final",
		Text:      "Hello world",
		Language:  "en",
		Timestamp: time.Now(),
		IsFinal:   true,
	}

	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio data"),
		Metadata: map[string]interface{}{
			"transcription_event": transcriptionEvent,
		},
	}

	// Process without callbacks - should complete but not call anything
	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err) // Should not fail the pipeline
	assert.Equal(suite.T(), input.Data, output.Data)
	assert.True(suite.T(), output.Metadata["broadcast_completed"].(bool))

	// Check stats - should show no broadcast attempts since no callbacks
	stats := suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(0), stats.TotalBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.FailedBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.FinalBroadcasts)
}

func (suite *BroadcastStageTestSuite) TestProcessWithPartialTranscription() {
	// Create input with partial transcription - this should NOT be broadcast
	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio data"),
		Metadata: map[string]interface{}{
			"participant_id": "user-123",
			// No transcription_event since partial transcriptions are not saved to metadata
		},
	}

	// Process - should skip broadcasting since no final transcription
	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.Nil(suite.T(), output.Metadata["broadcast_completed"]) // Should not broadcast

	// Check that no broadcast was attempted
	stats := suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(0), stats.TotalBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.PartialBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.FailedBroadcasts)
}

func (suite *BroadcastStageTestSuite) TestCallbackError() {
	// Test callback that returns an error
	callback := func(ctx context.Context, event TranscriptionEvent, participantMetadata, trackID string) error {
		return fmt.Errorf("callback error")
	}

	suite.stage.AddBroadcastCallback(callback)

	transcriptionEvent := TranscriptionEvent{
		Type:      "final",
		Text:      "Hello world",
		Language:  "en",
		Timestamp: time.Now(),
		IsFinal:   true,
	}

	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio data"),
		Metadata: map[string]interface{}{
			"transcription_event": transcriptionEvent,
		},
	}

	// Process should not fail even if callback fails
	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), output.Metadata["broadcast_completed"].(bool))

	// Stats should show a failed broadcast
	stats := suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(1), stats.TotalBroadcasts)
	assert.Equal(suite.T(), uint64(1), stats.FailedBroadcasts)
}

func (suite *BroadcastStageTestSuite) TestGetStats() {
	stats := suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(0), stats.TotalBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.PartialBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.FinalBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.FailedBroadcasts)
	assert.Equal(suite.T(), uint64(0), stats.BytesSent)
	assert.Equal(suite.T(), float64(0), stats.AverageBroadcastMs)
}

func (suite *BroadcastStageTestSuite) TestProcessVideoData() {
	// Should not process video data
	assert.False(suite.T(), suite.stage.CanProcess(MediaTypeVideo))

	input := MediaData{
		Type:      MediaTypeVideo,
		TrackID:   "video-1",
		Timestamp: time.Now(),
		Data:      []byte("video data"),
	}

	// Even though CanProcess returns false, Process should handle gracefully
	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), input.Data, output.Data)
}

func (suite *BroadcastStageTestSuite) TestProcessWithEmptyTranscriptionText() {
	transcriptionEvent := TranscriptionEvent{
		Type:      "final",
		Text:      "", // Empty text
		Language:  "en",
		Timestamp: time.Now(),
		IsFinal:   true,
	}

	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio data"),
		Metadata: map[string]interface{}{
			"transcription_event": transcriptionEvent,
		},
	}

	// Should skip broadcasting for empty text
	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.Nil(suite.T(), output.Metadata["broadcast_completed"])

	// No broadcasts should be attempted
	stats := suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(0), stats.TotalBroadcasts)
}

func (suite *BroadcastStageTestSuite) TestProcessWithCompleteTranscriptionEvent() {
	// Add a callback so broadcasts actually happen
	suite.stage.AddBroadcastCallback(func(ctx context.Context, event TranscriptionEvent, participantMetadata, trackID string) error {
		return nil
	})

	transcriptionEvent := TranscriptionEvent{
		Type:      "final",
		Text:      "Hello world",
		Language:  "en",
		Timestamp: time.Now(),
		IsFinal:   true,
	}

	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio data"),
		Metadata: map[string]interface{}{
			"transcription_event": transcriptionEvent,
		},
	}

	// Should process complete transcription event
	output, err := suite.stage.Process(suite.ctx, input)
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), output.Metadata["broadcast_completed"].(bool))

	// Check that broadcast was attempted
	stats := suite.stage.GetStats()
	assert.Equal(suite.T(), uint64(1), stats.TotalBroadcasts)
}

func TestBroadcastStageSuite(t *testing.T) {
	suite.Run(t, new(BroadcastStageTestSuite))
}
