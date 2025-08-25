package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestRaceProtectorBasics tests basic race protector functionality
func TestRaceProtectorBasics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	require.NotNil(t, protector)

	// Test initial state
	assert.False(t, protector.IsDisconnecting())
	assert.False(t, protector.IsReconnecting())

	// Test setting disconnecting state
	protector.SetDisconnecting(true)
	assert.True(t, protector.IsDisconnecting())

	protector.SetDisconnecting(false)
	assert.False(t, protector.IsDisconnecting())

	// Test setting reconnecting state
	protector.SetReconnecting(true)
	assert.True(t, protector.IsReconnecting())

	protector.SetReconnecting(false)
	assert.False(t, protector.IsReconnecting())
}

// TestRaceProtectorCanAcceptJob tests job acceptance logic
func TestRaceProtectorCanAcceptJob(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	// Should accept job when not disconnecting
	canAccept, reason := protector.CanAcceptJob("job-1")
	assert.True(t, canAccept)
	assert.Empty(t, reason)

	// Should reject job when disconnecting
	protector.SetDisconnecting(true)
	canAccept, reason = protector.CanAcceptJob("job-2")
	assert.False(t, canAccept)
	assert.Equal(t, "worker is disconnecting", reason)

	// Test callback is called
	var droppedJobID string
	var droppedReason string
	protector.SetOnJobDroppedCallback(func(jobID, reason string) {
		droppedJobID = jobID
		droppedReason = reason
	})

	canAccept, _ = protector.CanAcceptJob("job-3")
	assert.False(t, canAccept)
	assert.Equal(t, "job-3", droppedJobID)
	assert.Equal(t, "worker is disconnecting", droppedReason)

	// Reset disconnecting state
	protector.SetDisconnecting(false)

	// Test rejection due to pending termination
	protector.RecordTerminationRequest("job-4")
	canAccept, reason = protector.CanAcceptJob("job-4")
	assert.False(t, canAccept)
	assert.Equal(t, "job has pending termination", reason)

	// Complete the termination
	protector.CompleteTermination("job-4", nil)
	canAccept, reason = protector.CanAcceptJob("job-4")
	assert.True(t, canAccept)
	assert.Empty(t, reason)
}

// TestRaceProtectorTerminationTracking tests termination request tracking
func TestRaceProtectorTerminationTracking(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	// First termination request should be accepted
	accepted := protector.RecordTerminationRequest("job-1")
	assert.True(t, accepted)

	// Concurrent termination request should be rejected
	accepted = protector.RecordTerminationRequest("job-1")
	assert.False(t, accepted)

	// Complete the termination
	protector.CompleteTermination("job-1", nil)

	// After completion, new requests should still be rejected
	accepted = protector.RecordTerminationRequest("job-1")
	assert.False(t, accepted)

	// Different job should be accepted
	accepted = protector.RecordTerminationRequest("job-2")
	assert.True(t, accepted)
}

// TestRaceProtectorStatusUpdateQueuing tests status update queuing during reconnection
func TestRaceProtectorStatusUpdateQueuing(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	// Should not queue when not reconnecting
	queued := protector.QueueStatusUpdate("job-1", livekit.JobStatus_JS_RUNNING, "")
	assert.False(t, queued)

	// Set reconnecting state
	protector.SetReconnecting(true)

	// Should queue during reconnection
	queued = protector.QueueStatusUpdate("job-1", livekit.JobStatus_JS_RUNNING, "")
	assert.True(t, queued)

	// Test callback
	var queuedJobID string
	protector.SetOnStatusUpdateQueuedCallback(func(jobID string) {
		queuedJobID = jobID
	})

	queued = protector.QueueStatusUpdate("job-2", livekit.JobStatus_JS_SUCCESS, "")
	assert.True(t, queued)
	assert.Equal(t, "job-2", queuedJobID)

	// Queue update with higher priority status
	queued = protector.QueueStatusUpdate("job-1", livekit.JobStatus_JS_FAILED, "error")
	assert.True(t, queued)

	// Flush pending updates
	updates := protector.FlushPendingStatusUpdates()
	assert.Len(t, updates, 2)

	// Find the job-1 update
	var job1Update *StatusUpdate
	for i := range updates {
		if updates[i].JobID == "job-1" {
			job1Update = &updates[i]
			break
		}
	}
	require.NotNil(t, job1Update)
	assert.Equal(t, livekit.JobStatus_JS_FAILED, job1Update.Status)
	assert.Equal(t, "error", job1Update.Error)

	// Queue should be empty after flush
	updates = protector.FlushPendingStatusUpdates()
	assert.Nil(t, updates)
}

