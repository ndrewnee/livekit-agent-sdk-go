package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestRaceProtectorJobDuringDisconnect tests job rejection during disconnection
func TestRaceProtectorJobDuringDisconnect(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	// Initially should accept jobs
	canAccept, reason := protector.CanAcceptJob("job1")
	assert.True(t, canAccept)
	assert.Empty(t, reason)
	
	// Set disconnecting
	protector.SetDisconnecting(true)
	
	// Should reject jobs
	canAccept, reason = protector.CanAcceptJob("job2")
	assert.False(t, canAccept)
	assert.Equal(t, "worker is disconnecting", reason)
	
	// Check metrics
	metrics := protector.GetMetrics()
	assert.True(t, metrics["is_disconnecting"].(bool))
	assert.Equal(t, int64(1), metrics["dropped_jobs_during_disconnect"].(int64))
	
	// Clear disconnecting
	protector.SetDisconnecting(false)
	
	// Should accept jobs again
	canAccept, reason = protector.CanAcceptJob("job3")
	assert.True(t, canAccept)
	assert.Empty(t, reason)
}

// TestRaceProtectorConcurrentTermination tests concurrent termination protection
func TestRaceProtectorConcurrentTermination(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	// First termination should be allowed
	allowed := protector.RecordTerminationRequest("job1")
	assert.True(t, allowed)
	
	// Concurrent terminations should be rejected
	allowed = protector.RecordTerminationRequest("job1")
	assert.False(t, allowed)
	
	allowed = protector.RecordTerminationRequest("job1")
	assert.False(t, allowed)
	
	// Complete the termination
	protector.CompleteTermination("job1", nil)
	
	// Should still reject (already completed)
	allowed = protector.RecordTerminationRequest("job1")
	assert.False(t, allowed)
	
	// Different job should be allowed
	allowed = protector.RecordTerminationRequest("job2")
	assert.True(t, allowed)
}

// TestRaceProtectorStatusUpdateDuringReconnection tests status update queuing
func TestRaceProtectorStatusUpdateDuringReconnection(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	// Initially should not queue
	queued := protector.QueueStatusUpdate("job1", livekit.JobStatus_JS_RUNNING, "")
	assert.False(t, queued)
	
	// Set reconnecting
	protector.SetReconnecting(true)
	
	// Should queue updates
	queued = protector.QueueStatusUpdate("job1", livekit.JobStatus_JS_RUNNING, "")
	assert.True(t, queued)
	
	queued = protector.QueueStatusUpdate("job2", livekit.JobStatus_JS_SUCCESS, "")
	assert.True(t, queued)
	
	// Update existing with higher priority status
	queued = protector.QueueStatusUpdate("job1", livekit.JobStatus_JS_FAILED, "error")
	assert.True(t, queued)
	
	// Check metrics
	metrics := protector.GetMetrics()
	assert.True(t, metrics["is_reconnecting"].(bool))
	assert.Equal(t, 2, metrics["pending_status_updates"].(int))
	
	// Flush updates
	updates := protector.FlushPendingStatusUpdates()
	assert.Len(t, updates, 2)
	
	// Find job1 update - should have FAILED status
	for _, update := range updates {
		if update.JobID == "job1" {
			assert.Equal(t, livekit.JobStatus_JS_FAILED, update.Status)
			assert.Equal(t, "error", update.Error)
		}
	}
	
	// Queue should be empty
	metrics = protector.GetMetrics()
	assert.Equal(t, 0, metrics["pending_status_updates"].(int))
}

// TestRaceProtectorJobRejectionWithTermination tests job rejection when termination is pending
func TestRaceProtectorJobRejectionWithTermination(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	// Record a termination
	protector.RecordTerminationRequest("job1")
	
	// Should reject new assignment for same job
	canAccept, reason := protector.CanAcceptJob("job1")
	assert.False(t, canAccept)
	assert.Equal(t, "job has pending termination", reason)
	
	// Complete termination
	protector.CompleteTermination("job1", nil)
	
	// Should accept now (termination completed)
	canAccept, reason = protector.CanAcceptJob("job1")
	assert.True(t, canAccept)
	assert.Equal(t, "", reason)
}

// TestRaceProtectorConcurrentOperations tests thread safety
func TestRaceProtectorConcurrentOperations(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	var wg sync.WaitGroup
	
	// Concurrent disconnection checks
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			
			protector.SetDisconnecting(id%2 == 0)
			protector.CanAcceptJob(string(rune('A' + id)))
			protector.SetDisconnecting(false)
		}(i)
	}
	
	// Concurrent terminations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			
			jobID := string(rune('A' + id%5))
			protector.RecordTerminationRequest(jobID)
			time.Sleep(10 * time.Millisecond)
			protector.CompleteTermination(jobID, nil)
		}(i)
	}
	
	// Concurrent status updates
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			
			protector.SetReconnecting(true)
			protector.QueueStatusUpdate(string(rune('A'+id)), livekit.JobStatus_JS_RUNNING, "")
			protector.SetReconnecting(false)
		}(i)
	}
	
	wg.Wait()
	
	// Verify no panics and state is consistent
	metrics := protector.GetMetrics()
	assert.NotNil(t, metrics)
}

