package egress

import (
	"context"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEgressWorkerCreation tests worker initialization
func TestEgressWorkerCreation(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		want   *EgressWorker
	}{
		{
			name:   "creates worker with nil config",
			config: nil,
			want: &EgressWorker{
				config:         DefaultConfig(),
				sessions:       make(map[string]*RecordingSession),
				monitoredRooms: make(map[string]*roomMonitor),
			},
		},
		{
			name: "creates worker with custom config",
			config: &Config{
				MaxConcurrentSessions: 5,
				VideoQuality:          2,
			},
			want: &EgressWorker{
				config: &Config{
					MaxConcurrentSessions: 5,
					VideoQuality:          2,
				},
				sessions:       make(map[string]*RecordingSession),
				monitoredRooms: make(map[string]*roomMonitor),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := NewEgressWorker(tt.config)
			assert.NotNil(t, worker)
			assert.NotNil(t, worker.config)
			assert.NotNil(t, worker.sessions)
			assert.NotNil(t, worker.monitoredRooms)

			if tt.config != nil {
				assert.Equal(t, tt.config.MaxConcurrentSessions, worker.config.MaxConcurrentSessions)
				assert.Equal(t, tt.config.VideoQuality, worker.config.VideoQuality)
			}
		})
	}
}

// TestEgressHandlerJobRequest tests job acceptance logic
func TestEgressHandlerJobRequest(t *testing.T) {
	tests := []struct {
		name           string
		job            *livekit.Job
		currentSessions int
		maxSessions    int
		wantAccept     bool
		wantMetadata   bool
	}{
		{
			name: "accepts room job when under capacity",
			job: &livekit.Job{
				Id:   "job-1",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
			currentSessions: 2,
			maxSessions:    5,
			wantAccept:     true,
			wantMetadata:   true,
		},
		{
			name: "rejects room job when at capacity",
			job: &livekit.Job{
				Id:   "job-2",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
			currentSessions: 5,
			maxSessions:    5,
			wantAccept:     false,
			wantMetadata:   false,
		},
		{
			name: "accepts participant job",
			job: &livekit.Job{
				Id:   "job-3",
				Type: livekit.JobType_JT_PARTICIPANT,
				Participant: &livekit.ParticipantInfo{
					Identity: "user-1",
					Name:     "User 1",
				},
			},
			currentSessions: 0,
			maxSessions:    10,
			wantAccept:     true,
			wantMetadata:   true,
		},
		{
			name: "rejects publisher job",
			job: &livekit.Job{
				Id:   "job-4",
				Type: livekit.JobType_JT_PUBLISHER,
			},
			currentSessions: 0,
			maxSessions:    10,
			wantAccept:     false,
			wantMetadata:   false,
		},
		{
			name: "accepts with unlimited sessions (max=0)",
			job: &livekit.Job{
				Id:   "job-5",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
			currentSessions: 100,
			maxSessions:    0, // unlimited
			wantAccept:     true,
			wantMetadata:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worker := NewEgressWorker(&Config{
				MaxConcurrentSessions: tt.maxSessions,
			})

			// Simulate current sessions
			for i := 0; i < tt.currentSessions; i++ {
				worker.sessions[string(rune(i))] = &RecordingSession{}
			}

			handler := &egressHandler{
				worker: worker,
				config: worker.config,
			}

			ctx := context.Background()
			accept, metadata := handler.OnJobRequest(ctx, tt.job)

			assert.Equal(t, tt.wantAccept, accept)
			if tt.wantMetadata {
				assert.NotNil(t, metadata)
				assert.NotEmpty(t, metadata.ParticipantName)
				assert.NotEmpty(t, metadata.ParticipantIdentity)
			} else {
				assert.Nil(t, metadata)
			}
		})
	}
}

// TestRoomMonitoring tests room monitoring functionality
func TestRoomMonitoring(t *testing.T) {
	worker := NewEgressWorker(DefaultConfig())
	handler := &egressHandler{
		worker: worker,
		config: worker.config,
	}

	ctx := context.Background()

	// Test adding a room for monitoring
	jobCtx := &agent.JobContext{
		Job: &livekit.Job{
			Id:   "room-job-1",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "test-room-1"},
		},
		Room: &lksdk.Room{}, // Mock room
	}

	err := handler.handleRoomJob(ctx, jobCtx)
	require.NoError(t, err)

	// Verify room is being monitored
	worker.roomsMu.RLock()
	monitor, exists := worker.monitoredRooms["test-room-1"]
	worker.roomsMu.RUnlock()

	assert.True(t, exists)
	assert.NotNil(t, monitor)
	assert.Equal(t, "test-room-1", monitor.roomName)
	assert.NotNil(t, monitor.participants)

	// Test adding same room again (should reuse existing monitor)
	jobCtx2 := &agent.JobContext{
		Job: &livekit.Job{
			Id:   "room-job-2",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "test-room-1"},
		},
		Room: &lksdk.Room{},
	}

	err = handler.handleRoomJob(ctx, jobCtx2)
	require.NoError(t, err)

	// Should still have only one monitor for the room
	assert.Equal(t, 1, worker.GetMonitoredRoomCount())
}

