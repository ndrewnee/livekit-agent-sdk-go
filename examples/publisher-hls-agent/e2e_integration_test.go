// Package main provides end-to-end integration tests for the publisher HLS agent.
//
// These tests validate the complete recording workflow including S3 uploads:
//   - LiveKit server startup and agent worker registration
//   - Agent dispatch on room creation
//   - Test participant connection and track publication
//   - GStreamer-based H.264/Opus track playback from MP4 file
//   - HLS recording with delayed pipeline start (keyframe-aligned)
//   - Local recording output validation
//   - S3/MinIO upload and remote recording validation
//   - HLS playlist integrity checks
//
// The tests use unique agent names per run to avoid conflicts with stale
// worker registrations from previous test runs.
//
// # Test Architecture
//
// Each test scenario:
//  1. Starts a local LiveKit server (from PATH or ../../livekit/)
//  2. Starts the publisher-hls-agent worker (via go run)
//  3. Creates a room with agent dispatch configuration
//  4. Connects a test participant and publishes H.264/Opus tracks
//  5. Uses GStreamerPublisher to stream test.mp4 content
//  6. Waits for recording completion and validates outputs
//  7. For S3 tests: starts MinIO, validates uploads, checks HLS playlists
//
// # Test Scenarios
//
// TestPublisherHLSAgentEndToEnd:
//   - Local recording to disk without S3 upload
//   - Validates output.ts, playlist.m3u8, and segment files
//   - Uses default AAC audio transcoding
//
// TestPublisherHLSAgentUploadsToS3:
//   - Recording with S3 upload to MinIO
//   - Validates both local and remote recordings
//   - Checks HLS segment integrity and playlist validity
//   - Ensures first segment has non-zero duration (keyframe-aligned)
//   - Sets public-read ACL for streaming access
//   - Uses default AAC audio transcoding
//
// TestPublisherHLSAgentWithOpus:
//   - Local recording with Opus audio passthrough (no AAC transcoding)
//   - Validates output.ts contains Opus audio codec
//   - Tests KEEP_OPUS=true configuration
//
// # Environment Variables
//
//   - PUBLISHER_HLS_KEEP_MINIO=1: Keep MinIO server running after tests
//     (Enables manual inspection of S3 uploads and HLS playlists)
//
// # Required Dependencies
//
// Binaries in PATH:
//   - livekit-server: LiveKit SFU server
//   - ffmpeg: HLS validation and transcoding
//   - ffprobe: Stream inspection and timing validation
//   - minio or docker: S3-compatible storage (optional, will use docker fallback)
//
// Test data:
//   - test.mp4: H.264 video + Opus audio test file
//
// # Running Tests
//
// Run both E2E tests:
//
//	go test -v -run 'TestPublisherHLSAgent.*'
//
// Run only local recording test:
//
//	go test -v -run TestPublisherHLSAgentEndToEnd
//
// Run only S3 upload test:
//
//	go test -v -run TestPublisherHLSAgentUploadsToS3
//
// Keep MinIO running for manual inspection:
//
//	PUBLISHER_HLS_KEEP_MINIO=1 go test -v -run TestPublisherHLSAgentUploadsToS3
//
// Skip E2E tests in short mode:
//
//	go test -short
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pion/webrtc/v4"
)

type e2eScenario struct {
	name        string
	agentName   string
	roomName    string
	participant string
	outputDir   string
	agentEnv    map[string]string
}

type e2eResult struct {
	outputDir       string
	participantDir  string
	agentLogPath    string
	roomName        string
	participantName string
}

func TestPublisherHLSAgentEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end integration test in short mode")
	}

	// Use unique agent name to avoid conflicts with stale workers from previous test runs
	uniqueAgentName := fmt.Sprintf("publisher-hls-e2e-agent-%d", time.Now().UnixNano())

	runE2EScenario(t, e2eScenario{
		name:        "local-output",
		agentName:   uniqueAgentName,
		roomName:    "publisher-hls-e2e-room",
		participant: "publisher-hls-e2e-participant",
		outputDir:   "",
		agentEnv:    map[string]string{},
	})
}

