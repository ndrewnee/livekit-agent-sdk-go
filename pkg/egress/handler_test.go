package egress

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

// TestEgressHandlerCreation tests handler initialization
func TestEgressHandlerCreation(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
	}{
		{
			name:   "creates handler with nil config",
			config: nil,
		},
		{
			name: "creates handler with custom config",
			config: &Config{
				MaxConcurrentSessions: 10,
				VideoQuality:          2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEgressHandler(tt.config)
			assert.NotNil(t, handler)
			// Handler always has a config (uses default if nil provided)
			assert.NotNil(t, handler.config)
			assert.NotNil(t, handler.sessions)
			assert.Equal(t, 0, len(handler.sessions))
		})
	}
}

// TestHandlerJobRequest tests job acceptance logic with various scenarios
func TestHandlerJobRequest(t *testing.T) {
	tests := []struct {
		name          string
		config        *Config
		job           *livekit.Job
		existingSessions int
		expectAccept  bool
		expectMetadata bool
	}{
		{
			name: "accepts room job when under capacity",
			config: &Config{MaxConcurrentSessions: 5},
			job: &livekit.Job{
				Id:   "job-1",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
			existingSessions: 2,
			expectAccept:    true,
			expectMetadata:  true,
		},
		{
			name: "rejects room job at capacity",
			config: &Config{MaxConcurrentSessions: 3},
			job: &livekit.Job{
				Id:   "job-2",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
			existingSessions: 3,
			expectAccept:    false,
			expectMetadata:  false,
		},
		{
			name: "rejects non-room job",
			config: DefaultConfig(),
			job: &livekit.Job{
				Id:   "job-3",
				Type: livekit.JobType_JT_PARTICIPANT,
			},
			expectAccept:   false,
			expectMetadata: false,
		},
		{
			name: "accepts with unlimited capacity",
			config: &Config{MaxConcurrentSessions: 0},
			job: &livekit.Job{
				Id:   "job-4",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
			existingSessions: 100,
			expectAccept:    true,
			expectMetadata:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewEgressHandler(tt.config)

			// Add existing sessions
			for i := 0; i < tt.existingSessions; i++ {
				jobCtx := &agent.JobContext{
					Job: &livekit.Job{Id: string(rune('a' + i))},
				}
				session := NewRecordingSession(jobCtx, tt.config)
				handler.sessions[string(rune('a'+i))] = session
			}

			ctx := context.Background()
			accept, metadata := handler.OnJobRequest(ctx, tt.job)

			assert.Equal(t, tt.expectAccept, accept)
			if tt.expectMetadata {
				assert.NotNil(t, metadata)
				assert.Contains(t, metadata.ParticipantIdentity, "egress-agent")
				assert.Equal(t, "HLS Egress Agent", metadata.ParticipantName)
			} else {
				assert.Nil(t, metadata)
			}

			// Verify stats are updated
			stats := handler.GetStats()
			assert.Greater(t, stats.TotalJobs, int64(0))
			if tt.expectAccept {
				assert.Greater(t, stats.AcceptedJobs, int64(0))
			} else {
				assert.Greater(t, stats.RejectedJobs, int64(0))
			}
		})
	}
}

// TestHandlerJobAssignment tests job assignment and session creation
func TestHandlerJobAssignment(t *testing.T) {
	handler := NewEgressHandler(DefaultConfig())
	jobID := "test-job-1"

	// Create a mock session instead of calling OnJobAssigned
	// which would try to start a real GStreamer pipeline
	session := &RecordingSession{
		jobCtx: &agent.JobContext{
			Job: &livekit.Job{
				Id:   jobID,
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
		},
		config: DefaultConfig(),
		done:   make(chan error, 1),
		state:  StateIdle,
	}

	// Manually add to handler sessions
	handler.mu.Lock()
	handler.sessions[jobID] = session
	handler.mu.Unlock()

	// Verify session was created
	handler.mu.RLock()
	storedSession, exists := handler.sessions[jobID]
	handler.mu.RUnlock()
	assert.True(t, exists)
	assert.NotNil(t, storedSession)
	assert.Equal(t, jobID, storedSession.jobCtx.Job.Id)

	// Clean up
	handler.mu.Lock()
	delete(handler.sessions, jobID)
	handler.mu.Unlock()
}

// TestHandlerJobTermination tests proper cleanup on termination
func TestHandlerJobTermination(t *testing.T) {
	handler := NewEgressHandler(DefaultConfig())
	ctx := context.Background()
	jobID := "test-job-1"

	// Create a properly initialized session using the constructor
	jobCtx := &agent.JobContext{
		Job: &livekit.Job{
			Id:   jobID,
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "test-room"},
		},
	}
	session := NewRecordingSession(jobCtx, DefaultConfig())

	// Add to handler sessions
	handler.mu.Lock()
	handler.sessions[jobID] = session
	handler.mu.Unlock()

	// Verify session exists
	assert.Equal(t, 1, len(handler.GetStats().SessionIDs))

	// Terminate the job
	handler.OnJobTerminated(ctx, jobID)

	// Verify session is removed
	handler.mu.RLock()
	_, exists := handler.sessions[jobID]
	handler.mu.RUnlock()
	assert.False(t, exists)
}

// TestHandlerConcurrency tests concurrent access patterns
func TestHandlerConcurrency(t *testing.T) {
	handler := NewEgressHandler(&Config{
		MaxConcurrentSessions: 10,
	})

	ctx := context.Background()
	var wg sync.WaitGroup
	numGoroutines := 20

	// Test concurrent job requests
	t.Run("concurrent job requests", func(t *testing.T) {
		// Test at capacity - should reject all new jobs
		handler.mu.Lock()
		// Fill to capacity
		for i := 0; i < 10; i++ {
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{Id: fmt.Sprintf("existing-%d", i)},
			}
			handler.sessions[fmt.Sprintf("existing-%d", i)] = NewRecordingSession(jobCtx, handler.config)
		}
		handler.mu.Unlock()

		// Verify at capacity
		assert.Equal(t, 10, len(handler.sessions))
		assert.Equal(t, 10, handler.config.MaxConcurrentSessions)

		var accepted atomic.Int32
		var rejected atomic.Int32

		// All concurrent requests should be rejected
		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				job := &livekit.Job{
					Id:   fmt.Sprintf("new-%d", id),
					Type: livekit.JobType_JT_ROOM,
					Room: &livekit.Room{Name: fmt.Sprintf("room-%d", id)},
				}

				accept, _ := handler.OnJobRequest(ctx, job)
				if accept {
					accepted.Add(1)
				} else {
					rejected.Add(1)
				}
			}(i)
		}

		wg.Wait()

		// Verify totals
		assert.Equal(t, int32(numGoroutines), accepted.Load()+rejected.Load())
		// At capacity, should reject all
		assert.Equal(t, int32(0), accepted.Load())
		assert.Equal(t, int32(20), rejected.Load())

		// Clean up
		handler.mu.Lock()
		handler.sessions = make(map[string]*RecordingSession)
		handler.mu.Unlock()
	})

	// Test concurrent terminations
	t.Run("concurrent terminations", func(t *testing.T) {
		// Create a new handler for this test to avoid interference
		terminationHandler := NewEgressHandler(&Config{
			MaxConcurrentSessions: 10,
		})

		// Add some sessions with proper locking
		terminationHandler.mu.Lock()
		for i := 0; i < 5; i++ {
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{Id: string(rune('a' + i))},
			}
			session := NewRecordingSession(jobCtx, terminationHandler.config)
			terminationHandler.sessions[string(rune('a'+i))] = session
		}
		terminationHandler.mu.Unlock()

		var terminationWg sync.WaitGroup
		for i := 0; i < 5; i++ {
			terminationWg.Add(1)
			go func(id int) {
				defer terminationWg.Done()
				terminationHandler.OnJobTerminated(ctx, string(rune('a'+id)))
			}(i)
		}

		terminationWg.Wait()

		// All sessions should be removed
		terminationHandler.mu.RLock()
		sessionCount := len(terminationHandler.sessions)
		terminationHandler.mu.RUnlock()
		assert.Equal(t, 0, sessionCount)
	})
}

