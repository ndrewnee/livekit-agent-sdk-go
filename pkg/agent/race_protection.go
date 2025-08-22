package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.uber.org/zap"
)

// RaceProtector provides protection against race conditions during worker lifecycle
type RaceProtector struct {
	mu                         sync.RWMutex
	logger                     *zap.Logger
	
	// State tracking
	isDisconnecting            atomic.Bool
	isReconnecting             atomic.Bool
	activeTerminations         map[string]*TerminationState
	pendingStatusUpdates       map[string]*StatusUpdate
	droppedJobsDuringDisconnect int64
	droppedStatusUpdates       int64
	
	// Callbacks
	onJobDropped         func(jobID string, reason string)
	onStatusUpdateQueued func(jobID string)
}

// TerminationState tracks termination request state
type TerminationState struct {
	JobID            string
	RequestedAt      time.Time
	RequestCount     int
	CompletedAt      *time.Time
	LastError        error
}

// StatusUpdate represents a pending status update
type StatusUpdate struct {
	JobID        string
	Status       livekit.JobStatus
	Error        string
	Timestamp    time.Time
	RetryCount   int
}

// NewRaceProtector creates a new race condition protector
func NewRaceProtector(logger *zap.Logger) *RaceProtector {
	return &RaceProtector{
		logger:               logger,
		activeTerminations:   make(map[string]*TerminationState),
		pendingStatusUpdates: make(map[string]*StatusUpdate),
	}
}

// SetDisconnecting marks the worker as disconnecting
func (p *RaceProtector) SetDisconnecting(disconnecting bool) {
	p.isDisconnecting.Store(disconnecting)
	
	if disconnecting {
		p.logger.Debug("Worker entering disconnection state")
	} else {
		p.logger.Debug("Worker exiting disconnection state")
		
		// Reset dropped counters
		atomic.StoreInt64(&p.droppedJobsDuringDisconnect, 0)
	}
}

// IsDisconnecting returns true if the worker is disconnecting
func (p *RaceProtector) IsDisconnecting() bool {
	return p.isDisconnecting.Load()
}

// SetReconnecting marks the worker as reconnecting
func (p *RaceProtector) SetReconnecting(reconnecting bool) {
	p.isReconnecting.Store(reconnecting)
	
	if reconnecting {
		p.logger.Debug("Worker entering reconnection state")
	} else {
		p.logger.Debug("Worker exiting reconnection state")
		
		// Reset dropped counters
		atomic.StoreInt64(&p.droppedStatusUpdates, 0)
	}
}

// IsReconnecting returns true if the worker is reconnecting
func (p *RaceProtector) IsReconnecting() bool {
	return p.isReconnecting.Load()
}

// CanAcceptJob checks if a job can be accepted given current state
func (p *RaceProtector) CanAcceptJob(jobID string) (bool, string) {
	// Check if disconnecting
	if p.IsDisconnecting() {
		atomic.AddInt64(&p.droppedJobsDuringDisconnect, 1)
		reason := "worker is disconnecting"
		
		p.logger.Warn("Rejecting job during disconnection",
			zap.String("jobID", jobID),
			zap.Int64("droppedCount", atomic.LoadInt64(&p.droppedJobsDuringDisconnect)),
		)
		
		// Call callback if set
		if p.onJobDropped != nil {
			p.onJobDropped(jobID, reason)
		}
		
		return false, reason
	}
	
	// Check if there's an active termination for this job
	p.mu.RLock()
	termState, hasTermination := p.activeTerminations[jobID]
	p.mu.RUnlock()
	
	if hasTermination && termState.CompletedAt == nil {
		reason := "job has pending termination"
		p.logger.Warn("Rejecting job with pending termination",
			zap.String("jobID", jobID),
			zap.Int("terminationRequests", termState.RequestCount),
		)
		return false, reason
	}
	
	return true, ""
}

// RecordTerminationRequest records a termination request and handles concurrent requests
func (p *RaceProtector) RecordTerminationRequest(jobID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	termState, exists := p.activeTerminations[jobID]
	if !exists {
		// First termination request
		p.activeTerminations[jobID] = &TerminationState{
			JobID:        jobID,
			RequestedAt:  time.Now(),
			RequestCount: 1,
		}
		p.logger.Debug("Recording termination request", zap.String("jobID", jobID))
		return true
	}
	
	// Concurrent termination request
	termState.RequestCount++
	
	if termState.CompletedAt != nil {
		// Job already terminated
		p.logger.Debug("Ignoring termination request for completed job",
			zap.String("jobID", jobID),
			zap.Timep("completedAt", termState.CompletedAt),
			zap.Int("requestCount", termState.RequestCount),
		)
		return false
	}
	
	p.logger.Warn("Concurrent termination request detected",
		zap.String("jobID", jobID),
		zap.Int("requestCount", termState.RequestCount),
		zap.Time("firstRequestAt", termState.RequestedAt),
	)
	
	// Allow first request to proceed, ignore subsequent ones
	return termState.RequestCount == 1
}

// CompleteTermination marks a termination as complete
func (p *RaceProtector) CompleteTermination(jobID string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	termState, exists := p.activeTerminations[jobID]
	if !exists {
		p.logger.Warn("Completing termination for untracked job", zap.String("jobID", jobID))
		return
	}
	
	now := time.Now()
	termState.CompletedAt = &now
	termState.LastError = err
	
	p.logger.Debug("Termination completed",
		zap.String("jobID", jobID),
		zap.Duration("duration", now.Sub(termState.RequestedAt)),
		zap.Int("requestCount", termState.RequestCount),
		zap.Error(err),
	)
}

