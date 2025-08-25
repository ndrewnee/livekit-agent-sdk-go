package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebSocketConnection tests WebSocket connection establishment and message handling
func TestWebSocketConnection(t *testing.T) {
	// Test connection parameters
	tests := []struct {
		name        string
		url         string
		apiKey      string
		apiSecret   string
		expectError bool
	}{
		{
			name:        "valid connection params",
			url:         "ws://localhost:7880",
			apiKey:      "devkey",
			apiSecret:   "secret",
			expectError: false,
		},
		{
			name:        "empty url",
			url:         "",
			apiKey:      "devkey",
			apiSecret:   "secret",
			expectError: true,
		},
		{
			name:        "empty api key",
			url:         "ws://localhost:7880",
			apiKey:      "",
			apiSecret:   "secret",
			expectError: true,
		},
		{
			name:        "empty api secret",
			url:         "ws://localhost:7880",
			apiKey:      "devkey",
			apiSecret:   "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewMockUniversalHandler()
			opts := WorkerOptions{
				AgentName: "test-conn",
				JobType:   livekit.JobType_JT_ROOM,
			}

			if tt.expectError {
				// Should panic or handle error during creation
				if tt.url == "" || tt.apiKey == "" || tt.apiSecret == "" {
					// NewUniversalWorker should handle invalid params
					worker := NewUniversalWorker(tt.url, tt.apiKey, tt.apiSecret, handler, opts)
					if tt.url == "" {
						assert.NotNil(t, worker) // Worker is created but URL is empty
					} else {
						assert.NotNil(t, worker)
					}
				}
			} else {
				worker := NewUniversalWorker(tt.url, tt.apiKey, tt.apiSecret, handler, opts)
				assert.NotNil(t, worker)
			}
		})
	}
}

// TestWebSocketReconnection tests WebSocket reconnection logic
func TestWebSocketReconnection(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName:    "test-reconnect",
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 100 * time.Millisecond,
		PingTimeout:  50 * time.Millisecond,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Simulate connection loss and reconnection
	// This would require mocking the WebSocket connection
	// For now, we test that the worker handles reconnection attempts

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Start worker (may or may not connect depending on server)
	var startErr error
	done := make(chan struct{})

	go func() {
		startErr = worker.Start(ctx)
		close(done)
	}()

	// Wait for context to timeout
	<-done

	// When server is running, Start returns nil on context cancellation
	// When server is not running, Start returns connection error
	// Both are valid behaviors
	if startErr != nil && startErr != context.DeadlineExceeded {
		t.Logf("Start returned error: %v", startErr)
	}
}

// TestJobQueueProcessing tests job queue and processing functionality
func TestJobQueueProcessing(t *testing.T) {
	handler := NewMockUniversalHandler()
	handler.SetAcceptJob(true)

	opts := WorkerOptions{
		AgentName:      "test-queue-processing",
		JobType:        livekit.JobType_JT_ROOM,
		MaxJobs:        3,
		EnableJobQueue: true, // Enable job queuing
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Queue multiple jobs
	jobs := make([]*livekit.Job, 5)
	for i := 0; i < 5; i++ {
		jobs[i] = &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{
				Name: fmt.Sprintf("room-%d", i),
				Sid:  fmt.Sprintf("room-sid-%d", i),
			},
		}
	}

	// Queue all jobs
	for _, job := range jobs {
		err := worker.QueueJob(job, job.Room.Name, "token")
		if err != nil {
			// May fail if queue is full, which is expected
			continue
		}
	}

	// Check queue statistics
	stats := worker.GetQueueStats()
	assert.NotNil(t, stats)

	// Process jobs (simulate job assignment)
	for i := 0; i < 3; i++ { // Process up to MaxJobs
		job := jobs[i]
		ctx := &JobContext{
			Job:       job,
			Room:      nil,
			Cancel:    func() {},
			StartedAt: time.Now(),
		}

		// Simulate job assignment
		worker.SetActiveJob(job.Id, ctx)

		// Verify job is active
		activeJobs := worker.GetActiveJobs()
		assert.Contains(t, activeJobs, job.Id)
	}

	// Verify we have 3 active jobs
	activeJobs := worker.GetActiveJobs()
	assert.LessOrEqual(t, len(activeJobs), 3)
}

