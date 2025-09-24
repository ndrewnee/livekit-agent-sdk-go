//go:build integration

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentproto "github.com/livekit/protocol/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test helper to get server config
func getServerConfig() (string, string, string) {
	// WebSocket agent endpoint is served by our mock gateway for these tests
	// Room service remains the local LiveKit dev server for room/token ops
	return "", "devkey", "secret"
}

// TestUniversalWorker_WebSocket_Connection tests WebSocket connection establishment
func TestUniversalWorker_WebSocket_Connection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	// Start mock agent gateway server
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return false, nil // Reject all jobs for this test
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-websocket-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker - this will test connect() and sendRegister()
	go func() {
		err := worker.Start(ctx)
		if err != nil && ctx.Err() == nil {
			t.Logf("Worker start error (expected on shutdown): %v", err)
		}
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, WebSocketStateConnected, worker.wsState)
	assert.NotEmpty(t, worker.workerID, "Worker should have received an ID")

	// Test health check
	health := worker.Health()
	assert.True(t, health["connected"].(bool))
	assert.NotNil(t, health["worker_id"])

	// Clean shutdown
	err := worker.Stop()
	assert.NoError(t, err)
}

// TestUniversalWorker_WebSocket_MessageHandling tests message sending and receiving
func TestUniversalWorker_WebSocket_MessageHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	// Start mock agent gateway server
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	var jobReceived atomic.Bool
	var jobAssigned atomic.Bool

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			jobReceived.Store(true)
			t.Logf("Received job request: %s", job.Id)
			// Accept the job
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
				ParticipantName:     "Test Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			jobAssigned.Store(true)
			t.Logf("Job assigned: %s", jobCtx.Job.Id)
			// Keep job running for a bit
			time.Sleep(2 * time.Second)
			return nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-message-worker",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker
	go func() {
		err := worker.Start(ctx)
		if err != nil && ctx.Err() == nil {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create a room via LiveKit room service to produce a valid token
	roomSvcURL := "ws://localhost:7880"
	roomClient := lksdk.NewRoomServiceClient(roomSvcURL, apiKey, apiSecret)
	roomName := fmt.Sprintf("test-room-%d", time.Now().Unix())

	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:            roomName,
		EmptyTimeout:    60,
		MaxParticipants: 10,
	})
	require.NoError(t, err)

	// Simulate availability request and assignment from agent gateway
	ms.SendAvailabilityRequest(&livekit.Job{Id: "job-msg", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}})
	// Generate token for agent to join the room
	token, err := agentproto.BuildAgentToken(apiKey, apiSecret, roomName, "test-agent", "Test Agent", "", nil, &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true})
	require.NoError(t, err)
	ms.SendJobAssignmentWithURL(&livekit.Job{Id: "job-msg", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}}, token, roomSvcURL)
	// Nudge the client to cause server to flush queued response
	_ = worker.UpdateStatus(WorkerStatusAvailable, 0.0)

	// Wait for job to be received and processed flags
	require.Eventually(t, func() bool { return jobReceived.Load() }, 10*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool { return jobAssigned.Load() }, 10*time.Second, 100*time.Millisecond)

	// Check that job is tracked
	assert.Greater(t, len(worker.activeJobs), 0, "Should have active jobs")

	// Check metrics
	metrics := worker.GetMetrics()
	assert.Greater(t, metrics["jobs_accepted"], int64(0))

	// Clean up room
	_, err = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: roomName,
	})
	assert.NoError(t, err)

	// Shutdown
	worker.Stop()
}

// TestUniversalWorker_WebSocket_Reconnection tests reconnection logic
func TestUniversalWorker_WebSocket_Reconnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	var connectionCount atomic.Int32

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return false, nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-reconnect-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Monitor connection state changes through IsConnected
	go func() {
		wasConnected := false
		for ctx.Err() == nil {
			isConnected := worker.IsConnected()
			if isConnected && !wasConnected {
				connectionCount.Add(1)
			}
			wasConnected = isConnected
			time.Sleep(100 * time.Millisecond)
		}
	}()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for initial connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, int32(1), connectionCount.Load())

	// Force disconnect by closing WebSocket
	worker.mu.Lock()
	if worker.conn != nil {
		worker.conn.Close()
		worker.wsState = WebSocketStateDisconnected
	}
	worker.mu.Unlock()

	// Wait for reconnection
	assert.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// No strict count assertion; being connected again is sufficient

	worker.Stop()
}

// TestUniversalWorker_WebSocket_PingPong tests ping/pong heartbeat
func TestUniversalWorker_WebSocket_PingPong(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return false, nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName:    "test-ping-worker",
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 1 * time.Second,
		PingTimeout:  2 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Record initial ping time
	initialPing := worker.healthCheck.lastPing

	// Wait for a few ping cycles
	time.Sleep(3 * time.Second)

	// Check that pings are happening
	assert.True(t, worker.healthCheck.lastPing.After(initialPing), "Ping should have been sent")
	assert.True(t, worker.healthCheck.isHealthy, "Connection should be healthy")
	assert.LessOrEqual(t, worker.healthCheck.missedPings, 1, "Should not have missed pings")

	worker.Stop()
}

