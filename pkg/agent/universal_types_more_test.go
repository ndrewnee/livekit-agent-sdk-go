package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestServerMessage_Getters(t *testing.T) {
	// Register
	m := &ServerMessage{Message: &ServerMessage_Register{Register: &livekit.RegisterWorkerResponse{WorkerId: "w1"}}}
	assert.NotNil(t, m.GetRegister())

	// Availability
	m = &ServerMessage{Message: &ServerMessage_Availability{Availability: &livekit.AvailabilityRequest{}}}
	assert.NotNil(t, m.GetAvailability())

	// Assignment
	m = &ServerMessage{Message: &ServerMessage_Assignment{Assignment: &livekit.JobAssignment{}}}
	assert.NotNil(t, m.GetAssignment())

	// Termination
	m = &ServerMessage{Message: &ServerMessage_Termination{Termination: &livekit.JobTermination{}}}
	assert.NotNil(t, m.GetTermination())

	// Ping
	m = &ServerMessage{Message: &ServerMessage_Ping{Ping: &livekit.Ping{}}}
	assert.NotNil(t, m.GetPing())
}

func TestWorkerStatusAndWebsocketURL(t *testing.T) {
	assert.Equal(t, livekit.WorkerStatus_WS_AVAILABLE, workerStatusToProto(WorkerStatusAvailable))
	assert.Equal(t, livekit.WorkerStatus_WS_FULL, workerStatusToProto(WorkerStatusFull))

	// http -> ws
	ws := buildWebSocketURL("http://example.com/api", "")
	assert.Contains(t, ws, "ws://example.com/api/")
	assert.Contains(t, ws, "protocol=")

	// https -> wss, trailing slash
	ws2 := buildWebSocketURL("https://example.com/", "")
	assert.Contains(t, ws2, "wss://example.com/")
}

func TestMessageAndValidatorNoops(t *testing.T) {
	mh := NewMessageHandler(NewDefaultLogger())
	mh.RegisterHandler("dummy", func(*ServerMessage) error { return nil })
	assert.NoError(t, mh.HandleMessage(&ServerMessage{}))

	v := NewProtocolValidator(false)
	assert.NoError(t, v.ValidateServerMessage(&ServerMessage{}))

	// Default logger Warn path
	NewDefaultLogger().Warn("warn", "field", 1)

	// Getters with non-matching type return nil
	_ = &ServerMessage{Message: &ServerMessage_Register{Register: &livekit.RegisterWorkerResponse{}}}
	assert.Nil(t, (&ServerMessage{Message: nil}).GetRegister())
	assert.Nil(t, (&ServerMessage{Message: nil}).GetAvailability())
	assert.Nil(t, (&ServerMessage{Message: nil}).GetAssignment())
	assert.Nil(t, (&ServerMessage{Message: nil}).GetTermination())
	assert.Nil(t, (&ServerMessage{Message: nil}).GetPing())
}
