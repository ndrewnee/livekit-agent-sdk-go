package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

func TestUniversalWorker_HandleJobTerminationAndSendPong(t *testing.T) {
	var termCount int32
	handler := &SimpleUniversalHandler{
		JobTerminatedFunc: func(ctx context.Context, jobID string) { atomic.AddInt32(&termCount, 1) },
	}

	w := &UniversalWorker{
		handler:             handler,
		activeJobs:          make(map[string]*JobContext),
		jobStartTimes:       make(map[string]time.Time),
		rooms:               make(map[string]*lksdk.Room),
		participantTrackers: make(map[string]*ParticipantTracker),
		logger:              NewDefaultLogger(),
		opts:                WorkerOptions{MaxJobs: 1},
	}

	// Seed an active job
	w.activeJobs["job-t"] = &JobContext{Cancel: func() {}, Room: &lksdk.Room{}}
	w.jobStartTimes["job-t"] = time.Now()
	w.rooms[""] = &lksdk.Room{} // room name can be empty in test
	w.participantTrackers[""] = &ParticipantTracker{}

	// Call termination handler
	assert.NoError(t, w.handleJobTermination(&livekit.JobTermination{JobId: "job-t"}))
	assert.Equal(t, int32(1), atomic.LoadInt32(&termCount))

	// sendPong is a no-op and should return nil
	assert.NoError(t, w.sendPong(123))
}
