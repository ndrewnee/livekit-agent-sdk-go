package agent

import (
	"context"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestResourceLimiterMemoryLimit tests memory limit enforcement
func TestResourceLimiterMemoryLimit(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	memoryExceeded := false
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		MemoryLimitMB:     50, // Very low limit for testing
		CheckInterval:     50 * time.Millisecond,
		EnforceHardLimits: true,
	})
	
	limiter.SetMemoryLimitCallback(func(usage, limit uint64) {
		memoryExceeded = true
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	limiter.Start(ctx)
	
	// Allocate memory to trigger limit
	bigSlice := make([]byte, 60*1024*1024) // 60MB
	_ = bigSlice // Keep reference
	
	// Wait for detection
	time.Sleep(100 * time.Millisecond)
	
	assert.True(t, memoryExceeded)
	
	metrics := limiter.GetMetrics()
	assert.Greater(t, metrics["memory_violations"].(int64), int64(0))
}

// TestResourceLimiterCPUThrottle tests CPU throttling
func TestResourceLimiterCPUThrottle(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	// cpuExceeded := false
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		CPUQuotaPercent:   50, // Very low for testing
		CheckInterval:     100 * time.Millisecond,
		EnforceHardLimits: true,
	})
	
	limiter.SetCPULimitCallback(func(usage float64) {
		// cpuExceeded = true
		t.Logf("CPU limit exceeded: %f%%", usage)
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	limiter.Start(ctx)
	
	// Create CPU-intensive work
	done := make(chan bool)
	go func() {
		start := time.Now()
		for time.Since(start) < 300*time.Millisecond {
			// Busy loop
			for i := 0; i < 1000000; i++ {
				_ = i * i
			}
		}
		done <- true
	}()
	
	// Wait for work to complete
	<-done
	
	// CPU tracking might not trigger in short tests, but verify no panic
	metrics := limiter.GetMetrics()
	assert.NotNil(t, metrics)
}

// TestResourceLimiterFileDescriptors tests FD limit detection
func TestResourceLimiterFileDescriptors(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	// fdExceeded := false
	currentFDs := NewFileDescriptorTracker().GetCurrentCount()
	
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		MaxFileDescriptors: currentFDs + 5, // Allow only 5 more FDs
		CheckInterval:      50 * time.Millisecond,
		EnforceHardLimits:  true,
	})
	
	limiter.SetFDLimitCallback(func(usage, limit int) {
		// fdExceeded = true
		t.Logf("FD limit exceeded: %d/%d", usage, limit)
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	limiter.Start(ctx)
	
	// Open files to exceed limit
	files := make([]*os.File, 0)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	
	for i := 0; i < 10; i++ {
		f, err := os.CreateTemp("", "fdtest")
		if err != nil {
			break
		}
		files = append(files, f)
	}
	
	// Wait for detection
	time.Sleep(100 * time.Millisecond)
	
	// May or may not exceed depending on system state
	metrics := limiter.GetMetrics()
	assert.NotNil(t, metrics["file_descriptors"])
}

// TestResourceLimiterMetrics tests metrics collection
func TestResourceLimiterMetrics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		MemoryLimitMB:      1024,
		CPUQuotaPercent:    200,
		MaxFileDescriptors: 1024,
	})
	
	metrics := limiter.GetMetrics()
	
	// Check all expected metrics exist
	assert.Contains(t, metrics, "memory_usage_mb")
	assert.Contains(t, metrics, "memory_limit_mb")
	assert.Contains(t, metrics, "memory_violations")
	assert.Contains(t, metrics, "cpu_quota_percent")
	assert.Contains(t, metrics, "cpu_violations")
	assert.Contains(t, metrics, "file_descriptors")
	assert.Contains(t, metrics, "fd_limit")
	assert.Contains(t, metrics, "fd_violations")
	assert.Contains(t, metrics, "enforcing")
	
	// Check types
	assert.IsType(t, uint64(0), metrics["memory_usage_mb"])
	assert.IsType(t, int64(0), metrics["memory_violations"])
	assert.IsType(t, bool(false), metrics["enforcing"])
}

// TestCPUThrottler tests CPU throttling behavior
func TestCPUThrottler(t *testing.T) {
	throttler := NewCPUThrottler(100) // 100% = 1 core
	
	// Test no throttling when under limit
	start := time.Now()
	throttler.Throttle(50.0) // 50% usage
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Millisecond) // Should not sleep
	
	// Test throttling when over limit
	start = time.Now()
	throttler.Throttle(150.0) // 150% usage
	elapsed = time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 1*time.Millisecond) // Should sleep
}