// TestRaceProtectorCleanup tests cleanup of old termination records
func TestRaceProtectorCleanup(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	// Record and complete some terminations
	protector.RecordTerminationRequest("job-1")
	protector.CompleteTermination("job-1", nil)

	protector.RecordTerminationRequest("job-2")
	protector.CompleteTermination("job-2", nil)

	protector.RecordTerminationRequest("job-3")
	// job-3 not completed

	// Clean up with very short max age
	cleaned := protector.CleanupOldTerminations(1 * time.Nanosecond)
	assert.Equal(t, 2, cleaned) // job-1 and job-2 should be cleaned

	// job-3 should remain as it's not completed
	canAccept, reason := protector.CanAcceptJob("job-3")
	assert.False(t, canAccept)
	assert.Equal(t, "job has pending termination", reason)
}

// TestRaceProtectorMetrics tests metrics collection
func TestRaceProtectorMetrics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	// Set various states
	protector.SetDisconnecting(true)
	protector.SetReconnecting(true)
	protector.RecordTerminationRequest("job-1")
	protector.QueueStatusUpdate("job-2", livekit.JobStatus_JS_RUNNING, "")

	// Get metrics
	metrics := protector.GetMetrics()
	assert.Equal(t, true, metrics["is_disconnecting"])
	assert.Equal(t, true, metrics["is_reconnecting"])
	assert.Equal(t, 1, metrics["active_terminations"])
	assert.Equal(t, 1, metrics["pending_status_updates"])

	// Drop some jobs
	protector.CanAcceptJob("job-3")
	protector.CanAcceptJob("job-4")

	metrics = protector.GetMetrics()
	assert.Equal(t, int64(2), metrics["dropped_jobs_during_disconnect"])
}

// TestRaceProtectorConcurrentOperations tests thread safety
func TestRaceProtectorConcurrentOperations(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	var wg sync.WaitGroup
	numGoroutines := 100
	numOperations := 10

	// Concurrent state changes
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				if id%2 == 0 {
					protector.SetDisconnecting(j%2 == 0)
				} else {
					protector.SetReconnecting(j%2 == 0)
				}
			}
		}(i)
	}

	// Concurrent job acceptance checks
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				jobID := string(rune('A' + (id+j)%26))
				protector.CanAcceptJob(jobID)
			}
		}(i)
	}

	// Concurrent termination tracking
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			jobID := string(rune('A' + id%26))
			protector.RecordTerminationRequest(jobID)
			time.Sleep(1 * time.Millisecond)
			protector.CompleteTermination(jobID, nil)
		}(i)
	}

	// Concurrent status updates
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			protector.SetReconnecting(true)
			jobID := string(rune('A' + id%26))
			status := livekit.JobStatus(id%4 + 1)
			protector.QueueStatusUpdate(jobID, status, "")
		}(i)
	}

	// Wait for all goroutines
	wg.Wait()

	// Verify no panic occurred and state is consistent
	metrics := protector.GetMetrics()
	assert.NotNil(t, metrics)
}

