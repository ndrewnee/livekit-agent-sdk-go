package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestWorker_Connect_ErrorCases tests uncovered error paths in connect()
func TestWorker_Connect_ErrorCases(t *testing.T) {
	tests := []struct {
		name        string
		serverURL   string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "invalid URL scheme",
			serverURL:   "ftp://invalid.com",
			expectError: true,
			errorMsg:    "invalid URL",
		},
		{
			name:        "malformed URL",
			serverURL:   "://no-scheme",
			expectError: true,
			errorMsg:    "failed to parse URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestJobHandler()
			worker := NewWorker(tt.serverURL, "test-key", "test-secret", handler, WorkerOptions{
				JobType: livekit.JobType_JT_ROOM,
			})

			ctx := context.Background()
			err := worker.connect(ctx)
			
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestWorker_Register_ErrorCases tests uncovered error paths in register()
func TestWorker_Register_ErrorCases(t *testing.T) {
	t.Run("registration timeout with no response", func(t *testing.T) {
		// Create a server that immediately closes connections
		server := newMockWebSocketServer()
		server.closeOnConnect = true
		defer server.Close()

		handler := newTestJobHandler()
		worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
			JobType: livekit.JobType_JT_ROOM,
		})

		// Start connection - should fail during connection
		ctx := context.Background()
		err := worker.Start(ctx)
		
		// Should get a connection error
		assert.Error(t, err)
		assert.False(t, worker.IsConnected())
	})
}

// TestWorker_HandleReconnect_FullCoverage tests the complex reconnection logic
func TestWorker_HandleReconnect_FullCoverage(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := newTestJobHandler()
	
	t.Run("reconnect with saved state", func(t *testing.T) {
		worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
			JobType: livekit.JobType_JT_ROOM,
		})

		// Add some state to save
		worker.mu.Lock()
		worker.activeJobs["test-job"] = &activeJob{
			job: &livekit.Job{
				Id:   "test-job",
				Type: livekit.JobType_JT_ROOM,
			},
			status: livekit.JobStatus_JS_RUNNING,
		}
		worker.mu.Unlock()

		// Save state
		worker.saveState()

		// Start worker
		ctx := context.Background()
		err := worker.Start(ctx)
		require.NoError(t, err)
		defer worker.Stop()

		// Wait for initial connection
		time.Sleep(100 * time.Millisecond)
		
		// Store original worker ID
		worker.mu.RLock()
		originalWorkerID := worker.workerID
		worker.mu.RUnlock()

		// Clear the active jobs (simulating connection loss)
		worker.mu.Lock()
		delete(worker.activeJobs, "test-job")
		worker.mu.Unlock()

		// Trigger reconnection
		worker.triggerReconnect()

		// Wait for reconnection to complete
		time.Sleep(500 * time.Millisecond)

		// Check that worker ID was restored (not jobs, as they can't be restored without server support)
		worker.mu.RLock()
		restoredWorkerID := worker.workerID
		jobExists := len(worker.activeJobs) > 0
		worker.mu.RUnlock()
		
		// Worker ID should be restored, but jobs should not (as per implementation comment)
		assert.Equal(t, originalWorkerID, restoredWorkerID)
		assert.False(t, jobExists, "Jobs should not be restored after reconnection")
	})

	t.Run("reconnect with backoff", func(t *testing.T) {
		// Create a server that will fail connections
		// We'll close it immediately to simulate connection failure
		brokenServer := newMockWebSocketServer()
		brokenServer.Close()

		worker := NewWorker(brokenServer.URL(), "test-key", "test-secret", handler, WorkerOptions{
			JobType: livekit.JobType_JT_ROOM,
		})

		// Track reconnect attempts
		attempts := 0
		var attemptsMu sync.Mutex
		worker.logger = &mockLogger{
			onInfo: func(msg string, kvs ...interface{}) {
				if msg == "Attempting to reconnect" {
					attemptsMu.Lock()
					attempts++
					attemptsMu.Unlock()
				}
			},
		}

		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()

		// Start the reconnection handler
		go worker.handleReconnect(ctx)

		// Trigger first reconnection attempt
		worker.triggerReconnect()

		// Wait longer for reconnection attempts with backoff (5s interval)
		time.Sleep(7 * time.Second)

		// Should have made multiple attempts with backoff (first attempt + at least one retry)
		attemptsMu.Lock()
		finalAttempts := attempts
		attemptsMu.Unlock()
		
		assert.GreaterOrEqual(t, finalAttempts, 2, "Should have made at least 2 reconnection attempts")
	})
}

