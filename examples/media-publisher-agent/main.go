package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
)

func main() {
	// Initialize logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Load configuration
	config := loadConfig()

	// Create media services
	audioService := NewAudioGeneratorService(config.AudioConfig)
	videoService := NewVideoGeneratorService(config.VideoConfig)

	// Create media publisher
	publisher := NewMediaPublisher(audioService, videoService)

	// Create job handler
	handler := &MediaPublisherHandler{
		publisher: publisher,
		config:    config,
	}

	// Create worker - configured for publisher jobs
	worker := agent.NewUniversalWorker(
		config.LiveKitURL,
		config.APIKey,
		config.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: "media-publisher",
			JobType:   livekit.JobType_JT_PUBLISHER,
			MaxJobs:   10, // Can handle multiple concurrent publishing jobs
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
		log.Println("Starting media publisher agent...")
		errChan <- worker.Start(ctx)
	}()

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		log.Printf("Received signal %v, shutting down...", sig)
		cancel()

		// Give worker time to clean up
		if err := worker.Stop(); err != nil {
			log.Printf("Error during shutdown: %v", err)
		}

	case err := <-errChan:
		if err != nil {
			log.Fatal("Worker error:", err)
		}
	}

	log.Println("Media publisher agent stopped")
}

func loadConfig() *Config {
	return &Config{
		LiveKitURL: getEnv("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:     mustGetEnv("LIVEKIT_API_KEY"),
		APISecret:  mustGetEnv("LIVEKIT_API_SECRET"),

		AudioConfig: AudioConfig{
			SampleRate: getEnvInt("AUDIO_SAMPLE_RATE", 48000),
			Channels:   getEnvInt("AUDIO_CHANNELS", 1),
			BitDepth:   getEnvInt("AUDIO_BIT_DEPTH", 16),
		},

		VideoConfig: VideoConfig{
			Width:     getEnvInt("VIDEO_WIDTH", 1280),
			Height:    getEnvInt("VIDEO_HEIGHT", 720),
			FrameRate: getEnvInt("VIDEO_FRAME_RATE", 30),
			Bitrate:   getEnvInt("VIDEO_BITRATE", 2000000),
		},
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

func getEnvInt(key string, defaultValue int) int {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.Atoi(str); err == nil {
			return value
		}
	}
	return defaultValue
}

// Config holds the application configuration
type Config struct {
	LiveKitURL  string
	APIKey      string
	APISecret   string
	AudioConfig AudioConfig
	VideoConfig VideoConfig
}

// AudioConfig holds audio generation configuration
type AudioConfig struct {
	SampleRate int
	Channels   int
	BitDepth   int
}

// VideoConfig holds video generation configuration
type VideoConfig struct {
	Width     int
	Height    int
	FrameRate int
	Bitrate   int
}
