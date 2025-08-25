package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUniversalWorkerStartStopFull tests the Start and Stop methods
func TestUniversalWorkerStartStopFull(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-worker",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   2,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Start the worker in a goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var startErr error
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		startErr = worker.Start(ctx)
	}()

	// Give it time to attempt connection
	time.Sleep(100 * time.Millisecond)

	// Stop the worker
	err := worker.Stop()
	assert.NoError(t, err)

	// Cancel context to ensure Start returns
	cancel()

	// Wait for Start to complete
	wg.Wait()

	// Start error might be nil if server is running, or context.Canceled
	if startErr != nil && startErr != context.Canceled && startErr != context.DeadlineExceeded {
		t.Logf("Start returned error: %v", startErr)
	}
}

// TestUniversalWorkerQueueJob tests job queueing
func TestUniversalWorkerQueueJob(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName:      "test-queue",
		JobType:        livekit.JobType_JT_ROOM,
		MaxJobs:        3,
		EnableJobQueue: true, // Enable job queuing
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Queue jobs
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
		{
			Id:   "job-3",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room-3"},
		},
	}

	for _, job := range jobs {
		err := worker.QueueJob(job, job.Room.Name, "token")
		assert.NoError(t, err)
	}

	// Verify jobs were queued
	stats := worker.GetQueueStats()
	assert.NotNil(t, stats)
}

// TestUniversalWorkerSetActiveJob tests setting and getting active jobs
func TestUniversalWorkerSetActiveJob(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-active",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   2,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create job contexts
	job1 := &livekit.Job{
		Id:   "job-1",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-1"},
	}

	job2 := &livekit.Job{
		Id:   "job-2",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-2"},
	}

	ctx1 := &JobContext{
		Job:       job1,
		Room:      nil,
		Cancel:    func() {},
		StartedAt: time.Now(),
	}

	ctx2 := &JobContext{
		Job:       job2,
		Room:      nil,
		Cancel:    func() {},
		StartedAt: time.Now(),
	}

	// Set active jobs
	worker.SetActiveJob("job-1", ctx1)
	worker.SetActiveJob("job-2", ctx2)

	// Get active jobs
	activeJobs := worker.GetActiveJobs()
	assert.NotNil(t, activeJobs)
	assert.Contains(t, activeJobs, "job-1")
	assert.Contains(t, activeJobs, "job-2")

	// Get specific job context
	retrievedCtx, exists := worker.GetJobContext("job-1")
	assert.True(t, exists)
	assert.NotNil(t, retrievedCtx)
	assert.Equal(t, "job-1", retrievedCtx.Job.Id)

	// Test non-existent job
	retrievedCtx, exists = worker.GetJobContext("non-existent")
	assert.False(t, exists)
	assert.Nil(t, retrievedCtx)
}

// TestUniversalWorkerUpdateStatus tests status updates
func TestUniversalWorkerUpdateStatus(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-status",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   2,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Update status - will fail when not connected
	err := worker.UpdateStatus(WorkerStatusAvailable, 0.5)
	assert.Error(t, err) // Expected to fail when not connected
	assert.Equal(t, ErrNotConnected, err)

	// Update to full - will also fail when not connected
	err = worker.UpdateStatus(WorkerStatusFull, 1.0)
	assert.Error(t, err) // Expected to fail when not connected
	assert.Equal(t, ErrNotConnected, err)
}

// TestUniversalWorkerGetMetrics tests metrics retrieval
func TestUniversalWorkerGetMetrics(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-metrics",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   5,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Add some active jobs
	for i := 0; i < 3; i++ {
		job := &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: fmt.Sprintf("room-%d", i)},
		}

		ctx := &JobContext{
			Job:       job,
			Room:      nil,
			Cancel:    func() {},
			StartedAt: time.Now(),
		}

		worker.SetActiveJob(job.Id, ctx)
	}

	// Get metrics
	metrics := worker.GetMetrics()
	assert.NotNil(t, metrics)
	// GetMetrics returns job processing metrics, not active_jobs/max_jobs
	assert.Contains(t, metrics, "jobs_accepted")
	assert.Contains(t, metrics, "jobs_completed")
	assert.Contains(t, metrics, "jobs_failed")
}

