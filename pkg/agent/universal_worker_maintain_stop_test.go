package agent

import (
	"context"
	"testing"
	"time"
)

func TestUniversalWorker_MaintainConnection_Stop(t *testing.T) {
	w := &UniversalWorker{
		opts:            WorkerOptions{PingInterval: time.Hour, PingTimeout: time.Second},
		logger:          NewDefaultLogger(),
		reconnectChan:   make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
		statusQueueChan: make(chan struct{}, 1),
		wsState:         WebSocketStateConnected,
	}
	ctx := context.Background()
	done := make(chan struct{})
	go func() { w.maintainConnection(ctx); close(done) }()
	// Immediately stop
	close(w.stopCh)
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("maintainConnection did not exit on stop")
	}
}
