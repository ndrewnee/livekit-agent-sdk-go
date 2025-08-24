package agent

import (
	"github.com/livekit/protocol/livekit"
)

// WorkerInterface defines the common interface for worker implementations.
// This allows shared components like JobRecoveryManager and LoadBatcher
// to work with both Worker and UniversalWorker.
type WorkerInterface interface {
	// UpdateStatus updates the worker's status and load
	UpdateStatus(status WorkerStatus, load float32) error

	// updateJobStatus updates the status of a specific job
	updateJobStatus(jobID string, status livekit.JobStatus, error string)

	// GetServerURL returns the server URL
	GetServerURL() string

	// GetLogger returns the logger instance
	GetLogger() Logger

	// GetActiveJobs returns active jobs (for recovery)
	GetActiveJobs() map[string]interface{}

	// SetActiveJob sets an active job (for recovery)
	SetActiveJob(jobID string, job interface{})
}