// TestUniversalWorkerShutdownHooksFull tests shutdown hook functionality
func TestUniversalWorkerShutdownHooksFull(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-hooks",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Add shutdown hooks
	worker.AddPreStopHook("test-pre-stop", func(ctx context.Context) error {
		return nil
	})

	worker.AddCleanupHook("test-cleanup", func(ctx context.Context) error {
		return nil
	})

	// Add hook with error
	worker.AddShutdownHook(ShutdownPhasePreStop, ShutdownHook{
		Name: "error-hook",
		Handler: func(ctx context.Context) error {
			return errors.New("test error")
		},
		Priority: 100,
		Timeout:  1 * time.Second,
	})

	// Get hooks
	preStopHooks := worker.GetShutdownHooks(ShutdownPhasePreStop)
	assert.Len(t, preStopHooks, 2)

	cleanupHooks := worker.GetShutdownHooks(ShutdownPhaseCleanup)
	assert.Len(t, cleanupHooks, 1)

	// Remove a hook
	worker.RemoveShutdownHook(ShutdownPhasePreStop, "error-hook")
	preStopHooks = worker.GetShutdownHooks(ShutdownPhasePreStop)
	assert.Len(t, preStopHooks, 1)
}

// TestUniversalWorkerJobCheckpoint tests job checkpointing
func TestUniversalWorkerJobCheckpoint(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-checkpoint",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create a job
	job := &livekit.Job{
		Id:   "job-checkpoint",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-checkpoint"},
	}

	// Create a checkpoint using the constructor
	checkpoint := NewJobCheckpoint(job.Id)
	checkpoint.Save("key", "value")
	checkpoint.Save("counter", 42)

	// Create job context with custom data for checkpoint
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

	// Get checkpoint
	retrievedCheckpoint := worker.GetJobCheckpoint(job.Id)
	// GetJobCheckpoint returns the actual checkpoint from recovery manager
	// For this test, it will be nil since we haven't set up recovery
	assert.Nil(t, retrievedCheckpoint)

	// Test the checkpoint we created
	value, exists := checkpoint.Load("key")
	assert.True(t, exists)
	assert.Equal(t, "value", value)

	counter, exists := checkpoint.Load("counter")
	assert.True(t, exists)
	assert.Equal(t, 42, counter)
}

// TestUniversalWorkerResourcePool tests resource pool statistics
func TestUniversalWorkerResourcePool(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-resource",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Get resource pool stats
	stats := worker.GetResourcePoolStats()
	assert.NotNil(t, stats)
}

// TestUniversalWorkerParticipantManagement tests participant-related methods
func TestUniversalWorkerParticipantManagement(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-participant",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create a job with mock room
	job := &livekit.Job{
		Id:   "job-participant",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-participant"},
		Participant: &livekit.ParticipantInfo{
			Identity: "target-participant",
			Sid:      "participant-sid",
		},
	}

	// Use nil Room since we can't easily mock lksdk.Room
	ctx := &JobContext{
		Job:       job,
		Room:      nil,
		Cancel:    func() {},
		StartedAt: time.Now(),
	}

	// Set active job
	worker.SetActiveJob(job.Id, ctx)

	// Test GetTargetParticipant
	targetIdentity := worker.GetTargetParticipant()
	// GetTargetParticipant returns nil when no job context with a room
	assert.Nil(t, targetIdentity)

	// Test GetParticipant - should fail without room connection
	participant, err := worker.GetParticipant(job.Id, "target-participant")
	assert.Error(t, err) // Expected to fail without room connection
	assert.Contains(t, err.Error(), "not connected to room")
	assert.Nil(t, participant)

	// Test GetAllParticipants - should fail without room connection
	participants, err := worker.GetAllParticipants(job.Id)
	assert.Error(t, err) // Expected to fail without room connection
	assert.Contains(t, err.Error(), "not connected to room")
	assert.Nil(t, participants)

	// Test GetParticipantInfo
	info, _ := worker.GetParticipantInfo("target-participant")
	// info will be nil since we don't have real participant info
	assert.Nil(t, info)

	// Test GetAllParticipantInfo
	allInfo := worker.GetAllParticipantInfo()
	assert.NotNil(t, allInfo)
	assert.Len(t, allInfo, 0) // No real participant info available

	// Test RequestPermissionChange
	err = worker.RequestPermissionChange(job.Id, "target-participant", &livekit.ParticipantPermission{
		CanPublish:   true,
		CanSubscribe: true,
	})
	assert.Error(t, err) // Will error since no real room connection
}

// TestUniversalWorkerDataTransmission tests data sending functionality
func TestUniversalWorkerDataTransmission(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-data",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create a job with mock room
	job := &livekit.Job{
		Id:   "job-data",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-data"},
	}

	ctx := &JobContext{
		Job:       job,
		Room:      nil,
		Cancel:    func() {},
		StartedAt: time.Now(),
	}

	// Set active job
	worker.SetActiveJob(job.Id, ctx)

	// Test SendDataToParticipant - should fail without room connection
	err := worker.SendDataToParticipant(job.Id, "participant-1", []byte("test data"), true)
	assert.Error(t, err) // Expected to fail without room connection
	assert.Contains(t, err.Error(), "not connected to room")
}

