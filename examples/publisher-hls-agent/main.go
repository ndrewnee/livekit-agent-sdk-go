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
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg := loadConfig()

	handler := NewPublisherHLSHandler(cfg)

	worker := agent.NewUniversalWorker(
		cfg.LiveKitURL,
		cfg.APIKey,
		cfg.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: cfg.AgentName,
			JobType:   livekit.JobType_JT_PUBLISHER,
			MaxJobs:   4,
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("starting publisher HLS agent %q as JT_PUBLISHER worker", cfg.AgentName)
		errCh <- worker.Start(ctx)
	}()

	select {
	case sig := <-sigCh:
		log.Printf("received signal %v, stopping worker", sig)
		cancel()
		worker.Stop()
	case err := <-errCh:
		if err != nil {
			log.Fatalf("worker exited with error: %v", err)
		}
	}

	handler.PrintSummary()
	log.Println("publisher HLS agent stopped")
}