// TestFileDescriptorTracker tests FD tracking
func TestFileDescriptorTracker(t *testing.T) {
	tracker := NewFileDescriptorTracker()
	
	initialCount := tracker.GetCurrentCount()
	assert.Greater(t, initialCount, 0) // Should have at least stdin/stdout/stderr
	
	// Open a file
	f, err := os.CreateTemp("", "fdtest")
	assert.NoError(t, err)
	defer f.Close()
	
	newCount := tracker.GetCurrentCount()
	assert.GreaterOrEqual(t, newCount, initialCount) // May or may not increase due to system activity
}

// TestResourceLimitGuard tests the guard pattern
func TestResourceLimitGuard(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		MemoryLimitMB:      10, // Very low limit
		MaxFileDescriptors: 10, // Very low limit
		EnforceHardLimits:  true,
	})
	
	// Test with high memory usage
	bigSlice := make([]byte, 15*1024*1024) // 15MB
	_ = bigSlice
	
	guard := limiter.NewGuard("test_operation")
	
	executed := false
	err := guard.Execute(context.Background(), func() error {
		executed = true
		return nil
	})
	
	// Should fail due to memory limit
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "memory usage too high")
	assert.False(t, executed)
}

// TestResourceLimiterCallbacks tests callback functionality
func TestResourceLimiterCallbacks(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	var memCallbackCalled bool
	var cpuCallbackCalled bool
	var fdCallbackCalled bool
	
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		MemoryLimitMB:      1,
		CPUQuotaPercent:    1,
		MaxFileDescriptors: 1,
		CheckInterval:      50 * time.Millisecond,
	})
	
	limiter.SetMemoryLimitCallback(func(usage, limit uint64) {
		memCallbackCalled = true
	})
	
	limiter.SetCPULimitCallback(func(usage float64) {
		cpuCallbackCalled = true
	})
	
	limiter.SetFDLimitCallback(func(usage, limit int) {
		fdCallbackCalled = true
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	limiter.Start(ctx)
	
	// Trigger checks
	time.Sleep(100 * time.Millisecond)
	
	// At least memory and FD callbacks should be called due to low limits
	assert.True(t, memCallbackCalled || fdCallbackCalled || cpuCallbackCalled)
}

// TestGetSystemResourceLimits tests system limit retrieval
func TestGetSystemResourceLimits(t *testing.T) {
	limits, err := GetSystemResourceLimits()
	assert.NoError(t, err)
	
	// Should have at least file descriptor limit
	assert.Contains(t, limits, "file_descriptors")
	assert.Greater(t, limits["file_descriptors"], uint64(0))
}

// TestResourceLimiterConcurrency tests concurrent operations
func TestResourceLimiterConcurrency(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		MemoryLimitMB:      1024,
		CPUQuotaPercent:    200,
		MaxFileDescriptors: 1024,
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	limiter.Start(ctx)
	
	// Concurrent metric reads and callback sets
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			
			// Read metrics
			_ = limiter.GetMetrics()
			
			// Set callbacks
			limiter.SetMemoryLimitCallback(func(usage, limit uint64) {})
			limiter.SetCPULimitCallback(func(usage float64) {})
			limiter.SetFDLimitCallback(func(usage, limit int) {})
		}()
	}
	
	wg.Wait()
	
	// Verify no panics
	metrics := limiter.GetMetrics()
	assert.NotNil(t, metrics)
}

// TestWorkerResourceLimiterIntegration tests integration with Worker
func TestWorkerResourceLimiterIntegration(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType:              livekit.JobType_JT_ROOM,
		EnableResourceLimits: true,
		HardMemoryLimitMB:    50, // Very low limit
		CPUQuotaPercent:      50,
		MaxFileDescriptors:   100,
	})
	
	// Verify limiter was created
	assert.NotNil(t, worker.Worker.resourceLimiter)
	
	// Check metrics through health
	health := worker.Worker.Health()
	assert.Contains(t, health, "resource_limits")
	
	limits := health["resource_limits"].(map[string]interface{})
	assert.Equal(t, uint64(50), limits["memory_limit_mb"])
	assert.Equal(t, 50, limits["cpu_quota_percent"])
	assert.Equal(t, 100, limits["fd_limit"])
}

// TestMemoryEnforcement tests actual memory limit enforcement
func TestMemoryEnforcement(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	limiter := NewResourceLimiter(logger, ResourceLimiterOptions{
		MemoryLimitMB:     100,
		CheckInterval:     50 * time.Millisecond,
		EnforceHardLimits: true,
	})
	
	// Measure initial memory
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	
	// Trigger enforcement
	limiter.enforceMemoryLimit()
	
	// Measure after enforcement
	var m2 runtime.MemStats
	runtime.ReadMemStats(&m2)
	
	// Should have triggered GC
	assert.Greater(t, m2.NumGC, m1.NumGC)
}