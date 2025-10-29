package agent

import (
	"context"
	"testing"
)

func TestUniversalWorker_HandleMessages_Stop(t *testing.T) {
	w := &UniversalWorker{logger: NewDefaultLogger(), stopCh: make(chan struct{})}
	close(w.stopCh)
	w.handleMessages(context.Background())
}
