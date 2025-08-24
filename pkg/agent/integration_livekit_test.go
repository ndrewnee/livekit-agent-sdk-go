//go:build !short
// +build !short

package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testLiveKitURL = "ws://localhost:7880"
	testAPIKey     = "devkey"
	testAPISecret  = "secret"
)

// TestIntegrationWorkerConnection tests actual worker connection to LiveKit server
func TestIntegrationWorkerConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, nil
		},
	}

	worker := NewUniversalWorker(testLiveKitURL, testAPIKey, testAPISecret, handler, WorkerOptions{
		AgentName: "test-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker
	go func() {
		err := worker.Start(ctx)
		if err != nil && err != context.Canceled {
			t.Logf("Worker start error (expected in test): %v", err)
		}
	}()

	// Wait for connection
	time.Sleep(2 * time.Second)

	// Check if connected
	if worker.IsConnected() {
		t.Log("Successfully connected to LiveKit server")

		// Test worker status update
		err := worker.UpdateStatus(WorkerStatusAvailable, 0.0)
		assert.NoError(t, err)

		// Get metrics
		metrics := worker.GetMetrics()
		assert.NotNil(t, metrics)

		// Get health
		health := worker.Health()
		assert.Contains(t, health, "connected")
		assert.True(t, health["connected"].(bool))
	} else {
		t.Log("Could not connect to LiveKit server - server may not be running")
	}

	// Stop worker
	err := worker.StopWithTimeout(2 * time.Second)
	if err != nil {
		t.Logf("Stop error: %v", err)
	}
}

// TestIntegrationRoomOperations tests room operations with LiveKit server
func TestIntegrationRoomOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	roomName := fmt.Sprintf("test-room-%d", time.Now().Unix())
	roomClient := lksdk.NewRoomServiceClient(testLiveKitURL, testAPIKey, testAPISecret)

	// Create room
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
	})
	if err != nil {
		t.Skipf("Could not create room - LiveKit server may not be running: %v", err)
	}
	require.NotNil(t, room)
	t.Logf("Created room: %s", room.Name)

	// Test worker with room job
	jobReceived := make(chan *livekit.Job, 1)
	roomConnected := make(chan *lksdk.Room, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			select {
			case jobReceived <- job:
			default:
			}
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
				ParticipantName:     "Test Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			if jobCtx.Room != nil {
				select {
				case roomConnected <- jobCtx.Room:
				default:
				}
			}
			return nil
		},
	}

	worker := NewUniversalWorker(testLiveKitURL, testAPIKey, testAPISecret, handler, WorkerOptions{
		AgentName: "test-room-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for worker to be ready
	time.Sleep(2 * time.Second)

	// Connect a participant to trigger potential job
	participantToken := generateToken(testAPIKey, testAPISecret, roomName, "test-participant")
	participantRoom := lksdk.NewRoom(&lksdk.RoomCallback{})

	err = participantRoom.JoinWithToken(testLiveKitURL, participantToken, lksdk.WithAutoSubscribe(false))
	if err == nil {
		defer participantRoom.Disconnect()
		t.Log("Participant connected to room")

		// Wait a bit for potential job assignment
		select {
		case job := <-jobReceived:
			t.Logf("Received job: %v", job.Id)
		case <-time.After(5 * time.Second):
			t.Log("No job received (expected if no agent dispatch configured)")
		}
	}

	// Delete room
	_, err = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: roomName,
	})
	if err != nil {
		t.Logf("Error deleting room: %v", err)
	}

	// Stop worker
	_ = worker.StopWithTimeout(2 * time.Second)
}

// TestIntegrationMultipleWorkers tests multiple workers connecting
func TestIntegrationMultipleWorkers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	const numWorkers = 3
	workers := make([]*UniversalWorker, numWorkers)
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		handler := &SimpleUniversalHandler{
			JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
				return true, nil
			},
		}

		workers[i] = NewUniversalWorker(testLiveKitURL, testAPIKey, testAPISecret, handler, WorkerOptions{
			AgentName: fmt.Sprintf("test-worker-%d", i),
			JobType:   livekit.JobType_JT_ROOM,
			MaxJobs:   2,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start all workers
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := workers[idx].Start(ctx)
			if err != nil && err != context.Canceled {
				t.Logf("Worker %d start error: %v", idx, err)
			}
		}(i)
	}

	// Wait a bit for connections
	time.Sleep(3 * time.Second)

	// Check how many connected
	connectedCount := 0
	for i, worker := range workers {
		if worker.IsConnected() {
			connectedCount++
			t.Logf("Worker %d connected", i)

			// Update status
			_ = worker.UpdateStatus(WorkerStatusAvailable, float32(i)*0.1)
		}
	}

	t.Logf("Connected workers: %d/%d", connectedCount, numWorkers)

	// Stop all workers
	for _, worker := range workers {
		go func(w *UniversalWorker) {
			_ = w.StopWithTimeout(2 * time.Second)
		}(worker)
	}

	// Cancel context to stop workers
	cancel()
	wg.Wait()
}