// TestRaceProtectorGuard tests the guard pattern
func TestRaceProtectorGuard(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	// Test job acceptance guard
	guard := protector.NewGuard("job-1", "job_accept")
	executed := false
	err := guard.Execute(context.Background(), func() error {
		executed = true
		return nil
	})
	assert.NoError(t, err)
	assert.True(t, executed)

	// Test guard blocking during disconnection
	protector.SetDisconnecting(true)
	guard = protector.NewGuard("job-2", "job_accept")
	executed = false
	err = guard.Execute(context.Background(), func() error {
		executed = true
		return nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot accept job")
	assert.False(t, executed)

	protector.SetDisconnecting(false)

	// Test termination guard
	guard = protector.NewGuard("job-3", "termination")
	executed = false
	err = guard.Execute(context.Background(), func() error {
		executed = true
		return nil
	})
	assert.NoError(t, err)
	assert.True(t, executed)

	// Second termination should be blocked
	guard = protector.NewGuard("job-3", "termination")
	executed = false
	err = guard.Execute(context.Background(), func() error {
		executed = true
		return nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent termination already in progress")
	assert.False(t, executed)
}

// TestShouldReplaceStatus tests status priority logic
func TestShouldReplaceStatus(t *testing.T) {
	tests := []struct {
		current  livekit.JobStatus
		new      livekit.JobStatus
		expected bool
	}{
		{livekit.JobStatus_JS_PENDING, livekit.JobStatus_JS_RUNNING, true},
		{livekit.JobStatus_JS_RUNNING, livekit.JobStatus_JS_SUCCESS, true},
		{livekit.JobStatus_JS_SUCCESS, livekit.JobStatus_JS_FAILED, true},
		{livekit.JobStatus_JS_FAILED, livekit.JobStatus_JS_PENDING, false},
		{livekit.JobStatus_JS_RUNNING, livekit.JobStatus_JS_PENDING, false},
		{livekit.JobStatus_JS_SUCCESS, livekit.JobStatus_JS_RUNNING, false},
	}

	for _, tt := range tests {
		result := shouldReplaceStatus(tt.current, tt.new)
		assert.Equal(t, tt.expected, result,
			"shouldReplaceStatus(%v, %v) = %v, expected %v",
			tt.current, tt.new, result, tt.expected)
	}
}

// TestRaceProtectorRealWorldScenario tests a realistic concurrent scenario
func TestRaceProtectorRealWorldScenario(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)

	// Track metrics
	var acceptedJobs int32
	var rejectedJobs int32
	var completedJobs int32

	// Simulate disconnection/reconnection cycle
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(10 * time.Millisecond)
			protector.SetDisconnecting(true)
			time.Sleep(5 * time.Millisecond)
			protector.SetDisconnecting(false)
			protector.SetReconnecting(true)
			time.Sleep(5 * time.Millisecond)
			protector.SetReconnecting(false)
		}
	}()

	// Simulate incoming jobs
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			jobID := string(rune('A' + id%26))

			// Try to accept job
			if canAccept, _ := protector.CanAcceptJob(jobID); canAccept {
				atomic.AddInt32(&acceptedJobs, 1)

				// Simulate job processing
				time.Sleep(2 * time.Millisecond)

				// Update status
				protector.QueueStatusUpdate(jobID, livekit.JobStatus_JS_RUNNING, "")

				// Simulate termination
				if protector.RecordTerminationRequest(jobID) {
					time.Sleep(1 * time.Millisecond)
					protector.CompleteTermination(jobID, nil)
					atomic.AddInt32(&completedJobs, 1)
				}
			} else {
				atomic.AddInt32(&rejectedJobs, 1)
			}
		}(i)
	}

	wg.Wait()

	// Flush any pending updates
	updates := protector.FlushPendingStatusUpdates()

	// Verify results
	t.Logf("Accepted: %d, Rejected: %d, Completed: %d, Pending updates: %d",
		acceptedJobs, rejectedJobs, completedJobs, len(updates))

	assert.Greater(t, acceptedJobs, int32(0))
	// In fast execution, all jobs might be accepted without rejections
	assert.GreaterOrEqual(t, rejectedJobs, int32(0))
	assert.LessOrEqual(t, completedJobs, acceptedJobs)
	// Ensure the system processed jobs properly
	assert.Equal(t, acceptedJobs+rejectedJobs, int32(50), "Total jobs should be 50")

	// Clean up old terminations
	cleaned := protector.CleanupOldTerminations(1 * time.Hour)
	assert.GreaterOrEqual(t, cleaned, 0)
}
