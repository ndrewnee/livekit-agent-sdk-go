package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress"
)

func main() {
	// Parse command line flags
	var (
		livekitURL = flag.String("url", getEnv("LIVEKIT_URL", "ws://localhost:7880"), "LiveKit server URL")
		apiKey     = flag.String("api-key", getEnv("LIVEKIT_API_KEY", "devkey"), "LiveKit API key")
		apiSecret  = flag.String("api-secret", getEnv("LIVEKIT_API_SECRET", "secret"), "LiveKit API secret")
		configPath = flag.String("config", "", "Path to configuration file")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting Egress Worker...")
	log.Printf("Connecting to LiveKit at: %s", *livekitURL)

	// Load configuration
	var config *egress.Config
	if *configPath != "" {
		// TODO: Load from file
		log.Printf("Loading config from: %s", *configPath)
	}

	// Use default config if none provided
	if config == nil {
		config = egress.DefaultConfig()

		// Customize some settings
		config.MaxConcurrentSessions = 10
		config.VideoQuality = 2 // HIGH quality

		// Configure storage
		config.StorageConfig.Type = "local"
		config.StorageConfig.LocalPath = "./recordings"

		// Configure pipeline
		config.PipelineConfig.OutputDir = "./recordings"
		config.PipelineConfig.SegmentDuration = 4
		config.PipelineConfig.PlaylistType = "event"

		// Configure recording preferences
		config.RecordingConfig.RecordVideo = true
		config.RecordingConfig.RecordAudio = true
		config.RecordingConfig.RecordScreenShare = true
		config.RecordingConfig.AutoStart = true

		// Configure network
		config.NetworkConfig.UseDirectInjection = true
		config.NetworkConfig.ReconnectAttempts = 5
		config.NetworkConfig.AdaptiveBitrate = true
		config.NetworkConfig.MaxBitrate = 5000 // 5 Mbps
		config.NetworkConfig.MinBitrate = 1000 // 1 Mbps
	}

	// Create the egress worker
	worker := egress.NewEgressWorker(config)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the worker in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := worker.Start(ctx, *livekitURL, *apiKey, *apiSecret); err != nil {
			errChan <- err
		}
	}()

	log.Printf("Egress Worker started successfully")
	log.Printf("Configuration:")
	log.Printf("  Max concurrent sessions: %d", config.MaxConcurrentSessions)
	log.Printf("  Storage type: %s", config.StorageConfig.Type)
	log.Printf("  Output directory: %s", config.PipelineConfig.OutputDir)
	log.Printf("  Video recording: %v", config.RecordingConfig.RecordVideo)
	log.Printf("  Audio recording: %v", config.RecordingConfig.RecordAudio)
	log.Printf("  Screen share recording: %v", config.RecordingConfig.RecordScreenShare)
	log.Printf("  Direct injection: %v", config.NetworkConfig.UseDirectInjection)
	log.Printf("")
	log.Printf("Worker is ready to accept jobs. Press Ctrl+C to stop.")

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()

		// Give worker time to clean up
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		done := make(chan struct{})
		go func() {
			if err := worker.Stop(); err != nil {
				log.Printf("Error stopping worker: %v", err)
			}
			close(done)
		}()

		select {
		case <-done:
			log.Printf("Worker stopped cleanly")
		case <-shutdownCtx.Done():
			log.Printf("Shutdown timeout exceeded")
		}

	case err := <-errChan:
		log.Printf("Worker error: %v", err)
		cancel()
		worker.Stop()
	}

	log.Printf("Egress Worker terminated")
}

// getEnv gets an environment variable with a default fallback
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}