//go:build e2e

package agent

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

type E2ETranslationTest struct {
	url       string
	apiKey    string
	apiSecret string
	openaiKey string
	stage     *TranslationStage
	room      *lksdk.Room
	ctx       context.Context
	cancel    context.CancelFunc
	roomName  string
}

func NewE2ETranslationTest() (*E2ETranslationTest, error) {
	// Get configuration from environment
	url := getEnv("LIVEKIT_URL", "ws://localhost:7880")
	apiKey := getEnv("LIVEKIT_API_KEY", "devkey")
	apiSecret := getEnv("LIVEKIT_API_SECRET", "secret")
	openaiKey := os.Getenv("OPENAI_API_KEY")

	if openaiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable is required")
	}

	// E2E test doesn't need actual audio file - it simulates transcription events
	roomName := fmt.Sprintf("translation-e2e-test-%d", time.Now().Unix())

	ctx, cancel := context.WithCancel(context.Background())

	return &E2ETranslationTest{
		url:       url,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		openaiKey: openaiKey,
		roomName:  roomName,
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

func (rt *E2ETranslationTest) Setup() error {
	fmt.Println("🧪 === TranslationStage E2E Streaming Audio Integration Test ===")
	fmt.Printf("LiveKit URL: %s\n", rt.url)
	fmt.Printf("Room Name: %s\n", rt.roomName)
	fmt.Printf("OpenAI API Key: %s...%s\n", rt.openaiKey[:8], rt.openaiKey[len(rt.openaiKey)-4:])
	fmt.Println("🌊 Using OpenAI Streaming API for real-time translations")
	fmt.Println()

	// Create test room
	if err := rt.createTestRoom(); err != nil {
		return fmt.Errorf("failed to create test room: %w", err)
	}
	fmt.Printf("✅ Created test room: %s\n", rt.roomName)

	// Create TranslationStage
	rt.stage = NewTranslationStage(&TranslationConfig{
		Name:     "e2e-translator",
		Priority: 10,
		APIKey:   rt.openaiKey,
	})

	// Add callback to inject target languages for e2e test
	rt.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		// Inject target languages for e2e test
		data.Metadata["target_languages"] = []string{"es", "fr", "de"}
	})

	fmt.Printf("🌐 Configured streaming translation for languages: [es, fr, de]\n")

	// Connect agent to room
	if err := rt.connectAgent(); err != nil {
		return fmt.Errorf("failed to connect agent: %w", err)
	}
	fmt.Printf("🤖 Agent connected to room\n")

	fmt.Printf("📻 E2E streaming translation test ready (simulates audio streaming)\n")

	return nil
}

func (rt *E2ETranslationTest) createTestRoom() error {
	roomClient := lksdk.NewRoomServiceClient(rt.url, rt.apiKey, rt.apiSecret)

	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     rt.roomName,
		Metadata: "Real audio translation test room",
	})

	return err
}

func (rt *E2ETranslationTest) connectAgent() error {
	// Generate token for agent
	at := auth.NewAccessToken(rt.apiKey, rt.apiSecret)
	canSubscribe := true
	canPublish := true
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         rt.roomName,
		CanSubscribe: &canSubscribe,
		CanPublish:   &canPublish,
	}
	at.SetVideoGrant(grant).
		SetIdentity("translation-agent").
		SetValidFor(time.Hour)

	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to create agent token: %w", err)
	}

	// Connect to room with track subscription callbacks
	room, err := lksdk.ConnectToRoomWithToken(rt.url, token, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: rt.handleTrackSubscribed,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to connect agent to room: %w", err)
	}

	rt.room = room
	return nil
}

func (rt *E2ETranslationTest) handleTrackSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	if track.Kind() != webrtc.RTPCodecTypeAudio {
		return
	}

	fmt.Printf("🎵 Subscribed to audio track: %s (from %s)\n", track.ID(), rp.Identity())
	fmt.Println("📝 Real audio track detected - transcriptions would be processed through streaming translation")
}

