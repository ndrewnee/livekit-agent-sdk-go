package agent

import (
	"context"
	"github.com/livekit/protocol/livekit"
	"testing"
)

func TestUniversalWorker_HandleAvailabilityRequest_Direct(t *testing.T) {
	w := &UniversalWorker{
		handler: &SimpleUniversalHandler{JobRequestFunc: func(_ context.Context, _ *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{ParticipantIdentity: "id", ParticipantName: "name"}
		}},
		logger: NewDefaultLogger(),
	}
	_ = w.handleAvailabilityRequest(&livekit.AvailabilityRequest{Job: &livekit.Job{Id: "jid"}})
}
