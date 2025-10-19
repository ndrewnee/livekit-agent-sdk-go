package agent

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestTimingGuard_AppliesBackpressureDelay(t *testing.T) {
	// Configure a small window/limit to trigger minimal sleep
	tm := NewTimingManager(zap.NewNop(), TimingManagerOptions{
		BackpressureWindow: 20 * time.Millisecond,
		BackpressureLimit:  5,
	})

	jobID := "bp-job"
	// Ensure deadline won’t be exceeded
	tm.SetDeadline(jobID, time.Now().Add(1*time.Second), "unit-test")

	// Record > limit events within the window
	for i := 0; i < 6; i++ {
		tm.RecordEvent()
	}

	guard := tm.NewGuard(jobID, "op")
	start := time.Now()
	if err := guard.Execute(context.Background(), func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("unexpected error from guard.Execute: %v", err)
	}
	elapsed := time.Since(start)

	// Expect a small but non-zero delay due to backpressure (> ~4ms)
	if elapsed < 4*time.Millisecond {
		t.Fatalf("expected backpressure delay, got elapsed=%v", elapsed)
	}
}