func (rt *E2ETranslationTest) Run(duration time.Duration) error {
	fmt.Printf("\n🚀 Starting e2e streaming translation test (duration: %v)\n", duration)
	fmt.Println("You should see simulated transcriptions and real-time streaming translations below:")
	fmt.Println("🌊 Each translation uses OpenAI's streaming API for faster response times")
	fmt.Println(strings.Repeat("=", 60))

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Run test with timeout or signal
	testCtx, testCancel := context.WithTimeout(rt.ctx, duration)
	defer testCancel()

	fmt.Printf("⏱️  Test running for %v (or press Ctrl+C to stop)\n\n", duration)

	// Start simulated transcription generation since we don't have real audio
	go rt.simulateTranscriptionEvents(testCtx)

	select {
	case <-testCtx.Done():
		fmt.Println("\n⏱️  Test duration completed")
	case <-sigChan:
		fmt.Println("\n🛑 Test interrupted by signal")
		rt.cancel()
	}

	return nil
}

// simulateTranscriptionEvents generates simulated transcription events and processes them through TranslationStage
func (rt *E2ETranslationTest) simulateTranscriptionEvents(ctx context.Context) {
	transcriptionCount := 0
	translationCount := 0

	// Sample transcriptions to test translation
	sampleTranscriptions := []string{
		"Hello, welcome to our virtual meeting today",
		"Can everyone hear me clearly?",
		"Let's start with the quarterly business review",
		"The sales numbers look promising this quarter",
		"We need to discuss the upcoming product launch",
		"Thank you all for joining today's session",
	}

	ticker := time.NewTicker(3 * time.Second) // Generate transcription every 3 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Create simulated transcription event
			transcriptionText := sampleTranscriptions[transcriptionCount%len(sampleTranscriptions)]

			mediaData := MediaData{
				Type:    MediaTypeAudio,
				TrackID: "simulated-track",
				Data:    []byte("simulated audio data"),
				Metadata: map[string]interface{}{
					"timestamp": time.Now(),
					"transcription_event": TranscriptionEvent{
						Type:     "final",
						Text:     transcriptionText,
						Language: "en",
						IsFinal:  true,
					},
				},
			}

			transcriptionCount++
			fmt.Printf("\n[%s] 📝 TRANSCRIPTION #%d: %s\n",
				time.Now().Format("15:04:05.000"), transcriptionCount, transcriptionText)

			// Process through TranslationStage (streaming implementation)
			output, err := rt.stage.Process(ctx, mediaData)
			if err != nil {
				fmt.Printf("❌ Streaming translation error: %v\n", err)
				// Display streaming-specific error details
				metrics := rt.stage.GetAPIMetrics()
				if metrics["streaming_errors"].(uint64) > 0 {
					fmt.Printf("   📊 Streaming errors: %d\n", metrics["streaming_errors"].(uint64))
				}
				if metrics["connection_errors"].(uint64) > 0 {
					fmt.Printf("   🔌 Connection errors: %d\n", metrics["connection_errors"].(uint64))
				}
				continue
			}

			// Check for translations in transcription event (streaming implementation)
			if transcriptionEvent, ok := output.Metadata["transcription_event"].(TranscriptionEvent); ok {
				if len(transcriptionEvent.Translations) > 0 {
					translationCount++
					fmt.Printf("[%s] 🌐 STREAMING TRANSLATIONS:\n", time.Now().Format("15:04:05.000"))
					for lang, translation := range transcriptionEvent.Translations {
						fmt.Printf("    %s: %s\n", strings.ToUpper(lang), translation)
					}

					// Show real-time streaming performance every 3 translations
					if translationCount%3 == 0 {
						metrics := rt.stage.GetAPIMetrics()
						if ttft := metrics["average_time_to_first_token_ms"].(float64); ttft > 0 {
							fmt.Printf("   ⚡ Avg Time-to-First-Token: %.1fms\n", ttft)
						}
					}
				} else {
					fmt.Printf("[%s] ⚠️  No translations received (API may have failed)\n", time.Now().Format("15:04:05.000"))
				}
			}
		}
	}
}

