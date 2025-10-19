package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// End-to-end LiveKit-backed test that exercises availability -> assignment -> handler flow
func TestUniversalWorker_LiveKit_AssignmentFlow(t *testing.T) {
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	agentName := fmt.Sprintf("uw-assignment-%d", time.Now().UnixNano())
	// Handler that accepts and returns quickly
	assigned := make(chan struct{}, 1)
	handler := &SimpleUniversalHandler{
		JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "agent", ParticipantName: agentName}
		},
		JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
			assigned <- struct{}{}
			return nil
		},
	}

	manager := NewTestRoomManager()
	worker := NewUniversalWorker(manager.URL, manager.APIKey, manager.APISecret, handler, WorkerOptions{
		AgentName: agentName,
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go func() { _ = worker.Start(ctx) }()
	// Wait for connection
	require.Eventually(t, func() bool { return worker.IsConnected() }, 5*time.Second, 100*time.Millisecond)

	// Create room with agent dispatch to trigger job assignment
	roomClient := lksdk.NewRoomServiceClient(manager.URL, manager.APIKey, manager.APISecret)
	roomName := fmt.Sprintf("assign-room-%d", time.Now().Unix())
	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Agent dispatch room",
		Agents: []*livekit.RoomAgentDispatch{
			{AgentName: agentName, Metadata: `{"test":true}`},
		},
	})
	require.NoError(t, err)

	// Wait for assignment to arrive
	select {
	case <-assigned:
		// success
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for job assignment")
	}

	_ = worker.Stop()
	assert.True(t, true)
}