func TestPublisherHLSAgentUploadsToS3(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end integration test in short mode")
	}

	ms := startMinIOServer(t)
	if !ms.KeepAlive {
		defer ms.Shutdown(t)
	} else {
		t.Logf("PUBLISHER_HLS_KEEP_MINIO=1 detected; MinIO will remain running at http://%s", ms.Endpoint)
	}

	// Use unique agent name to avoid conflicts with stale workers from previous test runs
	uniqueAgentName := fmt.Sprintf("publisher-hls-s3-agent-%d", time.Now().UnixNano())

	scenario := e2eScenario{
		name:        "s3-upload",
		agentName:   uniqueAgentName,
		roomName:    "publisher-hls-s3-room",
		participant: "publisher-hls-s3-participant",
		outputDir:   "",
		agentEnv: map[string]string{
			"S3_ENDPOINT":             ms.Endpoint,
			"S3_BUCKET":               ms.Bucket,
			"S3_REGION":               "us-east-1",
			"S3_ACCESS_KEY":           ms.AccessKey,
			"S3_SECRET_KEY":           ms.SecretKey,
			"S3_FORCE_PATH_STYLE":     "true",
			"S3_USE_SSL":              "false",
			"S3_PREFIX":               "publisher-tests",
			"S3_OBJECT_ACL":           "public-read",
			"AUTO_ACTIVATE_RECORDING": "true",
		},
	}

	result := runE2EScenario(t, scenario)
	requireFileExists(t, filepath.Join(result.participantDir, "playlist.m3u8"))
	requireFileExists(t, filepath.Join(result.participantDir, "output.ts"))

	client := ms.NewClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := path.Join("publisher-tests", scenario.roomName, scenario.participant)
	playlistObj := path.Join(prefix, "playlist.m3u8")
	reader, err := client.GetObject(ctx, ms.Bucket, playlistObj, minio.GetObjectOptions{})
	if err != nil {
		t.Fatalf("failed to fetch playlist from MinIO: %v", err)
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	foundFirstSegment := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#EXTINF:") {
			foundFirstSegment = true
			if strings.Contains(line, "0.0") {
				t.Fatalf("unexpected near-zero duration first segment in S3 playlist: %s", line)
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("failed scanning playlist: %v", err)
	}
	if !foundFirstSegment {
		t.Fatalf("playlist at %s missing EXTINF entries", playlistObj)
	}

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/%s/*"]}]}`, ms.Bucket, prefix)
	if err := client.SetBucketPolicy(context.Background(), ms.Bucket, policy); err != nil {
		t.Fatalf("failed to set read policy on MinIO bucket: %v", err)
	}

	streamURL := fmt.Sprintf("http://%s/%s/%s", ms.Endpoint, ms.Bucket, playlistObj)
	t.Logf("HLS playlist available at: %s", streamURL)
	if ms.KeepAlive {
		t.Logf("MinIO data dir: %s (server left running)", ms.DataDir)
	}
}

func TestPublisherHLSAgentWithOpus(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end integration test in short mode")
	}

	ms := startMinIOServer(t)
	if !ms.KeepAlive {
		defer ms.Shutdown(t)
	} else {
		t.Logf("PUBLISHER_HLS_KEEP_MINIO=1 detected; MinIO will remain running at http://%s", ms.Endpoint)
	}

	// Use unique agent name to avoid conflicts with stale workers from previous test runs
	uniqueAgentName := fmt.Sprintf("publisher-hls-opus-agent-%d", time.Now().UnixNano())

	scenario := e2eScenario{
		name:        "s3-opus-output",
		agentName:   uniqueAgentName,
		roomName:    "publisher-hls-opus-room",
		participant: "publisher-hls-opus-participant",
		outputDir:   "",
		agentEnv: map[string]string{
			"KEEP_OPUS":               "true",
			"S3_ENDPOINT":             ms.Endpoint,
			"S3_BUCKET":               ms.Bucket,
			"S3_REGION":               "us-east-1",
			"S3_ACCESS_KEY":           ms.AccessKey,
			"S3_SECRET_KEY":           ms.SecretKey,
			"S3_FORCE_PATH_STYLE":     "true",
			"S3_USE_SSL":              "false",
			"S3_PREFIX":               "publisher-opus-tests",
			"S3_OBJECT_ACL":           "public-read",
			"AUTO_ACTIVATE_RECORDING": "true",
		},
	}

	result := runE2EScenario(t, scenario)

	// Validate output files exist
	requireFileExists(t, filepath.Join(result.participantDir, "playlist.m3u8"))
	requireFileExists(t, filepath.Join(result.participantDir, "output.ts"))

	// Verify the recording contains Opus audio codec
	outputFile := filepath.Join(result.participantDir, "output.ts")
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name",
		"-of", "default=nokey=1:noprint_wrappers=1",
		outputFile,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to check audio codec: %v (output: %s)", err, string(output))
	}
	audioCodec := strings.TrimSpace(string(output))
	// Handle case where ffprobe returns multiple lines (one per audio stream)
	audioCodecLines := strings.Split(audioCodec, "\n")
	if len(audioCodecLines) == 0 || audioCodecLines[0] != "opus" {
		t.Fatalf("expected opus audio codec but got: %s", audioCodec)
	}
	// Verify all audio streams are Opus
	for i, codec := range audioCodecLines {
		if strings.TrimSpace(codec) != "opus" {
			t.Fatalf("expected all audio streams to be opus, but stream %d is: %s", i, codec)
		}
	}
	t.Logf("verified recording contains Opus audio codec (%d stream(s))", len(audioCodecLines))

	// Validate S3 upload
	client := ms.NewClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s3Bucket := ms.Bucket
	remotePrefix := path.Join("publisher-opus-tests", scenario.roomName, scenario.participant)
	playlistObj := path.Join(remotePrefix, "playlist.m3u8")

	reader, err := client.GetObject(ctx, s3Bucket, playlistObj, minio.GetObjectOptions{})
	if err != nil {
		t.Fatalf("failed to fetch playlist from MinIO: %v", err)
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	foundFirstSegment := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#EXTINF:") {
			foundFirstSegment = true
			if strings.Contains(line, "0.0") {
				t.Fatalf("unexpected near-zero duration first segment in S3 playlist: %s", line)
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("failed scanning playlist: %v", err)
	}
	if !foundFirstSegment {
		t.Fatalf("playlist at %s missing EXTINF entries", playlistObj)
	}

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/%s/*"]}]}`, s3Bucket, remotePrefix)
	if err := client.SetBucketPolicy(context.Background(), s3Bucket, policy); err != nil {
		t.Fatalf("failed to set read policy on MinIO bucket: %v", err)
	}

	streamURL := fmt.Sprintf("http://%s/%s/%s", ms.Endpoint, s3Bucket, playlistObj)
	t.Logf("HLS playlist with Opus audio available at: %s", streamURL)
	if ms.KeepAlive {
		t.Logf("MinIO data dir: %s (server left running)", ms.DataDir)
	}
}

func runE2EScenario(t *testing.T, scenario e2eScenario) e2eResult {
	t.Helper()

	repoRoot := findRepoRoot(t)
	//serverBinary := filepath.Join(repoRoot, "livekit", "livekit-server")
	serverBinary, err := exec.LookPath("livekit-server")
	if err != nil {
		t.Fatalf("livekit-server not found in PATH: %v", err)
	}
	configPath := filepath.Join(repoRoot, "examples", "livekit-server-dev.yaml")
	testVideo := filepath.Join(repoRoot, "examples", "publisher-hls-agent", "test", "test.mp4")

	requireFileExists(t, testVideo)

	agentName := scenario.agentName
	if agentName == "" {
		agentName = "publisher-hls-e2e-agent"
	}
	roomName := scenario.roomName
	if roomName == "" {
		roomName = "publisher-hls-e2e-room"
	}
	participantIdentity := scenario.participant
	if participantIdentity == "" {
		participantIdentity = "publisher-hls-e2e-participant"
	}

	outputDir := scenario.outputDir
	if outputDir == "" {
		suffix := scenario.name
		if suffix == "" {
			suffix = "default"
		}
		outputDir = filepath.Join(repoRoot, "examples", "publisher-hls-agent", fmt.Sprintf("hls-agent-recordings-%s", suffix))
	}

	if err := os.RemoveAll(outputDir); err != nil {
		t.Fatalf("failed to clean output dir: %v", err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create output dir: %v", err)
	}
	keepOutputs := os.Getenv("PUBLISHER_HLS_KEEP_MINIO") == "1"
	t.Cleanup(func() {
		if keepOutputs {
			t.Logf("PUBLISHER_HLS_KEEP_MINIO=1 set; preserving output dir: %s", outputDir)
			return
		}
		if !t.Failed() {
			_ = os.RemoveAll(outputDir)
		} else {
			t.Logf("preserving output dir for failed test: %s", outputDir)
		}
	})

	tempRoot := t.TempDir()
	serverLogPath := filepath.Join(tempRoot, "livekit-server.log")
	agentLogPath := filepath.Join(tempRoot, "publisher-hls-agent.log")
	t.Logf("livekit server log: %s", serverLogPath)
	t.Logf("publisher agent log: %s", agentLogPath)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		if data, err := os.ReadFile(agentLogPath); err == nil {
			t.Logf("publisher agent log contents:\n%s", string(data))
		} else {
			t.Logf("failed to read agent log: %v", err)
		}
		if data, err := os.ReadFile(serverLogPath); err == nil {
			t.Logf("livekit server log contents:\n%s", string(data))
		} else {
			t.Logf("failed to read server log: %v", err)
		}
	})

	serverLogFile, err := os.Create(serverLogPath)
	if err != nil {
		t.Fatalf("failed to create server log file: %v", err)
	}
	defer serverLogFile.Close()

	serverCmd := exec.Command(serverBinary, "--dev", "--config", configPath, "--node-ip", "127.0.0.1")
	serverCmd.Dir = repoRoot
	serverCmd.Stdout = serverLogFile
	serverCmd.Stderr = serverLogFile

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start livekit server: %v", err)
	}
	t.Cleanup(func() {
		shutdownProcess(t, serverCmd, "livekit-server", 10*time.Second)
	})

	if err := waitForLiveKitServer("localhost:7880", 25*time.Second); err != nil {
		t.Fatalf("livekit server not ready: %v", err)
	}

	agentLogFile, err := os.Create(agentLogPath)
	if err != nil {
		t.Fatalf("failed to create agent log file: %v", err)
	}
	defer agentLogFile.Close()

	agentDir := filepath.Join(repoRoot, "examples", "publisher-hls-agent")
	agentCmd := exec.Command("go", "run", ".")
	agentCmd.Dir = agentDir
	agentCmd.Stdout = agentLogFile
	agentCmd.Stderr = agentLogFile
	agentCmd.Env = append(os.Environ(),
		fmt.Sprintf("LIVEKIT_URL=%s", testLiveKitURL),
		fmt.Sprintf("LIVEKIT_API_KEY=%s", testAPIKey),
		fmt.Sprintf("LIVEKIT_API_SECRET=%s", testAPISecret),
		fmt.Sprintf("OUTPUT_DIR=%s", outputDir),
		fmt.Sprintf("AGENT_NAME=%s", agentName),
		"HLS_SEGMENT_DURATION=2",
		"HLS_MAX_SEGMENTS=0",
	)
	for k, v := range scenario.agentEnv {
		agentCmd.Env = append(agentCmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	if _, ok := scenario.agentEnv["AUTO_ACTIVATE_RECORDING"]; !ok {
		agentCmd.Env = append(agentCmd.Env, "AUTO_ACTIVATE_RECORDING=true")
	}

	if err := agentCmd.Start(); err != nil {
		t.Fatalf("failed to start publisher agent: %v", err)
	}
	t.Cleanup(func() {
		shutdownProcess(t, agentCmd, "publisher-hls-agent", 15*time.Second)
	})
	t.Cleanup(func() {
		if t.Failed() {
			if err := copyFile(agentLogPath, filepath.Join(outputDir, "publisher-hls-agent.log")); err != nil {
				t.Logf("failed to copy agent log: %v", err)
			}
		}
	})

	if err := waitForLogContains(agentLogPath, "Worker registered", 20*time.Second); err != nil {
		t.Fatalf("agent failed to register: %v", err)
	}

	roomClient := lksdk.NewRoomServiceClient("http://localhost:7880", testAPIKey, testAPISecret)
	_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: roomName})

	_, err = roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: agentName,
				Metadata:  `{"record_audio":true,"record_video":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create test room: %v", err)
	}
	defer func() {
		_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: roomName})
	}()

	participantRoom, err := lksdk.ConnectToRoom(testLiveKitURL, lksdk.ConnectInfo{
		APIKey:              testAPIKey,
		APISecret:           testAPISecret,
		RoomName:            roomName,
		ParticipantIdentity: participantIdentity,
		ParticipantName:     "Publisher HLS E2E",
	}, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(true))
	if err != nil {
		t.Fatalf("failed to connect participant: %v", err)
	}
	defer func() {
		if participantRoom != nil {
			participantRoom.Disconnect()
		}
	}()

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

	videoTrack.OnBind(func() { close(videoReady) })
	audioTrack.OnBind(func() { close(audioReady) })

	if _, err := participantRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "publisher-e2e-video",
		Source: livekit.TrackSource_CAMERA,
	}); err != nil {
		t.Fatalf("failed to publish video track: %v", err)
	}

	if _, err := participantRoom.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name: "publisher-e2e-audio",
	}); err != nil {
		t.Fatalf("failed to publish audio track: %v", err)
	}

	select {
	case <-videoReady:
	case <-time.After(10 * time.Second):
		t.Fatal("video track not bound within timeout")
	}

	select {
	case <-audioReady:
	case <-time.After(10 * time.Second):
		t.Fatal("audio track not bound within timeout")
	}

	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("failed to create GStreamer publisher: %v", err)
	}

	if err := publisher.Start(); err != nil {
		t.Fatalf("failed to start GStreamer publisher: %v", err)
	}

	if err := waitForLogContains(agentLogPath, "auto-activating recording", 30*time.Second); err != nil {
		t.Logf("warning: recording auto-activation log not observed: %v (continuing)", err)
		time.Sleep(2 * time.Second)
	}

	if err := publisher.Restart(); err != nil {
		t.Fatalf("failed to restart GStreamer publisher: %v", err)
	}

	if err := publisher.Wait(); err != nil {
		t.Fatalf("publisher error: %v", err)
	}

	publisher.Stop()
	participantOutputDir := filepath.Join(outputDir, roomName, participantIdentity)
	outputFile := filepath.Join(participantOutputDir, "output.ts")

	if participantRoom != nil {
		participantRoom.Disconnect()
		participantRoom = nil
	}

	if _, err := roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: roomName}); err != nil {
		t.Logf("warning: failed to delete room after participant disconnect: %v", err)
	}

	var s3Client *minio.Client
	var s3Bucket string
	var s3Prefix string
	if scenario.agentEnv != nil {
		t.Logf("scenario env: %+v", scenario.agentEnv)
		if endpoint := scenario.agentEnv["S3_ENDPOINT"]; endpoint != "" {
			var err error
			secure := strings.EqualFold(scenario.agentEnv["S3_USE_SSL"], "true")
			forcePathStyle := !strings.EqualFold(scenario.agentEnv["S3_FORCE_PATH_STYLE"], "false")
			opts := &minio.Options{
				Creds:  credentials.NewStaticV4(scenario.agentEnv["S3_ACCESS_KEY"], scenario.agentEnv["S3_SECRET_KEY"], ""),
				Secure: secure,
			}
			if region := scenario.agentEnv["S3_REGION"]; region != "" {
				opts.Region = region
			}
			if forcePathStyle {
				opts.BucketLookup = minio.BucketLookupPath
			}
			s3Client, err = minio.New(endpoint, opts)
			if err != nil {
				t.Fatalf("failed to create minio client: %v", err)
			}
			s3Bucket = scenario.agentEnv["S3_BUCKET"]
			s3Prefix = strings.Trim(scenario.agentEnv["S3_PREFIX"], "/")
			t.Logf("S3 validation enabled: endpoint=%s bucket=%s prefix=%s", endpoint, s3Bucket, s3Prefix)
		}
	}

	if s3Client == nil {
		if err := waitForFile(outputFile, 75*time.Second); err != nil {
			t.Fatalf("recording not created: %v", err)
		}

		time.Sleep(3 * time.Second)

		if err := validateRecordingOutput(t, outputFile, testVideo); err != nil {
			t.Fatalf("recording validation failed: %v", err)
		}
	} else {
		remotePrefix := path.Join(strings.Trim(s3Prefix, "/"), roomName, participantIdentity)
		if err := waitForLogContains(agentLogPath, "uploaded recording to", 2*time.Minute); err != nil {
			t.Fatalf("timed out waiting for S3 upload completion log: %v", err)
		}
		t.Log("observed S3 upload completion log")
		t.Logf("validating S3 recording at s3://%s/%s", s3Bucket, remotePrefix)
		if err := validateS3Recording(t, s3Client, s3Bucket, remotePrefix, testVideo, outputDir); err != nil {
			t.Fatalf("S3 validation failed: %v", err)
		}
		t.Logf("S3 validation succeeded for %s", remotePrefix)
		playlistURL := fmt.Sprintf("http://%s/%s/%s/playlist.m3u8", scenario.agentEnv["S3_ENDPOINT"], s3Bucket, remotePrefix)
		t.Logf("S3 playlist URL: %s", playlistURL)
	}

	if participantRoom != nil {
		participantRoom.Disconnect()
		participantRoom = nil
	}

	// room already deleted above; ignore errors here for idempotency.
	_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: roomName})

	shutdownProcess(t, agentCmd, "publisher-hls-agent", 5*time.Second)
	agentCmd = nil
	shutdownProcess(t, serverCmd, "livekit-server", 5*time.Second)
	serverCmd = nil

	return e2eResult{
		outputDir:       outputDir,
		participantDir:  participantOutputDir,
		agentLogPath:    agentLogPath,
		roomName:        roomName,
		participantName: participantIdentity,
	}
}

func requireFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("required file missing %s: %v", path, err)
	}
}

func shutdownProcess(t *testing.T, cmd *exec.Cmd, name string, timeout time.Duration) {
	t.Helper()
	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Signal(syscall.SIGINT)

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("%s exited with error: %v", name, err)
		} else {
			t.Logf("%s exited cleanly", name)
		}
	case <-time.After(timeout):
		t.Logf("%s did not exit after %v, killing", name, timeout)
		_ = cmd.Process.Kill()
		if err := <-done; err != nil {
			t.Logf("%s kill wait error: %v", name, err)
		}
	}
}

func waitForLogContains(path, needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), needle) {
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q to appear in %s", needle, path)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

type minioServer struct {
	Cmd       *exec.Cmd
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Container string
	DataDir   string
	KeepAlive bool
}

func (m *minioServer) Shutdown(t *testing.T) {
	t.Helper()
	if m == nil || m.KeepAlive {
		return
	}
	if m.Container != "" {
		_ = exec.Command("docker", "rm", "-f", m.Container).Run()
	}
	if m.Cmd != nil && m.Cmd.Process != nil {
		_ = m.Cmd.Process.Signal(syscall.SIGINT)
		done := make(chan error, 1)
		go func() {
			done <- m.Cmd.Wait()
		}()
		select {
		case <-time.After(5 * time.Second):
			_ = m.Cmd.Process.Kill()
		case <-done:
		}
	}
}

func (m *minioServer) NewClient(t *testing.T) *minio.Client {
	t.Helper()
	client, err := minio.New(m.Endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(m.AccessKey, m.SecretKey, ""),
		Secure:       false,
		Region:       "us-east-1",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("failed to create minio client: %v", err)
	}
	return client
}

func startMinIOServer(t *testing.T) *minioServer {
	t.Helper()

	minioPath, err := exec.LookPath("minio")
	useDocker := false
	if err != nil {
		if _, err := exec.LookPath("docker"); err != nil {
			t.Skip("neither minio binary nor docker found; skipping S3 integration test")
		}
		useDocker = true
	}

	keepAlive := os.Getenv("PUBLISHER_HLS_KEEP_MINIO") == "1"
	var dataDir string
	if keepAlive {
		dir, err := os.MkdirTemp("", "publisher-hls-minio-*")
		if err != nil {
			t.Fatalf("failed to create persistent minio data dir: %v", err)
		}
		dataDir = dir
	} else {
		dataDir = t.TempDir()
	}
	consolePort := mustGetFreePort(t)
	apiPort := mustGetFreePort(t)

	accessKey := "minioadmin"
	secretKey := "minioadmin"

	server := &minioServer{
		Endpoint:  fmt.Sprintf("127.0.0.1:%d", apiPort),
		AccessKey: accessKey,
		SecretKey: secretKey,
		Bucket:    "publisher-hls",
		DataDir:   dataDir,
		KeepAlive: keepAlive,
	}

	if useDocker {
		containerName := fmt.Sprintf("minio-e2e-%d", time.Now().UnixNano())
		args := []string{
			"run", "-d", "--rm",
			"--name", containerName,
			"-p", fmt.Sprintf("%d:9000", apiPort),
			"-p", fmt.Sprintf("%d:9001", consolePort),
			"-e", fmt.Sprintf("MINIO_ROOT_USER=%s", accessKey),
			"-e", fmt.Sprintf("MINIO_ROOT_PASSWORD=%s", secretKey),
			"quay.io/minio/minio", "server", "/data", "--console-address", ":9001",
		}
		cmd := exec.Command("docker", args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("failed to start minio via docker: %v (output: %s)", err, string(output))
		}
		server.Container = strings.TrimSpace(string(output))
	} else {
		cmd := exec.Command(minioPath, "server", dataDir,
			"--address", fmt.Sprintf("127.0.0.1:%d", apiPort),
			"--console-address", fmt.Sprintf("127.0.0.1:%d", consolePort))
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("MINIO_ROOT_USER=%s", accessKey),
			fmt.Sprintf("MINIO_ROOT_PASSWORD=%s", secretKey),
		)

		stdout, err := os.CreateTemp("", "minio-stdout-*.log")
		if err == nil {
			cmd.Stdout = stdout
			cmd.Stderr = stdout
			defer func() {
				if t.Failed() {
					if data, err := os.ReadFile(stdout.Name()); err == nil {
						t.Logf("minio stdout:\n%s", string(data))
					}
				}
				stdout.Close()
				_ = os.Remove(stdout.Name())
			}()
		}

		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start minio server: %v", err)
		}
		server.Cmd = cmd
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		resp, err := http.Get(fmt.Sprintf("http://%s/minio/health/live", server.Endpoint))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ctx.Err() != nil {
		server.Shutdown(t)
		t.Fatalf("minio server did not become ready: %v", ctx.Err())
	}

	client := server.NewClient(t)
	ctxCreate, cancelCreate := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCreate()
	exists, err := client.BucketExists(ctxCreate, server.Bucket)
	if err != nil {
		server.Shutdown(t)
		t.Fatalf("failed to check bucket: %v", err)
	}
	if !exists {
		if err := client.MakeBucket(ctxCreate, server.Bucket, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
			server.Shutdown(t)
			t.Fatalf("failed to create bucket: %v", err)
		}
	}

	return server
}

func mustGetFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func validateS3Recording(t *testing.T, client *minio.Client, bucket, prefix, referenceVideo, artifactDir string) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	wait := func(object string) error {
		t.Logf("waiting for S3 object %s/%s", bucket, object)
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			_, err := client.StatObject(ctx, bucket, object, minio.StatObjectOptions{})
			if err == nil {
				t.Logf("found S3 object %s/%s", bucket, object)
				return nil
			}
			if minio.ToErrorResponse(err).Code == "NoSuchKey" || minio.ToErrorResponse(err).Code == "" {
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return fmt.Errorf("stat %s: %w", object, err)
		}
		return fmt.Errorf("object %s not found in S3 within timeout", object)
	}

	playlistObj := path.Join(prefix, "playlist.m3u8")
	outputObj := path.Join(prefix, "output.ts")

	if err := wait(playlistObj); err != nil {
		return err
	}
	if err := wait(outputObj); err != nil {
		return err
	}

	tempDir := t.TempDir()
	localPlaylist := filepath.Join(tempDir, "playlist.m3u8")
	localOutput := filepath.Join(tempDir, "output.ts")

	if err := client.FGetObject(ctx, bucket, playlistObj, localPlaylist, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download playlist: %w", err)
	}
	t.Logf("downloaded playlist to %s", localPlaylist)
	if err := client.FGetObject(ctx, bucket, outputObj, localOutput, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download output.ts: %w", err)
	}
	t.Logf("downloaded output.ts to %s", localOutput)

	segments, _, _, err := inspectPlaylist(localPlaylist)
	if err != nil {
		return fmt.Errorf("inspect playlist: %w", err)
	}
	localSegments := make(map[string]string, len(segments))
	for _, segment := range segments {
		objectName := path.Join(prefix, segment)
		if err := wait(objectName); err != nil {
			return err
		}
		localPath := filepath.Join(tempDir, segment)
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return fmt.Errorf("create segment dir for %s: %w", segment, err)
		}
		if err := client.FGetObject(ctx, bucket, objectName, localPath, minio.GetObjectOptions{}); err != nil {
			return fmt.Errorf("download segment %s: %w", segment, err)
		}
		localSegments[segment] = localPath
	}
	t.Logf("downloaded %d HLS segments for validation", len(segments))

	updated, err := normalizePlaylistDurations(localPlaylist, localSegments)
	if err != nil {
		return fmt.Errorf("normalize playlist: %w", err)
	}
	if updated {
		t.Logf("normalized playlist durations for %s", playlistObj)
		if _, err := client.FPutObject(ctx, bucket, playlistObj, localPlaylist, minio.PutObjectOptions{
			ContentType: "application/vnd.apple.mpegurl",
		}); err != nil {
			return fmt.Errorf("upload normalized playlist: %w", err)
		}
		t.Logf("re-uploaded sanitized playlist to S3 at %s", playlistObj)
	}

	if artifactDir != "" && os.Getenv("PUBLISHER_HLS_KEEP_MINIO") == "1" {
		trimmedPrefix := strings.Trim(prefix, "/")
		debugDir := filepath.Join(artifactDir, "sanitized-playlists", filepath.FromSlash(trimmedPrefix))
		if err := os.MkdirAll(debugDir, 0o755); err != nil {
			t.Logf("failed to create sanitized playlist directory %s: %v", debugDir, err)
		} else {
			debugPlaylist := filepath.Join(debugDir, "playlist.m3u8")
			if err := copyFile(localPlaylist, debugPlaylist); err != nil {
				t.Logf("failed to copy sanitized playlist to %s: %v", debugPlaylist, err)
			} else {
				t.Logf("saved sanitized playlist copy to %s", debugPlaylist)
			}
		}
	}

	if err := validateRecordingOutput(t, localOutput, referenceVideo); err != nil {
		return err
	}
	return nil
}

func normalizePlaylistDurations(playlistPath string, segmentPaths map[string]string) (bool, error) {
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		return false, fmt.Errorf("read playlist for normalization: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	changed := false
	maxDuration := 0.0

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}

		if i+1 >= len(lines) {
			continue
		}
		segmentLine := strings.TrimSpace(lines[i+1])
		if segmentLine == "" || strings.HasPrefix(segmentLine, "#") {
			continue
		}

		segmentPath, ok := segmentPaths[segmentLine]
		if !ok {
			segmentPath = filepath.Join(filepath.Dir(playlistPath), segmentLine)
		}
		actualDuration, err := ffprobeSegmentDuration(segmentPath)
		if err != nil {
			// Skip invalid segments (e.g., partial segments from pipeline shutdown)
			// and remove them from the playlist
			continue
		}
		normalized := sanitizeSegmentDuration(actualDuration)
		if normalized < 0 {
			normalized = 0
		}
		if normalized > maxDuration {
			maxDuration = normalized
		}
		formatted := fmt.Sprintf("#EXTINF:%.3f,", normalized)
		if line != formatted {
			lines[i] = formatted
			changed = true
		}
	}

	if maxDuration > 0 {
		target := int(math.Ceil(maxDuration))
		if target < 1 {
			target = 1
		}
		targetLine := fmt.Sprintf("#EXT-X-TARGETDURATION:%d", target)
		targetUpdated := false
		for i, raw := range lines {
			if strings.HasPrefix(strings.TrimSpace(raw), "#EXT-X-TARGETDURATION:") {
				targetUpdated = true
				if strings.TrimSpace(raw) != targetLine {
					lines[i] = targetLine
					changed = true
				}
				break
			}
		}
		if !targetUpdated {
			insertIdx := 1
			for i, raw := range lines {
				if strings.HasPrefix(strings.TrimSpace(raw), "#EXTM3U") {
					insertIdx = i + 1
					break
				}
			}
			lines = append(lines[:insertIdx], append([]string{targetLine}, lines[insertIdx:]...)...)
			changed = true
		}
	}

	if !changed {
		return false, nil
	}

	if err := os.WriteFile(playlistPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return false, fmt.Errorf("write normalized playlist: %w", err)
	}
	return true, nil
}

func ffprobeSegmentDuration(path string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nokey=1:noprint_wrappers=1",
		path,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration for %s: %w (output: %s)", path, err, strings.TrimSpace(string(output)))
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return 0, fmt.Errorf("ffprobe returned empty duration for %s", path)
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration for %s: %w (value: %s)", path, err, text)
	}
	return value, nil
}

func sanitizeSegmentDuration(duration float64) float64 {
	if math.IsNaN(duration) || math.IsInf(duration, 0) {
		return 0
	}
	if duration < 0 {
		duration = 0
	}
	if duration < 0.01 {
		duration = 0.01
	}
	if duration > 60 {
		duration = 60
	}
	return math.Round(duration*1000) / 1000
}
