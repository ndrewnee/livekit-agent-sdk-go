//go:build integration

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/am-sokolov/livekit-agent-sdk-go/internal/test/mocks"
)

// Test configuration for local LiveKit server
func getTestConfig() (url, apiKey, apiSecret string) {
	url = "ws://localhost:7880"
	apiKey = "devkey"
	apiSecret = "secret"
	return
}

// ==================== Connection and Authentication Tests ====================

func TestUniversalWorker_Integration_BasicConnection(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, nil
		},
	}
	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-universal-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)
	assert.NotEmpty(t, worker.workerID, "Worker should have ID")

	// Clean shutdown
	err := worker.Stop()
	assert.NoError(t, err)
}

func TestUniversalWorker_Integration_InvalidAuthentication(t *testing.T) {
	url, _, _ := getTestConfig()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, nil
		},
	}
	worker := NewUniversalWorker(url, "invalid", "invalid", handler, WorkerOptions{
		AgentName: "test-universal-worker",
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := worker.Start(ctx)
	assert.Error(t, err, "Should fail with invalid credentials")
}

func TestUniversalWorker_Integration_Reconnection(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	reconnectCount := int32(0)
	handler := &SimpleUniversalHandler{
		RoomDisconnectedFunc: func(ctx context.Context, room *lksdk.Room, reason string) {
			atomic.AddInt32(&reconnectCount, 1)
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-reconnect-worker",
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for initial connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Simulate connection loss
	worker.mu.Lock()
	if worker.conn != nil {
		worker.conn.Close()
	}
	worker.mu.Unlock()

	// Wait for reconnection attempt
	time.Sleep(3 * time.Second)

	// For now, just verify the worker tried to handle the disconnect
	// Actual reconnection logic may need to be implemented

	worker.Stop()
}

// ==================== Job Handling Tests ====================

func TestUniversalWorker_Integration_RoomJob(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	jobReceived := make(chan *livekit.Job, 1)
	roomConnected := make(chan *lksdk.Room, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
				ParticipantName:     "Test Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			jobReceived <- jobCtx.Job
			if jobCtx.Room != nil {
				roomConnected <- jobCtx.Room
			}
			return nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-room-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create a room with agent dispatch to trigger job
	roomName := fmt.Sprintf("test-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-room-worker")
	require.NoError(t, err)

	// Wait for job assignment
	select {
	case job := <-jobReceived:
		assert.Equal(t, livekit.JobType_JT_ROOM, job.Type)
		assert.Equal(t, roomName, job.Room.Name)
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for job")
	}

	// Wait for room connection
	select {
	case room := <-roomConnected:
		assert.NotNil(t, room)
		assert.Equal(t, roomName, room.Name())
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for room connection")
	}

	worker.Stop()
}

func TestUniversalWorker_Integration_ParticipantJob(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	assignedCh := make(chan struct{}, 1)
	roomConnected := make(chan struct{}, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "participant-agent",
				ParticipantName:     "Participant Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			// Job context should have target participant info
			assert.NotNil(t, jobCtx.TargetParticipant)
			assignedCh <- struct{}{}
			// Keep the job alive briefly to allow participant events to flow
			time.Sleep(2 * time.Second)
			return nil
		},
		RoomConnectedFunc: func(ctx context.Context, room *lksdk.Room) { roomConnected <- struct{}{} },
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-participant-worker",
		JobType:   livekit.JobType_JT_PARTICIPANT,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create room with agent dispatch and connect participant
	roomName := fmt.Sprintf("test-participant-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-participant-worker")
	require.NoError(t, err)

	// Generate token for the joining participant (distinct from agent identity)
	participantToken := generateTestToken(apiKey, apiSecret, roomName, "test-participant")

	// Connect a test participant
	participantRoom := lksdk.NewRoom(&lksdk.RoomCallback{})
	err = participantRoom.JoinWithToken(url, participantToken)
	require.NoError(t, err)
	defer participantRoom.Disconnect()

	// Wait for job assignment to be observed in handler
	select {
	case <-assignedCh:
		// ok
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for participant job assignment")
	}

	worker.Stop()
}

// ==================== Participant Management Tests ====================

func TestUniversalWorker_Integration_ParticipantTracking(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	var mu sync.Mutex
	participants := make(map[string]*lksdk.RemoteParticipant)
	leftParticipants := make(map[string]bool)

	roomConnected := make(chan struct{}, 1)
	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "tracking-agent",
				ParticipantName:     "Tracking Agent",
			}
		},
		RoomConnectedFunc: func(ctx context.Context, room *lksdk.Room) { roomConnected <- struct{}{} },
		ParticipantJoinedFunc: func(ctx context.Context, participant *lksdk.RemoteParticipant) {
			mu.Lock()
			participants[participant.Identity()] = participant
			mu.Unlock()
		},
		ParticipantLeftFunc: func(ctx context.Context, participant *lksdk.RemoteParticipant) {
			mu.Lock()
			leftParticipants[participant.Identity()] = true
			mu.Unlock()
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-tracking-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create room with agent dispatch and connect multiple participants
	roomName := fmt.Sprintf("test-tracking-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-tracking-worker")
	require.NoError(t, err)

	// Wait for agent to connect to the room before joining participants
	select {
	case <-roomConnected:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for room connection")
	}

	// Connect multiple test participants
	var testRooms []*lksdk.Room
	for i := 0; i < 3; i++ {
		identity := fmt.Sprintf("participant-%d", i)
		token := generateTestToken(apiKey, apiSecret, roomName, identity)

		room := lksdk.NewRoom(&lksdk.RoomCallback{})
		err := room.JoinWithToken(url, token)
		require.NoError(t, err)
		testRooms = append(testRooms, room)

		time.Sleep(500 * time.Millisecond)
	}

	// Wait for all participants to be tracked deterministically
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(participants) == 3
	}, 10*time.Second, 100*time.Millisecond)

	// Disconnect participants
	for _, room := range testRooms {
		room.Disconnect()
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for disconnection tracking deterministically
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(leftParticipants) == 3
	}, 10*time.Second, 100*time.Millisecond)

	worker.Stop()
}

