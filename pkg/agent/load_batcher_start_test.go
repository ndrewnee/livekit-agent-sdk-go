package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
)

type lbMockWorker struct{}

func (lbMockWorker) UpdateStatus(status WorkerStatus, load float32) error                 { return nil }
func (lbMockWorker) updateJobStatus(jobID string, status livekit.JobStatus, error string) {}
func (lbMockWorker) GetServerURL() string                                                 { return "" }
func (lbMockWorker) GetLogger() Logger                                                    { return NewDefaultLogger() }
func (lbMockWorker) GetActiveJobs() map[string]interface{}                                { return nil }
func (lbMockWorker) SetActiveJob(jobID string, job interface{})                           {}

func TestLoadBatcher_StartStopAgain(t *testing.T) {
	b := NewLoadBatcher(lbMockWorker{}, 5*time.Millisecond)
	b.Start()
	b.Update(WorkerStatusAvailable, 0.2)
	time.Sleep(7 * time.Millisecond)
	b.Stop()
}
