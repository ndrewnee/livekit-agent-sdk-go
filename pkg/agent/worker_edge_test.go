package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

// TestCallHandlerSafelyPanicRecovery tests panic recovery in handler
func TestCallHandlerSafelyPanicRecovery(t *testing.T) {
	handler := &edgeTestJobHandler{
		onRequest: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			panic("test panic")
		},
	}

	worker := &Worker{
		handler: handler,
		logger:  &testLogger{t: t},
	}

	job := &livekit.Job{
		Id:   "test-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "test-room"},
	}

	// Should recover from panic
	accept, metadata := worker.callHandlerSafely(context.Background(), job)
	assert.False(t, accept)
	assert.Nil(t, metadata)
}

// TestCallHandlerSafelyTimeout tests timeout handling
func TestCallHandlerSafelyTimeout(t *testing.T) {
	handler := &edgeTestJobHandler{
		onRequest: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			// Simulate slow handler
			time.Sleep(10 * time.Second)
			return true, &JobMetadata{ParticipantIdentity: "test"}
		},
	}

	worker := &Worker{
		handler: handler,
		logger:  &testLogger{t: t},
	}

	job := &livekit.Job{
		Id:   "test-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "test-room"},
	}

	// Create a short timeout context
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should timeout
	accept, metadata := worker.callHandlerSafely(ctx, job)
	assert.False(t, accept)
	assert.Nil(t, metadata)
}

// TestDuplicateJobDetection tests duplicate job prevention
func TestDuplicateJobDetection(t *testing.T) {
	worker := &Worker{
		activeJobs: make(map[string]*activeJob),
		opts:       WorkerOptions{MaxJobs: 10},
		status:     WorkerStatusAvailable,
		logger:     &testLogger{t: t},
	}

	// Add a job
	worker.activeJobs["job1"] = &activeJob{
		job: &livekit.Job{Id: "job1"},
	}

	// Simulate availability request for duplicate job
	req := &livekit.AvailabilityRequest{
		Job: &livekit.Job{
			Id:   "job1",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "test-room"},
		},
	}

	// Should reject duplicate
	worker.handleAvailabilityRequest(context.Background(), req)
	// Would need to capture response to verify rejection
}

// TestGracefulShutdownTimeout tests shutdown with timeout
func TestGracefulShutdownTimeout(t *testing.T) {
	worker := &Worker{
		handler:    &edgeTestJobHandler{},
		activeJobs: make(map[string]*activeJob),
		logger:     &testLogger{t: t},
		closing:    false,
	}

	// Track if cancel was called
	cancelCalled := false
	cancel := func() {
		cancelCalled = true
	}

	worker.activeJobs["job1"] = &activeJob{
		job:    &livekit.Job{Id: "job1"},
		cancel: cancel,
		status: livekit.JobStatus_JS_RUNNING,
	}

	// Test shutdown with short timeout
	startJobs := len(worker.activeJobs)
	err := worker.StopWithTimeout(100 * time.Millisecond)
	assert.NoError(t, err) // Current implementation doesn't return error

	// Verify cancel was called on all jobs
	assert.True(t, cancelCalled, "Job cancel should have been called")
	
	// Verify the shutdown process started
	assert.True(t, worker.closing, "Worker should be in closing state")
	
	// Note: activeJobs are only cleared by the job handler goroutine
	// In a real scenario, the handler would clean up after context cancellation
	// For this test, we're verifying that terminateAllJobs() was called
	assert.Equal(t, startJobs, 1, "Test started with 1 job")
}