// TestIntegrationDataChannel tests data channel functionality
func TestIntegrationDataChannel(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	roomName := fmt.Sprintf("test-data-room-%d", time.Now().Unix())
	dataReceived := make(chan []byte, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, nil
		},
		DataReceivedFunc: func(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
			select {
			case dataReceived <- data:
			default:
			}
		},
	}

	worker := NewUniversalWorker(testLiveKitURL, testAPIKey, testAPISecret, handler, WorkerOptions{
		AgentName: "test-data-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Start worker
	go func() {
		_ = worker.Start(ctx)
	}()

	// Create room
	roomClient := lksdk.NewRoomServiceClient(testLiveKitURL, testAPIKey, testAPISecret)
	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
	})
	if err != nil {
		t.Skipf("Could not create room: %v", err)
	}

	// Connect participant
	participantToken := generateToken(testAPIKey, testAPISecret, roomName, "data-sender")
	room := lksdk.NewRoom(&lksdk.RoomCallback{})

	err = room.JoinWithToken(testLiveKitURL, participantToken)
	if err == nil {
		defer room.Disconnect()

		// Send data
		testData := []byte("test-data-message")
		err = room.LocalParticipant.PublishData(testData, lksdk.WithDataPublishReliable(true))
		if err != nil {
			t.Logf("Error publishing data: %v", err)
		} else {
			t.Log("Data published successfully")

			// Wait for data
			select {
			case data := <-dataReceived:
				assert.Equal(t, testData, data)
				t.Log("Data received by worker")
			case <-time.After(5 * time.Second):
				t.Log("No data received (worker may not be in room)")
			}
		}
	}

	// Cleanup
	_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: roomName,
	})
	_ = worker.StopWithTimeout(2 * time.Second)
}

// TestIntegrationReconnection tests worker reconnection
func TestIntegrationReconnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	reconnectCount := 0
	mu := sync.Mutex{}

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			mu.Lock()
			reconnectCount++
			mu.Unlock()
			return true, nil
		},
	}

	worker := NewUniversalWorker(testLiveKitURL, testAPIKey, testAPISecret, handler, WorkerOptions{
		AgentName: "test-reconnect-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start worker
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for initial connection
	time.Sleep(2 * time.Second)

	if worker.IsConnected() {
		t.Log("Worker connected initially")

		// Force disconnect by closing connection
		worker.mu.Lock()
		if worker.conn != nil {
			_ = worker.conn.Close()
		}
		worker.mu.Unlock()

		t.Log("Forced disconnect")

		// Wait for reconnection
		time.Sleep(5 * time.Second)

		mu.Lock()
		count := reconnectCount
		mu.Unlock()

		t.Logf("Connection attempts: %d", count)
	}

	// Stop worker
	_ = worker.StopWithTimeout(2 * time.Second)
}

// TestIntegrationShutdownHooks tests shutdown hooks execution
func TestIntegrationShutdownHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	preStopExecuted := false
	cleanupExecuted := false

	handler := &SimpleUniversalHandler{}
	worker := NewUniversalWorker(testLiveKitURL, testAPIKey, testAPISecret, handler, WorkerOptions{
		AgentName: "test-shutdown-worker",
		JobType:   livekit.JobType_JT_ROOM,
	})

	// Add shutdown hooks
	err := worker.AddPreStopHook("test-pre-stop", func(ctx context.Context) error {
		preStopExecuted = true
		t.Log("Pre-stop hook executed")
		return nil
	})
	require.NoError(t, err)

	err = worker.AddCleanupHook("test-cleanup", func(ctx context.Context) error {
		cleanupExecuted = true
		t.Log("Cleanup hook executed")
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait a bit
	time.Sleep(2 * time.Second)

	// Stop with timeout to trigger hooks
	err = worker.StopWithTimeout(3 * time.Second)

	// Check if hooks were executed
	if preStopExecuted || cleanupExecuted {
		t.Logf("Hooks executed - PreStop: %v, Cleanup: %v", preStopExecuted, cleanupExecuted)
	}
}

// TestIntegrationLoadCalculation tests load calculation with real metrics
func TestIntegrationLoadCalculation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	handler := &SimpleUniversalHandler{}
	worker := NewUniversalWorker(testLiveKitURL, testAPIKey, testAPISecret, handler, WorkerOptions{
		AgentName:           "test-load-worker",
		JobType:             livekit.JobType_JT_ROOM,
		MaxJobs:             5,
		LoadCalculator:      &DefaultLoadCalculator{},
		EnableCPUMemoryLoad: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection
	time.Sleep(2 * time.Second)

	// Get load metrics
	load := worker.GetCurrentLoad()
	t.Logf("Current load: %.2f", load)
	assert.GreaterOrEqual(t, load, float32(0))
	assert.LessOrEqual(t, load, float32(1))

	// Simulate job assignment to increase load
	worker.mu.Lock()
	worker.activeJobs["test-job-1"] = &JobContext{
		Job:       &livekit.Job{Id: "test-job-1"},
		StartedAt: time.Now(),
	}
	worker.mu.Unlock()

	// Check load increased
	newLoad := worker.GetCurrentLoad()
	t.Logf("Load after job: %.2f", newLoad)
	assert.Greater(t, newLoad, load)

	// Get metrics
	metrics := worker.GetMetrics()
	t.Logf("Worker metrics: %+v", metrics)

	// Stop worker
	_ = worker.StopWithTimeout(2 * time.Second)
}

// Helper function to generate token
func generateToken(apiKey, apiSecret, room, identity string) string {
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     room,
	}
	at.SetVideoGrant(grant)
	at.SetIdentity(identity)
	token, _ := at.ToJWT()
	return token
}
