package agent

import (
	"context"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/agent-sdk-go/internal/test/mocks"
)

// testJobHandler wraps MockJobHandler to implement the JobHandler interface properly
type testJobHandler struct {
	mock *mocks.MockJobHandler
	
	// Direct function setters for tests
	OnJobRequestFunc    func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata)
	OnJobAssignedFunc   func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
	OnJobTerminatedFunc func(ctx context.Context, jobID string)
}

func newTestJobHandler() *testJobHandler {
	return &testJobHandler{
		mock: mocks.NewMockJobHandler(),
	}
}

func (h *testJobHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	// Use test-specific function if set
	if h.OnJobRequestFunc != nil {
		return h.OnJobRequestFunc(ctx, job)
	}
	
	// Otherwise use mock
	accepted, mockMetadata := h.mock.OnJobRequest(ctx, job)
	
	if mockMetadata == nil {
		return accepted, nil
	}
	
	// Convert mock metadata to real metadata
	metadata := &JobMetadata{
		ParticipantIdentity:   mockMetadata.ParticipantIdentity,
		ParticipantName:       mockMetadata.ParticipantName,
		ParticipantMetadata:   mockMetadata.ParticipantMetadata,
		ParticipantAttributes: mockMetadata.ParticipantAttributes,
		SupportsResume:        mockMetadata.SupportsResume,
	}
	
	return accepted, metadata
}

func (h *testJobHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	// Use test-specific function if set
	if h.OnJobAssignedFunc != nil {
		return h.OnJobAssignedFunc(ctx, job, room)
	}
	return h.mock.OnJobAssigned(ctx, job, room)
}

func (h *testJobHandler) OnJobTerminated(ctx context.Context, jobID string) {
	// Use test-specific function if set
	if h.OnJobTerminatedFunc != nil {
		h.OnJobTerminatedFunc(ctx, jobID)
		return
	}
	h.mock.OnJobTerminated(ctx, jobID)
}

// Helper methods to access mock data
func (h *testJobHandler) GetJobRequests() []mocks.JobRequestCall {
	return h.mock.GetJobRequests()
}

func (h *testJobHandler) GetJobAssignments() []mocks.JobAssignmentCall {
	return h.mock.GetJobAssignments()
}

func (h *testJobHandler) GetJobTerminations() []mocks.JobTerminationCall {
	return h.mock.GetJobTerminations()
}

func (h *testJobHandler) Reset() {
	h.mock.Reset()
}