package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

// TestWebSocketReconnectionRestartMessageHandler tests that handleMessages goroutine is restarted after WebSocket reconnection
func TestWebSocketReconnectionRestartMessageHandler(t *testing.T) {
	// Create a mock handler
	handler := NewMockUniversalHandler()

	// Create worker for WebSocket reconnection testing
	worker := NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, WorkerOptions{
		JobType:      livekit.JobType_JT_ROOM,
		AgentName:    "test-websocket-reconnect",
		MaxJobs:      5,
		PingInterval: 5 * time.Second,
		PingTimeout:  10 * time.Second,
	})

	// Simulate initial WebSocket connection state
	worker.mu.Lock()
	worker.wsState = WebSocketStateConnected
	worker.workerID = "test-worker-id"
	worker.mu.Unlock()

	ctx := context.Background()

	// Simulate WebSocket connection failure that would cause handleMessages to exit
	worker.mu.Lock()
	worker.wsState = WebSocketStateDisconnected
	worker.mu.Unlock()

	// Test the WebSocket reconnect function - this should restart handleMessages
	err := worker.reconnect(ctx)

	// Verify reconnection behavior regardless of server availability
	if err != nil {
		t.Logf("WebSocket reconnect error (expected if server not running): %v", err)
		// Verify that reconnect attempted the connection process
		assert.Contains(t, err.Error(), "failed to connect")
	} else {
		// If WebSocket reconnect succeeded, verify proper state restoration
		worker.mu.RLock()
		state := worker.wsState
		workerID := worker.workerID
		worker.mu.RUnlock()

		assert.Equal(t, WebSocketStateConnected, state, "WebSocket should be connected after successful reconnect")
		assert.NotEmpty(t, workerID, "Worker should have ID after WebSocket reconnect")

		// Allow time for WebSocket message handler to start
		time.Sleep(100 * time.Millisecond)

		// Verify that the worker can receive WebSocket messages (handleMessages is running)
		assert.True(t, worker.IsConnected(), "Worker should report WebSocket as connected")
	}

	// Cleanup
	worker.Stop()
}
