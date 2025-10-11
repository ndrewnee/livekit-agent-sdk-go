package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

const (
	testLiveKitURL  = "ws://localhost:7880"
	testAPIKey      = "devkey"
	testAPISecret   = "secret"
	testRoomName    = "test-recording-room"
	testParticipant = "test-participant"
)

func TestParticipantHLSRecorder(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	// Set up environment
	os.Setenv("LIVEKIT_URL", testLiveKitURL)
	os.Setenv("LIVEKIT_API_KEY", testAPIKey)
	os.Setenv("LIVEKIT_API_SECRET", testAPISecret)
	os.Setenv("OUTPUT_DIR", "test-recordings")
	os.Setenv("INACTIVITY_TIMEOUT", "10s")

	// Clean up previous test recordings
	os.RemoveAll("test-recordings")

	t.Log("=== Step 1: Verify test video exists ===")
	testVideo := "../../examples/egress-agent/test-data/test.mp4"
	if _, err := os.Stat(testVideo); os.IsNotExist(err) {
		t.Fatalf("Test video not found: %s", testVideo)
	}
	t.Log("✓ Test video exists")

	t.Log("\n=== Step 2: Start recording agent FIRST ===")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create recorder manager
	config := loadConfig()
	recorderManager := NewRecorderManager(config)

	// Create handler
	handler := &ParticipantHLSHandler{
		recorderManager: recorderManager,
		config:          config,
	}

	// Create worker for JT_PUBLISHER jobs
	// This ensures the agent is dispatched AFTER tracks are published
	worker := agent.NewUniversalWorker(
		config.LiveKitURL,
		config.APIKey,
		config.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: "test-hls-recorder",
			JobType:   livekit.JobType_JT_PUBLISHER,
			MaxJobs:   1,
		},
	)

	// Start worker
	workerErr := make(chan error, 1)
	go func() {
		workerErr <- worker.Start(ctx)
	}()

	// Give worker time to start
	time.Sleep(2 * time.Second)
	t.Log("✓ Recording agent started")

	t.Log("\n=== Step 3: Create test room with agent dispatch ===")
	roomServiceClient := lksdk.NewRoomServiceClient(testLiveKitURL, testAPIKey, testAPISecret)

	// Delete room if it exists
	_, _ = roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: testRoomName,
	})

	// Configure agent dispatch for JT_PUBLISHER jobs
	// The LiveKit server will dispatch JT_PUBLISHER jobs when participant publishes tracks
	// AND dispatch JT_PARTICIPANT jobs when participant connects
	// Our worker only accepts JT_PUBLISHER jobs (see handler.go OnJobRequest)
	room, err := roomServiceClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: testRoomName,
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "test-hls-recorder",
				Metadata:  `{"record_audio":true,"record_video":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create room: %v", err)
	}
	t.Logf("✓ Room created: %s", room.Name)

	// Clean up room on exit
	defer func() {
		_, _ = roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
			Room: testRoomName,
		})
	}()

	t.Log("\n=== Step 4: Connect participant and publish tracks ===")

	// Connect participant
	participantRoom, err := lksdk.ConnectToRoom(testLiveKitURL, lksdk.ConnectInfo{
		APIKey:              testAPIKey,
		APISecret:           testAPISecret,
		RoomName:            testRoomName,
		ParticipantIdentity: testParticipant,
		ParticipantName:     "Test Participant",
	}, &lksdk.RoomCallback{})
	if err != nil {
		t.Fatalf("Failed to connect participant: %v", err)
	}
	defer participantRoom.Disconnect()
	t.Log("✓ Participant connected")

	// Create video track using LiveKit SDK (H.264)
	// IMPORTANT: Include SDPFmtpLine with H.264 profile parameters
	// profile-level-id=42e01f is Baseline Profile Level 3.1 (widely compatible)
	// packetization-mode=1 is required for proper H.264 fragmentation
	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	// Create audio track using LiveKit SDK
	audioTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		t.Fatalf("Failed to create audio track: %v", err)
	}

	// Create publisher BEFORE publishing tracks
	t.Log("  → Creating GStreamer publisher...")
	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}

	// Set up OnBind callbacks to start publisher when tracks are ready
	videoBound := make(chan struct{})
	audioBound := make(chan struct{})

	videoTrack.OnBind(func() {
		t.Log("  → Video track bound, ready to write samples")
		close(videoBound)
	})

	audioTrack.OnBind(func() {
		t.Log("  → Audio track bound, ready to write samples")
		close(audioBound)
	})

	// Now publish the tracks
	_, err = participantRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "test-video",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		t.Fatalf("Failed to publish video track: %v", err)
	}
	t.Log("✓ Video track published")

	_, err = participantRoom.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name: "test-audio",
	})
	if err != nil {
		t.Fatalf("Failed to publish audio track: %v", err)
	}
	t.Log("✓ Audio track published")

	// Wait for both tracks to be bound before starting publisher
	t.Log("  → Waiting for tracks to bind...")
	<-videoBound
	<-audioBound
	t.Log("  → Both tracks bound, starting publisher")

	t.Log("\n=== Step 5: Start GStreamer publisher ===")
	if err := publisher.Start(); err != nil {
		t.Fatalf("Failed to start publisher: %v", err)
	}
	t.Log("✓ Publisher started")

	// IMPORTANT: Give publisher time to send data
	// The robust implementation will skip any initial empty packets and process real data
	t.Log("  → Publisher is now sending data, agent should receive real packets...")
	time.Sleep(2 * time.Second)

	t.Log("\n=== Step 6: Wait for publishing to complete ===")
	if err := publisher.Wait(); err != nil {
		t.Fatalf("Publisher error: %v", err)
	}
	t.Log("✓ Publishing complete")

	t.Log("\n=== Step 7: Allow time for recording to finalize ===")
	time.Sleep(3 * time.Second)

	t.Log("\n=== Step 8: Stop publisher and disconnect ===")
	publisher.Stop()
	participantRoom.Disconnect()
	time.Sleep(2 * time.Second)
	t.Log("✓ Participant disconnected")

	t.Log("\n=== Step 9: Stop recording agent ===")
	cancel()
	worker.Stop()
	t.Log("  → Waiting for GStreamer pipeline to flush...")
	time.Sleep(10 * time.Second) // Give GStreamer time to flush all buffered data
	t.Log("✓ Recording agent stopped")

	t.Log("\n=== Step 10: Validate recording output ===")
	if err := validateRecording(t, testRoomName, testParticipant); err != nil {
		t.Fatalf("Recording validation failed: %v", err)
	}
	t.Log("✓ Recording is valid")

	t.Log("\n✅ All tests passed!")
}

func validateRecording(t *testing.T, roomName, participantIdentity string) error {
	// Check if output file exists
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

	// Validate with ffprobe
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", outputFile)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe failed: %w", err)
	}

	var duration float64
	if _, err := fmt.Sscanf(string(output), "%f", &duration); err != nil {
		t.Logf("  → Warning: could not parse duration: %v", err)
	}
	t.Logf("  → Recording duration: %.2f seconds", duration)

	if duration < 1.0 {
		return fmt.Errorf("recording too short: %.2f seconds", duration)
	}

	// Check for video and audio streams
	cmd = exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", outputFile)
	output, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe stream check failed: %w", err)
	}

	outputStr := string(output)
	hasVideo := false
	hasAudio := false
	for i := 0; i < len(outputStr); i++ {
		if outputStr[i] == 'v' {
			hasVideo = true
		}
		if outputStr[i] == 'a' {
			hasAudio = true
		}
	}

	t.Logf("  → Has video: %v, Has audio: %v", hasVideo, hasAudio)

	// Create HLS segments for playback verification
	t.Log("  → Creating HLS segments from output.ts...")
	playlistPath := fmt.Sprintf("test-recordings/%s/%s/playlist.m3u8", roomName, participantIdentity)
	segmentPattern := fmt.Sprintf("test-recordings/%s/%s/segment_%%05d.ts", roomName, participantIdentity)

	cmd = exec.Command("ffmpeg", "-i", outputFile,
		"-c", "copy",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_segment_filename", segmentPattern,
		"-y", playlistPath)

	if output, err := cmd.CombinedOutput(); err != nil {
		t.Logf("  → ffmpeg output: %s", string(output))
		return fmt.Errorf("failed to create HLS segments: %w", err)
	}

	// Verify playlist was created
	if _, err := os.Stat(playlistPath); os.IsNotExist(err) {
		return fmt.Errorf("playlist not created: %s", playlistPath)
	}
	t.Logf("  → HLS playlist created: %s", playlistPath)

	// Count segments
	segments, err := os.ReadDir(fmt.Sprintf("test-recordings/%s/%s", roomName, participantIdentity))
	if err != nil {
		return fmt.Errorf("failed to read output directory: %w", err)
	}

	segmentCount := 0
	for _, seg := range segments {
		if !seg.IsDir() && len(seg.Name()) > 11 && seg.Name()[:8] == "segment_" {
			segmentCount++
		}
	}
	t.Logf("  → Created %d HLS segments", segmentCount)

	return nil
}
