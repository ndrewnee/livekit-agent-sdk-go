//go:build integration
// +build integration

package agent_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// These tests require a running LiveKit server
// Run with: go test -tags=integration

func getTestConfig() (url, apiKey, apiSecret string) {
	url = os.Getenv("LIVEKIT_URL")
	if url == "" {
		url = "http://localhost:7880"
	}

	apiKey = os.Getenv("LIVEKIT_API_KEY")
	if apiKey == "" {
		apiKey = "devkey"
	}

	apiSecret = os.Getenv("LIVEKIT_API_SECRET")
	if apiSecret == "" {
		apiSecret = "secret"
	}

	return
}

func TestWorkerIntegration_BasicConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	url, apiKey, apiSecret := getTestConfig()

	connected := make(chan struct{})
	handler := &agent.SimpleJobHandler{
		OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			t.Logf("Job assigned: %s for room %s", job.Id, job.Room.Name)
			<-ctx.Done()
			return nil
		},
	}

	worker := agent.NewWorker(url, apiKey, apiSecret, handler, agent.WorkerOptions{
		AgentName: "integration-test",
		Version:   "1.0.0",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker in goroutine
	go func() {
		if err := worker.Start(ctx); err != nil {
			t.Errorf("Failed to start worker: %v", err)
			return
		}
		close(connected)
	}()

	// Wait for connection
	select {
	case <-connected:
		t.Log("Worker connected successfully")
	case <-time.After(5 * time.Second):
		t.Fatal("Worker failed to connect within timeout")
	}

	// Let it run for a bit
	time.Sleep(2 * time.Second)

	// Graceful shutdown
	if err := worker.Stop(); err != nil {
		t.Errorf("Failed to stop worker: %v", err)
	}
}

func TestWorkerIntegration_MultipleWorkers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	url, apiKey, apiSecret := getTestConfig()

	numWorkers := 3
	var wg sync.WaitGroup
	workers := make([]*agent.Worker, numWorkers)

	// Create multiple workers
	for i := 0; i < numWorkers; i++ {
		workerID := i
		handler := &agent.SimpleJobHandler{
			OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
				t.Logf("Worker %d handling job %s", workerID, job.Id)
				<-ctx.Done()
				return nil
			},
		}

		workers[i] = agent.NewWorker(url, apiKey, apiSecret, handler, agent.WorkerOptions{
			AgentName: "multi-worker-test",
			Version:   "1.0.0",
			JobType:   livekit.JobType_JT_ROOM,
			MaxJobs:   2,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start all workers
	for i, worker := range workers {
		wg.Add(1)
		go func(w *agent.Worker, id int) {
			defer wg.Done()
			t.Logf("Starting worker %d", id)
			if err := w.Start(ctx); err != nil {
				t.Errorf("Worker %d failed to start: %v", id, err)
			}
		}(worker, i)
	}

	// Let them run
	time.Sleep(3 * time.Second)

	// Update their status
	for i, worker := range workers {
		load := float32(i) * 0.3
		if err := worker.UpdateStatus(agent.WorkerStatusAvailable, load); err != nil {
			t.Errorf("Worker %d failed to update status: %v", i, err)
		}
	}

	// Let them run a bit more
	time.Sleep(2 * time.Second)

	// Stop all workers
	var stopWg sync.WaitGroup
	for i, worker := range workers {
		stopWg.Add(1)
		go func(w *agent.Worker, id int) {
			defer stopWg.Done()
			t.Logf("Stopping worker %d", id)
			if err := w.Stop(); err != nil {
				t.Errorf("Worker %d failed to stop: %v", id, err)
			}
		}(worker, i)
	}

	stopWg.Wait()
	cancel()
	wg.Wait()
}

func TestWorkerIntegration_JobHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	url, apiKey, apiSecret := getTestConfig()

	jobReceived := make(chan *livekit.Job, 1)
	jobCompleted := make(chan struct{})

	handler := &agent.SimpleJobHandler{
		OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			t.Logf("Handling job %s for room %s", job.Id, job.Room.Name)

			select {
			case jobReceived <- job:
			default:
			}

			// Simulate some work
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
				close(jobCompleted)
				return nil
			}
		},
		Metadata: func(job *livekit.Job) *agent.JobMetadata {
			return &agent.JobMetadata{
				ParticipantIdentity: "test-agent-" + job.Id,
				ParticipantName:     "Test Agent",
				ParticipantMetadata: `{"test": true}`,
			}
		},
	}

	worker := agent.NewWorker(url, apiKey, apiSecret, handler, agent.WorkerOptions{
		AgentName: "job-test",
		Version:   "1.0.0",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := worker.Start(ctx); err != nil {
		t.Fatalf("Failed to start worker: %v", err)
	}

	// Note: To actually receive jobs, you would need to:
	// 1. Create a room with agent dispatch configuration
	// 2. Have the server route a job to this worker
	// This would require additional setup with the LiveKit server

	// For now, we just verify the worker is running
	time.Sleep(2 * time.Second)

	if err := worker.Stop(); err != nil {
		t.Errorf("Failed to stop worker: %v", err)
	}
}

func TestWorkerIntegration_Reconnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	url, apiKey, apiSecret := getTestConfig()

	// Track reconnection events
	_ = make(chan struct{}, 5) // reconnects channel - to be used for tracking reconnection events

	handler := &agent.SimpleJobHandler{
		OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			<-ctx.Done()
			return nil
		},
	}

	// Create a custom logger to track reconnection attempts
	worker := agent.NewWorker(url, apiKey, apiSecret, handler, agent.WorkerOptions{
		AgentName: "reconnect-test",
		Version:   "1.0.0",
		JobType:   livekit.JobType_JT_ROOM,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := worker.Start(ctx); err != nil {
		t.Fatalf("Failed to start worker: %v", err)
	}

	// Run for a while to test stability
	time.Sleep(3 * time.Second)

	// Note: Testing actual reconnection would require disrupting the connection
	// which is difficult in an integration test without server cooperation

	if err := worker.Stop(); err != nil {
		t.Errorf("Failed to stop worker: %v", err)
	}
}
