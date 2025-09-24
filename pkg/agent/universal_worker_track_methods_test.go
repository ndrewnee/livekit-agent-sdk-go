package agent

import (
	"context"
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestUniversalWorker_TrackMethods_ErrorPaths(t *testing.T) {
	w := &UniversalWorker{
		subscribedTracks: make(map[string]*PublisherTrackSubscription),
		activeJobs:       make(map[string]*JobContext),
		logger:           NewDefaultLogger(),
		loadCalculator:   &DefaultLoadCalculator{},
		statusQueueChan:  make(chan struct{}, 10),
	}

	// SubscribeToTrack with nil publication
	assert.Error(t, w.SubscribeToTrack(nil))

	// UnsubscribeFromTrack with nil publication
	assert.Error(t, w.UnsubscribeFromTrack(nil))

	// EnableTrack on non-existent sid
	assert.Error(t, w.EnableTrack("missing", true))

	// runJobHandler panic path
	w.handler = &SimpleUniversalHandler{JobAssignedFunc: func(ctx context.Context, jobCtx *JobContext) error {
		panic("boom")
	}}
	job := &livekit.Job{Id: "job-run"}
	jc := &JobContext{Job: job}
	w.activeJobs = map[string]*JobContext{"job-run": jc}
	w.runJobHandler(context.Background(), jc)
}
