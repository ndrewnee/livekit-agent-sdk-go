package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkerJobAssignmentFlow tests the complete job assignment flow
func TestWorkerJobAssignmentFlow(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()
	
	roomServer := newMockRoomServer()
	defer roomServer.Close()

	jobAssigned := make(chan *livekit.Job, 1)
	roomConnected := make(chan *lksdk.Room, 1)
	
	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
				ParticipantName:     "Test Agent",
			}
		},
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			jobAssigned <- job
			roomConnected <- room
			// Simulate work
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Send job availability request
	job := &livekit.Job{
		Id:   "test-job-assign",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{
			Name: "test-room",
			Sid:  "RM_test123",
		},
	}
	server.SendAvailabilityRequest(job)

	// Wait for availability response
	msg, err := server.WaitForMessage("availability", time.Second)
	require.NoError(t, err)
	assert.True(t, msg.GetAvailability().Available)

	// Send job assignment with mock room URL
	server.SendJobAssignment(job, "test-token")

	// Wait for job status update to RUNNING
	msg, err = server.WaitForMessage("updateJob", 2*time.Second)
	require.NoError(t, err)
	
	updateJob := msg.GetUpdateJob()
	assert.Equal(t, "test-job-assign", updateJob.JobId)
	assert.Equal(t, livekit.JobStatus_JS_RUNNING, updateJob.Status)

	// Verify handler was called
	select {
	case assignedJob := <-jobAssigned:
		assert.Equal(t, job.Id, assignedJob.Id)
	case <-time.After(2 * time.Second):
		t.Fatal("Job was not assigned to handler")
	}

	// Wait for job completion
	msg, err = server.WaitForMessage("updateJob", 2*time.Second)
	require.NoError(t, err)
	
	updateJob = msg.GetUpdateJob()
	assert.Equal(t, "test-job-assign", updateJob.JobId)
	assert.Equal(t, livekit.JobStatus_JS_SUCCESS, updateJob.Status)

	// Verify job was cleaned up
	worker.mu.RLock()
	_, exists := worker.activeJobs[job.Id]
	worker.mu.RUnlock()
	assert.False(t, exists, "Job should be removed after completion")
}

// TestWorkerJobAssignmentWithError tests job assignment with handler error
func TestWorkerJobAssignmentWithError(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "test-agent"}
		},
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			// Simulate error
			return assert.AnError
		},
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Send job assignment
	job := &livekit.Job{
		Id:   "test-job-error",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "test-room"},
	}
	
	// First availability
	server.SendAvailabilityRequest(job)
	_, err = server.WaitForMessage("availability", time.Second)
	require.NoError(t, err)
	
	// Then assignment
	server.SendJobAssignment(job, "test-token")

	// Should get RUNNING then FAILED status
	msg, err := server.WaitForMessage("updateJob", 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, livekit.JobStatus_JS_RUNNING, msg.GetUpdateJob().Status)

	msg, err = server.WaitForMessage("updateJob", 2*time.Second)
	require.NoError(t, err)
	updateJob := msg.GetUpdateJob()
	assert.Equal(t, livekit.JobStatus_JS_FAILED, updateJob.Status)
	assert.Contains(t, updateJob.Error, "assert.AnError")
}

