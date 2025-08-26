// working_test.go - Working integration test
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/joho/godotenv"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func main() {
	// Load .env file from current directory first, then parent directories
	envPaths := []string{".env", "../../.env", "../../../.env"}
	for _, path := range envPaths {
		if err := godotenv.Load(path); err == nil {
			absPath, _ := filepath.Abs(path)
			fmt.Printf("Loaded .env from: %s\n", absPath)
			break
		}
	}

	// Get configuration
	url := getEnv("LIVEKIT_URL", "ws://localhost:7880")
	apiKey := getEnv("LIVEKIT_API_KEY", "devkey")
	apiSecret := getEnv("LIVEKIT_API_SECRET", "secret")
	openaiKey := os.Getenv("OPENAI_API_KEY")

	if openaiKey == "" {
		log.Fatal("OPENAI_API_KEY environment variable is required")
	}

	fmt.Println("\n=== OpenAI Realtime API Transcription Test ===")
	fmt.Printf("LiveKit URL: %s\n", url)
	fmt.Printf("OpenAI API Key: %s...%s\n", openaiKey[:8], openaiKey[len(openaiKey)-4:])
	fmt.Println()

	// Create test room
	roomName := fmt.Sprintf("realtime-test-%d", time.Now().Unix())
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)
	_, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Realtime transcription test room",
	})
	if err != nil {
		log.Fatalf("Failed to create room: %v", err)
	}
	fmt.Printf("✓ Created test room: %s\n", roomName)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\n\nShutting down test...")
		cancel()
	}()

	// Create OpenAI Realtime transcription stage
	fmt.Println("\n1. Creating Realtime transcription stage...")
	realtimeStage := agent.NewRealtimeTranscriptionStage(
		"openai-realtime",
		10,
		openaiKey,
		"", // use default model
	)

	// Add transcription callback
	transcriptionCount := 0
	realtimeStage.AddTranscriptionCallback(func(event agent.TranscriptionEvent) {
		transcriptionCount++
		timestamp := event.Timestamp.Format("15:04:05.000")

		if event.Error != nil {
			fmt.Printf("[%s] ❌ TRANSCRIPTION ERROR: %v\n", timestamp, event.Error)
			return
		}

		status := "📝 PARTIAL"
		if event.IsFinal {
			status = "✅ FINAL  "
		}

		fmt.Printf("[%s] %s: %s\n", timestamp, status, event.Text)
	})

	// Connect to OpenAI Realtime API
	fmt.Println("2. Connecting to OpenAI Realtime API...")
	if err := realtimeStage.Connect(ctx); err != nil {
		log.Fatalf("Failed to connect to OpenAI: %v", err)
	}
	defer realtimeStage.Disconnect()
	fmt.Println("✓ Connected to OpenAI Realtime API")

	// Create agent connection
	fmt.Println("\n3. Connecting transcription agent to room...")
	agentRoom := connectAgent(ctx, url, apiKey, apiSecret, roomName, realtimeStage)
	defer agentRoom.Disconnect()
	fmt.Println("✓ Agent connected and listening")

	// Start publisher after a delay
	go func() {
		time.Sleep(3 * time.Second)
		fmt.Println("\n4. Starting audio publisher...")
		publishAudio(ctx, url, apiKey, apiSecret, roomName)
	}()

	// Run for test duration
	testDuration := 30 * time.Second
	fmt.Printf("\n📊 Test will run for %v (or press Ctrl+C to stop)\n", testDuration)
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("TRANSCRIPTIONS:")
	fmt.Println(strings.Repeat("=", 60))

	select {
	case <-time.After(testDuration):
		fmt.Println("\n\n⏱️ Test duration completed")
	case <-ctx.Done():
		fmt.Println("\n\n🛑 Test cancelled")
	}

	// Print statistics
	stats := realtimeStage.GetStats()
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📈 FINAL STATISTICS:")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Total Transcriptions:    %d\n", transcriptionCount)
	fmt.Printf("Partial Transcriptions:  %d\n", stats.PartialTranscriptions)
	fmt.Printf("Final Transcriptions:    %d\n", stats.FinalTranscriptions)
	fmt.Printf("Audio Packets Sent:      %d\n", stats.AudioPacketsSent)
	fmt.Printf("Errors:                  %d\n", stats.Errors)
	fmt.Printf("Connection Attempts:     %d\n", stats.ConnectionAttempts)
	fmt.Printf("Connection Successes:    %d\n", stats.ConnectionSuccesses)
	fmt.Printf("Connection Failures:     %d\n", stats.ConnectionFailures)
	fmt.Println(strings.Repeat("=", 60))
}

