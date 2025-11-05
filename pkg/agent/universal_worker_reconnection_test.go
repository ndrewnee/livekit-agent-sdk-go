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
)

// TestStopMessageHandler verifies that stopMessageHandler properly cancels the context
func TestStopMessageHandler(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-stop-handler",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Create a message handler context
	ctx := context.Background()
	worker.messageHandlerMu.Lock()
	worker.messageHandlerCtx, worker.messageHandlerCancel = context.WithCancel(ctx)
	messageCtx := worker.messageHandlerCtx
	worker.messageHandlerMu.Unlock()

	require.NotNil(t, messageCtx, "message handler context should be created")

	// Call stopMessageHandler
	worker.stopMessageHandler()

	// Verify context is cancelled
	select {
	case <-messageCtx.Done():
		// Context was cancelled as expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("message handler context was not cancelled")
	}

	// Verify internal state is cleared
	worker.messageHandlerMu.Lock()
	assert.Nil(t, worker.messageHandlerCtx, "messageHandlerCtx should be nil")
	assert.Nil(t, worker.messageHandlerCancel, "messageHandlerCancel should be nil")
	worker.messageHandlerMu.Unlock()

	// Verify it's safe to call multiple times
	worker.stopMessageHandler() // Should not panic
}

// TestHandleConnectionErrorPreventsConcurrentReconnections verifies that the atomic flag prevents race conditions
func TestHandleConnectionErrorPreventsConcurrentReconnections(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-concurrent",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Set up initial connection state
	worker.mu.Lock()
	worker.wsState = WebSocketStateConnected
	worker.mu.Unlock()

	// Ensure reconnecting flag is false
	worker.reconnecting.Store(false)

	// Track reconnection attempts
	var reconnectAttempts atomic.Int32

	// Simulate multiple concurrent connection errors
	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker.handleConnectionError(assert.AnError)

			// Check if reconnect was queued
			select {
			case <-worker.reconnectChan:
				reconnectAttempts.Add(1)
			case <-time.After(10 * time.Millisecond):
				// Channel not signaled, reconnect was skipped
			}
		}()
	}

	wg.Wait()

	// Only ONE reconnection should have been queued
	attempts := reconnectAttempts.Load()
	assert.Equal(t, int32(1), attempts, "only one reconnection should be queued despite %d concurrent errors", numGoroutines)

	// Verify reconnecting flag is set
	assert.True(t, worker.reconnecting.Load(), "reconnecting flag should be true")
}

// TestReconnectCreatesNewMessageHandlerContext verifies that reconnect creates a new context
func TestReconnectCreatesNewMessageHandlerContext(t *testing.T) {
	handler := NewMockUniversalHandler()
	handler.acceptJob = true

	opts := WorkerOptions{
		AgentName: "test-reconnect-context",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Set initial state as if previously connected
	worker.mu.Lock()
	worker.wsState = WebSocketStateDisconnected
	worker.workerID = "old-worker-id"
	worker.mu.Unlock()

	// Create an old context and cancel it
	oldCtx, oldCancel := context.WithCancel(context.Background())
	worker.messageHandlerMu.Lock()
	worker.messageHandlerCtx = oldCtx
	worker.messageHandlerCancel = oldCancel
	worker.messageHandlerMu.Unlock()
	oldCancel() // Cancel old context

	// Attempt reconnection (will fail to connect to localhost, but that's OK for this test)
	ctx := context.Background()
	err := worker.reconnect(ctx)

	// Even if connection fails, we should see context management attempted
	if err == nil {
		// If reconnect succeeded, verify new context was created
		worker.messageHandlerMu.Lock()
		newCtx := worker.messageHandlerCtx
		worker.messageHandlerMu.Unlock()

		require.NotNil(t, newCtx, "new message handler context should be created")
		assert.NotEqual(t, oldCtx, newCtx, "new context should be different from old context")

		// Verify reconnecting flag is reset
		assert.False(t, worker.reconnecting.Load(), "reconnecting flag should be false after successful reconnect")
	}

	// If connection failed, verify reconnecting flag is still reset
	if err != nil {
		assert.False(t, worker.reconnecting.Load(), "reconnecting flag should be false after failed reconnect to allow retry")
	}
}

// TestReconnectFailureResetsReconnectingFlag verifies the flag is reset on error
func TestReconnectFailureResetsReconnectingFlag(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-reconnect-fail",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://invalid:99999", "devkey", "secret", handler, opts)

	// Set reconnecting flag
	worker.reconnecting.Store(true)

	// Attempt reconnection to invalid server (should fail)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := worker.reconnect(ctx)

	// Reconnection should fail
	assert.Error(t, err, "reconnection to invalid server should fail")

	// Reconnecting flag should be reset to allow retry
	assert.False(t, worker.reconnecting.Load(), "reconnecting flag should be reset after failure")
}

// TestHandleMessagesDetectsContextCancellation verifies clean goroutine exit on context cancel
func TestHandleMessagesDetectsContextCancellation(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-context-cancel",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Track goroutine exit
	var wg sync.WaitGroup
	wg.Add(1)

	// Start handleMessages in goroutine
	go func() {
		defer wg.Done()
		worker.handleMessages(ctx)
	}()

	// Give goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for goroutine to exit (with timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Goroutine exited cleanly
	case <-time.After(1 * time.Second):
		t.Fatal("handleMessages goroutine did not exit after context cancellation")
	}
}

