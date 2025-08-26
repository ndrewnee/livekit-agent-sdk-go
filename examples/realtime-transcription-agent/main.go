// realtime-transcription-agent - End-to-end integration test for OpenAI Realtime API transcription
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/joho/godotenv"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v3"
)

// RealtimeTranscriptionAgent demonstrates OpenAI Realtime API transcription
type RealtimeTranscriptionAgent struct {
	room               *lksdk.Room
	pipeline           *agent.MediaPipeline
	realtimeStage      *agent.RealtimeTranscriptionStage
	trackSubscriptions map[string]bool
	transcriptionCount int
}

// NewRealtimeTranscriptionAgent creates a new transcription agent
func NewRealtimeTranscriptionAgent() *RealtimeTranscriptionAgent {
	return &RealtimeTranscriptionAgent{
		trackSubscriptions: make(map[string]bool),
	}
}

// Initialize sets up the agent
func (a *RealtimeTranscriptionAgent) Initialize(ctx context.Context, room *lksdk.Room) error {
	getLogger := logger.GetLogger()
	getLogger.Infow("Initializing Realtime Transcription Agent")

	// Get OpenAI API key from environment
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY environment variable not set")
	}

	// Create media pipeline
	a.pipeline = agent.NewMediaPipeline()

	// Create Realtime transcription stage
	a.realtimeStage = agent.NewRealtimeTranscriptionStage(
		"openai-realtime",
		10, // priority
		apiKey,
		"", // use default model
	)

	// Add transcription callback to print to console
	a.realtimeStage.AddTranscriptionCallback(func(event agent.TranscriptionEvent) {
		a.transcriptionCount++
		timestamp := event.Timestamp.Format("15:04:05.000")

		if event.Error != nil {
			fmt.Printf("[%s] TRANSCRIPTION ERROR: %v\n", timestamp, event.Error)
			return
		}

		status := "PARTIAL"
		if event.IsFinal {
			status = "FINAL"
		}

		fmt.Printf("[%s] %s: %s\n", timestamp, status, event.Text)

		// Log every 10th transcription for progress tracking
		if a.transcriptionCount%10 == 0 {
			getLogger.Infow("Transcription progress",
				"count", a.transcriptionCount,
				"lastText", event.Text)
		}
	})

	// Add stage to pipeline
	a.pipeline.AddStage(a.realtimeStage)

	// Store room reference
	a.room = room

	// Set up room event handlers
	a.setupRoomHandlers()

	// Connect to OpenAI Realtime API
	if err := a.realtimeStage.Connect(ctx); err != nil {
		getLogger.Errorw("Failed to connect to OpenAI Realtime API", err)
		return fmt.Errorf("failed to connect to OpenAI Realtime API: %w", err)
	}

	getLogger.Infow("Agent initialized successfully",
		"realtimeConnected", a.realtimeStage.IsConnected())

	// Subscribe to existing tracks
	for _, p := range a.room.GetRemoteParticipants() {
		for _, track := range p.TrackPublications() {
			if track.IsSubscribed() && track.Track() != nil {
				// Track is already subscribed, handle it
				// Note: we'll handle this in the OnTrackSubscribed callback
			}
		}
	}

	return nil
}

// setupRoomHandlers sets up LiveKit room event handlers
func (a *RealtimeTranscriptionAgent) setupRoomHandlers() {
	// Room callbacks are set during room creation, not after
	// We'll need to pass them to ConnectToRoom instead
}

// handleTrackSubscribed processes subscribed audio tracks
func (a *RealtimeTranscriptionAgent) handleTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication) {
	getLogger := logger.GetLogger()

	// Avoid duplicate processing
	if a.trackSubscriptions[track.ID()] {
		return
	}
	a.trackSubscriptions[track.ID()] = true

	getLogger.Infow("Processing audio track",
		"trackID", track.ID(),
		"codec", track.Codec())

	// Read RTP packets from the track
	go func() {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				getLogger.Errorw("Error reading RTP packet", err)
				return
			}
			// Create MediaData from RTP packet
			mediaData := agent.MediaData{
				Type:    agent.MediaTypeAudio,
				TrackID: track.ID(),
				Data:    packet.Payload,
				Metadata: map[string]interface{}{
					"rtp_header": &packet.Header,
					"timestamp":  time.Now(),
					"codec":      track.Codec(),
				},
			}

			// Process through pipeline
			ctx := context.Background()
			output, err := a.pipeline.Process(ctx, mediaData)
			if err != nil {
				getLogger.Errorw("Pipeline processing error", err,
					"trackID", track.ID())
				return
			}

			// Check if transcription was added to metadata
			if transcription, ok := output.Metadata["transcription"].(string); ok && transcription != "" {
				getLogger.Debugw("Transcription in metadata",
					"text", transcription)
			}
		}
	}()
}

// Cleanup cleans up the agent
func (a *RealtimeTranscriptionAgent) Cleanup() error {
	getLogger := logger.GetLogger()
	getLogger.Infow("Cleaning up agent")

	// Disconnect from OpenAI Realtime API
	if a.realtimeStage != nil {
		a.realtimeStage.Disconnect()
	}

	// Stop the pipeline
	if a.pipeline != nil {
		a.pipeline.Stop()
	}

	// Disconnect from room
	if a.room != nil {
		a.room.Disconnect()
	}

	// Print final statistics
	if a.realtimeStage != nil {
		stats := a.realtimeStage.GetStats()
		fmt.Println("\n=== Final Statistics ===")
		fmt.Printf("Total Transcriptions: %d\n", a.transcriptionCount)
		fmt.Printf("Partial Transcriptions: %d\n", stats.PartialTranscriptions)
		fmt.Printf("Final Transcriptions: %d\n", stats.FinalTranscriptions)
		fmt.Printf("Audio Packets Sent: %d\n", stats.AudioPacketsSent)
		fmt.Printf("Errors: %d\n", stats.Errors)
		fmt.Printf("Connection Attempts: %d\n", stats.ConnectionAttempts)
		fmt.Printf("Connection Successes: %d\n", stats.ConnectionSuccesses)
		fmt.Printf("Connection Failures: %d\n", stats.ConnectionFailures)
	}

	return nil
}

func main() {
	// Run the simple test by default
	if err := SimpleTranscriptionTest(); err != nil {
		log.Fatal(err)
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
