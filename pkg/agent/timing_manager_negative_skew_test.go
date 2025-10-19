package agent

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// Verifies negative skew path (abs of negative duration) and deadline adjustment backwards
func TestTimingManager_NegativeSkewAdjustsDeadlinesBackward(t *testing.T) {
	tm := NewTimingManager(zap.NewNop(), TimingManagerOptions{SkewThreshold: 5 * time.Millisecond})

	jobID := "neg-skew"
	deadline := time.Now().Add(300 * time.Millisecond)
	tm.SetDeadline(jobID, deadline, "test")

	beforeCtx, ok := tm.GetDeadline(jobID)
	if !ok {
		t.Fatalf("deadline not set")
	}
	before := beforeCtx.AdjustedDeadline

	// Negative skew: server behind local by 30ms
	recv := time.Now()
	server := recv.Add(-30 * time.Millisecond)
	tm.UpdateServerTime(server, recv)

	afterCtx, ok := tm.GetDeadline(jobID)
	if !ok {
		t.Fatalf("deadline missing")
	}
	after := afterCtx.AdjustedDeadline

	// Expect deadline to move earlier (backward) by roughly the adjustment
	if before.Sub(after) < 20*time.Millisecond { // allow some tolerance
		t.Fatalf("expected deadline to move earlier, diff=%v", before.Sub(after))
	}
}
