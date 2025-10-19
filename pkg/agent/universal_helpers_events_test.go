package agent

import (
	"github.com/livekit/protocol/livekit"
	"testing"
)

func TestUniversalHelpers_HandleParticipantEvents(t *testing.T) {
	w := &UniversalWorker{participants: make(map[string]*ParticipantInfo), eventProcessor: NewUniversalEventProcessor()}
	defer w.eventProcessor.Stop()

	// Seed participant
	w.participants["alice"] = &ParticipantInfo{}
	pi := &livekit.ParticipantInfo{Identity: "alice"}

	w.handleConnectionQualityChanged(pi, livekit.ConnectionQuality_EXCELLENT)
	w.handleActiveSpeakersChanged([]*livekit.ParticipantInfo{pi})
	w.handleDataReceived([]byte("hi"), pi)
}
