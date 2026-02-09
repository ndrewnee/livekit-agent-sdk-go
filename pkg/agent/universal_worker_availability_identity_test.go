package agent

import (
	"context"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/internal/test/mocks"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniversalWorker_handleAvailabilityRequest_setsDefaultIdentityWhenEmpty(t *testing.T) {
	ms := newMockWebSocketServer()
	defer ms.Close()

	handler := NewMockUniversalHandler()
	handler.SetJobMetadata(&JobMetadata{}) // Explicit non-nil but empty identity.

	worker := NewUniversalWorker(ms.URL(), "devkey", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
		Logger:  mocks.NewMockLogger(),
	})
	t.Cleanup(func() { _ = worker.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, worker.connect(ctx))

	req := &livekit.AvailabilityRequest{
		Job: &livekit.Job{
			Id:   "job-identity",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room-1"},
		},
	}
	require.NoError(t, worker.handleAvailabilityRequest(req))

	msg, err := ms.WaitForMessage("availability", 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, msg.GetAvailability())

	assert.Equal(t, "agent-job-identity", msg.GetAvailability().ParticipantIdentity)
	assert.NotEmpty(t, msg.GetAvailability().ParticipantName)
}
