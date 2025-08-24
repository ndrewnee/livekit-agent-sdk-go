package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ShutdownHook represents a single shutdown hook that will be executed during worker shutdown.
// Hooks are organized by phase and priority, allowing for ordered cleanup operations.
// Each hook has a timeout to prevent hanging during shutdown.
type ShutdownHook struct {
	Name     string
	Priority int // Lower numbers run first
	Timeout  time.Duration
	Handler  func(context.Context) error
}

// ShutdownPhase represents different phases of the shutdown process, allowing hooks
// to be executed at appropriate times during worker termination. Each phase serves
// a specific purpose in the shutdown sequence.
type ShutdownPhase string

const (
	// ShutdownPhasePreStop is executed before any jobs are terminated.
	// Use this phase for preparation tasks like notifying external systems.
	ShutdownPhasePreStop ShutdownPhase = "pre_stop"

	// ShutdownPhaseStopJobs is executed during job termination.
	// Use this phase for job-specific cleanup that must happen during shutdown.
	ShutdownPhaseStopJobs ShutdownPhase = "stop_jobs"

	// ShutdownPhasePostJobs is executed after all jobs have completed or been terminated.
	// Use this phase for cleanup that depends on jobs being stopped.
	ShutdownPhasePostJobs ShutdownPhase = "post_jobs"

	// ShutdownPhaseCleanup is executed during general resource cleanup.
	// Use this phase for releasing resources like database connections.
	ShutdownPhaseCleanup ShutdownPhase = "cleanup"

	// ShutdownPhaseFinal is executed as the last step before worker termination.
	// Use this phase for final tasks like flushing logs or metrics.
	ShutdownPhaseFinal ShutdownPhase = "final"
)

// ShutdownHookManager manages registration and execution of shutdown hooks across all phases.
// It ensures hooks are executed in the correct order (by phase and priority) with proper
// timeout handling and error recovery. The manager is thread-safe and supports dynamic
// hook registration and removal.
type ShutdownHookManager struct {
	mu     sync.RWMutex
	logger *zap.Logger
	hooks  map[ShutdownPhase][]ShutdownHook
}

// NewShutdownHookManager creates a new shutdown hook manager with initialized phases.
// All shutdown phases are pre-configured with empty hook lists. The provided logger
// is used for debugging hook execution and reporting errors.
func NewShutdownHookManager(logger *zap.Logger) *ShutdownHookManager {
	return &ShutdownHookManager{
		logger: logger,
		hooks: map[ShutdownPhase][]ShutdownHook{
			ShutdownPhasePreStop:  {},
			ShutdownPhaseStopJobs: {},
			ShutdownPhasePostJobs: {},
			ShutdownPhaseCleanup:  {},
			ShutdownPhaseFinal:    {},
		},
	}
}

// AddHook registers a shutdown hook for execution in the specified phase.
// Hooks are automatically sorted by priority (lower numbers execute first).
// If no timeout is specified on the hook, a default 5-second timeout is applied.
// Returns an error if the phase is invalid.
func (m *ShutdownHookManager) AddHook(phase ShutdownPhase, hook ShutdownHook) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.hooks[phase]; !exists {
		return fmt.Errorf("invalid shutdown phase: %s", phase)
	}

	// Set default timeout if not specified
	if hook.Timeout == 0 {
		hook.Timeout = 5 * time.Second
	}

	m.hooks[phase] = append(m.hooks[phase], hook)

	// Sort hooks by priority
	m.sortHooks(phase)

	m.logger.Debug("Added shutdown hook",
		zap.String("phase", string(phase)),
		zap.String("name", hook.Name),
		zap.Int("priority", hook.Priority),
		zap.Duration("timeout", hook.Timeout),
	)

	return nil
}

// sortHooks sorts hooks by priority (lower numbers first)
func (m *ShutdownHookManager) sortHooks(phase ShutdownPhase) {
	hooks := m.hooks[phase]
	for i := 0; i < len(hooks); i++ {
		for j := i + 1; j < len(hooks); j++ {
			if hooks[i].Priority > hooks[j].Priority {
				hooks[i], hooks[j] = hooks[j], hooks[i]
			}
		}
	}
}

