package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkerConnect tests the full connection and registration flow
func TestWorkerConnect(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{}
	opts := WorkerOptions{
		AgentName:    "test-agent",
		Version:      "1.0.0",
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 100 * time.Millisecond,
		PingTimeout:  50 * time.Millisecond,
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, opts)
	
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start worker
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	msg, err := server.WaitForMessage("register", time.Second)
	require.NoError(t, err)
	
	register := msg.GetRegister()
	assert.NotNil(t, register)
	assert.Equal(t, livekit.JobType_JT_ROOM, register.Type)
	assert.Equal(t, "test-agent", register.AgentName)
	assert.Equal(t, "1.0.0", register.Version)

	// Wait for initial status update
	msg, err = server.WaitForMessage("update", time.Second)
	require.NoError(t, err)
	
	update := msg.GetUpdateWorker()
	assert.NotNil(t, update)
	assert.NotNil(t, update.Status)
	assert.Equal(t, livekit.WorkerStatus_WS_AVAILABLE, *update.Status)

	// Verify worker is connected
	assert.True(t, worker.IsConnected())
	assert.Equal(t, server.workerID, worker.workerID)
}

// TestWorkerPingPong tests the ping/pong keepalive mechanism
func TestWorkerPingPong(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{}
	opts := WorkerOptions{
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 50 * time.Millisecond,
		PingTimeout:  100 * time.Millisecond,
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, opts)
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration to complete
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Wait for multiple pings
	for i := 0; i < 3; i++ {
		msg, err := server.WaitForMessage("ping", 200*time.Millisecond)
		require.NoError(t, err)
		
		ping := msg.GetPing()
		assert.NotNil(t, ping)
		assert.Greater(t, ping.Timestamp, int64(0))
	}

	// Verify connection health
	health := worker.Health()
	assert.True(t, health["connected"].(bool))
	assert.Greater(t, health["last_ping_time"].(time.Time).Unix(), int64(0))
}

// TestWorkerReconnection tests reconnection after connection loss
func TestWorkerReconnection(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{}
	opts := WorkerOptions{
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 50 * time.Millisecond,
		PingTimeout:  50 * time.Millisecond,
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, opts)
	worker.savedState = &WorkerState{
		ActiveJobs: make(map[string]*JobState),
	}
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for initial connection
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Simulate ping failure by stopping server responses
	server.simulateErrors = true
	
	// Wait for reconnection to trigger
	time.Sleep(200 * time.Millisecond)
	
	// Re-enable server
	server.simulateErrors = false
	
	// Should see another registration
	msg, err := server.WaitForMessage("register", 2*time.Second)
	require.NoError(t, err)
	assert.NotNil(t, msg.GetRegister())
	
	// Worker should have preserved its ID
	assert.Equal(t, server.workerID, worker.workerID)
}

// TestWorkerJobFlow tests the complete job lifecycle
func TestWorkerJobFlow(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	acceptedJobs := make(chan *livekit.Job, 1)
	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			acceptedJobs <- job
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
				ParticipantName:     "Test Agent",
			}
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

	// Send job availability request
	job := &livekit.Job{
		Id:   "test-job-1",
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
	
	availResp := msg.GetAvailability()
	assert.NotNil(t, availResp)
	assert.Equal(t, "test-job-1", availResp.JobId)
	assert.True(t, availResp.Available)
	assert.Equal(t, "test-agent", availResp.ParticipantIdentity)

	// Verify handler was called
	select {
	case receivedJob := <-acceptedJobs:
		assert.Equal(t, job.Id, receivedJob.Id)
	case <-time.After(time.Second):
		t.Fatal("Handler not called for job request")
	}
}

// TestWorkerStatusUpdates tests worker status and load updates
func TestWorkerStatusUpdates(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{}
	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
		MaxJobs: 2,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Update status
	err = worker.UpdateStatus(WorkerStatusFull, 0.8)
	require.NoError(t, err)

	// Wait for status update message
	msg, err := server.WaitForMessage("update", time.Second)
	require.NoError(t, err)
	
	update := msg.GetUpdateWorker()
	assert.NotNil(t, update)
	assert.NotNil(t, update.Status)
	assert.Equal(t, livekit.WorkerStatus_WS_FULL, *update.Status)
	assert.Equal(t, float32(0.8), update.Load)
}

