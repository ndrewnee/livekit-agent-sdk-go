package egress

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// RecordingSession manages a single room recording session
// This coordinates between LiveKit room events and the GStreamer pipeline
type RecordingSession struct {
	jobCtx      *agent.JobContext
	config      *Config
	room        *lksdk.Room

	// Components
	trackManager       *TrackManager
	trackSubscriber    *TrackSubscriber
	participantTracker *ParticipantTracker
	rtpRouter          *RTPRouter
	pipeline           *pipeline.DirectPipeline
	codecTracker       *CodecTracker
	connManager        *ConnectionManager

	// State management
	state         SessionState
	stateMu       sync.RWMutex
	startTime     time.Time
	stopTime      time.Time

	// Graceful shutdown
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan error
	stopOnce      sync.Once

	// Statistics
	stats         SessionStats
	lastStatsTime time.Time

	// Pending tracks (subscribed before Start())
	pendingTracks   []*webrtc.TrackRemote
	pendingTracksMu sync.Mutex
}

// SessionState represents the state of a recording session
type SessionState int

const (
	StateIdle SessionState = iota
	StateStarting
	StateRecording
	StateStopping
	StateStopped
	StateError
)

// SessionStats holds statistics for a recording session
type SessionStats struct {
	StartTime          time.Time `json:"start_time"`
	Duration           int64     `json:"duration_seconds"`
	TracksSubscribed   int32     `json:"tracks_subscribed"`
	TracksUnsubscribed int32     `json:"tracks_unsubscribed"`
	PacketsReceived    uint64    `json:"packets_received"`
	PacketsDropped     uint64    `json:"packets_dropped"`
	BytesReceived      uint64    `json:"bytes_received"`
	Participants       int32     `json:"participants"`
	Reconnections      int32     `json:"reconnections"`
	Errors             int32     `json:"errors"`
}

// NewRecordingSession creates a new recording session
func NewRecordingSession(jobCtx *agent.JobContext, config *Config) *RecordingSession {
	ctx, cancel := context.WithCancel(context.Background())

	// Handle nil config
	if config == nil {
		config = DefaultConfig()
	}

	// Initialize session
	session := &RecordingSession{
		jobCtx: jobCtx,
		config: config,
		state:  StateIdle,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan error, 1),
	}

	// Only set room and codec tracker if jobCtx is not nil
	if jobCtx != nil {
		session.room = jobCtx.Room
		if jobCtx.Job != nil {
			session.codecTracker = NewCodecTracker(jobCtx.Job.Id)
		}
	}

	session.lastStatsTime = time.Now()
	return session
}

// getJobID safely returns the job ID or empty string if nil
func (s *RecordingSession) getJobID() string {
	if s.jobCtx != nil && s.jobCtx.Job != nil {
		return s.jobCtx.Job.Id
	}
	return ""
}

// getRoomName safely returns the room name or empty string if nil
func (s *RecordingSession) getRoomName() string {
	if s.jobCtx != nil && s.jobCtx.Job != nil && s.jobCtx.Job.Room != nil {
		return s.jobCtx.Job.Room.Name
	}
	return ""
}

