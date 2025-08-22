package mocks

import (
	"context"
	"sync"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// JobMetadata contains agent-specific metadata for a job
type JobMetadata struct {
	ParticipantIdentity   string
	ParticipantName       string
	ParticipantMetadata   string
	ParticipantAttributes map[string]string
	SupportsResume        bool
}

// MockJobHandler implements a mock JobHandler for testing
type MockJobHandler struct {
	mu                   sync.Mutex
	OnJobRequestFunc     func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata)
	OnJobAssignedFunc    func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
	OnJobTerminatedFunc  func(ctx context.Context, jobID string)
	
	// Track calls
	JobRequests    []JobRequestCall
	JobAssignments []JobAssignmentCall
	JobTerminations []JobTerminationCall
}

type JobRequestCall struct {
	Job      *livekit.Job
	Accepted bool
	Metadata *JobMetadata
}

type JobAssignmentCall struct {
	Job   *livekit.Job
	Room  *lksdk.Room
	Error error
}

type JobTerminationCall struct {
	JobID  string
}

func NewMockJobHandler() *MockJobHandler {
	return &MockJobHandler{
		JobRequests:     make([]JobRequestCall, 0),
		JobAssignments:  make([]JobAssignmentCall, 0),
		JobTerminations: make([]JobTerminationCall, 0),
	}
}

func (m *MockJobHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	accepted := true
	metadata := &JobMetadata{
		ParticipantIdentity: "test-agent-" + job.Id,
		ParticipantName:     "Test Agent",
	}
	
	if m.OnJobRequestFunc != nil {
		accepted, metadata = m.OnJobRequestFunc(ctx, job)
	}
	
	m.JobRequests = append(m.JobRequests, JobRequestCall{
		Job:      job,
		Accepted: accepted,
		Metadata: metadata,
	})
	
	return accepted, metadata
}

func (m *MockJobHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	var err error
	if m.OnJobAssignedFunc != nil {
		err = m.OnJobAssignedFunc(ctx, job, room)
	}
	
	m.JobAssignments = append(m.JobAssignments, JobAssignmentCall{
		Job:   job,
		Room:  room,
		Error: err,
	})
	
	return err
}

func (m *MockJobHandler) OnJobTerminated(ctx context.Context, jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.OnJobTerminatedFunc != nil {
		m.OnJobTerminatedFunc(ctx, jobID)
	}
	
	m.JobTerminations = append(m.JobTerminations, JobTerminationCall{
		JobID:  jobID,
	})
}

// GetJobRequests returns all job request calls
func (m *MockJobHandler) GetJobRequests() []JobRequestCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]JobRequestCall{}, m.JobRequests...)
}

// GetJobAssignments returns all job assignment calls
func (m *MockJobHandler) GetJobAssignments() []JobAssignmentCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]JobAssignmentCall{}, m.JobAssignments...)
}

// GetJobTerminations returns all job termination calls
func (m *MockJobHandler) GetJobTerminations() []JobTerminationCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]JobTerminationCall{}, m.JobTerminations...)
}

// Reset clears all recorded calls
func (m *MockJobHandler) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.JobRequests = make([]JobRequestCall, 0)
	m.JobAssignments = make([]JobAssignmentCall, 0)
	m.JobTerminations = make([]JobTerminationCall, 0)
}