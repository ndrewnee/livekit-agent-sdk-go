// +build integration,livekit

package egress

import (
	"context"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEgressHandlerJobAcceptance tests that the egress handler accepts room recording jobs
func TestEgressHandlerJobAcceptance(t *testing.T) {
	config := DefaultConfig()
	handler := NewEgressHandler(config)

	tests := []struct {
		name           string
		job            *livekit.Job
		expectAccept   bool
		expectIdentity string
	}{
		{
			name: "accepts room recording job",
			job: &livekit.Job{
				Id:   "test-job-1",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{
					Name: "test-room",
				},
			},
			expectAccept:   true,
			expectIdentity: "egress-agent-test-job-1",
		},
		{
			name: "rejects non-room job",
			job: &livekit.Job{
				Id:   "test-job-2",
				Type: livekit.JobType(999), // Invalid job type
			},
			expectAccept: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			accepted, metadata := handler.OnJobRequest(ctx, tt.job)

			assert.Equal(t, tt.expectAccept, accepted)
			if tt.expectAccept {
				require.NotNil(t, metadata)
				assert.Equal(t, tt.expectIdentity, metadata.ParticipantIdentity)
				assert.Equal(t, "HLS Egress Agent", metadata.ParticipantName)
			} else {
				assert.Nil(t, metadata)
			}
		})
	}

	// Check statistics
	stats := handler.GetStats()
	assert.Equal(t, int64(2), stats.TotalJobs)
	assert.Equal(t, int64(1), stats.AcceptedJobs)
	assert.Equal(t, int64(1), stats.RejectedJobs)
}

// TestEgressHandlerCapacityLimit tests concurrent session limits
func TestEgressHandlerCapacityLimit(t *testing.T) {
	config := DefaultConfig()
	config.MaxConcurrentSessions = 2
	handler := NewEgressHandler(config)

	ctx := context.Background()

	// Accept first two jobs
	for i := 0; i < 2; i++ {
		job := &livekit.Job{
			Id:   string(rune('a' + i)),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room"},
		}
		accepted, _ := handler.OnJobRequest(ctx, job)
		assert.True(t, accepted, "should accept job %d", i+1)

		// Simulate job assignment
		handler.sessions[job.Id] = &RecordingSession{}
	}

	// Third job should be rejected due to capacity
	job := &livekit.Job{
		Id:   "c",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room"},
	}
	accepted, _ := handler.OnJobRequest(ctx, job)
	assert.False(t, accepted, "should reject job when at capacity")

	stats := handler.GetStats()
	assert.Equal(t, int64(3), stats.TotalJobs)
	assert.Equal(t, int64(2), stats.AcceptedJobs)
	assert.Equal(t, int64(1), stats.RejectedJobs)
	assert.Equal(t, 2, stats.ActiveSessions)
}

// TestConnectionManager tests connection management and auto-reconnect
func TestConnectionManager(t *testing.T) {
	// Create mock room
	room := &livekit.Room{
		Callback: &livekit.RoomCallback{},
	}

	config := &NetworkConfig{
		ReconnectAttempts: 3,
		ReconnectDelay:    100 * time.Millisecond,
	}

	cm := NewConnectionManager(room, config)

	// Set callbacks
	var connectedCount, disconnectedCount, reconnectingCount int
	cm.SetCallbacks(
		func() { connectedCount++ },
		func(err error) { disconnectedCount++ },
		func() { reconnectingCount++ },
		func(err error) { t.Logf("Connection failed: %v", err) },
	)

	// Start connection manager
	err := cm.Start()
	require.NoError(t, err)

	// Simulate connection state changes
	room.Callback.OnConnectionStateChanged(livekit.ConnectionStateConnected)
	assert.Equal(t, ConnectionStateConnected, cm.GetState())

	// Simulate disconnection
	room.Callback.OnDisconnected()
	assert.Equal(t, ConnectionStateDisconnected, cm.GetState())

	// Wait for potential reconnection attempt
	time.Sleep(200 * time.Millisecond)

	// Simulate successful reconnection
	room.Callback.OnReconnected()
	assert.Equal(t, ConnectionStateConnected, cm.GetState())

	// Stop connection manager
	cm.Stop()

	// Verify callback counts
	assert.GreaterOrEqual(t, connectedCount, 1)
	assert.GreaterOrEqual(t, disconnectedCount, 1)

	// Check statistics
	stats := cm.GetStats()
	assert.GreaterOrEqual(t, stats.SuccessfulConnects, int64(1))
	assert.GreaterOrEqual(t, stats.Disconnections, int64(1))
}

