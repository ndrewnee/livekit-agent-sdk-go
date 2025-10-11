package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
)

func main() {
	// Load configuration
	config := loadConfig()

	// Create recorder manager
	recorderManager := NewRecorderManager(config)

	// Create job handler
	handler := &ParticipantHLSHandler{
		recorderManager: recorderManager,
		config:          config,
	}

	// Create worker - configured for participant jobs
	worker := agent.NewUniversalWorker(
		config.LiveKitURL,
		config.APIKey,
		config.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: "participant-hls-recorder",
			JobType:   livekit.JobType_JT_PARTICIPANT,
			MaxJobs:   10, // Can record multiple participants simultaneously
		},
	)

	// Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start worker in goroutine
	errChan := make(chan error, 1)
	go func() {
		log.Println("Starting participant HLS recording agent...")
		errChan <- worker.Start(ctx)
	}()

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()

		// Stop worker
		worker.Stop()

	case err := <-errChan:
		if err != nil {
			log.Fatal("Worker error:", err)
		}
	}

	// Print final summary
	recorderManager.PrintSummary()
	log.Println("Participant HLS recording agent stopped")
}
