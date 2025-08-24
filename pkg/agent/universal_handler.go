package agent

import (
	"context"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// UniversalHandler is a unified interface that combines all handler capabilities.
// Implementations can choose which callbacks to implement based on their needs.
// All callbacks are optional - default no-op implementations are provided via BaseHandler.
type UniversalHandler interface {
	// Core job lifecycle
	OnJobRequest(ctx context.Context, job *livekit.Job) (accept bool, metadata *JobMetadata)
	OnJobAssigned(ctx context.Context, jobCtx *JobContext) error
	OnJobTerminated(ctx context.Context, jobID string)

	// Helper method to get job metadata (called internally)
	GetJobMetadata(job *livekit.Job) *JobMetadata

	// Room events (all job types)
	OnRoomConnected(ctx context.Context, room *lksdk.Room)
	OnRoomDisconnected(ctx context.Context, room *lksdk.Room, reason string)
	OnRoomMetadataChanged(ctx context.Context, oldMetadata, newMetadata string)

	// Participant events (all job types)
	OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant)
	OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant)
	OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string)
	OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool)

	// Track events (all job types)
	OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)
	OnTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)
	OnTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant)
	OnTrackUnsubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant)
	OnTrackMuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant)
	OnTrackUnmuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant)

	// Media events
	OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind)

	// Quality events
	OnConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality)

	// Active speaker events
	OnActiveSpeakersChanged(ctx context.Context, speakers []lksdk.Participant)
}

// BaseHandler provides default no-op implementations for all UniversalHandler methods.
// Embed this in your handler to only override the methods you need.
type BaseHandler struct{}

// Core job lifecycle
func (h *BaseHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	return true, &JobMetadata{} // Accept all jobs by default
}

func (h *BaseHandler) OnJobAssigned(ctx context.Context, jobCtx *JobContext) error {
	// Default implementation - just wait for context cancellation
	<-ctx.Done()
	return nil
}

func (h *BaseHandler) OnJobTerminated(ctx context.Context, jobID string) {
	// No-op
}

func (h *BaseHandler) GetJobMetadata(job *livekit.Job) *JobMetadata {
	_, metadata := h.OnJobRequest(context.Background(), job)
	return metadata
}

// Room events
func (h *BaseHandler) OnRoomConnected(ctx context.Context, room *lksdk.Room) {
	// No-op
}

func (h *BaseHandler) OnRoomDisconnected(ctx context.Context, room *lksdk.Room, reason string) {
	// No-op
}

func (h *BaseHandler) OnRoomMetadataChanged(ctx context.Context, oldMetadata, newMetadata string) {
	// No-op
}

// Participant events
func (h *BaseHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	// No-op
}

func (h *BaseHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	// No-op
}

func (h *BaseHandler) OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
	// No-op
}

func (h *BaseHandler) OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool) {
	// No-op
}

// Track events
func (h *BaseHandler) OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	// No-op
}

func (h *BaseHandler) OnTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	// No-op
}

func (h *BaseHandler) OnTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	// No-op
}

func (h *BaseHandler) OnTrackUnsubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	// No-op
}

func (h *BaseHandler) OnTrackMuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {
	// No-op
}

func (h *BaseHandler) OnTrackUnmuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {
	// No-op
}

// Media events
func (h *BaseHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	// No-op
}

// Quality events
func (h *BaseHandler) OnConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
	// No-op
}

// Active speaker events
func (h *BaseHandler) OnActiveSpeakersChanged(ctx context.Context, speakers []lksdk.Participant) {
	// No-op
}

// SimpleUniversalHandler is a convenience handler that uses function fields for callbacks.
// This allows for quick prototyping without creating a full handler type.
type SimpleUniversalHandler struct {
	BaseHandler

	// Optional callbacks - set only the ones you need
	JobRequestFunc                 func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata)
	JobAssignedFunc                func(ctx context.Context, jobCtx *JobContext) error
	JobTerminatedFunc              func(ctx context.Context, jobID string)
	RoomConnectedFunc              func(ctx context.Context, room *lksdk.Room)
	RoomDisconnectedFunc           func(ctx context.Context, room *lksdk.Room, reason string)
	RoomMetadataChangedFunc        func(ctx context.Context, oldMetadata, newMetadata string)
	ParticipantJoinedFunc          func(ctx context.Context, participant *lksdk.RemoteParticipant)
	ParticipantLeftFunc            func(ctx context.Context, participant *lksdk.RemoteParticipant)
	ParticipantMetadataChangedFunc func(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string)
	ParticipantSpeakingChangedFunc func(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool)
	TrackPublishedFunc             func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)
	TrackUnpublishedFunc           func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)
	TrackSubscribedFunc            func(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant)
	TrackUnsubscribedFunc          func(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant)
	TrackMutedFunc                 func(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant)
	TrackUnmutedFunc               func(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant)
	DataReceivedFunc               func(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind)
	ConnectionQualityChangedFunc   func(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality)
	ActiveSpeakersChangedFunc      func(ctx context.Context, speakers []lksdk.Participant)
}

// Implement UniversalHandler by calling the function fields if set

func (h *SimpleUniversalHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	if h.JobRequestFunc != nil {
		return h.JobRequestFunc(ctx, job)
	}
	return h.BaseHandler.OnJobRequest(ctx, job)
}

func (h *SimpleUniversalHandler) OnJobAssigned(ctx context.Context, jobCtx *JobContext) error {
	if h.JobAssignedFunc != nil {
		return h.JobAssignedFunc(ctx, jobCtx)
	}
	return h.BaseHandler.OnJobAssigned(ctx, jobCtx)
}

