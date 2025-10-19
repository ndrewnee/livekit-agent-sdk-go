package agent

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestResourceMonitorOOMDetection tests OOM detection
func TestResourceMonitorOOMDetection(t *testing.T) {
	logger := zap.NewNop()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
		CheckInterval: 50 * time.Millisecond,
		MemoryLimitMB: 1, // Very low limit to trigger OOM
	})

	var mu sync.Mutex
	oomDetected := false
	monitor.SetOOMCallback(func() {
		mu.Lock()
		oomDetected = true
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	monitor.Start(ctx)

	// Allocate memory to trigger OOM
	_ = make([]byte, 2*1024*1024) // 2MB

	// Wait for detection
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	detected := oomDetected
	mu.Unlock()
	assert.True(t, detected)
	assert.False(t, monitor.IsHealthy())

	metrics := monitor.GetMetrics()
	assert.True(t, metrics["oom_detected"].(bool))

	monitor.Stop()
}

// TestResourceMonitorGoroutineLeakDetection tests goroutine leak detection
func TestResourceMonitorGoroutineLeakDetection(t *testing.T) {
	logger := zap.NewNop()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
		CheckInterval:          50 * time.Millisecond,
		GoroutineLimit:         runtime.NumGoroutine() + 10, // Low limit
		GoroutineLeakThreshold: 2,                           // Quick detection
	})

	var mu sync.Mutex
	leakDetected := false
	leakCount := 0
	monitor.SetLeakCallback(func(count int) {
		mu.Lock()
		leakDetected = true
		leakCount = count
		mu.Unlock()
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

	mu.Lock()
	detected := leakDetected
	count := leakCount
	mu.Unlock()
	assert.True(t, detected)
	assert.Greater(t, count, 0)

	metrics := monitor.GetMetrics()
	assert.True(t, metrics["leak_detected"].(bool))

	// Clean up goroutines
	close(stopLeak)
	time.Sleep(50 * time.Millisecond)

	monitor.Stop()
}

// TestResourceMonitorCircularDependency tests circular dependency detection
func TestResourceMonitorCircularDependency(t *testing.T) {
	logger := zap.NewNop()
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
	assert.Len(t, detectedCycle, 4)                     // C, A, B, C (cycle starts from where it was detected)
	assert.Equal(t, detectedCycle[0], detectedCycle[3]) // First and last should be the same to complete the cycle

	metrics := monitor.GetMetrics()
	assert.True(t, metrics["circular_dep_detected"].(bool))
}

// TestResourceMonitorHealthStatus tests resource health status
func TestResourceMonitorHealthStatus(t *testing.T) {
	logger := zap.NewNop()
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
	logger := zap.NewNop()
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
	logger := zap.NewNop()
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
	logger := zap.NewNop()
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

// TestResourceHealthLevels tests health level determination with LiveKit server
func TestResourceHealthLevels(t *testing.T) {
	logger := zap.NewNop()

	// Connect to local LiveKit server to create realistic resource usage
	manager := NewTestRoomManager()

	// Create a room to establish baseline resource usage
	room, err := manager.CreateRoom("resource-test-room")
	if err != nil {
		t.Skipf("LiveKit server not available: %v", err)
		return
	}
	defer room.Disconnect()

	// Get baseline resource usage with LiveKit connection
	var baselineMemStats runtime.MemStats
	runtime.ReadMemStats(&baselineMemStats)
	baselineMemoryMB := int(baselineMemStats.Alloc / 1024 / 1024)
	baselineGoroutines := runtime.NumGoroutine()

	t.Logf("Baseline with LiveKit: Memory=%dMB, Goroutines=%d", baselineMemoryMB, baselineGoroutines)

	testCases := []struct {
		name          string
		setupFunc     func() func() // Setup and return cleanup function
		memoryLimitMB int
		expectedLevel ResourceHealthLevel
		expectAtLeast bool // If true, expect at least this level (could be higher)
	}{
		{
			name: "Good - Normal LiveKit Operations",
			setupFunc: func() func() {
				// Normal operation with single room connection
				return func() {} // No additional cleanup needed
			},
			memoryLimitMB: baselineMemoryMB * 10, // 10x current usage
			expectedLevel: ResourceHealthGood,
		},
		{
			name: "Warning - High Memory Usage",
			setupFunc: func() func() {
				// Create multiple room connections to increase memory usage
				rooms := make([]*lksdk.Room, 0, 5)
				for i := 0; i < 5; i++ {
					r, err := manager.CreateRoom(fmt.Sprintf("resource-test-%d", i))
					if err == nil {
						rooms = append(rooms, r)
					}
				}

				// Also publish some tracks to increase load
				for _, r := range rooms {
					_, _ = NewSyntheticAudioTrack("test-audio", 48000, 2)
					manager.PublishSyntheticAudioTrack(r, "audio-track")
				}

				// Allocate some memory to push usage higher
				// This simulates a more memory-intensive agent operation
				largeData := make([]byte, 10*1024*1024) // 10MB
				for i := range largeData {
					largeData[i] = byte(i % 256)
				}

				return func() {
					for _, r := range rooms {
						r.Disconnect()
					}
					largeData = nil // Release memory
					runtime.GC()
				}
			},
			memoryLimitMB: 10, // Low limit to trigger warning (current usage will be > 80%)
			expectedLevel: ResourceHealthWarning,
			expectAtLeast: true, // Might be critical if memory is > 90%
		},
		{
			name: "Critical - Very Low Memory Limit with LiveKit Load",
			setupFunc: func() func() {
				// Create some LiveKit activity
				rooms := make([]*lksdk.Room, 0, 3)
				for i := 0; i < 3; i++ {
					r, err := manager.CreateRoom(fmt.Sprintf("critical-test-%d", i))
					if err == nil {
						rooms = append(rooms, r)
					}
				}

				return func() {
					for _, r := range rooms {
						r.Disconnect()
					}
				}
			},
			memoryLimitMB: 1, // Extremely low limit to force critical
			expectedLevel: ResourceHealthCritical,
		},
		{
			name: "Good - After Cleanup",
			setupFunc: func() func() {
				// Test that resources return to good after cleanup
				// First create load
				rooms := make([]*lksdk.Room, 0, 3)
				for i := 0; i < 3; i++ {
					r, err := manager.CreateRoom(fmt.Sprintf("cleanup-test-%d", i))
					if err == nil {
						rooms = append(rooms, r)
					}
				}

				// Clean up immediately
				for _, r := range rooms {
					r.Disconnect()
				}

				// Force garbage collection to free memory
				runtime.GC()
				runtime.Gosched()
				time.Sleep(100 * time.Millisecond)

				return func() {} // Already cleaned up
			},
			memoryLimitMB: baselineMemoryMB * 5,
			expectedLevel: ResourceHealthGood,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup test conditions
			cleanup := tc.setupFunc()
			defer cleanup()

			// Give time for resources to stabilize
			time.Sleep(50 * time.Millisecond)

			// Create monitor with specified limits
			monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
				MemoryLimitMB:  tc.memoryLimitMB,
				GoroutineLimit: 10000, // Default value
			})

			// Get resource status
			status := monitor.GetResourceStatus()

			// Log details for debugging
			t.Logf("Memory: %d MB / %d MB (%.1f%%)",
				status.MemoryUsageMB, status.MemoryLimitMB, status.MemoryPercent)
			t.Logf("Goroutines: %d / %d",
				status.GoroutineCount, status.GoroutineLimit)
			t.Logf("Health Level: %v (expected: %v)", status.HealthLevel, tc.expectedLevel)

			// Verify health level
			if tc.expectAtLeast {
				assert.True(t, status.HealthLevel >= tc.expectedLevel,
					"Expected at least %v, got %v", tc.expectedLevel, status.HealthLevel)
			} else {
				assert.Equal(t, tc.expectedLevel, status.HealthLevel)
			}
		})
	}

	// Additional test: Monitor resource changes during LiveKit operations
	t.Run("Resource Monitoring During Operations", func(t *testing.T) {
		monitor := NewResourceMonitor(logger, ResourceMonitorOptions{
			MemoryLimitMB:  baselineMemoryMB * 20,
			GoroutineLimit: 10000,
		})

		// Get initial status
		initialStatus := monitor.GetResourceStatus()
		assert.Equal(t, ResourceHealthGood, initialStatus.HealthLevel)

		// Create load
		rooms := make([]*lksdk.Room, 0, 10)
		for i := 0; i < 10; i++ {
			r, err := manager.CreateRoom(fmt.Sprintf("monitor-test-%d", i))
			if err == nil {
				rooms = append(rooms, r)
			}
		}

		// Check status under load
		loadStatus := monitor.GetResourceStatus()
		t.Logf("Under load - Memory: %dMB->%dMB, Goroutines: %d->%d",
			initialStatus.MemoryUsageMB, loadStatus.MemoryUsageMB,
			initialStatus.GoroutineCount, loadStatus.GoroutineCount)

		// Memory and goroutines should have increased
		assert.Greater(t, loadStatus.MemoryUsageMB, initialStatus.MemoryUsageMB)
		assert.Greater(t, loadStatus.GoroutineCount, initialStatus.GoroutineCount)

		// Clean up
		for _, r := range rooms {
			r.Disconnect()
		}

		// Force cleanup
		runtime.GC()
		time.Sleep(100 * time.Millisecond)

		// Check status after cleanup
		finalStatus := monitor.GetResourceStatus()
		t.Logf("After cleanup - Memory: %dMB, Goroutines: %d",
			finalStatus.MemoryUsageMB, finalStatus.GoroutineCount)

		// Should still be healthy
		assert.Equal(t, ResourceHealthGood, finalStatus.HealthLevel)
	})
}

