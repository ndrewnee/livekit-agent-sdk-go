package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// JobRecoveryHandler provides hooks for job recovery during reconnection.
//
// Implement this interface to customize how jobs are recovered after
// a worker loses and regains connection to the server. This allows
// for application-specific recovery logic and state restoration.
//
// The recovery process:
//  1. Worker loses connection
//  2. Jobs are saved with their state
//  3. Worker reconnects
//  4. OnJobRecoveryAttempt is called for each saved job
//  5. If approved, recovery is attempted
//  6. OnJobRecovered or OnJobRecoveryFailed is called based on result
type JobRecoveryHandler interface {
	// OnJobRecoveryAttempt is called when trying to recover a job after reconnection.
	//
	// This method allows the handler to decide whether to attempt recovery
	// based on the job state. Return true to proceed with recovery, false
	// to abandon the job.
	//
	// Common decision factors:
	//   - Job status (only recover running jobs)
	//   - Time since disconnection
	//   - Job type or priority
	//   - Available resources
	OnJobRecoveryAttempt(ctx context.Context, jobID string, jobState *JobState) bool

	// OnJobRecovered is called when a job has been successfully recovered.
	//
	// The job is now active again with a valid room connection. The handler
	// can use this callback to restore application state, resume processing,
	// or notify other components.
	//
	// The provided room is already connected and ready for use.
	OnJobRecovered(ctx context.Context, job *livekit.Job, room *lksdk.Room)

	// OnJobRecoveryFailed is called when job recovery fails.
	//
	// Recovery can fail for various reasons:
	//   - Room no longer exists
	//   - Token expired
	//   - Network issues
	//   - Resource constraints
	//
	// The handler can use this to clean up state, log errors, or notify
	// monitoring systems.
	OnJobRecoveryFailed(ctx context.Context, jobID string, err error)
}

// DefaultJobRecoveryHandler provides a default recovery implementation.
//
// The default behavior:
//   - Attempts recovery only for jobs with JS_RUNNING status
//   - No special handling on successful recovery
//   - No special handling on failed recovery
//
// This handler is used when no custom handler is provided.
type DefaultJobRecoveryHandler struct{}

// OnJobRecoveryAttempt implements JobRecoveryHandler.
// By default, only attempts recovery for jobs with JS_RUNNING status.
func (d *DefaultJobRecoveryHandler) OnJobRecoveryAttempt(ctx context.Context, jobID string, jobState *JobState) bool {
	// By default, attempt recovery for jobs that were running
	return jobState.Status == livekit.JobStatus_JS_RUNNING
}

// OnJobRecovered implements JobRecoveryHandler.
// Default implementation does nothing.
func (d *DefaultJobRecoveryHandler) OnJobRecovered(ctx context.Context, job *livekit.Job, room *lksdk.Room) {
	// Default: no-op
}

// OnJobRecoveryFailed implements JobRecoveryHandler.
// Default implementation does nothing.
func (d *DefaultJobRecoveryHandler) OnJobRecoveryFailed(ctx context.Context, jobID string, err error) {
	// Default: no-op
}

// JobRecoveryManager manages job recovery during reconnection.
//
// The manager tracks active jobs and attempts to restore them after
// network disruptions. It works with JobRecoveryHandler to provide
// customizable recovery behavior.
//
// Key features:
//   - Automatic job state preservation
//   - Configurable recovery timeout
//   - Retry logic with attempt limits
//   - Concurrent recovery attempts
//   - Room reconnection support
type JobRecoveryManager struct {
	worker          *Worker
	recoveryHandler JobRecoveryHandler
	mu              sync.Mutex
	pendingRecovery map[string]*RecoverableJob
	recoveryTimeout time.Duration
}

// RecoverableJob contains all information needed to recover a job.
//
// This structure preserves job state during disconnections and provides
// everything needed to restore the job when the worker reconnects.
type RecoverableJob struct {
	// JobID is the unique identifier of the job
	JobID       string
	
	// JobState contains the runtime state of the job
	JobState    *JobState
	
	// JobData is the original job assignment from the server
	JobData     *livekit.Job
	
	// RoomToken is the authentication token for the room (if available)
	RoomToken   string
	
	// LastUpdate tracks when this job was last active
	LastUpdate  time.Time
	
	// RecoveryAttempts counts how many times recovery has been tried
	RecoveryAttempts int
}

