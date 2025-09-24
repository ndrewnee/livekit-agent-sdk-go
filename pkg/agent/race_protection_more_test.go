package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.uber.org/zap"
)

func TestRaceProtector_Flows(t *testing.T) {
	rp := NewRaceProtector(zap.NewNop())

	// Disconnecting rejects jobs
	rp.SetDisconnecting(true)
	ok, reason := rp.CanAcceptJob("j1")
	if ok || reason == "" {
		t.Fatalf("expected reject during disconnect")
	}
	rp.SetDisconnecting(false)

	// Termination requests
	first := rp.RecordTerminationRequest("jobT")
	if !first {
		t.Fatalf("first termination should proceed")
	}
	// Concurrent request increments count and returns false
	_ = rp.RecordTerminationRequest("jobT")

	// Complete termination
	rp.CompleteTermination("jobT", nil)
	// Now CanAcceptJob should allow
	ok, _ = rp.CanAcceptJob("jobT")
	if !ok {
		t.Fatalf("expected accept after completed termination")
	}

	// Queue status updates while reconnecting
	rp.SetReconnecting(true)
	queued := rp.QueueStatusUpdate("j2", livekit.JobStatus_JS_RUNNING, "")
	if !queued {
		t.Fatalf("expected queued")
	}
	// Replace with more severe status
	_ = rp.QueueStatusUpdate("j2", livekit.JobStatus_JS_FAILED, "err")
	// Flush
	updates := rp.FlushPendingStatusUpdates()
	if len(updates) == 0 {
		t.Fatalf("expected updates")
	}
	rp.SetReconnecting(false)

	_ = time.Now() // appease linter
}
