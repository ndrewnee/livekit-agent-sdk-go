package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func TestSimpleJobHandler(t *testing.T) {
	t.Run("default metadata", func(t *testing.T) {
		handler := &SimpleJobHandler{}

		tests := []struct {
			job      *livekit.Job
			wantName string
			wantID   string
		}{
			{
				job: &livekit.Job{
					Id:   "123",
					Type: livekit.JobType_JT_ROOM,
				},
				wantName: "Room Agent",
				wantID:   "agent-123",
			},
			{
				job: &livekit.Job{
					Id:   "456",
					Type: livekit.JobType_JT_PUBLISHER,
					Participant: &livekit.ParticipantInfo{
						Identity: "user-abc",
					},
				},
				wantName: "Publisher Agent",
				wantID:   "agent-pub-user-abc",
			},
			{
				job: &livekit.Job{
					Id:   "789",
					Type: livekit.JobType_JT_PARTICIPANT,
					Participant: &livekit.ParticipantInfo{
						Identity: "user-xyz",
					},
				},
				wantName: "Participant Agent",
				wantID:   "agent-part-user-xyz",
			},
		}

		for _, tt := range tests {
			t.Run(tt.wantName, func(t *testing.T) {
				accept, metadata := handler.OnJobRequest(context.Background(), tt.job)
				
				if !accept {
					t.Error("Expected job to be accepted")
				}
				
				if metadata.ParticipantIdentity != tt.wantID {
					t.Errorf("Expected identity %s, got %s", tt.wantID, metadata.ParticipantIdentity)
				}
				
				if metadata.ParticipantName != tt.wantName {
					t.Errorf("Expected name %s, got %s", tt.wantName, metadata.ParticipantName)
				}
			})
		}
	})

	t.Run("custom metadata", func(t *testing.T) {
		handler := &SimpleJobHandler{
			Metadata: func(job *livekit.Job) *JobMetadata {
				return &JobMetadata{
					ParticipantIdentity:   "custom-" + job.Id,
					ParticipantName:       "Custom Agent",
					ParticipantMetadata:   `{"custom": true}`,
					ParticipantAttributes: map[string]string{"role": "custom"},
					SupportsResume:        true,
				}
			},
		}

		job := &livekit.Job{Id: "custom-job", Type: livekit.JobType_JT_ROOM}
		accept, metadata := handler.OnJobRequest(context.Background(), job)

		if !accept {
			t.Error("Expected job to be accepted")
		}

		if metadata.ParticipantIdentity != "custom-custom-job" {
			t.Errorf("Unexpected identity: %s", metadata.ParticipantIdentity)
		}

		if !metadata.SupportsResume {
			t.Error("Expected SupportsResume to be true")
		}
	})

	t.Run("job handler", func(t *testing.T) {
		jobHandled := make(chan struct{})
		var capturedJob *livekit.Job
		var capturedRoom *lksdk.Room

		handler := &SimpleJobHandler{
			OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
				capturedJob = job
				capturedRoom = room
				close(jobHandled)
				return nil
			},
		}

		job := &livekit.Job{Id: "test-job"}
		room := &lksdk.Room{} // Would be a real room in practice

		err := handler.OnJobAssigned(context.Background(), job, room)
		if err != nil {
			t.Fatalf("OnJobAssigned error: %v", err)
		}

		select {
		case <-jobHandled:
			// Success
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Job handler was not called")
		}

		if capturedJob != job {
			t.Error("Job was not passed correctly")
		}

		if capturedRoom != room {
			t.Error("Room was not passed correctly")
		}
	})

	t.Run("termination handler", func(t *testing.T) {
		terminated := make(chan struct{})
		var capturedJobID string

		handler := &SimpleJobHandler{
			OnTerminated: func(jobID string) {
				capturedJobID = jobID
				close(terminated)
			},
		}

		handler.OnJobTerminated(context.Background(), "term-job")

		select {
		case <-terminated:
			// Success
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Termination handler was not called")
		}

		if capturedJobID != "term-job" {
			t.Errorf("Expected job ID term-job, got %s", capturedJobID)
		}

		// Reason field no longer exists
	})
}

