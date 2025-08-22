package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"google.golang.org/protobuf/proto"
)

// mockRoom implements a minimal mock of lksdk.Room for testing
type mockRoom struct {
	name         string
	disconnected bool
	mu           sync.Mutex
}

func newMockRoom(name string) *mockRoom {
	return &mockRoom{name: name}
}

func (m *mockRoom) Name() string {
	return m.name
}

func (m *mockRoom) Disconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnected = true
}

func (m *mockRoom) IsDisconnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disconnected
}

func TestWorker_handleJobAssignment(t *testing.T) {
	// Skip this test as it requires a real room connection
	t.Skip("handleJobAssignment requires a real room connection which can't be easily mocked")
	
	tests := []struct {
		name           string
		assignment     *livekit.JobAssignment
		handlerError   error
		expectStatus   livekit.JobStatus
		expectErrorMsg string
	}{
		{
			name: "successful job assignment",
			assignment: &livekit.JobAssignment{
				Job: &livekit.Job{
					Id:   "J_123",
					Type: livekit.JobType_JT_ROOM,
					Room: &livekit.Room{
						Name: "test-room",
					},
					State: &livekit.JobState{
						ParticipantIdentity: "agent-123",
					},
				},
				Token: "test-token",
			},
			handlerError: nil,
			expectStatus: livekit.JobStatus_JS_SUCCESS,
		},
		{
			name: "job handler error",
			assignment: &livekit.JobAssignment{
				Job: &livekit.Job{
					Id:   "J_456",
					Type: livekit.JobType_JT_PUBLISHER,
					Room: &livekit.Room{
						Name: "test-room-2",
					},
					State: &livekit.JobState{
						ParticipantIdentity: "agent-456",
					},
				},
				Token: "test-token",
			},
			handlerError:   errors.New("handler failed"),
			expectStatus:   livekit.JobStatus_JS_FAILED,
			expectErrorMsg: "handler failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := newTestJobHandler()
			handlerCalled := make(chan struct{})
			
			handler.OnJobAssignedFunc = func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
				defer close(handlerCalled)
				
				// Verify job details
				if job.Id != tt.assignment.Job.Id {
					t.Errorf("Expected job ID %s, got %s", tt.assignment.Job.Id, job.Id)
				}
				
				// Simulate some work
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(10 * time.Millisecond):
					return tt.handlerError
				}
			}

			worker := newTestableWorker(handler, WorkerOptions{
				JobType: livekit.JobType_JT_ROOM,
			})
			worker.injectConnection()

			// We need to mock the room connection
			// For this test, we'll simulate by checking the messages sent
			ctx := context.Background()
			
			// Run in goroutine to allow async behavior
			go worker.handleJobAssignment(ctx, tt.assignment)

			// Wait for handler to be called
			select {
			case <-handlerCalled:
				// Success
			case <-time.After(100 * time.Millisecond):
				t.Fatal("Handler was not called")
			}

			// Give time for status updates
			time.Sleep(50 * time.Millisecond)

			// Check that job was added to active jobs
			worker.mu.RLock()
			_, exists := worker.activeJobs[tt.assignment.Job.Id]
			worker.mu.RUnlock()

			if !exists && tt.expectStatus == livekit.JobStatus_JS_SUCCESS {
				// Job might have already completed and been removed
				// Check the messages for status updates
			}

			// Verify status updates were sent
			messages := worker.mockConn.GetWrittenMessages()
			
			runningFound := false
			finalStatusFound := false
			var finalStatus *livekit.UpdateJobStatus

			for _, msg := range messages {
				var workerMsg livekit.WorkerMessage
				if err := proto.Unmarshal(msg.Data, &workerMsg); err == nil {
					if update := workerMsg.GetUpdateJob(); update != nil {
						if update.JobId == tt.assignment.Job.Id {
							if update.Status == livekit.JobStatus_JS_RUNNING {
								runningFound = true
							}
							if update.Status == tt.expectStatus {
								finalStatusFound = true
								finalStatus = update
							}
						}
					}
				}
			}

			if !runningFound {
				t.Error("Expected RUNNING status update")
			}

			if !finalStatusFound {
				t.Errorf("Expected final status %v not found", tt.expectStatus)
			}

			if tt.expectErrorMsg != "" && finalStatus != nil {
				if finalStatus.Error != tt.expectErrorMsg {
					t.Errorf("Expected error message %s, got %s", tt.expectErrorMsg, finalStatus.Error)
				}
			}

			// Verify handler was called
			assignments := handler.GetJobAssignments()
			if len(assignments) != 1 {
				t.Fatalf("Expected 1 job assignment, got %d", len(assignments))
			}

			if assignments[0].Job.Id != tt.assignment.Job.Id {
				t.Errorf("Expected job ID %s, got %s", tt.assignment.Job.Id, assignments[0].Job.Id)
			}
		})
	}
}

