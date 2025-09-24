package agent

import (
	"context"
	"testing"
)

func TestUniversalWorker_HandleMessages_Cancelled(t *testing.T) {
	w := &UniversalWorker{logger: NewDefaultLogger(), stopCh: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Should return immediately without panic
	w.handleMessages(ctx)
}