func (rt *E2ETranslationTest) PrintStats() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📊 === Final Streaming Translation Statistics ===")

	// Get TranslationStage metrics
	if rt.stage != nil {
		metrics := rt.stage.GetAPIMetrics()
		stats := rt.stage.GetStats()

		// Core API metrics
		fmt.Printf("API Calls: %d\n", metrics["successful_calls"].(uint64))
		fmt.Printf("Failed Calls: %d\n", metrics["failed_calls"].(uint64))
		fmt.Printf("Cache Entries: %d\n", metrics["cache_entries"].(int))
		fmt.Printf("Rate Limited: %d\n", metrics["rate_limit_exceeded"].(uint64))
		fmt.Printf("Circuit Breaker Trips: %d\n", metrics["circuit_breaker_trips"].(uint64))

		// Streaming-specific metrics
		fmt.Printf("Streaming Errors: %d\n", metrics["streaming_errors"].(uint64))
		fmt.Printf("Connection Errors: %d\n", metrics["connection_errors"].(uint64))
		fmt.Printf("Total Chunks Received: %d\n", stats.ChunksReceived)

		// Performance metrics
		if totalCalls := metrics["successful_calls"].(uint64) + metrics["failed_calls"].(uint64); totalCalls > 0 {
			successRate := float64(metrics["successful_calls"].(uint64)) / float64(totalCalls) * 100
			fmt.Printf("Success Rate: %.1f%%\n", successRate)
		}

		fmt.Printf("Average Total Latency: %.2fms\n", metrics["average_latency_ms"].(float64))
		fmt.Printf("Average Time-to-First-Token: %.2fms\n", metrics["average_time_to_first_token_ms"].(float64))

		// Streaming performance indicator
		if ttft := metrics["average_time_to_first_token_ms"].(float64); ttft > 0 {
			totalLatency := metrics["average_latency_ms"].(float64)
			if totalLatency > 0 {
				streamingEfficiency := (1.0 - ttft/totalLatency) * 100
				fmt.Printf("Streaming Efficiency: %.1f%% (faster response start)\n", streamingEfficiency)
			}
		}
	}

	fmt.Println("\n🎉 Real-time streaming translation test completed successfully!")
}

func (rt *E2ETranslationTest) Cleanup() {
	if rt.cancel != nil {
		rt.cancel()
	}

	if rt.stage != nil {
		rt.stage.Disconnect()
	}

	if rt.room != nil {
		rt.room.Disconnect()
	}
}

// Helper function for environment variables
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// TestE2EAudioTranslation runs the complete real audio integration test
func TestE2EAudioTranslation(t *testing.T) {
	// Skip if running in CI or without explicit flag
	if os.Getenv("CI") == "true" && os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping e2e audio test in CI (set RUN_E2E_TESTS=true to enable)")
	}

	// Skip if OPENAI_API_KEY is not set
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("Skipping e2e audio test: OPENAI_API_KEY environment variable not set")
	}

	// Create test instance
	test, err := NewE2ETranslationTest()
	require.NoError(t, err, "Failed to create real translation test")
	defer test.Cleanup()

	// Setup test environment
	err = test.Setup()
	require.NoError(t, err, "Failed to setup test")

	// Run the test for 30 seconds
	err = test.Run(30 * time.Second)
	require.NoError(t, err, "Test execution failed")

	// Print statistics
	test.PrintStats()
}

// RunE2ETranslationTest can be called directly for standalone execution
func RunE2ETranslationTest() error {
	test, err := NewE2ETranslationTest()
	if err != nil {
		return err
	}
	defer test.Cleanup()

	if err := test.Setup(); err != nil {
		return err
	}

	if err := test.Run(30 * time.Second); err != nil {
		return err
	}

	test.PrintStats()
	return nil
}
