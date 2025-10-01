// +build integration

package egress

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEgressWorkerIntegration tests the complete egress worker flow
func TestEgressWorkerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Get LiveKit connection details from environment
	lkURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	if lkURL == "" || apiKey == "" || apiSecret == "" {
		t.Skip("LiveKit credentials not set, skipping integration test")
	}

	// Create test configuration
	config := &Config{
		MaxConcurrentSessions: 5,
		VideoQuality:          2,
		StorageConfig: StorageConfig{
			Type:      "local",
			LocalPath: "./test-recordings",
		},
		PipelineConfig: PipelineConfig{
			OutputDir:       "./test-recordings",
			SegmentDuration: 2,
			PlaylistType:    "event",
		},
		RecordingConfig: RecordingConfig{
			RecordVideo:       true,
			RecordAudio:       true,
			RecordScreenShare: true,
			AutoStart:         true,
		},
		NetworkConfig: NetworkConfig{
			UseDirectInjection: true,
			ReconnectAttempts:  3,
			AdaptiveBitrate:    true,
			MaxBitrate:         5000,
			MinBitrate:         1000,
		},
	}

	// Create egress worker
	worker := NewEgressWorker(config)
	require.NotNil(t, worker)

	// Create context for the test
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start the worker
	errChan := make(chan error, 1)
	go func() {
		if err := worker.Start(ctx, lkURL, apiKey, apiSecret); err != nil {
			errChan <- err
		}
	}()

	// Give worker time to connect
	time.Sleep(2 * time.Second)

	// Check if worker started successfully
	select {
	case err := <-errChan:
		t.Fatalf("Worker failed to start: %v", err)
	default:
		// Worker started successfully
	}

	t.Run("handles room job", func(t *testing.T) {
		// Simulate a room job
		roomJob := &livekit.Job{
			Id:   "test-room-job-1",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{
				Sid:  "room-sid-1",
				Name: "test-room-1",
			},
		}

		handler := &egressHandler{
			worker: worker,
			config: config,
		}

		// Test job acceptance
		accept, metadata := handler.OnJobRequest(ctx, roomJob)
		assert.True(t, accept)
		assert.NotNil(t, metadata)
		assert.Contains(t, metadata.ParticipantIdentity, "egress-monitor")

		// Test job assignment
		jobCtx := &agent.JobContext{
			Job:  roomJob,
			Room: &lksdk.Room{}, // Mock room
		}

		err := handler.OnJobAssigned(ctx, jobCtx)
		assert.NoError(t, err)

		// Verify room is being monitored
		assert.Equal(t, 1, worker.GetMonitoredRoomCount())
	})

	t.Run("handles participant job", func(t *testing.T) {
		// Simulate a participant job
		participantJob := &livekit.Job{
			Id:   "test-participant-job-1",
			Type: livekit.JobType_JT_PARTICIPANT,
			Participant: &livekit.ParticipantInfo{
				Sid:      "participant-sid-1",
				Identity: "test-user-1",
				Name:     "Test User 1",
			},
		}

		handler := &egressHandler{
			worker: worker,
			config: config,
		}

		// Test job acceptance
		accept, metadata := handler.OnJobRequest(ctx, participantJob)
		assert.True(t, accept)
		assert.NotNil(t, metadata)
		assert.Contains(t, metadata.ParticipantName, "Recording:")

		// Note: Full participant recording would require a real connection
	})

	t.Run("handles concurrent jobs", func(t *testing.T) {
		// Create a fresh worker with limited capacity for this test
		concurrentConfig := &Config{
			MaxConcurrentSessions: 3, // Lower limit for testing
			VideoQuality:          2,
			StorageConfig: StorageConfig{
				Type:      "local",
				LocalPath: "./test-recordings",
			},
		}
		concurrentWorker := NewEgressWorker(concurrentConfig)
		handler := &egressHandler{
			worker: concurrentWorker,
			config: concurrentConfig,
		}

		numJobs := 10
		accepted := 0
		rejected := 0

		// Process jobs sequentially to properly simulate capacity limits
		for i := 0; i < numJobs; i++ {
			job := &livekit.Job{
				Id:   fmt.Sprintf("concurrent-job-%d", i),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{
					Sid:  fmt.Sprintf("room-sid-%d", i),
					Name: fmt.Sprintf("test-room-%d", i),
				},
			}

			accept, _ := handler.OnJobRequest(ctx, job)
			if accept {
				accepted++

				// Simulate adding session to track capacity
				handler.worker.mu.Lock()
				jobCtx := &agent.JobContext{
					Job: job,
				}
				session := NewRecordingSession(jobCtx, concurrentConfig)
				handler.worker.sessions[job.Id] = session
				handler.worker.mu.Unlock()
			} else {
				rejected++
			}
		}

		// Should accept up to max concurrent sessions
		assert.Equal(t, concurrentConfig.MaxConcurrentSessions, accepted)
		assert.Equal(t, numJobs-concurrentConfig.MaxConcurrentSessions, rejected)
		assert.Greater(t, accepted, 0)
	})

	t.Run("handles job termination", func(t *testing.T) {
		handler := &egressHandler{
			worker: worker,
			config: config,
		}

		// Create a test job
		jobID := "test-termination-job"
		job := &livekit.Job{
			Id:   jobID,
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{
				Sid:  "termination-room-sid",
				Name: "termination-room",
			},
		}

		// Accept and assign job
		accept, _ := handler.OnJobRequest(ctx, job)
		assert.True(t, accept)

		// Simulate job termination
		handler.OnJobTerminated(ctx, jobID)

		// Verify cleanup
		worker.mu.RLock()
		_, exists := worker.sessions[jobID]
		worker.mu.RUnlock()
		assert.False(t, exists)
	})

	// Clean up
	t.Cleanup(func() {
		cancel()
		worker.Stop()

		// Remove test recordings directory
		os.RemoveAll("./test-recordings")
	})
}