// Start starts the recording session
func (s *RecordingSession) Start(ctx context.Context) error {
	s.stateMu.Lock()
	if s.state != StateIdle {
		s.stateMu.Unlock()
		return fmt.Errorf("session already started (state: %v)", s.state)
	}
	s.state = StateStarting
	s.startTime = time.Now()
	s.stats.StartTime = s.startTime
	s.stateMu.Unlock()

	logger.Infow("starting recording session",
		"jobID", s.getJobID(),
		"roomName", s.getRoomName())

	// Initialize components
	if err := s.initializeComponents(); err != nil {
		s.setState(StateError)
		return fmt.Errorf("failed to initialize components: %w", err)
	}

	// Start RTP router BEFORE pipeline so tracks that subscribe during pipeline.Start() can be forwarded
	fmt.Printf("===== About to start RTP router =====\n")
	if err := s.rtpRouter.Start(); err != nil {
		s.setState(StateError)
		return fmt.Errorf("failed to start RTP router: %w", err)
	}
	fmt.Printf("===== RTP router started successfully =====\n")

	// Start the pipeline (this may block waiting for PAUSED state)
	if err := s.pipeline.Start(); err != nil {
		s.setState(StateError)
		s.rtpRouter.Stop() // Stop router if pipeline fails
		return fmt.Errorf("failed to start pipeline: %w", err)
	}

	// Forward any tracks that were subscribed before Start()
	s.pendingTracksMu.Lock()
	pendingCount := len(s.pendingTracks)
	if pendingCount > 0 {
		fmt.Printf("===== Forwarding %d pending tracks to RTP router =====\n", pendingCount)
		for _, track := range s.pendingTracks {
			s.rtpRouter.ForwardTrack(track)
			fmt.Printf("===== Pending track %s forwarded (kind: %v) =====\n", track.ID(), track.Kind())
		}
		s.pendingTracks = nil // Clear pending tracks
	} else {
		fmt.Printf("===== No pending tracks to forward =====\n")
	}
	s.pendingTracksMu.Unlock()

	// Subscribe to existing tracks
	s.subscribeToExistingTracks()

	// Update state
	s.setState(StateRecording)

	// Start monitoring
	go s.monitor()

	logger.Infow("recording session started successfully",
		"jobID", s.getJobID(),
		"participants", len(s.room.GetRemoteParticipants()))

	return nil
}

// initializeComponents initializes all session components
func (s *RecordingSession) initializeComponents() error {
	// Create connection manager
	s.connManager = NewConnectionManager(s.room, &s.config.NetworkConfig)
	s.connManager.SetCallbacks(
		func() { logger.Infow("connection established", "jobID", s.getJobID()) },
		func(err error) { logger.Errorw("connection lost", err, "jobID", s.getJobID()) },
		func() { logger.Infow("reconnecting", "jobID", s.getJobID()) },
		func(err error) {
			logger.Errorw("connection failed", err, "jobID", s.getJobID())
			s.done <- err
		},
	)

	// Start connection manager
	if err := s.connManager.Start(); err != nil {
		return fmt.Errorf("failed to start connection manager: %w", err)
	}

	// Create track manager - needs router and codec tracker
	// TODO: Need to create RTP router first
	// s.trackManager = NewTrackManager(s.getJobID(), s.rtpRouter, s.codecTracker)

	// Create participant tracker
	s.participantTracker = NewParticipantTracker()
	s.participantTracker.SetCallbacks(
		func(participant *ParticipantInfo) {
			logger.Infow("participant joined recording",
				"identity", participant.Identity,
				"tracks", participant.TrackCount)
		},
		func(participant *ParticipantInfo) {
			logger.Infow("participant left recording",
				"identity", participant.Identity,
				"duration", time.Since(participant.JoinedAt))
		},
	)

	// Create quality settings
	// TODO: Add AudioQuality to Config or use default
	qualitySettings := NewQualitySettings(s.config.VideoQuality, AudioQualityHigh)
	if s.config.NetworkConfig.AdaptiveBitrate {
		qualitySettings.SetAdaptiveStream(true)
		qualitySettings.SetBitrateRange(
			uint32(s.config.NetworkConfig.MinBitrate*1000),
			uint32(s.config.NetworkConfig.MaxBitrate*1000))
	}

	// Create track subscriber
	s.trackSubscriber = NewTrackSubscriber(&s.config.RecordingConfig, s.room)
	s.trackSubscriber.SetQualitySettings(qualitySettings)
	s.trackSubscriber.SetCallbacks(
		func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication) {
			// Forward to RTP router when track is subscribed
			if s.rtpRouter != nil {
				s.rtpRouter.ForwardTrack(track)
			}
		},
		func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication) {
			// Track unsubscribed
			logger.Debugw("track unsubscribed callback", "trackID", publication.SID())
		},
	)

	// Create pipeline configuration
	pipelineConfig := &pipeline.Config{
		OutputDir:          s.config.PipelineConfig.OutputDir,
		SegmentDuration:    s.config.PipelineConfig.SegmentDuration,
		JitterBufferMs:     s.config.PipelineConfig.JitterBufferMs,
		AudioMode:          pipeline.AudioPassThrough, // Zero-transcode per SPECS.md
		EnableScreenshots:  s.config.RecordingConfig.EnableScreenshots,
		ScreenshotInterval: s.config.RecordingConfig.ScreenshotInterval,
		AllowAsyncStart:    s.config.PipelineConfig.AllowAsyncStart,
	}

	// Validate pipeline config
	if err := pipeline.ValidateConfig(pipelineConfig); err != nil {
		return fmt.Errorf("invalid pipeline config: %w", err)
	}

	// Create the pipeline
	sessionID := fmt.Sprintf("%s-%d", s.getJobID(), time.Now().Unix())
	directPipeline, err := pipeline.NewDirectPipeline(pipelineConfig, sessionID)
	if err != nil {
		return fmt.Errorf("failed to create pipeline: %w", err)
	}
	s.pipeline = directPipeline

	// Create RTP router
	s.rtpRouter = NewRTPRouter(s.config, s.pipeline)

	return nil
}

