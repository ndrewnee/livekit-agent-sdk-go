package agent

import (
	"testing"
	"time"
)

func TestUniversalWorker_StopWithTimeout_SucceedsWhenDoneClosed(t *testing.T) {
	w := &UniversalWorker{
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	// Simulate worker already finished
	close(w.doneCh)

	// Should return immediately without error
	if err := w.StopWithTimeout(50 * time.Millisecond); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}
