package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
)

// Minimal test to cover LoadBatcher.Start no-op path.
func TestLoadBatcherStartNoop(t *testing.T) {
	// mock worker to satisfy interface
	mw := &mockWorkerInterface{}
	b := NewLoadBatcher(mw, 10*time.Millisecond)
	b.Start() // no-op
	// ensure Update and Stop can be called without panic
	b.Update(WorkerStatusAvailable, 0.1)
	time.Sleep(12 * time.Millisecond)
	b.Stop()
}

type mockWorkerInterface struct{}

func (m *mockWorkerInterface) UpdateStatus(status WorkerStatus, load float32) error                 { return nil }
func (m *mockWorkerInterface) GetActiveJobs() map[string]interface{}                                { return map[string]interface{}{} }
func (m *mockWorkerInterface) updateJobStatus(jobID string, status livekit.JobStatus, error string) {}
func (m *mockWorkerInterface) GetServerURL() string                                                 { return "" }
func (m *mockWorkerInterface) GetLogger() Logger                                                    { return NewDefaultLogger() }
func (m *mockWorkerInterface) SetActiveJob(jobID string, job interface{})                           {}
