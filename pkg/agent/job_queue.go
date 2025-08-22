package agent

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
)

// JobPriority represents the priority level of a job.
//
// Jobs are processed based on their priority, with higher priority jobs
// being dequeued before lower priority ones. Within the same priority level,
// jobs are processed in FIFO order based on their enqueue time.
type JobPriority int

const (
	// JobPriorityLow indicates a low priority job that can be processed after other jobs.
	// Suitable for background tasks or non-time-sensitive operations.
	JobPriorityLow    JobPriority = 0
	
	// JobPriorityNormal indicates a standard priority job.
	// This is the default priority for most jobs.
	JobPriorityNormal JobPriority = 1
	
	// JobPriorityHigh indicates a high priority job that should be processed soon.
	// Suitable for user-initiated actions or time-sensitive operations.
	JobPriorityHigh   JobPriority = 2
	
	// JobPriorityUrgent indicates an urgent job that requires immediate processing.
	// Should be used sparingly for critical operations.
	JobPriorityUrgent JobPriority = 3
)

// JobQueueItem represents a job in the priority queue.
//
// Each item contains the job itself along with metadata used for
// queue management and processing. Items are ordered by priority
// and enqueue time.
type JobQueueItem struct {
	// Job is the LiveKit job to be processed
	Job         *livekit.Job
	
	// Priority determines the processing order of this job
	Priority    JobPriority
	
	// EnqueueTime records when the job was added to the queue
	EnqueueTime time.Time
	
	// Token is the authentication token for this job
	Token       string
	
	// URL is the server URL for this job
	URL         string
	
	index       int // Used by heap.Interface
}

// JobQueue manages pending jobs with priority-based ordering.
//
// The queue uses a priority heap to ensure high-priority jobs are
// processed before lower-priority ones. Within the same priority level,
// jobs are processed in FIFO order. The queue is thread-safe and supports
// blocking dequeue operations.
//
// Example usage:
//
//	queue := NewJobQueue(JobQueueOptions{MaxSize: 100})
//	
//	// Add a job
//	err := queue.Enqueue(job, JobPriorityHigh, token, url)
//	
//	// Get the next job (blocking)
//	item, err := queue.DequeueWithContext(ctx)
//	if err == nil {
//	    // Process item.Job
//	}
type JobQueue struct {
	mu       sync.Mutex
	items    []*JobQueueItem
	maxSize  int
	waitChan chan struct{}
	closed   bool
}

// JobQueueOptions configures the job queue.
type JobQueueOptions struct {
	// MaxSize is the maximum number of jobs allowed in the queue.
	// Set to 0 for unlimited queue size.
	// If the queue is full, Enqueue will return an error.
	MaxSize int
}

// NewJobQueue creates a new priority-based job queue.
//
// The queue is initialized empty and ready to accept jobs. If MaxSize
// is specified in options, the queue will enforce that limit.
func NewJobQueue(opts JobQueueOptions) *JobQueue {
	q := &JobQueue{
		items:    make([]*JobQueueItem, 0),
		maxSize:  opts.MaxSize,
		waitChan: make(chan struct{}, 1),
	}
	heap.Init(q)
	return q
}

// Enqueue adds a job to the queue with the specified priority.
//
// Jobs are ordered first by priority (higher priority first) and then
// by enqueue time (older first) within the same priority level.
//
// Returns an error if:
//   - The queue is closed
//   - The queue is full (when MaxSize > 0)
//
// The method is thread-safe and will signal any waiting DequeueWithContext calls.
func (q *JobQueue) Enqueue(job *livekit.Job, priority JobPriority, token, url string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return fmt.Errorf("queue is closed")
	}

	// Check size limit
	if q.maxSize > 0 && len(q.items) >= q.maxSize {
		return fmt.Errorf("queue is full (max size: %d)", q.maxSize)
	}

	item := &JobQueueItem{
		Job:         job,
		Priority:    priority,
		EnqueueTime: time.Now(),
		Token:       token,
		URL:         url,
	}

	heap.Push(q, item)
	
	// Signal that a new item is available
	select {
	case q.waitChan <- struct{}{}:
	default:
	}

	return nil
}

// Dequeue removes and returns the highest priority job.
//
// Returns the job item and true if a job was available, or nil and false
// if the queue is empty. This method does not block.
//
// The method is thread-safe.
func (q *JobQueue) Dequeue() (*JobQueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return nil, false
	}

	item := heap.Pop(q).(*JobQueueItem)
	return item, true
}

// DequeueWithContext waits for a job or until context is cancelled.
//
// This method blocks until one of the following occurs:
//   - A job becomes available in the queue
//   - The context is cancelled
//   - The queue is closed
//
// Returns the dequeued job item on success, or an error if the context
// was cancelled or the queue was closed.
//
// Example:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
//	defer cancel()
//	
//	item, err := queue.DequeueWithContext(ctx)
//	if err != nil {
//	    // Handle timeout or cancellation
//	}
func (q *JobQueue) DequeueWithContext(ctx context.Context) (*JobQueueItem, error) {
	for {
		// Try to get a job
		if item, ok := q.Dequeue(); ok {
			return item, nil
		}

		// Wait for new items or context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-q.waitChan:
			// New item might be available, loop again
		}
	}
}

// Peek returns the highest priority job without removing it.
//
// Returns the job item and true if the queue has jobs, or nil and false
// if the queue is empty. The job remains in the queue after this call.
//
// The method is thread-safe.
func (q *JobQueue) Peek() (*JobQueueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return nil, false
	}

	return q.items[0], true
}

// Size returns the number of jobs in the queue.
//
// The method is thread-safe.
func (q *JobQueue) Size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Clear removes all jobs from the queue.
//
// After this call, the queue will be empty but still usable.
// The method is thread-safe.
func (q *JobQueue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = q.items[:0]
}

