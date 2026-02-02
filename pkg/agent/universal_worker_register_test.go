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

func TestUniversalWorker_sendRegister_includesPingIntervalAndPermissions(t *testing.T) {
	ms := newMockWebSocketServer()
	defer ms.Close()

	handler := NewMockUniversalHandler()
	worker := NewUniversalWorker(ms.URL(), "devkey", "secret", handler, WorkerOptions{
		JobType:      livekit.JobType_JT_ROOM,
		PingInterval: 5 * time.Second,
		Logger:       mocks.NewMockLogger(),
	})
	t.Cleanup(func() { _ = worker.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, worker.connect(ctx))

	msg, err := ms.WaitForMessage("register", 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, msg.GetRegister())

	reg := msg.GetRegister()
	assert.Equal(t, uint32(5), reg.PingInterval)

	require.NotNil(t, reg.AllowedPermissions)
	assert.True(t, reg.AllowedPermissions.CanSubscribe)
	assert.True(t, reg.AllowedPermissions.CanPublish)
	assert.True(t, reg.AllowedPermissions.CanPublishData)
	assert.True(t, reg.AllowedPermissions.CanUpdateMetadata)
}
