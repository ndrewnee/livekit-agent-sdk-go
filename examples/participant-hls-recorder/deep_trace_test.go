package main

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// TestDeepTrace adds extensive logging to trace where H.264 payload disappears
func TestDeepTrace(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping deep trace test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	t.Log("=== DEEP TRACE: H.264 Payload Investigation ===")

	// Clean up
	os.RemoveAll("test-recordings")
	os.MkdirAll("test-recordings", 0755)

	// Create room
	roomServiceClient := lksdk.NewRoomServiceClient(testLiveKitURL2, testAPIKey2, testAPISecret2)
	_, _ = roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: testRoomName2,
	})
	_, err := roomServiceClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: testRoomName2,
	})
	if err != nil {
		t.Fatalf("Failed to create room: %v", err)
	}
	defer roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: testRoomName2,
	})

	t.Log("✓ Room created")

	// Create recorder with deep tracing
	recorderCallbacks := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if track.Kind() != webrtc.RTPCodecTypeVideo {
					return // Only trace video
				}

				t.Logf("✓ Video track subscribed: %s", track.Codec().MimeType)

				go func() {
					for packetNum := 0; packetNum < 10; packetNum++ {
						t.Logf("\n[TRACE %d] ========== Reading packet %d ==========", packetNum, packetNum+1)

						// Step 1: Read raw bytes
						buf := make([]byte, 1500)
						t.Logf("[TRACE %d] Step 1: Calling track.Read() for raw bytes", packetNum)
						n, _, err := track.Read(buf)
						if err != nil {
							t.Logf("[TRACE %d] track.Read() error: %v", packetNum, err)
							return
						}
						t.Logf("[TRACE %d] Step 1 RESULT: Read %d bytes", packetNum, n)
						t.Logf("[TRACE %d] First 20 bytes: %02x", packetNum, buf[:min(20, n)])

						// Step 2: Manually unmarshal to see if it works
						manualPacket := &rtp.Packet{}
						t.Logf("[TRACE %d] Step 2: Manually unmarshaling bytes [0:%d]", packetNum, n)
						if err := manualPacket.Unmarshal(buf[:n]); err != nil {
							t.Logf("[TRACE %d] Step 2 ERROR: Manual unmarshal failed: %v", packetNum, err)
						} else {
							t.Logf("[TRACE %d] Step 2 RESULT: Manual unmarshal SUCCESS", packetNum)
							t.Logf("[TRACE %d]   - Payload length: %d bytes", packetNum, len(manualPacket.Payload))
							t.Logf("[TRACE %d]   - Sequence: %d", packetNum, manualPacket.SequenceNumber)
							t.Logf("[TRACE %d]   - Timestamp: %d", packetNum, manualPacket.Timestamp)
							if len(manualPacket.Payload) > 0 {
								t.Logf("[TRACE %d]   - Payload first 4 bytes: %02x %02x %02x %02x",
									packetNum,
									manualPacket.Payload[0],
									manualPacket.Payload[min(1, len(manualPacket.Payload)-1)],
									manualPacket.Payload[min(2, len(manualPacket.Payload)-1)],
									manualPacket.Payload[min(3, len(manualPacket.Payload)-1)])
							}
						}

						// Step 3: Now call track.ReadRTP() and see what we get
						t.Logf("[TRACE %d] Step 3: Calling track.ReadRTP()", packetNum)
						rtpPacket, _, err := track.ReadRTP()
						if err != nil {
							t.Logf("[TRACE %d] Step 3 ERROR: track.ReadRTP() failed: %v", packetNum, err)
							return
						}
						t.Logf("[TRACE %d] Step 3 RESULT: ReadRTP() SUCCESS", packetNum)
						t.Logf("[TRACE %d]   - Payload length: %d bytes ❌", packetNum, len(rtpPacket.Payload))
						t.Logf("[TRACE %d]   - Sequence: %d", packetNum, rtpPacket.SequenceNumber)
						t.Logf("[TRACE %d]   - Timestamp: %d", packetNum, rtpPacket.Timestamp)
						if len(rtpPacket.Payload) > 0 {
							t.Logf("[TRACE %d]   - Payload first 4 bytes: %02x %02x %02x %02x",
								packetNum,
								rtpPacket.Payload[0],
								rtpPacket.Payload[min(1, len(rtpPacket.Payload)-1)],
								rtpPacket.Payload[min(2, len(rtpPacket.Payload)-1)],
								rtpPacket.Payload[min(3, len(rtpPacket.Payload)-1)])
						} else {
							t.Logf("[TRACE %d]   - ❌ PAYLOAD IS EMPTY!", packetNum)
						}

						t.Logf("[TRACE %d] ========== End of packet %d ==========\n", packetNum, packetNum+1)

						// Small delay between packets
						time.Sleep(100 * time.Millisecond)
					}
				}()
			},
		},
	}

	recorderRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "recorder-trace",
		ParticipantName:     "Recorder Trace",
	}, recorderCallbacks)
	if err != nil {
		t.Fatalf("Failed to connect recorder: %v", err)
	}
	defer recorderRoom.Disconnect()

	time.Sleep(2 * time.Second)

	// Connect publisher
	publisherRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "publisher-trace",
		ParticipantName:     "Publisher Trace",
	}, &lksdk.RoomCallback{})
	if err != nil {
		t.Fatalf("Failed to connect publisher: %v", err)
	}
	defer publisherRoom.Disconnect()

	// Create and publish video track
	testVideo := "../../examples/egress-agent/test-data/test.mp4"
	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, nil) // Video only
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}

	videoBound := make(chan struct{})
	videoTrack.OnBind(func() {
		close(videoBound)
	})

	_, err = publisherRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "test-video",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		t.Fatalf("Failed to publish video track: %v", err)
	}

	<-videoBound
	if err := publisher.Start(); err != nil {
		t.Fatalf("Failed to start publisher: %v", err)
	}

	t.Log("✓ Publisher started, waiting 5 seconds for trace...")
	time.Sleep(5 * time.Second)

	publisher.Stop()
	publisherRoom.Disconnect()
	recorderRoom.Disconnect()

	t.Log("\n✅ Deep trace complete - check logs above!")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