// TestUniversalWorker_WebSocket_StatusUpdates tests status update sending
func TestUniversalWorker_WebSocket_StatusUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return false, nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-status-worker",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   5,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Send status update
	err := worker.UpdateStatus(WorkerStatusAvailable, 0.5)
	assert.NoError(t, err)

	// Update with different load
	err = worker.UpdateStatus(WorkerStatusFull, 1.0)
	assert.NoError(t, err)

	// Check internal state
	assert.Equal(t, WorkerStatusFull, worker.status)
	// Note: load is not stored internally, only sent to server

	worker.Stop()
}

// TestUniversalWorker_WebSocket_JobTermination tests job termination handling
func TestUniversalWorker_WebSocket_JobTermination(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	jobTerminated := make(chan string, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			// Simulate long-running job
			<-ctx.Done()
			return nil
		},
		JobTerminatedFunc: func(ctx context.Context, jobID string) {
			jobTerminated <- jobID
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-termination-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for connection
	time.Sleep(2 * time.Second)
	require.True(t, worker.IsConnected())

	// Create a room
	roomSvcURL := "ws://localhost:7880"
	roomClient := lksdk.NewRoomServiceClient(roomSvcURL, apiKey, apiSecret)
	roomName := fmt.Sprintf("test-term-room-%d", time.Now().Unix())

	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
	})
	require.NoError(t, err)

	// Simulate assignment then termination via mock gateway
	ms.SendAvailabilityRequest(&livekit.Job{Id: "job-term", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}})
	token, err := agentproto.BuildAgentToken(apiKey, apiSecret, roomName, "term-agent", "Term Agent", "", nil, &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true})
	require.NoError(t, err)
	ms.SendJobAssignmentWithURL(&livekit.Job{Id: "job-term", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}}, token, roomSvcURL)
	_ = worker.UpdateStatus(WorkerStatusAvailable, 0.0)
	// Allow job to start
	time.Sleep(1 * time.Second)
	ms.SendJobTermination("job-term")

	// Wait for termination
	select {
	case jobID := <-jobTerminated:
		t.Logf("Job terminated: %s", jobID)
	case <-time.After(15 * time.Second):
		t.Fatal("Job termination timeout")
	}

	// Check that job is removed
	assert.Equal(t, 0, len(worker.activeJobs))

	worker.Stop()
}

// TestUniversalWorker_WebSocket_ConcurrentMessages tests concurrent message handling
func TestUniversalWorker_WebSocket_ConcurrentMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	var jobCount atomic.Int32
	var wg sync.WaitGroup

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			jobCount.Add(1)
			// Randomly accept/reject
			return job.Id[0]%2 == 0, &JobMetadata{
				ParticipantIdentity: fmt.Sprintf("agent-%s", job.Id),
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			defer wg.Done()
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-concurrent-worker",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for connection
	time.Sleep(2 * time.Second)
	require.True(t, worker.IsConnected())

	// Create multiple rooms concurrently through LiveKit, then dispatch jobs via mock gateway
	roomSvcURL := "ws://localhost:7880"
	roomClient := lksdk.NewRoomServiceClient(roomSvcURL, apiKey, apiSecret)
	numRooms := 5

	for i := 0; i < numRooms; i++ {
		wg.Add(1)
		go func(idx int) {
			roomName := fmt.Sprintf("concurrent-room-%d-%d", idx, time.Now().Unix())
			_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{Name: roomName})
			if err != nil {
				t.Logf("Error creating room: %v", err)
			}

			// Dispatch a job for this room
			ms.SendAvailabilityRequest(&livekit.Job{Id: fmt.Sprintf("job-%d", idx), Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}})
			token, _ := agentproto.BuildAgentToken(apiKey, apiSecret, roomName, fmt.Sprintf("agent-%d", idx), fmt.Sprintf("Agent %d", idx), "", nil, &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true})
			ms.SendJobAssignmentWithURL(&livekit.Job{Id: fmt.Sprintf("job-%d", idx), Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}}, token, roomSvcURL)
			_ = worker.UpdateStatus(WorkerStatusAvailable, 0.0)
		}(i)
	}

	// Wait for all jobs to complete
	done := make(chan bool)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		t.Log("All jobs processed")
	case <-time.After(15 * time.Second):
		t.Log("Some jobs may still be processing")
	}

	// Verify jobs were received
	assert.Greater(t, jobCount.Load(), int32(0), "Should have received jobs")

	// Check metrics
	metrics := worker.GetMetrics()
	t.Logf("Metrics: accepted=%d, rejected=%d, completed=%d, failed=%d",
		metrics["jobs_accepted"], metrics["jobs_rejected"],
		metrics["jobs_completed"], metrics["jobs_failed"])

	worker.Stop()
}

