package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUniversalWorkerMethods tests all public methods of UniversalWorker
func TestUniversalWorkerMethods(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test requiring server")
	}

	handler := &SimpleUniversalHandler{}
	opts := WorkerOptions{
		AgentName: "test-worker-methods",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   2,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Test GetServerURL
	assert.Equal(t, "ws://localhost:7880", worker.GetServerURL())

	// Test GetLogger
	logger := worker.GetLogger()
	assert.NotNil(t, logger)

	// Test IsConnected (before start)
	assert.False(t, worker.IsConnected())

	// Test Health
	health := worker.Health()
	assert.NotNil(t, health)
	// Health returns a map, check status key exists
	assert.NotNil(t, health["status"])

	// Test GetMetrics
	metrics := worker.GetMetrics()
	assert.NotNil(t, metrics)
	// Metrics returns a map
	assert.NotNil(t, metrics["active_jobs"])
	assert.NotNil(t, metrics["max_jobs"])

	// Test GetQueueStats
	queueStats := worker.GetQueueStats()
	assert.NotNil(t, queueStats)
	// Queue stats is a map
	assert.NotNil(t, queueStats)

	// Test GetResourcePoolStats
	poolStats := worker.GetResourcePoolStats()
	assert.NotNil(t, poolStats)

	// Test GetActiveJobs
	jobs := worker.GetActiveJobs()
	assert.NotNil(t, jobs)
	assert.Len(t, jobs, 0)

	// Test GetJobContext (should be nil when no job)
	jobCtx, _ := worker.GetJobContext("non-existent")
	assert.Nil(t, jobCtx)

	// Test GetJobCheckpoint (should be nil when no job)
	checkpoint := worker.GetJobCheckpoint("non-existent")
	assert.Nil(t, checkpoint)

	// Test UpdateStatus (should succeed even when not connected)
	err := worker.UpdateStatus(WorkerStatusAvailable, 0.0)
	assert.NoError(t, err)

	// Test PublishTrack (should fail when not connected)
	track := &webrtc.TrackLocalStaticRTP{}
	_, err = worker.PublishTrack("test-job", track)
	assert.Error(t, err)

	// Test GetParticipant (should return nil when not connected)
	participant, _ := worker.GetParticipant("test-job", "test-identity")
	assert.Nil(t, participant)

	// Test GetAllParticipants (should return empty when not connected)
	participants, _ := worker.GetAllParticipants("test-job")
	assert.NotNil(t, participants)
	assert.Len(t, participants, 0)

	// Test SendDataToParticipant (should fail when not connected)
	err = worker.SendDataToParticipant("test-job", "test-identity", []byte("test"), true)
	assert.Error(t, err)

	// Test QueueJob
	job := &livekit.Job{
		Id:   "test-job-1",
		Room: &livekit.Room{Name: "test-room"},
		Type: livekit.JobType_JT_ROOM,
	}
	err = worker.QueueJob(job, "test-room", "token")
	assert.NoError(t, err)

	// Test SetActiveJob
	jobContext := &JobContext{
		Job:       job,
		Room:      nil,
		Cancel:    func() {},
		StartedAt: time.Now(),
	}
	worker.SetActiveJob("test-job-1", jobContext)

	// Verify job is now active
	activeJobs := worker.GetActiveJobs()
	assert.NotNil(t, activeJobs)
	// activeJobs is a map of job IDs
	_, exists := activeJobs["test-job-1"]
	assert.True(t, exists)

	// Test GetJobContext (should return the context now)
	retrievedCtx, _ := worker.GetJobContext("test-job-1")
	assert.NotNil(t, retrievedCtx)
	assert.Equal(t, job.Id, retrievedCtx.Job.Id)

	// Test GetTargetParticipant
	targetParticipant := worker.GetTargetParticipant()
	assert.Empty(t, targetParticipant)

	// Test GetParticipantInfo
	info, _ := worker.GetParticipantInfo("test-identity")
	assert.Nil(t, info)

	// Test GetAllParticipantInfo
	allInfo := worker.GetAllParticipantInfo()
	assert.NotNil(t, allInfo)
	assert.Len(t, allInfo, 0)

	// Test RequestPermissionChange
	err = worker.RequestPermissionChange("test-job", "test-identity", &livekit.ParticipantPermission{
		CanPublish:   true,
		CanSubscribe: true,
	})
	assert.Error(t, err) // Should fail when not connected

	// Test StopWithTimeout
	err = worker.StopWithTimeout(100 * time.Millisecond)
	assert.NoError(t, err)
}

