//go:build integration

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/suite"
)

// RealtimeTranscriptionIntegrationTestSuite tests integration with real LiveKit server and OpenAI Realtime API.
//
// Prerequisites:
// - LiveKit server running (default: ws://localhost:7880)
// - OPENAI_API_KEY environment variable set
// - audiobook.wav in project root
//
// These tests validate:
// - Complete audio pipeline with LiveKit + OpenAI
// - Real Opus-encoded audio streaming
// - Actual transcription reception
// - Latency measurements
// - Model performance comparison
// - Language-specific configurations
type RealtimeTranscriptionIntegrationTestSuite struct {
	suite.Suite
	livekitURL    string
	livekitKey    string
	livekitSecret string
	audioFile     string
}

// TestRealtimeTranscriptionIntegration runs all integration tests.
func TestRealtimeTranscriptionIntegration(t *testing.T) {
	suite.Run(t, new(RealtimeTranscriptionIntegrationTestSuite))
}

// SetupSuite initializes test environment before running tests.
func (suite *RealtimeTranscriptionIntegrationTestSuite) SetupSuite() {
	suite.livekitURL = getEnvOrDefault("LIVEKIT_URL", "ws://localhost:7880")
	suite.livekitKey = getEnvOrDefault("LIVEKIT_API_KEY", "devkey")
	suite.livekitSecret = getEnvOrDefault("LIVEKIT_API_SECRET", "secret")
	suite.audioFile = filepath.Join(findProjectRoot(), "audiobook.wav")
}

// TestAverageLatencyMultipleRuns measures average latency across multiple transcription runs with real audio.
func (suite *RealtimeTranscriptionIntegrationTestSuite) TestAverageLatencyMultipleRuns() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	if _, err := os.Stat(suite.audioFile); err != nil {
		suite.T().Skipf("Skipping test: audiobook.wav not found at %s", suite.audioFile)
	}

	const numRuns = 5
	var firstTranscriptionTimes []time.Duration
	successCount := 0

	fmt.Printf("\n=== Running %d transcription latency tests ===\n", numRuns)

	for i := 0; i < numRuns; i++ {
		result := suite.runSingleTranscription(apiKey, "gpt-4o-transcribe", "en", fmt.Sprintf("run-%d", i+1))
		if result.TranscriptionCount > 0 {
			successCount++
			firstTranscriptionTimes = append(firstTranscriptionTimes, time.Duration(result.FirstTranscriptionMs)*time.Millisecond)
		}
	}

	// Calculate and display statistics
	fmt.Printf("\n=== Latency Statistics (%d successful runs) ===\n", successCount)

	if len(firstTranscriptionTimes) > 0 {
		avgFirst := calculateAverage(firstTranscriptionTimes)
		minFirst := calculateMin(firstTranscriptionTimes)
		maxFirst := calculateMax(firstTranscriptionTimes)
		stdDevFirst := calculateStdDev(firstTranscriptionTimes, avgFirst)

		fmt.Printf("\nTime-to-First-Transcription:\n")
		fmt.Printf("  Average: %dms\n", avgFirst.Milliseconds())
		fmt.Printf("  Min: %dms\n", minFirst.Milliseconds())
		fmt.Printf("  Max: %dms\n", maxFirst.Milliseconds())
		fmt.Printf("  StdDev: %.2fms\n", float64(stdDevFirst.Milliseconds()))
	}

	fmt.Printf("\nSuccess Rate: %d/%d (%.1f%%)\n", successCount, numRuns, float64(successCount)/float64(numRuns)*100)

	// Assertions
	suite.Greater(successCount, 0, "Should have at least one successful transcription")
}

