package agent

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// Ensures adjustDeadlinesForSkew actually updates existing deadlines
func TestTimingManager_AdjustsExistingDeadlinesOnSkew(t *testing.T) {
	tm := NewTimingManager(zap.NewNop(), TimingManagerOptions{SkewThreshold: 5 * time.Millisecond})

	jobID := "skew-job"
	original := time.Now().Add(150 * time.Millisecond)
	tm.SetDeadline(jobID, original, "unit-test")

	// Capture adjusted deadline before skew change
	beforeCtx, ok := tm.GetDeadline(jobID)
	if !ok {
		t.Fatalf("expected deadline to be set")
	}
	before := beforeCtx.AdjustedDeadline

	// Introduce a positive server skew to trigger adjustment
	recv := time.Now()
	server := recv.Add(25 * time.Millisecond)
	tm.UpdateServerTime(server, recv)

	afterCtx, ok := tm.GetDeadline(jobID)
	if !ok {
		t.Fatalf("expected deadline to remain set")
	}
	after := afterCtx.AdjustedDeadline

	// Expect the adjusted deadline to shift forward by roughly the skew delta
	if after.Sub(before) < 20*time.Millisecond { // allow a small tolerance
		t.Fatalf("expected adjusted deadline to move forward, got diff=%v", after.Sub(before))
	}
}
