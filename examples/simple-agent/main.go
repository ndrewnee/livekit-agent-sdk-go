package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
	// Parse command line flags
	serverURL := flag.String("url", "http://localhost:7880", "LiveKit server URL")
	apiKey := flag.String("api-key", "devkey", "LiveKit API key")
	apiSecret := flag.String("api-secret", "secret", "LiveKit API secret")
	agentName := flag.String("agent-name", "simple-agent", "Agent name")
	flag.Parse()

	if *apiKey == "" || *apiSecret == "" {
		log.Fatal("API key and secret are required")
	}

	// Create a simple agent handler
	handler := &agent.SimpleJobHandler{
		OnJob: handleJob,
		Metadata: func(job *livekit.Job) *agent.JobMetadata {
			log.Printf("Received job request: ID=%s, Type=%v, Room=%s", job.Id, job.Type, job.Room.Name)
			return &agent.JobMetadata{
				ParticipantIdentity: fmt.Sprintf("agent-%s", job.Id),
				ParticipantName:     "Simple Agent",
				ParticipantMetadata: `{"type": "simple-agent"}`,
			}
		},
		OnTerminated: func(jobID string) {
			log.Printf("Job %s terminated", jobID)
		},
	}

	// Create worker with options
	opts := agent.WorkerOptions{
		AgentName: *agentName,
		Version:   "1.0.0",
		JobType:   livekit.JobType_JT_ROOM,
		MaxJobs:   5,
	}

	// Create the worker
	worker := agent.NewWorker(*serverURL, *apiKey, *apiSecret, handler, opts)

	// Start the worker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := worker.Start(ctx); err != nil {
		log.Fatalf("Failed to start worker: %v", err)
	}

	log.Printf("Agent started, waiting for jobs...")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	if err := worker.Stop(); err != nil {
		log.Printf("Error stopping worker: %v", err)
	}
}

func handleJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	log.Printf("Handling job %s for room %s", job.Id, job.Room.Name)

	// Check if we have a valid room connection
	if room == nil || room.LocalParticipant == nil {
		log.Printf("Room not properly connected, simulating agent behavior")
		// In a real scenario, this would not happen as the SDK ensures a connected room
		<-ctx.Done()
		return nil
	}

	// Send a greeting message
	if err := room.LocalParticipant.PublishData(
		[]byte("Hello from the simple agent!"),
		lksdk.WithDataPublishReliable(true),
	); err != nil {
		log.Printf("Failed to send greeting: %v", err)
	}

	// Simulate some work
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Job %s completed", job.Id)
			return nil
		case <-ticker.C:
			// Send periodic messages
			msg := fmt.Sprintf("Agent update at %s", time.Now().Format("15:04:05"))
			if err := room.LocalParticipant.PublishData(
				[]byte(msg),
				lksdk.WithDataPublishReliable(true),
			); err != nil {
				log.Printf("Failed to send update: %v", err)
			}
		}
	}
}
