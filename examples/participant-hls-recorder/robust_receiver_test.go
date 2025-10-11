package main

import (
	"context"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// RobustH264Receiver implements all best practices for H.264 RTP reception
type RobustH264Receiver struct {
	track              *webrtc.TrackRemote
	mu                 sync.Mutex
	packetsReceived    int
	packetsEmpty       int
	packetsWithPayload int
	keyframesReceived  int
	firstKeyframeAt    int
	bytesReceived      int64
	done               chan struct{}
	ready              chan struct{} // Signals when first keyframe is received
	logger             *log.Logger
}

func NewRobustH264Receiver(track *webrtc.TrackRemote, logger *log.Logger) *RobustH264Receiver {
	return &RobustH264Receiver{
		track:  track,
		done:   make(chan struct{}),
		ready:  make(chan struct{}),
		logger: logger,
	}
}

// Start begins receiving packets (runs until Stop is called)
func (r *RobustH264Receiver) Start() {
	go r.receiveLoop()
}

// Stop stops receiving packets
func (r *RobustH264Receiver) Stop() {
	close(r.done)
}

// WaitReady waits until first keyframe is received (with timeout)
func (r *RobustH264Receiver) WaitReady(timeout time.Duration) bool {
	select {
	case <-r.ready:
		return true
	case <-time.After(timeout):
		return false
	}
}

// receiveLoop implements all 4 best practices
func (r *RobustH264Receiver) receiveLoop() {
	r.logger.Println("📡 Receiver: Starting packet receive loop")

	readySignaled := false

	// 1. ✅ CONTINUOUSLY read packets (don't stop after N packets)
	for {
		select {
		case <-r.done:
			r.logger.Println("📡 Receiver: Stopped")
			return
		default:
		}

		buf := make([]byte, 1500)
		n, _, err := r.track.Read(buf)
		if err != nil {
			r.logger.Printf("📡 Receiver: Read error: %v", err)
			return
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(buf[:n]); err != nil {
			r.logger.Printf("📡 Receiver: Unmarshal error: %v", err)
			continue
		}

		r.mu.Lock()
		r.packetsReceived++
		packetNum := r.packetsReceived
		r.mu.Unlock()

		// 2. ✅ SKIP empty packets
		if len(packet.Payload) == 0 {
			r.mu.Lock()
			r.packetsEmpty++
			r.mu.Unlock()

			// Log first few empty packets
			if packetNum <= 5 {
				r.logger.Printf("📡 Receiver: Packet %d: EMPTY (seq=%d) - skipping ⏭️",
					packetNum, packet.SequenceNumber)
			}
			continue // ← CRITICAL: Skip processing
		}

		// Packet has payload
		r.mu.Lock()
		r.packetsWithPayload++
		r.bytesReceived += int64(len(packet.Payload))
		payloadCount := r.packetsWithPayload
		r.mu.Unlock()

		// 3. ✅ WAIT for first H.264 keyframe before processing
		if !readySignaled {
			isKeyframe := isH264Keyframe(packet.Payload)

			if isKeyframe {
				r.mu.Lock()
				r.keyframesReceived++
				r.firstKeyframeAt = packetNum
				r.mu.Unlock()

				r.logger.Printf("🔑 Receiver: KEYFRAME received at packet %d (seq=%d, payload=%d bytes)",
					packetNum, packet.SequenceNumber, len(packet.Payload))
				r.logger.Println("✅ Receiver: Ready for processing!")

				// 4. ✅ PROPER synchronization (no hardcoded delays)
				close(r.ready) // Signal that receiver is ready
				readySignaled = true
			} else {
				// Still waiting for keyframe
				if payloadCount <= 3 {
					r.logger.Printf("⏳ Receiver: Packet %d has payload (%d bytes, seq=%d) but NOT a keyframe - waiting...",
						packetNum, len(packet.Payload), packet.SequenceNumber)
				}
				continue // Skip non-keyframe packets until we get first keyframe
			}
		}

		// Check if this is a keyframe (after first one)
		if readySignaled && isH264Keyframe(packet.Payload) {
			r.mu.Lock()
			r.keyframesReceived++
			r.mu.Unlock()
		}

		// Now ready to process packet
		if payloadCount <= 10 || (payloadCount%50 == 0) {
			r.logger.Printf("📦 Receiver: Packet %d: seq=%d payload=%d bytes (total: %d packets, %d bytes)",
				packetNum, packet.SequenceNumber, len(packet.Payload), payloadCount, r.bytesReceived)
		}

		// Here you would normally process the packet (save to file, decode, etc.)
		// processPacket(packet)
	}
}

// GetStats returns receiver statistics
func (r *RobustH264Receiver) GetStats() (received, empty, withPayload, keyframes, firstKeyframeAt int, bytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.packetsReceived, r.packetsEmpty, r.packetsWithPayload, r.keyframesReceived, r.firstKeyframeAt, r.bytesReceived
}

// TestRobustReceiver demonstrates the robust H.264 receiver implementation
func TestRobustReceiver(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping robust receiver test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	t.Log("=== ROBUST H.264 RECEIVER TEST ===")
	t.Log("Implements all 4 best practices:")
	t.Log("  1. ✅ Continuously read packets")
	t.Log("  2. ✅ Skip empty packets")
	t.Log("  3. ✅ Wait for first keyframe")
	t.Log("  4. ✅ Proper synchronization (no hardcoded delays)")

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

	var receiver *RobustH264Receiver
	var receiverMu sync.Mutex
	trackSubscribed := make(chan struct{})

	// Create recorder with robust receiver
	recorderCallbacks := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if track.Kind() != webrtc.RTPCodecTypeVideo {
					return
				}

				t.Logf("✓ Video track subscribed: %s", track.Codec().MimeType)

				// Create and start robust receiver
				receiverMu.Lock()
				receiver = NewRobustH264Receiver(track, log.Default())
				receiver.Start()
				receiverMu.Unlock()

				close(trackSubscribed)
			},
		},
	}

	recorderRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "recorder-robust",
		ParticipantName:     "Recorder Robust",
	}, recorderCallbacks)
	if err != nil {
		t.Fatalf("Failed to connect recorder: %v", err)
	}
	defer recorderRoom.Disconnect()

	time.Sleep(1 * time.Second)

	// Connect publisher
	publisherRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "publisher-robust",
		ParticipantName:     "Publisher Robust",
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

	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, nil)
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

	t.Log("✓ Publisher started")

	// Wait for track to be subscribed
	select {
	case <-trackSubscribed:
		t.Log("✓ Track subscribed")
	case <-time.After(10 * time.Second):
		t.Fatal("❌ Timeout waiting for track subscription")
	}

	// 4. ✅ Wait for receiver to be ready (proper synchronization!)
	t.Log("⏳ Waiting for receiver to receive first keyframe...")

	receiverMu.Lock()
	r := receiver
	receiverMu.Unlock()

	if r == nil {
		t.Fatal("❌ Receiver not initialized")
	}

	if !r.WaitReady(10 * time.Second) {
		t.Fatal("❌ Timeout waiting for first keyframe")
	}

	t.Log("✅ Receiver is ready! First keyframe received.")

	// Let it run for a few seconds to collect more packets
	t.Log("📊 Collecting more packets for 5 seconds...")
	time.Sleep(5 * time.Second)

	// Stop receiver
	receiverMu.Lock()
	r = receiver
	receiverMu.Unlock()

	if r != nil {
		r.Stop()
	}

	publisher.Stop()
	publisherRoom.Disconnect()
	recorderRoom.Disconnect()

	// Get statistics
	received, empty, withPayload, keyframes, firstKeyframeAt, bytes := r.GetStats()

	t.Log("\n========================================")
	t.Log("STATISTICS")
	t.Log("========================================")
	t.Logf("Total packets received: %d", received)
	t.Logf("Empty packets (skipped): %d", empty)
	t.Logf("Packets with payload: %d", withPayload)
	t.Logf("Keyframes received: %d", keyframes)
	t.Logf("First keyframe at packet: %d", firstKeyframeAt)
	t.Logf("Total bytes received: %d", bytes)

	// Verify results
	if withPayload == 0 {
		t.Fatal("❌ FAIL: No packets with payload received")
	}

	if keyframes == 0 {
		t.Fatal("❌ FAIL: No keyframes detected")
	}

	if empty > 0 {
		t.Logf("✅ Successfully skipped %d empty packets", empty)
	}

	t.Logf("✅ Successfully received %d packets with payloads", withPayload)
	t.Logf("✅ Successfully detected %d keyframes", keyframes)

	t.Log("\n========================================")
	t.Log("CONCLUSION")
	t.Log("========================================")
	t.Log("✅ All 4 best practices implemented successfully:")
	t.Log("   1. ✅ Continuous reading (ran until Stop called)")
	t.Log("   2. ✅ Empty packets skipped")
	t.Log("   3. ✅ Waited for first keyframe before processing")
	t.Log("   4. ✅ Proper synchronization (WaitReady channel)")
	t.Log("")
	t.Log("🎉 Robust H.264 receiver works correctly!")
}
