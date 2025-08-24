package agent

import (
	"context"
	"testing"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
)

// TestBaseHandlerMethods tests all methods of BaseHandler
func TestBaseHandlerMethods(t *testing.T) {
	handler := &BaseHandler{}
	ctx := context.Background()

	// Test job-related methods
	t.Run("OnJobRequest", func(t *testing.T) {
		accept, metadata := handler.OnJobRequest(ctx, &livekit.Job{})
		assert.True(t, accept)
		assert.NotNil(t, metadata)
	})

	t.Run("OnJobAssigned", func(t *testing.T) {
		// Create a cancellable context for testing
		testCtx, cancel := context.WithCancel(ctx)
		cancel() // Cancel immediately so the handler doesn't block

		err := handler.OnJobAssigned(testCtx, &JobContext{})
		assert.NoError(t, err)
	})

	t.Run("OnJobTerminated", func(t *testing.T) {
		handler.OnJobTerminated(ctx, "job-1")
		// No assertions needed for no-op method
	})

	t.Run("GetJobMetadata", func(t *testing.T) {
		metadata := handler.GetJobMetadata(&livekit.Job{})
		assert.NotNil(t, metadata)
	})

	// Test room-related methods
	t.Run("OnRoomConnected", func(t *testing.T) {
		handler.OnRoomConnected(ctx, nil)
		// No assertions needed for no-op method
	})

	t.Run("OnRoomDisconnected", func(t *testing.T) {
		handler.OnRoomDisconnected(ctx, nil, "test reason")
		// No assertions needed for no-op method
	})

	t.Run("OnRoomMetadataChanged", func(t *testing.T) {
		handler.OnRoomMetadataChanged(ctx, "old-metadata", "new-metadata")
		// No assertions needed for no-op method
	})

	// Test participant-related methods (using nil since they're no-op)
	t.Run("OnParticipantJoined", func(t *testing.T) {
		handler.OnParticipantJoined(ctx, nil)
		// No assertions needed for no-op method
	})

	t.Run("OnParticipantLeft", func(t *testing.T) {
		handler.OnParticipantLeft(ctx, nil)
		// No assertions needed for no-op method
	})

	t.Run("OnParticipantMetadataChanged", func(t *testing.T) {
		handler.OnParticipantMetadataChanged(ctx, nil, "old-metadata")
		// No assertions needed for no-op method
	})

	t.Run("OnParticipantSpeakingChanged", func(t *testing.T) {
		handler.OnParticipantSpeakingChanged(ctx, nil, true)
		// No assertions needed for no-op method
	})

	// Test track-related methods (using nil since they're no-op)
	t.Run("OnTrackPublished", func(t *testing.T) {
		handler.OnTrackPublished(ctx, nil, nil)
		// No assertions needed for no-op method
	})

	t.Run("OnTrackUnpublished", func(t *testing.T) {
		handler.OnTrackUnpublished(ctx, nil, nil)
		// No assertions needed for no-op method
	})

	t.Run("OnTrackSubscribed", func(t *testing.T) {
		handler.OnTrackSubscribed(ctx, nil, nil, nil)
		// No assertions needed for no-op method
	})

	t.Run("OnTrackUnsubscribed", func(t *testing.T) {
		handler.OnTrackUnsubscribed(ctx, nil, nil, nil)
		// No assertions needed for no-op method
	})

	// Test data and quality methods
	t.Run("OnDataReceived", func(t *testing.T) {
		handler.OnDataReceived(ctx, []byte("test-data"), nil, livekit.DataPacket_RELIABLE)
		// No assertions needed for no-op method
	})

	t.Run("OnConnectionQualityChanged", func(t *testing.T) {
		handler.OnConnectionQualityChanged(ctx, nil, livekit.ConnectionQuality_EXCELLENT)
		// No assertions needed for no-op method
	})

	t.Run("OnActiveSpeakersChanged", func(t *testing.T) {
		handler.OnActiveSpeakersChanged(ctx, nil)
		// No assertions needed for no-op method
	})
}

// TestCustomHandler tests a custom handler implementation
func TestCustomHandler(t *testing.T) {
	// Create a custom handler type that embeds BaseHandler
	type CustomHandler struct {
		BaseHandler
		jobRequestCalledFlag bool
	}

	handler := &CustomHandler{}

	// Since BaseHandler methods can't be directly overridden,
	// we test that the base methods work correctly
	ctx := context.Background()

	// Test that base methods work
	accept, metadata := handler.OnJobRequest(ctx, &livekit.Job{})
	assert.True(t, accept) // BaseHandler accepts all jobs by default
	assert.NotNil(t, metadata)

	// Test OnJobAssigned with cancelled context
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	err := handler.OnJobAssigned(cancelCtx, &JobContext{})
	assert.NoError(t, err)

	// Test OnJobTerminated (no-op)
	handler.OnJobTerminated(ctx, "job-1")
}