// TestWorkerJobTerminationFlow tests job termination
func TestWorkerJobTerminationFlow(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	var jobCtx context.Context
	jobStarted := make(chan struct{})
	terminated := make(chan string, 1)
	
	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "test-agent"}
		},
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			jobCtx = ctx
			close(jobStarted)
			// Wait for termination
			<-ctx.Done()
			return ctx.Err()
		},
		OnJobTerminatedFunc: func(ctx context.Context, jobID string) {
			terminated <- jobID
		},
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Manually add a job to simulate it's running
	job := &livekit.Job{
		Id:   "test-job-term",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "test-room"},
	}
	
	// Create job context
	jobCtx, cancel := context.WithCancel(ctx)
	worker.mu.Lock()
	worker.activeJobs[job.Id] = &activeJob{
		job:       job,
		cancel:    cancel,
		status:    livekit.JobStatus_JS_RUNNING,
		startedAt: time.Now(),
	}
	worker.mu.Unlock()

	// Send termination
	server.SendJobTermination(job.Id)

	// Verify termination handler was called
	select {
	case jobID := <-terminated:
		assert.Equal(t, job.Id, jobID)
	case <-time.After(time.Second):
		t.Fatal("Termination handler not called")
	}

	// Verify context was cancelled
	select {
	case <-jobCtx.Done():
		// Good
	default:
		t.Error("Job context was not cancelled")
	}
}

// TestWorkerConcurrentJobs tests handling multiple concurrent jobs
func TestWorkerConcurrentJobs(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	jobCount := 3
	jobsStarted := make(chan string, jobCount)
	jobsCompleted := make(chan string, jobCount)
	
	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "test-agent"}
		},
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			jobsStarted <- job.Id
			// Simulate concurrent work
			time.Sleep(100 * time.Millisecond)
			jobsCompleted <- job.Id
			return nil
		},
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
		MaxJobs: 5,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Send multiple job requests
	var wg sync.WaitGroup
	for i := 0; i < jobCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			
			job := &livekit.Job{
				Id:   fmt.Sprintf("concurrent-job-%d", idx),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: fmt.Sprintf("room-%d", idx)},
			}
			
			// Send availability request
			server.SendAvailabilityRequest(job)
			
			// Wait for response
			msg, err := server.WaitForMessage("availability", time.Second)
			if err != nil {
				t.Errorf("Failed to get availability response for job %d: %v", idx, err)
				return
			}
			
			if msg.GetAvailability().Available {
				// Send assignment
				server.SendJobAssignment(job, "test-token")
			}
		}(i)
	}

	// Wait for all jobs to be sent
	wg.Wait()

	// Verify all jobs started
	started := make(map[string]bool)
	for i := 0; i < jobCount; i++ {
		select {
		case jobID := <-jobsStarted:
			started[jobID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("Not all jobs started")
		}
	}
	assert.Equal(t, jobCount, len(started))

	// Verify concurrent execution
	worker.mu.RLock()
	activeCount := len(worker.activeJobs)
	worker.mu.RUnlock()
	assert.Greater(t, activeCount, 1, "Should have multiple concurrent jobs")

	// Wait for completion
	completed := make(map[string]bool)
	for i := 0; i < jobCount; i++ {
		select {
		case jobID := <-jobsCompleted:
			completed[jobID] = true
		case <-time.After(2 * time.Second):
			t.Fatal("Not all jobs completed")
		}
	}
	assert.Equal(t, jobCount, len(completed))
}

// TestWorkerPingPongMechanism tests ping/pong in detail
func TestWorkerPingPongMechanism(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{}
	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 50 * time.Millisecond,
		PingTimeout:  100 * time.Millisecond,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Collect ping messages
	pings := make([]*livekit.WorkerPing, 0)
	for i := 0; i < 5; i++ {
		msg, err := server.WaitForMessage("ping", 200*time.Millisecond)
		require.NoError(t, err)
		
		ping := msg.GetPing()
		require.NotNil(t, ping)
		pings = append(pings, ping)
		
		// Verify timestamp increases
		if i > 0 {
			assert.Greater(t, ping.Timestamp, pings[i-1].Timestamp)
		}
	}

	// Verify health shows recent ping
	health := worker.Health()
	lastPing := health["last_ping_time"].(time.Time)
	assert.WithinDuration(t, time.Now(), lastPing, 200*time.Millisecond)
	
	// Test ping failure detection
	server.simulateErrors = true
	time.Sleep(200 * time.Millisecond)
	
	// Error count should increase
	health = worker.Health()
	assert.Greater(t, health["error_count"].(int), 0)
}