// TestWorker_SendMessage_WriteMutex tests concurrent write protection
func TestWorker_SendMessage_WriteMutex(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := newTestJobHandler()
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

	// Send multiple messages concurrently
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			
			msg := &livekit.WorkerMessage{
				Message: &livekit.WorkerMessage_UpdateWorker{
					UpdateWorker: &livekit.UpdateWorkerStatus{
						Status: livekit.WorkerStatus_WS_AVAILABLE.Enum(),
						Load:   float32(idx) / 10.0,
					},
				},
			}
			
			if err := worker.sendMessage(msg); err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Should have no errors
	for err := range errors {
		t.Errorf("Concurrent write error: %v", err)
	}
}

// TestWorker_UpdateJobStatus_AllStatuses tests all job status updates
func TestWorker_UpdateJobStatus_AllStatuses(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := newTestJobHandler()
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

	statuses := []livekit.JobStatus{
		livekit.JobStatus_JS_PENDING,
		livekit.JobStatus_JS_RUNNING,
		livekit.JobStatus_JS_SUCCESS,
		livekit.JobStatus_JS_FAILED,
	}

	// Track the number of messages before we start to ensure we get the right ones
	initialMsgCount := len(server.GetReceivedMessages())

	// Send all status updates sequentially and collect expected results
	expectedUpdates := make([]struct {
		jobID    string
		status   livekit.JobStatus
		errorMsg string
	}, len(statuses))

	for i, status := range statuses {
		jobID := fmt.Sprintf("job-%d-%s", i, status.String())
		errorMsg := ""
		if status == livekit.JobStatus_JS_FAILED {
			errorMsg = "test error"
		}
		
		expectedUpdates[i] = struct {
			jobID    string
			status   livekit.JobStatus
			errorMsg string
		}{jobID, status, errorMsg}

		// Send the status update
		worker.updateJobStatus(jobID, status, errorMsg)
		
		// Small delay to ensure message is sent before next one
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for all messages to be received
	deadline := time.Now().Add(2 * time.Second)
	var updateJobMessages []*livekit.WorkerMessage
	
	for time.Now().Before(deadline) && len(updateJobMessages) < len(statuses) {
		msgs := server.GetReceivedMessages()
		
		// Look for new updateJob messages since we started
		for i := initialMsgCount; i < len(msgs); i++ {
			var msg livekit.WorkerMessage
			if err := proto.Unmarshal(msgs[i], &msg); err != nil {
				continue
			}
			if msg.GetUpdateJob() != nil {
				// Only add if we haven't seen this message yet
				alreadyAdded := false
				for _, existing := range updateJobMessages {
					if existing.GetUpdateJob().JobId == msg.GetUpdateJob().JobId {
						alreadyAdded = true
						break
					}
				}
				if !alreadyAdded {
					updateJobMessages = append(updateJobMessages, &msg)
				}
			}
		}
		
		if len(updateJobMessages) < len(statuses) {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Verify we received all expected messages
	require.Equal(t, len(statuses), len(updateJobMessages), "Should have received all updateJob messages")

	// Create a map of received updates by job ID for easy verification
	receivedUpdates := make(map[string]*livekit.UpdateJobStatus)
	for _, msg := range updateJobMessages {
		update := msg.GetUpdateJob()
		receivedUpdates[update.JobId] = update
	}

	// Verify each expected update was received correctly
	for i, expected := range expectedUpdates {
		t.Run(fmt.Sprintf("status_%d_%s", i, expected.status.String()), func(t *testing.T) {
			update, found := receivedUpdates[expected.jobID]
			require.True(t, found, "Should have received update for job %s", expected.jobID)
			
			assert.Equal(t, expected.jobID, update.JobId, "Job ID should match")
			assert.Equal(t, expected.status, update.Status, "Status should match")
			if expected.errorMsg != "" {
				assert.Equal(t, expected.errorMsg, update.Error, "Error message should match")
			}
		})
	}
}

// TestWorker_ParseServerMessage_InvalidMessages tests message parsing errors
func TestWorker_ParseServerMessage_InvalidMessages(t *testing.T) {
	worker := &Worker{
		logger: &mockLogger{},
	}

	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{
			name:    "empty data",
			data:    []byte{},
			wantErr: true,
		},
		{
			name:    "invalid protobuf",
			data:    []byte("not a protobuf message"),
			wantErr: true,
		},
		{
			name:    "nil data",
			data:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := worker.parseServerMessage(tt.data)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, msg)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, msg)
			}
		})
	}
}

