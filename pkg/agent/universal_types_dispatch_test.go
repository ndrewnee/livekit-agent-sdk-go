package agent

import (
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"testing"
)

// Verifies MessageHandler dispatches using short keys like "Pong"
func TestMessageHandler_ShortKeyDispatch(t *testing.T) {
	var called bool
	mh := NewMessageHandler(NewDefaultLogger())
	mh.RegisterHandler("Pong", func(*ServerMessage) error { called = true; return nil })

	msg := &ServerMessage{Message: &livekit.ServerMessage_Pong{Pong: &livekit.WorkerPong{}}}
	assert.NoError(t, mh.HandleMessage(msg))
	assert.True(t, called)
}
