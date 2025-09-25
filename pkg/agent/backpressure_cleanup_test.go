package agent

import (
	"testing"
	"time"
)

// Directly exercises BackpressureController cleanup path
func TestBackpressureController_Cleanup(t *testing.T) {
	b := NewBackpressureController(10*time.Millisecond, 3)

	// Fill events
	for i := 0; i < 3; i++ {
		b.RecordEvent()
	}

	// Deterministically trigger cleanup without sleeping by invoking it
	// with a synthetic "now" that advances beyond the window.
	future := time.Now().Add(b.window + time.Millisecond)
	b.mu.Lock()
	b.cleanup(future)
	b.mu.Unlock()

	// Record one fresh event after cleanup; current rate should reflect it.
	b.RecordEvent()

	if b.GetCurrentRate() < 1 {
		t.Fatalf("expected rate >= 1 after cleanup, got %v", b.GetCurrentRate())
	}
}