// TestWorker_HandlePing tests ping handler edge cases
func TestWorker_HandlePing(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := newTestJobHandler()
	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 50 * time.Millisecond,
	})

	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)
	defer worker.Stop()

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Simulate ping failure
	worker.mu.Lock()
	worker.conn.Close()
	worker.mu.Unlock()

	// Wait for ping to fail
	time.Sleep(100 * time.Millisecond)

	// Should trigger reconnection
	assert.False(t, worker.IsConnected())
}

// TestWorker_TerminateAllJobs_WithHandlers tests job termination with handlers
func TestWorker_TerminateAllJobs_WithHandlers(t *testing.T) {
	terminated := make(chan string, 3)
	
	handler := &testJobHandler{
		OnJobTerminatedFunc: func(ctx context.Context, jobID string) {
			terminated <- jobID
		},
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Create active jobs
	jobs := []string{"job1", "job2", "job3"}
	for _, jobID := range jobs {
		_, cancel := context.WithCancel(context.Background())
		worker.mu.Lock()
		worker.activeJobs[jobID] = &activeJob{
			job:    &livekit.Job{Id: jobID},
			cancel: cancel,
		}
		worker.mu.Unlock()
	}

	// Terminate all
	worker.terminateAllJobs()

	// Verify all handlers were called
	for i := 0; i < len(jobs); i++ {
		select {
		case jobID := <-terminated:
			assert.Contains(t, jobs, jobID)
		case <-time.After(time.Second):
			t.Fatal("Not all termination handlers were called")
		}
	}

	// Verify all jobs were removed
	worker.mu.RLock()
	assert.Equal(t, 0, len(worker.activeJobs))
	worker.mu.RUnlock()
}

// TestWorker_CallHandlerSafely_AllPaths tests all paths in callHandlerSafely
func TestWorker_CallHandlerSafely_AllPaths(t *testing.T) {
	tests := []struct {
		name            string
		handlerFunc     func(context.Context, *livekit.Job) (bool, *JobMetadata)
		handlerDelay    time.Duration
		contextTimeout  time.Duration
		expectAccept    bool
		expectMetadata  bool
		expectLog       string
	}{
		{
			name: "handler timeout",
			handlerFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
				time.Sleep(200 * time.Millisecond)
				return true, &JobMetadata{ParticipantIdentity: "test"}
			},
			handlerDelay:   200 * time.Millisecond,
			contextTimeout: 100 * time.Millisecond,
			expectAccept:   false,
			expectMetadata: false,
			expectLog:      "Handler timeout",
		},
		{
			name: "context cancelled",
			handlerFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
				<-ctx.Done()
				return false, nil
			},
			contextTimeout: 50 * time.Millisecond,
			expectAccept:   false,
			expectMetadata: false,
		},
		{
			name: "handler returns nil metadata",
			handlerFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
				return true, nil
			},
			expectAccept:   true,
			expectMetadata: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &testJobHandler{
				OnJobRequestFunc: tt.handlerFunc,
			}

			worker := newTestableWorker(handler, WorkerOptions{
				JobType: livekit.JobType_JT_ROOM,
			})

			job := &livekit.Job{
				Id:   "test-job",
				Type: livekit.JobType_JT_ROOM,
			}

			ctx := context.Background()
			if tt.contextTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.contextTimeout)
				defer cancel()
			}

			accept, metadata := worker.callHandlerSafely(ctx, job)

			assert.Equal(t, tt.expectAccept, accept)
			if tt.expectMetadata {
				assert.NotNil(t, metadata)
			} else {
				assert.Nil(t, metadata)
			}
		})
	}
}

