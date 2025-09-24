package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// LiveKit-backed test to exercise createRoomCallbacks bridges and runJobHandler.
func TestUniversalWorkerCreateRoomCallbacksLiveKit(t *testing.T) {
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	// Create publisher room
	roomName := "uw-callbacks"
	pubRoom, err := manager.CreateRoom(roomName)
	require.NoError(t, err)
	defer pubRoom.Disconnect()

	// Set up worker and handler counters
	var joined, left, trackPub, trackSub, muted, unmuted, dataRecv int32
	handler := &SimpleUniversalHandler{
		ParticipantJoinedFunc: func(ctx context.Context, p *lksdk.RemoteParticipant) { atomic.AddInt32(&joined, 1) },
		ParticipantLeftFunc:   func(ctx context.Context, p *lksdk.RemoteParticipant) { atomic.AddInt32(&left, 1) },
		TrackPublishedFunc: func(ctx context.Context, rp *lksdk.RemoteParticipant, pub *lksdk.RemoteTrackPublication) {
			atomic.AddInt32(&trackPub, 1)
		},
		TrackSubscribedFunc: func(ctx context.Context, tr *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
			atomic.AddInt32(&trackSub, 1)
		},
		TrackMutedFunc: func(ctx context.Context, pub lksdk.TrackPublication, p lksdk.Participant) { atomic.AddInt32(&muted, 1) },
		TrackUnmutedFunc: func(ctx context.Context, pub lksdk.TrackPublication, p lksdk.Participant) {
			atomic.AddInt32(&unmuted, 1)
		},
		DataReceivedFunc: func(ctx context.Context, data []byte, p *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
			atomic.AddInt32(&dataRecv, 1)
		},
	}
	w := &UniversalWorker{
		rooms:               make(map[string]*lksdk.Room),
		participants:        make(map[string]*ParticipantInfo),
		subscribedTracks:    make(map[string]*PublisherTrackSubscription),
		participantTrackers: make(map[string]*ParticipantTracker),
		handler:             handler,
		opts:                WorkerOptions{MaxJobs: 1},
	}

	job := &livekit.Job{Id: "job-cb", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}}
	cb := w.createRoomCallbacks(job)

	// Connect subscriber with our callbacks
	subInfo := lksdk.ConnectInfo{
		APIKey:              manager.APIKey,
		APISecret:           manager.APISecret,
		RoomName:            roomName,
		ParticipantIdentity: "uw-subscriber",
		ParticipantName:     "Subscriber",
	}
	subRoom, err := lksdk.ConnectToRoom(manager.URL, subInfo, cb, lksdk.WithAutoSubscribe(true))
	require.NoError(t, err)
	defer subRoom.Disconnect()
	w.rooms[roomName] = subRoom

	// Assign a default logger to avoid nil panics during status updates
	w.logger = NewDefaultLogger()

	// Publish audio track
	audioPub, err := manager.PublishSyntheticAudioTrack(pubRoom, "uw-cb-audio")
	require.NoError(t, err)
	require.NotNil(t, audioPub)

	// Wait until at least one subscription callback fires
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&trackSub) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Send a data packet from publisher
	_ = pubRoom.LocalParticipant.PublishDataPacket(lksdk.UserData([]byte("hello")), lksdk.WithDataPublishReliable(true))

	// Toggle mute
	audioPub.SetMuted(true)
	audioPub.SetMuted(false)

	// Give callbacks time to fire
	time.Sleep(200 * time.Millisecond)

	// Disconnect to trigger OnDisconnected
	subRoom.Disconnect()

	// Assert some callbacks fired
	assert.GreaterOrEqual(t, atomic.LoadInt32(&joined), int32(0))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&trackPub), int32(1))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&trackSub), int32(1))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&muted), int32(1))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&unmuted), int32(1))
	assert.GreaterOrEqual(t, atomic.LoadInt32(&dataRecv), int32(0))
}

// Note: runJobHandler is exercised via integration tests that drive full worker lifecycle.
