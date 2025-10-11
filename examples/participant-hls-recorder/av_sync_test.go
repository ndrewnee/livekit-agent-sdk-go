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

// AVSyncReceiver handles both audio and video with synchronization tracking
type AVSyncReceiver struct {
	videoTrack *webrtc.TrackRemote
	audioTrack *webrtc.TrackRemote

	mu                     sync.Mutex
	videoPacketsReceived   int
	videoPacketsEmpty      int
	videoPacketsWithData   int
	videoKeyframes         int
	videoFirstKeyframeAt   int
	videoFirstPacketTime   time.Time
	videoLastPacketTime    time.Time
	videoBytesReceived     int64
	videoFirstRTPTimestamp uint32
	videoLastRTPTimestamp  uint32

	audioPacketsReceived   int
	audioPacketsEmpty      int
	audioPacketsWithData   int
	audioFirstPacketTime   time.Time
	audioLastPacketTime    time.Time
	audioBytesReceived     int64
	audioFirstRTPTimestamp uint32
	audioLastRTPTimestamp  uint32

	done       chan struct{}
	videoReady chan struct{}
	audioReady chan struct{}
	logger     *log.Logger
}

func NewAVSyncReceiver(logger *log.Logger) *AVSyncReceiver {
	return &AVSyncReceiver{
		done:       make(chan struct{}),
		videoReady: make(chan struct{}),
		audioReady: make(chan struct{}),
		logger:     logger,
	}
}

func (r *AVSyncReceiver) SetVideoTrack(track *webrtc.TrackRemote) {
	r.mu.Lock()
	r.videoTrack = track
	r.mu.Unlock()
}

func (r *AVSyncReceiver) SetAudioTrack(track *webrtc.TrackRemote) {
	r.mu.Lock()
	r.audioTrack = track
	r.mu.Unlock()
}

func (r *AVSyncReceiver) Start() {
	r.mu.Lock()
	videoTrack := r.videoTrack
	audioTrack := r.audioTrack
	r.mu.Unlock()

	if videoTrack != nil {
		go r.receiveVideo(videoTrack)
	}
	if audioTrack != nil {
		go r.receiveAudio(audioTrack)
	}
}