// ExecutePhase executes all registered hooks for the specified shutdown phase in priority order.
// Hooks are executed sequentially (not in parallel) to maintain ordering guarantees.
// If a hook fails or times out, execution continues with remaining hooks, but the first
// error is returned. Each hook runs with its individual timeout context.
func (m *ShutdownHookManager) ExecutePhase(ctx context.Context, phase ShutdownPhase) error {
	m.mu.RLock()
	hooks := make([]ShutdownHook, len(m.hooks[phase]))
	copy(hooks, m.hooks[phase])
	m.mu.RUnlock()

	if len(hooks) == 0 {
		return nil
	}

	m.logger.Info("Executing shutdown hooks", zap.String("phase", string(phase)), zap.Int("count", len(hooks)))

	var firstError error
	for _, hook := range hooks {
		if err := m.executeHook(ctx, hook); err != nil {
			m.logger.Error("Shutdown hook failed",
				zap.String("phase", string(phase)),
				zap.String("name", hook.Name),
				zap.Error(err),
			)
			if firstError == nil {
				firstError = err
			}
			// Continue executing other hooks even if one fails
		}
	}

	return firstError
}

// executeHook executes a single hook with timeout
func (m *ShutdownHookManager) executeHook(ctx context.Context, hook ShutdownHook) error {
	// Create timeout context for this hook
	hookCtx, cancel := context.WithTimeout(ctx, hook.Timeout)
	defer cancel()

	// Channel to receive result
	done := make(chan error, 1)

	// Execute hook in goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("hook panicked: %v", r)
			}
		}()
		done <- hook.Handler(hookCtx)
	}()

	// Wait for completion or timeout
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("hook %s failed: %w", hook.Name, err)
		}
		m.logger.Debug("Shutdown hook completed", zap.String("name", hook.Name))
		return nil
	case <-hookCtx.Done():
		return fmt.Errorf("hook %s timed out after %v", hook.Name, hook.Timeout)
	}
}

// RemoveHook removes a specific shutdown hook identified by phase and name.
// Returns true if the hook was found and removed, false if no matching hook exists.
// This is useful for dynamic hook management or cleanup of temporary hooks.
func (m *ShutdownHookManager) RemoveHook(phase ShutdownPhase, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	hooks, exists := m.hooks[phase]
	if !exists {
		return false
	}

	for i, hook := range hooks {
		if hook.Name == name {
			// Remove hook
			m.hooks[phase] = append(hooks[:i], hooks[i+1:]...)
			m.logger.Debug("Removed shutdown hook", zap.String("phase", string(phase)), zap.String("name", name))
			return true
		}
	}

	return false
}

// GetHooks returns a copy of all hooks registered for the specified phase.
// The returned slice is independent and can be safely modified without affecting
// the internal hook registry. Hooks are returned in priority order.
func (m *ShutdownHookManager) GetHooks(phase ShutdownPhase) []ShutdownHook {
	m.mu.RLock()
	defer m.mu.RUnlock()

	hooks := make([]ShutdownHook, len(m.hooks[phase]))
	copy(hooks, m.hooks[phase])
	return hooks
}

// ClearHooks removes all hooks registered for the specified phase.
// This is useful for cleanup or resetting hook registration for a phase.
func (m *ShutdownHookManager) ClearHooks(phase ShutdownPhase) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.hooks[phase]; exists {
		m.hooks[phase] = []ShutdownHook{}
		m.logger.Debug("Cleared shutdown hooks", zap.String("phase", string(phase)))
	}
}

// ClearAllHooks removes all registered hooks from all phases.
// This completely resets the hook manager to its initial empty state.
func (m *ShutdownHookManager) ClearAllHooks() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for phase := range m.hooks {
		m.hooks[phase] = []ShutdownHook{}
	}
	m.logger.Debug("Cleared all shutdown hooks")
}

// GetHookCount returns the total number of registered hooks across all phases.
// This is useful for monitoring and debugging hook registration.
func (m *ShutdownHookManager) GetHookCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, hooks := range m.hooks {
		count += len(hooks)
	}
	return count
}

// DefaultShutdownHooks provides factory methods for creating commonly needed shutdown hooks.
// These hooks handle standard cleanup tasks like flushing metrics, logs, draining connections,
// and backing up state. Each factory method returns a pre-configured ShutdownHook with
// appropriate priorities and timeouts.
type DefaultShutdownHooks struct{}

