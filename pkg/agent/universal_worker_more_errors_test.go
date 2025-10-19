package agent

import (
	"context"
	"github.com/livekit/protocol/livekit"
	"testing"
)

func TestUniversalWorker_ShutdownHooks_WhenNil(t *testing.T) {
	w := &UniversalWorker{}
	if err := w.AddShutdownHook(ShutdownPhasePreStop, ShutdownHook{Name: "x", Handler: func(context.Context) error { return nil }}); err == nil {
		t.Fatalf("expected error when shutdownHandler is nil")
	}
	if ok := w.RemoveShutdownHook(ShutdownPhaseCleanup, "x"); ok {
		t.Fatalf("expected false when shutdownHandler is nil")
	}
	if hooks := w.GetShutdownHooks(ShutdownPhaseFinal); hooks != nil {
		t.Fatalf("expected nil hooks when shutdownHandler is nil")
	}
}

func TestUniversalWorker_QueueJob_NoQueue(t *testing.T) {
	w := &UniversalWorker{}
	if err := w.QueueJob(&livekit.Job{Id: "j"}, "", ""); err == nil {
		t.Fatalf("expected error without jobQueue")
	}
}

func TestUniversalWorker_PublishTrack_Errors(t *testing.T) {
	w := &UniversalWorker{activeJobs: make(map[string]*JobContext)}
	if _, err := w.PublishTrack("missing", nil); err == nil {
		t.Fatalf("expected error for missing job")
	}
	w.activeJobs["j1"] = &JobContext{}
	if _, err := w.PublishTrack("j1", nil); err == nil {
		t.Fatalf("expected error for missing room")
	}
}