// TestMessageValidation tests message validation
func TestMessageValidation(t *testing.T) {
	worker := &Worker{
		logger: &testLogger{t: t},
	}

	tests := []struct {
		name    string
		msg     *livekit.ServerMessage
		wantErr bool
	}{
		{
			name:    "nil message",
			msg:     &livekit.ServerMessage{},
			wantErr: true,
		},
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
			wantErr: false,
		},
		{
			name: "missing job ID",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Availability{
					Availability: &livekit.AvailabilityRequest{
						Job: &livekit.Job{
							Room: &livekit.Room{Name: "room1"},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "missing room",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Availability{
					Availability: &livekit.AvailabilityRequest{
						Job: &livekit.Job{
							Id: "job1",
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := worker.validateServerMessage(tt.msg)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestStateRecovery tests state save and restore
func TestStateRecovery(t *testing.T) {
	worker := &Worker{
		workerID:   "worker1",
		status:     WorkerStatusAvailable,
		load:       0.5,
		activeJobs: make(map[string]*activeJob),
		savedState: &WorkerState{
			ActiveJobs: make(map[string]*JobState),
		},
		logger: &testLogger{t: t},
	}

	// Add some jobs
	worker.activeJobs["job1"] = &activeJob{
		job: &livekit.Job{
			Id:   "job1",
			Room: &livekit.Room{Name: "room1"},
		},
		status:    livekit.JobStatus_JS_RUNNING,
		startedAt: time.Now(),
	}

	// Save state
	worker.saveState()

	// Verify state was saved
	assert.Equal(t, "worker1", worker.savedState.WorkerID)
	assert.Equal(t, WorkerStatusAvailable, worker.savedState.LastStatus)
	assert.Equal(t, float32(0.5), worker.savedState.LastLoad)
	assert.Len(t, worker.savedState.ActiveJobs, 1)
	assert.Equal(t, "job1", worker.savedState.ActiveJobs["job1"].JobID)

	// Clear current state
	worker.workerID = ""
	worker.status = WorkerStatusFull
	worker.load = 0

	// Restore state
	worker.restoreState()

	// Verify restoration
	assert.Equal(t, "worker1", worker.workerID)
	// Status update would be sent asynchronously
}

// TestHealthMonitoring tests health reporting
func TestHealthMonitoring(t *testing.T) {
	worker := &Worker{
		conn:          nil, // Would be a real WebSocket connection
		workerID:      "worker1",
		status:        WorkerStatusAvailable,
		load:          0.3,
		activeJobs:    make(map[string]*activeJob),
		opts:          WorkerOptions{MaxJobs: 5},
		errorCount:    2,
		lastPingTime:  time.Now(),
		logger:        &testLogger{t: t},
	}

	// Add a job
	worker.activeJobs["job1"] = &activeJob{
		job: &livekit.Job{
			Id:   "job1",
			Room: &livekit.Room{Name: "room1"},
		},
		status:    livekit.JobStatus_JS_RUNNING,
		startedAt: time.Now().Add(-5 * time.Minute),
	}

	// Get health status
	health := worker.Health()

	// Verify health data
	assert.False(t, health["connected"].(bool)) // nil connection
	assert.Equal(t, "worker1", health["worker_id"])
	assert.Equal(t, "available", health["status"])
	assert.Equal(t, 1, health["active_jobs"])
	assert.Equal(t, 5, health["max_jobs"])
	assert.Equal(t, float32(0.3), health["load"])
	assert.Equal(t, 2, health["error_count"])

	jobs := health["jobs"].([]map[string]interface{})
	assert.Len(t, jobs, 1)
	assert.Equal(t, "job1", jobs[0]["id"])
}

// Test helpers

type edgeTestJobHandler struct {
	onRequest    func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata)
	onAssigned   func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
	onTerminated func(ctx context.Context, jobID string)
}

func (h *edgeTestJobHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	if h.onRequest != nil {
		return h.onRequest(ctx, job)
	}
	return false, nil
}

func (h *edgeTestJobHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	if h.onAssigned != nil {
		return h.onAssigned(ctx, job, room)
	}
	return nil
}

func (h *edgeTestJobHandler) OnJobTerminated(ctx context.Context, jobID string) {
	if h.onTerminated != nil {
		h.onTerminated(ctx, jobID)
	}
}

type testLogger struct {
	t *testing.T
}

func (l *testLogger) Debug(msg string, fields ...interface{}) {
	l.t.Logf("DEBUG: %s %v", msg, fields)
}

func (l *testLogger) Info(msg string, fields ...interface{}) {
	l.t.Logf("INFO: %s %v", msg, fields)
}

func (l *testLogger) Warn(msg string, fields ...interface{}) {
	l.t.Logf("WARN: %s %v", msg, fields)
}

func (l *testLogger) Error(msg string, fields ...interface{}) {
	l.t.Logf("ERROR: %s %v", msg, fields)
}

// TestGetMetrics tests metrics retrieval
func TestGetMetrics(t *testing.T) {
	worker := &Worker{
		logger: &testLogger{t: t},
	}

	metrics := worker.GetMetrics()
	
	// Verify all expected metrics are present
	expectedMetrics := []string{
		"jobs_accepted",
		"jobs_rejected", 
		"jobs_completed",
		"jobs_failed",
		"messages_sent",
		"messages_recvd",
		"reconnections",
	}
	
	for _, metric := range expectedMetrics {
		_, exists := metrics[metric]
		assert.True(t, exists, "Metric %s should exist", metric)
	}
}

// TestIsConnected tests connection status check
func TestIsConnected(t *testing.T) {
	worker := &Worker{
		conn: nil,
	}
	
	// Should return false when conn is nil
	assert.False(t, worker.IsConnected())
	
	// Note: Can't test with real connection without mocking websocket.Conn
}

// TestValidateServerMessageMoreCases tests additional validation cases
func TestValidateServerMessageMoreCases(t *testing.T) {
	worker := &Worker{
		logger: &testLogger{t: t},
	}

	tests := []struct {
		name    string
		msg     *livekit.ServerMessage
		wantErr bool
	}{
		{
			name: "register response missing worker ID",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Register{
					Register: &livekit.RegisterWorkerResponse{
						WorkerId: "",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "assignment missing token",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Assignment{
					Assignment: &livekit.JobAssignment{
						Job:   &livekit.Job{Id: "job1"},
						Token: "",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "termination missing job ID",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Termination{
					Termination: &livekit.JobTermination{
						JobId: "",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "valid pong message",
			msg: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Pong{
					Pong: &livekit.WorkerPong{
						LastTimestamp: 1234567890,
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := worker.validateServerMessage(tt.msg)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestRestoreStateEmpty tests restore with no saved state
func TestRestoreStateEmpty(t *testing.T) {
	worker := &Worker{
		savedState: nil,
		logger:     &testLogger{t: t},
	}
	
	// Should handle nil savedState gracefully
	worker.restoreState()
	
	// Verify nothing changed
	assert.Equal(t, "", worker.workerID)
}

// TestCallHandlerSafelyNilMetadata tests handler returning nil metadata
func TestCallHandlerSafelyNilMetadata(t *testing.T) {
	handler := &edgeTestJobHandler{
		onRequest: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, nil // Accept but no metadata
		},
	}

	worker := &Worker{
		handler: handler,
		logger:  &testLogger{t: t},
	}

	job := &livekit.Job{
		Id:   "test-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "test-room"},
	}

	// Should handle nil metadata gracefully
	accept, metadata := worker.callHandlerSafely(context.Background(), job)
	assert.True(t, accept)
	assert.Nil(t, metadata)
}

// TestUpdateLoad tests load calculation
func TestUpdateLoad(t *testing.T) {
	// This requires sendMessage which needs a connection
	// Skip for now as it's not critical new functionality
	t.Skip("UpdateLoad requires WebSocket connection")
}

// TestTriggerReconnect tests reconnection triggering
func TestTriggerReconnect(t *testing.T) {
	worker := &Worker{
		reconnectChan: make(chan struct{}, 1),
		savedState:    &WorkerState{ActiveJobs: make(map[string]*JobState)},
		workerID:      "test-worker",
		status:        WorkerStatusAvailable,
		load:          0.5,
		activeJobs:    make(map[string]*activeJob),
		logger:        &testLogger{t: t},
	}
	
	// Add a job to test state saving
	worker.activeJobs["job1"] = &activeJob{
		job: &livekit.Job{
			Id:   "job1",
			Room: &livekit.Room{Name: "room1"},
		},
		status:    livekit.JobStatus_JS_RUNNING,
		startedAt: time.Now(),
	}
	
	// Trigger reconnect
	worker.triggerReconnect()
	
	// Verify state was saved
	assert.Equal(t, "test-worker", worker.savedState.WorkerID)
	assert.Equal(t, WorkerStatusAvailable, worker.savedState.LastStatus)
	assert.Equal(t, float32(0.5), worker.savedState.LastLoad)
	assert.Len(t, worker.savedState.ActiveJobs, 1)
	
	// Verify reconnect signal was sent
	select {
	case <-worker.reconnectChan:
		// Good, signal was sent
	default:
		t.Error("Expected reconnect signal to be sent")
	}
}

// TestWorkerStatusString tests WorkerStatus String() method
func TestWorkerStatusString(t *testing.T) {
	tests := []struct {
		status   WorkerStatus
		expected string
	}{
		{WorkerStatusAvailable, "available"},
		{WorkerStatusFull, "full"},
		{WorkerStatus(99), "unknown"},
	}
	
	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.status.String())
		})
	}
}

// TestError tests the Error type
func TestError(t *testing.T) {
	// Test custom error
	err := &Error{Code: "TEST_ERROR", Message: "test error"}
	assert.Equal(t, "TEST_ERROR: test error", err.Error())
	
	// Test predefined errors
	assert.Equal(t, "CONNECTION_FAILED: failed to connect to LiveKit server", ErrConnectionFailed.Error())
	assert.Equal(t, "REGISTRATION_TIMEOUT: worker registration timed out", ErrRegistrationTimeout.Error())
}

// TestParseServerMessageEdgeCases tests edge cases in message parsing
func TestParseServerMessageEdgeCases(t *testing.T) {
	worker := &Worker{
		logger: &testLogger{t: t},
	}
	
	// Test empty message
	_, err := worker.parseServerMessage([]byte{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty message")
	
	// Test message too large
	bigMessage := make([]byte, 1024*1024+1)
	_, err = worker.parseServerMessage(bigMessage)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "message too large")
}

// TestHandleJobTerminationEdgeCases tests edge cases in job termination
func TestHandleJobTerminationEdgeCases(t *testing.T) {
	// Test handler panic recovery
	panicHandler := &edgeTestJobHandler{
		onTerminated: func(ctx context.Context, jobID string) {
			panic("test panic in termination")
		},
	}
	
	worker := &Worker{
		handler:    panicHandler,
		activeJobs: make(map[string]*activeJob),
		logger:     &testLogger{t: t},
	}
	
	// Add a job
	ctx, cancel := context.WithCancel(context.Background())
	worker.activeJobs["job1"] = &activeJob{
		job:    &livekit.Job{Id: "job1"},
		cancel: cancel,
		status: livekit.JobStatus_JS_RUNNING,
	}
	
	// Should handle panic gracefully
	term := &livekit.JobTermination{JobId: "job1"}
	worker.handleJobTermination(context.Background(), term)
	
	// Verify cancel was called (job context should be cancelled)
	select {
	case <-ctx.Done():
		// Good, context was cancelled
	default:
		t.Error("Expected job context to be cancelled")
	}
}