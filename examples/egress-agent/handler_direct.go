package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/router"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// DirectEgressHandler handles egress jobs using direct pipeline injection
type DirectEgressHandler struct {
	agent.BaseHandler // Embed base handler for default implementations

	config   *pipeline.Config
	sessions map[string]*DirectRecordingSession
	mu       sync.RWMutex
}

// DirectRecordingSession handles individual room recordings with direct pipeline
type DirectRecordingSession struct {
	job       *livekit.Job
	room      *lksdk.Room
	config    *pipeline.Config
	pipeline  *pipeline.DirectPipeline
	router    *router.DirectRouter
	cancel    context.CancelFunc
	mu        sync.RWMutex
}

// NewDirectEgressHandler creates a new direct egress handler
func NewDirectEgressHandler(config *pipeline.Config) *DirectEgressHandler {
	return &DirectEgressHandler{
		config:   config,
		sessions: make(map[string]*DirectRecordingSession),
	}
}

// OnJobRequest implements agent.UniversalHandler
func (h *DirectEgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	// Accept all room recording jobs
	if job.Type != livekit.JobType_JT_ROOM {
		return false, nil
	}

	return true, &agent.JobMetadata{
		ParticipantIdentity: fmt.Sprintf("egress-agent-%s", job.Id),
		ParticipantName:     "HLS Egress Agent (Direct)",
		ParticipantMetadata: `{"agent_type": "egress", "mode": "direct"}`,
	}
}

// OnJobAssigned implements agent.UniversalHandler
func (h *DirectEgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	log.Printf("Job assigned: %s for room: %s", jobCtx.Job.Id, jobCtx.Job.Room.Name)

	// Create recording session
	session := NewDirectRecordingSession(jobCtx, h.config)

	h.mu.Lock()
	h.sessions[jobCtx.Job.Id] = session
	h.mu.Unlock()

	// Start the recording session
	return session.Start(ctx)
}

// OnJobTerminated implements agent.UniversalHandler
func (h *DirectEgressHandler) OnJobTerminated(ctx context.Context, jobID string) {
	log.Printf("Job terminated: %s", jobID)

	h.mu.Lock()
	session, exists := h.sessions[jobID]
	if exists {
		delete(h.sessions, jobID)
	}
	h.mu.Unlock()

	if exists {
		session.Stop()
	}
}

// Shutdown stops all active sessions
func (h *DirectEgressHandler) Shutdown() {
	h.mu.Lock()
	sessions := make([]*DirectRecordingSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.sessions = make(map[string]*DirectRecordingSession)
	h.mu.Unlock()

	// Stop all sessions
	for _, session := range sessions {
		session.Stop()
	}
}

// NewDirectRecordingSession creates a new direct recording session
func NewDirectRecordingSession(jobCtx *agent.JobContext, config *pipeline.Config) *DirectRecordingSession {
	return &DirectRecordingSession{
		job:    jobCtx.Job,
		room:   jobCtx.Room,
		config: config,
	}
}

// Start begins the recording session
func (s *DirectRecordingSession) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Create direct pipeline (no UDP ports needed!)
	pipeline, err := pipeline.NewDirectPipeline(s.config, s.job.Id)
	if err != nil {
		return fmt.Errorf("failed to create direct pipeline: %w", err)
	}
	s.pipeline = pipeline

	// Create direct router that injects packets directly into pipeline
	router, err := router.NewDirectRouter(pipeline)
	if err != nil {
		pipeline.Stop()
		return fmt.Errorf("failed to create direct router: %w", err)
	}
	s.router = router

	// Start the pipeline
	if err := pipeline.Start(); err != nil {
		router.Close()
		pipeline.Stop()
		return fmt.Errorf("failed to start pipeline: %w", err)
	}

	// Set up track handling callbacks
	s.room.Callback.OnTrackSubscribed = s.OnTrackSubscribed
	s.room.Callback.OnParticipantDisconnected = s.OnParticipantDisconnected

	log.Printf("Direct recording session started for room: %s (session: %s)", s.job.Room.Name, s.job.Id)
	return nil
}

// Stop ends the recording session
func (s *DirectRecordingSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}

	if s.router != nil {
		s.router.Close()
		s.router = nil
	}

	if s.pipeline != nil {
		s.pipeline.Stop()
		s.pipeline = nil
	}

	log.Printf("Direct recording session stopped for room: %s", s.job.Room.Name)
}

// OnTrackSubscribed handles new track subscriptions
func (s *DirectRecordingSession) OnTrackSubscribed(
	track *webrtc.TrackRemote,
	publication *lksdk.RemoteTrackPublication,
	participant *lksdk.RemoteParticipant,
) {
	// Verify codec support
	if !s.isCodecSupported(track.Codec()) {
		log.Printf("Unsupported codec: %s", track.Codec().MimeType)
		return
	}

	log.Printf("Track subscribed: %s (%s) from participant: %s",
		track.ID(), track.Codec().MimeType, participant.Identity())

	// Forward RTP packets directly to pipeline
	go s.forwardRTPPackets(track)
}

// OnParticipantDisconnected handles participant disconnection
func (s *DirectRecordingSession) OnParticipantDisconnected(p *lksdk.RemoteParticipant) {
	log.Printf("Participant disconnected: %s", p.Identity())

	// Check if any participants remain
	if len(s.room.GetRemoteParticipants()) == 0 {
		log.Println("No participants remaining, keeping pipeline running for potential reconnections")
		// The gap filling (videorate/audiorate) will handle missing data
	}
}

// forwardRTPPackets reads RTP packets from track and forwards directly to pipeline
func (s *DirectRecordingSession) forwardRTPPackets(track *webrtc.TrackRemote) {
	for {
		// Read RTP packet from WebRTC
		packet, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("Error reading RTP from track %s: %v", track.ID(), err)
			break
		}

		// Determine track kind
		kind := router.TrackKindVideo
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			kind = router.TrackKindAudio
		}

		// Route directly to pipeline (no UDP!)
		if err := s.router.RoutePacket(packet, kind); err != nil {
			// Only log errors occasionally to avoid spam
			// The pipeline has jitter buffers to handle occasional packet loss
		}
	}
}

// isCodecSupported checks if a codec is supported for zero-transcode
func (s *DirectRecordingSession) isCodecSupported(codec webrtc.RTPCodecParameters) bool {
	switch codec.MimeType {
	case "video/H264":
		return true
	case "audio/opus":
		return true
	case "audio/mpeg": // MP3
		return true
	default:
		return false
	}
}

// GetStats returns current pipeline statistics
func (s *DirectRecordingSession) GetStats() *pipeline.Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.pipeline != nil {
		return s.pipeline.GetStats()
	}
	return nil
}