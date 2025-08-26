// simple_main.go - Simplified integration test without full MediaPipeline
// Build and run: go build -o realtime-test simple_main.go && ./realtime-test
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
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

// SimpleTranscriptionTest runs a simple end-to-end test
func SimpleTranscriptionTest() error {
	// Load .env file
	if err := godotenv.Load("../../.env"); err != nil {
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

	fmt.Println("=== Simple OpenAI Realtime API Test ===")
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
		return fmt.Errorf("failed to create room: %w", err)
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

	// Create OpenAI Realtime transcription stage
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
			fmt.Printf("[%s] TRANSCRIPTION ERROR: %v\n", timestamp, event.Error)
			return
		}

		status := "PARTIAL"
		if event.IsFinal {
			status = "FINAL  "
		}

		fmt.Printf("[%s] %s: %s\n", timestamp, status, event.Text)
	})

	// Connect to OpenAI Realtime API
	fmt.Println("Connecting to OpenAI Realtime API...")
	if err := realtimeStage.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to OpenAI: %w", err)
	}
	defer realtimeStage.Disconnect()
	fmt.Println("Connected to OpenAI Realtime API")

	// Create agent token and connect to room
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

	// Connect to room as agent with callbacks
	getLogger := logger.GetLogger()
	room, err := lksdk.ConnectToRoom(url, token, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				getLogger.Infow("Track subscribed",
					"trackID", track.ID(),
					"codec", track.Codec(),
					"participant", rp.Identity())

				if track.Kind() == webrtc.RTPCodecTypeAudio {
					// Process audio track in goroutine
					go func() {
						fmt.Printf("Processing audio track: %s\n", track.ID())
						packetCount := 0
						for {
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

							// Log progress every 100 packets
							if packetCount%100 == 0 {
								fmt.Printf("Processed %d audio packets\n", packetCount)
							}
						}
					}()
				}
			},
		},
		OnConnected: func() {
			fmt.Println("Agent connected to room")
		},
		OnDisconnected: func() {
			fmt.Println("Agent disconnected from room")
		},
	}, lksdk.WithAutoSubscribe(true))
	if err != nil {
		return fmt.Errorf("failed to connect agent to room: %w", err)
	}
	defer room.Disconnect()

	fmt.Println("Agent ready and listening...")

	// Start publisher in background after delay
	go func() {
		time.Sleep(2 * time.Second)
		fmt.Println("\nStarting audio publisher...")

		// Create publisher token
		at := auth.NewAccessToken(apiKey, apiSecret)
		grant := &auth.VideoGrant{
			RoomJoin:     true,
			Room:         roomName,
			CanPublish:   true,
			CanSubscribe: false,
		}
		at.AddGrant(grant).
			SetIdentity("audio-publisher").
			SetValidFor(3600)

		pubToken, _ := at.ToJWT()

		// Connect publisher
		pubRoom, err := lksdk.ConnectToRoom(url, pubToken, &lksdk.RoomCallback{
			OnConnected: func() {
				fmt.Println("Publisher connected to room")
			},
		})
		if err != nil {
			fmt.Printf("Publisher connection error: %v\n", err)
			return
		}
		defer pubRoom.Disconnect()

		// Create and publish a test audio track
		track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		})
		if err != nil {
			fmt.Printf("Track creation error: %v\n", err)
			return
		}

		_, err = pubRoom.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
			Name:   "test-audio",
			Source: livekit.TrackSource_MICROPHONE,
		})
		if err != nil {
			fmt.Printf("Track publish error: %v\n", err)
			return
		}

		fmt.Println("Publishing test audio...")

		// Send test audio packets
		ticker := time.NewTicker(20 * time.Millisecond) // 50Hz
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Send silence or test audio
				testData := make([]byte, 960*2*2) // 20ms of stereo 16-bit audio at 48kHz
				track.WriteSample(testData, 20*time.Millisecond)
			}
		}
	}()

	// Run for test duration
	testDuration := 30 * time.Second
	fmt.Printf("\nTest will run for %v (or press Ctrl+C to stop)\n", testDuration)
	fmt.Println("=" + "="*50)

	select {
	case <-time.After(testDuration):
		fmt.Println("\nTest duration completed")
	case <-ctx.Done():
		fmt.Println("\nTest cancelled")
	}

	// Print statistics
	stats := realtimeStage.GetStats()
	fmt.Println("\n" + "="*50)
	fmt.Println("=== Final Statistics ===")
	fmt.Printf("Total Transcriptions: %d\n", transcriptionCount)
	fmt.Printf("Partial: %d, Final: %d\n", stats.PartialTranscriptions, stats.FinalTranscriptions)
	fmt.Printf("Audio Packets: %d\n", stats.AudioPacketsSent)
	fmt.Printf("Errors: %d\n", stats.Errors)

	return nil
}

// WriteSample writes audio samples to the track
func (t *lksdk.LocalTrack) WriteSample(data []byte, duration time.Duration) error {
	// Convert to RTP packet
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111, // Opus
			SequenceNumber: uint16(time.Now().UnixNano() & 0xFFFF),
			Timestamp:      uint32(time.Now().UnixNano() / 1000000),
			SSRC:           12345,
		},
		Payload: data,
	}

	buf, err := packet.Marshal()
	if err != nil {
		return err
	}

	// Write to track (this is a simplified version)
	// In reality, you'd use the proper media writing API
	_ = buf
	return nil
}

// getEnv gets environment variable with default
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// main runs the test
func main() {
	if err := SimpleTranscriptionTest(); err != nil {
		log.Fatal(err)
	}
}
