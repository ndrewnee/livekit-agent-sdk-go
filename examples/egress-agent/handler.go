package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// AudioProcessingMode defines how audio is processed
type AudioProcessingMode string

const (
	AudioPassThrough  AudioProcessingMode = "passthrough"  // Opus/MP3 as-is
	AudioTranscodeAAC AudioProcessingMode = "transcode_aac" // Transcode to AAC
	AudioTranscodeMP3 AudioProcessingMode = "transcode_mp3" // Transcode to MP3
)

// EgressConfig holds the egress agent configuration
type EgressConfig struct {
	OutputDir          string              // HLS output directory
	SegmentDuration    int                 // Segment duration in seconds
	JitterBufferMs     int                 // Jitter buffer size in milliseconds
	VideoPort          int                 // UDP port for video RTP
	AudioPort          int                 // UDP port for audio RTP
	AudioMode          AudioProcessingMode // Audio processing mode
	AACBitrate         int                 // AAC bitrate in kbps
	MP3Bitrate         int                 // MP3 bitrate in kbps
	EnableScreenshots  bool                // Enable screenshot extraction
	ScreenshotInterval int                 // Screenshot interval in seconds
	S3Config           *S3Config           // Optional S3 configuration
}

// S3Config holds S3 storage configuration
type S3Config struct {
	Endpoint  string
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
}

// EgressHandler handles egress jobs
type EgressHandler struct {
	agent.BaseHandler // Embed base handler for default implementations

	config   *EgressConfig
	sessions map[string]*RecordingSession
	mu       sync.RWMutex
}

// RecordingSession handles individual room recordings
type RecordingSession struct {
	job       *livekit.Job
	room      *lksdk.Room
	config    *EgressConfig
	pipeline  *PipelineManager
	rtpRouter *RTPRouter
	cancel    context.CancelFunc
	mu        sync.RWMutex
}

// NewEgressHandler creates a new egress handler
func NewEgressHandler(config *EgressConfig) *EgressHandler {
	return &EgressHandler{
		config:   config,
		sessions: make(map[string]*RecordingSession),
	}
}

// OnJobRequest implements agent.UniversalHandler
func (h *EgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	// Accept all room recording jobs
	if job.Type != livekit.JobType_JT_ROOM {
		return false, nil
	}

	return true, &agent.JobMetadata{
		ParticipantIdentity: fmt.Sprintf("egress-agent-%s", job.Id),
		ParticipantName:     "HLS Egress Agent",
		ParticipantMetadata: `{"agent_type": "egress"}`,
	}
}

// OnJobAssigned implements agent.UniversalHandler
func (h *EgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	log.Printf("Job assigned: %s for room: %s", jobCtx.Job.Id, jobCtx.Job.Room.Name)

	// Create recording session
	session := NewRecordingSession(jobCtx, h.config)

	h.mu.Lock()
	h.sessions[jobCtx.Job.Id] = session
	h.mu.Unlock()

	// Start the recording session
	return session.Start(ctx)
}

// OnJobTerminated implements agent.UniversalHandler
func (h *EgressHandler) OnJobTerminated(ctx context.Context, jobID string) {
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
func (h *EgressHandler) Shutdown() {
	h.mu.Lock()
	sessions := make([]*RecordingSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.sessions = make(map[string]*RecordingSession)
	h.mu.Unlock()

	// Stop all sessions
	for _, session := range sessions {
		session.Stop()
	}
}

// NewRecordingSession creates a new recording session
func NewRecordingSession(jobCtx *agent.JobContext, config *EgressConfig) *RecordingSession {
	return &RecordingSession{
		job:    jobCtx.Job,
		room:   jobCtx.Room,
		config: config,
	}
}

// Start begins the recording session
func (s *RecordingSession) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Create RTP router
	router, err := NewRTPRouter(s.config.VideoPort, s.config.AudioPort)
	if err != nil {
		return fmt.Errorf("failed to create RTP router: %w", err)
	}
	s.rtpRouter = router

	// Create and start GStreamer pipeline
	pipeline := NewPipelineManager(s.config)
	if err := pipeline.Start(); err != nil {
		router.Close()
		return fmt.Errorf("failed to start pipeline: %w", err)
	}
	s.pipeline = pipeline

	// Set up track handling callbacks
	s.room.Callback.OnTrackSubscribed = s.OnTrackSubscribed
	s.room.Callback.OnParticipantDisconnected = s.OnParticipantDisconnected

	log.Printf("Recording session started for room: %s", s.job.Room.Name)
	return nil
}

// Stop ends the recording session
func (s *RecordingSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}

	if s.pipeline != nil {
		s.pipeline.Stop()
		s.pipeline = nil
	}

	if s.rtpRouter != nil {
		s.rtpRouter.Close()
		s.rtpRouter = nil
	}

	log.Printf("Recording session stopped for room: %s", s.job.Room.Name)
}

// OnTrackSubscribed handles new track subscriptions
func (s *RecordingSession) OnTrackSubscribed(
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

	// Forward RTP packets to GStreamer
	go s.forwardRTPPackets(track)
}

// OnParticipantDisconnected handles participant disconnection
func (s *RecordingSession) OnParticipantDisconnected(p *lksdk.RemoteParticipant) {
	log.Printf("Participant disconnected: %s", p.Identity())

	// Check if any participants remain
	if len(s.room.GetRemoteParticipants()) == 0 {
		log.Println("No participants remaining, keeping pipeline running for potential reconnections")
	}
}

// forwardRTPPackets reads RTP packets from track and forwards to GStreamer
func (s *RecordingSession) forwardRTPPackets(track *webrtc.TrackRemote) {
	for {
		// Read RTP packet from WebRTC
		packet, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("Error reading RTP from track %s: %v", track.ID(), err)
			break
		}

		// Determine track kind
		kind := TrackKindVideo
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			kind = TrackKindAudio
		}

		// Route to GStreamer via UDP
		if err := s.rtpRouter.RoutePacket(packet, kind); err != nil {
			log.Printf("Failed to route packet: %v", err)
		}
	}
}

// isCodecSupported checks if a codec is supported for zero-transcode
func (s *RecordingSession) isCodecSupported(codec webrtc.RTPCodecParameters) bool {
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