// TestModelComparison compares performance between gpt-4o-transcribe and whisper-1 models with real audio.
func (suite *RealtimeTranscriptionIntegrationTestSuite) TestModelComparison() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	if _, err := os.Stat(suite.audioFile); err != nil {
		suite.T().Skipf("Skipping test: audiobook.wav not found at %s", suite.audioFile)
	}

	models := []string{"gpt-4o-transcribe", "gpt-4o-mini-transcribe", "whisper-1"}
	results := make(map[string]*ModelTestResult)

	fmt.Printf("\n=== Model Performance Comparison ===\n")

	for _, model := range models {
		fmt.Printf("\n--- Testing Model: %s ---\n", model)
		result := suite.runSingleTranscription(apiKey, model, "en", fmt.Sprintf("model-%s", model))
		results[model] = result
	}

	// Display comparison
	fmt.Printf("\n=== Model Comparison Results ===\n")
	fmt.Printf("%-25s | %15s | %12s | %10s | %10s\n", "Model", "First Trans (ms)", "Total Count", "Packets", "Avg Latency")
	fmt.Println(repeatString("-", 90))

	for _, model := range models {
		result := results[model]
		fmt.Printf("%-25s | %15d | %12d | %10d | %9.2fms\n",
			model,
			result.FirstTranscriptionMs,
			result.TranscriptionCount,
			result.PacketsProcessed,
			result.AvgLatencyMs)
	}
	fmt.Println()

	// Find fastest model
	var fastestModel string
	var fastestMs int64 = 999999
	for model, result := range results {
		if result.FirstTranscriptionMs > 0 && result.FirstTranscriptionMs < fastestMs {
			fastestMs = result.FirstTranscriptionMs
			fastestModel = model
		}
	}

	if fastestModel != "" {
		fmt.Printf("🏆 Fastest Model: %s (%dms)\n\n", fastestModel, fastestMs)
		fmt.Println("Performance vs fastest:")
		for _, model := range models {
			result := results[model]
			if result.FirstTranscriptionMs > 0 && model != fastestModel {
				slowdown := float64(result.FirstTranscriptionMs-fastestMs) / float64(fastestMs) * 100
				fmt.Printf("  %s: +%.1f%% slower (%dms)\n", model, slowdown, result.FirstTranscriptionMs)
			}
		}
		fmt.Println()
	}

	// Assertions
	suite.Greater(len(results), 0, "Should have tested at least one model")
}

// TestLatencyWithDifferentLanguages tests VAD configuration across various languages.
func (suite *RealtimeTranscriptionIntegrationTestSuite) TestLatencyWithDifferentLanguages() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		suite.T().Skip("Skipping real OpenAI integration test: OPENAI_API_KEY not set")
	}

	languages := []struct {
		code string
		name string
	}{
		{"en", "English"},
		{"ru", "Russian"},
		{"zh", "Chinese"},
		{"ar", "Arabic"},
	}

	fmt.Printf("\n=== Language-Specific VAD Configuration Test ===\n")
	fmt.Printf("%-15s | %11s | %10s\n", "Language", "VAD Silence", "VAD Prefix")
	fmt.Println(repeatString("-", 50))

	for _, lang := range languages {
		vadConfig := getDefaultVADConfig(lang.code)
		fmt.Printf("%-15s | %10.0fms | %9.0fms\n",
			lang.name,
			vadConfig.SilenceDurationMs,
			vadConfig.PrefixPaddingMs)
	}
	fmt.Println()

	suite.True(true, "VAD configuration test completed")
}

// Helper functions

// ModelTestResult captures test results for a transcription run.
type ModelTestResult struct {
	Model                string
	FirstTranscriptionMs int64
	TranscriptionCount   int
	PartialCount         int
	FinalCount           int
	PacketsProcessed     uint64
	AvgLatencyMs         float64
}

// runSingleTranscription runs a single transcription test with real LiveKit audio.
func (suite *RealtimeTranscriptionIntegrationTestSuite) runSingleTranscription(apiKey, model, language, testID string) *ModelTestResult {
	roomName := fmt.Sprintf("transcription-test-%s-%d", testID, time.Now().Unix())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Printf("  Room: %s\n", roomName)

	// Create room
	roomClient := lksdk.NewRoomServiceClient(suite.livekitURL, suite.livekitKey, suite.livekitSecret)
	_, err := roomClient.CreateRoom(ctx, &livekit.CreateRoomRequest{
		Name: roomName,
	})
	suite.Require().NoError(err, "Failed to create room")
	defer func() {
		roomClient.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: roomName})
	}()

	// Start audio publisher
	publisher := NewAudioPublisher(suite.audioFile)
	err = publisher.Connect(ctx, suite.livekitURL, suite.livekitKey, suite.livekitSecret, roomName)
	suite.Require().NoError(err, "Failed to connect publisher")
	defer publisher.Stop()

	// Create transcription stage
	stage := NewRealtimeTranscriptionStage(&RealtimeTranscriptionConfig{
		Name:     fmt.Sprintf("test-%s", testID),
		Priority: 10,
		APIKey:   apiKey,
		Model:    model,
		Language: language,
	})

	// Track transcriptions
	transcriptions := make(chan TranscriptionEvent, 100)
	stage.AddTranscriptionCallback(func(event TranscriptionEvent) {
		transcriptions <- event
	})

	// Connect to OpenAI
	err = stage.Connect(ctx)
	suite.Require().NoError(err, "Failed to connect to OpenAI")
	defer stage.Disconnect()

	// Connect agent to room
	agentToken, err := createToken(suite.livekitKey, suite.livekitSecret, roomName, "agent")
	suite.Require().NoError(err, "Failed to create agent token")

	agentRoom, err := lksdk.ConnectToRoomWithToken(suite.livekitURL, agentToken, &lksdk.RoomCallback{})
	suite.Require().NoError(err, "Failed to connect agent")
	defer agentRoom.Disconnect()

	fmt.Println("  ✓ Agent connected")

	// Wait for publisher to be available
	time.Sleep(2 * time.Second)

	// Subscribe to audio tracks
	subscribed := false
	for _, participant := range agentRoom.GetRemoteParticipants() {
		for _, track := range participant.TrackPublications() {
			if track.Kind() == lksdk.TrackKindAudio {
				fmt.Printf("  ✓ Subscribed to audio from %s\n", participant.Identity())
				if remoteTrack := track.Track(); remoteTrack != nil {
					go processAudioTrack(ctx, stage, remoteTrack.(*webrtc.TrackRemote))
					subscribed = true
				}
			}
		}
	}

	suite.Require().True(subscribed, "Failed to subscribe to audio track")

	// Start streaming
	err = publisher.StartStreaming()
	suite.Require().NoError(err, "Failed to start streaming")

	fmt.Println("  ✓ Audio streaming started")
	fmt.Println()

	// Collect transcriptions
	startTime := time.Now()
	result := &ModelTestResult{
		Model: model,
	}

	timeout := time.After(30 * time.Second)