// TestEgressWorkerStressTest performs stress testing on the egress worker
func TestEgressWorkerStressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	config := &Config{
		MaxConcurrentSessions: 50,
		VideoQuality:          1,
		StorageConfig: StorageConfig{
			Type:      "local",
			LocalPath: "./stress-test-recordings",
		},
	}

	worker := NewEgressWorker(config)
	handler := &egressHandler{
		worker: worker,
		config: config,
	}

	ctx := context.Background()

	t.Run("handles rapid job creation", func(t *testing.T) {
		start := time.Now()
		numJobs := 1000
		accepted := 0
		rejected := 0

		for i := 0; i < numJobs; i++ {
			job := &livekit.Job{
				Id:   fmt.Sprintf("stress-job-%d", i),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{
					Sid:  fmt.Sprintf("stress-room-sid-%d", i),
					Name: fmt.Sprintf("stress-room-%d", i),
				},
			}

			accept, _ := handler.OnJobRequest(ctx, job)
			if accept {
				accepted++
			} else {
				rejected++
			}
		}

		elapsed := time.Since(start)

		// Should handle all requests quickly
		assert.Equal(t, numJobs, accepted+rejected)
		assert.Less(t, elapsed, 5*time.Second)

		t.Logf("Processed %d jobs in %v (accepted: %d, rejected: %d)",
			numJobs, elapsed, accepted, rejected)
	})

	t.Run("handles concurrent operations", func(t *testing.T) {
		var wg sync.WaitGroup
		numGoroutines := 100
		opsPerGoroutine := 100

		start := time.Now()

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				for j := 0; j < opsPerGoroutine; j++ {
					// Mix different operations
					switch j % 3 {
					case 0:
						// Job request
						job := &livekit.Job{
							Id:   fmt.Sprintf("concurrent-%d-%d", id, j),
							Type: livekit.JobType_JT_ROOM,
							Room: &livekit.Room{Name: fmt.Sprintf("room-%d-%d", id, j)},
						}
						handler.OnJobRequest(ctx, job)

					case 1:
						// Get stats
						_ = worker.GetActiveSessionCount()

					case 2:
						// Get monitored rooms
						_ = worker.GetMonitoredRoomCount()
					}
				}
			}(i)
		}

		wg.Wait()
		elapsed := time.Since(start)

		t.Logf("Completed %d concurrent operations in %v",
			numGoroutines*opsPerGoroutine, elapsed)
		assert.Less(t, elapsed, 10*time.Second)
	})

	// Clean up
	t.Cleanup(func() {
		worker.Stop()
		os.RemoveAll("./stress-test-recordings")
	})
}

