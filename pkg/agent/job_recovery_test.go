package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

// TestJobRecoveryManager tests basic job recovery functionality
func TestJobRecoveryManager(t *testing.T) {
	worker := newMockWorker(nil)

	recoveryHandler := &mockJobRecoveryHandler{
		shouldRecover: true,
	}
	manager := NewJobRecoveryManager(worker, recoveryHandler)

	// Save a job for recovery
	job := &livekit.Job{
		Id:   "test-job-1",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{
			Name: "test-room",
		},
	}
	manager.SaveJobForRecovery(job.Id, job, "test-token")

	// Verify job is saved
	jobs := manager.GetRecoverableJobs()
	assert.Len(t, jobs, 1)
	assert.Equal(t, "test-job-1", jobs["test-job-1"].JobID)
	assert.Equal(t, "test-token", jobs["test-job-1"].RoomToken)
}

// TestJobRecoveryAttempt tests the recovery attempt process
func TestJobRecoveryAttempt(t *testing.T) {
	worker := newMockWorker(nil)

	recoveryHandler := &mockJobRecoveryHandler{
		shouldRecover: true,
	}
	manager := NewJobRecoveryManager(worker, recoveryHandler)

	// Save multiple jobs
	jobs := []*livekit.Job{
		{
			Id:   "job-1",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room-1"},
		},
		{
			Id:   "job-2",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room-2"},
		},
	}

	for _, job := range jobs {
		// Use empty token to avoid connection attempts during test
		manager.SaveJobForRecovery(job.Id, job, "")
	}

	// Attempt recovery
	ctx := context.Background()
	results := manager.AttemptJobRecovery(ctx)

	// Should have results for both jobs
	assert.Len(t, results, 2)

	// Both should fail because we don't have valid tokens
	assert.NotNil(t, results["job-1"])
	assert.NotNil(t, results["job-2"])

	// Verify recovery handler was called
	assert.Equal(t, 2, recoveryHandler.getAttemptCalls())
	assert.Equal(t, 2, recoveryHandler.getFailedCalls())
}

// TestJobRecoveryTimeout tests that old jobs are not recovered
func TestJobRecoveryTimeout(t *testing.T) {
	worker := newMockWorker(nil)

	recoveryHandler := &mockJobRecoveryHandler{
		shouldRecover: true,
	}
	manager := NewJobRecoveryManager(worker, recoveryHandler)
	manager.recoveryTimeout = 1 * time.Millisecond // Very short timeout

	// Save a job
	job := &livekit.Job{
		Id:   "old-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room"},
	}
	manager.SaveJobForRecovery(job.Id, job, "token")

	// Wait for timeout
	time.Sleep(2 * time.Millisecond)

	// Attempt recovery
	ctx := context.Background()
	results := manager.AttemptJobRecovery(ctx)

	// Should have failed due to timeout
	assert.Len(t, results, 1)
	assert.Contains(t, results["old-job"].Error(), "too old")

	// Job should be removed from recovery
	jobs := manager.GetRecoverableJobs()
	assert.Len(t, jobs, 0)
}

// TestPartialMessageBuffer tests partial message handling
func TestPartialMessageBuffer(t *testing.T) {
	buffer := NewPartialMessageBuffer(1024)

	// Test appending data
	err := buffer.Append(1, []byte(`{"partial":`))
	assert.NoError(t, err)

	// Should not have complete message yet
	_, _, ok := buffer.GetComplete()
	assert.False(t, ok)

	// Complete the JSON
	err = buffer.Append(1, []byte(`"message"}`))
	assert.NoError(t, err)

	// Should now have complete message
	msgType, data, ok := buffer.GetComplete()
	assert.True(t, ok)
	assert.Equal(t, 1, msgType)
	assert.Equal(t, `{"partial":"message"}`, string(data))

	// Buffer should be cleared after getting complete message
	_, _, ok = buffer.GetComplete()
	assert.False(t, ok)
}

// TestPartialMessageBufferSizeLimit tests buffer size limits
func TestPartialMessageBufferSizeLimit(t *testing.T) {
	buffer := NewPartialMessageBuffer(10) // Very small buffer

	// Try to append data that exceeds limit
	err := buffer.Append(1, []byte("this is too long"))
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "exceeds limit")
	}
}