func TestJobContext(t *testing.T) {
	t.Run("basic context", func(t *testing.T) {
		ctx := context.Background()
		job := &livekit.Job{Id: "test-job"}
		room := &lksdk.Room{}

		jc := NewJobContext(ctx, job, room)

		if jc.Job != job {
			t.Error("Job not set correctly")
		}

		if jc.Room != room {
			t.Error("Room not set correctly")
		}

		if jc.Context() != ctx {
			t.Error("Context not set correctly")
		}
	})

	t.Run("done channel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		jc := NewJobContext(ctx, &livekit.Job{}, &lksdk.Room{})

		select {
		case <-jc.Done():
			t.Fatal("Done channel should not be closed yet")
		default:
			// Good
		}

		cancel()

		select {
		case <-jc.Done():
			// Success
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Done channel was not closed")
		}
	})

	t.Run("sleep", func(t *testing.T) {
		ctx := context.Background()
		jc := NewJobContext(ctx, &livekit.Job{}, &lksdk.Room{})

		start := time.Now()
		err := jc.Sleep(50 * time.Millisecond)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("Sleep error: %v", err)
		}

		if elapsed < 40*time.Millisecond || elapsed > 70*time.Millisecond {
			t.Errorf("Sleep duration incorrect: %v", elapsed)
		}
	})

	t.Run("sleep cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		jc := NewJobContext(ctx, &livekit.Job{}, &lksdk.Room{})

		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		err := jc.Sleep(100 * time.Millisecond)
		elapsed := time.Since(start)

		if err != context.Canceled {
			t.Fatalf("Expected context.Canceled, got %v", err)
		}

		if elapsed > 40*time.Millisecond {
			t.Errorf("Sleep should have been interrupted early: %v", elapsed)
		}
	})

	t.Run("publish data", func(t *testing.T) {
		// We need to create a proper mock room or skip this test
		// Since PublishData requires a valid LocalParticipant, we'll skip it for unit tests
		t.Skip("PublishData requires a connected room with LocalParticipant")
	})
}

func TestLoadBalancer(t *testing.T) {
	t.Run("basic operations", func(t *testing.T) {
		lb := NewLoadBalancer()

		// Initially empty
		if count := lb.GetWorkerCount(); count != 0 {
			t.Errorf("Expected 0 workers, got %d", count)
		}

		// Add workers
		lb.UpdateWorker("w1", 0.2, 2, 10)
		lb.UpdateWorker("w2", 0.5, 5, 10)
		lb.UpdateWorker("w3", 0.8, 8, 10)

		if count := lb.GetWorkerCount(); count != 3 {
			t.Errorf("Expected 3 workers, got %d", count)
		}

		// Get least loaded
		worker := lb.GetLeastLoadedWorker()
		if worker == nil {
			t.Fatal("Expected to get a worker")
		}

		if worker.ID != "w1" {
			t.Errorf("Expected worker w1 (lowest load), got %s", worker.ID)
		}

		// Remove worker
		lb.RemoveWorker("w1")

		if count := lb.GetWorkerCount(); count != 2 {
			t.Errorf("Expected 2 workers after removal, got %d", count)
		}

		// Now w2 should be least loaded
		worker = lb.GetLeastLoadedWorker()
		if worker != nil && worker.ID != "w2" {
			t.Errorf("Expected worker w2, got %s", worker.ID)
		}
	})

	t.Run("stale workers", func(t *testing.T) {
		lb := NewLoadBalancer()

		// Add a worker
		lb.UpdateWorker("w1", 0.1, 1, 10)

		// Manually set last update to be old
		lb.mu.Lock()
		lb.workers["w1"].LastUpdate = time.Now().Add(-60 * time.Second)
		lb.mu.Unlock()

		// Should not count stale workers
		if count := lb.GetWorkerCount(); count != 0 {
			t.Errorf("Expected 0 active workers (stale), got %d", count)
		}

		// Should not return stale workers
		worker := lb.GetLeastLoadedWorker()
		if worker != nil {
			t.Error("Should not return stale worker")
		}
	})

	t.Run("full workers", func(t *testing.T) {
		lb := NewLoadBalancer()

		// Add workers at capacity
		lb.UpdateWorker("w1", 1.0, 5, 5)  // At capacity
		lb.UpdateWorker("w2", 0.8, 4, 5)  // Below capacity

		worker := lb.GetLeastLoadedWorker()
		if worker == nil {
			t.Fatal("Expected to get a worker")
		}

		if worker.ID != "w2" {
			t.Errorf("Expected worker w2 (not full), got %s", worker.ID)
		}
	})

	t.Run("concurrent access", func(t *testing.T) {
		lb := NewLoadBalancer()
		
		var wg sync.WaitGroup
		
		// Multiple goroutines updating workers
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					workerID := fmt.Sprintf("w%d", id)
					load := float32(j) / 100.0
					lb.UpdateWorker(workerID, load, j, 100)
					
					if j%10 == 0 {
						lb.GetLeastLoadedWorker()
						lb.GetWorkerCount()
					}
					
					if j%20 == 0 {
						lb.RemoveWorker(workerID)
					}
				}
			}(i)
		}

		wg.Wait()
		
		// Should complete without race conditions or panics
	})
}

func TestWorkerInfo(t *testing.T) {
	info := &WorkerInfo{
		ID:         "test-worker",
		Load:       0.75,
		JobCount:   3,
		MaxJobs:    4,
		LastUpdate: time.Now(),
	}

	if info.ID != "test-worker" {
		t.Errorf("Expected ID test-worker, got %s", info.ID)
	}

	if info.Load != 0.75 {
		t.Errorf("Expected load 0.75, got %f", info.Load)
	}

	if info.JobCount != 3 {
		t.Errorf("Expected job count 3, got %d", info.JobCount)
	}

	if info.MaxJobs != 4 {
		t.Errorf("Expected max jobs 4, got %d", info.MaxJobs)
	}

	if time.Since(info.LastUpdate) > time.Second {
		t.Error("LastUpdate should be recent")
	}
}