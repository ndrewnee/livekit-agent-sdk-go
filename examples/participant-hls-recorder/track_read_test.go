package main

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// TestTrackRead tests using track.Read() instead of track.ReadRTP()
// to see if raw bytes are delivered even when ReadRTP returns empty payloads
func TestTrackRead(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	t.Log("=== Testing track.Read() vs track.ReadRTP() ===")

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

	// Create room
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

	t.Log("\n=== Step 3: Connect RECORDER ===")

	// Track stats
	videoReadBytes := 0
	videoReadRTPPayloadBytes := 0
	audioReadBytes := 0
	audioReadRTPPayloadBytes := 0

	// Create callbacks for track subscription
	recorderCallbacks := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				t.Logf("  → Track subscribed: kind=%s, codec=%s", track.Kind(), track.Codec().MimeType)

				// Start goroutine to read using track.Read()
				go func() {
					packetCount := 0
					for {
						// Use track.Read() to read raw RTP packet bytes
						buf := make([]byte, 1500)
						_ = track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
						n, _, err := track.Read(buf)
						if err != nil {
							t.Logf("  → Read() error for %s: %v", track.Kind(), err)
							return
						}

						packetCount++

						if track.Kind() == webrtc.RTPCodecTypeVideo {
							videoReadBytes += n

							if packetCount <= 5 {
								t.Logf("[VIDEO] track.Read() packet %d: %d bytes", packetCount, n)
								if n > 0 {
									t.Logf("  First 8 bytes: %02x %02x %02x %02x %02x %02x %02x %02x",
										buf[0], buf[min(1, n-1)], buf[min(2, n-1)], buf[min(3, n-1)],
										buf[min(4, n-1)], buf[min(5, n-1)], buf[min(6, n-1)], buf[min(7, n-1)])
								}
							}
						} else if track.Kind() == webrtc.RTPCodecTypeAudio {
							audioReadBytes += n

							if packetCount <= 5 {
								t.Logf("[AUDIO] track.Read() packet %d: %d bytes", packetCount, n)
							}
						}
					}
				}()

				// Also test ReadRTP() for comparison
				go func() {
					packetCount := 0
					for {
						_ = track.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
						pkt, _, err := track.ReadRTP()
						if err != nil {
							t.Logf("  → ReadRTP() error for %s: %v", track.Kind(), err)
							return
						}

						packetCount++

						if track.Kind() == webrtc.RTPCodecTypeVideo {
							videoReadRTPPayloadBytes += len(pkt.Payload)

							if packetCount <= 5 {
								t.Logf("[VIDEO] track.ReadRTP() packet %d: payload=%d bytes, seq=%d",
									packetCount, len(pkt.Payload), pkt.SequenceNumber)
							}
						} else if track.Kind() == webrtc.RTPCodecTypeAudio {
							audioReadRTPPayloadBytes += len(pkt.Payload)

							if packetCount <= 5 {
								t.Logf("[AUDIO] track.ReadRTP() packet %d: payload=%d bytes",
									packetCount, len(pkt.Payload))
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
		ParticipantIdentity: "recorder-trackread",
		ParticipantName:     "Recorder TrackRead",
	}, recorderCallbacks)
	if err != nil {
		t.Fatalf("Failed to connect recorder: %v", err)
	}
	defer recorderRoom.Disconnect()
	t.Log("✓ Recorder connected")

	time.Sleep(2 * time.Second)

	t.Log("\n=== Step 4: Connect PUBLISHER ===")

	// Connect publisher
	publisherRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "publisher-trackread",
		ParticipantName:     "Publisher TrackRead",
	}, &lksdk.RoomCallback{})
	if err != nil {
		t.Fatalf("Failed to connect publisher: %v", err)
	}
	defer publisherRoom.Disconnect()
	t.Log("✓ Publisher connected")

	t.Log("\n=== Step 5: Publisher publishes tracks ===")

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
	t.Logf("VIDEO:")
	t.Logf("  track.Read():    %d bytes total", videoReadBytes)
	t.Logf("  track.ReadRTP(): %d bytes total", videoReadRTPPayloadBytes)

	t.Logf("AUDIO:")
	t.Logf("  track.Read():    %d bytes total", audioReadBytes)
	t.Logf("  track.ReadRTP(): %d bytes total", audioReadRTPPayloadBytes)

	// Analyze results
	if videoReadBytes > 0 && videoReadRTPPayloadBytes == 0 {
		t.Log("\n🎯 CRITICAL FINDING: track.Read() works but track.ReadRTP() returns empty payloads!")
		t.Log("   This means the issue is in RTP packet deserialization/parsing layer.")
	} else if videoReadBytes == 0 && videoReadRTPPayloadBytes == 0 {
		t.Log("\n❌ Both methods receive no video data - issue is in WebRTC transport layer")
	} else if videoReadBytes > 0 && videoReadRTPPayloadBytes > 0 {
		t.Log("\n✅ Both methods work - something different about this test setup!")
	}

	t.Log("\n✅ Test complete!")
}
