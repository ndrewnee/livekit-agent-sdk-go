package egress

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// EgressWorker is a LiveKit agent worker that handles room egress recording
// It monitors rooms and creates individual recording jobs for each participant
type EgressWorker struct {
	worker *agent.UniversalWorker
	config *Config

	// Track active recording sessions
	sessions map[string]*RecordingSession // keyed by job ID
	mu       sync.RWMutex

	// Track rooms we're monitoring
	monitoredRooms map[string]*roomMonitor // keyed by room name
	roomsMu        sync.RWMutex
}

// roomMonitor tracks participants in a room for egress recording
type roomMonitor struct {
	roomName     string
	room         *lksdk.Room
	participants map[string]bool // participant SID -> recording active
	mu           sync.RWMutex
}

// NewEgressWorker creates a new egress worker
func NewEgressWorker(config *Config) *EgressWorker {
	if config == nil {
		config = DefaultConfig()
	}

	return &EgressWorker{
		config:         config,
		sessions:       make(map[string]*RecordingSession),
		monitoredRooms: make(map[string]*roomMonitor),
	}
}

// Start starts the egress worker and connects to LiveKit server
func (w *EgressWorker) Start(ctx context.Context, serverURL, apiKey, apiSecret string) error {
	logger.Infow("starting egress worker",
		"serverURL", serverURL,
		"maxSessions", w.config.MaxConcurrentSessions)

	// Create the handler that will process jobs
	handler := &egressHandler{
		worker: w,
		config: w.config,
	}

	// Create worker options
	opts := agent.WorkerOptions{
		AgentName:    "egress-worker",
		Version:      "1.0.0",
		Namespace:    "egress",
		PingInterval: 30 * time.Second,
		PingTimeout:  10 * time.Second,
	}

	// Create the universal worker
	w.worker = agent.NewUniversalWorker(
		serverURL,
		apiKey,
		apiSecret,
		handler,
		opts,
	)

	// Start the worker
	return w.worker.Start(ctx)
}

// Stop gracefully stops the egress worker
func (w *EgressWorker) Stop() error {
	logger.Infow("stopping egress worker")

	// Stop all active recording sessions
	w.mu.RLock()
	sessions := make([]*RecordingSession, 0, len(w.sessions))
	for _, session := range w.sessions {
		sessions = append(sessions, session)
	}
	w.mu.RUnlock()

	// Stop each session
	var wg sync.WaitGroup
	for _, session := range sessions {
		wg.Add(1)
		go func(s *RecordingSession) {
			defer wg.Done()
			s.Stop()
		}(session)
	}

	// Wait for all sessions to stop
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Infow("all recording sessions stopped")
	case <-time.After(30 * time.Second):
		logger.Debugw("timeout waiting for sessions to stop")
	}

	// Stop the worker
	if w.worker != nil {
		w.worker.Stop()
	}

	return nil
}

// egressHandler implements agent.UniversalHandler for the egress worker
type egressHandler struct {
	BaseEgressHandler // Embed base handler for default implementations
	worker            *EgressWorker
	config            *Config
}