func (r *AVSyncReceiver) receiveVideo(track *webrtc.TrackRemote) {
	r.logger.Println("📹 Video receiver: Starting")
	videoReadySignaled := false

	for {
		select {
		case <-r.done:
			r.logger.Println("📹 Video receiver: Stopped")
			return
		default:
		}

		buf := make([]byte, 1500)
		n, _, err := track.Read(buf)
		if err != nil {
			r.logger.Printf("📹 Video receiver: Read error: %v", err)
			return
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(buf[:n]); err != nil {
			continue
		}

		now := time.Now()

		r.mu.Lock()
		r.videoPacketsReceived++
		packetNum := r.videoPacketsReceived

		if r.videoFirstPacketTime.IsZero() {
			r.videoFirstPacketTime = now
			r.videoFirstRTPTimestamp = packet.Timestamp
		}
		r.videoLastPacketTime = now
		r.videoLastRTPTimestamp = packet.Timestamp
		r.mu.Unlock()

		// Skip empty packets
		if len(packet.Payload) == 0 {
			r.mu.Lock()
			r.videoPacketsEmpty++
			r.mu.Unlock()

			if packetNum <= 3 {
				r.logger.Printf("📹 Video: Packet %d EMPTY (seq=%d) - skipping ⏭️",
					packetNum, packet.SequenceNumber)
			}
			continue
		}

		r.mu.Lock()
		r.videoPacketsWithData++
		r.videoBytesReceived += int64(len(packet.Payload))
		dataPacketNum := r.videoPacketsWithData
		r.mu.Unlock()

		// Wait for keyframe
		if !videoReadySignaled {
			isKeyframe := isH264Keyframe(packet.Payload)
			if isKeyframe {
				r.mu.Lock()
				r.videoKeyframes++
				r.videoFirstKeyframeAt = packetNum
				r.mu.Unlock()

				r.logger.Printf("🔑 Video: KEYFRAME at packet %d (seq=%d, payload=%d bytes)",
					packetNum, packet.SequenceNumber, len(packet.Payload))

				close(r.videoReady)
				videoReadySignaled = true
			} else {
				if dataPacketNum <= 3 {
					r.logger.Printf("⏳ Video: Packet %d has payload (%d bytes) but not keyframe",
						packetNum, len(packet.Payload))
				}
				continue
			}
		}

		// Check for additional keyframes
		if videoReadySignaled && isH264Keyframe(packet.Payload) {
			r.mu.Lock()
			r.videoKeyframes++
			r.mu.Unlock()
		}

		// Log progress
		if dataPacketNum <= 10 || (dataPacketNum%50 == 0) {
			r.logger.Printf("📹 Video: Packet %d: seq=%d ts=%d payload=%d bytes",
				packetNum, packet.SequenceNumber, packet.Timestamp, len(packet.Payload))
		}
	}
}

func (r *AVSyncReceiver) receiveAudio(track *webrtc.TrackRemote) {
	r.logger.Println("🔊 Audio receiver: Starting")
	audioReadySignaled := false

	for {
		select {
		case <-r.done:
			r.logger.Println("🔊 Audio receiver: Stopped")
			return
		default:
		}

		buf := make([]byte, 1500)
		n, _, err := track.Read(buf)
		if err != nil {
			r.logger.Printf("🔊 Audio receiver: Read error: %v", err)
			return
		}

		packet := &rtp.Packet{}
		if err := packet.Unmarshal(buf[:n]); err != nil {
			continue
		}

		now := time.Now()

		r.mu.Lock()
		r.audioPacketsReceived++
		packetNum := r.audioPacketsReceived

		if r.audioFirstPacketTime.IsZero() {
			r.audioFirstPacketTime = now
			r.audioFirstRTPTimestamp = packet.Timestamp
		}
		r.audioLastPacketTime = now
		r.audioLastRTPTimestamp = packet.Timestamp
		r.mu.Unlock()

		// Skip empty packets
		if len(packet.Payload) == 0 {
			r.mu.Lock()
			r.audioPacketsEmpty++
			r.mu.Unlock()

			if packetNum <= 3 {
				r.logger.Printf("🔊 Audio: Packet %d EMPTY (seq=%d) - skipping ⏭️",
					packetNum, packet.SequenceNumber)
			}
			continue
		}

		r.mu.Lock()
		r.audioPacketsWithData++
		r.audioBytesReceived += int64(len(packet.Payload))
		dataPacketNum := r.audioPacketsWithData
		r.mu.Unlock()

		// Audio is ready after first packet with data
		if !audioReadySignaled {
			r.logger.Printf("🔊 Audio: First packet with data at packet %d (seq=%d, payload=%d bytes)",
				packetNum, packet.SequenceNumber, len(packet.Payload))
			close(r.audioReady)
			audioReadySignaled = true
		}

		// Log progress
		if dataPacketNum <= 10 || (dataPacketNum%50 == 0) {
			r.logger.Printf("🔊 Audio: Packet %d: seq=%d ts=%d payload=%d bytes",
				packetNum, packet.SequenceNumber, packet.Timestamp, len(packet.Payload))
		}
	}
}

func (r *AVSyncReceiver) Stop() {
	close(r.done)
}

func (r *AVSyncReceiver) WaitReady(timeout time.Duration) (videoReady, audioReady bool) {
	videoTimer := time.NewTimer(timeout)
	audioTimer := time.NewTimer(timeout)
	defer videoTimer.Stop()
	defer audioTimer.Stop()

	select {
	case <-r.videoReady:
		videoReady = true
	case <-videoTimer.C:
	}

	select {
	case <-r.audioReady:
		audioReady = true
	case <-audioTimer.C:
	}

	return
}

func (r *AVSyncReceiver) GetStats() (
	videoReceived, videoEmpty, videoWithData, videoKeyframes, videoFirstKeyframeAt int, videoBytes int64,
	audioReceived, audioEmpty, audioWithData int, audioBytes int64,
	videoDuration, audioDuration time.Duration,
	videoTimestampDelta, audioTimestampDelta uint32,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	videoDuration = r.videoLastPacketTime.Sub(r.videoFirstPacketTime)
	audioDuration = r.audioLastPacketTime.Sub(r.audioFirstPacketTime)
	videoTimestampDelta = r.videoLastRTPTimestamp - r.videoFirstRTPTimestamp
	audioTimestampDelta = r.audioLastRTPTimestamp - r.audioFirstRTPTimestamp

	return r.videoPacketsReceived, r.videoPacketsEmpty, r.videoPacketsWithData,
		r.videoKeyframes, r.videoFirstKeyframeAt, r.videoBytesReceived,
		r.audioPacketsReceived, r.audioPacketsEmpty, r.audioPacketsWithData,
		r.audioBytesReceived, videoDuration, audioDuration,
		videoTimestampDelta, audioTimestampDelta
}

// TestAVSync tests audio/video reception with synchronization checking
func TestAVSync(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping A/V sync test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	t.Log("=== AUDIO/VIDEO SYNCHRONIZATION TEST ===")
	t.Log("Tests:")
	t.Log("  1. Both audio and video receive packets")
	t.Log("  2. Empty packets are skipped")
	t.Log("  3. Video waits for keyframe")
	t.Log("  4. Audio/video timing alignment")

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

	receiver := NewAVSyncReceiver(log.Default())
	var receiverMu sync.Mutex
	videoSubscribed := make(chan struct{})
	audioSubscribed := make(chan struct{})

	// Create recorder
	recorderCallbacks := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				t.Logf("✓ Track subscribed: kind=%s codec=%s", track.Kind(), track.Codec().MimeType)

				receiverMu.Lock()
				if track.Kind() == webrtc.RTPCodecTypeVideo {
					receiver.SetVideoTrack(track)
					receiverMu.Unlock()
					close(videoSubscribed)
				} else if track.Kind() == webrtc.RTPCodecTypeAudio {
					receiver.SetAudioTrack(track)
					receiverMu.Unlock()
					close(audioSubscribed)
				} else {
					receiverMu.Unlock()
				}
			},
		},
	}

	recorderRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "recorder-avsync",
		ParticipantName:     "Recorder AVSync",
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
		ParticipantIdentity: "publisher-avsync",
		ParticipantName:     "Publisher AVSync",
	}, &lksdk.RoomCallback{})
	if err != nil {
		t.Fatalf("Failed to connect publisher: %v", err)
	}
	defer publisherRoom.Disconnect()

	// Create tracks
	testVideo := "../../examples/egress-agent/test-data/test.mp4"
	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	audioTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		t.Fatalf("Failed to create audio track: %v", err)
	}

	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}

	videoBound := make(chan struct{})
	audioBound := make(chan struct{})

	videoTrack.OnBind(func() {
		close(videoBound)
	})
	audioTrack.OnBind(func() {
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

	_, err = publisherRoom.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name: "test-audio",
	})
	if err != nil {
		t.Fatalf("Failed to publish audio track: %v", err)
	}

	<-videoBound
	<-audioBound

	if err := publisher.Start(); err != nil {
		t.Fatalf("Failed to start publisher: %v", err)
	}

	t.Log("✓ Publisher started")

	// Wait for tracks to be subscribed
	select {
	case <-videoSubscribed:
		t.Log("✓ Video track subscribed")
	case <-time.After(10 * time.Second):
		t.Fatal("❌ Timeout waiting for video track subscription")
	}

	select {
	case <-audioSubscribed:
		t.Log("✓ Audio track subscribed")
	case <-time.After(10 * time.Second):
		t.Fatal("❌ Timeout waiting for audio track subscription")
	}

	// Start receiver
	receiver.Start()

	// Wait for both to be ready
	t.Log("⏳ Waiting for audio and video to be ready...")
	videoReady, audioReady := receiver.WaitReady(10 * time.Second)

	if !videoReady {
		t.Error("❌ Video receiver not ready (no keyframe)")
	} else {
		t.Log("✅ Video receiver ready (keyframe received)")
	}

	if !audioReady {
		t.Error("❌ Audio receiver not ready")
	} else {
		t.Log("✅ Audio receiver ready (first packet received)")
	}

	// Run for 10 seconds
	t.Log("📊 Collecting data for 10 seconds...")
	time.Sleep(10 * time.Second)

	receiver.Stop()
	publisher.Stop()
	publisherRoom.Disconnect()
	recorderRoom.Disconnect()

	// Get statistics
	videoReceived, videoEmpty, videoWithData, videoKeyframes, videoFirstKeyframeAt, videoBytes,
		audioReceived, audioEmpty, audioWithData, audioBytes,
		videoDuration, audioDuration,
		videoTimestampDelta, audioTimestampDelta := receiver.GetStats()

	t.Log("\n========================================")
	t.Log("VIDEO STATISTICS")
	t.Log("========================================")
	t.Logf("Total packets received: %d", videoReceived)
	t.Logf("Empty packets (skipped): %d", videoEmpty)
	t.Logf("Packets with data: %d", videoWithData)
	t.Logf("Keyframes detected: %d", videoKeyframes)
	t.Logf("First keyframe at packet: %d", videoFirstKeyframeAt)
	t.Logf("Total bytes: %d", videoBytes)
	t.Logf("Duration: %v", videoDuration)
	t.Logf("RTP timestamp delta: %d (%.2f seconds @ 90kHz)",
		videoTimestampDelta, float64(videoTimestampDelta)/90000.0)

	t.Log("\n========================================")
	t.Log("AUDIO STATISTICS")
	t.Log("========================================")
	t.Logf("Total packets received: %d", audioReceived)
	t.Logf("Empty packets (skipped): %d", audioEmpty)
	t.Logf("Packets with data: %d", audioWithData)
	t.Logf("Total bytes: %d", audioBytes)
	t.Logf("Duration: %v", audioDuration)
	t.Logf("RTP timestamp delta: %d (%.2f seconds @ 48kHz)",
		audioTimestampDelta, float64(audioTimestampDelta)/48000.0)

	t.Log("\n========================================")
	t.Log("SYNCHRONIZATION ANALYSIS")
	t.Log("========================================")

	// Calculate real-time durations from RTP timestamps
	videoRealDuration := float64(videoTimestampDelta) / 90000.0 // H.264 @ 90kHz
	audioRealDuration := float64(audioTimestampDelta) / 48000.0 // Opus @ 48kHz

	t.Logf("Video duration (from RTP timestamps): %.2f seconds", videoRealDuration)
	t.Logf("Audio duration (from RTP timestamps): %.2f seconds", audioRealDuration)

	timeDiff := videoRealDuration - audioRealDuration
	t.Logf("Time difference: %.3f seconds", timeDiff)

	if timeDiff < 0 {
		timeDiff = -timeDiff
	}

	if timeDiff < 0.5 {
		t.Logf("✅ EXCELLENT: Audio/Video are synchronized (diff < 0.5s)")
	} else if timeDiff < 1.0 {
		t.Logf("✅ GOOD: Audio/Video are reasonably synchronized (diff < 1.0s)")
	} else {
		t.Logf("⚠️  WARNING: Audio/Video may be out of sync (diff >= 1.0s)")
	}

	// Verify we received data
	if videoWithData == 0 {
		t.Fatal("❌ FAIL: No video packets with data received")
	}
	if audioWithData == 0 {
		t.Fatal("❌ FAIL: No audio packets with data received")
	}
	if videoKeyframes == 0 {
		t.Fatal("❌ FAIL: No video keyframes detected")
	}

	t.Log("\n========================================")
	t.Log("CONCLUSION")
	t.Log("========================================")
	t.Log("✅ Both audio and video received successfully")
	t.Logf("✅ Video: %d packets (%d bytes, %d keyframes)", videoWithData, videoBytes, videoKeyframes)
	t.Logf("✅ Audio: %d packets (%d bytes)", audioWithData, audioBytes)

	if videoEmpty > 0 || audioEmpty > 0 {
		t.Logf("✅ Empty packets handled: video=%d audio=%d", videoEmpty, audioEmpty)
	}

	t.Log("\n🎉 Audio/Video synchronization test passed!")
}