// subscribeToExistingTracks subscribes to tracks already in the room
func (s *RecordingSession) subscribeToExistingTracks() {
	participants := s.room.GetRemoteParticipants()

	logger.Infow("subscribing to existing tracks",
		"jobID", s.getJobID(),
		"participantCount", len(participants))

	for _, participant := range participants {
		// Get track publications from participant
		publications := participant.TrackPublications()

		logger.Infow("participant tracks",
			"identity", participant.Identity(),
			"trackCount", len(publications))

		for _, pub := range publications {
			// Cast to RemoteTrackPublication
			publication, ok := pub.(*lksdk.RemoteTrackPublication)
			if !ok {
				continue
			}

			// Check if track is already subscribed
			if publication.IsSubscribed() {
				logger.Debugw("track already subscribed",
					"trackID", publication.SID(),
					"participantID", participant.SID())
				continue
			}

			// Check if we should subscribe to this track
			if s.shouldSubscribeToTrack(publication) {
				logger.Infow("subscribing to track",
					"trackID", publication.SID(),
					"participantID", participant.SID(),
					"kind", publication.Kind(),
					"name", publication.Name())

				// Subscribe to the track
				if err := publication.SetSubscribed(true); err != nil {
					logger.Errorw("failed to subscribe to track", err,
						"trackID", publication.SID())
					atomic.AddInt32(&s.stats.Errors, 1)
				} else {
					logger.Infow("successfully subscribed to track",
						"trackID", publication.SID())
				}
			}
		}
	}
}

// shouldSubscribeToTrack determines if we should subscribe to a track
func (s *RecordingSession) shouldSubscribeToTrack(track *lksdk.RemoteTrackPublication) bool {
	// Check track kind
	switch track.Kind() {
	case lksdk.TrackKindVideo:
		if !s.config.RecordingConfig.RecordVideo {
			return false
		}
		// Check if it's screen share
		if track.Source() == livekit.TrackSource_SCREEN_SHARE && !s.config.RecordingConfig.RecordScreenShare {
			return false
		}
	case lksdk.TrackKindAudio:
		if !s.config.RecordingConfig.RecordAudio {
			return false
		}
	default:
		return false
	}

	// Check preferred tracks
	if s.config.RecordingConfig.PreferredVideoTrack != "" && track.Kind() == lksdk.TrackKindVideo {
		return track.SID() == s.config.RecordingConfig.PreferredVideoTrack
	}
	if s.config.RecordingConfig.PreferredAudioTrack != "" && track.Kind() == lksdk.TrackKindAudio {
		return track.SID() == s.config.RecordingConfig.PreferredAudioTrack
	}

	return true
}

// Stop stops the recording session
func (s *RecordingSession) Stop() {
	s.stopOnce.Do(func() {
		// Get job ID safely
		jobID := ""
		if s.jobCtx != nil && s.jobCtx.Job != nil {
			jobID = s.getJobID()
		}
		logger.Infow("stopping recording session", "jobID", jobID)

		s.setState(StateStopping)
		s.stopTime = time.Now()

		// Stop router first
		if s.rtpRouter != nil {
			s.rtpRouter.Stop()
		}

		// Stop pipeline
		if s.pipeline != nil {
			s.pipeline.Stop()
		}

		// Stop connection manager
		if s.connManager != nil {
			s.connManager.Stop()
		}

		// Cancel context
		if s.cancel != nil {
			s.cancel()
		}

		// Update stats
		s.stats.Duration = int64(s.stopTime.Sub(s.startTime).Seconds())

		s.setState(StateStopped)
		close(s.done)

		logger.Infow("recording session stopped",
			"jobID", jobID,
			"duration", s.stats.Duration)
	})
}