// TestResourceGuardRetries tests retry logic in resource guard
func TestResourceGuardRetries(t *testing.T) {
	logger := zap.NewNop()
	monitor := NewResourceMonitor(logger, ResourceMonitorOptions{})
	guard := NewResourceGuard(monitor)
	guard.maxRetries = 3
	guard.backoffMs = 10

	// Start healthy
	monitor.mu.Lock()
	monitor.oomDetected = false
	monitor.mu.Unlock()

	attempts := 0
	checkCount := 0

	// Use a goroutine to simulate clearing OOM after some checks
	done := make(chan bool)
	go func() {
		for {
			select {
			case <-done:
				return
			default:
				time.Sleep(20 * time.Millisecond)
				monitor.mu.Lock()
				checkCount++
				if checkCount >= 2 {
					monitor.oomDetected = false
				}
				monitor.mu.Unlock()
			}
		}
	}()
	defer close(done)

	// Set OOM just before execution
	monitor.mu.Lock()
	monitor.oomDetected = true
	monitor.mu.Unlock()

	err := guard.ExecuteWithProtection(func() error {
		attempts++
		return nil
	})

	// The function should eventually execute when resources become healthy
	assert.NoError(t, err)
	assert.Equal(t, 1, attempts) // Function only executes once when healthy
}
