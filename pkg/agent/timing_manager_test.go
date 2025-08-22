package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestClockSkewDetection tests clock skew detection and correction
func TestClockSkewDetection(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewTimingManager(logger, TimingManagerOptions{
		MaxSkewSamples: 3,
		SkewThreshold:  500 * time.Millisecond,
	})
	
	// Simulate server time ahead by 2 seconds
	serverTime := time.Now().Add(2 * time.Second)
	localTime := time.Now()
	
	// Add samples
	manager.UpdateServerTime(serverTime, localTime)
	manager.UpdateServerTime(serverTime.Add(1*time.Second), localTime.Add(1*time.Second))
	manager.UpdateServerTime(serverTime.Add(2*time.Second), localTime.Add(2*time.Second))
	
	// Check that skew was detected
	metrics := manager.GetMetrics()
	assert.Greater(t, metrics["skew_detections"].(int64), int64(0))
	assert.InDelta(t, 2000, metrics["clock_skew_offset_ms"].(int64), 100)
	
	// Server time should be adjusted
	adjustedTime := manager.ServerTimeNow()
	expectedTime := time.Now().Add(2 * time.Second)
	assert.WithinDuration(t, expectedTime, adjustedTime, 100*time.Millisecond)
}

// TestDeadlinePropagation tests deadline setting and propagation
func TestDeadlinePropagation(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewTimingManager(logger, TimingManagerOptions{})
	
	jobID := "test-job"
	deadline := time.Now().Add(5 * time.Second)
	
	// Set deadline
	manager.SetDeadline(jobID, deadline, "test")
	
	// Get deadline
	ctx, exists := manager.GetDeadline(jobID)
	assert.True(t, exists)
	assert.Equal(t, jobID, ctx.JobID)
	assert.WithinDuration(t, deadline, ctx.OriginalDeadline, time.Millisecond)
	
	// Propagate to context
	baseCtx := context.Background()
	ctxWithDeadline, cancel := manager.PropagateDeadline(baseCtx, jobID)
	defer cancel()
	
	// Check context has deadline
	ctxDeadline, ok := ctxWithDeadline.Deadline()
	assert.True(t, ok)
	assert.WithinDuration(t, deadline, ctxDeadline, 100*time.Millisecond)
	
	// Remove deadline
	manager.RemoveDeadline(jobID)
	_, exists = manager.GetDeadline(jobID)
	assert.False(t, exists)
}

// TestDeadlineExceeded tests deadline exceeded detection
func TestDeadlineExceeded(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewTimingManager(logger, TimingManagerOptions{})
	
	jobID := "test-job"
	
	// Set deadline in the past
	deadline := time.Now().Add(-1 * time.Second)
	manager.SetDeadline(jobID, deadline, "test")
	
	// Check deadline
	exceeded, overBy := manager.CheckDeadline(jobID)
	assert.True(t, exceeded)
	assert.Greater(t, overBy, time.Duration(0))
	
	// Check metrics
	metrics := manager.GetMetrics()
	assert.Greater(t, metrics["missed_deadlines"].(int64), int64(0))
}

// TestBackpressureController tests backpressure functionality
func TestBackpressureController(t *testing.T) {
	controller := NewBackpressureController(100*time.Millisecond, 5)
	
	// Should not apply backpressure initially
	assert.False(t, controller.ShouldApplyBackpressure())
	assert.Equal(t, time.Duration(0), controller.GetDelay())
	
	// Generate events to trigger backpressure
	for i := 0; i < 6; i++ {
		controller.RecordEvent()
	}
	
	// Should apply backpressure
	assert.True(t, controller.ShouldApplyBackpressure())
	assert.Greater(t, controller.GetDelay(), time.Duration(0))
	assert.True(t, controller.IsActive())
	
	// Wait for window to pass
	time.Sleep(150 * time.Millisecond)
	
	// Should no longer apply backpressure
	assert.False(t, controller.ShouldApplyBackpressure())
}

// TestBackpressureIntegration tests backpressure with timing manager
func TestBackpressureIntegration(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewTimingManager(logger, TimingManagerOptions{
		BackpressureWindow: 100 * time.Millisecond,
		BackpressureLimit:  3,
	})
	
	// Generate events
	for i := 0; i < 5; i++ {
		manager.RecordEvent()
	}
	
	// Check backpressure
	assert.True(t, manager.CheckBackpressure())
	delay := manager.GetBackpressureDelay()
	assert.Greater(t, delay, time.Duration(0))
	
	// Check metrics
	metrics := manager.GetMetrics()
	assert.True(t, metrics["backpressure_active"].(bool))
	assert.Greater(t, metrics["backpressure_events"].(int64), int64(0))
}

