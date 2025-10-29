package agent

import (
	"context"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestSimpleUniversalHandler_GetJobMetadata_UsesJobRequestFunc(t *testing.T) {
	h := &SimpleUniversalHandler{}
	expected := &JobMetadata{ParticipantIdentity: "meta-id"}
	h.JobRequestFunc = func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
		return true, expected
	}

	meta := h.GetJobMetadata(&livekit.Job{Id: "j"})
	assert.NotNil(t, meta)
	assert.Equal(t, "meta-id", meta.ParticipantIdentity)
}
