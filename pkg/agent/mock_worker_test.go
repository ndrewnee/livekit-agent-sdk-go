package agent

import (
	"sync"

	"github.com/livekit/protocol/livekit"
)

// mockWorker implements WorkerInterface for testing
type mockWorker struct {
	mu         sync.Mutex
	status     WorkerStatus
	load       float32
	serverURL  string
	logger     Logger
	activeJobs map[string]interface{}
}

// newMockWorker creates a new mock worker for testing
func newMockWorker(logger Logger) *mockWorker {
	if logger == nil {
		logger = NewDefaultLogger()
	}
	return &mockWorker{
		serverURL:  "http://localhost:7880",
		logger:     logger,
		activeJobs: make(map[string]interface{}),
	}
}

// UpdateStatus updates the worker's status and load
func (m *mockWorker) UpdateStatus(status WorkerStatus, load float32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = status
	m.load = load
	return nil
}

// updateJobStatus updates the status of a specific job
func (m *mockWorker) updateJobStatus(jobID string, status livekit.JobStatus, error string) {
	// Mock implementation - just log it
	m.logger.Info("Job status updated", "jobID", jobID, "status", status, "error", error)
}

// GetServerURL returns the server URL
func (m *mockWorker) GetServerURL() string {
	return m.serverURL
}

// GetLogger returns the logger instance
func (m *mockWorker) GetLogger() Logger {
	return m.logger
}

// GetActiveJobs returns active jobs (for recovery)
func (m *mockWorker) GetActiveJobs() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs := make(map[string]interface{})
	for k, v := range m.activeJobs {
		jobs[k] = v
	}
	return jobs
}

// SetActiveJob sets an active job (for recovery)
func (m *mockWorker) SetActiveJob(jobID string, job interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeJobs[jobID] = job
}
