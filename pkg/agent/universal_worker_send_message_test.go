package agent

import (
	"github.com/livekit/protocol/livekit"
	"testing"
)

func TestUniversalWorker_SendMessage_NotConnected(t *testing.T) {
	w := &UniversalWorker{}
	if err := w.sendMessage(&livekit.WorkerMessage{}); err == nil {
		t.Fatalf("expected not connected error")
	}
}
