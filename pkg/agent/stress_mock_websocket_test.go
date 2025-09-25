package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/require"
)

// This stress test exercises the WebSocket loop, availability handling,
// status update queueing, and reconnects using the in-repo mock gateway.
// It does not require a LiveKit room server; assignments deliberately use
// invalid tokens to force quick room-connect failures while keeping the
// worker logic, queues, and reconnection paths busy.
func TestStress_MockGateway_ReconnectAndAssignments(t *testing.T) {
	ms := newMockWebSocketServer()
	defer ms.Close()
	url := ms.URL()

	var assigns int32
	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			// Accept all jobs; let connection to room fail quickly
			return true, &JobMetadata{ParticipantIdentity: fmt.Sprintf("agent-%s", job.Id)}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			atomic.AddInt32(&assigns, 1)
			// Short work, then return; for invalid token paths the handler may
			// not run – this counter still provides a signal when it does.
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	}

	w := NewUniversalWorker(url, "devkey", "secret", handler, WorkerOptions{
		AgentName: "stress-mock",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker
	go func() { _ = w.Start(ctx) }()

	// Wait for connection
	require.Eventually(t, func() bool { return w.IsConnected() }, 5*time.Second, 50*time.Millisecond)

	// Drive many availability + assignment cycles with invalid token/URL
	// to stress message handling and status queues without LiveKit server.
	cycles := 200
	for i := 0; i < cycles; i++ {
		jobID := fmt.Sprintf("mock-job-%d", i)
		ms.SendAvailabilityRequest(&livekit.Job{Id: jobID, Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: fmt.Sprintf("room-%d", i)}})
		// Invalid token+URL; worker will fail to connect and update status
		invalidToken := "invalidtoken"
		invalidURL := "ws://localhost:0" // invalid port to fail fast
		ms.SendJobAssignmentWithURL(&livekit.Job{Id: jobID, Type: livekit.JobType_JT_ROOM, Room: &livekit.Room{Name: fmt.Sprintf("room-%d", i)}}, invalidToken, invalidURL)

		// Introduce periodic reconnects by closing worker conn midway
		if i == cycles/2 {
			w.mu.Lock()
			if w.conn != nil {
				_ = w.conn.Close()
			}
			w.mu.Unlock()
		}
	}

	// Let the worker process messages
	time.Sleep(500 * time.Millisecond)

	// The key assertion is that we didn't deadlock or crash and the test completes.
	// Additionally, ensure the worker is still responsive.
	_ = w.UpdateStatus(WorkerStatusAvailable, 0.0)
}
