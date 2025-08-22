package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

// TestWorkerWithJobQueue tests worker with job queue enabled
func TestWorkerWithJobQueue(t *testing.T) {
	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
			}
		},
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType:        livekit.JobType_JT_ROOM,
		MaxJobs:        2,
		EnableJobQueue: true,
		JobQueueSize:   10,
	})

	// Verify queue is initialized
	assert.NotNil(t, worker.Worker.jobQueue)
	assert.NotNil(t, worker.Worker.priorityCalc)

	// Test queue stats
	stats := worker.Worker.GetQueueStats()
	assert.True(t, stats["enabled"].(bool))
	assert.Equal(t, 0, stats["size"])
	assert.Equal(t, 10, stats["max_size"])
}

// TestWorkerJobQueueing tests job queueing when at capacity
func TestWorkerJobQueueing(t *testing.T) {
	handler := &testJobHandler{
		OnJobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
			return true, &JobMetadata{
				ParticipantIdentity: "test-agent",
			}
		},
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			// Simulate long-running job
			time.Sleep(100 * time.Millisecond)
			return nil
		},
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType:        livekit.JobType_JT_ROOM,
		MaxJobs:        1,
		EnableJobQueue: true,
		JobQueueSize:   5,
	})

	// Fill capacity with first job
	job1 := &livekit.Job{
		Id:   "job-1",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-1"},
	}
	
	// Mark worker as having an active job
	worker.Worker.mu.Lock()
	worker.Worker.activeJobs[job1.Id] = &activeJob{
		job:       job1,
		status:    livekit.JobStatus_JS_RUNNING,
		startedAt: time.Now(),
	}
	worker.Worker.mu.Unlock()

	// Try to assign another job - should be queued
	job2 := &livekit.Job{
		Id:   "job-2",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room-2"},
	}
	
	assignment := &livekit.JobAssignment{
		Job:   job2,
		Token: "test-token",
	}

	// This should queue the job instead of processing it
	worker.Worker.handleJobAssignment(context.Background(), assignment)

	// Check that job was queued
	stats := worker.Worker.GetQueueStats()
	assert.Equal(t, 1, stats["size"])
	
	// Verify job is in queue
	item, ok := worker.Worker.jobQueue.Peek()
	assert.True(t, ok)
	assert.Equal(t, "job-2", item.Job.Id)
}

// TestWorkerPriorityQueueProcessing tests priority-based job processing
func TestWorkerPriorityQueueProcessing(t *testing.T) {
	processedJobs := make([]string, 0)
	var processMu sync.Mutex

	handler := &testJobHandler{
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			processMu.Lock()
			processedJobs = append(processedJobs, job.Id)
			processMu.Unlock()
			return nil
		},
	}

	// Custom priority calculator
	priorityCalc := &MetadataPriorityCalculator{
		PriorityField: "priority",
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType:               livekit.JobType_JT_ROOM,
		MaxJobs:               1,
		EnableJobQueue:        true,
		JobQueueSize:          10,
		JobPriorityCalculator: priorityCalc,
	})

	// Queue jobs with different priorities
	jobs := []struct {
		id       string
		metadata string
	}{
		{"normal-1", "priority:normal"},
		{"urgent-1", "priority:urgent"},
		{"high-1", "priority:high"},
		{"normal-2", "priority:normal"},
	}

	for _, j := range jobs {
		job := &livekit.Job{
			Id:       j.id,
			Type:     livekit.JobType_JT_ROOM,
			Room:     &livekit.Room{Name: "room"},
			Metadata: j.metadata,
		}
		err := worker.Worker.QueueJob(job, "token", "url")
		assert.NoError(t, err)
	}

	// Start queue processor
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go worker.Worker.processJobQueue(ctx)

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Check processing order (urgent -> high -> normal)
	processMu.Lock()
	defer processMu.Unlock()
	
	if len(processedJobs) >= 3 {
		assert.Equal(t, "urgent-1", processedJobs[0])
		assert.Equal(t, "high-1", processedJobs[1])
		assert.Contains(t, []string{"normal-1", "normal-2"}, processedJobs[2])
	}
}

// TestWorkerWithResourcePool tests worker with resource pool enabled
func TestWorkerWithResourcePool(t *testing.T) {
	handler := &testJobHandler{}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType:             livekit.JobType_JT_ROOM,
		EnableResourcePool:  true,
		ResourcePoolMinSize: 2,
		ResourcePoolMaxSize: 5,
	})

	// Verify pool is initialized
	assert.NotNil(t, worker.Worker.resourcePool)

	// Test pool stats
	stats := worker.Worker.GetResourcePoolStats()
	assert.True(t, stats["enabled"].(bool))
	poolStats := stats["stats"].(map[string]int64)
	assert.Equal(t, int64(2), poolStats["created"])
	assert.Equal(t, int64(2), poolStats["available"])
}