func (h *SimpleUniversalHandler) OnJobTerminated(ctx context.Context, jobID string) {
	if h.JobTerminatedFunc != nil {
		h.JobTerminatedFunc(ctx, jobID)
		return
	}
	h.BaseHandler.OnJobTerminated(ctx, jobID)
}

// GetJobMetadata returns metadata for a job (used when job is assigned)
func (h *SimpleUniversalHandler) GetJobMetadata(job *livekit.Job) *JobMetadata {
	// Call JobRequestFunc to get the metadata
	if h.JobRequestFunc != nil {
		_, metadata := h.JobRequestFunc(context.Background(), job)
		return metadata
	}
	return nil
}

func (h *SimpleUniversalHandler) OnRoomConnected(ctx context.Context, room *lksdk.Room) {
	if h.RoomConnectedFunc != nil {
		h.RoomConnectedFunc(ctx, room)
		return
	}
	h.BaseHandler.OnRoomConnected(ctx, room)
}

func (h *SimpleUniversalHandler) OnRoomDisconnected(ctx context.Context, room *lksdk.Room, reason string) {
	if h.RoomDisconnectedFunc != nil {
		h.RoomDisconnectedFunc(ctx, room, reason)
		return
	}
	h.BaseHandler.OnRoomDisconnected(ctx, room, reason)
}

func (h *SimpleUniversalHandler) OnRoomMetadataChanged(ctx context.Context, oldMetadata, newMetadata string) {
	if h.RoomMetadataChangedFunc != nil {
		h.RoomMetadataChangedFunc(ctx, oldMetadata, newMetadata)
		return
	}
	h.BaseHandler.OnRoomMetadataChanged(ctx, oldMetadata, newMetadata)
}

func (h *SimpleUniversalHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	if h.ParticipantJoinedFunc != nil {
		h.ParticipantJoinedFunc(ctx, participant)
		return
	}
	h.BaseHandler.OnParticipantJoined(ctx, participant)
}

func (h *SimpleUniversalHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	if h.ParticipantLeftFunc != nil {
		h.ParticipantLeftFunc(ctx, participant)
		return
	}
	h.BaseHandler.OnParticipantLeft(ctx, participant)
}

func (h *SimpleUniversalHandler) OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
	if h.ParticipantMetadataChangedFunc != nil {
		h.ParticipantMetadataChangedFunc(ctx, participant, oldMetadata)
		return
	}
	h.BaseHandler.OnParticipantMetadataChanged(ctx, participant, oldMetadata)
}

func (h *SimpleUniversalHandler) OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool) {
	if h.ParticipantSpeakingChangedFunc != nil {
		h.ParticipantSpeakingChangedFunc(ctx, participant, speaking)
		return
	}
	h.BaseHandler.OnParticipantSpeakingChanged(ctx, participant, speaking)
}

func (h *SimpleUniversalHandler) OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	if h.TrackPublishedFunc != nil {
		h.TrackPublishedFunc(ctx, participant, publication)
		return
	}
	h.BaseHandler.OnTrackPublished(ctx, participant, publication)
}

func (h *SimpleUniversalHandler) OnTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	if h.TrackUnpublishedFunc != nil {
		h.TrackUnpublishedFunc(ctx, participant, publication)
		return
	}
	h.BaseHandler.OnTrackUnpublished(ctx, participant, publication)
}

func (h *SimpleUniversalHandler) OnTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	if h.TrackSubscribedFunc != nil {
		h.TrackSubscribedFunc(ctx, track, publication, participant)
		return
	}
	h.BaseHandler.OnTrackSubscribed(ctx, track, publication, participant)
}

func (h *SimpleUniversalHandler) OnTrackUnsubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	if h.TrackUnsubscribedFunc != nil {
		h.TrackUnsubscribedFunc(ctx, track, publication, participant)
		return
	}
	h.BaseHandler.OnTrackUnsubscribed(ctx, track, publication, participant)
}

func (h *SimpleUniversalHandler) OnTrackMuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {
	if h.TrackMutedFunc != nil {
		h.TrackMutedFunc(ctx, publication, participant)
		return
	}
	h.BaseHandler.OnTrackMuted(ctx, publication, participant)
}

func (h *SimpleUniversalHandler) OnTrackUnmuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {
	if h.TrackUnmutedFunc != nil {
		h.TrackUnmutedFunc(ctx, publication, participant)
		return
	}
	h.BaseHandler.OnTrackUnmuted(ctx, publication, participant)
}

func (h *SimpleUniversalHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	if h.DataReceivedFunc != nil {
		h.DataReceivedFunc(ctx, data, participant, kind)
		return
	}
	h.BaseHandler.OnDataReceived(ctx, data, participant, kind)
}

func (h *SimpleUniversalHandler) OnConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
	if h.ConnectionQualityChangedFunc != nil {
		h.ConnectionQualityChangedFunc(ctx, participant, quality)
		return
	}
	h.BaseHandler.OnConnectionQualityChanged(ctx, participant, quality)
}

func (h *SimpleUniversalHandler) OnActiveSpeakersChanged(ctx context.Context, speakers []lksdk.Participant) {
	if h.ActiveSpeakersChangedFunc != nil {
		h.ActiveSpeakersChangedFunc(ctx, speakers)
		return
	}
	h.BaseHandler.OnActiveSpeakersChanged(ctx, speakers)
}
