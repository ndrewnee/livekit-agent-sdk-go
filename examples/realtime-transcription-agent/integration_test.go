// integration_test.go - End-to-end integration test
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/joho/godotenv"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// RunIntegrationTest runs the complete end-to-end test
func RunIntegrationTest() error {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Get configuration
	url := getEnv("LIVEKIT_URL", "ws://localhost:7880")
	apiKey := getEnv("LIVEKIT_API_KEY", "devkey")
	apiSecret := getEnv("LIVEKIT_API_SECRET", "secret")
	openaiKey := os.Getenv("OPENAI_API_KEY")

	if openaiKey == "" {
		return fmt.Errorf("OPENAI_API_KEY environment variable is required")
	}

	// Find audiobook.wav file
	audioFile := "audiobook.wav"
	if _, err := os.Stat(audioFile); os.IsNotExist(err) {
		// Try in parent directories
		audioFile = "../../audiobook.wav"
		if _, err := os.Stat(audioFile); os.IsNotExist(err) {
			return fmt.Errorf("audiobook.wav not found")
		}
	}

	absPath, _ := filepath.Abs(audioFile)

	fmt.Println("=== OpenAI Realtime API Integration Test ===")
	fmt.Printf("LiveKit URL: %s\n", url)
	fmt.Printf("Audio File: %s\n", absPath)
	fmt.Printf("OpenAI API Key: %s...%s\n", openaiKey[:8], openaiKey[len(openaiKey)-4:])
	fmt.Println()

	// Create test room
	roomName := fmt.Sprintf("realtime-test-%d", time.Now().Unix())
	if err := createTestRoom(url, apiKey, apiSecret, roomName); err != nil {
		return fmt.Errorf("failed to create test room: %w", err)
	}
	fmt.Printf("Created test room: %s\n", roomName)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down test...")
		cancel()
	}()

	// Start the transcription agent in the background
	agentDone := make(chan error, 1)
	go func() {
		fmt.Println("\nStarting transcription agent...")
		agentDone <- runTranscriptionAgent(ctx, url, apiKey, apiSecret, roomName)
	}()

	// Wait for agent to initialize
	time.Sleep(3 * time.Second)

	// Start the audio publisher
	fmt.Println("\nStarting audio publisher...")
	publisherDone := make(chan error, 1)
	go func() {
		publisherDone <- RunPublisher(ctx, url, apiKey, apiSecret, roomName, audioFile)
	}()

	// Wait for test duration or cancellation
	testDuration := 30 * time.Second
	fmt.Printf("\nTest will run for %v (or press Ctrl+C to stop)\n", testDuration)
	fmt.Println("You should see transcriptions appearing below:\n")
	fmt.Println("=" + "="*50)

	select {
	case <-time.After(testDuration):
		fmt.Println("\nTest duration completed")
		cancel()
	case <-ctx.Done():
		fmt.Println("\nTest cancelled")
	}

	// Wait for components to shut down
	select {
	case err := <-agentDone:
		if err != nil {
			fmt.Printf("Agent error: %v\n", err)
		}
	case <-time.After(5 * time.Second):
		fmt.Println("Agent shutdown timeout")
	}

	select {
	case err := <-publisherDone:
		if err != nil {
			fmt.Printf("Publisher error: %v\n", err)
		}
	case <-time.After(5 * time.Second):
		fmt.Println("Publisher shutdown timeout")
	}

	fmt.Println("\n" + "="*50)
	fmt.Println("Integration test completed")

	return nil
}

// createTestRoom creates a LiveKit room with agent dispatch
func createTestRoom(url, apiKey, apiSecret, roomName string) error {
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)

	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Realtime transcription test room",
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "realtime-transcription-agent",
				Metadata:  `{"purpose": "transcription"}`,
			},
		},
	})

	return err
}

// runTranscriptionAgent runs the transcription agent
func runTranscriptionAgent(ctx context.Context, url, apiKey, apiSecret, roomName string) error {
	// Generate token for agent
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanSubscribe: true,
		CanPublish:   false,
	}
	at.AddGrant(grant).
		SetIdentity("transcription-agent").
		SetValidFor(3600)

	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to create agent token: %w", err)
	}

	// Create and initialize the agent
	testAgent := NewRealtimeTranscriptionAgent()

	// Connect to room as agent
	room, err := lksdk.ConnectToRoom(url, token, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(true))
	if err != nil {
		return fmt.Errorf("failed to connect agent to room: %w", err)
	}
	defer room.Disconnect()

	testAgent.room = room

	// Initialize the agent components
	openaiKey := os.Getenv("OPENAI_API_KEY")
	testAgent.pipeline = agent.NewMediaPipeline()
	testAgent.realtimeStage = agent.NewRealtimeTranscriptionStage(
		"openai-realtime",
		10,
		openaiKey,
		"",
	)

	// Add transcription callback
	testAgent.realtimeStage.AddTranscriptionCallback(func(event agent.TranscriptionEvent) {
		testAgent.transcriptionCount++
		timestamp := event.Timestamp.Format("15:04:05.000")

		if event.Error != nil {
			fmt.Printf("[%s] TRANSCRIPTION ERROR: %v\n", timestamp, event.Error)
			return
		}

		status := "PARTIAL"
		if event.IsFinal {
			status = "FINAL  "
		}

		fmt.Printf("[%s] %s: %s\n", timestamp, status, event.Text)
	})

	testAgent.pipeline.AddStage(testAgent.realtimeStage)

	// Connect to OpenAI Realtime API
	if err := testAgent.realtimeStage.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to OpenAI: %w", err)
	}
	defer testAgent.realtimeStage.Disconnect()

	// Set up room handlers
	testAgent.setupRoomHandlers()

	// Start pipeline
	testAgent.pipeline.Start()
	defer testAgent.pipeline.Stop()

	fmt.Println("Transcription agent ready and listening...")

	// Wait for context cancellation
	<-ctx.Done()

	// Print statistics
	stats := testAgent.realtimeStage.GetStats()
	fmt.Println("\n=== Transcription Statistics ===")
	fmt.Printf("Total Transcriptions: %d\n", testAgent.transcriptionCount)
	fmt.Printf("Partial: %d, Final: %d\n", stats.PartialTranscriptions, stats.FinalTranscriptions)
	fmt.Printf("Audio Packets: %d\n", stats.AudioPacketsSent)
	fmt.Printf("Errors: %d\n", stats.Errors)

	return nil
}