// MockTrackPublication implements enough of RemoteTrackPublication for testing
type MockTrackPublication struct {
	sid        string
	subscribed bool
}

func (m *MockTrackPublication) SID() string {
	return m.sid
}

func (m *MockTrackPublication) SetSubscribed(subscribed bool) error {
	m.subscribed = subscribed
	return nil
}

func (m *MockTrackPublication) IsSubscribed() bool {
	return m.subscribed
}

// Implement remaining required methods with minimal implementation
func (m *MockTrackPublication) Kind() livekit.TrackType {
	return livekit.TrackType_VIDEO
}

func (m *MockTrackPublication) Name() string {
	return "test-track"
}

func (m *MockTrackPublication) Source() livekit.TrackSource {
	return livekit.TrackSource_CAMERA
}

func (m *MockTrackPublication) Muted() bool {
	return false
}

func (m *MockTrackPublication) Width() int {
	return 1920
}

func (m *MockTrackPublication) Height() int {
	return 1080
}

func (m *MockTrackPublication) FPS() int {
	return 30
}

func (m *MockTrackPublication) MimeType() string {
	return "video/vp8"
}

// TestHandlerWithMockTrack tests handler with mock track
func TestHandlerWithMockTrack(t *testing.T) {
	handler := &BaseHandler{}
	ctx := context.Background()

	// Create mock track
	mockTrack := &MockTrackPublication{
		sid:        "track-1",
		subscribed: false,
	}

	// Test track methods with proper interface (even though they're no-op)
	handler.OnTrackPublished(ctx, nil, nil)
	handler.OnTrackUnpublished(ctx, nil, nil)

	// Verify mock track works
	assert.Equal(t, "track-1", mockTrack.SID())
	assert.False(t, mockTrack.IsSubscribed())

	err := mockTrack.SetSubscribed(true)
	assert.NoError(t, err)
	assert.True(t, mockTrack.IsSubscribed())
}

// TestHandlerWithWebRTCTrack tests handler with WebRTC track
func TestHandlerWithWebRTCTrack(t *testing.T) {
	handler := &BaseHandler{}
	ctx := context.Background()

	// Test with nil track (valid for no-op methods)
	var track *webrtc.TrackRemote = nil
	handler.OnTrackSubscribed(ctx, track, nil, nil)
	handler.OnTrackUnsubscribed(ctx, track, nil, nil)
}

// TestHandlerWithParticipant tests handler with participant
func TestHandlerWithParticipant(t *testing.T) {
	handler := &BaseHandler{}
	ctx := context.Background()

	// Test with nil participant (valid for no-op methods)
	var participant *lksdk.RemoteParticipant = nil
	handler.OnParticipantJoined(ctx, participant)
	handler.OnParticipantLeft(ctx, participant)
	handler.OnParticipantMetadataChanged(ctx, participant, "old")
	handler.OnParticipantSpeakingChanged(ctx, participant, true)
	handler.OnConnectionQualityChanged(ctx, participant, livekit.ConnectionQuality_POOR)
}

// TestHandlerTrackMutedUnmuted tests track muted/unmuted callbacks
func TestHandlerTrackMutedUnmuted(t *testing.T) {
	handler := &BaseHandler{}
	ctx := context.Background()

	// These methods take different interfaces
	var publication lksdk.TrackPublication = nil
	var participant lksdk.Participant = nil

	handler.OnTrackMuted(ctx, publication, participant)
	handler.OnTrackUnmuted(ctx, publication, participant)
}

// TestHandlerDataReceived tests data received callback
func TestHandlerDataReceived(t *testing.T) {
	handler := &BaseHandler{}
	ctx := context.Background()

	testData := []byte("test data packet")
	handler.OnDataReceived(ctx, testData, nil, livekit.DataPacket_RELIABLE)
	handler.OnDataReceived(ctx, testData, nil, livekit.DataPacket_LOSSY)
}

// TestHandlerActiveSpeakers tests active speakers callback
func TestHandlerActiveSpeakers(t *testing.T) {
	handler := &BaseHandler{}
	ctx := context.Background()

	// Test with empty speakers list
	handler.OnActiveSpeakersChanged(ctx, []lksdk.Participant{})

	// Test with nil speakers list
	handler.OnActiveSpeakersChanged(ctx, nil)
}
