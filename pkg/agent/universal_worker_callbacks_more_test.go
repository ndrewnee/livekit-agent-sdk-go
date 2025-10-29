package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func TestCreateRoomCallbacks_DisconnectReasonsAndSignals(t *testing.T) {
	w := &UniversalWorker{
		rooms:        make(map[string]*lksdk.Room),
		participants: make(map[string]*ParticipantInfo),
		logger:       NewDefaultLogger(),
		opts:         WorkerOptions{MaxJobs: 1},
	}
	w.handler = &SimpleUniversalHandler{}
	job := &livekit.Job{Id: "j1", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: "r1"}}
	room := &lksdk.Room{}
	w.rooms["r1"] = room

	cb := w.createRoomCallbacks(job)

	// Exercise disconnect callbacks
	cb.OnDisconnected()
	cb.OnDisconnectedWithReason(lksdk.DuplicateIdentity)
	cb.OnDisconnectedWithReason(lksdk.ParticipantRemoved)
	cb.OnDisconnectedWithReason(lksdk.RoomClosed)

	// Reconnect signals
	cb.OnReconnecting()
	cb.OnReconnected()

	// Metadata and participant callbacks (safe variants)
	// Add room entry to trigger inner metadata handling
	w.rooms[job.Room.Name] = &lksdk.Room{}
	cb.OnRoomMetadataChanged("new-meta")
	// Track mute/unmute callbacks with nil interfaces
	cb.ParticipantCallback.OnTrackMuted(nil, nil)
	cb.ParticipantCallback.OnTrackUnmuted(nil, nil)
	// Track publish/unpublish callbacks with nils (handler no-ops)
	cb.ParticipantCallback.OnTrackPublished(nil, nil)
	cb.ParticipantCallback.OnTrackUnpublished(nil, nil)
	// Active speakers with empty list
	cb.OnActiveSpeakersChanged(nil)
	// Participant metadata/speaking/quality/data callbacks with safe inputs
	cb.ParticipantCallback.OnMetadataChanged("old", nil)
	cb.ParticipantCallback.OnIsSpeakingChanged(nil)
	cb.ParticipantCallback.OnConnectionQualityChanged(&livekit.ConnectionQualityInfo{Quality: livekit.ConnectionQuality_GOOD}, nil)
	dp := lksdk.UserData([]byte("hi"))
	cb.ParticipantCallback.OnDataPacket(dp, lksdk.DataReceiveParams{Sender: &lksdk.RemoteParticipant{}})

	// Participant connect/disconnect (safe with empty RemoteParticipant)
	rp := &lksdk.RemoteParticipant{}
	cb.OnParticipantConnected(rp)
	cb.OnParticipantDisconnected(rp)
}