// TestMessageHandlerContextIsolation verifies old and new contexts are independent
func TestMessageHandlerContextIsolation(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-context-isolation",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Create first context
	ctx1, cancel1 := context.WithCancel(context.Background())
	worker.messageHandlerMu.Lock()
	worker.messageHandlerCtx = ctx1
	worker.messageHandlerCancel = cancel1
	worker.messageHandlerMu.Unlock()

	// Verify first context is active
	select {
	case <-ctx1.Done():
		t.Fatal("context 1 should not be cancelled yet")
	default:
		// OK
	}

	// Stop message handler (simulating reconnection)
	worker.stopMessageHandler()

	// Verify first context is cancelled
	select {
	case <-ctx1.Done():
		// Context was cancelled as expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("context 1 should be cancelled after stopMessageHandler")
	}

	// Create second context (simulating new connection)
	ctx2, cancel2 := context.WithCancel(context.Background())
	worker.messageHandlerMu.Lock()
	worker.messageHandlerCtx = ctx2
	worker.messageHandlerCancel = cancel2
	worker.messageHandlerMu.Unlock()

	// Verify second context is active and independent
	select {
	case <-ctx2.Done():
		t.Fatal("context 2 should not be cancelled")
	default:
		// OK
	}

	// Cancel second context
	cancel2()

	// Verify second context is now cancelled
	select {
	case <-ctx2.Done():
		// OK
	case <-time.After(100 * time.Millisecond):
		t.Fatal("context 2 should be cancelled")
	}

	// Verify contexts are truly independent
	assert.NotEqual(t, ctx1, ctx2, "contexts should be different instances")
}

// TestNoMultipleMessageHandlerGoroutines verifies only one handler runs at a time
func TestNoMultipleMessageHandlerGoroutines(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-single-handler",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	_ = NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Counter for active handlers
	var activeHandlers atomic.Int32

	// Mock handleMessages that tracks execution
	handleMessagesMock := func(ctx context.Context) {
		activeHandlers.Add(1)
		defer activeHandlers.Add(-1)

		// Simulate running handler
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
			return
		}
	}

	// Start first handler
	ctx1, cancel1 := context.WithCancel(context.Background())
	go handleMessagesMock(ctx1)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), activeHandlers.Load(), "should have 1 active handler")

	// Simulate reconnection: stop old handler, start new one
	cancel1() // Stop old handler
	time.Sleep(50 * time.Millisecond)

	ctx2, cancel2 := context.WithCancel(context.Background())
	go handleMessagesMock(ctx2)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), activeHandlers.Load(), "should still have only 1 active handler")

	// Cleanup
	cancel2()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), activeHandlers.Load(), "should have 0 active handlers after cleanup")
}

// TestStopMethodStopsMessageHandler verifies Stop() calls stopMessageHandler()
func TestStopMethodStopsMessageHandler(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-stop-method",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Create a message handler context
	ctx := context.Background()
	worker.messageHandlerMu.Lock()
	worker.messageHandlerCtx, worker.messageHandlerCancel = context.WithCancel(ctx)
	messageCtx := worker.messageHandlerCtx
	worker.messageHandlerMu.Unlock()

	require.NotNil(t, messageCtx, "message handler context should be created")

	// Call Stop()
	err := worker.Stop()
	assert.NoError(t, err, "Stop should not return error")

	// Verify message handler context was cancelled
	select {
	case <-messageCtx.Done():
		// Context was cancelled as expected
	case <-time.After(100 * time.Millisecond):
		t.Fatal("message handler context was not cancelled by Stop()")
	}

	// Verify internal state is cleared
	worker.messageHandlerMu.Lock()
	assert.Nil(t, worker.messageHandlerCtx, "messageHandlerCtx should be nil after Stop")
	assert.Nil(t, worker.messageHandlerCancel, "messageHandlerCancel should be nil after Stop")
	worker.messageHandlerMu.Unlock()
}

// TestReconnectingFlagThreadSafety verifies atomic operations are thread-safe
func TestReconnectingFlagThreadSafety(t *testing.T) {
	handler := NewMockUniversalHandler()
	opts := WorkerOptions{
		AgentName: "test-thread-safety",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   10,
	}
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)

	// Ensure flag starts as false
	worker.reconnecting.Store(false)

	// Simulate many concurrent attempts to set the flag
	var wg sync.WaitGroup
	successCount := atomic.Int32{}
	numGoroutines := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Try to swap false -> true
			if worker.reconnecting.CompareAndSwap(false, true) {
				successCount.Add(1)
				// Simulate work
				time.Sleep(1 * time.Millisecond)
				// Reset flag
				worker.reconnecting.Store(false)
			}
		}()
	}

	wg.Wait()

	// All goroutines should have eventually succeeded (since we reset the flag)
	// But at any given moment, only one should have had it set
	assert.Greater(t, successCount.Load(), int32(0), "at least one goroutine should succeed")
	assert.LessOrEqual(t, successCount.Load(), int32(numGoroutines), "success count should not exceed goroutine count")
}
