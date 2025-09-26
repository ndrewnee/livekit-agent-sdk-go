package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
)

var (
	url       = flag.String("url", os.Getenv("LIVEKIT_URL"), "LiveKit server URL")
	apiKey    = flag.String("api-key", os.Getenv("LIVEKIT_API_KEY"), "LiveKit API key")
	apiSecret = flag.String("api-secret", os.Getenv("LIVEKIT_API_SECRET"), "LiveKit API secret")
	configFile = flag.String("config", "config.yaml", "Configuration file path")
)

func main() {
	flag.Parse()

	// Validate required parameters
	if *url == "" || *apiKey == "" || *apiSecret == "" {
		log.Fatal("LiveKit URL, API key, and API secret are required")
	}

	// Load configuration
	config, err := LoadConfig(*configFile)
	if err != nil {
		log.Printf("Warning: Failed to load config file %s: %v. Using defaults.", *configFile, err)
		config = DefaultConfig()
	}

	// Create egress handler
	handler := NewEgressHandler(config)

	// Create worker using livekit-agent-sdk-go
	worker := agent.NewUniversalWorker(
		*url,
		*apiKey,
		*apiSecret,
		handler,
		agent.WorkerOptions{
			AgentName: "egress-agent",
			JobType:   livekit.JobType_JT_ROOM,
			MaxJobs:   10, // Max concurrent room recordings
		},
	)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start worker
	errChan := make(chan error, 1)
	go func() {
		if err := worker.Start(ctx); err != nil {
			errChan <- err
		}
	}()

	log.Println("Egress worker started, waiting for room jobs...")

	// Wait for shutdown
	select {
	case <-sigChan:
		log.Println("Received shutdown signal")
	case err := <-errChan:
		log.Printf("Worker error: %v", err)
	}

	// Cleanup
	cancel()
	handler.Shutdown()
	log.Println("Shutdown complete")
}

// DefaultConfig returns default configuration
func DefaultConfig() *EgressConfig {
	return &EgressConfig{
		OutputDir:       "/tmp/recordings",
		SegmentDuration: 4,
		JitterBufferMs:  200,
		VideoPort:       5004,
		AudioPort:       5006,
		AudioMode:       AudioPassThrough,
		EnableScreenshots: false,
	}
}

// LoadConfig loads configuration from file
func LoadConfig(path string) (*EgressConfig, error) {
	// TODO: Implement YAML configuration loading
	// For now, return default config
	return DefaultConfig(), fmt.Errorf("config loading not yet implemented")
}