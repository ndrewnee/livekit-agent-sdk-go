package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestUniversalWorker_SendPing_ReconnectOnMissedPongs(t *testing.T) {
	w := &UniversalWorker{
		opts:            WorkerOptions{PingTimeout: 10 * time.Millisecond},
		reconnectChan:   make(chan struct{}, 1),
		logger:          NewDefaultLogger(),
		wsState:         WebSocketStateConnected,
		statusQueueChan: make(chan struct{}, 1),
	}

	// Simulate old lastPong to trigger missed pings
	w.mu.Lock()
	w.healthCheck.lastPong = time.Now().Add(-time.Second)
	w.mu.Unlock()

	// Call sendPing enough times to exceed threshold (missedPings > 3)
	for i := 0; i < 5; i++ {
		_ = w.sendPing()
	}

	// Should have marked as disconnected and queued a reconnect
	w.mu.Lock()
	state := w.wsState
	w.mu.Unlock()
	assert.Equal(t, WebSocketStateDisconnected, state)

	select {
	case <-w.reconnectChan:
		// ok
	default:
		t.Fatalf("expected reconnect signal")
	}

	// Also exercise queueStatusUpdate with nil conn via updateJobStatus path
	w.updateJobStatus("job", livekit.JobStatus_JS_RUNNING, "")
}