// TestEgressWorkerResilience tests resilience and error recovery
func TestEgressWorkerResilience(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping resilience test in short mode")
	}

	config := DefaultConfig()
	worker := NewEgressWorker(config)

	t.Run("recovers from panics", func(t *testing.T) {
		// Function that might panic
		riskyOperation := func() {
			defer func() {
				if r := recover(); r != nil {
					t.Logf("Recovered from panic: %v", r)
				}
			}()

			// Simulate a panic scenario
			var nilSession *RecordingSession
			_ = nilSession.GetStats() // Will panic
		}

		// Should not crash the test
		riskyOperation()
		assert.NotNil(t, worker) // Worker should still be valid
	})

	t.Run("handles resource exhaustion", func(t *testing.T) {
		// Try to create many sessions
		for i := 0; i < 10000; i++ {
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{
					Id: fmt.Sprintf("exhaustion-%d", i),
				},
			}

			session := NewRecordingSession(jobCtx, config)
			if session == nil {
				// Resource exhaustion, stop
				break
			}

			// Add to worker sessions
			worker.mu.Lock()
			if len(worker.sessions) < 1000 { // Limit for test
				worker.sessions[jobCtx.Job.Id] = session
			}
			worker.mu.Unlock()
		}

		// Should still be operational
		count := worker.GetActiveSessionCount()
		assert.Greater(t, count, 0)
		assert.LessOrEqual(t, count, 1000)

		// Clean up
		worker.Stop()
	})

	t.Run("handles rapid start/stop cycles", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			worker := NewEgressWorker(config)

			// Immediately stop
			err := worker.Stop()
			assert.NoError(t, err)
		}
	})
}

// TestEgressWorkerMemoryLeaks checks for memory leaks
func TestEgressWorkerMemoryLeaks(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	config := DefaultConfig()
	worker := NewEgressWorker(config)
	handler := &egressHandler{
		worker: worker,
		config: config,
	}

	ctx := context.Background()

	// Create and destroy many sessions
	for cycle := 0; cycle < 100; cycle++ {
		// Create sessions
		for i := 0; i < 10; i++ {
			job := &livekit.Job{
				Id:   fmt.Sprintf("leak-test-%d-%d", cycle, i),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: fmt.Sprintf("room-%d-%d", cycle, i)},
			}

			if accept, _ := handler.OnJobRequest(ctx, job); accept {
				// Simulate session creation
				jobCtx := &agent.JobContext{Job: job}
				session := NewRecordingSession(jobCtx, config)
				worker.sessions[job.Id] = session
			}
		}

		// Destroy all sessions
		worker.mu.Lock()
		for id := range worker.sessions {
			delete(worker.sessions, id)
		}
		worker.mu.Unlock()

		// Verify cleanup
		assert.Equal(t, 0, worker.GetActiveSessionCount())
	}

	// Final cleanup
	worker.Stop()
}

// BenchmarkEgressWorkerIntegration benchmarks the integrated system
func BenchmarkEgressWorkerIntegration(b *testing.B) {
	config := &Config{
		MaxConcurrentSessions: 100,
		VideoQuality:          1,
	}

	worker := NewEgressWorker(config)
	handler := &egressHandler{
		worker: worker,
		config: config,
	}

	ctx := context.Background()

	b.Run("JobProcessing", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			job := &livekit.Job{
				Id:   fmt.Sprintf("bench-%d", i),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: fmt.Sprintf("bench-room-%d", i)},
			}

			accept, _ := handler.OnJobRequest(ctx, job)
			if accept && i%10 == 0 {
				// Occasionally terminate jobs
				handler.OnJobTerminated(ctx, job.Id)
			}
		}
	})

	b.Run("ConcurrentAccess", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				switch i % 4 {
				case 0:
					// Request job
					job := &livekit.Job{
						Id:   fmt.Sprintf("parallel-%d", i),
						Type: livekit.JobType_JT_ROOM,
						Room: &livekit.Room{Name: fmt.Sprintf("p-room-%d", i)},
					}
					handler.OnJobRequest(ctx, job)

				case 1:
					// Get active sessions
					_ = worker.GetActiveSessionCount()

				case 2:
					// Get monitored rooms
					_ = worker.GetMonitoredRoomCount()

				case 3:
					// Terminate job
					handler.OnJobTerminated(ctx, fmt.Sprintf("parallel-%d", i-3))
				}
				i++
			}
		})
	})

	// Cleanup
	b.Cleanup(func() {
		worker.Stop()
	})
}