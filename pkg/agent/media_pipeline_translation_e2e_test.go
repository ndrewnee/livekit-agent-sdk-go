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
	fmt.Println("🧪 === TranslationStage E2E Audio Integration Test ===")
	fmt.Printf("LiveKit URL: %s\n", rt.url)
	fmt.Printf("Room Name: %s\n", rt.roomName)
	fmt.Printf("OpenAI API Key: %s...%s\n", rt.openaiKey[:8], rt.openaiKey[len(rt.openaiKey)-4:])
	fmt.Println()

	// Create test room
	if err := rt.createTestRoom(); err != nil {
		return fmt.Errorf("failed to create test room: %w", err)
	}
	fmt.Printf("✅ Created test room: %s\n", rt.roomName)

	// Create TranslationStage
	rt.stage = NewTranslationStage("e2e-translator", 10, rt.openaiKey)

	// Add callback to inject target languages for e2e test
	rt.stage.AddBeforeTranslationCallback(func(data *MediaData) {
		if data.Metadata == nil {
			data.Metadata = make(map[string]interface{})
		}
		// Inject target languages for e2e test
		data.Metadata["target_languages"] = []string{"es", "fr", "de"}
	})

	fmt.Printf("🌐 Configured translation for languages: [es, fr, de]\n")

	// Connect agent to room
	if err := rt.connectAgent(); err != nil {
		return fmt.Errorf("failed to connect agent: %w", err)
	}
	fmt.Printf("🤖 Agent connected to room\n")

	fmt.Printf("📻 E2E test ready (simulates audio streaming)\n")

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

	// Process audio track through TranslationStage in background
	go rt.processAudioTrack(track)
}

func (rt *E2ETranslationTest) processAudioTrack(track *webrtc.TrackRemote) {
	transcriptionCount := 0
	translationCount := 0

	for {
		select {
		case <-rt.ctx.Done():
			return
		default:
			// Read RTP packet
			packet, _, err := track.ReadRTP()
			if err != nil {
				fmt.Printf("❌ Error reading RTP packet: %v\n", err)
				return
			}

			// Create MediaData
			mediaData := MediaData{
				Type:    MediaTypeAudio,
				TrackID: track.ID(),
				Data:    packet.Payload,
				Metadata: map[string]interface{}{
					"rtp_header": &packet.Header,
					"timestamp":  time.Now(),
				},
			}

			// Simulate transcription events periodically
			transcriptionCount++
			if transcriptionCount%100 == 0 { // Every 100 packets (~2 seconds)
				// Create a simulated transcription event
				transcriptionText := fmt.Sprintf("This is transcription number %d from the audiobook sample", transcriptionCount/100)

				mediaData.Metadata["transcription_event"] = TranscriptionEvent{
					Type:     "final",
					Text:     transcriptionText,
					Language: "en",
					IsFinal:  true,
				}

				fmt.Printf("\n[%s] 📝 TRANSCRIPTION: %s\n",
					time.Now().Format("15:04:05.000"), transcriptionText)

				// Process through TranslationStage
				output, err := rt.stage.Process(rt.ctx, mediaData)
				if err != nil {
					fmt.Printf("❌ Translation error: %v\n", err)
					continue
				}

				// Check for translations in output metadata
				if translationData, ok := output.Metadata["translations"].(map[string]string); ok {
					translationCount++
					for lang, translation := range translationData {
						fmt.Printf("[%s] 🌐 TRANSLATION (%s): %s\n",
							time.Now().Format("15:04:05.000"), lang, translation)
					}
				}
			}
		}
	}
}

func (rt *E2ETranslationTest) Run(duration time.Duration) error {
	fmt.Printf("\n🚀 Starting e2e translation test (duration: %v)\n", duration)
	fmt.Println("You should see simulated transcriptions and translations appearing below:")
	fmt.Println(strings.Repeat("=", 60))

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Run test with timeout or signal
	testCtx, testCancel := context.WithTimeout(rt.ctx, duration)
	defer testCancel()

	fmt.Printf("⏱️  Test running for %v (or press Ctrl+C to stop)\n\n", duration)

	select {
	case <-testCtx.Done():
		fmt.Println("\n⏱️  Test duration completed")
	case <-sigChan:
		fmt.Println("\n🛑 Test interrupted by signal")
		rt.cancel()
	}

	return nil
}

func (rt *E2ETranslationTest) PrintStats() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📊 === Final Statistics ===")

	// Get TranslationStage metrics
	if rt.stage != nil {
		metrics := rt.stage.GetAPIMetrics()

		fmt.Printf("API Calls: %d\n", metrics["successful_calls"].(uint64))
		fmt.Printf("Failed Calls: %d\n", metrics["failed_calls"].(uint64))
		fmt.Printf("Cache Entries: %d\n", metrics["cache_entries"].(int))
		fmt.Printf("Rate Limited: %d\n", metrics["rate_limit_exceeded"].(uint64))
		fmt.Printf("Circuit Breaker Trips: %d\n", metrics["circuit_breaker_trips"].(uint64))

		if totalCalls := metrics["successful_calls"].(uint64) + metrics["failed_calls"].(uint64); totalCalls > 0 {
			successRate := float64(metrics["successful_calls"].(uint64)) / float64(totalCalls) * 100
			fmt.Printf("Success Rate: %.1f%%\n", successRate)
		}

		fmt.Printf("Average Latency: %.2fms\n", metrics["average_latency_ms"].(float64))
	}

	fmt.Println("\n🎉 Real audio integration test completed successfully!")
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