// TestParticipantTracking tests participant join/leave handling
func TestParticipantTracking(t *testing.T) {
	worker := NewEgressWorker(DefaultConfig())
	handler := &egressHandler{
		worker: worker,
		config: worker.config,
	}

	ctx := context.Background()

	// Set up a monitored room
	monitor := &roomMonitor{
		roomName:     "test-room",
		participants: make(map[string]bool),
	}
	worker.monitoredRooms["test-room"] = monitor

	// Test participant join
	participant := &lksdk.RemoteParticipant{}
	// Note: In real implementation, we'd need to properly mock the participant
	// with SID and Identity methods

	handler.OnParticipantJoined(ctx, participant)

	// Test participant leave
	handler.OnParticipantLeft(ctx, participant)

	// Verify participant is removed from tracking
	monitor.mu.RLock()
	count := len(monitor.participants)
	monitor.mu.RUnlock()
	assert.Equal(t, 0, count)
}

// TestConcurrentSessionManagement tests concurrent session handling
func TestConcurrentSessionManagement(t *testing.T) {
	worker := NewEgressWorker(&Config{
		MaxConcurrentSessions: 3,
	})

	// Test adding sessions concurrently
	done := make(chan bool, 5)

	for i := 0; i < 5; i++ {
		go func(id int) {
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{
					Id:   string(rune('a' + id)),
					Type: livekit.JobType_JT_PARTICIPANT,
					Participant: &livekit.ParticipantInfo{
						Identity: string(rune('a' + id)),
					},
				},
			}

			session := NewRecordingSession(jobCtx, worker.config)

			worker.mu.Lock()
			if len(worker.sessions) < worker.config.MaxConcurrentSessions {
				worker.sessions[jobCtx.Job.Id] = session
			}
			worker.mu.Unlock()

			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 5; i++ {
		<-done
	}

	// Verify we don't exceed max sessions
	assert.LessOrEqual(t, worker.GetActiveSessionCount(), 3)
}

// TestJobTermination tests proper cleanup on job termination
func TestJobTermination(t *testing.T) {
	worker := NewEgressWorker(DefaultConfig())
	handler := &egressHandler{
		worker: worker,
		config: worker.config,
	}

	ctx := context.Background()
	jobID := "test-job-1"

	// Create a mock session
	jobCtx := &agent.JobContext{
		Job: &livekit.Job{
			Id: jobID,
		},
	}
	session := NewRecordingSession(jobCtx, worker.config)
	worker.sessions[jobID] = session

	// Verify session exists
	assert.Equal(t, 1, worker.GetActiveSessionCount())

	// Terminate the job
	handler.OnJobTerminated(ctx, jobID)

	// Verify session is cleaned up
	worker.mu.RLock()
	_, exists := worker.sessions[jobID]
	worker.mu.RUnlock()
	assert.False(t, exists)
}

// TestWorkerShutdown tests graceful shutdown
func TestWorkerShutdown(t *testing.T) {
	worker := NewEgressWorker(DefaultConfig())

	// Add some active sessions
	for i := 0; i < 3; i++ {
		jobCtx := &agent.JobContext{
			Job: &livekit.Job{
				Id: string(rune('a' + i)),
			},
		}
		session := NewRecordingSession(jobCtx, worker.config)
		worker.sessions[jobCtx.Job.Id] = session
	}

	// Test shutdown
	err := worker.Stop()
	assert.NoError(t, err)

	// Verify all sessions are stopped
	// Note: In real implementation, we'd verify Stop() was called on each session
}

// TestEdgeCases tests various edge cases
func TestEdgeCases(t *testing.T) {
	t.Run("handles nil job gracefully", func(t *testing.T) {
		worker := NewEgressWorker(DefaultConfig())
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		ctx := context.Background()
		accept, metadata := handler.OnJobRequest(ctx, nil)
		assert.False(t, accept)
		assert.Nil(t, metadata)
	})

	t.Run("handles job with nil room", func(t *testing.T) {
		worker := NewEgressWorker(DefaultConfig())
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		job := &livekit.Job{
			Id:   "job-1",
			Type: livekit.JobType_JT_ROOM,
			Room: nil,
		}

		ctx := context.Background()
		accept, _ := handler.OnJobRequest(ctx, job)
		assert.True(t, accept) // Should still accept, will handle nil check later
	})

	t.Run("handles job with nil participant", func(t *testing.T) {
		worker := NewEgressWorker(DefaultConfig())
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		job := &livekit.Job{
			Id:          "job-1",
			Type:        livekit.JobType_JT_PARTICIPANT,
			Participant: nil,
		}

		ctx := context.Background()
		accept, _ := handler.OnJobRequest(ctx, job)
		assert.True(t, accept) // Should still accept, will handle nil check later
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		worker := NewEgressWorker(DefaultConfig())
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		jobCtx := &agent.JobContext{
			Job: &livekit.Job{
				Id:   "job-1",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "test-room"},
			},
		}

		// Should handle cancelled context gracefully
		err := handler.handleRoomJob(ctx, jobCtx)
		assert.NoError(t, err) // Room monitoring setup should succeed even with cancelled context
	})

	t.Run("handles rapid start/stop", func(t *testing.T) {
		worker := NewEgressWorker(DefaultConfig())

		// Rapidly start and stop
		for i := 0; i < 10; i++ {
			go func() {
				worker.Stop()
			}()
		}

		// Should not panic or deadlock
		time.Sleep(100 * time.Millisecond)
	})
}