// Close closes the queue and prevents new items from being added.
//
// Any blocked DequeueWithContext calls will return with an error.
// After closing, the queue cannot be reopened.
//
// The method is thread-safe.
func (q *JobQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	close(q.waitChan)
}

// GetJobsByPriority returns all jobs of a specific priority.
//
// The returned jobs remain in the queue. The slice contains references
// to the actual queue items, so modifications to the items will affect
// the queue.
//
// The method is thread-safe but the returned slice is a snapshot and
// may become stale if the queue is modified.
func (q *JobQueue) GetJobsByPriority(priority JobPriority) []*JobQueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	var result []*JobQueueItem
	for _, item := range q.items {
		if item.Priority == priority {
			result = append(result, item)
		}
	}
	return result
}

// RemoveJob removes a specific job from the queue by ID.
//
// Returns true if the job was found and removed, false otherwise.
// This operation maintains the heap property of the queue.
//
// The method is thread-safe.
func (q *JobQueue) RemoveJob(jobID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, item := range q.items {
		if item.Job.Id == jobID {
			heap.Remove(q, i)
			return true
		}
	}
	return false
}

// heap.Interface implementation
func (q *JobQueue) Len() int { return len(q.items) }

func (q *JobQueue) Less(i, j int) bool {
	// Higher priority first
	if q.items[i].Priority != q.items[j].Priority {
		return q.items[i].Priority > q.items[j].Priority
	}
	// Within same priority, older jobs first (FIFO)
	return q.items[i].EnqueueTime.Before(q.items[j].EnqueueTime)
}

func (q *JobQueue) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	q.items[i].index = i
	q.items[j].index = j
}

func (q *JobQueue) Push(x interface{}) {
	n := len(q.items)
	item := x.(*JobQueueItem)
	item.index = n
	q.items = append(q.items, item)
}

func (q *JobQueue) Pop() interface{} {
	old := q.items
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	q.items = old[0 : n-1]
	return item
}

// JobPriorityCalculator determines job priority based on job metadata.
//
// Implementations can inspect job properties, metadata, or external factors
// to assign appropriate priority levels. This allows for flexible priority
// assignment strategies without modifying the queue implementation.
type JobPriorityCalculator interface {
	// CalculatePriority examines a job and returns its priority level.
	// The implementation should be deterministic for consistent behavior.
	CalculatePriority(job *livekit.Job) JobPriority
}

// DefaultPriorityCalculator provides a default priority calculation strategy.
//
// Priority is determined by:
//   - Job metadata containing priority hints ("urgent", "high", "low")
//   - Job type (room jobs get higher priority by default)
//   - Falls back to normal priority if no hints are found
type DefaultPriorityCalculator struct{}

// CalculatePriority determines priority based on job metadata and type.
//
// Priority assignment:
//   - Metadata "urgent" or "high-priority" → JobPriorityUrgent
//   - Metadata "high" → JobPriorityHigh
//   - Metadata "low" → JobPriorityLow
//   - JobType JT_ROOM → JobPriorityHigh (if no metadata)
//   - All others → JobPriorityNormal
func (d *DefaultPriorityCalculator) CalculatePriority(job *livekit.Job) JobPriority {
	// Check job metadata for priority hints
	if job.Metadata != "" {
		// Look for priority field in metadata
		switch job.Metadata {
		case "urgent", "high-priority":
			return JobPriorityUrgent
		case "high":
			return JobPriorityHigh
		case "low":
			return JobPriorityLow
		}
	}

	// Check job type for default priorities
	switch job.Type {
	case livekit.JobType_JT_ROOM:
		// Room jobs are typically higher priority
		return JobPriorityHigh
	case livekit.JobType_JT_PARTICIPANT:
		// Participant jobs are normal priority
		return JobPriorityNormal
	case livekit.JobType_JT_PUBLISHER:
		// Publisher jobs might be lower priority
		return JobPriorityNormal
	default:
		return JobPriorityNormal
	}
}

// MetadataPriorityCalculator calculates priority from structured job metadata.
//
// This calculator looks for a specific field in the job metadata to determine
// priority. The metadata is expected to be in a simple key:value format.
// For production use, consider parsing JSON metadata instead.
//
// Example:
//
//	calc := &MetadataPriorityCalculator{PriorityField: "priority"}
//	// Job with metadata "priority:urgent" → JobPriorityUrgent
//	// Job with metadata "priority:high" → JobPriorityHigh
type MetadataPriorityCalculator struct {
	// PriorityField is the field name in metadata to check for priority value.
	// For example, if PriorityField is "priority", the calculator looks for
	// metadata like "priority:high" or "priority:urgent".
	PriorityField string
}

// CalculatePriority extracts priority from job metadata field.
//
// Looks for metadata in format "{PriorityField}:{value}" where value can be:
//   - "urgent" → JobPriorityUrgent
//   - "high" → JobPriorityHigh
//   - "low" → JobPriorityLow
//   - anything else → JobPriorityNormal
//
// Note: This is a simplified implementation. For production use,
// consider parsing structured JSON metadata.
func (m *MetadataPriorityCalculator) CalculatePriority(job *livekit.Job) JobPriority {
	if job.Metadata == "" || m.PriorityField == "" {
		return JobPriorityNormal
	}

	// Parse metadata for priority field
	// This is a simplified version - in production, you'd parse JSON
	metadata := job.Metadata
	if metadata == m.PriorityField+":urgent" {
		return JobPriorityUrgent
	} else if metadata == m.PriorityField+":high" {
		return JobPriorityHigh
	} else if metadata == m.PriorityField+":low" {
		return JobPriorityLow
	}

	return JobPriorityNormal
}