func connectAgent(ctx context.Context, url, apiKey, apiSecret, roomName string, realtimeStage *agent.RealtimeTranscriptionStage) *lksdk.Room {
	getLogger := logger.GetLogger()

	// Create agent token
	at := auth.NewAccessToken(apiKey, apiSecret)
	canSubscribe := true
	canPublish := false
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanSubscribe: &canSubscribe,
		CanPublish:   &canPublish,
	}
	at.AddGrant(grant).SetIdentity("transcription-agent").SetValidFor(3600)
	token, _ := at.ToJWT()

	// Connect to room with token
	room, err := lksdk.ConnectToRoomWithToken(url, token, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				getLogger.Infow("Track subscribed",
					"trackID", track.ID(),
					"codec", track.Codec(),
					"participant", rp.Identity())

				if track.Kind() == webrtc.RTPCodecTypeAudio {
					// Process audio track in goroutine
					go processAudioTrack(ctx, track, realtimeStage)
				}
			},
		},
	}, lksdk.WithAutoSubscribe(true))

	if err != nil {
		log.Fatalf("Failed to connect agent to room: %v", err)
	}

	return room
}

func processAudioTrack(ctx context.Context, track *webrtc.TrackRemote, realtimeStage *agent.RealtimeTranscriptionStage) {
	getLogger := logger.GetLogger()
	fmt.Printf("🎧 Processing audio track: %s\n", track.ID())

	packetCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
			packet, _, err := track.ReadRTP()
			if err != nil {
				getLogger.Errorw("Error reading RTP packet", err)
				return
			}

			packetCount++

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

			// Process with Realtime stage
			_, err = realtimeStage.Process(ctx, mediaData)
			if err != nil {
				getLogger.Errorw("Realtime processing error", err)
			}

			// Log progress every 500 packets (about 10 seconds)
			if packetCount%500 == 0 {
				fmt.Printf("📦 Processed %d audio packets\n", packetCount)
			}
		}
	}
}

func publishAudio(ctx context.Context, url, apiKey, apiSecret, roomName string) {
	// Find audio file
	audioFile := findAudioFile()
	if audioFile == "" {
		fmt.Println("❌ No audio file found (audiobook.wav)")
		return
	}
	fmt.Printf("✓ Found audio file: %s\n", audioFile)

	// Create publisher token
	at := auth.NewAccessToken(apiKey, apiSecret)
	canPub := true
	canSub := false
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanPublish:   &canPub,
		CanSubscribe: &canSub,
	}
	at.AddGrant(grant).SetIdentity("audio-publisher").SetValidFor(3600)
	pubToken, _ := at.ToJWT()

	// Connect publisher to room
	pubRoom, err := lksdk.ConnectToRoomWithToken(url, pubToken, &lksdk.RoomCallback{})
	if err != nil {
		fmt.Printf("❌ Publisher connection error: %v\n", err)
		return
	}
	defer pubRoom.Disconnect()
	fmt.Println("✓ Publisher connected to room")

	// Create and publish audio track
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		fmt.Printf("❌ Track creation error: %v\n", err)
		return
	}

	_, err = pubRoom.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "audiobook",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		fmt.Printf("❌ Track publish error: %v\n", err)
		return
	}

	fmt.Println("✓ Publishing audio stream...")

	// Stream audio file
	go streamAudioFile(ctx, track, audioFile)

	// Keep publisher alive
	<-ctx.Done()
}

func streamAudioFile(ctx context.Context, track *lksdk.LocalSampleTrack, audioFile string) {
	file, err := os.Open(audioFile)
	if err != nil {
		fmt.Printf("❌ Failed to open audio file: %v\n", err)
		return
	}
	defer file.Close()

	// Skip WAV header (44 bytes for standard WAV)
	file.Seek(44, 0)

	// Read and send audio data
	// Use smaller buffer to avoid packet size errors (max 1200 bytes)
	buffer := make([]byte, 960) // 10ms of mono 16-bit audio at 48kHz
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	frameCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := file.Read(buffer)
			if err != nil {
				// Loop the file
				file.Seek(44, 0)
				continue
			}

			if n > 0 {
				// Write samples to track using the proper API
				sample := media.Sample{
					Data:      buffer[:n],
					Duration:  20 * time.Millisecond,
					Timestamp: time.Now(),
				}
				if err := track.WriteSample(sample, nil); err != nil {
					fmt.Printf("❌ Write sample error: %v\n", err)
				}

				frameCount++
				if frameCount%1000 == 0 { // Every 20 seconds
					fmt.Printf("🎵 Streamed %d audio frames\n", frameCount)
				}
			}
		}
	}
}

func findAudioFile() string {
	paths := []string{
		"audiobook.wav",
		"../../audiobook.wav",
		"../../../audiobook.wav",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
