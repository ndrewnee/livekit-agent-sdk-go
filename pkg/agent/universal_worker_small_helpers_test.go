package agent

import (
	"context"
	"testing"
	"time"
)

func TestUniversalWorker_ResourcePoolAndHooksAndQueues(t *testing.T) {
	w := &UniversalWorker{
		logger: NewDefaultLogger(),
	}

	// ResourcePool stats when disabled
	rpStats := w.GetResourcePoolStats()
	if rpStats["enabled"].(bool) != false {
		t.Fatalf("expected resource pool disabled")
	}

	// Enable a small resource pool
	pool, err := NewResourcePool(&WorkerResourceFactory{}, ResourcePoolOptions{MinSize: 0, MaxSize: 1, MaxIdleTime: time.Second})
	if err != nil {
		t.Fatalf("pool create: %v", err)
	}
	defer pool.Close()
	w.resourcePool = pool
	_ = w.GetResourcePoolStats()

	// Queue stats with and without job queue
	_ = w.GetQueueStats()
	w.jobQueue = NewJobQueue(JobQueueOptions{MaxSize: 4})
	_ = w.GetQueueStats()

	// Shutdown hooks lifecycle
	w.shutdownHandler = NewShutdownHandler(NewDefaultLogger())
	if err := w.AddPreStopHook("pre", func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("AddPreStopHook: %v", err)
	}
	if err := w.AddCleanupHook("clean", func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("AddCleanupHook: %v", err)
	}
	hooks := w.GetShutdownHooks(ShutdownPhasePreStop)
	if len(hooks) == 0 {
		t.Fatalf("expected pre_stop hook present")
	}
	removed := w.RemoveShutdownHook(ShutdownPhaseCleanup, "clean")
	if !removed {
		t.Fatalf("expected cleanup hook removed")
	}
}