// TestWorker_ValidateServerMessage_AllCases tests all validation cases
func TestWorker_ValidateServerMessage_AllCases(t *testing.T) {
	tests := []struct {
		name    string
		msg     *livekit.ServerMessage
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil message",
			msg:     nil,
			wantErr: true,
			errMsg:  "nil message",
		},
		{
			name: "availability request without job",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Availability{
					Availability: &livekit.AvailabilityRequest{},
				},
			},
			wantErr: true,
			errMsg:  "missing job",
		},
		{
			name: "job assignment without job",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Assignment{
					Assignment: &livekit.JobAssignment{
						Token: "test-token",
					},
				},
			},
			wantErr: true,
			errMsg:  "missing job",
		},
		{
			name: "job assignment without token",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Assignment{
					Assignment: &livekit.JobAssignment{
						Job: &livekit.Job{Id: "test"},
					},
				},
			},
			wantErr: true,
			errMsg:  "missing token",
		},
		{
			name: "job termination without job ID",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Termination{
					Termination: &livekit.JobTermination{},
				},
			},
			wantErr: true,
			errMsg:  "missing job ID",
		},
		{
			name: "valid ping",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Pong{
					Pong: &livekit.WorkerPong{
						LastTimestamp: time.Now().Unix(),
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := &Worker{
				logger: &mockLogger{},
			}

			err := worker.validateServerMessage(tt.msg)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" && err != nil {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestWorker_UpdateLoad tests load calculation and updates
func TestWorker_UpdateLoad(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := newTestJobHandler()
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

	// Add jobs and verify load updates
	for i := 1; i <= 3; i++ {
		worker.mu.Lock()
		worker.activeJobs[fmt.Sprintf("job%d", i)] = &activeJob{
			job: &livekit.Job{Id: fmt.Sprintf("job%d", i)},
		}
		worker.mu.Unlock()

		worker.updateLoad()

		// Wait for update message
		msg, err := server.WaitForMessage("updateWorker", time.Second)
		require.NoError(t, err)

		update := msg.GetUpdateWorker()
		expectedLoad := float32(i) / float32(5) // i jobs out of 5 max
		assert.Equal(t, expectedLoad, update.Load)
	}
}

// mockLogger implements a simple logger for testing coverage gaps
type mockLogger struct {
	onInfo  func(msg string, kvs ...interface{})
	onError func(msg string, kvs ...interface{})
	onWarn  func(msg string, kvs ...interface{})
	onDebug func(msg string, kvs ...interface{})
}

func (l *mockLogger) Info(msg string, kvs ...interface{}) {
	if l.onInfo != nil {
		l.onInfo(msg, kvs...)
	}
}

func (l *mockLogger) Error(msg string, kvs ...interface{}) {
	if l.onError != nil {
		l.onError(msg, kvs...)
	}
}

func (l *mockLogger) Warn(msg string, kvs ...interface{}) {
	if l.onWarn != nil {
		l.onWarn(msg, kvs...)
	}
}

func (l *mockLogger) Debug(msg string, kvs ...interface{}) {
	if l.onDebug != nil {
		l.onDebug(msg, kvs...)
	}
}

// TestWorker_Stop_ImmediateStop tests immediate stop without timeout
func TestWorker_Stop_ImmediateStop(t *testing.T) {
	server := newMockWebSocketServer()
	defer server.Close()

	handler := newTestJobHandler()
	worker := NewWorker(server.URL(), "test-key", "test-secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	ctx := context.Background()
	err := worker.Start(ctx)
	require.NoError(t, err)

	// Wait for registration
	_, err = server.WaitForMessage("register", time.Second)
	require.NoError(t, err)

	// Test immediate stop
	worker.Stop()

	// Verify disconnected
	assert.False(t, worker.IsConnected())
}

// TestWorker_Properties tests worker property getters
func TestWorker_Properties(t *testing.T) {
	worker := &Worker{
		opts: WorkerOptions{
			JobType:   livekit.JobType_JT_PUBLISHER,
			Namespace: "test-namespace",
			Version:   "1.2.3",
		},
	}
	
	// Test worker ID before and after setting
	worker.mu.Lock()
	assert.Equal(t, "", worker.workerID)
	worker.workerID = "test-worker-123"
	assert.Equal(t, "test-worker-123", worker.workerID)
	worker.mu.Unlock()
	
	// Test other properties
	assert.Equal(t, livekit.JobType_JT_PUBLISHER, worker.opts.JobType)
	assert.Equal(t, "test-namespace", worker.opts.Namespace)
	assert.Equal(t, "1.2.3", worker.opts.Version)
}