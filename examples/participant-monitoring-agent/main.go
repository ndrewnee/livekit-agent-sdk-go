package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
)

func main() {
	// Load configuration
	config := loadConfig()

	// Create participant monitor
	monitor := NewParticipantMonitor()

	// Create job handler
	handler := &ParticipantMonitoringHandler{
		monitor: monitor,
		config:  config,
	}

	// Create worker - configured for participant jobs
	worker := agent.NewUniversalWorker(
		config.LiveKitURL,
		config.APIKey,
		config.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: "participant-monitor",
			JobType:   livekit.JobType_JT_PARTICIPANT,
			MaxJobs:   50, // Can monitor many participants
		},
	)

	// Set up graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start monitoring service
	go monitor.StartService(ctx)

	// Start worker in goroutine
	errChan := make(chan error, 1)
	go func() {
		log.Println("Starting participant monitoring agent...")
		errChan <- worker.Start(ctx)
	}()

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()

		// Give worker time to clean up
		// Removed unused timeout context

		worker.Stop()

	case err := <-errChan:
		if err != nil {
			log.Fatal("Worker error:", err)
		}
	}

	// Print final monitoring summary
	monitor.PrintSummary()
	log.Println("Participant monitoring agent stopped")
}

func loadConfig() *Config {
	return &Config{
		LiveKitURL:                 getEnv("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:                     mustGetEnv("LIVEKIT_API_KEY"),
		APISecret:                  mustGetEnv("LIVEKIT_API_SECRET"),
		ConnectionQualityThreshold: getEnvFloat("CONNECTION_QUALITY_THRESHOLD", 0.5),
		InactivityTimeout:          getEnvDuration("INACTIVITY_TIMEOUT", 5*time.Minute),
		SpeakingThreshold:          getEnvFloat("SPEAKING_THRESHOLD", 0.01),
		EnableNotifications:        getEnvBool("ENABLE_NOTIFICATIONS", true),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func mustGetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("Environment variable %s is required", key)
	}
	return value
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.ParseFloat(str, 64); err == nil {
			return value
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if str := os.Getenv(key); str != "" {
		if value, err := time.ParseDuration(str); err == nil {
			return value
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.ParseBool(str); err == nil {
			return value
		}
	}
	return defaultValue
}
