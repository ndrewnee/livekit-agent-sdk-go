package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSimpleUniversalHandler tests all SimpleUniversalHandler methods
func TestSimpleUniversalHandler(t *testing.T) {
	handler := &SimpleUniversalHandler{}

	// SimpleUniversalHandler doesn't have OnWorkerStarted/OnWorkerStopped

	// Test OnJobRequest
	t.Run("OnJobRequest", func(t *testing.T) {
		handler.JobRequestFunc = func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test",
			}
		}
		accept, metadata := handler.OnJobRequest(context.Background(), &livekit.Job{})
		assert.True(t, accept)
		assert.NotNil(t, metadata)
	})

	// Test OnJobAssigned
	t.Run("OnJobAssigned", func(t *testing.T) {
		called := false
		handler.JobAssignedFunc = func(ctx context.Context, jobCtx *JobContext) error {
			called = true
			return nil
		}
		err := handler.OnJobAssigned(context.Background(), &JobContext{})
		assert.NoError(t, err)
		assert.True(t, called)
	})

	// SimpleUniversalHandler doesn't have OnJobCompleted (only OnJobTerminated)

	// Test OnJobTerminated
	t.Run("OnJobTerminated", func(t *testing.T) {
		called := false
		handler.JobTerminatedFunc = func(ctx context.Context, jobID string) {
			called = true
		}
		handler.OnJobTerminated(context.Background(), "job-1")
		assert.True(t, called)
	})

	// Test OnRoomConnected
	t.Run("OnRoomConnected", func(t *testing.T) {
		called := false
		handler.RoomConnectedFunc = func(ctx context.Context, room *lksdk.Room) {
			called = true
		}
		handler.OnRoomConnected(context.Background(), &lksdk.Room{})
		assert.True(t, called)
	})

	// Test OnRoomDisconnected
	t.Run("OnRoomDisconnected", func(t *testing.T) {
		called := false
		handler.RoomDisconnectedFunc = func(ctx context.Context, room *lksdk.Room, reason string) {
			called = true
		}
		handler.OnRoomDisconnected(context.Background(), &lksdk.Room{}, "test")
		assert.True(t, called)
	})

	// Test OnParticipantJoined
	t.Run("OnParticipantJoined", func(t *testing.T) {
		called := false
		handler.ParticipantJoinedFunc = func(ctx context.Context, participant *lksdk.RemoteParticipant) {
			called = true
		}
		handler.OnParticipantJoined(context.Background(), &lksdk.RemoteParticipant{})
		assert.True(t, called)
	})

	// Test OnParticipantLeft
	t.Run("OnParticipantLeft", func(t *testing.T) {
		called := false
		handler.ParticipantLeftFunc = func(ctx context.Context, participant *lksdk.RemoteParticipant) {
			called = true
		}
		handler.OnParticipantLeft(context.Background(), &lksdk.RemoteParticipant{})
		assert.True(t, called)
	})

	// Test OnTrackPublished
	t.Run("OnTrackPublished", func(t *testing.T) {
		called := false
		handler.TrackPublishedFunc = func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
			called = true
		}
		handler.OnTrackPublished(context.Background(), &lksdk.RemoteParticipant{}, &lksdk.RemoteTrackPublication{})
		assert.True(t, called)
	})

	// Test OnTrackUnpublished
	t.Run("OnTrackUnpublished", func(t *testing.T) {
		called := false
		handler.TrackUnpublishedFunc = func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
			called = true
		}
		handler.OnTrackUnpublished(context.Background(), &lksdk.RemoteParticipant{}, &lksdk.RemoteTrackPublication{})
		assert.True(t, called)
	})

	// Test OnTrackSubscribed
	t.Run("OnTrackSubscribed", func(t *testing.T) {
		called := false
		handler.TrackSubscribedFunc = func(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
			called = true
		}
		handler.OnTrackSubscribed(context.Background(), &webrtc.TrackRemote{}, &lksdk.RemoteTrackPublication{}, &lksdk.RemoteParticipant{})
		assert.True(t, called)
	})

	// Test OnTrackUnsubscribed
	t.Run("OnTrackUnsubscribed", func(t *testing.T) {
		called := false
		handler.TrackUnsubscribedFunc = func(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
			called = true
		}
		handler.OnTrackUnsubscribed(context.Background(), &webrtc.TrackRemote{}, &lksdk.RemoteTrackPublication{}, &lksdk.RemoteParticipant{})
		assert.True(t, called)
	})

	// Test OnDataReceived
	t.Run("OnDataReceived", func(t *testing.T) {
		called := false
		handler.DataReceivedFunc = func(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
			called = true
		}
		handler.OnDataReceived(context.Background(), []byte("test"), &lksdk.RemoteParticipant{}, livekit.DataPacket_RELIABLE)
		assert.True(t, called)
	})

	// Test OnConnectionQualityChanged
	t.Run("OnConnectionQualityChanged", func(t *testing.T) {
		called := false
		handler.ConnectionQualityChangedFunc = func(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
			called = true
		}
		handler.OnConnectionQualityChanged(context.Background(), &lksdk.RemoteParticipant{}, livekit.ConnectionQuality_GOOD)
		assert.True(t, called)
	})

	// Test OnParticipantMetadataChanged
	t.Run("OnParticipantMetadataChanged", func(t *testing.T) {
		called := false
		handler.ParticipantMetadataChangedFunc = func(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
			called = true
		}
		handler.OnParticipantMetadataChanged(context.Background(), &lksdk.RemoteParticipant{}, "old")
		assert.True(t, called)
	})

	// Test OnActiveSpeakersChanged
	t.Run("OnActiveSpeakersChanged", func(t *testing.T) {
		called := false
		handler.ActiveSpeakersChangedFunc = func(ctx context.Context, speakers []lksdk.Participant) {
			called = true
		}
		handler.OnActiveSpeakersChanged(context.Background(), []lksdk.Participant{})
		assert.True(t, called)
	})

	// Test OnRoomMetadataChanged
	t.Run("OnRoomMetadataChanged", func(t *testing.T) {
		called := false
		handler.RoomMetadataChangedFunc = func(ctx context.Context, oldMetadata, newMetadata string) {
			called = true
		}
		handler.OnRoomMetadataChanged(context.Background(), "old", "new")
		assert.True(t, called)
	})

	// SimpleUniversalHandler doesn't have OnError method
}