// TestTrackSubscriber tests track subscription logic
func TestTrackSubscriber(t *testing.T) {
	config := &RecordingConfig{
		RecordVideo:       true,
		RecordAudio:       true,
		RecordScreenShare: false,
		AutoStart:         true,
	}

	room := &livekit.Room{}
	ts := NewTrackSubscriber(config, room)

	// Set quality settings
	qs := NewQualitySettings(livekit.VideoQuality_HIGH, livekit.AudioQuality_AUDIO_QUALITY_HIGH)
	ts.SetQualitySettings(qs)

	// Create mock participant and publication
	participant := &livekit.RemoteParticipant{}
	publication := &livekit.RemoteTrackPublication{}

	// Test shouldSubscribe logic
	tests := []struct {
		name           string
		kind           livekit.TrackKind
		source         livekit.TrackSource
		expectSubscribe bool
	}{
		{
			name:           "subscribe to video camera",
			kind:           livekit.TrackKind_VIDEO,
			source:         livekit.TrackSource_CAMERA,
			expectSubscribe: true,
		},
		{
			name:           "don't subscribe to screen share when disabled",
			kind:           livekit.TrackKind_VIDEO,
			source:         livekit.TrackSource_SCREEN_SHARE,
			expectSubscribe: false,
		},
		{
			name:           "subscribe to audio microphone",
			kind:           livekit.TrackKind_AUDIO,
			source:         livekit.TrackSource_MICROPHONE,
			expectSubscribe: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock publication methods
			publication.Kind = func() livekit.TrackKind { return tt.kind }
			publication.Source = func() livekit.TrackSource { return tt.source }

			shouldSub := ts.shouldSubscribe(publication, participant)
			assert.Equal(t, tt.expectSubscribe, shouldSub)
		})
	}

	// Check statistics
	stats := ts.GetStats()
	assert.Equal(t, int64(0), stats.TotalTracksSubscribed)
	assert.Equal(t, 0, stats.CurrentSubscriptions)
}

// TestParticipantTracker tests participant tracking
func TestParticipantTracker(t *testing.T) {
	pt := NewParticipantTracker()

	// Set callbacks
	var joinedCount, leftCount int
	pt.SetCallbacks(
		func(p *ParticipantInfo) { joinedCount++ },
		func(p *ParticipantInfo) { leftCount++ },
	)

	// Create mock participants
	participant1 := &livekit.RemoteParticipant{}
	participant1.SID = func() string { return "p1" }
	participant1.Identity = func() string { return "user1" }
	participant1.Name = func() string { return "User 1" }

	participant2 := &livekit.RemoteParticipant{}
	participant2.SID = func() string { return "p2" }
	participant2.Identity = func() string { return "user2" }
	participant2.Name = func() string { return "User 2" }

	// Test participant join
	pt.OnParticipantConnected(participant1)
	assert.Equal(t, 1, pt.GetParticipantCount())
	assert.Equal(t, 1, joinedCount)

	pt.OnParticipantConnected(participant2)
	assert.Equal(t, 2, pt.GetParticipantCount())
	assert.Equal(t, 2, joinedCount)

	// Verify participant info
	info, exists := pt.GetParticipant("p1")
	assert.True(t, exists)
	assert.Equal(t, "p1", info.SID)
	assert.Equal(t, "user1", info.Identity)
	assert.True(t, info.IsActive)

	// Test minimum participants check
	assert.True(t, pt.HasMinimumParticipants(2))
	assert.False(t, pt.HasMinimumParticipants(3))

	// Test participant leave
	pt.OnParticipantDisconnected(participant1)
	assert.Equal(t, 1, pt.GetParticipantCount())
	assert.Equal(t, 1, leftCount)

	// Check statistics
	stats := pt.GetStats()
	assert.Equal(t, int64(2), stats.TotalParticipantsJoined)
	assert.Equal(t, int64(1), stats.TotalParticipantsLeft)
	assert.Equal(t, 1, stats.CurrentParticipants)
	assert.Equal(t, 2, stats.PeakParticipants)
}

// TestCodecTracker tests codec verification and locking
func TestCodecTracker(t *testing.T) {
	ct := NewCodecTracker("test-session")

	// Test video codec validation
	h264Codec := webrtc.RTPCodecParameters{
		MimeType:    "video/H264",
		PayloadType: 96,
	}

	err := ct.ValidateVideoCodec(h264Codec)
	assert.NoError(t, err, "should accept H264 codec")

	// Try to change codec (should fail)
	vp8Codec := webrtc.RTPCodecParameters{
		MimeType:    "video/VP8",
		PayloadType: 97,
	}

	err = ct.ValidateVideoCodec(vp8Codec)
	assert.Error(t, err, "should reject codec change")
	assert.Contains(t, err.Error(), "codec change detected")

	// Test audio codec validation
	opusCodec := webrtc.RTPCodecParameters{
		MimeType:    "audio/opus",
		PayloadType: 111,
	}

	err = ct.ValidateAudioCodec(opusCodec)
	assert.NoError(t, err, "should accept Opus codec")

	// Test unsupported codec
	unsupportedCodec := webrtc.RTPCodecParameters{
		MimeType:    "video/UNKNOWN",
		PayloadType: 100,
	}

	ct.Reset() // Reset to test unsupported codec
	err = ct.ValidateVideoCodec(unsupportedCodec)
	assert.Error(t, err, "should reject unsupported codec")
	assert.Contains(t, err.Error(), "unsupported")

	// Check codec info
	ct.Reset()
	ct.ValidateVideoCodec(h264Codec)
	ct.ValidateAudioCodec(opusCodec)

	info := ct.GetCodecInfo()
	assert.Equal(t, "video/H264", info.VideoCodec)
	assert.Equal(t, "audio/opus", info.AudioCodec)
	assert.Equal(t, int64(0), info.RejectCount)
}

