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
	// Initialize logger
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Simple Room Agent")

	// Load configuration
	config := loadConfig()

	// Create handler
	handler := NewRoomAnalyticsHandler()

	// Create worker
	worker := agent.NewUniversalWorker(
		config.LiveKitURL,
		config.APIKey,
		config.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: "simple-room-agent",
			JobType:   livekit.JobType_JT_ROOM,
			MaxJobs:   5,
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

	log.Println("Worker started, waiting for jobs...")

	// Wait for shutdown
	select {
	case <-sigChan:
		log.Println("Received shutdown signal")
	case err := <-errChan:
		log.Printf("Worker error: %v", err)
	}

	// Cleanup
	log.Println("Shutting down gracefully...")
	cancel()
	handler.Shutdown()
	log.Println("Shutdown complete")
}

// Configuration structure
type Config struct {
	LiveKitURL string
	APIKey     string
	APISecret  string
}

// Load configuration from environment variables
func loadConfig() *Config {
	config := &Config{
		LiveKitURL: getEnv("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:     mustGetEnv("LIVEKIT_API_KEY"),
		APISecret:  mustGetEnv("LIVEKIT_API_SECRET"),
	}

	log.Printf("Configuration loaded - URL: %s", config.LiveKitURL)
	return config
}

// Helper function to get environment variable with default
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Helper function to get required environment variable
func mustGetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("Environment variable %s is required", key)
	}
	return value
}
