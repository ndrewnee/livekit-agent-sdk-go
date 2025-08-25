package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestShutdownHookManager tests basic hook management
func TestShutdownHookManager(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	// Test adding hooks
	hook1 := ShutdownHook{
		Name:     "test_hook_1",
		Priority: 100,
		Handler: func(ctx context.Context) error {
			return nil
		},
	}

	err := manager.AddHook(ShutdownPhasePreStop, hook1)
	assert.NoError(t, err)

	// Test getting hooks
	hooks := manager.GetHooks(ShutdownPhasePreStop)
	assert.Len(t, hooks, 1)
	assert.Equal(t, "test_hook_1", hooks[0].Name)

	// Test removing hooks
	removed := manager.RemoveHook(ShutdownPhasePreStop, "test_hook_1")
	assert.True(t, removed)

	hooks = manager.GetHooks(ShutdownPhasePreStop)
	assert.Len(t, hooks, 0)

	// Test removing non-existent hook
	removed = manager.RemoveHook(ShutdownPhasePreStop, "non_existent")
	assert.False(t, removed)
}

// TestShutdownHookPriority tests hook execution order
func TestShutdownHookPriority(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	var executionOrder []string

	// Add hooks with different priorities
	hooks := []ShutdownHook{
		{
			Name:     "hook_3",
			Priority: 300,
			Handler: func(ctx context.Context) error {
				executionOrder = append(executionOrder, "hook_3")
				return nil
			},
		},
		{
			Name:     "hook_1",
			Priority: 100,
			Handler: func(ctx context.Context) error {
				executionOrder = append(executionOrder, "hook_1")
				return nil
			},
		},
		{
			Name:     "hook_2",
			Priority: 200,
			Handler: func(ctx context.Context) error {
				executionOrder = append(executionOrder, "hook_2")
				return nil
			},
		},
	}

	for _, hook := range hooks {
		err := manager.AddHook(ShutdownPhaseCleanup, hook)
		assert.NoError(t, err)
	}

	// Execute phase
	ctx := context.Background()
	err := manager.ExecutePhase(ctx, ShutdownPhaseCleanup)
	assert.NoError(t, err)

	// Check execution order (lower priority numbers run first)
	assert.Equal(t, []string{"hook_1", "hook_2", "hook_3"}, executionOrder)
}

// TestShutdownHookTimeout tests hook timeout handling
func TestShutdownHookTimeout(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	hook := ShutdownHook{
		Name:    "timeout_hook",
		Timeout: 50 * time.Millisecond,
		Handler: func(ctx context.Context) error {
			time.Sleep(100 * time.Millisecond)
			return nil
		},
	}

	err := manager.AddHook(ShutdownPhaseStopJobs, hook)
	assert.NoError(t, err)

	ctx := context.Background()
	err = manager.ExecutePhase(ctx, ShutdownPhaseStopJobs)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "timed out")
	}
}

// TestShutdownHookError tests error handling
func TestShutdownHookError(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	testError := errors.New("hook error")

	hook := ShutdownHook{
		Name: "error_hook",
		Handler: func(ctx context.Context) error {
			return testError
		},
	}

	err := manager.AddHook(ShutdownPhasePostJobs, hook)
	assert.NoError(t, err)

	ctx := context.Background()
	err = manager.ExecutePhase(ctx, ShutdownPhasePostJobs)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "hook error")
	}
}

// TestShutdownHookPanic tests panic recovery
func TestShutdownHookPanic(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	hook := ShutdownHook{
		Name: "panic_hook",
		Handler: func(ctx context.Context) error {
			panic("test panic")
		},
	}

	err := manager.AddHook(ShutdownPhaseFinal, hook)
	assert.NoError(t, err)

	ctx := context.Background()
	err = manager.ExecutePhase(ctx, ShutdownPhaseFinal)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "hook panicked")
	}
}

// TestShutdownHookConcurrency tests concurrent operations
func TestShutdownHookConcurrency(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	// Add hooks concurrently
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(id int) {
			hook := ShutdownHook{
				Name:     string(rune('A' + id)),
				Priority: id * 10,
				Handler: func(ctx context.Context) error {
					return nil
				},
			}
			_ = manager.AddHook(ShutdownPhaseCleanup, hook)
			done <- struct{}{}
		}(i)
	}

	// Wait for all additions
	for i := 0; i < 10; i++ {
		<-done
	}

	// Check all hooks were added
	hooks := manager.GetHooks(ShutdownPhaseCleanup)
	assert.Len(t, hooks, 10)
}

// TestDefaultShutdownHooks tests default hooks
func TestDefaultShutdownHooks(t *testing.T) {
	defaults := DefaultShutdownHooks{}

	// Test metrics flush hook
	flushed := false
	metricsHook := defaults.NewMetricsFlushHook(func() error {
		flushed = true
		return nil
	})

	assert.Equal(t, "metrics_flush", metricsHook.Name)
	assert.Equal(t, 100, metricsHook.Priority)

	err := metricsHook.Handler(context.Background())
	assert.NoError(t, err)
	assert.True(t, flushed)

	// Test log flush hook
	logger, _ := zap.NewDevelopment()
	logHook := defaults.NewLogFlushHook(logger)
	assert.Equal(t, "log_flush", logHook.Name)
	assert.Equal(t, 200, logHook.Priority)

	// Test connection drain hook
	drained := false
	drainHook := defaults.NewConnectionDrainHook(func(ctx context.Context) error {
		drained = true
		return nil
	})

	assert.Equal(t, "connection_drain", drainHook.Name)
	assert.Equal(t, 50, drainHook.Priority)

	err = drainHook.Handler(context.Background())
	assert.NoError(t, err)
	assert.True(t, drained)

	// Test state backup hook
	backedUp := false
	backupHook := defaults.NewStateBackupHook(func() error {
		backedUp = true
		return nil
	})

	assert.Equal(t, "state_backup", backupHook.Name)
	assert.Equal(t, 75, backupHook.Priority)

	err = backupHook.Handler(context.Background())
	assert.NoError(t, err)
	assert.True(t, backedUp)
}

