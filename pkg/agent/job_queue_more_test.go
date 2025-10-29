package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
)

// Covers Clear method explicitly.
func TestJobQueueClear(t *testing.T) {
	q := NewJobQueue(JobQueueOptions{MaxSize: 10})
	_ = q.Enqueue(&livekit.Job{Id: "a"}, JobPriorityNormal, "t", "u")
	_ = q.Enqueue(&livekit.Job{Id: "b"}, JobPriorityHigh, "t", "u")
	if q.Size() == 0 {
		t.Fatalf("expected items in queue before clear")
	}
	q.Clear()
	if q.Size() != 0 {
		t.Fatalf("expected empty queue after clear")
	}
}