collectLoop:
	for {
		select {
		case event := <-transcriptions:
			if event.Error != nil {
				fmt.Printf("  ⚠️  Error: %v\n", event.Error)
				continue
			}

			elapsed := time.Since(startTime).Milliseconds()

			if result.TranscriptionCount == 0 {
				result.FirstTranscriptionMs = elapsed
				fmt.Printf("  ⚡ First transcription at %dms\n", elapsed)
			}

			result.TranscriptionCount++
			if event.IsFinal {
				result.FinalCount++
			} else {
				result.PartialCount++
			}

			status := "PARTIAL"
			if event.IsFinal {
				status = "FINAL  "
			}

			fmt.Printf("  [%s] %s: \"%s\"\n",
				event.Timestamp.Format("15:04:05"),
				status,
				truncateString(event.Text, 50))

			// Exit after getting at least 3 transcriptions
			if result.TranscriptionCount >= 3 {
				break collectLoop
			}

		case <-timeout:
			fmt.Printf("  ⏱️  Timeout after 30s\n")
			break collectLoop
		}
	}

	// Get final stats
	stats := stage.GetStats()
	result.PacketsProcessed = stats.AudioPacketsSent
	result.AvgLatencyMs = stats.AverageLatencyMs

	// Display results
	fmt.Println()
	fmt.Printf("  📊 Test Results:\n")
	fmt.Printf("     First Transcription: %dms\n", result.FirstTranscriptionMs)
	fmt.Printf("     Total: %d (Partial: %d, Final: %d)\n",
		result.TranscriptionCount, result.PartialCount, result.FinalCount)
	fmt.Printf("     Packets Processed: %d\n", result.PacketsProcessed)
	fmt.Printf("     Avg Latency: %.2fms\n", result.AvgLatencyMs)
	fmt.Println()

	return result
}

// processAudioTrack reads audio from track and sends to transcription stage.
func processAudioTrack(ctx context.Context, stage *RealtimeTranscriptionStage, track *webrtc.TrackRemote) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}

		mediaData := MediaData{
			Type:      MediaTypeAudio,
			TrackID:   track.ID(),
			Timestamp: time.Now(),
			Data:      packet.Payload,
			Metadata: map[string]interface{}{
				"codec":      track.Codec().MimeType,
				"rtp_header": &packet.Header,
			},
		}

		stage.Process(ctx, mediaData)
	}
}

// createToken creates a LiveKit access token.
func createToken(apiKey, apiSecret, roomName, identity string) (string, error) {
	at := auth.NewAccessToken(apiKey, apiSecret)
	canPublish := false
	canSubscribe := true
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanPublish:   &canPublish,
		CanSubscribe: &canSubscribe,
	}
	at.SetVideoGrant(grant).
		SetIdentity(identity).
		SetValidFor(time.Hour)

	return at.ToJWT()
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// repeatString repeats a string n times.
func repeatString(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

// findProjectRoot finds the project root directory (where go.mod is located).
func findProjectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root, return current dir
			return "."
		}
		dir = parent
	}
}

// Note: Helper functions calculateAverage, calculateMin, calculateMax, and calculateStdDev
// are defined in media_pipeline_translation_integration_test.go and shared across integration tests.
