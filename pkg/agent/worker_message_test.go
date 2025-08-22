package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/agent-sdk-go/internal/test/mocks"
	"github.com/livekit/protocol/livekit"
)

func TestWorker_handleServerMessage(t *testing.T) {
	// Skip tests that require sendMessage to work
	t.Skip("handleServerMessage tests require a real websocket connection")
	
	tests := []struct {
		name    string
		message *livekit.ServerMessage
		setup   func(*testableWorker)
		verify  func(*testing.T, *testableWorker)
	}{
		{
			name: "availability request",
			message: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Availability{
					Availability: &livekit.AvailabilityRequest{
						Job: &livekit.Job{
							Id:   "J_123",
							Type: livekit.JobType_JT_ROOM,
						},
					},
				},
			},
			verify: func(t *testing.T, w *testableWorker) {
				// Should have sent availability response
				messages := w.mockConn.GetWrittenMessages()
				if len(messages) != 1 {
					t.Errorf("Expected 1 message, got %d", len(messages))
				}
			},
		},
		{
			name: "job termination",
			message: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Termination{
					Termination: &livekit.JobTermination{
						JobId:  "J_123",
					},
				},
			},
			setup: func(w *testableWorker) {
				// Add an active job
				_, cancel := context.WithCancel(context.Background())
				w.mu.Lock()
				w.activeJobs["J_123"] = &activeJob{
					job: &livekit.Job{Id: "J_123"},
					cancel: cancel,
				}
				w.mu.Unlock()
			},
			verify: func(t *testing.T, w *testableWorker) {
				handler := w.handler.(*testJobHandler)
				terminations := handler.GetJobTerminations()
				if len(terminations) != 1 {
					t.Errorf("Expected 1 termination, got %d", len(terminations))
					return
				}
				if terminations[0].JobID != "J_123" {
					t.Errorf("Expected job ID J_123, got %s", terminations[0].JobID)
				}
			},
		},
		{
			name: "pong message",
			message: &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Pong{
					Pong: &livekit.WorkerPong{
						LastTimestamp: time.Now().Unix(),
						Timestamp:     time.Now().Unix(),
					},
				},
			},
			verify: func(t *testing.T, w *testableWorker) {
				// Pong should be handled silently
				messages := w.mockConn.GetWrittenMessages()
				if len(messages) != 0 {
					t.Errorf("Expected no messages, got %d", len(messages))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestJobHandler()
			worker := newTestableWorker(handler, WorkerOptions{
				JobType: livekit.JobType_JT_ROOM,
			})
			worker.injectConnection()

			if tt.setup != nil {
				tt.setup(worker)
			}

			ctx := context.Background()
			worker.handleServerMessage(ctx, tt.message)

			if tt.verify != nil {
				tt.verify(t, worker)
			}
		})
	}
}

func TestWorker_handleJobTermination(t *testing.T) {
	handler := newTestJobHandler()
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Test terminating non-existent job
	term := &livekit.JobTermination{
		JobId:  "J_UNKNOWN",
	}

	ctx := context.Background()
	worker.handleJobTermination(ctx, term)

	// Should log warning but not crash
	logger := worker.logger.(*mocks.MockLogger)
	if !logger.HasMessage("WARN", "Received termination for unknown job") {
		t.Error("Expected warning for unknown job")
	}

	// Test terminating existing job
	jobCtx, cancel := context.WithCancel(context.Background())
	worker.mu.Lock()
	worker.activeJobs["J_123"] = &activeJob{
		job:    &livekit.Job{Id: "J_123"},
		cancel: cancel,
	}
	worker.mu.Unlock()

	term2 := &livekit.JobTermination{
		JobId:  "J_123",
	}

	// Track if context was cancelled
	cancelled := make(chan struct{})
	go func() {
		<-jobCtx.Done()
		close(cancelled)
	}()

	worker.handleJobTermination(ctx, term2)

	// Context should be cancelled
	select {
	case <-cancelled:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Error("Job context was not cancelled")
	}

	// Handler should be notified
	terminations := handler.GetJobTerminations()
	if len(terminations) != 1 {
		t.Fatalf("Expected 1 termination, got %d", len(terminations))
	}

	if terminations[0].JobID != "J_123" {
		t.Errorf("Expected job ID J_123, got %s", terminations[0].JobID)
	}

	// Reason field no longer exists in JobTermination
}

func TestWorker_updateJobStatus(t *testing.T) {
	// Skip this test as sendMessage requires a real connection
	t.Skip("updateJobStatus requires a real websocket connection to send messages")
}

func TestWorker_handlePing(t *testing.T) {
	// Skip this test as it requires sendMessage to work
	t.Skip("handlePing requires a real websocket connection")
}

func TestWorker_updateLoad(t *testing.T) {
	// Skip this test as it requires sendMessage to work
	t.Skip("updateLoad requires a real websocket connection")
}