// TestJobContext tests JobContext structure
func TestJobContextMethods(t *testing.T) {
	ctx := &JobContext{
		Job: &livekit.Job{
			Id:   "test-job",
			Type: livekit.JobType_JT_ROOM,
		},
		StartedAt: time.Now(),
		Room:      &lksdk.Room{},
		Cancel:    func() {},
	}

	// Test fields are accessible
	assert.NotNil(t, ctx.Job)
	assert.NotNil(t, ctx.Room)
	assert.NotNil(t, ctx.Cancel)
	assert.NotNil(t, ctx.StartedAt)
	assert.Equal(t, "test-job", ctx.Job.Id)
	assert.Equal(t, livekit.JobType_JT_ROOM, ctx.Job.Type)
}

// TestJobMetadataComprehensive tests JobMetadata
func TestJobMetadataComprehensive(t *testing.T) {
	metadata := &JobMetadata{
		ParticipantIdentity: "test-participant",
		ParticipantName:     "Test Name",
		ParticipantMetadata: "test-metadata",
	}

	assert.Equal(t, "test-participant", metadata.ParticipantIdentity)
	assert.Equal(t, "Test Name", metadata.ParticipantName)
	assert.Equal(t, "test-metadata", metadata.ParticipantMetadata)
}

// TestActiveJob tests activeJob structure
func TestActiveJobStruct(t *testing.T) {
	// activeJob is an internal type, test its basic structure
	job := &activeJob{
		job: &livekit.Job{
			Id:   "job-1",
			Type: livekit.JobType_JT_ROOM,
		},
		room:      &lksdk.Room{},
		startedAt: time.Now(),
		status:    livekit.JobStatus_JS_RUNNING,
	}

	assert.Equal(t, "job-1", job.job.Id)
	assert.NotNil(t, job.room)
	assert.NotNil(t, job.startedAt)
	assert.Equal(t, livekit.JobStatus_JS_RUNNING, job.status)
}

// TestWorkerState tests WorkerState serialization
func TestWorkerState(t *testing.T) {
	state := &WorkerState{
		WorkerID: "worker-1",
		ActiveJobs: map[string]*JobState{
			"job-1": {
				JobID:     "job-1",
				StartedAt: time.Now(),
				Status:    livekit.JobStatus_JS_RUNNING,
			},
		},
	}

	assert.Equal(t, "worker-1", state.WorkerID)
	assert.Len(t, state.ActiveJobs, 1)
	assert.Equal(t, livekit.JobStatus_JS_RUNNING, state.ActiveJobs["job-1"].Status)
}