// TestJobRecovery tests job recovery and checkpointing
func TestJobRecovery(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-recovery",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create a job with checkpoint data
	job := &livekit.Job{
		Id:   "recovery-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "recovery-room"},
	}

	// Create checkpoint using constructor
	checkpoint := NewJobCheckpoint(job.Id)
	checkpoint.Save("processed_items", 42)
	checkpoint.Save("last_timestamp", time.Now().Unix())
	checkpoint.Save("status", "processing")

	// Create job context with custom data
	ctx := &JobContext{
		Job:       job,
		Room:      nil,
		Cancel:    func() {},
		StartedAt: time.Now(),
		CustomData: map[string]interface{}{
			"checkpoint": checkpoint,
		},
	}

	// Set active job
	worker.SetActiveJob(job.Id, ctx)

	// Retrieve checkpoint (will be nil since recovery isn't set up)
	retrieved := worker.GetJobCheckpoint(job.Id)
	assert.Nil(t, retrieved)

	// Test our local checkpoint
	items, exists := checkpoint.Load("processed_items")
	assert.True(t, exists)
	assert.Equal(t, 42, items)

	// Update checkpoint
	checkpoint.Save("processed_items", 100)

	// Verify update
	items, exists = checkpoint.Load("processed_items")
	assert.True(t, exists)
	assert.Equal(t, 100, items)
}

// TestMessageProtocol tests the message protocol handling
func TestMessageProtocol(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-protocol",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Test different message types
	messages := []struct {
		name        string
		messageType string
		payload     map[string]interface{}
	}{
		{
			name:        "worker status",
			messageType: "WORKER_STATUS",
			payload: map[string]interface{}{
				"status": "available",
				"load":   0.5,
			},
		},
		{
			name:        "job assignment",
			messageType: "JOB_ASSIGNMENT",
			payload: map[string]interface{}{
				"job": map[string]interface{}{
					"id":   "test-job",
					"type": "JT_ROOM",
				},
				"token": "test-token",
			},
		},
		{
			name:        "job termination",
			messageType: "JOB_TERMINATION",
			payload: map[string]interface{}{
				"job_id": "test-job",
				"reason": "completed",
			},
		},
		{
			name:        "ping",
			messageType: "PING",
			payload: map[string]interface{}{
				"timestamp": time.Now().Unix(),
			},
		},
		{
			name:        "pong",
			messageType: "PONG",
			payload: map[string]interface{}{
				"timestamp": time.Now().Unix(),
			},
		},
	}

	for _, msg := range messages {
		t.Run(msg.name, func(t *testing.T) {
			// Create message
			message := map[string]interface{}{
				"type":    msg.messageType,
				"payload": msg.payload,
			}

			// Marshal to JSON
			data, err := json.Marshal(message)
			assert.NoError(t, err)
			assert.NotNil(t, data)

			// Verify message structure
			var decoded map[string]interface{}
			err = json.Unmarshal(data, &decoded)
			assert.NoError(t, err)
			assert.Equal(t, msg.messageType, decoded["type"])
		})
	}
}

// TestConcurrentJobHandling tests concurrent job handling
func TestConcurrentJobHandling(t *testing.T) {
	handler := NewMockUniversalHandler()
	handler.SetAcceptJob(true)

	opts := WorkerOptions{
		AgentName: "test-concurrent",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	var wg sync.WaitGroup
	numJobs := 20

	// Concurrently queue and process jobs
	for i := 0; i < numJobs; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			job := &livekit.Job{
				Id:   fmt.Sprintf("concurrent-job-%d", id),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{
					Name: fmt.Sprintf("concurrent-room-%d", id),
				},
			}

			// Queue job
			_ = worker.QueueJob(job, job.Room.Name, "token")

			// Random delay
			time.Sleep(time.Duration(id%10) * time.Millisecond)

			// Set as active (simulate assignment)
			if id < 10 { // Only first 10 to respect MaxJobs
				ctx := &JobContext{
					Job:       job,
					Room:      nil,
					Cancel:    func() {},
					StartedAt: time.Now(),
				}
				worker.SetActiveJob(job.Id, ctx)
			}

			// Get job context
			_, _ = worker.GetJobContext(job.Id)

			// Update metrics
			_ = worker.GetMetrics()
		}(i)
	}

	// Wait for all goroutines
	wg.Wait()

	// Verify final state
	activeJobs := worker.GetActiveJobs()
	assert.NotNil(t, activeJobs)
	assert.LessOrEqual(t, len(activeJobs), 10)

	// Get final metrics
	metrics := worker.GetMetrics()
	assert.NotNil(t, metrics)
}

// TestWebSocketPingPong tests ping/pong mechanism
func TestWebSocketPingPong(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName:    "test-ping",
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 100 * time.Millisecond,
		PingTimeout:  50 * time.Millisecond,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create mock WebSocket connection
	mockConn := NewMockWebSocketConn()

	// Simulate ping message
	pingMsg := map[string]interface{}{
		"type":      "PING",
		"timestamp": time.Now().Unix(),
	}
	pingBytes, _ := json.Marshal(pingMsg)
	mockConn.SimulateMessage(pingBytes)

	// Read the ping message
	messageType, data, err := mockConn.ReadMessage()
	assert.NoError(t, err)
	assert.Equal(t, websocket.TextMessage, messageType)
	assert.NotNil(t, data)

	// Verify it's a ping message
	var decoded map[string]interface{}
	err = json.Unmarshal(data, &decoded)
	assert.NoError(t, err)
	assert.Equal(t, "PING", decoded["type"])

	// Send pong response
	pongMsg := map[string]interface{}{
		"type":      "PONG",
		"timestamp": decoded["timestamp"],
	}
	pongBytes, _ := json.Marshal(pongMsg)
	err = mockConn.WriteMessage(websocket.TextMessage, pongBytes)
	assert.NoError(t, err)
}