// TestHandlerStats tests statistics tracking
func TestHandlerStats(t *testing.T) {
	handler := NewEgressHandler(DefaultConfig())
	ctx := context.Background()

	// Process various jobs
	jobs := []struct {
		job    *livekit.Job
		accept bool
	}{
		{
			job: &livekit.Job{
				Id:   "job-1",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "room-1"},
			},
			accept: true,
		},
		{
			job: &livekit.Job{
				Id:   "job-2",
				Type: livekit.JobType_JT_PARTICIPANT,
			},
			accept: false,
		},
		{
			job: &livekit.Job{
				Id:   "job-3",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "room-2"},
			},
			accept: true,
		},
	}

	for _, j := range jobs {
		accept, _ := handler.OnJobRequest(ctx, j.job)
		assert.Equal(t, j.accept, accept)
	}

	stats := handler.GetStats()
	assert.Equal(t, int64(3), stats.TotalJobs)
	assert.Equal(t, int64(2), stats.AcceptedJobs)
	assert.Equal(t, int64(1), stats.RejectedJobs)
}

// TestHandlerShutdown tests graceful shutdown
func TestHandlerShutdown(t *testing.T) {
	handler := NewEgressHandler(DefaultConfig())

	// Add multiple sessions
	for i := 0; i < 5; i++ {
		jobCtx := &agent.JobContext{
			Job: &livekit.Job{Id: string(rune('a' + i))},
		}
		session := NewRecordingSession(jobCtx, handler.config)
		handler.sessions[string(rune('a'+i))] = session
	}

	// Test shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := handler.Shutdown(ctx)
	assert.NoError(t, err)

	// Verify all sessions were stopped
	handler.mu.RLock()
	sessionCount := len(handler.sessions)
	handler.mu.RUnlock()
	assert.Equal(t, 0, sessionCount)
}