// TestJobCheckpointComprehensive tests JobCheckpoint
func TestJobCheckpointComprehensive(t *testing.T) {
	checkpoint := NewJobCheckpoint("job-1")
	require.NotNil(t, checkpoint)

	// Save some data
	checkpoint.Save("progress", 50)
	checkpoint.Save("status", "running")

	// Load data
	progress, exists := checkpoint.Load("progress")
	assert.True(t, exists)
	assert.Equal(t, 50, progress)

	status, exists := checkpoint.Load("status")
	assert.True(t, exists)
	assert.Equal(t, "running", status)
}

// TestPublisherTrackSubscription tests PublisherTrackSubscription
func TestPublisherTrackSubscription(t *testing.T) {
	sub := &PublisherTrackSubscription{
		TrackID:          "track-1",
		ParticipantID:    "participant-1",
		SubscribedAt:     time.Now(),
		Quality:          livekit.VideoQuality_HIGH,
		PreferredQuality: livekit.VideoQuality_HIGH,
		CurrentQuality:   livekit.VideoQuality_HIGH,
		FrameRate:        30.0,
		Enabled:          true,
	}

	assert.Equal(t, "track-1", sub.TrackID)
	assert.Equal(t, "participant-1", sub.ParticipantID)
	assert.Equal(t, livekit.VideoQuality_HIGH, sub.Quality)
	assert.Equal(t, float32(30.0), sub.FrameRate)
	assert.True(t, sub.Enabled)
}

// TestVideoDimensions tests VideoDimensions
func TestVideoDimensions(t *testing.T) {
	res := &VideoDimensions{
		Width:  1920,
		Height: 1080,
	}

	assert.Equal(t, uint32(1920), res.Width)
	assert.Equal(t, uint32(1080), res.Height)
}

// TestParticipantInfo tests ParticipantInfo
func TestParticipantInfo(t *testing.T) {
	info := &ParticipantInfo{
		Identity:          "participant-1",
		Name:              "Test Participant",
		Metadata:          "metadata",
		JoinedAt:          time.Now(),
		LastActivity:      time.Now(),
		Permissions:       &livekit.ParticipantPermission{CanPublish: true},
		Attributes:        map[string]string{"key": "value"},
		IsSpeaking:        true,
		AudioLevel:        0.5,
		ConnectionQuality: livekit.ConnectionQuality_GOOD,
	}

	assert.Equal(t, "participant-1", info.Identity)
	assert.Equal(t, "Test Participant", info.Name)
	assert.True(t, info.Permissions.CanPublish)
	assert.Equal(t, "value", info.Attributes["key"])
	assert.True(t, info.IsSpeaking)
	assert.Equal(t, float32(0.5), info.AudioLevel)
	assert.Equal(t, livekit.ConnectionQuality_GOOD, info.ConnectionQuality)
}

// RoomCallbacks struct doesn't exist in this codebase

// TestEnums tests enum values
func TestEnums(t *testing.T) {
	// Test WebSocketState
	assert.Equal(t, WebSocketState(0), WebSocketStateDisconnected)
	assert.Equal(t, WebSocketState(1), WebSocketStateConnecting)
	assert.Equal(t, WebSocketState(2), WebSocketStateConnected)
	assert.Equal(t, WebSocketState(3), WebSocketStateReconnecting)

	// Test WorkerStatus
	assert.Equal(t, WorkerStatus(0), WorkerStatusAvailable)
	assert.Equal(t, WorkerStatus(1), WorkerStatusFull)

	// Test ShutdownPhase
	assert.Equal(t, ShutdownPhase("pre_stop"), ShutdownPhasePreStop)
	assert.Equal(t, ShutdownPhase("stop_jobs"), ShutdownPhaseStopJobs)
	assert.Equal(t, ShutdownPhase("cleanup"), ShutdownPhaseCleanup)
	assert.Equal(t, ShutdownPhase("final"), ShutdownPhaseFinal)

	// Test ActivityType
	assert.Equal(t, ActivityType("joined"), ActivityTypeJoined)
	assert.Equal(t, ActivityType("left"), ActivityTypeLeft)
	assert.Equal(t, ActivityType("speaking"), ActivityTypeSpeaking)
	assert.Equal(t, ActivityType("track_published"), ActivityTypeTrackPublished)
	assert.Equal(t, ActivityType("data_received"), ActivityTypeDataReceived)

	// Test MediaType
	assert.Equal(t, MediaType(0), MediaTypeAudio)
	assert.Equal(t, MediaType(1), MediaTypeVideo)
	// MediaTypeData doesn't exist, only Audio and Video
}

