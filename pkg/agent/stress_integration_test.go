//go:build integration

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Stress: spawn multiple rooms to trigger concurrent jobs and verify throughput.
func TestStress_Integration_ConcurrentJobs(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	var activeMu sync.Mutex
	active := make(map[string]bool)
	var maxConcurrent int32
	var processed int32

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: fmt.Sprintf("stress-%s", job.Id)}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			activeMu.Lock()
			active[jobCtx.Job.Id] = true
			if int32(len(active)) > atomic.LoadInt32(&maxConcurrent) {
				atomic.StoreInt32(&maxConcurrent, int32(len(active)))
			}
			activeMu.Unlock()

			// Simulate work
			time.Sleep(300 * time.Millisecond)

			activeMu.Lock()
			delete(active, jobCtx.Job.Id)
			activeMu.Unlock()
			atomic.AddInt32(&processed, 1)
			return nil
		},
	}

	w := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "stress-cjobs",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   5,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// Wait for connection
	require.Eventually(t, func() bool { return w.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	// Trigger more jobs than MaxJobs concurrently
	total := 10
	for i := 0; i < total; i++ {
		roomName := fmt.Sprintf("stress-cjobs-%d-%d", time.Now().Unix(), i)
		_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "stress-cjobs")
		require.NoError(t, err)
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for processing
	require.Eventually(t, func() bool { return atomic.LoadInt32(&processed) >= int32(total) }, 30*time.Second, 100*time.Millisecond)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&maxConcurrent), int32(2), "should observe concurrency > 1")
}

// Stress: burst data messages from a participant to the agent and ensure delivery.
func TestStress_Integration_DataBurst(t *testing.T) {
	url, apiKey, apiSecret := getTestConfig()

	var recvCount int32
	roomConnected := make(chan struct{}, 1)

	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "stress-data"}
		},
		RoomConnectedFunc: func(ctx context.Context, room *lksdk.Room) { roomConnected <- struct{}{} },
		DataReceivedFunc: func(ctx context.Context, data []byte, p *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
			atomic.AddInt32(&recvCount, 1)
		},
	}

	w := NewUniversalWorker(url, apiKey, apiSecret, handler, WorkerOptions{
		AgentName: "stress-data",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()
	require.Eventually(t, func() bool { return w.IsConnected() }, 10*time.Second, 100*time.Millisecond)

	roomName := fmt.Sprintf("stress-data-%d", time.Now().Unix())
	_, err := createTestRoomWithAgent(apiKey, apiSecret, url, roomName, "stress-data")
	require.NoError(t, err)

	// Wait for agent to join room
	select {
	case <-roomConnected:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for agent room connection")
	}

	// Connect sender participant
	token := generateTestToken(apiKey, apiSecret, roomName, "sender")
	room := lksdk.NewRoom(&lksdk.RoomCallback{})
	require.NoError(t, room.JoinWithToken(url, token))
	defer room.Disconnect()

	// Burst publish N packets
	const N = 100
	for i := 0; i < N; i++ {
		_ = room.LocalParticipant.PublishData([]byte("x"), lksdk.WithDataPublishReliable(true))
	}

	require.Eventually(t, func() bool { return atomic.LoadInt32(&recvCount) >= N }, 15*time.Second, 50*time.Millisecond)
}