// NewJobRecoveryManager creates a new job recovery manager.
//
// If handler is nil, DefaultJobRecoveryHandler will be used.
// The manager uses a 30-second timeout for recovery attempts by default.
func NewJobRecoveryManager(worker *Worker, handler JobRecoveryHandler) *JobRecoveryManager {
	if handler == nil {
		handler = &DefaultJobRecoveryHandler{}
	}
	return &JobRecoveryManager{
		worker:          worker,
		recoveryHandler: handler,
		pendingRecovery: make(map[string]*RecoverableJob),
		recoveryTimeout: 30 * time.Second,
	}
}

// SaveJobForRecovery saves job information for potential recovery.
//
// Call this method when a job starts to enable recovery if the worker
// disconnects. The job data, token, and current state are preserved.
//
// The saved job will be available for recovery until:
//   - Recovery succeeds
//   - Recovery timeout expires
//   - Maximum retry attempts are exceeded
//   - The job is explicitly removed
func (m *JobRecoveryManager) SaveJobForRecovery(jobID string, job *livekit.Job, token string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pendingRecovery[jobID] = &RecoverableJob{
		JobID:      jobID,
		JobData:    job,
		RoomToken:  token,
		LastUpdate: time.Now(),
		JobState: &JobState{
			JobID:     jobID,
			Status:    livekit.JobStatus_JS_RUNNING,
			StartedAt: time.Now(),
			RoomName:  job.Room.Name,
		},
	}
}

// AttemptJobRecovery tries to recover all pending jobs after reconnection.
//
// This method should be called after the worker successfully reconnects
// to the server. It will:
//  1. Check each saved job with the recovery handler
//  2. Skip jobs that are too old or declined by the handler  
//  3. Attempt to recover approved jobs concurrently
//  4. Update job states based on recovery results
//
// Returns a map of jobID to error for all recovery attempts.
// A nil error indicates successful recovery.
func (m *JobRecoveryManager) AttemptJobRecovery(ctx context.Context) map[string]error {
	m.mu.Lock()
	jobs := make(map[string]*RecoverableJob)
	for id, job := range m.pendingRecovery {
		jobs[id] = job
	}
	m.mu.Unlock()

	results := make(map[string]error)
	var wg sync.WaitGroup

	for jobID, recoverableJob := range jobs {
		// Check if handler wants to recover this job
		if !m.recoveryHandler.OnJobRecoveryAttempt(ctx, jobID, recoverableJob.JobState) {
			results[jobID] = fmt.Errorf("recovery declined by handler")
			m.removeFromRecovery(jobID)
			continue
		}

		// Check if job is too old
		if time.Since(recoverableJob.LastUpdate) > m.recoveryTimeout {
			results[jobID] = fmt.Errorf("job too old for recovery")
			m.removeFromRecovery(jobID)
			continue
		}

		wg.Add(1)
		go func(jobID string, job *RecoverableJob) {
			defer wg.Done()
			
			err := m.recoverJob(ctx, jobID, job)
			results[jobID] = err
			
			if err != nil {
				m.recoveryHandler.OnJobRecoveryFailed(ctx, jobID, err)
				// Keep in recovery queue for potential retry
				m.incrementRecoveryAttempts(jobID)
			} else {
				m.removeFromRecovery(jobID)
			}
		}(jobID, recoverableJob)
	}

	wg.Wait()
	return results
}

