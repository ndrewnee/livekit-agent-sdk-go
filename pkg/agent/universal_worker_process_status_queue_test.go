package agent

import (
	"github.com/livekit/protocol/livekit"
	"testing"
)

func TestUniversalWorker_ProcessStatusQueue_Retries(t *testing.T) {
	w := &UniversalWorker{
		logger:          NewDefaultLogger(),
		statusQueueChan: make(chan struct{}, 10),
	}
	// Queue a failing update
	w.queueStatusUpdate(statusUpdate{jobID: "job", status: livekit.JobStatus_JS_RUNNING, error: ""})
	// Process multiple times to increment retries and hit max-retry branch
	for i := 0; i < 4; i++ {
		w.processStatusQueue()
	}
}