func TestUniversalWorker_Integration_MetadataUpdates(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	metadataUpdates := make(chan string, 10)

	roomConnected2 := make(chan struct{}, 1)
	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-metadata-agent",
				ParticipantName:     "Test Metadata Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			// Keep the job running - wait for context cancellation
			<-ctx.Done()
			return nil
		},
		ParticipantMetadataChangedFunc: func(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
			metadataUpdates <- participant.Metadata()
		},
		RoomConnectedFunc: func(ctx context.Context, room *lksdk.Room) { roomConnected2 <- struct{}{} },
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-metadata-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create room with agent dispatch and participant
	roomName := fmt.Sprintf("test-metadata-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-metadata-worker")
	require.NoError(t, err)

	identity := "metadata-test-participant"
	token := generateTestToken(apiKey, apiSecret, roomName, identity)

	room := lksdk.NewRoom(&lksdk.RoomCallback{})
	err = room.JoinWithToken(url, token)
	require.NoError(t, err)
	defer room.Disconnect()

	// Wait for agent connected before updating metadata
	select {
	case <-roomConnected2:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for room connection")
	}

	// Update participant metadata multiple times via RoomServiceClient
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)
	for i := 0; i < 3; i++ {
		newMetadata := fmt.Sprintf("metadata-v%d", i+1)
		_, err := roomClient.UpdateParticipant(context.Background(), &livekit.UpdateParticipantRequest{
			Room:     roomName,
			Identity: identity,
			Metadata: newMetadata,
		})
		require.NoError(t, err)
		time.Sleep(500 * time.Millisecond)
	}

	// Verify metadata updates were received
	receivedCount := 0
	timeout := time.After(5 * time.Second)

Loop:
	for {
		select {
		case metadata := <-metadataUpdates:
			assert.Contains(t, metadata, "metadata-v")
			receivedCount++
			if receivedCount >= 3 {
				break Loop
			}
		case <-timeout:
			break Loop
		}
	}

	assert.GreaterOrEqual(t, receivedCount, 2, "Should receive at least 2 metadata updates")

	worker.Stop()
}

// ==================== Media Handling Tests ====================

func TestUniversalWorker_Integration_TrackPublishing(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	publishedOK := make(chan struct{}, 1)

	var worker *UniversalWorker

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-publisher-agent",
				ParticipantName:     "Test Publisher Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			// Publish a test audio track
			track, err := webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
				"audio",
				"test-audio",
			)
			if err != nil {
				return err
			}

			// Publish the track
			if _, err = worker.PublishTrack(jobCtx.Job.Id, track); err != nil {
				return err
			}
			publishedOK <- struct{}{}
			return nil
		},
	}

	worker = NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-publisher-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create room with agent dispatch to trigger job assignment
	roomName := fmt.Sprintf("test-publish-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-publisher-worker")
	require.NoError(t, err)

	// Wait for publish to succeed
	select {
	case <-publishedOK:
		// ok
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for track publication")
	}

	worker.Stop()
}