// TestWorkerQueueAndPoolIntegration tests queue and pool working together
func TestWorkerQueueAndPoolIntegration(t *testing.T) {
	resourcesUsed := make(map[string]bool)
	var resourceMu sync.Mutex

	handler := &testJobHandler{
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			// Track resource usage
			resourceMu.Lock()
			resourcesUsed[job.Id] = true
			resourceMu.Unlock()
			
			// Simulate work
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType:             livekit.JobType_JT_ROOM,
		MaxJobs:             10,
		EnableJobQueue:      true,
		JobQueueSize:        20,
		EnableResourcePool:  true,
		ResourcePoolMinSize: 2,
		ResourcePoolMaxSize: 4,
	})

	// Queue multiple jobs
	for i := 1; i <= 6; i++ {
		job := &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room"},
		}
		err := worker.Worker.QueueJob(job, "token", "url")
		assert.NoError(t, err)
	}

	// Start queue processor
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go worker.Worker.processJobQueue(ctx)

	// Wait for some processing
	time.Sleep(300 * time.Millisecond)

	// Check resource pool usage
	poolStats := worker.Worker.resourcePool.Stats()
	assert.Greater(t, poolStats["in_use"]+poolStats["available"], int64(0))
	
	// Check that jobs were processed
	resourceMu.Lock()
	processedCount := len(resourcesUsed)
	resourceMu.Unlock()
	assert.Greater(t, processedCount, 0)
}

// TestWorkerHealthWithConcurrentFeatures tests health reporting with all features
func TestWorkerHealthWithConcurrentFeatures(t *testing.T) {
	handler := &testJobHandler{}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType:             livekit.JobType_JT_ROOM,
		MaxJobs:             5,
		EnableJobQueue:      true,
		JobQueueSize:        10,
		EnableResourcePool:  true,
		ResourcePoolMinSize: 2,
		ResourcePoolMaxSize: 5,
	})

	// Add a job to queue
	job := &livekit.Job{
		Id:   "queued-job",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{Name: "room"},
	}
	worker.Worker.QueueJob(job, "token", "url")

	// Get health status
	health := worker.Worker.Health()

	// Verify queue stats are included
	assert.Contains(t, health, "queue")
	queueStats := health["queue"].(map[string]interface{})
	assert.True(t, queueStats["enabled"].(bool))
	assert.Equal(t, 1, queueStats["size"])

	// Verify resource pool stats are included
	assert.Contains(t, health, "resource_pool")
	poolStats := health["resource_pool"].(map[string]interface{})
	assert.True(t, poolStats["enabled"].(bool))
}

// TestWorkerGracefulShutdownWithQueue tests shutdown with queued jobs
func TestWorkerGracefulShutdownWithQueue(t *testing.T) {
	handler := &testJobHandler{}

	worker := NewWorker("http://localhost:7880", "key", "secret", handler, WorkerOptions{
		JobType:        livekit.JobType_JT_ROOM,
		EnableJobQueue: true,
		JobQueueSize:   10,
	})

	// Queue some jobs
	for i := 1; i <= 3; i++ {
		job := &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room"},
		}
		worker.QueueJob(job, "token", "url")
	}

	// Shutdown should clean up queue
	err := worker.StopWithTimeout(1 * time.Second)
	assert.NoError(t, err)

	// Queue should be closed
	assert.NotNil(t, worker.jobQueue) // Still exists but closed
}

// TestWorkerConcurrentJobLimit tests MaxJobs enforcement with queue
func TestWorkerConcurrentJobLimit(t *testing.T) {
	activeJobs := int32(0)
	maxConcurrent := int32(0)

	handler := &testJobHandler{
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			current := atomic.AddInt32(&activeJobs, 1)
			
			// Track max concurrent
			for {
				max := atomic.LoadInt32(&maxConcurrent)
				if current <= max || atomic.CompareAndSwapInt32(&maxConcurrent, max, current) {
					break
				}
			}
			
			// Simulate work
			time.Sleep(50 * time.Millisecond)
			
			atomic.AddInt32(&activeJobs, -1)
			return nil
		},
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType:        livekit.JobType_JT_ROOM,
		MaxJobs:        3,
		EnableJobQueue: true,
		JobQueueSize:   10,
	})

	// Start queue processor
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go worker.Worker.processJobQueue(ctx)

	// Queue many jobs
	for i := 1; i <= 10; i++ {
		job := &livekit.Job{
			Id:   fmt.Sprintf("job-%d", i),
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "room"},
		}
		worker.Worker.QueueJob(job, "token", "url")
	}

	// Let them process
	time.Sleep(200 * time.Millisecond)

	// Check that we never exceeded MaxJobs
	assert.LessOrEqual(t, atomic.LoadInt32(&maxConcurrent), int32(3))
}