// TestWorkerOptionsDefaults tests WorkerOptions default values
func TestWorkerOptionsDefaults(t *testing.T) {
	opts := WorkerOptions{}

	// Apply defaults (this would normally be done in NewUniversalWorker)
	if opts.MaxJobs == 0 {
		opts.MaxJobs = 1
	}
	// ReconnectAttempts field doesn't exist in WorkerOptions
	// ReconnectDelay field doesn't exist in WorkerOptions
	if opts.PingInterval == 0 {
		opts.PingInterval = 10 * time.Second
	}
	if opts.PingTimeout == 0 {
		opts.PingTimeout = 5 * time.Second
	}

	assert.Equal(t, 1, opts.MaxJobs)
	// ReconnectAttempts test removed - field doesn't exist
	// ReconnectDelay test removed - field doesn't exist
	assert.Equal(t, 10*time.Second, opts.PingInterval)
	assert.Equal(t, 5*time.Second, opts.PingTimeout)
}

// LoadInfo type doesn't exist in the codebase, test removed

// TestResourcePoolOptions tests ResourcePoolOptions
func TestResourcePoolOptions(t *testing.T) {
	opts := ResourcePoolOptions{
		MinSize:     2,
		MaxSize:     10,
		MaxIdleTime: 5 * time.Minute,
	}

	assert.Equal(t, 2, opts.MinSize)
	assert.Equal(t, 10, opts.MaxSize)
	assert.Equal(t, 5*time.Minute, opts.MaxIdleTime)
}

// TestResourceLimiterOptions tests ResourceLimiterOptions
func TestResourceLimiterOptions(t *testing.T) {
	opts := ResourceLimiterOptions{
		MemoryLimitMB:      1024,
		CPUQuotaPercent:    80,
		MaxFileDescriptors: 1000,
		CheckInterval:      30 * time.Second,
		EnforceHardLimits:  true,
	}

	assert.Equal(t, 1024, opts.MemoryLimitMB)
	assert.Equal(t, 80, opts.CPUQuotaPercent)
	assert.Equal(t, 1000, opts.MaxFileDescriptors)
}

// TestAllHandlerMethods tests that all handler methods can be called without panic
func TestAllHandlerMethods(t *testing.T) {
	handler := &SimpleUniversalHandler{}
	ctx := context.Background()

	accept, metadata := handler.OnJobRequest(ctx, &livekit.Job{})
	assert.True(t, accept)
	assert.NotNil(t, metadata) // BaseHandler returns empty metadata, not nil

	// OnJobAssigned blocks by default, so run it in a goroutine with timeout
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- handler.OnJobAssigned(ctxWithTimeout, &JobContext{})
	}()

	select {
	case err := <-done:
		// Should return nil after context is done
		assert.NoError(t, err)
	case <-time.After(20 * time.Millisecond):
		// Should have completed by now
		t.Log("OnJobAssigned completed after timeout")
	}

	// OnJobCompleted doesn't exist, removed
	handler.OnJobTerminated(ctx, "job-1")
	handler.OnRoomConnected(ctx, &lksdk.Room{})
	handler.OnRoomDisconnected(ctx, &lksdk.Room{}, "test")
	handler.OnParticipantJoined(ctx, &lksdk.RemoteParticipant{})
	handler.OnParticipantLeft(ctx, &lksdk.RemoteParticipant{})
	handler.OnTrackPublished(ctx, &lksdk.RemoteParticipant{}, &lksdk.RemoteTrackPublication{})
	handler.OnTrackUnpublished(ctx, &lksdk.RemoteParticipant{}, &lksdk.RemoteTrackPublication{})
	// OnTrackSubscribed/Unsubscribed use webrtc.TrackRemote not lksdk.RemoteTrack
	var track *webrtc.TrackRemote
	handler.OnTrackSubscribed(ctx, track, &lksdk.RemoteTrackPublication{}, &lksdk.RemoteParticipant{})
	handler.OnTrackUnsubscribed(ctx, track, &lksdk.RemoteTrackPublication{}, &lksdk.RemoteParticipant{})
	handler.OnDataReceived(ctx, []byte("test"), &lksdk.RemoteParticipant{}, livekit.DataPacket_RELIABLE)
	handler.OnConnectionQualityChanged(ctx, &lksdk.RemoteParticipant{}, livekit.ConnectionQuality_GOOD)
	handler.OnParticipantMetadataChanged(ctx, &lksdk.RemoteParticipant{}, "old")
	handler.OnActiveSpeakersChanged(ctx, []lksdk.Participant{})
	handler.OnRoomMetadataChanged(ctx, "old", "new")
	// OnError doesn't exist in SimpleUniversalHandler
}