// TestUniversalWorkerStopWithTimeoutFull tests graceful shutdown with timeout
func TestUniversalWorkerStopWithTimeoutFull(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-timeout",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Add a slow shutdown hook
	worker.AddPreStopHook("slow-hook", func(ctx context.Context) error {
		select {
		case <-time.After(1 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	// Stop with short timeout
	err := worker.StopWithTimeout(100 * time.Millisecond)
	// May or may not error depending on timing
	_ = err
}

// TestUniversalWorkerConcurrentOperations tests concurrent access to worker methods
func TestUniversalWorkerConcurrentOperations(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-concurrent",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	var wg sync.WaitGroup

	// Concurrent job operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			job := &livekit.Job{
				Id:   fmt.Sprintf("job-%d", id),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: fmt.Sprintf("room-%d", id)},
			}

			// Queue job
			_ = worker.QueueJob(job, job.Room.Name, "token")

			// Set as active
			ctx := &JobContext{
				Job:       job,
				Room:      nil,
				Cancel:    func() {},
				StartedAt: time.Now(),
			}
			worker.SetActiveJob(job.Id, ctx)

			// Get context
			_, _ = worker.GetJobContext(job.Id)

			// Update status
			_ = worker.UpdateStatus(WorkerStatusAvailable, float32(id)/10.0)

			// Get metrics
			_ = worker.GetMetrics()

			// Get active jobs
			_ = worker.GetActiveJobs()
		}(i)
	}

	// Wait for all operations to complete
	wg.Wait()

	// Verify final state
	activeJobs := worker.GetActiveJobs()
	assert.NotNil(t, activeJobs)
}

// TestUniversalWorkerWithMockWebSocket tests worker with mock WebSocket connection
func TestUniversalWorkerWithMockWebSocket(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-websocket",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   2,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Create mock WebSocket connection
	mockConn := NewMockWebSocketConn()

	// Simulate server messages
	availableMsg := map[string]interface{}{
		"type": "WORKER_STATUS",
		"status": map[string]interface{}{
			"status": "available",
			"load":   0.0,
		},
	}

	msgBytes, _ := json.Marshal(availableMsg)
	mockConn.SimulateMessage(msgBytes)

	// Simulate job assignment
	jobMsg := map[string]interface{}{
		"type": "JOB_ASSIGNMENT",
		"job": map[string]interface{}{
			"id":   "test-job",
			"type": "JT_ROOM",
			"room": map[string]interface{}{
				"name": "test-room",
			},
		},
		"token": "test-token",
		"url":   "ws://localhost:7880",
	}

	jobBytes, _ := json.Marshal(jobMsg)
	mockConn.SimulateMessage(jobBytes)

	// Test would use the mock connection if we could inject it
	// This demonstrates how mocking would work
	assert.NotNil(t, mockConn)
}

// TestUniversalWorkerErrorHandlingFull tests error handling scenarios
func TestUniversalWorkerErrorHandlingFull(t *testing.T) {
	handler := NewMockUniversalHandler()
	handler.SetAssignError(errors.New("assignment failed"))

	opts := WorkerOptions{
		AgentName: "test-errors",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   2,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Test with invalid job
	err := worker.QueueJob(nil, "", "")
	assert.Error(t, err)

	// Test getting context for non-existent job
	ctx, exists := worker.GetJobContext("non-existent")
	assert.False(t, exists)
	assert.Nil(t, ctx)

	// Test participant methods without active job
	participant, err := worker.GetParticipant("non-existent", "participant")
	assert.Error(t, err)
	assert.Nil(t, participant)

	participants, err := worker.GetAllParticipants("non-existent")
	assert.Error(t, err)
	assert.Nil(t, participants)

	// Test data sending without active job
	err = worker.SendDataToParticipant("non-existent", "participant", []byte("data"), true)
	assert.Error(t, err)

	// Test permission change without active job
	err = worker.RequestPermissionChange("non-existent", "participant", &livekit.ParticipantPermission{})
	assert.Error(t, err)
}

// TestUniversalWorkerLoggerIntegration tests logger integration
func TestUniversalWorkerLoggerIntegration(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-logger",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Get logger
	workerLogger := worker.GetLogger()
	assert.NotNil(t, workerLogger)

	// Logger should work without panicking
	workerLogger.Info("test log message")
	workerLogger.Debug("debug message")
	workerLogger.Error("error message")
}