// TestRealLifeScenarios tests real-world usage patterns
func TestRealLifeScenarios(t *testing.T) {
	t.Run("multiple participants join simultaneously", func(t *testing.T) {
		worker := NewEgressWorker(&Config{
			MaxConcurrentSessions: 10,
		})
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		// Set up monitored room
		monitor := &roomMonitor{
			roomName:     "conference-room",
			participants: make(map[string]bool),
		}
		worker.monitoredRooms["conference-room"] = monitor

		ctx := context.Background()

		// Simulate 5 participants joining at once
		done := make(chan bool, 5)
		for i := 0; i < 5; i++ {
			go func(id int) {
				participant := &lksdk.RemoteParticipant{}
				handler.OnParticipantJoined(ctx, participant)
				done <- true
			}(i)
		}

		// Wait for all to complete
		for i := 0; i < 5; i++ {
			<-done
		}

		// Should handle all participants without issues
	})

	t.Run("participant reconnection", func(t *testing.T) {
		worker := NewEgressWorker(DefaultConfig())
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		monitor := &roomMonitor{
			roomName:     "test-room",
			participants: make(map[string]bool),
		}
		worker.monitoredRooms["test-room"] = monitor

		ctx := context.Background()
		participant := &lksdk.RemoteParticipant{}

		// Participant joins
		handler.OnParticipantJoined(ctx, participant)

		// Participant disconnects
		handler.OnParticipantLeft(ctx, participant)

		// Participant reconnects
		handler.OnParticipantJoined(ctx, participant)

		// Should handle reconnection gracefully
	})

	t.Run("room with no participants", func(t *testing.T) {
		worker := NewEgressWorker(DefaultConfig())
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		ctx := context.Background()
		jobCtx := &agent.JobContext{
			Job: &livekit.Job{
				Id:   "empty-room-job",
				Type: livekit.JobType_JT_ROOM,
				Room: &livekit.Room{Name: "empty-room"},
			},
			Room: &lksdk.Room{},
		}

		// Monitor empty room
		err := handler.handleRoomJob(ctx, jobCtx)
		assert.NoError(t, err)

		// Should handle empty room without issues
		assert.Equal(t, 1, worker.GetMonitoredRoomCount())
		assert.Equal(t, 0, worker.GetActiveSessionCount())
	})

	t.Run("job recovery after crash", func(t *testing.T) {
		// Simulate worker crash and recovery
		worker1 := NewEgressWorker(DefaultConfig())

		// Add some sessions
		for i := 0; i < 3; i++ {
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{
					Id: string(rune('a' + i)),
				},
			}
			session := NewRecordingSession(jobCtx, worker1.config)
			worker1.sessions[jobCtx.Job.Id] = session
		}

		// Simulate crash - worker stops
		worker1.Stop()

		// Create new worker instance (simulating restart)
		worker2 := NewEgressWorker(DefaultConfig())

		// New worker should start fresh
		assert.Equal(t, 0, worker2.GetActiveSessionCount())
		assert.Equal(t, 0, worker2.GetMonitoredRoomCount())
	})
}

// BenchmarkWorkerOperations benchmarks common operations
func BenchmarkWorkerOperations(b *testing.B) {
	b.Run("JobRequest", func(b *testing.B) {
		worker := NewEgressWorker(DefaultConfig())
		handler := &egressHandler{
			worker: worker,
			config: worker.config,
		}

		job := &livekit.Job{
			Id:   "bench-job",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "bench-room"},
		}

		ctx := context.Background()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			handler.OnJobRequest(ctx, job)
		}
	})

	b.Run("SessionManagement", func(b *testing.B) {
		worker := NewEgressWorker(DefaultConfig())

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			jobCtx := &agent.JobContext{
				Job: &livekit.Job{
					Id: string(rune(i % 256)),
				},
			}
			session := NewRecordingSession(jobCtx, worker.config)

			worker.mu.Lock()
			worker.sessions[jobCtx.Job.Id] = session
			worker.mu.Unlock()

			worker.mu.Lock()
			delete(worker.sessions, jobCtx.Job.Id)
			worker.mu.Unlock()
		}
	})
}