func (m *JobRecoveryManager) recoverJob(ctx context.Context, jobID string, recoverableJob *RecoverableJob) error {
	// First, notify server that we're still working on this job
	m.worker.updateJobStatus(jobID, livekit.JobStatus_JS_RUNNING, "recovering after reconnection")

	// If we have a room token, try to reconnect to the room
	if recoverableJob.RoomToken != "" && recoverableJob.JobData != nil {
		// Set up room callbacks
		roomCallback := &lksdk.RoomCallback{
			OnDisconnected: func() {
				m.worker.logger.Info("Recovered job disconnected from room", "jobID", jobID)
			},
		}

		// Try to connect with the saved token
		room, err := lksdk.ConnectToRoomWithToken(m.worker.serverURL, recoverableJob.RoomToken, roomCallback, lksdk.WithAutoSubscribe(false))
		if err == nil {
			// Successfully reconnected to room
			m.worker.logger.Info("Successfully recovered job", "jobID", jobID)
			
			// Re-register the job in activeJobs
			m.worker.mu.Lock()
			m.worker.activeJobs[jobID] = &activeJob{
				job:       recoverableJob.JobData,
				room:      room,
				status:    livekit.JobStatus_JS_RUNNING,
				startedAt: recoverableJob.JobState.StartedAt,
			}
			m.worker.mu.Unlock()

			// Notify handler of successful recovery
			m.recoveryHandler.OnJobRecovered(ctx, recoverableJob.JobData, room)
			return nil
		}
		
		// Token might be expired or room might be gone
		m.worker.logger.Warn("Failed to reconnect to room for job recovery", "jobID", jobID, "error", err)
	}

	// Without a valid token or if room connection failed, we can only maintain job state
	// and wait for new instructions from the server
	m.worker.logger.Info("Job marked for recovery but requires new assignment", "jobID", jobID)
	
	// Keep the job in a recoverable state
	return fmt.Errorf("job recovery requires new room token")
}

func (m *JobRecoveryManager) removeFromRecovery(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pendingRecovery, jobID)
}

func (m *JobRecoveryManager) incrementRecoveryAttempts(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if job, exists := m.pendingRecovery[jobID]; exists {
		job.RecoveryAttempts++
		if job.RecoveryAttempts > 3 {
			// Too many attempts, remove from recovery
			delete(m.pendingRecovery, jobID)
		}
	}
}

// GetRecoverableJobs returns all jobs pending recovery.
//
// This method returns a copy of the pending recovery map, so modifications
// to the returned map won't affect the internal state.
//
// Use this to inspect which jobs are queued for recovery, their states,
// and recovery attempt counts.
func (m *JobRecoveryManager) GetRecoverableJobs() map[string]*RecoverableJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	jobs := make(map[string]*RecoverableJob)
	for id, job := range m.pendingRecovery {
		jobs[id] = job
	}
	return jobs
}

// PartialMessageBuffer handles partial WebSocket messages during reconnection.
//
// When a WebSocket connection is interrupted, messages may be partially
// received. This buffer helps preserve partial data and attempt to
// reconstruct complete messages after reconnection.
//
// The buffer includes size limits and staleness detection to prevent
// memory issues from incomplete messages.
type PartialMessageBuffer struct {
	mu          sync.Mutex
	buffer      []byte
	messageType int
	lastUpdate  time.Time
	maxSize     int
}

// NewPartialMessageBuffer creates a new buffer for partial messages.
//
// maxSize limits the buffer size to prevent memory issues.
// If maxSize <= 0, defaults to 1MB.
func NewPartialMessageBuffer(maxSize int) *PartialMessageBuffer {
	if maxSize <= 0 {
		maxSize = 1024 * 1024 // 1MB default
	}
	return &PartialMessageBuffer{
		maxSize: maxSize,
	}
}

// Append adds data to the partial message buffer.
//
// If the message type changes from the previous append, the buffer
// is reset. Returns an error if adding the data would exceed maxSize.
//
// The method is thread-safe.
func (b *PartialMessageBuffer) Append(messageType int, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// If this is a new message type, reset the buffer
	if b.messageType != messageType && len(b.buffer) > 0 {
		b.buffer = nil
	}

	b.messageType = messageType
	b.lastUpdate = time.Now()

	// Check size limit
	if len(b.buffer)+len(data) > b.maxSize {
		return fmt.Errorf("message size exceeds limit")
	}

	b.buffer = append(b.buffer, data...)
	return nil
}