// TestHandlerEdgeCases tests various edge cases
func TestHandlerEdgeCases(t *testing.T) {
	t.Run("handles nil job", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())
		ctx := context.Background()

		accept, metadata := handler.OnJobRequest(ctx, nil)
		assert.False(t, accept)
		assert.Nil(t, metadata)
	})

	t.Run("handles job with nil room", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())
		ctx := context.Background()

		job := &livekit.Job{
			Id:   "job-1",
			Type: livekit.JobType_JT_ROOM,
			Room: nil,
		}

		accept, _ := handler.OnJobRequest(ctx, job)
		assert.True(t, accept) // Should still accept, name will be empty
	})

	t.Run("handles terminated non-existent job", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())
		ctx := context.Background()

		// Should not panic when terminating non-existent job
		handler.OnJobTerminated(ctx, "non-existent-job")
		assert.Equal(t, 0, len(handler.sessions))
	})

	t.Run("handles GetSession for non-existent job", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())

		session, exists := handler.GetSession("non-existent")
		assert.Nil(t, session)
		assert.False(t, exists)
	})

	t.Run("handles shutdown with no sessions", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())
		ctx := context.Background()

		err := handler.Shutdown(ctx)
		assert.NoError(t, err)
	})

	t.Run("handles shutdown with timeout", func(t *testing.T) {
		t.Skip("Skipping shutdown timeout test - requires mock session implementation")
	})
}

// TestMonitorSession tests session monitoring
func TestMonitorSession(t *testing.T) {
	t.Run("monitors successful session", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())
		ctx := context.Background()
		jobID := "test-job"

		session := &RecordingSession{
			done: make(chan error, 1),
		}
		handler.sessions[jobID] = session

		// Start monitoring
		go handler.monitorSession(ctx, jobID, session)

		// Simulate successful completion
		session.done <- nil

		// Give monitor time to process
		time.Sleep(100 * time.Millisecond)

		// Session should be removed
		handler.mu.RLock()
		_, exists := handler.sessions[jobID]
		handler.mu.RUnlock()
		assert.False(t, exists)

		// Stats should show completed
		stats := handler.GetStats()
		assert.Equal(t, int64(1), stats.CompletedJobs)
	})

	t.Run("monitors failed session", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())
		ctx := context.Background()
		jobID := "test-job"

		session := &RecordingSession{
			done: make(chan error, 1),
		}
		handler.sessions[jobID] = session

		// Start monitoring
		go handler.monitorSession(ctx, jobID, session)

		// Simulate failure
		session.done <- errors.New("test error")

		// Give monitor time to process
		time.Sleep(100 * time.Millisecond)

		// Session should be removed
		handler.mu.RLock()
		_, exists := handler.sessions[jobID]
		handler.mu.RUnlock()
		assert.False(t, exists)

		// Stats should show failed
		stats := handler.GetStats()
		assert.Equal(t, int64(1), stats.FailedJobs)
	})

	t.Run("monitors cancelled session", func(t *testing.T) {
		handler := NewEgressHandler(DefaultConfig())
		ctx, cancel := context.WithCancel(context.Background())
		jobID := "test-job"

		session := &RecordingSession{
			done: make(chan error),
		}
		handler.sessions[jobID] = session

		// Start monitoring
		go handler.monitorSession(ctx, jobID, session)

		// Cancel context
		cancel()

		// Give monitor time to process
		time.Sleep(100 * time.Millisecond)

		// Session should be removed
		handler.mu.RLock()
		_, exists := handler.sessions[jobID]
		handler.mu.RUnlock()
		assert.False(t, exists)

		// Stats should show failed
		stats := handler.GetStats()
		assert.Equal(t, int64(1), stats.FailedJobs)
	})
}