// TestQualitySettings tests quality configuration
func TestQualitySettings(t *testing.T) {
	qs := NewQualitySettings(livekit.VideoQuality_HIGH, livekit.AudioQuality_AUDIO_QUALITY_HIGH)

	// Check default dimensions for HIGH quality
	width, height, fps := qs.GetDimensions()
	assert.Equal(t, uint32(1920), width)
	assert.Equal(t, uint32(1080), height)
	assert.Equal(t, uint32(30), fps)

	// Test quality preset
	qs.ApplyPreset(QualityPresetMedium)
	assert.Equal(t, livekit.VideoQuality_MEDIUM, qs.GetVideoQuality())
	assert.Equal(t, livekit.AudioQuality_AUDIO_QUALITY_MEDIUM, qs.GetAudioQuality())

	width, height, fps = qs.GetDimensions()
	assert.Equal(t, uint32(1280), width)
	assert.Equal(t, uint32(720), height)

	// Test custom dimensions
	qs.SetPreferredDimensions(1920, 1080, 60)
	width, height, fps = qs.GetDimensions()
	assert.Equal(t, uint32(1920), width)
	assert.Equal(t, uint32(1080), height)
	assert.Equal(t, uint32(60), fps)

	// Test bitrate range
	qs.SetBitrateRange(1000000, 5000000)
	min, max := qs.GetBitrateRange()
	assert.Equal(t, uint32(1000000), min)
	assert.Equal(t, uint32(5000000), max)

	// Test adaptive stream
	qs.SetAdaptiveStream(true)
	assert.True(t, qs.IsAdaptiveStreamEnabled())

	// Test recommended bitrate calculation
	bitrate := RecommendedBitrateForResolution(1920, 1080)
	assert.Equal(t, uint32(5000000), bitrate)

	bitrate = RecommendedBitrateForResolution(1280, 720)
	assert.Equal(t, uint32(2500000), bitrate)
}

// TestRTPRouterStatistics tests RTP router statistics tracking
func TestRTPRouterStatistics(t *testing.T) {
	config := DefaultConfig()
	router := NewRTPRouter(config, nil)

	// Start router
	err := router.Start()
	require.NoError(t, err)

	// Check initial stats
	stats := router.GetStats()
	assert.Equal(t, uint64(0), stats.PacketsReceived)
	assert.Equal(t, uint64(0), stats.PacketsForwarded)
	assert.Equal(t, uint64(0), stats.PacketsDropped)

	// Check packet loss rate
	lossRate := router.GetPacketLossRate()
	assert.Equal(t, float64(0), lossRate)

	// Check health status
	assert.True(t, router.IsHealthy(), "router should be healthy initially")

	// Stop router
	router.Stop()

	// Reset stats
	router.ResetStats()
	stats = router.GetStats()
	assert.Equal(t, uint64(0), stats.PacketsReceived)
}

// TestRecordingSessionLifecycle tests the full recording session lifecycle
func TestRecordingSessionLifecycle(t *testing.T) {
	// Create mock job context
	jobCtx := &agent.JobContext{
		Job: &livekit.Job{
			Id:   "test-job",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{
				Name: "test-room",
			},
		},
		Room: &livekit.Room{
			Callback: &livekit.RoomCallback{},
		},
	}

	config := DefaultConfig()
	config.RecordingConfig.AutoStart = true
	config.RecordingConfig.RecordVideo = true
	config.RecordingConfig.RecordAudio = true

	session := NewRecordingSession(jobCtx, config)

	// Check initial state
	assert.Equal(t, StateIdle, session.GetState())

	// Note: Full session start would require mock pipeline and actual LiveKit connection
	// This test verifies the structure is in place

	// Check stats structure
	stats := session.GetStats()
	assert.NotNil(t, stats.StartTime)
	assert.Equal(t, int64(0), stats.Duration)
	assert.Equal(t, int32(0), stats.TracksSubscribed)

	// Verify session can be stopped
	session.Stop()
	assert.Eventually(t, func() bool {
		return session.GetState() == StateStopped
	}, time.Second, 10*time.Millisecond)
}

// TestEgressHandlerShutdown tests graceful shutdown
func TestEgressHandlerShutdown(t *testing.T) {
	config := DefaultConfig()
	handler := NewEgressHandler(config)

	// Add a mock session
	handler.sessions["test-session"] = &RecordingSession{
		state: StateRecording,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := handler.Shutdown(ctx)
	assert.NoError(t, err)

	// Verify all sessions were cleaned up
	assert.Equal(t, 0, len(handler.sessions))
}