// TestTimingGuard tests the timing guard functionality
func TestTimingGuard(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewTimingManager(logger, TimingManagerOptions{})
	
	jobID := "test-job"
	deadline := time.Now().Add(1 * time.Second)
	manager.SetDeadline(jobID, deadline, "test")
	
	// Test successful execution
	guard := manager.NewGuard(jobID, "test_op")
	executed := false
	
	err := guard.Execute(context.Background(), func(ctx context.Context) error {
		executed = true
		// Check context has deadline
		_, hasDeadline := ctx.Deadline()
		assert.True(t, hasDeadline)
		return nil
	})
	
	assert.NoError(t, err)
	assert.True(t, executed)
	
	// Test with exceeded deadline
	time.Sleep(1100 * time.Millisecond)
	
	executed = false
	err = guard.Execute(context.Background(), func(ctx context.Context) error {
		executed = true
		return nil
	})
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deadline exceeded")
	assert.False(t, executed)
}

// TestClockSkewAdjustment tests deadline adjustment for clock skew
func TestClockSkewAdjustment(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewTimingManager(logger, TimingManagerOptions{
		MaxSkewSamples: 1,
		SkewThreshold:  100 * time.Millisecond,
	})
	
	// Set initial deadline
	jobID := "test-job"
	originalDeadline := time.Now().Add(5 * time.Second)
	manager.SetDeadline(jobID, originalDeadline, "test")
	
	// Get initial deadline
	ctx1, _ := manager.GetDeadline(jobID)
	initialAdjusted := ctx1.AdjustedDeadline
	
	// Simulate clock skew detection
	serverTime := time.Now().Add(1 * time.Second)
	manager.UpdateServerTime(serverTime, time.Now())
	
	// Get adjusted deadline
	ctx2, _ := manager.GetDeadline(jobID)
	newAdjusted := ctx2.AdjustedDeadline
	
	// Deadline should be adjusted for skew
	assert.NotEqual(t, initialAdjusted, newAdjusted)
}

// TestConcurrentOperations tests thread safety
func TestConcurrentOperations(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewTimingManager(logger, TimingManagerOptions{})
	
	var wg sync.WaitGroup
	
	// Concurrent deadline operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			
			jobID := string(rune('A' + id))
			deadline := time.Now().Add(time.Duration(id) * time.Second)
			
			manager.SetDeadline(jobID, deadline, "test")
			manager.CheckDeadline(jobID)
			manager.RemoveDeadline(jobID)
		}(i)
	}
	
	// Concurrent event recording
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			
			manager.RecordEvent()
			manager.CheckBackpressure()
		}()
	}
	
	// Concurrent clock skew updates
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			
			serverTime := time.Now().Add(time.Duration(offset) * time.Second)
			manager.UpdateServerTime(serverTime, time.Now())
		}(i)
	}
	
	wg.Wait()
	
	// Verify no panics and get metrics
	metrics := manager.GetMetrics()
	assert.NotNil(t, metrics)
}

// TestClockSkewDetector tests the standalone clock skew detector
func TestClockSkewDetector(t *testing.T) {
	detector := NewClockSkewDetector(3)
	
	// Add samples with 1 second skew
	for i := 0; i < 3; i++ {
		local := time.Now()
		remote := local.Add(1 * time.Second)
		skew := detector.AddSample(local, remote)
		
		// After 3 samples, average should be ~1 second
		if i == 2 {
			assert.InDelta(t, float64(1*time.Second), float64(skew), float64(100*time.Millisecond))
		}
	}
	
	avgSkew := detector.GetAverageSkew()
	assert.InDelta(t, float64(1*time.Second), float64(avgSkew), float64(100*time.Millisecond))
}

// TestDeadlineManager tests the high-level deadline manager
func TestDeadlineManager(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	tm := NewTimingManager(logger, TimingManagerOptions{})
	dm := NewDeadlineManager(tm, logger)
	
	// Set job deadline
	job := &livekit.Job{
		Id: "test-job",
		Room: &livekit.Room{
			Name: "test-room",
		},
	}
	
	dm.SetJobDeadline(job)
	
	// Create context with deadline
	ctx, cancel := dm.CreateContextWithDeadline(context.Background(), job.Id)
	defer cancel()
	
	// Verify context has deadline
	deadline, ok := ctx.Deadline()
	assert.True(t, ok)
	assert.False(t, deadline.IsZero())
}

// TestWorkerTimingIntegration tests integration with Worker
func TestWorkerTimingIntegration(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	// Verify timing manager was created
	assert.NotNil(t, worker.Worker.timingManager)
	
	// Check health includes timing metrics
	health := worker.Worker.Health()
	assert.Contains(t, health, "timing")
	
	timing := health["timing"].(map[string]interface{})
	assert.Contains(t, timing, "clock_skew_offset_ms")
	assert.Contains(t, timing, "backpressure_active")
}

// TestBackpressureRate tests accurate rate calculation
func TestBackpressureRate(t *testing.T) {
	controller := NewBackpressureController(1*time.Second, 10)
	
	// Record exactly 10 events
	for i := 0; i < 10; i++ {
		controller.RecordEvent()
		time.Sleep(50 * time.Millisecond)
	}
	
	rate := controller.GetCurrentRate()
	assert.InDelta(t, 10.0, rate, 1.0)
	
	// Should not trigger backpressure at limit
	assert.False(t, controller.ShouldApplyBackpressure())
	
	// One more should trigger
	controller.RecordEvent()
	assert.True(t, controller.ShouldApplyBackpressure())
}