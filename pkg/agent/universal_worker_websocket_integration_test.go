//go:build integration

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test helper to get server config
func getServerConfig() (string, string, string) {
	url := "ws://localhost:7880"
	apiKey := "devkey"
	apiSecret := "secret"
	return url, apiKey, apiSecret
}

// TestUniversalWorker_WebSocket_Connection tests WebSocket connection establishment
func TestUniversalWorker_WebSocket_Connection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	url, apiKey, apiSecret := getServerConfig()

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

	// Wait for connection
	time.Sleep(2 * time.Second)

	// Check connection status
	assert.True(t, worker.IsConnected(), "Worker should be connected")
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

	url, apiKey, apiSecret := getServerConfig()

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

	// Wait for connection
	time.Sleep(2 * time.Second)
	require.True(t, worker.IsConnected())

	// Create a room to trigger a job
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)
	roomName := fmt.Sprintf("test-room-%d", time.Now().Unix())

	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:            roomName,
		EmptyTimeout:    60,
		MaxParticipants: 10,
	})
	require.NoError(t, err)

	// Dispatch a job to our agent
	_, err = roomClient.UpdateRoomMetadata(context.Background(), &livekit.UpdateRoomMetadataRequest{
		Room:     roomName,
		Metadata: `{"agent_request": "test"}`,
	})

	// Wait for job to be received and processed
	require.Eventually(t, func() bool {
		return jobReceived.Load()
	}, 10*time.Second, 100*time.Millisecond, "Job should be received")

	require.Eventually(t, func() bool {
		return jobAssigned.Load()
	}, 10*time.Second, 100*time.Millisecond, "Job should be assigned")

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

	url, apiKey, apiSecret := getServerConfig()

	var connectionCount atomic.Int32
	reconnected := make(chan bool, 1)

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
				count := connectionCount.Add(1)
				if count > 1 {
					select {
					case reconnected <- true:
					default:
					}
				}
			}
			wasConnected = isConnected
			time.Sleep(100 * time.Millisecond)
		}
	}()

	// Start worker
	go func() {
		worker.Start(ctx)
	}()

	// Wait for initial connection
	time.Sleep(2 * time.Second)
	require.True(t, worker.IsConnected())
	assert.Equal(t, int32(1), connectionCount.Load())

	// Force disconnect by closing WebSocket
	worker.mu.Lock()
	if worker.conn != nil {
		worker.conn.Close()
		worker.wsState = WebSocketStateDisconnected
	}
	worker.mu.Unlock()

	// Wait for reconnection
	select {
	case <-reconnected:
		t.Log("Successfully reconnected")
	case <-time.After(10 * time.Second):
		t.Fatal("Reconnection timeout")
	}

	// Verify reconnected
	assert.Eventually(t, func() bool {
		return worker.IsConnected()
	}, 5*time.Second, 100*time.Millisecond)

	assert.GreaterOrEqual(t, connectionCount.Load(), int32(2), "Should have reconnected at least once")

	worker.Stop()
}

// TestUniversalWorker_WebSocket_PingPong tests ping/pong heartbeat
func TestUniversalWorker_WebSocket_PingPong(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	url, apiKey, apiSecret := getServerConfig()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return false, nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName:    "test-ping-worker",
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 1 * time.Second, // Fast ping for testing
		PingTimeout:  500 * time.Millisecond,
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

	// Record initial ping time
	initialPing := worker.healthCheck.lastPing

	// Wait for a few ping cycles
	time.Sleep(3 * time.Second)

	// Check that pings are happening
	assert.True(t, worker.healthCheck.lastPing.After(initialPing), "Ping should have been sent")
	assert.True(t, worker.healthCheck.isHealthy, "Connection should be healthy")
	assert.Equal(t, 0, worker.healthCheck.missedPings, "Should not have missed pings")

	worker.Stop()
}

// TestUniversalWorker_WebSocket_StatusUpdates tests status update sending
func TestUniversalWorker_WebSocket_StatusUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	url, apiKey, apiSecret := getServerConfig()

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

	// Wait for connection
	time.Sleep(2 * time.Second)
	require.True(t, worker.IsConnected())

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

	url, apiKey, apiSecret := getServerConfig()

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
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)
	roomName := fmt.Sprintf("test-term-room-%d", time.Now().Unix())

	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
	})
	require.NoError(t, err)

	// Wait for job to be assigned
	time.Sleep(3 * time.Second)

	// Delete room to trigger termination
	_, err = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: room.Name,
	})
	require.NoError(t, err)

	// Wait for termination
	select {
	case jobID := <-jobTerminated:
		t.Logf("Job terminated: %s", jobID)
	case <-time.After(10 * time.Second):
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

	url, apiKey, apiSecret := getServerConfig()

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

	// Create multiple rooms concurrently
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)
	numRooms := 5

	for i := 0; i < numRooms; i++ {
		wg.Add(1)
		go func(idx int) {
			roomName := fmt.Sprintf("concurrent-room-%d-%d", idx, time.Now().Unix())
			_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
				Name: roomName,
			})
			if err != nil {
				t.Logf("Error creating room: %v", err)
			}

			// Clean up after a delay
			time.Sleep(5 * time.Second)
			roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
				Room: roomName,
			})
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
	url, _, _ := getServerConfig()

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

	url, apiKey, apiSecret := getServerConfig()

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
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)

	for i := 0; i < 2; i++ {
		roomName := fmt.Sprintf("load-room-%d-%d", i, time.Now().Unix())
		_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
			Name: roomName,
		})
		require.NoError(t, err)
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

	url, apiKey, apiSecret := getServerConfig()

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