// TestPartialMessageBufferStale tests stale buffer detection
func TestPartialMessageBufferStale(t *testing.T) {
	buffer := NewPartialMessageBuffer(1024)

	// Add some data
	err := buffer.Append(1, []byte("partial"))
	assert.NoError(t, err)

	// Should not be stale immediately
	assert.False(t, buffer.IsStale(1*time.Hour))

	// Should be stale with very short timeout
	assert.True(t, buffer.IsStale(1*time.Nanosecond))
}

// TestJobCheckpoint tests job checkpoint functionality
func TestJobCheckpoint(t *testing.T) {
	checkpoint := NewJobCheckpoint("test-job")

	// Save some data
	checkpoint.Save("progress", 50)
	checkpoint.Save("status", "processing")
	checkpoint.Save("items", []string{"a", "b", "c"})

	// Load data
	progress, ok := checkpoint.Load("progress")
	assert.True(t, ok)
	assert.Equal(t, 50, progress)

	status, ok := checkpoint.Load("status")
	assert.True(t, ok)
	assert.Equal(t, "processing", status)

	// Non-existent key
	_, ok = checkpoint.Load("missing")
	assert.False(t, ok)

	// Get all data
	all := checkpoint.GetAll()
	assert.Len(t, all, 3)
	assert.Equal(t, 50, all["progress"])
	assert.Equal(t, "processing", all["status"])

	// Clear checkpoint
	checkpoint.Clear()
	all = checkpoint.GetAll()
	assert.Len(t, all, 0)
}

// TestWorkerJobRecoveryIntegration tests job recovery integration with worker
func TestWorkerJobRecoveryIntegration(t *testing.T) {
	recoveryHandler := &mockJobRecoveryHandler{
		shouldRecover: true,
	}

	// Using UniversalWorker instead of deprecated Worker
	handler := &MockUniversalHandler{}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, WorkerOptions{
		JobType:            livekit.JobType_JT_ROOM,
		EnableJobRecovery:  true,
		JobRecoveryHandler: recoveryHandler,
	})
	defer worker.Stop() // Ensure cleanup

	// Verify recovery manager is created
	assert.NotNil(t, worker.recoveryManager)
}

// TestDefaultJobRecoveryHandler tests the default recovery handler
func TestDefaultJobRecoveryHandler(t *testing.T) {
	handler := &DefaultJobRecoveryHandler{}

	ctx := context.Background()

	// Should recover running jobs
	runningJob := &JobState{
		JobID:  "job-1",
		Status: livekit.JobStatus_JS_RUNNING,
	}
	assert.True(t, handler.OnJobRecoveryAttempt(ctx, "job-1", runningJob))

	// Should not recover completed jobs
	completedJob := &JobState{
		JobID:  "job-2",
		Status: livekit.JobStatus_JS_SUCCESS,
	}
	assert.False(t, handler.OnJobRecoveryAttempt(ctx, "job-2", completedJob))

	// Should not recover failed jobs
	failedJob := &JobState{
		JobID:  "job-3",
		Status: livekit.JobStatus_JS_FAILED,
	}
	assert.False(t, handler.OnJobRecoveryAttempt(ctx, "job-3", failedJob))
}

// mockJobRecoveryHandler for testing
type mockJobRecoveryHandler struct {
	mu             sync.Mutex
	shouldRecover  bool
	attemptCalls   int
	recoveredCalls int
	failedCalls    int
}

func (m *mockJobRecoveryHandler) OnJobRecoveryAttempt(ctx context.Context, jobID string, jobState *JobState) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attemptCalls++
	return m.shouldRecover
}

func (m *mockJobRecoveryHandler) OnJobRecovered(ctx context.Context, job *livekit.Job, room *lksdk.Room) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveredCalls++
}

func (m *mockJobRecoveryHandler) OnJobRecoveryFailed(ctx context.Context, jobID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failedCalls++
}

func (m *mockJobRecoveryHandler) getAttemptCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attemptCalls
}

func (m *mockJobRecoveryHandler) getRecoveredCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoveredCalls
}

func (m *mockJobRecoveryHandler) getFailedCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failedCalls
}
