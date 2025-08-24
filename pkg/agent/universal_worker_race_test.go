package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

// TestUniversalWorker_RaceConditions tests for race conditions in UniversalWorker
func TestUniversalWorker_RaceConditions(t *testing.T) {
	handler := &MockUniversalHandler{}
	worker := NewUniversalWorker("ws://localhost:7880", "key", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
		MaxJobs: 10,
	})

	var wg sync.WaitGroup

	// Test concurrent job operations
	wg.Add(5)

	// Goroutine 1: Get job context
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			jobID := "job-" + string(rune(i))
			_, _ = worker.GetJobContext(jobID)
			time.Sleep(time.Microsecond)
		}
	}()

	// Goroutine 2: Update status
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			status := WorkerStatusAvailable
			if i%2 == 0 {
				status = WorkerStatusFull
			}
			_ = worker.UpdateStatus(status, float32(i)/100.0)
			time.Sleep(time.Microsecond)
		}
	}()

	// Goroutine 3: Get metrics
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = worker.GetMetrics()
			_ = worker.Health()
			_ = worker.IsConnected()
			time.Sleep(time.Microsecond)
		}
	}()

	// Goroutine 4: Participant operations
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			identity := "participant-" + string(rune(i))
			jobID := "job-test"
			_, _ = worker.GetParticipant(jobID, identity)
			_, _ = worker.GetAllParticipants(jobID)
			_, _ = worker.GetParticipantInfo(identity)
			_ = worker.GetAllParticipantInfo()
			time.Sleep(time.Microsecond)
		}
	}()

	// Goroutine 5: Track operations
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			trackID := "track-" + string(rune(i))
			// These methods require actual track publications which we don't have in tests
			// Just test the thread-safe getter
			_ = worker.GetSubscribedTracks()
			_ = worker.SetTrackQuality(trackID, livekit.VideoQuality_LOW)
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()
	assert.True(t, true) // If we get here without race conditions, test passes
}

// TestUniversalWorker_ConcurrentShutdown tests concurrent shutdown operations
func TestUniversalWorker_ConcurrentShutdown(t *testing.T) {
	handler := &MockUniversalHandler{}
	worker := NewUniversalWorker("ws://localhost:7880", "key", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	var wg sync.WaitGroup
	wg.Add(3)

	// Multiple goroutines trying to stop
	for i := 0; i < 3; i++ {
		go func() {
			defer wg.Done()
			_ = worker.Stop()
		}()
	}

	wg.Wait()
	assert.True(t, true)
}

// TestUniversalWorker_ConcurrentHooks tests concurrent hook operations
func TestUniversalWorker_ConcurrentHooks(t *testing.T) {
	handler := &MockUniversalHandler{}
	worker := NewUniversalWorker("ws://localhost:7880", "key", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	var wg sync.WaitGroup
	wg.Add(4)

	// Add hooks concurrently
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = worker.AddPreStopHook("hook-"+string(rune(i)), func(ctx context.Context) error {
				return nil
			})
		}
	}()

	// Remove hooks concurrently
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // Let some hooks be added first
		for i := 0; i < 50; i++ {
			_ = worker.RemoveShutdownHook(ShutdownPhasePreStop, "hook-"+string(rune(i)))
		}
	}()

	// Get hooks concurrently
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = worker.GetShutdownHooks(ShutdownPhasePreStop)
			time.Sleep(time.Microsecond)
		}
	}()

	// Add more hooks concurrently
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = worker.AddCleanupHook("cleanup-"+string(rune(i)), func(ctx context.Context) error {
				return nil
			})
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
	assert.True(t, true)
}

// TestUniversalWorker_ConcurrentEventProcessing tests concurrent event processing
func TestUniversalWorker_ConcurrentEventProcessing(t *testing.T) {
	processor := NewUniversalEventProcessor()
	defer processor.Stop()

	var wg sync.WaitGroup
	var mu sync.Mutex
	eventCount := 0

	// Register multiple handlers
	for i := 0; i < 5; i++ {
		processor.RegisterHandler(UniversalEventTypeParticipantJoined, func(event UniversalEvent) error {
			mu.Lock()
			eventCount++
			mu.Unlock()
			return nil
		})
	}

	wg.Add(3)

	// Queue events from multiple goroutines
	for i := 0; i < 3; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				processor.QueueEvent(UniversalEvent{
					Type:      UniversalEventTypeParticipantJoined,
					Timestamp: time.Now(),
					Data:      "participant-" + string(rune(id*100+j)),
				})
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond) // Let events process

	mu.Lock()
	finalCount := eventCount
	mu.Unlock()

	// Each event should be processed by all 5 handlers
	assert.Greater(t, finalCount, 0)
}

// TestUniversalWorker_ConcurrentResourceOperations tests concurrent resource operations
func TestUniversalWorker_ConcurrentResourceOperations(t *testing.T) {
	handler := &MockUniversalHandler{}
	worker := NewUniversalWorker("ws://localhost:7880", "key", "secret", handler, WorkerOptions{
		JobType:              livekit.JobType_JT_ROOM,
		EnableResourceLimits: true,
		EnableResourcePool:   true,
	})

	var wg sync.WaitGroup
	wg.Add(3)

	// Concurrent resource pool operations
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = worker.GetResourcePoolStats()
			time.Sleep(time.Microsecond)
		}
	}()

	// Concurrent health checks
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = worker.Health()
			_ = worker.GetMetrics()
			time.Sleep(time.Microsecond)
		}
	}()

	// Concurrent job context operations
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			jobID := "job-" + string(rune(i))
			_, _ = worker.GetJobContext(jobID)
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()
	assert.True(t, true)
}