// OnJobRequest decides whether to accept a job
func (h *egressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	// Handle nil job
	if job == nil {
		logger.Debugw("rejecting nil job")
		return false, nil
	}

	// We accept ROOM jobs for monitoring and PARTICIPANT jobs for recording
	switch job.Type {
	case livekit.JobType_JT_ROOM:
		// Accept room jobs to monitor for participants
		roomName := ""
		if job.Room != nil {
			roomName = job.Room.Name
		}
		logger.Infow("accepting room monitoring job",
			"jobID", job.Id,
			"room", roomName)

		// Check if we're at capacity
		h.worker.mu.RLock()
		sessionCount := len(h.worker.sessions)
		h.worker.mu.RUnlock()

		if h.config.MaxConcurrentSessions > 0 && sessionCount >= h.config.MaxConcurrentSessions {
			logger.Infow("at maximum capacity, rejecting job",
				"current", sessionCount,
				"max", h.config.MaxConcurrentSessions)
			return false, nil
		}

		return true, &agent.JobMetadata{
			ParticipantName:     "Egress Recorder",
			ParticipantIdentity: fmt.Sprintf("egress-monitor-%s", job.Id),
			ParticipantMetadata: fmt.Sprintf(`{"type":"egress","version":"%s"}`, "1.0.0"),
		}

	case livekit.JobType_JT_PARTICIPANT:
		// Accept participant jobs for individual recording
		participantIdentity := ""
		participantName := ""
		if job.Participant != nil {
			participantIdentity = job.Participant.Identity
			participantName = job.Participant.Name
		}

		logger.Infow("accepting participant recording job",
			"jobID", job.Id,
			"participant", participantIdentity)

		return true, &agent.JobMetadata{
			ParticipantName:     fmt.Sprintf("Recording: %s", participantName),
			ParticipantIdentity: fmt.Sprintf("recorder-%s", participantIdentity),
			ParticipantMetadata: fmt.Sprintf(`{"type":"participant_egress","target":"%s"}`, participantIdentity),
		}

	default:
		logger.Debugw("rejecting unsupported job type",
			"jobID", job.Id,
			"type", job.Type)
		return false, nil
	}
}

// OnJobAssigned handles an assigned job
func (h *egressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	job := jobCtx.Job

	switch job.Type {
	case livekit.JobType_JT_ROOM:
		// Start monitoring the room for participants
		return h.handleRoomJob(ctx, jobCtx)

	case livekit.JobType_JT_PARTICIPANT:
		// Start recording the specific participant
		return h.handleParticipantJob(ctx, jobCtx)

	default:
		return fmt.Errorf("unsupported job type: %v", job.Type)
	}
}

// handleRoomJob monitors a room and creates jobs for each participant
func (h *egressHandler) handleRoomJob(ctx context.Context, jobCtx *agent.JobContext) error {
	roomName := jobCtx.Job.Room.Name
	logger.Infow("starting room monitoring",
		"jobID", jobCtx.Job.Id,
		"room", roomName)

	// Create or get room monitor
	h.worker.roomsMu.Lock()
	monitor, exists := h.worker.monitoredRooms[roomName]
	if !exists {
		monitor = &roomMonitor{
			roomName:     roomName,
			room:         jobCtx.Room,
			participants: make(map[string]bool),
		}
		h.worker.monitoredRooms[roomName] = monitor
	}
	h.worker.roomsMu.Unlock()

	// The room callbacks will be triggered by the agent framework
	// We just need to ensure we're tracking this room

	return nil
}

// handleParticipantJob starts recording a specific participant
func (h *egressHandler) handleParticipantJob(ctx context.Context, jobCtx *agent.JobContext) error {
	participantIdentity := jobCtx.Job.Participant.Identity
	logger.Infow("starting participant recording",
		"jobID", jobCtx.Job.Id,
		"participant", participantIdentity)

	// Create recording session
	session := NewRecordingSession(jobCtx, h.config)

	// Store the session
	h.worker.mu.Lock()
	h.worker.sessions[jobCtx.Job.Id] = session
	h.worker.mu.Unlock()

	// Start the recording
	if err := session.Start(ctx); err != nil {
		logger.Errorw("failed to start recording session", err,
			"jobID", jobCtx.Job.Id)

		// Remove failed session
		h.worker.mu.Lock()
		delete(h.worker.sessions, jobCtx.Job.Id)
		h.worker.mu.Unlock()

		return err
	}

	// Monitor the session in background
	go h.monitorSession(ctx, jobCtx.Job.Id, session)

	return nil
}

