//go:build integration

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

func TestUniversalWorker_SimpleConnection(t *testing.T) {
	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return false, nil
		},
	}

	worker := NewUniversalWorker(
		"ws://localhost:7880",
		"devkey",
		"secret",
		handler,
		WorkerOptions{
			AgentName: "test-simple",
			JobType:   livekit.JobType_JT_ROOM,
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- worker.Start(ctx)
	}()

	// Wait for connection deterministically
	require.Eventually(t, func() bool { return worker.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Stop worker
	worker.Stop()

	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Logf("Worker stopped with error: %v", err)
		}
	case <-time.After(1 * time.Second):
		// OK
	}
}