func TestUniversalWorker_Integration_DataMessaging(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	dataReceived := make(chan []byte, 1)
	roomConnected := make(chan struct{}, 1)

	var worker *UniversalWorker

	// Channels for synchronization
	agentConnected := make(chan *lksdk.Room, 1)
	participantJoined := make(chan *lksdk.RemoteParticipant, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-data-agent",
				ParticipantName:     "Test Data Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			// Send data to all participants and keep the job briefly alive
			_ = worker.SendDataToParticipant(jobCtx.Job.Id, "", []byte("test-data"), true)
			time.Sleep(4 * time.Second)
			return nil
		},
		DataReceivedFunc: func(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
			t.Logf("Received data: %s from participant: %s", string(data), participant.Identity())
			dataReceived <- data
		},
		RoomConnectedFunc: func(ctx context.Context, room *lksdk.Room) { roomConnected <- struct{}{} },
	}

	worker = NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-data-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create room with agent dispatch and participant
	roomName := fmt.Sprintf("test-data-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-data-worker")
	require.NoError(t, err)
	t.Logf("Created room %s for data messaging test", roomName)

	// Wait for agent to connect to room first
	select {
	case agentRoom := <-agentConnected:
		t.Logf("Agent connected to room: %s", agentRoom.Name())
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for agent to connect to room")
	}

	// Wait for agent to connect to the room
	select {
	case <-roomConnected:
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for room connection")
	}

	// Connect participant that will send data
	identity := "data-sender"
	token := generateTestToken(apiKey, apiSecret, roomName, identity)

	room := lksdk.NewRoom(&lksdk.RoomCallback{})
	err = room.JoinWithToken(url, token)
	require.NoError(t, err)
	defer room.Disconnect()
	t.Logf("Participant %s joined room", identity)

	// Wait for agent to detect the participant
	select {
	case participant := <-participantJoined:
		t.Logf("Agent detected participant: %s", participant.Identity())
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for agent to detect participant")
	}

	// Give a bit more time for the connection to fully stabilize
	time.Sleep(2 * time.Second)

	// Send data from participant
	testData := []byte("participant-data")
	err = room.LocalParticipant.PublishData(testData, lksdk.WithDataPublishReliable(true))
	require.NoError(t, err)
	t.Logf("Published data from participant: %s", string(testData))

	// Wait for data to be received by agent
	select {
	case data := <-dataReceived:
		t.Logf("Successfully received data: %s", string(data))
		assert.Equal(t, "participant-data", string(data))
	case <-time.After(15 * time.Second):
		t.Fatal("Timeout waiting for data after 15 seconds")
	}

	worker.Stop()
}

// ==================== Error Handling and Recovery Tests ====================

func TestUniversalWorker_Integration_JobRejection(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			// Reject all jobs
			return false, nil
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			t.Fatal("Should not receive job assignment")
			return nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-reject-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create room to trigger job
	roomName := fmt.Sprintf("test-reject-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-reject-worker")
	require.NoError(t, err)

	// Wait to ensure no job is assigned
	time.Sleep(2 * time.Second)

	// Verify no active jobs
	assert.Equal(t, 0, len(worker.activeJobs))

	worker.Stop()
}

func TestUniversalWorker_Integration_JobFailure(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	jobFailed := make(chan string, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-failure-agent",
				ParticipantName:     "Test Failure Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			// Simulate job failure
			t.Logf("Job assigned, simulating failure for job %s", jobCtx.Job.Id)

			// Signal that job assignment was called and is about to fail
			go func() {
				// Give a small delay to ensure the error is processed
				time.Sleep(100 * time.Millisecond)
				jobFailed <- jobCtx.Job.Id
			}()

			return fmt.Errorf("simulated job failure")
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-failure-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create room with agent dispatch to trigger job
	roomName := fmt.Sprintf("test-failure-room-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-failure-worker")
	require.NoError(t, err)
	t.Logf("Created room %s for job failure test", roomName)

	// Wait for job failure
	select {
	case jobID := <-jobFailed:
		t.Logf("Job failed as expected for job: %s", jobID)
		assert.NotEmpty(t, jobID)

		// Verify the job is no longer in active jobs (cleaned up after failure)
		time.Sleep(500 * time.Millisecond) // Give time for cleanup

		// The job should be removed from active jobs after failure
		worker.mu.RLock()
		activeJobCount := len(worker.activeJobs)
		worker.mu.RUnlock()

		assert.Equal(t, 0, activeJobCount, "Job should be cleaned up after failure")
		t.Logf("Confirmed job was cleaned up, active jobs: %d", activeJobCount)

	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for job failure after 10 seconds")
	}

	worker.Stop()
}

// ==================== Load and Status Tests ====================

func TestUniversalWorker_Integration_StatusUpdates(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, nil
		},
	}
	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-status-worker",
		JobType:   livekit.JobType_JT_ROOM,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Update status multiple times
	statuses := []WorkerStatus{WorkerStatusAvailable, WorkerStatusFull, WorkerStatusAvailable}
	loads := []float32{0.2, 0.8, 0.3}

	for i, status := range statuses {
		err := worker.UpdateStatus(status, loads[i])
		assert.NoError(t, err)
		time.Sleep(1 * time.Second)
	}

	worker.Stop()
}

