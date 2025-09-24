package agent

import "testing"

func TestUniversalWorker_GetAllParticipantInfo(t *testing.T) {
	w := &UniversalWorker{participants: make(map[string]*ParticipantInfo)}
	w.participants["a"] = &ParticipantInfo{Identity: "a"}
	w.participants["b"] = &ParticipantInfo{Identity: "b"}
	infos := w.GetAllParticipantInfo()
	if len(infos) != 2 {
		t.Fatalf("expected 2, got %d", len(infos))
	}
}
