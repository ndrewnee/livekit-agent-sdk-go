package egress

import (
	"context"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// BaseEgressHandler provides default no-op implementations for all UniversalHandler methods
// Embed this in your handler to only implement the methods you need
type BaseEgressHandler struct{}

// Core job lifecycle - these should typically be overridden
func (h *BaseEgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	return false, nil
}

func (h *BaseEgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	return nil
}

func (h *BaseEgressHandler) OnJobTerminated(ctx context.Context, jobID string) {}

func (h *BaseEgressHandler) GetJobMetadata(job *livekit.Job) *agent.JobMetadata {
	return &agent.JobMetadata{
		ParticipantName:     "Egress Worker",
		ParticipantIdentity: "egress-worker",
		ParticipantMetadata: "{}",
	}
}

// Room events
func (h *BaseEgressHandler) OnRoomConnected(ctx context.Context, room *lksdk.Room) {}

func (h *BaseEgressHandler) OnRoomDisconnected(ctx context.Context, room *lksdk.Room, reason string) {}

func (h *BaseEgressHandler) OnRoomMetadataChanged(ctx context.Context, oldMetadata, newMetadata string) {}

// Participant events
func (h *BaseEgressHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {}

func (h *BaseEgressHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {}

func (h *BaseEgressHandler) OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {}

func (h *BaseEgressHandler) OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool) {}

// Track events
func (h *BaseEgressHandler) OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {}

func (h *BaseEgressHandler) OnTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {}

func (h *BaseEgressHandler) OnTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {}

func (h *BaseEgressHandler) OnTrackUnsubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {}

func (h *BaseEgressHandler) OnTrackMuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {}

func (h *BaseEgressHandler) OnTrackUnmuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {}

// Media events
func (h *BaseEgressHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {}

// Quality events
func (h *BaseEgressHandler) OnConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {}

// Active speaker events
func (h *BaseEgressHandler) OnActiveSpeakersChanged(ctx context.Context, speakers []lksdk.Participant) {}

// Ensure BaseEgressHandler implements UniversalHandler
var _ agent.UniversalHandler = (*BaseEgressHandler)(nil)