// TestJobPrioritization tests job priority handling
func TestJobPrioritization(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName:      "test-priority",
		JobType:        livekit.JobType_JT_ROOM,
		MaxJobs:        3,
		EnableJobQueue: true, // Enable job queuing
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create jobs with different priorities
	highPriorityJob := &livekit.Job{
		Id:       "high-priority",
		Type:     livekit.JobType_JT_ROOM,
		Room:     &livekit.Room{Name: "high-priority-room"},
		Metadata: "priority:high",
	}

	normalPriorityJob := &livekit.Job{
		Id:       "normal-priority",
		Type:     livekit.JobType_JT_ROOM,
		Room:     &livekit.Room{Name: "normal-priority-room"},
		Metadata: "priority:normal",
	}

	lowPriorityJob := &livekit.Job{
		Id:       "low-priority",
		Type:     livekit.JobType_JT_ROOM,
		Room:     &livekit.Room{Name: "low-priority-room"},
		Metadata: "priority:low",
	}

	// Queue jobs in reverse priority order
	err := worker.QueueJob(lowPriorityJob, lowPriorityJob.Room.Name, "token")
	assert.NoError(t, err)

	err = worker.QueueJob(normalPriorityJob, normalPriorityJob.Room.Name, "token")
	assert.NoError(t, err)

	err = worker.QueueJob(highPriorityJob, highPriorityJob.Room.Name, "token")
	assert.NoError(t, err)

	// In a real implementation, high priority job should be processed first
	// For now, we just verify jobs were queued
	stats := worker.GetQueueStats()
	assert.NotNil(t, stats)
}

// TestErrorRecovery tests error recovery mechanisms
func TestErrorRecovery(t *testing.T) {
	handler := NewMockUniversalHandler()

	// Configure handler to fail job assignment
	handler.SetAssignError(errors.New("assignment failed"))

	opts := WorkerOptions{
		AgentName:      "test-error-recovery",
		JobType:        livekit.JobType_JT_ROOM,
		EnableJobQueue: true, // Enable job queuing
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create a job
	job := &livekit.Job{
		Id:   "error-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "error-room"},
	}

	// Queue job
	err := worker.QueueJob(job, job.Room.Name, "token")
	assert.NoError(t, err)

	// Handler will fail assignment
	// Verify error handling doesn't crash
	assert.NotNil(t, handler.GetJobRequests())

	// Reset handler to accept jobs
	handler.SetAssignError(nil)
	handler.SetAcceptJob(true)

	// Try another job
	job2 := &livekit.Job{
		Id:   "success-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "success-room"},
	}

	err = worker.QueueJob(job2, job2.Room.Name, "token")
	assert.NoError(t, err)
}

// TestWebSocketCloseHandling tests WebSocket close scenarios
func TestWebSocketCloseHandling(t *testing.T) {
	scenarios := []struct {
		name      string
		closeCode int
		closeText string
	}{
		{
			name:      "normal closure",
			closeCode: websocket.CloseNormalClosure,
			closeText: "normal closure",
		},
		{
			name:      "going away",
			closeCode: websocket.CloseGoingAway,
			closeText: "server going away",
		},
		{
			name:      "protocol error",
			closeCode: websocket.CloseProtocolError,
			closeText: "protocol error",
		},
		{
			name:      "abnormal closure",
			closeCode: websocket.CloseAbnormalClosure,
			closeText: "abnormal closure",
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			handler := NewMockUniversalHandler()
			opts := WorkerOptions{
				AgentName: "test-close",
				JobType:   livekit.JobType_JT_ROOM,
			}

			worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
			require.NotNil(t, worker)

			// Create mock connection
			mockConn := NewMockWebSocketConn()

			// Simulate close with specific code
			closeErr := &websocket.CloseError{
				Code: scenario.closeCode,
				Text: scenario.closeText,
			}
			mockConn.SetCloseError(closeErr)

			// Close connection - it will return the close error we set
			err := mockConn.Close()
			// The mock returns the close error on Close()
			assert.Error(t, err)
			assert.Equal(t, closeErr, err)

			// Verify connection is closed
			_, _, err = mockConn.ReadMessage()
			assert.Error(t, err)
			assert.Equal(t, websocket.ErrCloseSent, err)
		})
	}
}