// TestWorkerGracefulShutdown tests graceful shutdown
func TestWorkerGracefulShutdown(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	shutdownStarted := make(chan struct{})
	shutdownComplete := make(chan struct{})
	
	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "test"}
		},
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			close(shutdownStarted)
			<-ctx.Done() // Wait for cancellation
			time.Sleep(50 * time.Millisecond) // Simulate cleanup
			close(shutdownComplete)
			return nil
		},
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Simulate an active job
	worker.mu.Lock()
	_, cancel := context.WithCancel(ctx)
	worker.activeJobs["job1"] = &activeJob{
		job:    &livekit.Job{Id: "job1"},
		cancel: cancel,
		status: livekit.JobStatus_JS_RUNNING,
	}
	worker.mu.Unlock()

	// Start shutdown
	go func() {
		err := worker.StopWithTimeout(200 * time.Millisecond)
		assert.NoError(t, err)
	}()

	// Wait a bit then verify shutdown state
	time.Sleep(50 * time.Millisecond)
	assert.True(t, worker.closing)

	// Verify connection was closed
	time.Sleep(300 * time.Millisecond)
	assert.False(t, worker.IsConnected())
}

// TestWorkerMessageValidation tests message validation
func TestWorkerMessageValidation(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{}
	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Test various message scenarios
	tests := []struct {
		name string
		msg  *livekit.ServerMessage
		valid bool
	}{
		{
			name: "valid availability request",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Availability{
					Availability: &livekit.AvailabilityRequest{
						Job: &livekit.Job{
							Id:   "job1",
							Room: &livekit.Room{Name: "room1"},
						},
					},
				},
			},
			valid: true,
		},
		{
			name: "missing job in availability",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Availability{
					Availability: &livekit.AvailabilityRequest{},
				},
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := worker.validateServerMessage(tt.msg)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

// TestWorkerJobTermination tests job termination handling
func TestWorkerJobTermination(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	terminatedJobs := make(chan string, 1)
	handler := &testJobHandler{
		OnJobTerminatedFunc: func(ctx context.Context, jobID string) {
			terminatedJobs <- jobID
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

	// Add an active job
	jobCtx, cancel := context.WithCancel(ctx)
	worker.mu.Lock()
	worker.activeJobs["job1"] = &activeJob{
		job:    &livekit.Job{Id: "job1"},
		cancel: cancel,
		status: livekit.JobStatus_JS_RUNNING,
	}
	worker.mu.Unlock()

	// Send termination
	server.SendJobTermination("job1")

	// Verify handler was called
	select {
	case jobID := <-terminatedJobs:
		assert.Equal(t, "job1", jobID)
	case <-time.After(time.Second):
		t.Fatal("Termination handler not called")
	}

	// Verify context was cancelled
	select {
	case <-jobCtx.Done():
		// Good
	default:
		t.Error("Job context not cancelled")
	}
}

// TestWorkerConnectionFailure tests connection failure handling
func TestWorkerConnectionFailure(t *testing.T) {
	server := newMockWebSocketServer()
	server.closeOnConnect = true
	defer server.Close()

	handler := &testJobHandler{}
	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	assert.Error(t, err)
	assert.False(t, worker.IsConnected())
}

// TestWorkerRegistrationTimeout tests registration timeout
func TestWorkerRegistrationTimeout(t *testing.T) {
	server := newMockWebSocketServer()
	server.delayResponse = 15 * time.Second // Longer than registration timeout
	defer server.Close()

	handler := &testJobHandler{}
	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "registration timed out"))
}

// TestWorkerInvalidAuth tests invalid authentication
func TestWorkerInvalidAuth(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{}
	// Use invalid credentials
	worker := NewWorker(server.URL(), "", "", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	assert.Error(t, err)
}

// TestWorkerMaxJobs tests max jobs enforcement
func TestWorkerMaxJobs(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "test"}
		},
	}

	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
		MaxJobs: 1,
	})
	
	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Add an active job to reach max
	worker.mu.Lock()
	worker.activeJobs["existing"] = &activeJob{
		job: &livekit.Job{Id: "existing"},
	}
	worker.mu.Unlock()

	// Send job request
	job := &livekit.Job{
		Id:   "new-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room"},
	}
	server.SendAvailabilityRequest(job)

	// Wait for response
	msg, err := server.WaitForMessage("availability", time.Second)
	require.NoError(t, err)
	
	availResp := msg.GetAvailability()
	assert.False(t, availResp.Available) // Should reject due to max jobs
}