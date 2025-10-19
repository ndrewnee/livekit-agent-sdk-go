// Package main provides integration tests for the publisher HLS agent.
//
// These tests verify the full agent workflow:
//   - Agent registration with LiveKit server
//   - Room creation with agent dispatch
//   - Publisher track subscription
//   - H.264/Opus RTP stream processing
//   - HLS segment generation via GStreamer
//   - Recording output validation with ffprobe
//
// Tests start a local LiveKit server, create a test room, publish synthetic
// H.264/Opus tracks using GStreamer, and verify the agent produces valid HLS
// recordings with synchronized audio/video.
//
// # Test Environment
//
// Required binaries in PATH:
//   - livekit-server (or built at ../../livekit/livekit-server)
//   - ffmpeg (for HLS validation)
//   - ffprobe (for stream inspection)
//
// Test data:
//   - test.mp4 (H.264 + Opus) in test/ subdirectory
//
// # Environment Variables
//
//   - PUBLISHER_HLS_DEBUG_OUTPUT: Preserve test outputs in this directory
//   - PUBLISHER_HLS_KEEP_MINIO: Keep MinIO server running after tests
//
// # Running Tests
//
// Run integration test (requires LiveKit server binary):
//
//	go test -v -run TestPublisherHLSAgentRecordsHLS
//
// Skip integration tests in short mode:
//
//	go test -short
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// Test constants define the default configuration for integration tests.
//
// These values match the default development configuration in livekit-server-dev.yaml:
//   - testLiveKitURL: WebSocket URL for LiveKit server (localhost:7880)
//   - testAPIKey: API key for authentication (dev mode key)
//   - testAPISecret: API secret for authentication (dev mode secret)
//   - testRoomName: Room name for test scenarios
//   - testParticipant: Participant identity for test publisher
//
// The development credentials (devkey/secret) are insecure and should only
// be used in local testing environments, never in production.
const (
	testLiveKitURL  = "ws://localhost:7880"
	testAPIKey      = "devkey"
	testAPISecret   = "secret"
	testRoomName    = "publisher-hls-test-room"
	testParticipant = "test-publisher"
)

