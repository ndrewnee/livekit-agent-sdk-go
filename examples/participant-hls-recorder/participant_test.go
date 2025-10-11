package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

const (
	testLiveKitURL2 = "ws://localhost:7880"
	testAPIKey2     = "devkey"
	testAPISecret2  = "secret"
	testRoomName2   = "test-participant-recorder"
)

// TestAsRegularParticipant tests if the issue is specific to agents
// by connecting the recorder as a regular participant instead of an agent
func TestAsRegularParticipant(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	t.Log("=== Testing recorder as REGULAR PARTICIPANT (not agent) ===")

	// Clean up previous test recordings
	os.RemoveAll("test-recordings")
	os.MkdirAll("test-recordings", 0755)

	t.Log("\n=== Step 1: Verify test video exists ===")
	testVideo := "../../examples/egress-agent/test-data/test.mp4"
	if _, err := os.Stat(testVideo); os.IsNotExist(err) {
		t.Fatalf("Test video not found: %s", testVideo)
	}
	t.Log("✓ Test video exists")

	t.Log("\n=== Step 2: Create test room ===")
	roomServiceClient := lksdk.NewRoomServiceClient(testLiveKitURL2, testAPIKey2, testAPISecret2)

	// Delete room if it exists
	_, _ = roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: testRoomName2,
	})

	// Create room WITHOUT agent dispatch (regular room)
	_, err := roomServiceClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: testRoomName2,
	})
	if err != nil {
		t.Fatalf("Failed to create room: %v", err)
	}
	t.Logf("✓ Room created: %s", testRoomName2)

	// Clean up room on exit
	defer func() {
		_, _ = roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
			Room: testRoomName2,
		})
	}()

	t.Log("\n=== Step 3: Connect RECORDER as regular participant ===")

	// Track subscription monitoring
	videoPacketsReceived := 0
	audioPacketsReceived := 0
	videoPayloadTotal := 0
	audioPayloadTotal := 0

	// Create callbacks for track subscription
	recorderCallbacks := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				t.Logf("  → Track subscribed: kind=%s, codec=%s, participant=%s", track.Kind(), track.Codec().MimeType, rp.Identity())

				// Start goroutine to read RTP packets
				go func() {
					for {
						pkt, _, err := track.ReadRTP()
						if err != nil {
							t.Logf("  → ReadRTP error for %s: %v", track.Kind(), err)
							return
						}

						payloadSize := len(pkt.Payload)

						// CRITICAL: Copy payload immediately to prevent buffer reuse issues
						payloadCopy := make([]byte, payloadSize)
						copy(payloadCopy, pkt.Payload)

						if track.Kind() == webrtc.RTPCodecTypeVideo {
							videoPacketsReceived++
							videoPayloadTotal += payloadSize
							if videoPacketsReceived <= 5 {
								t.Logf("[DEBUG VIDEO PARTICIPANT] Packet %d: seq=%d, payloadSize=%d, PT=%d",
									videoPacketsReceived, pkt.SequenceNumber, payloadSize, pkt.PayloadType)
								if payloadSize > 0 {
									t.Logf("  → First 4 bytes: %02x %02x %02x %02x",
										pkt.Payload[0], pkt.Payload[min(1, payloadSize-1)],
										pkt.Payload[min(2, payloadSize-1)], pkt.Payload[min(3, payloadSize-1)])
								} else {
									t.Logf("  → PAYLOAD IS EMPTY (cap=%d)", cap(pkt.Payload))
								}
							}
						} else if track.Kind() == webrtc.RTPCodecTypeAudio {
							audioPacketsReceived++
							audioPayloadTotal += payloadSize
							if audioPacketsReceived <= 5 {
								t.Logf("[DEBUG AUDIO PARTICIPANT] Packet %d: seq=%d, payloadSize=%d",
									audioPacketsReceived, pkt.SequenceNumber, payloadSize)
							}
						}
					}
				}()
			},
		},
	}

	// Create recorder participant
	recorderRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "recorder-participant",
		ParticipantName:     "Recorder Participant",
	}, recorderCallbacks)
	if err != nil {
		t.Fatalf("Failed to connect recorder: %v", err)
	}
	defer recorderRoom.Disconnect()
	t.Log("✓ Recorder connected as regular participant")

	time.Sleep(2 * time.Second)

	t.Log("\n=== Step 4: Connect PUBLISHER participant ===")

	// Connect publisher
	publisherRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "publisher-participant",
		ParticipantName:     "Publisher Participant",
	}, &lksdk.RoomCallback{})
	if err != nil {
		t.Fatalf("Failed to connect publisher: %v", err)
	}
	defer publisherRoom.Disconnect()
	t.Log("✓ Publisher connected")

	t.Log("\n=== Step 5: Publisher publishes video track ===")

	// Create video track
	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	// Create audio track
	audioTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		t.Fatalf("Failed to create audio track: %v", err)
	}

	// Create publisher
	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}

	// Set up OnBind callbacks
	videoBound := make(chan struct{})
	audioBound := make(chan struct{})

	videoTrack.OnBind(func() {
		t.Log("  → Video track bound")
		close(videoBound)
	})

	audioTrack.OnBind(func() {
		t.Log("  → Audio track bound")
		close(audioBound)
	})

	// Publish tracks
	_, err = publisherRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "test-video",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		t.Fatalf("Failed to publish video track: %v", err)
	}
	t.Log("✓ Video track published")

	_, err = publisherRoom.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name: "test-audio",
	})
	if err != nil {
		t.Fatalf("Failed to publish audio track: %v", err)
	}
	t.Log("✓ Audio track published")

	// Wait for tracks to bind
	t.Log("  → Waiting for tracks to bind...")
	<-videoBound
	<-audioBound
	t.Log("  → Both tracks bound, starting publisher")

	t.Log("\n=== Step 6: Start publishing ===")
	if err := publisher.Start(); err != nil {
		t.Fatalf("Failed to start publisher: %v", err)
	}
	t.Log("✓ Publisher started")

	t.Log("\n=== Step 7: Wait for publishing (10 seconds) ===")
	time.Sleep(10 * time.Second)

	t.Log("\n=== Step 8: Stop and disconnect ===")
	publisher.Stop()
	publisherRoom.Disconnect()
	recorderRoom.Disconnect()
	time.Sleep(2 * time.Second)

	t.Log("\n=== RESULTS ===")
	t.Logf("Video packets received: %d (total payload: %d bytes)", videoPacketsReceived, videoPayloadTotal)
	t.Logf("Audio packets received: %d (total payload: %d bytes)", audioPacketsReceived, audioPayloadTotal)

	if videoPacketsReceived > 0 && videoPayloadTotal == 0 {
		t.Log("❌ ISSUE CONFIRMED: Video packets received but ALL payloads empty (regular participant)")
	} else if videoPacketsReceived > 0 && videoPayloadTotal > 0 {
		t.Log("✅ SUCCESS: Regular participant receives video packets with FULL payloads!")
		t.Log("   This means the issue is AGENT-SPECIFIC, not a late subscription problem")
	} else {
		t.Log("⚠️  WARNING: No video packets received by recorder")
	}

	t.Log("\n✅ Test complete!")
}

func validateParticipantRecording(t *testing.T, roomName, participantIdentity string) error {
	outputFile := fmt.Sprintf("test-recordings/%s/%s/output.ts", roomName, participantIdentity)
	stat, err := os.Stat(outputFile)
	if os.IsNotExist(err) {
		return fmt.Errorf("output file not found: %s", outputFile)
	}
	if err != nil {
		return fmt.Errorf("failed to stat output file: %w", err)
	}

	if stat.Size() == 0 {
		return fmt.Errorf("output file is empty")
	}

	t.Logf("  → Output file exists: %s (size: %d bytes)", outputFile, stat.Size())
	return nil
}
