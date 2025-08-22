package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// SimpleJobHandler provides a simplified interface for handling jobs without needing
// to implement all JobHandler methods. This is useful for quick prototyping or simple
// agents that don't need the full complexity of the complete JobHandler interface.
type SimpleJobHandler struct {
	// OnJob is called when a job is assigned
	OnJob func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
	
	// Metadata provides participant information for the agent
	Metadata func(job *livekit.Job) *JobMetadata
	
	// OnTerminated is called when a job is terminated (optional)
	OnTerminated func(jobID string)
}

// OnJobRequest implements the JobHandler interface by providing automatic metadata
// generation based on job type. If a custom Metadata function is provided, it will
// be used instead. Always returns true (accepts all jobs).
func (h *SimpleJobHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	if h.Metadata != nil {
		return true, h.Metadata(job)
	}
	
	// Default metadata
	identity := fmt.Sprintf("agent-%s", job.Id)
	name := "Agent"
	
	switch job.Type {
	case livekit.JobType_JT_ROOM:
		name = "Room Agent"
	case livekit.JobType_JT_PUBLISHER:
		name = "Publisher Agent"
		if job.Participant != nil {
			identity = fmt.Sprintf("agent-pub-%s", job.Participant.Identity)
		}
	case livekit.JobType_JT_PARTICIPANT:
		name = "Participant Agent"
		if job.Participant != nil {
			identity = fmt.Sprintf("agent-part-%s", job.Participant.Identity)
		}
	}
	
	return true, &JobMetadata{
		ParticipantIdentity: identity,
		ParticipantName:     name,
	}
}

// OnJobAssigned implements the JobHandler interface by delegating to the OnJob function
// if provided. Returns nil if no OnJob function is set.
func (h *SimpleJobHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	if h.OnJob != nil {
		return h.OnJob(ctx, job, room)
	}
	return nil
}

// OnJobTerminated implements the JobHandler interface by delegating to the OnTerminated
// function if provided. Does nothing if no OnTerminated function is set.
func (h *SimpleJobHandler) OnJobTerminated(ctx context.Context, jobID string) {
	if h.OnTerminated != nil {
		h.OnTerminated(jobID)
	}
}

// JobContext provides a convenient wrapper around job execution context with utility
// methods for common operations like waiting for participants, publishing data, and
// managing job lifecycle. This simplifies job handler implementations.
type JobContext struct {
	Job  *livekit.Job
	Room *lksdk.Room
	ctx  context.Context
}

// NewJobContext creates a new job context wrapper with the provided context, job, and room.
// This is typically called at the beginning of a job handler to create a convenient
// interface for job operations.
func NewJobContext(ctx context.Context, job *livekit.Job, room *lksdk.Room) *JobContext {
	return &JobContext{
		Job:  job,
		Room: room,
		ctx:  ctx,
	}
}

// Context returns the underlying context for the job execution.
func (jc *JobContext) Context() context.Context {
	return jc.ctx
}

// Done returns a channel that's closed when the job should stop execution.
// This is equivalent to jc.Context().Done().
func (jc *JobContext) Done() <-chan struct{} {
	return jc.ctx.Done()
}

// Sleep pauses execution for the specified duration or until the job is cancelled.
// Returns an error if the job context is cancelled during the sleep.
func (jc *JobContext) Sleep(d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-jc.ctx.Done():
		return jc.ctx.Err()
	}
}

// GetTargetParticipant returns the target participant for publisher/participant jobs.
// Returns nil if the job doesn't have a target participant or if the participant
// is not currently in the room.
func (jc *JobContext) GetTargetParticipant() *lksdk.RemoteParticipant {
	if jc.Job.Participant == nil {
		return nil
	}
	
	for _, p := range jc.Room.GetRemoteParticipants() {
		if p.Identity() == jc.Job.Participant.Identity {
			return p
		}
	}
	
	return nil
}

// WaitForParticipant waits for a participant with the specified identity to join the room.
// It polls every 100ms until the participant joins or the timeout is reached.
// Returns an error if the timeout is exceeded or the job context is cancelled.
func (jc *JobContext) WaitForParticipant(identity string, timeout time.Duration) (*lksdk.RemoteParticipant, error) {
	deadline := time.Now().Add(timeout)
	
	for {
		// Check if participant already exists
		for _, p := range jc.Room.GetRemoteParticipants() {
			if p.Identity() == identity {
				return p, nil
			}
		}
		
		// Check timeout
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for participant %s", identity)
		}
		
		// Wait a bit before checking again
		if err := jc.Sleep(100 * time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// PublishData publishes data to the room with optional reliability and targeting.
// If destinationIdentities is provided, the data is sent only to those participants.
// Otherwise, it's sent to all participants in the room.
func (jc *JobContext) PublishData(data []byte, reliable bool, destinationIdentities []string) error {
	opts := lksdk.WithDataPublishReliable(reliable)
	if len(destinationIdentities) > 0 {
		opts = lksdk.WithDataPublishDestination(destinationIdentities)
	}
	
	return jc.Room.LocalParticipant.PublishData(data, opts)
}

// LoadBalancer helps distribute work across multiple workers by tracking their load
// and availability. It can be used to implement custom load balancing strategies
// for job distribution in multi-worker deployments.
type LoadBalancer struct {
	workers map[string]*WorkerInfo
	mu      sync.RWMutex
}

// WorkerInfo tracks information about a worker including its current load, job count,
// and capacity. This information is used by the LoadBalancer to make distribution decisions.
type WorkerInfo struct {
	ID         string
	Load       float32
	JobCount   int
	MaxJobs    int
	LastUpdate time.Time
}

// NewLoadBalancer creates a new load balancer with an empty worker registry.
func NewLoadBalancer() *LoadBalancer {
	return &LoadBalancer{
		workers: make(map[string]*WorkerInfo),
	}
}

// UpdateWorker updates or adds information about a worker. This should be called
// periodically by workers to report their current status to the load balancer.
func (lb *LoadBalancer) UpdateWorker(id string, load float32, jobCount, maxJobs int) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	
	lb.workers[id] = &WorkerInfo{
		ID:         id,
		Load:       load,
		JobCount:   jobCount,
		MaxJobs:    maxJobs,
		LastUpdate: time.Now(),
	}
}

// RemoveWorker removes a worker from the load balancer tracking.
// This should be called when a worker shuts down or becomes unavailable.
func (lb *LoadBalancer) RemoveWorker(id string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	
	delete(lb.workers, id)
}

// GetLeastLoadedWorker returns the worker with the lowest load that is still accepting jobs.
// Workers that haven't updated in the last 30 seconds or are at capacity are ignored.
// Returns nil if no suitable worker is available.
func (lb *LoadBalancer) GetLeastLoadedWorker() *WorkerInfo {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	
	var best *WorkerInfo
	var bestLoad float32 = 2.0 // Above maximum
	
	for _, worker := range lb.workers {
		// Skip workers that haven't updated recently
		if time.Since(worker.LastUpdate) > 30*time.Second {
			continue
		}
		
		// Skip full workers
		if worker.MaxJobs > 0 && worker.JobCount >= worker.MaxJobs {
			continue
		}
		
		if worker.Load < bestLoad {
			best = worker
			bestLoad = worker.Load
		}
	}
	
	return best
}

// GetWorkerCount returns the number of active workers (those that have reported
// status within the last 30 seconds).
func (lb *LoadBalancer) GetWorkerCount() int {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	
	count := 0
	for _, worker := range lb.workers {
		if time.Since(worker.LastUpdate) <= 30*time.Second {
			count++
		}
	}
	
	return count
}