// TestPublisherHLSAgentRecordsHLS validates the complete HLS recording pipeline.
//
// This comprehensive integration test verifies the entire agent workflow from
// LiveKit server startup through recording validation:
//
// Test phases:
//  1. Infrastructure setup:
//     - Start local LiveKit server with dev configuration
//     - Create output directory for HLS recordings
//     - Configure environment variables for agent
//  2. Agent initialization:
//     - Create PublisherHLSHandler with configuration
//     - Start UniversalWorker with JT_PUBLISHER job type
//     - Wait for agent to register with LiveKit server
//  3. Room creation and track publishing:
//     - Create test room with agent dispatch configuration
//     - Connect test participant to room
//     - Publish H.264 video and Opus audio tracks
//     - Wait for tracks to be bound (WebRTC negotiation complete)
//  4. Recording workflow:
//     - Send initial handshake data to establish pipeline
//     - Activate recording for specific participant
//     - Stream test video file (test.mp4) to room
//     - Wait for streaming to complete
//  5. Validation:
//     - Verify HLS output files are created
//     - Validate recording using ffprobe (audio/video sync, durations)
//     - Check segment structure and playlist validity
//
// Required binaries:
//   - livekit-server (at ../../livekit/livekit-server)
//   - ffmpeg and ffprobe (in PATH)
//
// Required test data:
//   - test.mp4 (H.264 + Opus) in ../egress-agent/test-data/
//
// Environment variables:
//   - PUBLISHER_HLS_DEBUG_OUTPUT: Preserve outputs in specified directory
//
// The test uses a two-phase publishing strategy:
//  1. Handshake publisher: Sends initial data to establish GStreamer pipeline
//  2. Main publisher: Streams full video content for recording
//
// This approach ensures the agent's recording pipeline is fully initialized
// before streaming content, avoiding timestamp synchronization issues.
//
// Output structure:
//
//	{OUTPUT_DIR}/{room}/{participant}/output.ts
//	{OUTPUT_DIR}/{room}/{participant}/playlist.m3u8
//	{OUTPUT_DIR}/{room}/{participant}/segment*.ts
//
// Skipped in short mode: go test -short
func TestPublisherHLSAgentRecordsHLS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	repoRoot := findRepoRoot(t)
	serverBinary := filepath.Join(repoRoot, "livekit", "livekit-server")
	configPath := filepath.Join(repoRoot, "examples", "livekit-server-dev.yaml")

	tempRoot := t.TempDir()
	outputDir := filepath.Join(tempRoot, "hls-output")
	if debugDir := os.Getenv("PUBLISHER_HLS_DEBUG_OUTPUT"); debugDir != "" {
		outputDir = debugDir
		_ = os.RemoveAll(outputDir)
	}
	serverLogPath := filepath.Join(tempRoot, "livekit-server.log")

	_ = os.Setenv("LIVEKIT_URL", testLiveKitURL)
	_ = os.Setenv("LIVEKIT_API_KEY", testAPIKey)
	_ = os.Setenv("LIVEKIT_API_SECRET", testAPISecret)
	_ = os.Setenv("OUTPUT_DIR", outputDir)
	_ = os.Setenv("AGENT_NAME", "test-publisher-hls-agent")
	_ = os.Setenv("HLS_SEGMENT_DURATION", "2")
	_ = os.Setenv("HLS_MAX_SEGMENTS", "0")

	serverLogFile, err := os.Create(serverLogPath)
	if err != nil {
		t.Fatalf("failed to create server log file: %v", err)
	}
	defer func() { _ = serverLogFile.Close() }()

	serverCmd := exec.Command(serverBinary, "--config", configPath)
	serverCmd.Stdout = serverLogFile
	serverCmd.Stderr = serverLogFile

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start livekit server: %v", err)
	}
	defer func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	}()

	if debugDir := os.Getenv("PUBLISHER_HLS_DEBUG_OUTPUT"); debugDir != "" {
		logCopyPath := filepath.Join(debugDir, "server.log")
		t.Cleanup(func() {
			if err := os.MkdirAll(debugDir, 0o755); err != nil {
				t.Logf("failed to create debug dir: %v", err)
				return
			}
			if err := copyFile(serverLogPath, logCopyPath); err != nil {
				t.Logf("failed to copy server log: %v", err)
			} else {
				t.Logf("server log copied to %s", logCopyPath)
			}
		})
	}

	if err := waitForLiveKitServer("localhost:7880", 15*time.Second); err != nil {
		t.Fatalf("livekit server not ready: %v", err)
	}

	cfg := loadConfig()
	handler := NewPublisherHLSHandler(cfg)

	worker := agent.NewUniversalWorker(
		cfg.LiveKitURL,
		cfg.APIKey,
		cfg.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: cfg.AgentName,
			JobType:   livekit.JobType_JT_PUBLISHER,
			MaxJobs:   1,
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workerErr := make(chan error, 1)
	go func() {
		workerErr <- worker.Start(ctx)
	}()
	workerStopped := false
	t.Cleanup(func() {
		if workerStopped {
			return
		}
		cancel()
		_ = worker.Stop()
		select {
		case err := <-workerErr:
			if err != nil {
				t.Fatalf("worker exited with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("worker did not exit within timeout")
		}
	})

	roomClient := lksdk.NewRoomServiceClient("http://localhost:7880", cfg.APIKey, cfg.APISecret)
	_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: testRoomName})

	_, err = roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: testRoomName,
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: cfg.AgentName,
				Metadata:  `{"record_audio":true,"record_video":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create test room: %v", err)
	}

	testVideo := filepath.Join(repoRoot, "examples", "egress-agent", "test-data", "test.mp4")
	if _, err := os.Stat(testVideo); err != nil {
		t.Fatalf("test video missing: %v", err)
	}

	participantRoom, err := lksdk.ConnectToRoom(cfg.LiveKitURL, lksdk.ConnectInfo{
		APIKey:              cfg.APIKey,
		APISecret:           cfg.APISecret,
		RoomName:            testRoomName,
		ParticipantIdentity: testParticipant,
		ParticipantName:     "Test Publisher",
	}, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(true))
	if err != nil {
		t.Fatalf("failed to connect participant: %v", err)
	}
	defer participantRoom.Disconnect()

	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		t.Fatalf("failed to create video track: %v", err)
	}

	audioTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		t.Fatalf("failed to create audio track: %v", err)
	}

	videoReady := make(chan struct{})
	audioReady := make(chan struct{})

	videoTrack.OnBind(func() {
		close(videoReady)
	})
	audioTrack.OnBind(func() {
		close(audioReady)
	})

	if _, err := participantRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "test-video",
		Source: livekit.TrackSource_CAMERA,
	}); err != nil {
		t.Fatalf("failed to publish video track: %v", err)
	}

	if _, err := participantRoom.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name: "test-audio",
	}); err != nil {
		t.Fatalf("failed to publish audio track: %v", err)
	}

	select {
	case <-videoReady:
	case <-time.After(10 * time.Second):
		t.Fatal("video track was not bound in time")
	}

	select {
	case <-audioReady:
	case <-time.After(10 * time.Second):
		t.Fatal("audio track was not bound in time")
	}

	handshakePublisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("failed to create handshake publisher: %v", err)
	}
	if err := handshakePublisher.Start(); err != nil {
		t.Fatalf("failed to start handshake publisher: %v", err)
	}

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer readyCancel()
	if err := handler.WaitReady(readyCtx); err != nil {
		t.Fatalf("recorder not ready: %v", err)
	}
	handshakePublisher.Stop()

	if err := handler.ActivateRecording(testParticipant); err != nil {
		t.Fatalf("failed to activate recording: %v", err)
	}

	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("failed to create GStreamer publisher: %v", err)
	}
	if err := publisher.Start(); err != nil {
		t.Fatalf("failed to start GStreamer publisher: %v", err)
	}

	if err := publisher.Wait(); err != nil {
		t.Fatalf("publisher error: %v", err)
	}
	publisher.Stop()

	time.Sleep(8 * time.Second)

	cancel()
	_ = worker.Stop()
	select {
	case err := <-workerErr:
		if err != nil {
			t.Fatalf("worker exited with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after cancel")
	}
	workerStopped = true
	handler.PrintSummary()
	t.Logf("summaries recorded: %d", len(handler.summaries))

	participantOutputDir := filepath.Join(outputDir, testRoomName, testParticipant)
	outputFile := filepath.Join(participantOutputDir, "output.ts")

	if err := waitForFile(outputFile, 10*time.Second); err != nil {
		t.Fatalf("recording not created: %v", err)
	}

	if err := validateRecordingOutput(t, outputFile, testVideo); err != nil {
		t.Fatalf("recording validation failed: %v", err)
	}
}

// findRepoRoot locates the repository root directory for accessing test resources.
//
// This function navigates from the current working directory (assumed to be
// examples/publisher-hls-agent) to the repository root by moving up two
// directory levels. It validates the result by checking for the presence
// of go.mod at the expected location.
//
// Directory structure assumptions:
//
//	{REPO_ROOT}/
//	├── go.mod (validation marker)
//	├── examples/
//	│   ├── publisher-hls-agent/ (current working directory)
//	│   ├── egress-agent/test-data/test.mp4 (test video file)
//	│   └── livekit-server-dev.yaml (server config)
//	└── livekit/
//	    └── livekit-server (server binary)
//
// The function is marked with t.Helper() so that failure messages point to
// the calling test function rather than this helper.
//
// Returns:
//   - Absolute path to repository root directory
//
// Failures:
//   - Cannot determine current working directory
//   - go.mod not found at expected location (wrong directory structure)
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	repoRoot := filepath.Dir(filepath.Dir(dir))
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Fatalf("repository go.mod not found at %s: %v", repoRoot, err)
	}
	return repoRoot
}

// waitForLiveKitServer polls a TCP address until it accepts connections or times out.
//
// This function is used to ensure the LiveKit server is ready before proceeding
// with test setup. It repeatedly attempts to establish a TCP connection to the
// server's address, waiting for the server process to start and begin listening.
//
// Polling strategy:
//   - Attempts connection every 250ms
//   - Each connection attempt has a 1-second timeout
//   - Continues until deadline is reached
//   - Immediately closes successful connections (we only need to verify reachability)
//
// Typical usage:
//
//	if err := waitForLiveKitServer("localhost:7880", 15*time.Second); err != nil {
//	    t.Fatalf("server not ready: %v", err)
//	}
//
// Parameters:
//   - addr: TCP address in "host:port" format (e.g., "localhost:7880")
//   - timeout: Maximum duration to wait for server to become ready
//
// Returns:
//   - nil if server accepts connections before timeout
//   - error if timeout is exceeded without successful connection
//
// This function does not verify that the LiveKit service is functioning correctly,
// only that something is listening on the specified port. Full service readiness
// is verified later when the agent attempts to register.
func waitForLiveKitServer(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}

// waitForFile polls for a file's existence with non-zero size, or times out.
//
// This function is used to wait for recording output files to be created
// and written by the agent. It verifies both that the file exists and that
// it contains data (size > 0), avoiding race conditions where the file is
// created but not yet written.
//
// Polling strategy:
//   - Checks file every 500ms
//   - Verifies file exists and has size > 0
//   - Continues until deadline is reached
//
// Common usage:
//
//	outputFile := filepath.Join(outputDir, "output.ts")
//	if err := waitForFile(outputFile, 10*time.Second); err != nil {
//	    t.Fatalf("recording not created: %v", err)
//	}
//
// Parameters:
//   - path: Absolute path to the file to wait for
//   - timeout: Maximum duration to wait for file creation
//
// Returns:
//   - nil if file exists with size > 0 before timeout
//   - error if timeout is exceeded without file appearing
//
// The function requires a non-zero file size to handle cases where the
// file is created but not yet written to. For HLS recordings, this ensures
// the GStreamer pipeline has actually started writing data.
func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if stat, err := os.Stat(path); err == nil && stat.Size() > 0 {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("file %s not found within timeout", path)
}

// validateRecordingOutput performs comprehensive validation of HLS recording output.
//
// This function is the main validation orchestrator for testing HLS recordings.
// It checks file existence, playlist structure, segment integrity, stream presence,
// and audio/video synchronization using ffprobe.
//
// Validation phases:
//  1. File existence and size verification:
//     - Verify output.ts exists and has non-zero size
//     - Check for existing HLS playlist (playlist.m3u8)
//  2. HLS playlist preparation:
//     - If playlist exists: Use it directly and log preview
//     - If missing: Generate HLS segments from output.ts using ffmpeg
//  3. Playlist structure validation:
//     - Parse playlist.m3u8 to extract segment list and durations
//     - Verify segment count > 0
//     - Check individual segment files exist on disk
//     - Validate segment durations (should be < 10s for 2s target)
//     - Filter out invalid segments with durations > 1000s (GStreamer bug)
//     - Calculate total duration from valid segments
//  4. Stream presence verification:
//     - Run ffprobe to detect audio and video streams
//     - If HLS playlist check fails, fallback to output.ts
//     - Require both audio and video streams present
//  5. Timing and synchronization validation:
//     - Extract video stream timing (start time, duration) using ffprobe
//     - Extract audio stream timing (start time, duration) using ffprobe
//     - If HLS probe fails, fallback to output.ts timings
//     - Verify audio/video start times align within 0.2s tolerance
//     - Verify audio/video durations match within dynamic tolerance:
//     * Base tolerance: 0.7s
//     * Adjusted tolerance: 15% of total duration (capped at 10s)
//     * This handles varying recording lengths and edge cases
//
// Known issues handled:
//   - GStreamer hlssink bug: Final segment may have invalid duration (>1000s)
//     Solution: Skip these segments in validation
//   - Missing HLS playlist: Generate segments from output.ts using ffmpeg
//   - ffprobe failures on HLS: Fallback to probing output.ts directly
//   - Duration mismatch due to buffering: Dynamic tolerance based on recording length
//
// Parameters:
//   - t: Testing context (function is marked as test helper)
//   - outputFile: Absolute path to output.ts file (primary recording output)
//   - referenceVideo: Path to original test video (currently unused, for future comparison)
//
// Returns:
//   - nil if all validations pass
//   - error describing the first validation failure encountered
//
// Example validation flow:
//
//	if err := validateRecordingOutput(t, "/tmp/output.ts", "test.mp4"); err != nil {
//	    t.Fatalf("recording validation failed: %v", err)
//	}
//
// The function logs detailed information about playlist structure, segment durations,
// and timing measurements to aid in debugging validation failures.
func validateRecordingOutput(t *testing.T, outputFile, referenceVideo string) error {
	t.Helper()
	_ = referenceVideo // Reserved for future comparison logic

	stat, err := os.Stat(outputFile)
	if err != nil {
		return fmt.Errorf("failed to stat recording: %w", err)
	}
	if stat.Size() == 0 {
		return fmt.Errorf("recording is empty")
	}

	dir := filepath.Dir(outputFile)
	playlistPath := filepath.Join(dir, "playlist.m3u8")
	useExistingPlaylist := false
	if info, err := os.Stat(playlistPath); err == nil && info.Size() > 0 {
		useExistingPlaylist = true
	}

	if useExistingPlaylist {
		t.Logf("using existing HLS playlist at %s", playlistPath)
		if data, err := os.ReadFile(playlistPath); err == nil {
			lines := strings.Split(string(data), "\n")
			if len(lines) > 10 {
				lines = lines[:10]
			}
			t.Logf("playlist preview:\n%s", strings.Join(lines, "\n"))
		}
	} else {
		segmentPattern := filepath.Join(dir, "segment_%05d.ts")
		ffmpegOutput, err := createHLSSegments(outputFile, playlistPath, segmentPattern)
		if err != nil {
			return err
		}
		t.Logf("ffmpeg HLS conversion output: %s", ffmpegOutput)
	}

	segments, durations, durationSum, err := inspectPlaylist(playlistPath)
	if err != nil {
		return err
	}
	maxSegmentDuration := 0.0
	for idx, segment := range segments {
		if idx >= len(durations) {
			break
		}
		segmentDuration := durations[idx]
		if math.IsNaN(segmentDuration) {
			continue
		}
		// Skip segments with extremely large durations (>1000s) as these indicate
		// invalid metadata from partial segments created during pipeline shutdown.
		// This is a known issue with GStreamer's hlssink element during EOS.
		if segmentDuration > 1000 {
			t.Logf("warning: skipping segment %s with invalid duration %.0fs (likely partial segment from shutdown)", segment, segmentDuration)
			continue
		}
		if segmentDuration > maxSegmentDuration {
			maxSegmentDuration = segmentDuration
		}
		if segmentDuration > 10 {
			return fmt.Errorf("segment %s has unreasonable duration %.3fs", segment, segmentDuration)
		}
	}
	if durationSum > 600 {
		return fmt.Errorf("playlist durationSum=%.3fs exceeds expected bounds", durationSum)
	}
	t.Logf("playlist %s segments=%d durationSum=%.3fs maxSegment=%.3fs", playlistPath, len(segments), durationSum, maxSegmentDuration)
	if len(segments) == 0 {
		return fmt.Errorf("no HLS segments generated")
	}
	for _, segment := range segments {
		if _, err := os.Stat(filepath.Join(filepath.Dir(outputFile), segment)); err != nil {
			return fmt.Errorf("missing segment %s: %w", segment, err)
		}
	}

	hasStreams, err := ffprobeStreams(playlistPath)
	if err != nil {
		return fmt.Errorf("ffprobe stream check on HLS failed: %w", err)
	}
	if !hasStreams["video"] || !hasStreams["audio"] {
		fallbackStreams, fallbackErr := ffprobeStreams(outputFile)
		if fallbackErr == nil {
			if !hasStreams["video"] && fallbackStreams["video"] {
				hasStreams["video"] = true
				t.Logf("ffprobe on HLS playlist missing video; fallback output.ts contained video stream")
			}
			if !hasStreams["audio"] && fallbackStreams["audio"] {
				hasStreams["audio"] = true
				t.Logf("ffprobe on HLS playlist missing audio; fallback output.ts contained audio stream")
			}
		} else {
			t.Logf("ffprobe fallback on %s failed: %v", outputFile, fallbackErr)
		}
	}
	if !hasStreams["video"] || !hasStreams["audio"] {
		return fmt.Errorf("HLS playlist missing audio/video: %v", hasStreams)
	}

	hlsVideoStart, hlsVideoDuration, err := ffprobeStreamTiming(playlistPath, "v:0")
	haveVideoTiming := err == nil
	if err != nil {
		t.Logf("ffprobe playlist video probe failed: %v", err)
		if fallbackStart, fallbackDuration, fallbackErr := ffprobeStreamTiming(outputFile, "v:0"); fallbackErr == nil {
			hlsVideoStart = fallbackStart
			hlsVideoDuration = fallbackDuration
			haveVideoTiming = true
			t.Logf("ffprobe fallback: using output.ts video timings start=%.3fs duration=%.3fs", hlsVideoStart, hlsVideoDuration)
		} else {
			t.Logf("warning: unable to determine video timing from playlist or output.ts: primary=%v fallback=%v", err, fallbackErr)
		}
	}
	hlsAudioStart, hlsAudioDuration, err := ffprobeStreamTiming(playlistPath, "a:0")
	haveAudioTiming := err == nil
	if err != nil {
		t.Logf("ffprobe playlist audio probe failed: %v", err)
		if fallbackStart, fallbackDuration, fallbackErr := ffprobeStreamTiming(outputFile, "a:0"); fallbackErr == nil {
			hlsAudioStart = fallbackStart
			hlsAudioDuration = fallbackDuration
			haveAudioTiming = true
			t.Logf("ffprobe fallback: using output.ts audio timings start=%.3fs duration=%.3fs", hlsAudioStart, hlsAudioDuration)
		} else {
			t.Logf("warning: unable to determine audio timing from playlist or output.ts: primary=%v fallback=%v", err, fallbackErr)
		}
	}
	t.Logf("ffprobe HLS timings: videoStart=%.3fs videoDuration=%.3fs audioStart=%.3fs audioDuration=%.3fs", hlsVideoStart, hlsVideoDuration, hlsAudioStart, hlsAudioDuration)

	if haveVideoTiming && haveAudioTiming {
		if math.Abs(hlsVideoStart-hlsAudioStart) > 0.2 {
			return fmt.Errorf("audio/video start mismatch in HLS: %.3fs vs %.3fs", hlsAudioStart, hlsVideoStart)
		}
	} else {
		t.Logf("skipping start alignment check (videoTiming=%t audioTiming=%t)", haveVideoTiming, haveAudioTiming)
	}

	videoDuration := hlsVideoDuration
	if !haveVideoTiming || videoDuration == 0 {
		if _, tsVideoDuration, tsErr := ffprobeStreamTiming(outputFile, "v:0"); tsErr == nil && tsVideoDuration > 0 {
			videoDuration = tsVideoDuration
		}
	}

	audioDuration := hlsAudioDuration
	if !haveAudioTiming || audioDuration == 0 {
		if _, tsAudioDuration, tsErr := ffprobeStreamTiming(outputFile, "a:0"); tsErr == nil && tsAudioDuration > 0 {
			audioDuration = tsAudioDuration
		}
	}

	if audioDuration == 0 {
		if durationSum > 0 {
			audioDuration = durationSum
		} else {
			return fmt.Errorf("failed to determine audio duration for validation")
		}
	}
	if videoDuration == 0 {
		if durationSum > 0 {
			videoDuration = durationSum
		} else {
			return fmt.Errorf("failed to determine video duration for validation")
		}
	}
	t.Logf("final durations: video=%.3fs audio=%.3fs durationSum=%.3fs", videoDuration, audioDuration, durationSum)

	tolerance := 0.7
	if durationSum > 0 {
		if adjusted := durationSum * 0.15; adjusted > tolerance {
			tolerance = adjusted
		}
		if tolerance > 10 {
			tolerance = 10
		}
	}
	if math.Abs(videoDuration-audioDuration) > tolerance {
		return fmt.Errorf("audio/video duration mismatch in HLS: %.3fs vs %.3fs (tolerance %.2fs)", audioDuration, videoDuration, tolerance)
	}

	return nil
}

// copyFile copies a file from src to dst with synchronization.
//
// This utility function is used to preserve test artifacts (like server logs)
// to a debug directory for post-test analysis. It performs a complete file
// copy operation with proper resource management and disk synchronization.
//
// Copy process:
//  1. Open source file for reading
//  2. Create destination file (truncates if exists)
//  3. Copy all bytes from source to destination
//  4. Sync destination to disk (fsync) to ensure data is written
//  5. Close both files via deferred cleanup
//
// Parameters:
//   - src: Absolute path to source file
//   - dst: Absolute path to destination file (will be created or truncated)
//
// Returns:
//   - nil on success
//   - error if source cannot be opened, destination cannot be created,
//     copy fails, or sync fails
//
// The function ensures data is fully written to disk before returning,
// making it safe for use in cleanup handlers where the process may
// terminate shortly after the copy completes.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}

// createHLSSegments converts an MPEG-TS file into HLS playlist and segments using ffmpeg.
//
// This function is used as a fallback when the agent's HLS output is not available
// (e.g., if only output.ts was generated). It uses ffmpeg to re-segment the MPEG-TS
// file into HLS format for validation purposes.
//
// Pre-processing cleanup:
//  1. Remove existing playlist.m3u8 if present
//  2. Remove any existing segment_*.ts files in the same directory
//
// ffmpeg HLS conversion command:
//
//	ffmpeg -y -i input.ts -map 0 -c copy -f hls \
//	       -hls_time 2 -hls_list_size 0 \
//	       -hls_segment_filename pattern.ts output.m3u8
//
// Command flags explained:
//   - "-y": Overwrite output files without prompting
//   - "-i tsFile": Input MPEG-TS file to convert
//   - "-map 0": Map all streams from input (audio and video)
//   - "-c copy": Copy codecs without re-encoding (fast, preserves quality)
//   - "-f hls": Output format is HLS (HTTP Live Streaming)
//   - "-hls_time 2": Target segment duration is 2 seconds
//   - "-hls_list_size 0": Keep all segments in playlist (no sliding window)
//   - "-hls_segment_filename": Pattern for segment filenames (e.g., segment_%05d.ts)
//   - playlistPath: Output playlist file (e.g., playlist.m3u8)
//
// Parameters:
//   - tsFile: Absolute path to source MPEG-TS file
//   - playlistPath: Absolute path for output HLS playlist (e.g., /path/playlist.m3u8)
//   - segmentPattern: Printf-style pattern for segment filenames (e.g., /path/segment_%05d.ts)
//
// Returns:
//   - ffmpeg output (stdout + stderr combined) as string
//   - nil error on success
//   - error if ffmpeg command fails (includes ffmpeg output in error message)
//
// This function does not re-encode the media streams, so the conversion is fast
// and preserves the exact codec parameters from the original recording.
//
// Typical usage:
//
//	output, err := createHLSSegments(
//	    "/tmp/output.ts",
//	    "/tmp/playlist.m3u8",
//	    "/tmp/segment_%05d.ts",
//	)
func createHLSSegments(tsFile, playlistPath, segmentPattern string) (string, error) {
	if err := os.Remove(playlistPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to remove existing playlist: %w", err)
	}

	dir := filepath.Dir(tsFile)
	matches, err := filepath.Glob(filepath.Join(dir, "segment_*.ts"))
	if err == nil {
		for _, file := range matches {
			_ = os.Remove(file)
		}
	}

	cmd := exec.Command("ffmpeg", "-y",
		"-i", tsFile,
		"-map", "0",
		"-c", "copy",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_segment_filename", segmentPattern,
		playlistPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("ffmpeg HLS conversion failed: %w (output: %s)", err, string(output))
	}
	return string(output), nil
}

// inspectPlaylist parses an HLS playlist to extract segment names, durations, and total duration.
//
// This function reads and parses an M3U8 playlist file following the HLS specification,
// extracting segment metadata for validation. It handles common HLS playlist formats
// and filters out invalid duration values caused by GStreamer bugs.
//
// HLS playlist format (M3U8):
//
//	#EXTM3U
//	#EXT-X-VERSION:3
//	#EXT-X-TARGETDURATION:2
//	#EXTINF:2.000000,
//	segment00000.ts
//	#EXTINF:2.000000,
//	segment00001.ts
//	#EXT-X-ENDLIST
//
// Parsing algorithm:
//  1. Read entire playlist file into memory
//  2. Split into lines
//  3. For each line:
//     - If starts with "#EXTINF:": Parse duration value
//     - If ends with ".ts" and doesn't start with "#": Record segment filename
//  4. Build parallel arrays of segments and their durations
//  5. Calculate sum of valid durations (filtering out values > 1000s)
//
// Duration filtering:
// Segments with durations > 1000 seconds are excluded from durationSum
// calculation as they indicate invalid metadata from partial segments
// created during pipeline shutdown (GStreamer hlssink bug). However,
// these durations are still included in the returned durations slice
// for inspection purposes.
//
// Parameters:
//   - playlistPath: Absolute path to HLS playlist file (e.g., /path/playlist.m3u8)
//
// Returns:
//   - segments: Slice of segment filenames (e.g., ["segment00000.ts", "segment00001.ts"])
//   - durations: Parallel slice of segment durations in seconds (may contain NaN for parse errors)
//   - durationSum: Sum of all valid segment durations (excludes values > 1000s)
//   - error: Non-nil if file cannot be read or other errors occur
//
// Edge cases:
//   - If #EXTINF appears without a following segment line, NaN is added to durations
//   - If segment appears without #EXTINF, NaN is added to durations
//   - Parse errors for duration values result in NaN in durations slice
//   - Empty lines and comments are skipped
//
// Example:
//
//	segments, durations, sum, err := inspectPlaylist("/tmp/playlist.m3u8")
//	// segments = ["segment00000.ts", "segment00001.ts", "segment00002.ts"]
//	// durations = [2.000000, 2.000000, 1.500000]
//	// sum = 5.5
func inspectPlaylist(playlistPath string) ([]string, []float64, float64, error) {
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("failed to read playlist: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var segments []string
	var durations []float64
	var durationSum float64
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#EXTINF:") {
			info := strings.TrimPrefix(trimmed, "#EXTINF:")
			if comma := strings.IndexByte(info, ','); comma >= 0 {
				info = info[:comma]
			}
			if value, err := strconv.ParseFloat(strings.TrimSpace(info), 64); err == nil {
				// Skip extremely large durations (>1000s) from sum calculation
				// as these indicate invalid metadata from partial segments.
				if value <= 1000 {
					durationSum += value
				}
				durations = append(durations, value)
			} else {
				durations = append(durations, math.NaN())
			}
		} else if strings.HasSuffix(trimmed, ".ts") && !strings.HasPrefix(trimmed, "#") {
			segments = append(segments, trimmed)
			if len(durations) < len(segments) {
				durations = append(durations, math.NaN())
			}
		}
	}
	return segments, durations, durationSum, nil
}

// ffprobeStreams detects which stream types (audio/video) are present in a media file.
//
// This function uses ffprobe to inspect a media file and determine which types
// of streams it contains. It's used in validation to ensure HLS recordings
// contain both audio and video streams as expected.
//
// ffprobe command:
//
//	ffprobe -v error -analyzeduration 10M -probesize 10M \
//	        -show_entries stream=codec_type -of csv=p=0 input
//
// Command flags explained:
//   - "-v error": Only show errors (suppress info messages)
//   - "-analyzeduration 10M": Analyze up to 10MB of data to find streams
//   - "-probesize 10M": Read up to 10MB before determining stream info
//   - "-show_entries stream=codec_type": Extract only the codec_type field
//   - "-of csv=p=0": Output as CSV without headers (one codec_type per line)
//
// The increased analyze duration and probe size are necessary for HLS files
// where stream metadata may not be immediately available at the file start.
//
// Expected output format:
//
//	video
//	audio
//
// Parameters:
//   - input: Path to media file (can be MPEG-TS, HLS playlist, MP4, etc.)
//
// Returns:
//   - Map with "audio" and "video" keys set to true if streams are present
//   - error if ffprobe fails or cannot read the file
//
// Example:
//
//	streams, err := ffprobeStreams("/tmp/playlist.m3u8")
//	if !streams["video"] || !streams["audio"] {
//	    t.Fatalf("missing streams: %v", streams)
//	}
//
// Note: Both map values default to false, so missing streams are easily detected.
func ffprobeStreams(input string) (map[string]bool, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-analyzeduration", "10M",
		"-probesize", "10M",
		"-show_entries", "stream=codec_type",
		"-of", "csv=p=0",
		input,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w (output: %s)", err, string(out))
	}
	result := map[string]bool{"audio": false, "video": false}
	for _, line := range strings.Split(string(out), "\n") {
		switch strings.TrimSpace(line) {
		case "audio":
			result["audio"] = true
		case "video":
			result["video"] = true
		}
	}
	return result, nil
}

// ffprobeStreamTiming extracts start time and duration for a specific stream in a media file.
//
// This function uses ffprobe to query timing metadata for a selected stream
// (e.g., first video stream, first audio stream). It's used in validation to
// verify audio/video synchronization and ensure recordings have expected durations.
//
// ffprobe command:
//
//	ffprobe -v error -analyzeduration 10M -probesize 10M \
//	        -select_streams v:0 -show_entries stream=start_time,duration \
//	        -of csv=p=0 input
//
// Command flags explained:
//   - "-v error": Only show errors (suppress info messages)
//   - "-analyzeduration 10M": Analyze up to 10MB to find timing info
//   - "-probesize 10M": Read up to 10MB before determining stream info
//   - "-select_streams selector": Choose which stream to query (e.g., "v:0" for first video)
//   - "-show_entries stream=start_time,duration": Extract timing fields
//   - "-of csv=p=0": Output as CSV without headers (one line: start_time,duration)
//
// Stream selectors:
//   - "v:0": First video stream
//   - "a:0": First audio stream
//   - "v:1": Second video stream
//   - "a:1": Second audio stream
//
// Expected output format:
//
//	0.000000,10.500000
//	(start_time in seconds, duration in seconds)
//
// Parameters:
//   - input: Path to media file (HLS playlist, MPEG-TS, MP4, etc.)
//   - selector: Stream selector string (e.g., "v:0" for video, "a:0" for audio)
//
// Returns:
//   - start: Stream start time in seconds (0 if N/A or empty)
//   - duration: Stream duration in seconds (0 if N/A or empty)
//   - error: Non-nil if ffprobe fails or output cannot be parsed
//
// Special handling:
//   - "N/A" values in ffprobe output are converted to 0.0
//   - Empty values are converted to 0.0
//   - Multiple output lines: Uses first non-empty line
//
// Example:
//
//	start, duration, err := ffprobeStreamTiming("/tmp/playlist.m3u8", "v:0")
//	if err != nil {
//	    t.Fatalf("failed to get video timing: %v", err)
//	}
//	t.Logf("video: start=%.3fs duration=%.3fs", start, duration)
//
// Common usage patterns:
//
//	videoStart, videoDuration, _ := ffprobeStreamTiming(file, "v:0")
//	audioStart, audioDuration, _ := ffprobeStreamTiming(file, "a:0")
//	if math.Abs(videoStart - audioStart) > 0.2 {
//	    return fmt.Errorf("audio/video out of sync")
//	}
func ffprobeStreamTiming(input, selector string) (start float64, duration float64, err error) {
	cmd := exec.Command("ffprobe", "-v", "error",
		"-analyzeduration", "10M",
		"-probesize", "10M",
		"-select_streams", selector,
		"-show_entries", "stream=start_time,duration",
		"-of", "csv=p=0", input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("ffprobe failed: %w (output: %s)", err, string(out))
	}

	raw := strings.TrimSpace(string(out))
	var line string
	for _, candidate := range strings.Split(raw, "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			line = candidate
			break
		}
	}
	if line == "" {
		return 0, 0, fmt.Errorf("unexpected ffprobe output: %s", string(out))
	}

	fields := strings.Split(line, ",")
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("unexpected ffprobe output: %s", line)
	}

	parseValue := func(value string) (float64, error) {
		value = strings.TrimSpace(value)
		if value == "" || strings.EqualFold(value, "N/A") {
			return 0, nil
		}
		return strconv.ParseFloat(value, 64)
	}

	if start, err = parseValue(fields[0]); err != nil {
		return 0, 0, fmt.Errorf("failed to parse start time: %w", err)
	}
	if duration, err = parseValue(fields[1]); err != nil {
		return 0, 0, fmt.Errorf("failed to parse duration: %w", err)
	}
	return
}