// Done returns a channel that's closed when the session ends
func (s *RecordingSession) Done() <-chan error {
	return s.done
}

// setState updates the session state
func (s *RecordingSession) setState(state SessionState) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.state = state
}

// GetState returns the current session state
func (s *RecordingSession) GetState() SessionState {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state
}

// GetStats returns session statistics
func (s *RecordingSession) GetStats() SessionStats {
	stats := s.stats
	if s.GetState() == StateRecording {
		stats.Duration = int64(time.Since(s.startTime).Seconds())
	}

	// Add router stats if available
	if s.rtpRouter != nil {
		routerStats := s.rtpRouter.GetStats()
		stats.PacketsReceived = routerStats.PacketsReceived
		stats.PacketsDropped = routerStats.PacketsDropped
		stats.BytesReceived = routerStats.BytesReceived
	}

	// Add current participant count
	if s.room != nil {
		stats.Participants = int32(len(s.room.GetRemoteParticipants()))
	}

	return stats
}

// monitor monitors the session for idle timeout and other conditions
func (s *RecordingSession) monitor() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	lastActivity := time.Now()

	for {
		select {
		case <-s.ctx.Done():
			return

		case <-ticker.C:
			// Check idle timeout
			if s.config.RecordingConfig.IdleTimeout > 0 {
				participants := len(s.room.GetRemoteParticipants())
				if participants < s.config.RecordingConfig.MinParticipants {
					if time.Since(lastActivity) > s.config.RecordingConfig.IdleTimeout {
						logger.Infow("idle timeout reached, stopping recording",
							"jobID", s.getJobID(),
							"participants", participants,
							"idleTime", time.Since(lastActivity))
						s.Stop()
						return
					}
				} else {
					lastActivity = time.Now()
				}
			}

			// Check max duration
			if s.config.RecordingConfig.MaxDuration > 0 {
				if time.Since(s.startTime) > s.config.RecordingConfig.MaxDuration {
					logger.Infow("max duration reached, stopping recording",
						"jobID", s.getJobID(),
						"duration", time.Since(s.startTime))
					s.Stop()
					return
				}
			}

			// Log statistics periodically
			s.logStatistics()
		}
	}
}

// logStatistics logs current session statistics
func (s *RecordingSession) logStatistics() {
	stats := s.GetStats()

	logger.Debugw("session statistics",
		"jobID", s.getJobID(),
		"duration", stats.Duration,
		"participants", stats.Participants,
		"tracks", atomic.LoadInt32(&stats.TracksSubscribed),
		"packets", stats.PacketsReceived,
		"dropped", stats.PacketsDropped,
		"bytes", stats.BytesReceived)
}