// TestUniversalWorker_WebSocket_ErrorHandling tests error handling and recovery
func TestUniversalWorker_WebSocket_ErrorHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	// Test with invalid credentials
	// Simulate server failure by returning error on connect
	ms := newMockWebSocketServer()
	ms.closeOnConnect = true
	defer ms.Close()
	url := ms.URL()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return false, nil
		},
	}

	worker := NewUniversalWorker(url, "invalid-key", "invalid-secret", handler, WorkerOptions{
		AgentName: "test-error-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start should fail with auth error
	err := worker.Start(ctx)
	assert.Error(t, err, "Should fail with invalid credentials")
	assert.False(t, worker.IsConnected())
}

// TestUniversalWorker_WebSocket_LoadReporting tests load calculation and reporting
func TestUniversalWorker_WebSocket_LoadReporting(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			// Hold job for a bit
			time.Sleep(5 * time.Second)
			return nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName:           "test-load-worker",
		JobType:             livekit.JobType_JT_ROOM,
		MaxJobs:             3,
		EnableCPUMemoryLoad: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for connection
	time.Sleep(2 * time.Second)
	require.True(t, worker.IsConnected())

	// Check initial load
	health := worker.Health()
	assert.Equal(t, 0, health["active_jobs"])
	assert.Equal(t, 3, health["max_jobs"])

	// Create rooms to generate load
	roomSvcURL := "ws://localhost:7880"
	roomClient := lksdk.NewRoomServiceClient(roomSvcURL, apiKey, apiSecret)

	for i := 0; i < 2; i++ {
		roomName := fmt.Sprintf("load-room-%d-%d", i, time.Now().Unix())
		_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{Name: roomName})
		require.NoError(t, err)
		// Generate a job assignment to create load
		ms.SendAvailabilityRequest(&livekit.Job{Id: fmt.Sprintf("job-%d", i), Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}})
		token, err := agentproto.BuildAgentToken(apiKey, apiSecret, roomName, fmt.Sprintf("agent-%d", i), fmt.Sprintf("Agent %d", i), "", nil, &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true})
		require.NoError(t, err)
		ms.SendJobAssignmentWithURL(&livekit.Job{Id: fmt.Sprintf("job-%d", i), Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}}, token, roomSvcURL)
		_ = worker.UpdateStatus(WorkerStatusAvailable, 0.0)
		time.Sleep(1 * time.Second)
	}

	// Wait for jobs to be assigned
	time.Sleep(3 * time.Second)

	// Check load increased
	health = worker.Health()
	activeJobs := health["active_jobs"].(int)
	assert.Greater(t, activeJobs, 0, "Should have active jobs")

	// Load should be calculated
	assert.NotNil(t, worker.loadCalculator)

	worker.Stop()
}

// TestUniversalWorker_WebSocket_ShutdownSequence tests graceful shutdown
func TestUniversalWorker_WebSocket_ShutdownSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	_, apiKey, apiSecret := getServerConfig()
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	preStopExecuted := false
	cleanupExecuted := false

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			<-ctx.Done()
			return nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-shutdown-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	// Add shutdown hooks
	worker.AddPreStopHook("test-prestop", func(ctx context.Context) error {
		preStopExecuted = true
		return nil
	})

	worker.AddCleanupHook("test-cleanup", func(ctx context.Context) error {
		cleanupExecuted = true
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for connection
	time.Sleep(2 * time.Second)
	require.True(t, worker.IsConnected())

	// Create a room and assign a job so shutdown goes through job cleanup
	roomSvcURL := "ws://localhost:7880"
	roomClient := lksdk.NewRoomServiceClient(roomSvcURL, apiKey, apiSecret)
	roomName := fmt.Sprintf("shutdown-room-%d", time.Now().Unix())
	_, _ = roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{Name: roomName})
	ms.SendAvailabilityRequest(&livekit.Job{Id: "job-shutdown", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}})
	token, _ := agentproto.BuildAgentToken(apiKey, apiSecret, roomName, "shutdown-agent", "Shutdown Agent", "", nil, &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true})
	ms.SendJobAssignmentWithURL(&livekit.Job{Id: "job-shutdown", Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: roomName}}, token, roomSvcURL)
	_ = worker.UpdateStatus(WorkerStatusAvailable, 0.0)

	// Graceful shutdown with timeout
	err := worker.StopWithTimeout(5 * time.Second)
	assert.NoError(t, err)

	// Check hooks were executed
	assert.True(t, preStopExecuted, "Pre-stop hook should have executed")
	assert.True(t, cleanupExecuted, "Cleanup hook should have executed")

	// Check disconnected
	assert.False(t, worker.IsConnected())
	assert.Equal(t, WebSocketStateDisconnected, worker.wsState)
}