// TestUniversalWorkerShutdownHooks tests shutdown hook functionality
func TestUniversalWorkerShutdownHooks(t *testing.T) {
	handler := &SimpleUniversalHandler{}
	opts := WorkerOptions{
		AgentName: "test-hooks",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Test AddShutdownHook
	hook1 := ShutdownHook{
		Name: "test-hook-1",
		Handler: func(ctx context.Context) error {
			return nil
		},
		Priority: 100,
		Timeout:  5 * time.Second,
	}
	worker.AddShutdownHook(ShutdownPhasePreStop, hook1)

	// Test GetShutdownHooks
	hooks := worker.GetShutdownHooks(ShutdownPhasePreStop)
	assert.Len(t, hooks, 1)
	assert.Equal(t, "test-hook-1", hooks[0].Name)

	// Test AddPreStopHook
	worker.AddPreStopHook("test-hook-2", func(ctx context.Context) error {
		return nil
	})

	hooks = worker.GetShutdownHooks(ShutdownPhasePreStop)
	assert.Len(t, hooks, 2)

	// Test AddCleanupHook
	worker.AddCleanupHook("test-hook-3", func(ctx context.Context) error {
		return nil
	})

	cleanupHooks := worker.GetShutdownHooks(ShutdownPhaseCleanup)
	assert.Len(t, cleanupHooks, 1)

	// Test RemoveShutdownHook
	worker.RemoveShutdownHook(ShutdownPhasePreStop, "test-hook-1")
	hooks = worker.GetShutdownHooks(ShutdownPhasePreStop)
	assert.Len(t, hooks, 1)
	assert.Equal(t, "test-hook-2", hooks[0].Name)
}

// TestUniversalWorkerStartStopMethods tests starting and stopping the worker
func TestUniversalWorkerStartStopMethods(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping test requiring server")
	}

	handler := &SimpleUniversalHandler{}
	opts := WorkerOptions{
		AgentName: "test-start-stop",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Start the worker in background
	go func() {
		err := worker.Start(context.Background())
		// Start might fail if server is not available, which is expected
		_ = err
	}()

	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// Stop the worker
	err := worker.Stop()
	assert.NoError(t, err)
}

// TestUniversalWorkerJobQueue tests job queueing functionality
func TestUniversalWorkerJobQueue(t *testing.T) {
	handler := &SimpleUniversalHandler{}
	handler.JobRequestFunc = func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
		// Accept all jobs
		return true, nil
	}

	opts := WorkerOptions{
		AgentName: "test-job-queue",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   3,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Queue multiple jobs
	for i := 0; i < 5; i++ {
		job := &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Room: &livekit.Room{Name: fmt.Sprintf("room-%d", i)},
			Type: livekit.JobType_JT_ROOM,
		}
		err := worker.QueueJob(job, "test-room", "token")
		if i < 3 {
			assert.NoError(t, err, "Should accept job %d", i)
		} else {
			// May reject if queue is full
			_ = err
		}
	}

	// Check queue stats
	stats := worker.GetQueueStats()
	assert.NotNil(t, stats)
	// stats is a map, just check it's not nil
	assert.NotNil(t, stats)
}

// TestUniversalWorkerHealthChecks tests health check functionality
func TestUniversalWorkerHealthChecks(t *testing.T) {
	handler := &SimpleUniversalHandler{}
	opts := WorkerOptions{
		AgentName: "test-health",
		JobType:   livekit.JobType_JT_ROOM,
	}

	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
	require.NotNil(t, worker)

	// Test initial health
	health := worker.Health()
	assert.NotNil(t, health["status"])
	assert.NotNil(t, health["last_checked"])
	// Check for error key

	// Test health with simulated unhealthy condition
	// This would require mocking internal state, which is complex
	// For now, we just verify the health check runs without panic
	for i := 0; i < 3; i++ {
		health = worker.Health()
		assert.NotNil(t, health)
		time.Sleep(10 * time.Millisecond)
	}
}
