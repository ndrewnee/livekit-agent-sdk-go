package agent

import (
	"errors"
	"testing"

	"github.com/am-sokolov/livekit-agent-sdk-go/internal/test/mocks"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

func TestUniversalWorker_handleJobAssignment_usesAssignmentTokenWhenPresent(t *testing.T) {
	oldConnectToRoomWithToken := connectToRoomWithToken
	oldConnectToRoom := connectToRoom
	t.Cleanup(func() {
		connectToRoomWithToken = oldConnectToRoomWithToken
		connectToRoom = oldConnectToRoom
	})

	var (
		withTokenCalls int
		connectCalls   int
		gotURL         string
		gotToken       string
	)

	connectToRoomWithToken = func(url, token string, _ *lksdk.RoomCallback, _ ...lksdk.ConnectOption) (*lksdk.Room, error) {
		withTokenCalls++
		gotURL = url
		gotToken = token
		return nil, errors.New("boom")
	}
	connectToRoom = func(url string, _ lksdk.ConnectInfo, _ *lksdk.RoomCallback, _ ...lksdk.ConnectOption) (*lksdk.Room, error) {
		connectCalls++
		return nil, errors.New("boom")
	}

	handler := NewMockUniversalHandler()
	worker := NewUniversalWorker("wss://livekit.example", "devkey", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
		Logger:  mocks.NewMockLogger(),
	})

	roomURL := "wss://room.example"
	worker.handleJobAssignment(&livekit.JobAssignment{
		Job: &livekit.Job{
			Id:   "job-1",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room-1"},
		},
		Url:   &roomURL,
		Token: "token-123",
	})

	assert.Equal(t, 1, withTokenCalls)
	assert.Equal(t, 0, connectCalls)
	assert.Equal(t, roomURL, gotURL)
	assert.Equal(t, "token-123", gotToken)
}
