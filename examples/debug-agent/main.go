package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
	log.Println("Starting debug agent...")

	// Create a debug handler that logs everything
	handler := &agent.SimpleJobHandler{
		OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			log.Printf("=== JOB HANDLER CALLED ===")
			log.Printf("Job ID: %s", job.Id)
			log.Printf("Job Type: %v", job.Type)
			log.Printf("Room: %s", job.Room.Name)

			// Just wait for context cancellation
			<-ctx.Done()
			log.Printf("Job %s context cancelled", job.Id)
			return nil
		},
		Metadata: func(job *livekit.Job) *agent.JobMetadata {
			log.Printf("=== METADATA REQUESTED ===")
			log.Printf("Job ID: %s", job.Id)
			log.Printf("Job Type: %v", job.Type)
			log.Printf("Room: %s", job.Room.Name)
			log.Printf("Agent Name: %s", job.AgentName)
			log.Printf("Namespace: %s", job.Namespace)

			return &agent.JobMetadata{
				ParticipantIdentity: fmt.Sprintf("debug-agent-%s", job.Id),
				ParticipantName:     "Debug Agent",
				ParticipantMetadata: `{"debug": true}`,
			}
		},
		OnTerminated: func(jobID string) {
			log.Printf("=== JOB TERMINATED: %s ===", jobID)
		},
	}

	// Create worker with minimal options
	opts := agent.WorkerOptions{
		AgentName: "simple-agent", // Match what we're using in room creation
		Version:   "1.0.0",
		JobType:   livekit.JobType_JT_ROOM,
	}

	log.Printf("Creating worker with options: AgentName=%s, JobType=%v", opts.AgentName, opts.JobType)

	worker := agent.NewWorker("http://localhost:7880", "devkey", "secret", handler, opts)

	ctx := context.Background()

	log.Println("Starting worker...")
	if err := worker.Start(ctx); err != nil {
		log.Fatalf("Failed to start worker: %v", err)
	}

	log.Println("Worker started successfully, waiting for jobs...")

	// Keep running
	for {
		time.Sleep(5 * time.Second)
		log.Println("Still waiting for jobs...")
	}
}
