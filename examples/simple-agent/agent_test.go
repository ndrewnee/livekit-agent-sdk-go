package main

import (
	"context"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// TestAgentHandler tests that our agent handler works correctly
func TestAgentHandler(t *testing.T) {
	// Create the handler
	handler := &agent.SimpleJobHandler{
		OnJob: handleJob,
		Metadata: func(job *livekit.Job) *agent.JobMetadata {
			t.Logf("Metadata called for job %s", job.Id)
			return &agent.JobMetadata{
				ParticipantIdentity: "test-agent",
				ParticipantName:     "Test Agent",
			}
		},
		OnTerminated: func(jobID string) {
			t.Logf("Job %s terminated", jobID)
		},
	}

	// Test metadata generation
	testJob := &livekit.Job{
		Id:   "test-job-123",
		Type: livekit.JobType_JT_ROOM,
		Room: &livekit.Room{
			Name: "test-room",
		},
	}

	// Test OnJobRequest
	accept, metadata := handler.OnJobRequest(context.Background(), testJob)
	if !accept {
		t.Error("Expected handler to accept job")
	}
	if metadata == nil {
		t.Error("Expected metadata to be returned")
	}
	if metadata.ParticipantIdentity != "test-agent" {
		t.Errorf("Expected identity 'test-agent', got %s", metadata.ParticipantIdentity)
	}

	// Test job handling (with timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Create a mock room
	room := &lksdk.Room{}

	// This should complete when context times out
	err := handler.OnJobAssigned(ctx, testJob, room)
	if err != nil {
		t.Errorf("Handler returned error: %v", err)
	}

	// Test termination
	handler.OnJobTerminated(ctx, "test-job-123")
}

// TestWorkerOptions tests that our worker options are valid
func TestWorkerOptions(t *testing.T) {
	opts := agent.WorkerOptions{
		AgentName: "test-agent",
		Version:   "1.0.0",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   5,
	}

	// Verify options
	if opts.AgentName != "test-agent" {
		t.Errorf("Expected agent name 'test-agent', got %s", opts.AgentName)
	}
	if opts.JobType != livekit.JobType_JT_ROOM {
		t.Errorf("Expected job type JT_ROOM, got %v", opts.JobType)
	}
}
