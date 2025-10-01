package egress

import (
	"context"
	"fmt"
	"sync"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

// EgressHandler implements the UniversalHandler interface for HLS egress recording
// This is the main entry point for the egress agent per PLAN.md Milestone 2
// DEPRECATED: Use EgressWorker instead which provides better integration with the agent framework
type EgressHandler struct {
	BaseEgressHandler // Embed base handler for default implementations
	config            *Config
	sessions          map[string]*RecordingSession // Active recording sessions indexed by job ID
	mu                sync.RWMutex

	// Statistics
	totalJobs     int64
	acceptedJobs  int64
	rejectedJobs  int64
	completedJobs int64
	failedJobs    int64
}

// NewEgressHandler creates a new egress handler with the given configuration
func NewEgressHandler(config *Config) *EgressHandler {
	// Use default config if nil
	if config == nil {
		config = DefaultConfig()
	}
	return &EgressHandler{
		config:   config,
		sessions: make(map[string]*RecordingSession),
	}
}

// OnJobRequest decides whether to accept a room recording job
// Per SPECS.md: Accept room recording jobs and provide participant metadata
func (h *EgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	// Track total jobs
	h.mu.Lock()
	h.totalJobs++
	h.mu.Unlock()

	// Handle nil job
	if job == nil {
		logger.Debugw("rejecting nil job")
		h.mu.Lock()
		h.rejectedJobs++
		h.mu.Unlock()
		return false, nil
	}

	// Only accept room recording jobs
	if job.Type != livekit.JobType_JT_ROOM {
		logger.Debugw("rejecting non-room job", "jobID", job.Id, "type", job.Type)
		h.mu.Lock()
		h.rejectedJobs++
		h.mu.Unlock()
		return false, nil
	}

	// Check if we're at capacity
	h.mu.RLock()
	sessionCount := len(h.sessions)
	maxSessions := h.config.MaxConcurrentSessions
	h.mu.RUnlock()

	if maxSessions > 0 && sessionCount >= maxSessions {
		logger.Debugw("at maximum concurrent sessions, rejecting job",
			"jobID", job.Id,
			"current", sessionCount,
			"max", h.config.MaxConcurrentSessions)
		h.mu.Lock()
		h.rejectedJobs++
		h.mu.Unlock()
		return false, nil
	}

	// Accept the job with appropriate metadata
	metadata := &agent.JobMetadata{
		ParticipantIdentity: fmt.Sprintf("egress-agent-%s", job.Id),
		ParticipantName:     "HLS Egress Agent",
	}

	// Get room name safely
	roomName := ""
	if job.Room != nil {
		roomName = job.Room.Name
	}

	logger.Infow("accepting egress job",
		"jobID", job.Id,
		"roomName", roomName,
		"identity", metadata.ParticipantIdentity)

	h.mu.Lock()
	h.acceptedJobs++
	h.mu.Unlock()

	return true, metadata
}

// OnJobAssigned handles the assigned recording job
// This is called after the job has been accepted and assigned to this agent
func (h *EgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	// Get room name safely
	roomName := ""
	if jobCtx != nil && jobCtx.Job != nil && jobCtx.Job.Room != nil {
		roomName = jobCtx.Job.Room.Name
	}

	logger.Infow("job assigned",
		"jobID", jobCtx.Job.Id,
		"roomName", roomName)

	// Create a new recording session for this job
	session := NewRecordingSession(jobCtx, h.config)

	// Register the session
	h.mu.Lock()
	h.sessions[jobCtx.Job.Id] = session
	h.mu.Unlock()

	// Set up room event callbacks
	h.setupRoomCallbacks(jobCtx, session)

	// Start the recording session
	if err := session.Start(ctx); err != nil {
		logger.Errorw("failed to start recording session", err,
			"jobID", jobCtx.Job.Id)

		// Clean up on failure
		h.mu.Lock()
		delete(h.sessions, jobCtx.Job.Id)
		h.failedJobs++
		h.mu.Unlock()

		return fmt.Errorf("failed to start recording: %w", err)
	}

	// Monitor session in background
	go h.monitorSession(ctx, jobCtx.Job.Id, session)

	return nil
}

// setupRoomCallbacks configures room event callbacks for the recording session
func (h *EgressHandler) setupRoomCallbacks(jobCtx *agent.JobContext, session *RecordingSession) {
	// room := jobCtx.Room // Currently unused due to SDK limitations

	// TODO: The LiveKit SDK v2 doesn't expose room callbacks directly
	// These callbacks should be registered during room creation or
	// through event handlers exposed by the SDK

	// For now, commenting out direct callback assignment
	// The session will need to monitor room state through alternative means

	// Track subscription events
	// room.Callback.OnTrackSubscribed = session.OnTrackSubscribed
	// room.Callback.OnTrackUnsubscribed = session.OnTrackUnsubscribed

	// Track publication events (for monitoring)
	// room.Callback.OnTrackPublished = session.OnTrackPublished
	// room.Callback.OnTrackUnpublished = session.OnTrackUnpublished

	// Participant events
	// room.Callback.OnParticipantConnected = session.OnParticipantConnected
	// room.Callback.OnParticipantDisconnected = session.OnParticipantDisconnected

	// Connection events
	// room.Callback.OnConnectionStateChanged = session.OnConnectionStateChanged
	// room.Callback.OnDisconnected = session.OnDisconnected
	// room.Callback.OnReconnected = session.OnReconnected

	// Data events (for control messages if needed)
	// room.Callback.OnDataPacketReceived = session.OnDataPacketReceived
}

// monitorSession monitors a recording session for completion or failure
func (h *EgressHandler) monitorSession(ctx context.Context, jobID string, session *RecordingSession) {
	defer func() {
		// Clean up session when done
		h.mu.Lock()
		delete(h.sessions, jobID)
		h.mu.Unlock()
	}()

	// Wait for session to complete or context to cancel
	select {
	case <-ctx.Done():
		logger.Infow("context cancelled, stopping session", "jobID", jobID)
		session.Stop()

		h.mu.Lock()
		h.failedJobs++
		h.mu.Unlock()

	case err := <-session.Done():
		if err != nil {
			logger.Errorw("session ended with error", err,
				"jobID", jobID)

			h.mu.Lock()
			h.failedJobs++
			h.mu.Unlock()
		} else {
			logger.Infow("session completed successfully", "jobID", jobID)

			h.mu.Lock()
			h.completedJobs++
			h.mu.Unlock()
		}
	}
}

// OnJobTerminated handles job termination
func (h *EgressHandler) OnJobTerminated(ctx context.Context, jobID string) {
	logger.Infow("job terminated", "jobID", jobID)

	// Stop the recording session if it exists
	h.mu.RLock()
	session, exists := h.sessions[jobID]
	h.mu.RUnlock()

	if exists {
		session.Stop()

		// Remove from active sessions
		h.mu.Lock()
		delete(h.sessions, jobID)
		h.mu.Unlock()
	}
}

// GetSession returns the recording session for a given job ID
func (h *EgressHandler) GetSession(jobID string) (*RecordingSession, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	session, exists := h.sessions[jobID]
	return session, exists
}

// GetStats returns handler statistics
func (h *EgressHandler) GetStats() HandlerStats {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return HandlerStats{
		TotalJobs:        h.totalJobs,
		AcceptedJobs:     h.acceptedJobs,
		RejectedJobs:     h.rejectedJobs,
		CompletedJobs:    h.completedJobs,
		FailedJobs:       h.failedJobs,
		ActiveSessions:   len(h.sessions),
		SessionIDs:       h.getSessionIDs(),
	}
}

// getSessionIDs returns a list of active session IDs
func (h *EgressHandler) getSessionIDs() []string {
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// Shutdown gracefully shuts down the handler and all active sessions
func (h *EgressHandler) Shutdown(ctx context.Context) error {
	logger.Infow("shutting down egress handler")

	// Get all active sessions
	h.mu.RLock()
	sessions := make([]*RecordingSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.RUnlock()

	// Stop all sessions
	var wg sync.WaitGroup
	for _, session := range sessions {
		wg.Add(1)
		go func(s *RecordingSession) {
			defer wg.Done()
			s.Stop()
		}(session)
	}

	// Wait for all sessions to stop or context to expire
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Infow("all sessions stopped successfully")
		// Clear all sessions
		h.mu.Lock()
		h.sessions = make(map[string]*RecordingSession)
		h.mu.Unlock()
		return nil
	case <-ctx.Done():
		logger.Debugw("shutdown timeout, some sessions may not have stopped cleanly")
		return ctx.Err()
	}
}

// HandlerStats contains statistics about the handler
type HandlerStats struct {
	TotalJobs      int64    `json:"total_jobs"`
	AcceptedJobs   int64    `json:"accepted_jobs"`
	RejectedJobs   int64    `json:"rejected_jobs"`
	CompletedJobs  int64    `json:"completed_jobs"`
	FailedJobs     int64    `json:"failed_jobs"`
	ActiveSessions int      `json:"active_sessions"`
	SessionIDs     []string `json:"session_ids"`
}