// NewMetricsFlushHook creates a shutdown hook that flushes metrics before termination.
// This hook has priority 100 and a 3-second timeout. It's typically used in the
// final shutdown phase to ensure metrics are sent to external systems.
func (d DefaultShutdownHooks) NewMetricsFlushHook(flush func() error) ShutdownHook {
	return ShutdownHook{
		Name:     "metrics_flush",
		Priority: 100,
		Timeout:  3 * time.Second,
		Handler: func(ctx context.Context) error {
			return flush()
		},
	}
}

// NewLogFlushHook creates a shutdown hook that flushes the logger's buffer before termination.
// This hook has priority 200 and a 2-second timeout. It ensures all pending log messages
// are written to their destinations before the worker shuts down.
func (d DefaultShutdownHooks) NewLogFlushHook(logger *zap.Logger) ShutdownHook {
	return ShutdownHook{
		Name:     "log_flush",
		Priority: 200,
		Timeout:  2 * time.Second,
		Handler: func(ctx context.Context) error {
			err := logger.Sync()
			// Ignore errors about syncing stderr/stdout in test environments
			if err != nil && strings.Contains(err.Error(), "/dev/stderr") {
				return nil
			}
			if err != nil && strings.Contains(err.Error(), "/dev/stdout") {
				return nil
			}
			return err
		},
	}
}

// NewConnectionDrainHook creates a shutdown hook that drains active connections gracefully.
// This hook has priority 50 and a 10-second timeout. It's typically used early in shutdown
// to allow clients to finish ongoing operations before forced termination.
func (d DefaultShutdownHooks) NewConnectionDrainHook(drain func(context.Context) error) ShutdownHook {
	return ShutdownHook{
		Name:     "connection_drain",
		Priority: 50,
		Timeout:  10 * time.Second,
		Handler:  drain,
	}
}

// NewStateBackupHook creates a shutdown hook that backs up application state before termination.
// This hook has priority 75 and a 5-second timeout. It's useful for persisting important
// state information that can be recovered when the worker restarts.
func (d DefaultShutdownHooks) NewStateBackupHook(backup func() error) ShutdownHook {
	return ShutdownHook{
		Name:     "state_backup",
		Priority: 75,
		Timeout:  5 * time.Second,
		Handler: func(ctx context.Context) error {
			return backup()
		},
	}
}

// ShutdownHookBuilder provides a fluent API for constructing custom shutdown hooks.
// It allows for readable, chainable configuration of hook properties like priority,
// timeout, and handler function. Use NewShutdownHookBuilder to create a new builder.
type ShutdownHookBuilder struct {
	hook ShutdownHook
}

// NewShutdownHookBuilder creates a new shutdown hook builder with default values.
// The hook is initialized with priority 100 and 5-second timeout. Use the fluent
// methods to customize these values and set the handler function.
func NewShutdownHookBuilder(name string) *ShutdownHookBuilder {
	return &ShutdownHookBuilder{
		hook: ShutdownHook{
			Name:     name,
			Priority: 100,
			Timeout:  5 * time.Second,
		},
	}
}

// WithPriority sets the hook priority (lower numbers execute first).
// Returns the builder for method chaining.
func (b *ShutdownHookBuilder) WithPriority(priority int) *ShutdownHookBuilder {
	b.hook.Priority = priority
	return b
}

// WithTimeout sets the maximum execution time for the hook.
// Returns the builder for method chaining.
func (b *ShutdownHookBuilder) WithTimeout(timeout time.Duration) *ShutdownHookBuilder {
	b.hook.Timeout = timeout
	return b
}

// WithHandler sets the function to be executed when the hook runs.
// The handler receives a context that will be cancelled if the timeout is exceeded.
// Returns the builder for method chaining.
func (b *ShutdownHookBuilder) WithHandler(handler func(context.Context) error) *ShutdownHookBuilder {
	b.hook.Handler = handler
	return b
}

// Build returns the fully constructed ShutdownHook with all configured properties.
// This is the final step in the builder chain.
func (b *ShutdownHookBuilder) Build() ShutdownHook {
	return b.hook
}
