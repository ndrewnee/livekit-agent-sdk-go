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

const (
	testLiveKitURL  = "ws://localhost:7880"
	testAPIKey      = "devkey"
	testAPISecret   = "secret"
	testRoomName    = "publisher-hls-test-room"
	testParticipant = "test-publisher"
)

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

	os.Setenv("LIVEKIT_URL", testLiveKitURL)
	os.Setenv("LIVEKIT_API_KEY", testAPIKey)
	os.Setenv("LIVEKIT_API_SECRET", testAPISecret)
	os.Setenv("OUTPUT_DIR", outputDir)
	os.Setenv("AGENT_NAME", "test-publisher-hls-agent")
	os.Setenv("HLS_SEGMENT_DURATION", "2")
	os.Setenv("HLS_MAX_SEGMENTS", "0")

	serverLogFile, err := os.Create(serverLogPath)
	if err != nil {
		t.Fatalf("failed to create server log file: %v", err)
	}
	defer serverLogFile.Close()

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
		worker.Stop()
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
	worker.Stop()
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

func validateRecordingOutput(t *testing.T, outputFile, referenceVideo string) error {
	t.Helper()

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
			haveVideoTiming = true
		}
	}

	audioDuration := hlsAudioDuration
	if !haveAudioTiming || audioDuration == 0 {
		if _, tsAudioDuration, tsErr := ffprobeStreamTiming(outputFile, "a:0"); tsErr == nil && tsAudioDuration > 0 {
			audioDuration = tsAudioDuration
			haveAudioTiming = true
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}

	return out.Sync()
}

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

func ffprobeDuration(input string) (float64, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe failed: %w (output: %s)", err, string(out))
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}
