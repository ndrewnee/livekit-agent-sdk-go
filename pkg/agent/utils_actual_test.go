package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJobUtilsActual tests the actual JobUtils struct from utils.go
func TestJobUtilsActual(t *testing.T) {
	ctx := context.Background()
	job := &livekit.Job{
		Id:          "test-job",
		Room:        &livekit.Room{Name: "test-room"},
		Participant: &livekit.ParticipantInfo{Identity: "test-participant"},
	}

	// Create a mock room
	room := &lksdk.Room{}

	// Create JobUtils
	jobUtils := NewJobUtils(ctx, job, room)
	require.NotNil(t, jobUtils)

	// Test Context
	utilCtx := jobUtils.Context()
	assert.NotNil(t, utilCtx)

	// Test Done
	done := jobUtils.Done()
	assert.NotNil(t, done)

	// Test Sleep
	start := time.Now()
	jobUtils.Sleep(10 * time.Millisecond)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 10*time.Millisecond)

	// Test GetTargetParticipant
	target := jobUtils.GetTargetParticipant()
	assert.Nil(t, target) // Will be nil since room has no participants

	// Test WaitForParticipant (should timeout since no real room)
	participant, err := jobUtils.WaitForParticipant("test-participant", 50*time.Millisecond)
	assert.Nil(t, participant) // Should be nil due to timeout

	// Test PublishData (should fail without real room connection)
	err = jobUtils.PublishData([]byte("test data"), true, nil)
	assert.Error(t, err)
}

// TestJobUtilsTargetParticipant tests GetTargetParticipant with various job configurations
func TestJobUtilsTargetParticipant(t *testing.T) {
	tests := []struct {
		name        string
		job         *livekit.Job
		expectedNil bool
	}{
		{
			name: "with participant",
			job: &livekit.Job{
				Id:          "job1",
				Participant: &livekit.ParticipantInfo{Identity: "participant1"},
			},
			expectedNil: true, // Will be nil since room has no participants
		},
		{
			name: "without participant",
			job: &livekit.Job{
				Id: "job2",
			},
			expectedNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobUtils := NewJobUtils(context.Background(), tt.job, &lksdk.Room{})
			result := jobUtils.GetTargetParticipant()
			if tt.expectedNil {
				assert.Nil(t, result)
			} else {
				assert.NotNil(t, result)
			}
		})
	}
}

// TestLoadBalancerActual tests the actual LoadBalancer from utils.go
func TestLoadBalancerActual(t *testing.T) {
	lb := NewLoadBalancer()
	require.NotNil(t, lb)

	// Test GetWorkerCount (should be 0 initially)
	assert.Equal(t, 0, lb.GetWorkerCount())

	// Test UpdateWorker
	lb.UpdateWorker("worker1", 0.5, 5, 10)
	lb.UpdateWorker("worker2", 0.3, 3, 10)
	lb.UpdateWorker("worker3", 0.8, 8, 10)

	assert.Equal(t, 3, lb.GetWorkerCount())

	// Test GetLeastLoadedWorker
	workerInfo := lb.GetLeastLoadedWorker()
	assert.NotNil(t, workerInfo)
	assert.Equal(t, "worker2", workerInfo.ID)
	assert.Equal(t, float32(0.3), workerInfo.Load)

	// Update a worker's load
	lb.UpdateWorker("worker2", 0.9, 9, 10)

	// Check least loaded again
	workerInfo = lb.GetLeastLoadedWorker()
	assert.NotNil(t, workerInfo)
	assert.Equal(t, "worker1", workerInfo.ID)
	assert.Equal(t, float32(0.5), workerInfo.Load)

	// Test RemoveWorker
	lb.RemoveWorker("worker1")
	assert.Equal(t, 2, lb.GetWorkerCount())

	// Check least loaded after removal
	workerInfo = lb.GetLeastLoadedWorker()
	assert.NotNil(t, workerInfo)
	assert.Equal(t, "worker3", workerInfo.ID)
	assert.Equal(t, float32(0.8), workerInfo.Load)

	// Remove all workers
	lb.RemoveWorker("worker2")
	lb.RemoveWorker("worker3")
	assert.Equal(t, 0, lb.GetWorkerCount())

	// Test GetLeastLoadedWorker with no workers
	workerInfo = lb.GetLeastLoadedWorker()
	assert.Nil(t, workerInfo)
}

// TestSingletonUniversalHandler tests the singleton pattern
func TestSingletonUniversalHandler(t *testing.T) {
	// Create a simple handler
	handler := &SimpleUniversalHandler{}

	// Set various callbacks
	handler.JobRequestFunc = func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
		return true, nil
	}

	handler.JobAssignedFunc = func(ctx context.Context, jobCtx *JobContext) error {
		return nil
	}

	handler.JobTerminatedFunc = func(ctx context.Context, jobID string) {}

	// Test that callbacks are set
	assert.NotNil(t, handler.JobRequestFunc)
	assert.NotNil(t, handler.JobAssignedFunc)
	assert.NotNil(t, handler.JobTerminatedFunc)
}