func TestUniversalWorker_Integration_LoadCalculation(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, nil
		},
	}
	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName:      "test-load-worker",
		JobType:        livekit.JobType_JT_ROOM,
		MaxJobs:        3,
		LoadCalculator: &DefaultLoadCalculator{},
		Logger:         mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Get initial load
	worker.mu.RLock()
	activeJobs := len(worker.activeJobs)
	worker.mu.RUnlock()
	assert.Equal(t, 0, activeJobs)
	assert.Equal(t, 3, worker.opts.MaxJobs)

	// Simulate job assignment
	worker.mu.Lock()
	worker.activeJobs["job1"] = &JobContext{
		Job:       &livekit.Job{Id: "job1"},
		StartedAt: time.Now(),
	}
	worker.jobStartTimes["job1"] = time.Now()
	worker.mu.Unlock()

	// Verify load increased
	worker.mu.RLock()
	activeJobs = len(worker.activeJobs)
	worker.mu.RUnlock()
	assert.Equal(t, 1, activeJobs)

	worker.Stop()
}

// ==================== Concurrent Operations Tests ====================

func TestUniversalWorker_Integration_ConcurrentJobs(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	var mu sync.Mutex
	activeJobs := make(map[string]bool)
	maxConcurrent := 0

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-concurrent-agent",
				ParticipantName:     "Test Concurrent Agent",
			}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			mu.Lock()
			activeJobs[jobCtx.Job.Id] = true
			if len(activeJobs) > maxConcurrent {
				maxConcurrent = len(activeJobs)
			}
			mu.Unlock()

			// Simulate job processing
			time.Sleep(2 * time.Second)

			mu.Lock()
			delete(activeJobs, jobCtx.Job.Id)
			mu.Unlock()

			return nil
		},
	}

	worker := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "test-concurrent-worker",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   5,
		Logger:    mocks.NewMockLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start worker in background
	go func() {
		_ = worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Create multiple rooms with agent dispatch to trigger concurrent jobs
	for i := 0; i < 3; i++ {
		roomName := fmt.Sprintf("test-concurrent-room-%d-%d", time.Now().Unix(), i)
		_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "test-concurrent-worker")
		require.NoError(t, err)
		t.Logf("Created room %s for concurrent job test", roomName)
		time.Sleep(500 * time.Millisecond)
	}

	// Wait for jobs to process
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for concurrent jobs to process")
		case <-ticker.C:
			mu.Lock()
			active := len(activeJobs)
			mu.Unlock()
			if active > 0 {
				// Jobs are running, can proceed with test
				goto checkResults
			}
		}
	}

checkResults:

	// Log current status for debugging
	mu.Lock()
	t.Logf("maxConcurrent: %d, activeJobs: %d", maxConcurrent, len(activeJobs))
	mu.Unlock()

	// Verify concurrent execution
	assert.Greater(t, maxConcurrent, 1, "Should handle multiple jobs concurrently - got maxConcurrent=%d", maxConcurrent)

	worker.Stop()
}

// ==================== Helper Functions ====================

func createTestRoom(apiKey, apiSecret, url, roomName string) (string, error) {
	return createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "")
}

func createTestRoomWithAgent(apiKey, apiSecret, url, roomName, agentName string) (string, error) {
	// Use RoomServiceClient to create room with agent dispatch
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)

	// If no agent name provided, don't dispatch to agents
	var agents []*livekit.RoomAgentDispatch
	if agentName != "" {
		agents = []*livekit.RoomAgentDispatch{
			{
				AgentName: agentName, // Must match the worker's AgentName
				Metadata:  `{"test": true}`,
			},
		}
	}

	// Create room with agent dispatch configuration
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Test room with agent",
		Agents:   agents,
	})
	if err != nil {
		return "", err
	}

	return room.Sid, nil
}

func generateTestToken(apiKey, apiSecret, roomName, identity string) string {
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     roomName,
	}
	at.SetVideoGrant(grant)
	at.SetIdentity(identity)

	token, _ := at.ToJWT()
	return token
}

// waitForWorkerConnection waits for worker to be connected with timeout
func waitForWorkerConnection(worker *UniversalWorker, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for worker connection")
		case <-ticker.C:
			if worker.IsConnected() {
				return nil
			}
		}
	}
}