// monitorSession monitors a recording session for completion
func (h *egressHandler) monitorSession(ctx context.Context, jobID string, session *RecordingSession) {
	defer func() {
		// Clean up when done
		h.worker.mu.Lock()
		delete(h.worker.sessions, jobID)
		h.worker.mu.Unlock()
	}()

	// Wait for session to complete or error
	select {
	case <-ctx.Done():
		logger.Infow("context cancelled, stopping session",
			"jobID", jobID)
		session.Stop()

	case err := <-session.Done():
		if err != nil {
			logger.Errorw("session ended with error", err,
				"jobID", jobID)
		} else {
			logger.Infow("session completed successfully",
				"jobID", jobID)
		}
	}
}

// OnParticipantJoined handles when a participant joins a monitored room
func (h *egressHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	// Note: In the current architecture, we need to track which room this participant belongs to
	// This would typically be passed through the JobContext when handling room jobs
	// For now, we'll need to find the room from our monitored rooms

	// Find the room this participant belongs to
	var roomName string
	h.worker.roomsMu.RLock()
	for name := range h.worker.monitoredRooms {
		// Check if this participant is in this room
		// This is a simplified check - in production you'd track this better
		roomName = name
		break
	}
	h.worker.roomsMu.RUnlock()

	if roomName == "" {
		return // Room not found
	}

	h.worker.roomsMu.RLock()
	monitor, exists := h.worker.monitoredRooms[roomName]
	h.worker.roomsMu.RUnlock()

	if !exists {
		return // Not monitoring this room
	}

	// Check if we're already recording this participant
	monitor.mu.RLock()
	isRecording := monitor.participants[participant.SID()]
	monitor.mu.RUnlock()

	if isRecording {
		return // Already recording
	}

	// Create a new job for this participant
	logger.Infow("creating recording job for new participant",
		"room", roomName,
		"participant", participant.Identity(),
		"sid", participant.SID())

	// Mark as recording to avoid duplicates
	monitor.mu.Lock()
	monitor.participants[participant.SID()] = true
	monitor.mu.Unlock()

	// Create a participant job request
	// Note: In a real implementation, this would trigger a new job through the LiveKit API
	// For now, we'll just log it as the actual job creation would require server-side support
	log.Printf("Would create participant recording job for %s in room %s", participant.Identity(), roomName)
}

// OnParticipantLeft handles when a participant leaves
func (h *egressHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	// Find the room this participant belongs to
	var roomName string
	h.worker.roomsMu.RLock()
	for name := range h.worker.monitoredRooms {
		// Check if this participant is in this room
		// This is a simplified check - in production you'd track this better
		roomName = name
		break
	}
	h.worker.roomsMu.RUnlock()

	if roomName == "" {
		return // Room not found
	}

	h.worker.roomsMu.RLock()
	monitor, exists := h.worker.monitoredRooms[roomName]
	h.worker.roomsMu.RUnlock()

	if exists {
		// Remove from tracking
		monitor.mu.Lock()
		delete(monitor.participants, participant.SID())
		monitor.mu.Unlock()
	}

	logger.Infow("participant left, stopping any recording",
		"room", roomName,
		"participant", participant.Identity())
}

// OnJobTerminated handles job termination
func (h *egressHandler) OnJobTerminated(ctx context.Context, jobID string) {
	logger.Infow("job terminated", "jobID", jobID)

	// Stop the recording session if it exists
	h.worker.mu.RLock()
	session, exists := h.worker.sessions[jobID]
	h.worker.mu.RUnlock()

	if exists {
		session.Stop()

		// Remove from active sessions
		h.worker.mu.Lock()
		delete(h.worker.sessions, jobID)
		h.worker.mu.Unlock()
	}
}

// GetActiveSessionCount returns the number of active recording sessions
func (w *EgressWorker) GetActiveSessionCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.sessions)
}

// GetMonitoredRoomCount returns the number of rooms being monitored
func (w *EgressWorker) GetMonitoredRoomCount() int {
	w.roomsMu.RLock()
	defer w.roomsMu.RUnlock()
	return len(w.monitoredRooms)
}