// Track subscription callbacks
func (s *RecordingSession) OnTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	// FORCE REBUILD - this comment changed
	fmt.Printf("\n\n===== ON TRACK SUBSCRIBED CALLED IN RECORDING SESSION =====\n\n")
	logger.Infow("!!!! TRACK SUBSCRIBED IN RECORDING SESSION !!!!",
		"trackID", publication.SID(),
		"participantID", participant.SID(),
		"kind", track.Kind(),
		"codec", track.Codec().MimeType)

	atomic.AddInt32(&s.stats.TracksSubscribed, 1)

	// Skip codec validation if codec tracker not initialized
	if s.codecTracker == nil {
		logger.Warnw("codec tracker not initialized, skipping validation", nil,
			"trackID", publication.SID())
	} else {
		// Verify codec
		codec := track.Codec()
		logger.Infow("DEBUG: Validating codec", "trackID", publication.SID(), "kind", track.Kind(), "mime", codec.MimeType)
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			if err := s.codecTracker.ValidateVideoCodec(codec); err != nil {
				logger.Errorw("unsupported video codec", err,
					"codec", codec.MimeType)
				atomic.AddInt32(&s.stats.Errors, 1)
				logger.Infow("DEBUG: Returning early due to invalid video codec")
				return
			}
			logger.Infow("DEBUG: Video codec validated successfully")
		} else if track.Kind() == webrtc.RTPCodecTypeAudio {
			if err := s.codecTracker.ValidateAudioCodec(codec); err != nil {
				logger.Errorw("unsupported audio codec", err,
					"codec", codec.MimeType)
				atomic.AddInt32(&s.stats.Errors, 1)
				logger.Infow("DEBUG: Returning early due to invalid audio codec")
				return
			}
			logger.Infow("DEBUG: Audio codec validated successfully")
		}
	}

	// Forward track to RTP router for packet forwarding
	if s.rtpRouter != nil {
		fmt.Printf("===== Forwarding track %s to RTP router =====\n", publication.SID())
		s.rtpRouter.ForwardTrack(track)
		fmt.Printf("===== Track %s forwarded to RTP router =====\n", publication.SID())
	} else {
		// RTP router not ready yet - queue track for later
		s.pendingTracksMu.Lock()
		s.pendingTracks = append(s.pendingTracks, track)
		s.pendingTracksMu.Unlock()
		fmt.Printf("===== Track %s queued (RTP router not ready yet) =====\n", publication.SID())
	}
}

func (s *RecordingSession) OnTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Infow("track unsubscribed",
		"trackID", publication.SID(),
		"participantID", participant.SID())

	atomic.AddInt32(&s.stats.TracksUnsubscribed, 1)

	// Unregister from manager
	// TODO: Implement UnregisterTrack method
	// s.trackManager.UnregisterTrack(track)
}

func (s *RecordingSession) OnTrackPublished(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Debugw("track published",
		"trackID", publication.SID(),
		"participantID", participant.SID())

	// Auto-subscribe if configured
	if s.config.RecordingConfig.AutoStart && s.shouldSubscribeToTrack(publication) {
		if err := publication.SetSubscribed(true); err != nil {
			logger.Errorw("failed to subscribe to new track", err,
				"trackID", publication.SID())
			atomic.AddInt32(&s.stats.Errors, 1)
		}
	}
}

func (s *RecordingSession) OnTrackUnpublished(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Debugw("track unpublished",
		"trackID", publication.SID(),
		"participantID", participant.SID())
}

// Participant callbacks
func (s *RecordingSession) OnParticipantConnected(participant *lksdk.RemoteParticipant) {
	logger.Infow("participant connected",
		"participantID", participant.SID(),
		"identity", participant.Identity())

	// Track participant
	if s.participantTracker != nil {
		s.participantTracker.OnParticipantConnected(participant)
	}
}

func (s *RecordingSession) OnParticipantDisconnected(participant *lksdk.RemoteParticipant) {
	logger.Infow("participant disconnected",
		"participantID", participant.SID(),
		"identity", participant.Identity())

	// Track participant
	if s.participantTracker != nil {
		s.participantTracker.OnParticipantDisconnected(participant)
	}
}

// Connection callbacks
func (s *RecordingSession) OnConnectionStateChanged(state lksdk.ConnectionState) {
	logger.Infow("connection state changed", "state", state)

	if state == lksdk.ConnectionStateReconnecting {
		atomic.AddInt32(&s.stats.Reconnections, 1)
	}
}

func (s *RecordingSession) OnDisconnected() {
	logger.Debugw("disconnected from room", "jobID", s.getJobID())

	// Stop recording on disconnect
	s.done <- fmt.Errorf("disconnected from room")
	s.Stop()
}

func (s *RecordingSession) OnReconnected() {
	logger.Infow("reconnected to room", "jobID", s.getJobID())
}

func (s *RecordingSession) OnDataPacketReceived(data []byte, participant *lksdk.RemoteParticipant) {
	// Handle data packets if needed for control messages
	logger.Debugw("data packet received",
		"size", len(data),
		"participant", participant.Identity())
}

// SetRoom updates the room reference (needed when room is created after session)
func (s *RecordingSession) SetRoom(room *lksdk.Room) {
	s.room = room
	if s.jobCtx != nil {
		s.jobCtx.Room = room
	}
}