func TestWorker_handleJobAssignment_Cleanup(t *testing.T) {
	// Skip this test as it requires a real room connection
	t.Skip("handleJobAssignment requires a real room connection which can't be easily mocked")
	
	handler := newTestJobHandler()
	jobStarted := make(chan struct{})
	jobCtx := make(chan context.Context, 1)

	handler.OnJobAssignedFunc = func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
		jobCtx <- ctx
		close(jobStarted)
		
		// Wait for context cancellation
		<-ctx.Done()
		return nil
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	worker.injectConnection()

	assignment := &livekit.JobAssignment{
		Job: &livekit.Job{
			Id:   "J_CLEANUP",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{
				Name: "cleanup-room",
			},
			State: &livekit.JobState{
				ParticipantIdentity: "agent-cleanup",
			},
		},
		Token: "test-token",
	}

	ctx := context.Background()
	go worker.handleJobAssignment(ctx, assignment)

	// Wait for job to start
	<-jobStarted

	// Job should be in active jobs
	worker.mu.RLock()
	activeJob, exists := worker.activeJobs["J_CLEANUP"]
	worker.mu.RUnlock()

	if !exists {
		t.Fatal("Job not found in active jobs")
	}

	// Cancel the job
	activeJob.cancel()

	// Wait for cleanup
	time.Sleep(50 * time.Millisecond)

	// Job should be removed from active jobs
	worker.mu.RLock()
	_, exists = worker.activeJobs["J_CLEANUP"]
	worker.mu.RUnlock()

	if exists {
		t.Error("Job was not removed from active jobs after completion")
	}

	// Load should be updated
	messages := worker.mockConn.GetWrittenMessages()
	loadUpdateFound := false

	for _, msg := range messages {
		var workerMsg livekit.WorkerMessage
		if err := proto.Unmarshal(msg.Data, &workerMsg); err == nil {
			if update := workerMsg.GetUpdateWorker(); update != nil {
				loadUpdateFound = true
			}
		}
	}

	if !loadUpdateFound {
		t.Error("Expected load update after job cleanup")
	}
}

func TestWorker_terminateAllJobs(t *testing.T) {
	worker := newTestableWorker(newTestJobHandler(), WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Create multiple active jobs
	contexts := make([]context.Context, 3)
	cancels := make([]context.CancelFunc, 3)

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		contexts[i] = ctx
		cancels[i] = cancel

		worker.mu.Lock()
		worker.activeJobs[string(rune('A'+i))] = &activeJob{
			job:    &livekit.Job{Id: string(rune('A' + i))},
			cancel: cancel,
		}
		worker.mu.Unlock()
	}

	// Track cancellations
	var wg sync.WaitGroup
	for i, ctx := range contexts {
		wg.Add(1)
		go func(idx int, c context.Context) {
			defer wg.Done()
			<-c.Done()
		}(i, ctx)
	}

	// Terminate all jobs
	worker.terminateAllJobs()

	// Wait for all contexts to be cancelled
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - all jobs terminated
	case <-time.After(100 * time.Millisecond):
		t.Error("Not all jobs were terminated")
	}
}

func TestWorker_JobLifecycle_Integration(t *testing.T) {
	// Skip this test as it requires a real room connection
	t.Skip("JobLifecycle integration test requires a real room connection")
	
	handler := newTestJobHandler()
	jobStates := make(chan string, 10)

	handler.OnJobRequestFunc = func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
		jobStates <- "requested"
		return true, &JobMetadata{
			ParticipantIdentity: "lifecycle-agent",
			ParticipantName:     "Lifecycle Test Agent",
		}
	}

	handler.OnJobAssignedFunc = func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
		jobStates <- "assigned"
		
		// Simulate some work
		select {
		case <-ctx.Done():
			jobStates <- "cancelled"
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			jobStates <- "completed"
			return nil
		}
	}

	handler.OnJobTerminatedFunc = func(ctx context.Context, jobID string) {
		jobStates <- "terminated"
	}

	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	worker.injectConnection()

	ctx := context.Background()

	// Step 1: Availability request
	availReq := &livekit.AvailabilityRequest{
		Job: &livekit.Job{
			Id:   "J_LIFECYCLE",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "lifecycle-room"},
		},
	}
	worker.handleAvailabilityRequest(ctx, availReq)

	// Verify requested state
	select {
	case state := <-jobStates:
		if state != "requested" {
			t.Errorf("Expected 'requested' state, got %s", state)
		}
	case <-time.After(50 * time.Millisecond):
		t.Error("Job request not received")
	}

	// Step 2: Job assignment
	assignment := &livekit.JobAssignment{
		Job: &livekit.Job{
			Id:   "J_LIFECYCLE",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{Name: "lifecycle-room"},
			State: &livekit.JobState{
				ParticipantIdentity: "lifecycle-agent",
			},
		},
		Token: "test-token",
	}

	go worker.handleJobAssignment(ctx, assignment)

	// Verify assigned and completed states
	expectedStates := []string{"assigned", "completed"}
	for _, expected := range expectedStates {
		select {
		case state := <-jobStates:
			if state != expected {
				t.Errorf("Expected '%s' state, got %s", expected, state)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("State '%s' not received", expected)
		}
	}

	// Wait a bit for cleanup
	time.Sleep(50 * time.Millisecond)

	// Verify job is no longer active
	worker.mu.RLock()
	_, exists := worker.activeJobs["J_LIFECYCLE"]
	worker.mu.RUnlock()

	if exists {
		t.Error("Job should not be in active jobs after completion")
	}

	// Step 3: Early termination scenario
	// Add a new job
	ctx2, cancel2 := context.WithCancel(context.Background())
	worker.mu.Lock()
	worker.activeJobs["J_TERMINATE"] = &activeJob{
		job:    &livekit.Job{Id: "J_TERMINATE"},
		cancel: cancel2,
	}
	worker.mu.Unlock()

	termination := &livekit.JobTermination{
		JobId:  "J_TERMINATE",
	}

	worker.handleJobTermination(ctx, termination)

	// Verify terminated state
	select {
	case state := <-jobStates:
		if state != "terminated" {
			t.Errorf("Expected 'terminated' state, got %s", state)
		}
	case <-time.After(50 * time.Millisecond):
		t.Error("Termination not received")
	}

	// Context should be cancelled
	select {
	case <-ctx2.Done():
		// Success
	case <-time.After(50 * time.Millisecond):
		t.Error("Job context was not cancelled")
	}
}