// TestRaceProtectorCallbacks tests callback functionality
func TestRaceProtectorCallbacks(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	var droppedJobs []string
	var queuedUpdates []string
	
	// Set callbacks
	protector.SetOnJobDroppedCallback(func(jobID, reason string) {
		droppedJobs = append(droppedJobs, jobID)
	})
	
	protector.SetOnStatusUpdateQueuedCallback(func(jobID string) {
		queuedUpdates = append(queuedUpdates, jobID)
	})
	
	// Trigger job drop
	protector.SetDisconnecting(true)
	protector.CanAcceptJob("job1")
	protector.CanAcceptJob("job2")
	
	assert.Len(t, droppedJobs, 2)
	assert.Contains(t, droppedJobs, "job1")
	assert.Contains(t, droppedJobs, "job2")
	
	// Trigger status queue
	protector.SetReconnecting(true)
	protector.QueueStatusUpdate("job3", livekit.JobStatus_JS_RUNNING, "")
	protector.QueueStatusUpdate("job4", livekit.JobStatus_JS_SUCCESS, "")
	
	assert.Len(t, queuedUpdates, 2)
	assert.Contains(t, queuedUpdates, "job3")
	assert.Contains(t, queuedUpdates, "job4")
}

// TestRaceProtectorCleanup tests old termination cleanup
func TestRaceProtectorCleanup(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	// Create some terminations
	protector.RecordTerminationRequest("job1")
	protector.CompleteTermination("job1", nil)
	
	time.Sleep(100 * time.Millisecond)
	
	protector.RecordTerminationRequest("job2")
	protector.CompleteTermination("job2", nil)
	
	// Cleanup old ones (100ms)
	cleaned := protector.CleanupOldTerminations(50 * time.Millisecond)
	assert.Equal(t, 1, cleaned) // Only job1 should be cleaned
	
	// Verify job2 still exists
	allowed := protector.RecordTerminationRequest("job2")
	assert.False(t, allowed) // Should still be tracked
}

// TestRaceProtectionGuard tests the guard pattern
func TestRaceProtectionGuard(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	protector := NewRaceProtector(logger)
	
	// Test job accept guard
	guard := protector.NewGuard("job1", "job_accept")
	
	executed := false
	err := guard.Execute(context.Background(), func() error {
		executed = true
		return nil
	})
	
	assert.NoError(t, err)
	assert.True(t, executed)
	
	// Test with disconnecting
	protector.SetDisconnecting(true)
	guard = protector.NewGuard("job2", "job_accept")
	
	executed = false
	err = guard.Execute(context.Background(), func() error {
		executed = true
		return nil
	})
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot accept job")
	assert.False(t, executed)
	
	// Test termination guard
	protector.SetDisconnecting(false)
	guard = protector.NewGuard("job3", "termination")
	
	var execCount int32
	for i := 0; i < 3; i++ {
		go func() {
			guard.Execute(context.Background(), func() error {
				atomic.AddInt32(&execCount, 1)
				return nil
			})
		}()
	}
	
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), atomic.LoadInt32(&execCount)) // Only one should execute
}

// TestWorkerRaceProtectionIntegration tests integration with Worker
func TestWorkerRaceProtectionIntegration(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	// Simulate disconnection scenario
	worker.Worker.raceProtector.SetDisconnecting(true)
	
	// Try to handle availability request
	req := &livekit.AvailabilityRequest{
		Job: &livekit.Job{
			Id: "test-job",
			Room: &livekit.Room{
				Name: "test-room",
			},
		},
	}
	
	// Should reject the job
	worker.Worker.handleAvailabilityRequest(context.Background(), req)
	
	// Verify job was rejected
	metrics := worker.Worker.raceProtector.GetMetrics()
	assert.Equal(t, int64(1), metrics["dropped_jobs_during_disconnect"].(int64))
}

// TestStatusUpdatePriority tests status update priority replacement
func TestStatusUpdatePriority(t *testing.T) {
	// Test the priority logic
	testCases := []struct {
		current  livekit.JobStatus
		new      livekit.JobStatus
		expected bool
	}{
		{livekit.JobStatus_JS_PENDING, livekit.JobStatus_JS_RUNNING, true},
		{livekit.JobStatus_JS_RUNNING, livekit.JobStatus_JS_SUCCESS, true},
		{livekit.JobStatus_JS_RUNNING, livekit.JobStatus_JS_FAILED, true},
		{livekit.JobStatus_JS_SUCCESS, livekit.JobStatus_JS_RUNNING, false},
		{livekit.JobStatus_JS_FAILED, livekit.JobStatus_JS_SUCCESS, false},
		// JS_CANCELLED is not available in current protocol version
		// {livekit.JobStatus_JS_FAILED, livekit.JobStatus_JS_CANCELLED, false},
	}
	
	for _, tc := range testCases {
		result := shouldReplaceStatus(tc.current, tc.new)
		assert.Equal(t, tc.expected, result, 
			"Expected %v -> %v to be %v", tc.current, tc.new, tc.expected)
	}
}