// TestShutdownHookBuilder tests the builder pattern
func TestShutdownHookBuilder(t *testing.T) {
	executed := false

	hook := NewShutdownHookBuilder("test_hook").
		WithPriority(50).
		WithTimeout(10 * time.Second).
		WithHandler(func(ctx context.Context) error {
			executed = true
			return nil
		}).
		Build()

	assert.Equal(t, "test_hook", hook.Name)
	assert.Equal(t, 50, hook.Priority)
	assert.Equal(t, 10*time.Second, hook.Timeout)

	err := hook.Handler(context.Background())
	assert.NoError(t, err)
	assert.True(t, executed)
}

// TestWorkerShutdownHooks tests shutdown hooks integration with worker
func TestWorkerShutdownHooks(t *testing.T) {
	handler := &MockUniversalHandler{}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	defer worker.Stop() // Ensure cleanup

	// Add custom hooks
	var hooksCalled int32

	// Pre-stop hook
	err := worker.AddPreStopHook("test_prestop", func(ctx context.Context) error {
		atomic.AddInt32(&hooksCalled, 1)
		return nil
	})
	assert.NoError(t, err)

	// Cleanup hook
	err = worker.AddCleanupHook("test_cleanup", func(ctx context.Context) error {
		atomic.AddInt32(&hooksCalled, 1)
		return nil
	})
	assert.NoError(t, err)

	// Custom phase hook
	err = worker.AddShutdownHook(ShutdownPhasePostJobs, ShutdownHook{
		Name: "test_postjobs",
		Handler: func(ctx context.Context) error {
			atomic.AddInt32(&hooksCalled, 1)
			return nil
		},
	})
	assert.NoError(t, err)

	// Verify hooks were added
	preStopHooks := worker.GetShutdownHooks(ShutdownPhasePreStop)
	assert.Len(t, preStopHooks, 1)

	cleanupHooks := worker.GetShutdownHooks(ShutdownPhaseCleanup)
	assert.Len(t, cleanupHooks, 1)

	postJobsHooks := worker.GetShutdownHooks(ShutdownPhasePostJobs)
	assert.Len(t, postJobsHooks, 1)

	// Remove a hook
	removed := worker.RemoveShutdownHook(ShutdownPhaseCleanup, "test_cleanup")
	assert.True(t, removed)

	cleanupHooks = worker.GetShutdownHooks(ShutdownPhaseCleanup)
	assert.Len(t, cleanupHooks, 0)
}

// TestMultipleHooksWithErrors tests multiple hooks where some fail
func TestMultipleHooksWithErrors(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	var executedHooks []string

	// Add multiple hooks, some that fail
	hooks := []ShutdownHook{
		{
			Name:     "hook_1",
			Priority: 1,
			Handler: func(ctx context.Context) error {
				executedHooks = append(executedHooks, "hook_1")
				return nil
			},
		},
		{
			Name:     "hook_2_fail",
			Priority: 2,
			Handler: func(ctx context.Context) error {
				executedHooks = append(executedHooks, "hook_2_fail")
				return errors.New("hook 2 error")
			},
		},
		{
			Name:     "hook_3",
			Priority: 3,
			Handler: func(ctx context.Context) error {
				executedHooks = append(executedHooks, "hook_3")
				return nil
			},
		},
	}

	for _, hook := range hooks {
		_ = manager.AddHook(ShutdownPhaseCleanup, hook)
	}

	// Execute phase - should continue despite error
	ctx := context.Background()
	err := manager.ExecutePhase(ctx, ShutdownPhaseCleanup)

	// Should return first error
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "hook 2 error")
	}

	// But all hooks should have executed
	assert.Equal(t, []string{"hook_1", "hook_2_fail", "hook_3"}, executedHooks)
}

// TestInvalidPhase tests adding hook to invalid phase
func TestInvalidPhase(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	hook := ShutdownHook{
		Name: "test",
		Handler: func(ctx context.Context) error {
			return nil
		},
	}

	err := manager.AddHook("invalid_phase", hook)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "invalid shutdown phase")
	}
}

// TestClearHooks tests clearing hooks
func TestClearHooks(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	manager := NewShutdownHookManager(logger)

	// Add hooks to multiple phases
	for _, phase := range []ShutdownPhase{ShutdownPhasePreStop, ShutdownPhaseCleanup, ShutdownPhaseFinal} {
		for i := 0; i < 3; i++ {
			hook := ShutdownHook{
				Name: string(rune('A' + i)),
				Handler: func(ctx context.Context) error {
					return nil
				},
			}
			_ = manager.AddHook(phase, hook)
		}
	}

	// Verify hooks were added
	assert.Equal(t, 9, manager.GetHookCount())

	// Clear specific phase
	manager.ClearHooks(ShutdownPhasePreStop)
	assert.Len(t, manager.GetHooks(ShutdownPhasePreStop), 0)
	assert.Equal(t, 6, manager.GetHookCount())

	// Clear all hooks
	manager.ClearAllHooks()
	assert.Equal(t, 0, manager.GetHookCount())
}