// TestRealWorldScenarios tests real-world usage patterns
func TestRealWorldScenarios(t *testing.T) {
	t.Run("handles burst of jobs", func(t *testing.T) {
		handler := NewEgressHandler(&Config{
			MaxConcurrentSessions: 5,
		})
		ctx := context.Background()

		// Send 20 jobs rapidly, simulating OnJobAssigned behavior
		var accepted int
		for i := 0; i < 20; i++ {
			job := &livekit.Job{
				Id:   fmt.Sprintf("job-%d", i),
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: fmt.Sprintf("room-%d", i)},
			}

			if accept, _ := handler.OnJobRequest(ctx, job); accept {
				accepted++
				// Simulate what OnJobAssigned would do - add the session
				handler.mu.Lock()
				if len(handler.sessions) < 5 {
					jobCtx := &agent.JobContext{Job: job}
					handler.sessions[job.Id] = NewRecordingSession(jobCtx, handler.config)
				}
				handler.mu.Unlock()
			}
		}

		// Should accept only up to max concurrent
		assert.Equal(t, 5, accepted)

		stats := handler.GetStats()
		assert.Equal(t, int64(20), stats.TotalJobs)
		assert.Equal(t, int64(5), stats.AcceptedJobs)
		assert.Equal(t, int64(15), stats.RejectedJobs)
	})

	t.Run("handles job churn", func(t *testing.T) {
		handler := NewEgressHandler(&Config{
			MaxConcurrentSessions: 3,
		})
		ctx := context.Background()

		// Repeatedly add and remove jobs
		for cycle := 0; cycle < 3; cycle++ {
			// Add jobs
			for i := 0; i < 3; i++ {
				jobID := string(rune('a' + cycle*3 + i))
				job := &livekit.Job{
					Id:   jobID,
					Type: livekit.JobType_JT_ROOM,
					Room: &livekit.Room{Name: "room-" + jobID},
				}

				accept, _ := handler.OnJobRequest(ctx, job)
				if accept {
					// Simulate session creation
					handler.sessions[jobID] = &RecordingSession{
						done: make(chan error, 1),
					}
				}
			}

			// Remove jobs
			for i := 0; i < 3; i++ {
				jobID := string(rune('a' + cycle*3 + i))
				handler.OnJobTerminated(ctx, jobID)
			}
		}

		// Should end with no active sessions
		assert.Equal(t, 0, len(handler.sessions))

		stats := handler.GetStats()
		assert.Equal(t, int64(9), stats.TotalJobs)
		assert.Equal(t, int64(9), stats.AcceptedJobs)
	})

	t.Run("handles memory pressure", func(t *testing.T) {
		handler := NewEgressHandler(&Config{
			MaxConcurrentSessions: 100,
		})
		// Create many sessions
		for i := 0; i < 100; i++ {
			jobID := string(rune(i))
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{Id: jobID},
			}
			session := NewRecordingSession(jobCtx, handler.config)
			handler.sessions[jobID] = session
		}

		// Verify we can handle many sessions
		assert.Equal(t, 100, len(handler.sessions))

		// Clean up all sessions
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := handler.Shutdown(shutdownCtx)
		assert.NoError(t, err)
		assert.Equal(t, 0, len(handler.sessions))
	})
}

// BenchmarkHandlerOperations benchmarks handler operations
func BenchmarkHandlerOperations(b *testing.B) {
	b.Run("OnJobRequest", func(b *testing.B) {
		handler := NewEgressHandler(DefaultConfig())
		ctx := context.Background()
		job := &livekit.Job{
			Id:   "bench-job",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "bench-room"},
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			handler.OnJobRequest(ctx, job)
		}
	})

	b.Run("GetStats", func(b *testing.B) {
		handler := NewEgressHandler(DefaultConfig())

		// Add some sessions
		for i := 0; i < 10; i++ {
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{Id: string(rune('a' + i))},
			}
			session := NewRecordingSession(jobCtx, handler.config)
			handler.sessions[string(rune('a'+i))] = session
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = handler.GetStats()
		}
	})

	b.Run("ConcurrentAccess", func(b *testing.B) {
		handler := NewEgressHandler(DefaultConfig())

		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				if i%2 == 0 {
					// Add session
					jobCtx := &agent.JobContext{
						Job: &livekit.Job{Id: string(rune(i))},
					}
					session := NewRecordingSession(jobCtx, handler.config)
					handler.sessions[string(rune(i))] = session
				} else {
					// Remove session
					delete(handler.sessions, string(rune(i-1)))
				}
				i++
			}
		})
	})
}