// GetComplete returns the complete message if available.
//
// For text messages, attempts to parse as JSON to verify completeness.
// Binary messages cannot be reliably validated without framing information.
//
// Returns:
//   - messageType: The WebSocket message type
//   - data: The complete message data (nil if incomplete)
//   - complete: true if a complete message is available
//
// If a complete message is returned, the buffer is cleared.
// The method is thread-safe.
func (b *PartialMessageBuffer) GetComplete() (int, []byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buffer) == 0 {
		return 0, nil, false
	}

	// Try to validate if we have a complete message
	// For JSON messages, check if we have valid JSON
	if b.messageType == 1 { // TextMessage
		var temp interface{}
		if err := json.Unmarshal(b.buffer, &temp); err == nil {
			// Valid JSON, message is complete
			data := make([]byte, len(b.buffer))
			copy(data, b.buffer)
			b.buffer = nil
			return b.messageType, data, true
		}
	}

	// For binary messages, we might need additional framing info
	// which WebSocket typically handles. Without server support,
	// we can't reliably detect message boundaries.

	return 0, nil, false
}

// Clear resets the buffer.
//
// Use this to discard incomplete data, such as when giving up on
// message reconstruction or starting fresh after reconnection.
//
// The method is thread-safe.
func (b *PartialMessageBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffer = nil
}

// IsStale checks if the buffer has been idle too long.
//
// Returns true if no data has been appended for longer than the
// specified timeout. Stale buffers likely contain incomplete messages
// that will never be completed.
//
// The method is thread-safe.
func (b *PartialMessageBuffer) IsStale(timeout time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Since(b.lastUpdate) > timeout
}

// JobResumptionData contains data needed to resume a job.
//
// This structure can be serialized and used to communicate job state
// between workers or persist state across restarts. It includes both
// system-level information and application-specific checkpoint data.
type JobResumptionData struct {
	// JobID uniquely identifies the job
	JobID         string                 `json:"job_id"`
	
	// WorkerID identifies the worker that was handling the job
	WorkerID      string                 `json:"worker_id"`
	
	// LastStatus is the job's status at the time of suspension
	LastStatus    livekit.JobStatus      `json:"last_status"`
	
	// LastUpdate is when the job state was last updated
	LastUpdate    time.Time              `json:"last_update"`
	
	// CheckpointData contains application-specific state for resumption
	CheckpointData map[string]interface{} `json:"checkpoint_data,omitempty"`
}

// JobCheckpoint allows handlers to save job progress.
//
// Checkpoints enable jobs to save their state periodically, making it
// possible to resume from a known point after interruptions. The checkpoint
// data is preserved during recovery attempts.
//
// Example usage:
//
//	checkpoint := NewJobCheckpoint(jobID)
//	checkpoint.Save("processed_count", 150)
//	checkpoint.Save("last_timestamp", time.Now())
//	
//	// Later, after recovery
//	if count, ok := checkpoint.Load("processed_count"); ok {
//	    processedCount := count.(int)
//	    // Resume from this count
//	}
type JobCheckpoint struct {
	mu    sync.RWMutex
	data  map[string]interface{}
	jobID string
}

// NewJobCheckpoint creates a new checkpoint for a job.
//
// The checkpoint starts empty and can be populated with Save calls.
func NewJobCheckpoint(jobID string) *JobCheckpoint {
	return &JobCheckpoint{
		jobID: jobID,
		data:  make(map[string]interface{}),
	}
}

// Save stores a checkpoint value.
//
// The value can be any serializable type. Common types include:
//   - Basic types: int, string, bool, float64
//   - Time values: time.Time
//   - Slices and maps of basic types
//
// The method is thread-safe.
func (c *JobCheckpoint) Save(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
}

// Load retrieves a checkpoint value.
//
// Returns the value and true if the key exists, or nil and false if not found.
// The caller should type assert the returned value to the expected type.
//
// The method is thread-safe.
func (c *JobCheckpoint) Load(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, exists := c.data[key]
	return val, exists
}

// GetAll returns all checkpoint data.
//
// Returns a copy of the checkpoint data map. Modifications to the
// returned map won't affect the checkpoint's internal state.
//
// This is useful for serialization or debugging.
// The method is thread-safe.
func (c *JobCheckpoint) GetAll() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	result := make(map[string]interface{})
	for k, v := range c.data {
		result[k] = v
	}
	return result
}

// Clear removes all checkpoint data.
//
// Use this to reset the checkpoint to an empty state.
// The method is thread-safe.
func (c *JobCheckpoint) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = make(map[string]interface{})
}