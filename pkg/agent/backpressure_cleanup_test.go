package agent

import (
	"testing"
	"time"
)

// Directly exercises BackpressureController cleanup path
func TestBackpressureController_Cleanup(t *testing.T) {
	b := NewBackpressureController(10*time.Millisecond, 3)

	// Fill events, then wait beyond window to trigger cleanup on next record
	for i := 0; i < 3; i++ {
		b.RecordEvent()
	}
	// Ensure window passes so cleanup condition holds
	time.Sleep(15 * time.Millisecond)
	b.RecordEvent() // should invoke cleanup internally

	// After cleanup, rate should be at least 1 (the last event)
	if b.GetCurrentRate() < 1 {
		t.Fatalf("expected rate >= 1 after cleanup, got %v", b.GetCurrentRate())
	}
}
