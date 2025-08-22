package agent

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestResourceMonitorOOMDetection tests OOM detection
func TestResourceMonitorOOMDetection(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
		CheckInterval: 50 * time.Millisecond,
		MemoryLimitMB: 1, // Very low limit to trigger OOM
	})
	
	oomDetected := false
	monitor.SetOOMCallback(func() {
		oomDetected = true
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	monitor.Start(ctx)
	
	// Allocate memory to trigger OOM
	_ = make([]byte, 2*1024*1024) // 2MB
	
	// Wait for detection
	time.Sleep(100 * time.Millisecond)
	
	assert.True(t, oomDetected)
	assert.False(t, monitor.IsHealthy())
	
	metrics := monitor.GetMetrics()
	assert.True(t, metrics["oom_detected"].(bool))
	
	monitor.Stop()
}

// TestResourceMonitorGoroutineLeakDetection tests goroutine leak detection
func TestResourceMonitorGoroutineLeakDetection(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
		CheckInterval:          50 * time.Millisecond,
		GoroutineLimit:         runtime.NumGoroutine() + 10, // Low limit
		GoroutineLeakThreshold: 2,                           // Quick detection
	})
	
	leakDetected := false
	leakCount := 0
	monitor.SetLeakCallback(func(count int) {
		leakDetected = true
		leakCount = count
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	monitor.Start(ctx)
	
	// Create goroutine leak
	stopLeak := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func() {
			<-stopLeak
		}()
	}
	
	// Wait for detection
	time.Sleep(200 * time.Millisecond)
	
	assert.True(t, leakDetected)
	assert.Greater(t, leakCount, 0)
	
	metrics := monitor.GetMetrics()
	assert.True(t, metrics["leak_detected"].(bool))
	
	// Clean up goroutines
	close(stopLeak)
	time.Sleep(50 * time.Millisecond)
	
	monitor.Stop()
}

// TestResourceMonitorCircularDependency tests circular dependency detection
func TestResourceMonitorCircularDependency(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{})
	
	circularDetected := false
	var detectedCycle []string
	monitor.SetCircularDependencyCallback(func(deps []string) {
		circularDetected = true
		detectedCycle = deps
	})
	
	// Create circular dependency
	monitor.AddDependency("A", "B")
	monitor.AddDependency("B", "C")
	monitor.AddDependency("C", "A") // Creates cycle A->B->C->A
	
	assert.True(t, circularDetected)
	assert.Len(t, detectedCycle, 4) // A, B, C, A
	assert.Equal(t, "A", detectedCycle[0])
	assert.Equal(t, "A", detectedCycle[3])
	
	metrics := monitor.GetMetrics()
	assert.True(t, metrics["circular_dep_detected"].(bool))
}

// TestResourceMonitorHealthStatus tests resource health status
func TestResourceMonitorHealthStatus(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
		MemoryLimitMB: 10000, // High limit to avoid OOM
	})
	
	// Initially healthy
	assert.True(t, monitor.IsHealthy())
	
	status := monitor.GetResourceStatus()
	assert.Equal(t, ResourceHealthGood, status.HealthLevel)
	assert.False(t, status.OOMDetected)
	assert.False(t, status.LeakDetected)
	assert.False(t, status.CircularDepDetected)
	
	// Simulate issues
	monitor.mu.Lock()
	monitor.oomDetected = true
	monitor.mu.Unlock()
	
	assert.False(t, monitor.IsHealthy())
	status = monitor.GetResourceStatus()
	assert.Equal(t, ResourceHealthCritical, status.HealthLevel)
}

// TestResourceGuard tests resource protection
func TestResourceGuard(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{})
	guard := NewResourceGuard(monitor)
	
	// Normal execution
	executed := false
	err := guard.ExecuteWithProtection(func() error {
		executed = true
		return nil
	})
	
	assert.NoError(t, err)
	assert.True(t, executed)
	
	// Execution with OOM
	monitor.mu.Lock()
	monitor.oomDetected = true
	monitor.mu.Unlock()
	
	guard.abortOnOOM = true
	executed = false
	err = guard.ExecuteWithProtection(func() error {
		executed = true
		return nil
	})
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "OOM detected")
	assert.False(t, executed)
}

// TestResourceMonitorMetrics tests metrics collection
func TestResourceMonitorMetrics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{})
	
	metrics := monitor.GetMetrics()
	
	// Check required metrics exist
	assert.Contains(t, metrics, "memory_alloc_mb")
	assert.Contains(t, metrics, "memory_sys_mb")
	assert.Contains(t, metrics, "memory_limit_mb")
	assert.Contains(t, metrics, "goroutine_count")
	assert.Contains(t, metrics, "goroutine_limit")
	assert.Contains(t, metrics, "oom_detected")
	assert.Contains(t, metrics, "leak_detected")
	assert.Contains(t, metrics, "circular_dep_detected")
	assert.Contains(t, metrics, "gc_runs")
	assert.Contains(t, metrics, "gc_pause_ms")
	
	// Check types
	assert.IsType(t, uint64(0), metrics["memory_alloc_mb"])
	assert.IsType(t, int(0), metrics["goroutine_count"])
	assert.IsType(t, false, metrics["oom_detected"])
}

// TestResourceMonitorConcurrency tests concurrent operations
func TestResourceMonitorConcurrency(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
		CheckInterval: 10 * time.Millisecond,
	})
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	monitor.Start(ctx)
	
	// Concurrent operations
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			
			// Add dependencies
			monitor.AddDependency(string(rune('A'+id)), string(rune('B'+id)))
			
			// Get metrics
			_ = monitor.GetMetrics()
			
			// Check health
			_ = monitor.IsHealthy()
			
			// Get status
			_ = monitor.GetResourceStatus()
		}(i)
	}
	
	wg.Wait()
	monitor.Stop()
}

// TestResourceHealthLevels tests health level determination
func TestResourceHealthLevels(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	
	testCases := []struct {
		name           string
		memoryPercent  float64
		goroutineCount int
		expectedLevel  ResourceHealthLevel
	}{
		{"Good", 50, 1000, ResourceHealthGood},
		{"Warning - Memory", 85, 1000, ResourceHealthWarning},
		{"Warning - Goroutines", 50, 6000, ResourceHealthWarning},
		{"Critical - Memory", 95, 1000, ResourceHealthCritical},
		{"Critical - Goroutines", 50, 9000, ResourceHealthCritical},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
				MemoryLimitMB: 100,
			})
			
			// Mock metrics
			monitor.mu.Lock()
			monitor.lastMemory = uint64(tc.memoryPercent) * 1024 * 1024
			monitor.lastGoroutineCount = tc.goroutineCount
			monitor.mu.Unlock()
			
			status := monitor.GetResourceStatus()
			assert.Equal(t, tc.expectedLevel, status.HealthLevel)
		})
	}
}

// TestResourceGuardRetries tests retry logic in resource guard
func TestResourceGuardRetries(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{})
	guard := NewResourceGuard(monitor)
	guard.maxRetries = 3
	guard.backoffMs = 10
	
	// Simulate temporary resource constraint
	monitor.mu.Lock()
	monitor.oomDetected = true
	monitor.mu.Unlock()
	
	attempts := 0
	err := guard.ExecuteWithProtection(func() error {
		attempts++
		// Clear OOM on second attempt
		if attempts == 2 {
			monitor.mu.Lock()
			monitor.oomDetected = false
			monitor.mu.Unlock()
		}
		return nil
	})
	
	assert.NoError(t, err)
	assert.Equal(t, 2, attempts)
}