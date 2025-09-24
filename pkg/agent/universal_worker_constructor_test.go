package agent

import (
	"testing"
	"time"
)

func TestNewUniversalWorker_WithOptions(t *testing.T) {
	opts := WorkerOptions{
		MaxJobs:             2,
		EnableJobQueue:      true,
		JobQueueSize:        8,
		EnableResourcePool:  true,
		ResourceFactory:     &WorkerResourceFactory{},
		ResourcePoolMinSize: 1,
		ResourcePoolMaxSize: 1,
		EnableCPUMemoryLoad: true,
		PingInterval:        5 * time.Millisecond,
		PingTimeout:         10 * time.Millisecond,
	}
	w := NewUniversalWorker("ws://localhost:7880", "key", "secret", &SimpleUniversalHandler{}, opts)
	if w == nil {
		t.Fatalf("worker is nil")
	}
	// Exercise simple accessors
	_ = w.Health()
	_ = w.GetMetrics()
	_ = w.GetQueueStats()
	_ = w.GetResourcePoolStats()
}