// QueueStatusUpdate queues a status update during reconnection
func (p *RaceProtector) QueueStatusUpdate(jobID string, status livekit.JobStatus, errorMsg string) bool {
	if !p.IsReconnecting() {
		// Not reconnecting, allow immediate update
		return false
	}
	
	p.mu.Lock()
	defer p.mu.Unlock()
	
	// Check if we already have a pending update for this job
	if existing, exists := p.pendingStatusUpdates[jobID]; exists {
		// Update only if the new status is more severe
		if shouldReplaceStatus(existing.Status, status) {
			existing.Status = status
			existing.Error = errorMsg
			existing.Timestamp = time.Now()
			p.logger.Debug("Updated queued status update",
				zap.String("jobID", jobID),
				zap.String("newStatus", status.String()),
				zap.String("previousStatus", existing.Status.String()),
			)
		}
		return true
	}
	
	// Queue new status update
	p.pendingStatusUpdates[jobID] = &StatusUpdate{
		JobID:     jobID,
		Status:    status,
		Error:     errorMsg,
		Timestamp: time.Now(),
	}
	
	atomic.AddInt64(&p.droppedStatusUpdates, 1)
	p.logger.Info("Queued status update during reconnection",
		zap.String("jobID", jobID),
		zap.String("status", status.String()),
		zap.Int("queueSize", len(p.pendingStatusUpdates)),
	)
	
	// Call callback if set
	if p.onStatusUpdateQueued != nil {
		p.onStatusUpdateQueued(jobID)
	}
	
	return true
}

// FlushPendingStatusUpdates returns all pending status updates and clears the queue
func (p *RaceProtector) FlushPendingStatusUpdates() []StatusUpdate {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	if len(p.pendingStatusUpdates) == 0 {
		return nil
	}
	
	updates := make([]StatusUpdate, 0, len(p.pendingStatusUpdates))
	for _, update := range p.pendingStatusUpdates {
		updates = append(updates, *update)
	}
	
	p.logger.Info("Flushing pending status updates",
		zap.Int("count", len(updates)),
		zap.Int64("totalDropped", atomic.LoadInt64(&p.droppedStatusUpdates)),
	)
	
	// Clear the queue
	p.pendingStatusUpdates = make(map[string]*StatusUpdate)
	
	return updates
}

// CleanupOldTerminations removes old termination records
func (p *RaceProtector) CleanupOldTerminations(maxAge time.Duration) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	now := time.Now()
	cleaned := 0
	
	for jobID, termState := range p.activeTerminations {
		if termState.CompletedAt != nil && now.Sub(*termState.CompletedAt) > maxAge {
			delete(p.activeTerminations, jobID)
			cleaned++
		}
	}
	
	if cleaned > 0 {
		p.logger.Debug("Cleaned up old termination records",
			zap.Int("count", cleaned),
			zap.Int("remaining", len(p.activeTerminations)),
		)
	}
	
	return cleaned
}

// GetMetrics returns race protection metrics
func (p *RaceProtector) GetMetrics() map[string]interface{} {
	p.mu.RLock()
	activeTerminations := len(p.activeTerminations)
	pendingUpdates := len(p.pendingStatusUpdates)
	p.mu.RUnlock()
	
	return map[string]interface{}{
		"is_disconnecting":            p.IsDisconnecting(),
		"is_reconnecting":             p.IsReconnecting(),
		"active_terminations":         activeTerminations,
		"pending_status_updates":      pendingUpdates,
		"dropped_jobs_during_disconnect": atomic.LoadInt64(&p.droppedJobsDuringDisconnect),
		"dropped_status_updates":      atomic.LoadInt64(&p.droppedStatusUpdates),
	}
}

// SetOnJobDroppedCallback sets the callback for dropped jobs
func (p *RaceProtector) SetOnJobDroppedCallback(cb func(jobID string, reason string)) {
	p.onJobDropped = cb
}

// SetOnStatusUpdateQueuedCallback sets the callback for queued status updates
func (p *RaceProtector) SetOnStatusUpdateQueuedCallback(cb func(jobID string)) {
	p.onStatusUpdateQueued = cb
}

// shouldReplaceStatus determines if a new status should replace an existing one
func shouldReplaceStatus(current, new livekit.JobStatus) bool {
	// Define status priority (higher number = higher priority)
	priority := map[livekit.JobStatus]int{
		livekit.JobStatus_JS_PENDING: 1,
		livekit.JobStatus_JS_RUNNING: 2,
		livekit.JobStatus_JS_SUCCESS: 3,
		livekit.JobStatus_JS_FAILED:  4,
	}
	
	currentPriority := priority[current]
	newPriority := priority[new]
	
	return newPriority > currentPriority
}

// RaceProtectionGuard provides automatic race protection for operations
type RaceProtectionGuard struct {
	protector *RaceProtector
	jobID     string
	operation string
}

// NewRaceProtectionGuard creates a new guard for an operation
func (p *RaceProtector) NewGuard(jobID string, operation string) *RaceProtectionGuard {
	return &RaceProtectionGuard{
		protector: p,
		jobID:     jobID,
		operation: operation,
	}
}

// Execute runs a function with race protection
func (g *RaceProtectionGuard) Execute(ctx context.Context, fn func() error) error {
	// Check if we can proceed
	switch g.operation {
	case "job_accept":
		if canAccept, reason := g.protector.CanAcceptJob(g.jobID); !canAccept {
			return fmt.Errorf("cannot accept job: %s", reason)
		}
	case "termination":
		if !g.protector.RecordTerminationRequest(g.jobID) {
			return fmt.Errorf("concurrent termination already in progress")
		}
		defer func() {
			g.protector.CompleteTermination(g.jobID, nil)
		}()
	}
	
	// Execute the function
	return fn()
}