package agent

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestUniversalWorker_SendDataToParticipant_Errors(t *testing.T) {
	w := &UniversalWorker{activeJobs: make(map[string]*JobContext)}
	// Missing job
	assert.Error(t, w.SendDataToParticipant("missing", "id", []byte("x"), true))

	// No room
	w.activeJobs["j1"] = &JobContext{}
	assert.Error(t, w.SendDataToParticipant("j1", "id", []byte("x"), true))
}
