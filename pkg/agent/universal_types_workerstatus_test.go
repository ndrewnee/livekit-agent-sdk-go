package agent

import (
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestWorkerStatusToProto_DefaultPath(t *testing.T) {
	// Unknown status should map to AVAILABLE by default
	s := workerStatusToProto(WorkerStatus(123))
	assert.Equal(t, livekit.WorkerStatus_WS_AVAILABLE, s)
}
