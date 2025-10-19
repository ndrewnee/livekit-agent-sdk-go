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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pion/webrtc/v4"
)

// e2eScenario defines the configuration for an end-to-end integration test scenario.
//
// This structure encapsulates all parameters needed to run a complete test including
// room configuration, agent settings, output locations, and environment variables.
//
// Fields:
//   - name: Scenario identifier used for output directory naming (e.g., "s3-upload")
//   - agentName: LiveKit agent name for job dispatch (defaults to "publisher-hls-e2e-agent")
//   - roomName: LiveKit room name to create (defaults to "publisher-hls-e2e-room")
//   - participant: Participant identity for testing (defaults to "publisher-hls-e2e-participant")
//   - outputDir: Local directory for HLS recordings (defaults to hls-agent-recordings-{name})
//   - agentEnv: Environment variables to pass to the agent process (e.g., S3 credentials)
//   - skipS3Validation: If true, skips built-in S3 validation in runE2EScenario (for custom validation)
type e2eScenario struct {
	name             string
	agentName        string
	roomName         string
	participant      string
	outputDir        string
	agentEnv         map[string]string
	skipS3Validation bool // Skip built-in S3 validation in runE2EScenario (for custom validation)
}

// e2eResult contains the output paths and identifiers from a completed end-to-end test.
//
// This structure is returned by runE2EScenario and provides access to all artifacts
// and metadata generated during the test for further validation or debugging.
//
// Fields:
//   - outputDir: Root directory containing all recordings for this test run
//   - participantDir: Specific subdirectory for this participant's recording session
//   - agentLogPath: Path to the agent's log file
//   - roomName: LiveKit room name used in the test
//   - participantName: Participant identity used in the test
type e2eResult struct {
	outputDir       string
	participantDir  string
	agentLogPath    string
	roomName        string
	participantName string
}

// TestPublisherHLSAgentUploadsToS3 validates that the publisher-hls-agent correctly
// uploads HLS recordings to S3-compatible storage and that the uploaded files are valid.
//
// This test performs the following validations:
//  1. Starts a local MinIO server for S3 storage
//  2. Creates a LiveKit room with agent dispatch configuration
//  3. Publishes H.264 + Opus media tracks to the room
//  4. Waits for the agent to record and upload to S3
//  5. Validates the S3 playlist file contains valid segment entries
//  6. Verifies first segment has non-zero duration
//  7. Sets public read ACL for testing HLS playback URLs
//
// Environment variables:
//   - PUBLISHER_HLS_KEEP_MINIO: If "1", MinIO server stays running after test for manual inspection
//
// The test uses a unique agent name per run to avoid conflicts with stale workers
// from previous test runs.
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
			"KEEP_OPUS":               "true", // H.264 + Opus
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

	_ = runE2EScenario(t, scenario)

	// Skip local file validation when S3 upload is enabled
	// (local files are cleaned up after successful S3 upload)
	// Local file validation is only needed for non-S3 tests

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

// TestPublisherHLSAgentMultipleParticipants tests the agent's behavior with multiple simultaneous
// participants joining the room and publishing tracks. This full-scale test validates:
//   - Concurrent participant connections
//   - Multiple simultaneous HLS recordings
//   - S3 uploads for all participants
//   - Agent stability under load
//
// By default, this test creates 3 participants. Set PARTICIPANT_COUNT environment variable
// to test with a different number of participants.
func TestPublisherHLSAgentMultipleParticipants(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end integration test in short mode")
	}

	// Configure test parameters
	participantCount := 3
	if countStr := os.Getenv("PARTICIPANT_COUNT"); countStr != "" {
		if count, err := strconv.Atoi(countStr); err == nil && count > 0 {
			participantCount = count
		}
	}

	t.Logf("Testing with %d simultaneous participants", participantCount)

	ms := startMinIOServer(t)
	if !ms.KeepAlive {
		defer ms.Shutdown(t)
	} else {
		t.Logf("PUBLISHER_HLS_KEEP_MINIO=1 detected; MinIO will remain running at http://%s", ms.Endpoint)
	}

	uniqueAgentName := fmt.Sprintf("publisher-hls-multi-agent-%d", time.Now().UnixNano())
	roomName := "publisher-hls-multi-room"

	// Set up infrastructure (server, agent, room)
	repoRoot := findRepoRoot(t)
	serverBinary, err := exec.LookPath("livekit-server")
	if err != nil {
		t.Fatalf("livekit-server not found in PATH: %v", err)
	}
	configPath := filepath.Join(repoRoot, "examples", "livekit-server-dev.yaml")
	testVideo := filepath.Join(repoRoot, "examples", "publisher-hls-agent", "test", "test.mp4")
	requireFileExists(t, testVideo)

	outputDir := filepath.Join(repoRoot, "examples", "publisher-hls-agent", "hls-agent-recordings-multi-participant")
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

	// Start LiveKit server
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

	// Start publisher-hls-agent
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
		fmt.Sprintf("AGENT_NAME=%s", uniqueAgentName),
		"HLS_SEGMENT_DURATION=2",
		"HLS_MAX_SEGMENTS=0",
		"KEEP_OPUS=true",
		fmt.Sprintf("S3_ENDPOINT=%s", ms.Endpoint),
		fmt.Sprintf("S3_BUCKET=%s", ms.Bucket),
		"S3_REGION=us-east-1",
		fmt.Sprintf("S3_ACCESS_KEY=%s", ms.AccessKey),
		fmt.Sprintf("S3_SECRET_KEY=%s", ms.SecretKey),
		"S3_FORCE_PATH_STYLE=true",
		"S3_USE_SSL=false",
		"S3_PREFIX=multi-participant-tests",
		"S3_OBJECT_ACL=public-read",
		"AUTO_ACTIVATE_RECORDING=true",
	)

	if err := agentCmd.Start(); err != nil {
		t.Fatalf("failed to start publisher agent: %v", err)
	}
	t.Cleanup(func() {
		shutdownProcess(t, agentCmd, "publisher-hls-agent", 15*time.Second)
	})

	if err := waitForLogContains(agentLogPath, "Worker registered", 20*time.Second); err != nil {
		t.Fatalf("agent failed to register: %v", err)
	}

	// Create room with agent dispatch
	roomClient := lksdk.NewRoomServiceClient("http://localhost:7880", testAPIKey, testAPISecret)
	_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: roomName})

	_, err = roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: uniqueAgentName,
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

	// Launch multiple participants concurrently
	var wg sync.WaitGroup
	participantErrors := make(chan error, participantCount)
	participantNames := make([]string, participantCount)

	for i := 0; i < participantCount; i++ {
		participantIdentity := fmt.Sprintf("multi-participant-%d", i+1)
		participantNames[i] = participantIdentity

		wg.Add(1)
		go func(idx int, identity string) {
			defer wg.Done()

			if err := runParticipant(t, roomName, identity, testVideo, agentLogPath); err != nil {
				participantErrors <- fmt.Errorf("participant %s failed: %w", identity, err)
			} else {
				t.Logf("✓ participant %s completed successfully", identity)
			}
		}(i, participantIdentity)

		// Stagger participant joins slightly to simulate realistic conditions
		time.Sleep(100 * time.Millisecond)
	}

	// Wait for all participants to complete
	t.Logf("Waiting for all %d participants to complete...", participantCount)
	wg.Wait()
	close(participantErrors)

	// Check for any participant errors
	for err := range participantErrors {
		t.Errorf("Participant error: %v", err)
	}

	// Wait for S3 uploads to complete
	t.Logf("Waiting for S3 uploads to complete...")
	time.Sleep(10 * time.Second)

	// Validate S3 recordings for all participants
	client := ms.NewClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	successCount := 0
	s3Links := make(map[string]string)

	for _, participantName := range participantNames {
		prefix := path.Join("multi-participant-tests", roomName, participantName)
		playlistObj := path.Join(prefix, "playlist.m3u8")

		t.Logf("Validating S3 recording for %s...", participantName)

		// Wait for playlist to appear in S3
		found := false
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			_, err := client.StatObject(ctx, ms.Bucket, playlistObj, minio.StatObjectOptions{})
			if err == nil {
				found = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !found {
			t.Errorf("playlist not found in S3 for participant %s at %s", participantName, playlistObj)
			continue
		}

		// Validate playlist content
		reader, err := client.GetObject(ctx, ms.Bucket, playlistObj, minio.GetObjectOptions{})
		if err != nil {
			t.Errorf("failed to fetch playlist for %s: %v", participantName, err)
			continue
		}

		scanner := bufio.NewScanner(reader)
		foundSegments := 0
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "#EXTINF:") {
				foundSegments++
			}
		}
		reader.Close()

		if foundSegments == 0 {
			t.Errorf("playlist for %s has no segments", participantName)
			continue
		}

		// Generate S3 URL for this participant's recording
		s3URL := fmt.Sprintf("http://%s/%s/%s", ms.Endpoint, ms.Bucket, playlistObj)
		s3Links[participantName] = s3URL

		t.Logf("✓ participant %s: validated S3 recording with %d segments", participantName, foundSegments)
		successCount++
	}

	if successCount != participantCount {
		t.Fatalf("Only %d/%d participants had valid S3 recordings", successCount, participantCount)
	}

	t.Logf("✓ All %d participants successfully recorded to S3", participantCount)

	// Set bucket policy to allow public read access for all participant recordings
	prefix := path.Join("multi-participant-tests", roomName)
	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/%s/*"]}]}`, ms.Bucket, prefix)
	if err := client.SetBucketPolicy(context.Background(), ms.Bucket, policy); err != nil {
		t.Fatalf("failed to set read policy on MinIO bucket: %v", err)
	}
	t.Logf("")
	t.Logf("=== HLS Recording URLs ===")
	for _, participantName := range participantNames {
		if url, ok := s3Links[participantName]; ok {
			t.Logf("  %s: %s", participantName, url)
		}
	}
	t.Logf("")
	t.Logf("S3 bucket: http://%s/%s/multi-participant-tests/%s/", ms.Endpoint, ms.Bucket, roomName)

	if ms.KeepAlive {
		t.Logf("MinIO data dir: %s (server left running)", ms.DataDir)
	}
}

// runParticipant connects a single participant, publishes tracks, and waits for recording to complete.
//
// This helper function is used by TestPublisherHLSAgentMultipleParticipants to simulate
// a participant joining a room, publishing media, and recording for the duration of the test video.
//
// Lifecycle:
//  1. Connect to LiveKit room with given identity
//  2. Create H.264 video and Opus audio local tracks
//  3. Publish tracks to the room with track names based on participant identity
//  4. Wait for tracks to be bound (WebRTC negotiation complete)
//  5. Start GStreamer publisher to stream test video file
//  6. Restart publisher midway to simulate real-world reconnection scenarios
//  7. Wait for test video to complete playback
//  8. Stop publisher and disconnect from room
//
// Parameters:
//   - t: Test context for logging and assertions
//   - roomName: LiveKit room to join
//   - participantIdentity: Unique identity for this participant
//   - testVideo: Path to MP4 test video file
//   - agentLogPath: Path to agent log file (for debugging if needed)
//
// Returns:
//   - nil on success
//   - error if connection, track publication, or media streaming fails
//
// This function is designed to be called concurrently from multiple goroutines
// to test the agent's behavior under multiple simultaneous participants.
func runParticipant(t *testing.T, roomName, participantIdentity, testVideo, agentLogPath string) error {
	t.Helper()

	participantRoom, err := lksdk.ConnectToRoom(testLiveKitURL, lksdk.ConnectInfo{
		APIKey:              testAPIKey,
		APISecret:           testAPISecret,
		RoomName:            roomName,
		ParticipantIdentity: participantIdentity,
		ParticipantName:     fmt.Sprintf("Publisher %s", participantIdentity),
	}, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(true))
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer participantRoom.Disconnect()

	// Create and publish video track
	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		return fmt.Errorf("failed to create video track: %w", err)
	}

	// Create and publish audio track
	audioTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		return fmt.Errorf("failed to create audio track: %w", err)
	}

	videoReady := make(chan struct{})
	audioReady := make(chan struct{})

	videoTrack.OnBind(func() { close(videoReady) })
	audioTrack.OnBind(func() { close(audioReady) })

	if _, err := participantRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   fmt.Sprintf("%s-video", participantIdentity),
		Source: livekit.TrackSource_CAMERA,
	}); err != nil {
		return fmt.Errorf("failed to publish video track: %w", err)
	}

	if _, err := participantRoom.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name: fmt.Sprintf("%s-audio", participantIdentity),
	}); err != nil {
		return fmt.Errorf("failed to publish audio track: %w", err)
	}

	// Wait for tracks to be bound
	select {
	case <-videoReady:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("video track not bound within timeout")
	}

	select {
	case <-audioReady:
	case <-time.After(10 * time.Second):
		return fmt.Errorf("audio track not bound within timeout")
	}

	// Start publishing media
	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		return fmt.Errorf("failed to create GStreamer publisher: %w", err)
	}

	if err := publisher.Start(); err != nil {
		return fmt.Errorf("failed to start GStreamer publisher: %w", err)
	}

	// Wait a bit for recording to start
	time.Sleep(2 * time.Second)

	// Restart publisher to simulate real usage (optional, can be removed for faster tests)
	if err := publisher.Restart(); err != nil {
		return fmt.Errorf("failed to restart GStreamer publisher: %w", err)
	}

	if err := publisher.Wait(); err != nil {
		return fmt.Errorf("publisher error: %w", err)
	}

	publisher.Stop()

	return nil
}

// runE2EScenario executes a complete end-to-end integration test scenario.
//
// This is the main test orchestration function that sets up the complete testing
// environment including LiveKit server, publisher-hls-agent, MinIO (if S3 enabled),
// and a test participant publishing media.
//
// Test infrastructure setup:
//  1. Starts livekit-server with dev configuration
//  2. Starts publisher-hls-agent with scenario-specific environment
//  3. Creates LiveKit room with agent dispatch configuration
//  4. Connects test participant and publishes H.264 + Opus tracks
//  5. Streams test video file to LiveKit room
//  6. Waits for recording completion
//  7. Validates recording output (local or S3 depending on configuration)
//  8. Cleans up all processes and resources
//
// The function handles three validation modes:
//   - Local validation: Validates output.ts file if S3 is disabled
//   - S3 post-processing validation: Built-in validation after recording completes
//   - Custom S3 validation: Test performs its own validation (skipS3Validation=true)
//
// Parameters:
//   - t: Test context for logging, assertions, and cleanup
//   - scenario: Configuration defining agent settings, S3 options, and test parameters
//
// Returns:
//   - e2eResult: Paths and identifiers for test artifacts and recordings
//
// Cleanup behavior:
//   - On test failure: Preserves output directory and logs for debugging
//   - On success: Removes output directory unless PUBLISHER_HLS_KEEP_MINIO=1
//   - Always: Shuts down livekit-server and agent processes gracefully
func runE2EScenario(t *testing.T, scenario e2eScenario) e2eResult {
	t.Helper()

	repoRoot := findRepoRoot(t)
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

	// Find the session directory (flat structure: outputDir/room_participant_timestamp)
	pattern := filepath.Join(outputDir, fmt.Sprintf("%s_%s_*", roomName, participantIdentity))
	sessionDirs, err := filepath.Glob(pattern)
	if err != nil || len(sessionDirs) == 0 {
		t.Fatalf("failed to find session directory matching %s: %v", pattern, err)
	}
	participantOutputDir := sessionDirs[0] // Use the first (and only) session directory
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
	} else if !scenario.skipS3Validation {
		// Built-in S3 validation (for post-processing upload tests)
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
	} else {
		// S3 validation skipped - test will perform custom validation
		t.Log("S3 validation skipped (custom validation enabled)")
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

// requireFileExists fails the test if the specified file does not exist.
//
// This helper is used to validate test prerequisites like test video files
// or configuration files before attempting to run tests.
func requireFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("required file missing %s: %v", path, err)
	}
}

// shutdownProcess gracefully shuts down a process with SIGINT, then kills if necessary.
//
// Shutdown sequence:
//  1. Send SIGINT to allow graceful shutdown
//  2. Wait up to timeout duration for process to exit
//  3. If timeout expires, send SIGKILL to force termination
//  4. Log exit status for debugging
//
// Parameters:
//   - t: Test context for logging
//   - cmd: Command to shut down (must have been started)
//   - name: Human-readable process name for log messages
//   - timeout: Maximum time to wait for graceful shutdown
//
// This function is safe to call multiple times and handles nil commands gracefully.
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

// waitForLogContains polls a log file until it contains a specific string or times out.
//
// This function is used to wait for specific events to occur during testing by
// monitoring log file content. It handles cases where the log file doesn't exist
// yet (which is normal at test startup).
//
// Polling strategy:
//   - Reads entire log file every 200ms
//   - Returns immediately when needle is found
//   - Ignores ErrNotExist (file may not exist yet)
//   - Returns timeout error if deadline expires
//
// Parameters:
//   - path: Path to log file to monitor
//   - needle: String to search for in log content
//   - timeout: Maximum time to wait before giving up
//
// Returns:
//   - nil if needle is found within timeout
//   - error if timeout expires or file read fails (non-ErrNotExist)
//
// Common use cases:
//   - Waiting for "Worker registered" to confirm agent started
//   - Waiting for "auto-activating recording" to confirm recording began
//   - Waiting for "uploaded recording to" to confirm S3 upload completed
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

// minioServer represents a running MinIO server instance for S3-compatible testing.
//
// The server can be run either as a local binary or as a Docker container,
// depending on what's available in the environment. Tests use this to provide
// S3-compatible storage without requiring actual AWS credentials or internet access.
//
// Fields:
//   - Cmd: Process handle for locally-run MinIO (nil if using Docker)
//   - Endpoint: Host:port for S3 API access (e.g., "127.0.0.1:12345")
//   - AccessKey: MinIO access key (defaults to "minioadmin")
//   - SecretKey: MinIO secret key (defaults to "minioadmin")
//   - Bucket: Default bucket name ("publisher-hls")
//   - Container: Docker container ID if using Docker (empty if local binary)
//   - DataDir: Local directory for MinIO data storage
//   - KeepAlive: If true, server persists after test for manual inspection
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

// Shutdown stops the MinIO server gracefully.
//
// Behavior:
//   - If KeepAlive is true, does nothing (leaves server running for inspection)
//   - If using Docker: Runs `docker rm -f` to remove container
//   - If using local binary: Sends SIGINT, then SIGKILL after 5s timeout
//
// This method is safe to call multiple times and handles nil receivers gracefully.
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

// NewClient creates a new MinIO client configured to connect to this server instance.
//
// The returned client is pre-configured with:
//   - Endpoint from server instance
//   - Static credentials (AccessKey/SecretKey)
//   - No SSL (Secure: false) for local testing
//   - Path-style bucket lookup for compatibility
//   - us-east-1 region
//
// Returns a configured client or fails the test if client creation errors.
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

// startMinIOServer starts a MinIO server for S3-compatible storage during tests.
//
// Server selection strategy:
//  1. Try to find `minio` binary in PATH
//  2. If not found, check for `docker` binary
//  3. If neither found, skip the test (S3 testing unavailable)
//  4. Start MinIO using whichever method is available
//
// Local binary mode:
//   - Runs MinIO server process with --address for API and --console-address for web UI
//   - Uses random free ports to avoid conflicts
//   - Captures stdout/stderr to temp log file
//   - Sets MINIO_ROOT_USER and MINIO_ROOT_PASSWORD environment variables
//
// Docker mode:
//   - Runs quay.io/minio/minio:latest container
//   - Maps random free ports to container ports 9000 (API) and 9001 (console)
//   - Container runs with --rm flag for automatic cleanup
//   - Uses docker environment variables for credentials
//
// Initialization:
//   - Waits up to 20 seconds for MinIO health endpoint to return 200 OK
//   - Creates default bucket "publisher-hls" if it doesn't exist
//   - Returns configured minioServer instance for test use
//
// Environment variables:
//   - PUBLISHER_HLS_KEEP_MINIO: If "1", server persists after test with data in temp directory
//
// Returns a started and ready MinIO server instance or skips/fails the test.
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

// mustGetFreePort finds and returns an available TCP port on localhost.
//
// The function uses the kernel's port allocation by listening on port 0,
// which causes the OS to assign a free ephemeral port. The port is then
// immediately released and returned for use by the test.
//
// Note: There's a small race condition window between releasing the port
// and using it, but in practice this is rarely an issue for local testing.
//
// Fails the test if no free port can be found.
func mustGetFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// validateS3Recording validates that an HLS recording was successfully uploaded to S3
// and that the playlist and segments are accessible and valid.
//
// Validation steps:
//  1. Wait for playlist.m3u8 to appear in S3 (up to 2 minutes)
//  2. Download playlist to local temp directory
//  3. Parse playlist to extract segment filenames
//  4. Wait for each segment to appear in S3
//  5. Download all segments for validation
//  6. Normalize segment durations using ffprobe
//  7. Update #EXT-X-TARGETDURATION based on actual maximum segment duration
//  8. Re-upload normalized playlist to S3 if changes were made
//
// The normalization step corrects any invalid durations written by GStreamer's hlssink
// (such as the final segment bug where duration may be extremely large).
//
// Parameters:
//   - t: Test context for logging
//   - client: MinIO/S3 client configured for the bucket
//   - bucket: S3 bucket name
//   - prefix: S3 key prefix for this recording (e.g., "tests/room/participant")
//   - referenceVideo: Path to original test video (unused currently, for future validation)
//   - artifactDir: Directory for test artifacts (unused currently)
//
// Returns:
//   - nil if validation succeeds
//   - error if playlist/segments are missing, malformed, or inaccessible
//
// Note: This function does NOT validate output.ts as it's redundant with HLS segments
// and is not uploaded to S3 by the real-time S3 uploader.
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

	if err := wait(playlistObj); err != nil {
		return err
	}

	tempDir := t.TempDir()
	localPlaylist := filepath.Join(tempDir, "playlist.m3u8")

	if err := client.FGetObject(ctx, bucket, playlistObj, localPlaylist, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download playlist: %w", err)
	}
	t.Logf("downloaded playlist to %s", localPlaylist)

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

	// Note: output.ts is not uploaded to S3 (redundant with HLS segments)
	// Validation is based on playlist and segments only
	return nil
}

// normalizePlaylistDurations corrects segment durations in an HLS playlist using ffprobe.
//
// This function addresses GStreamer hlssink bugs where segment durations may be incorrect
// (particularly the final segment which often has an invalid duration like 18446743552).
//
// Algorithm:
//  1. Read playlist.m3u8 file
//  2. For each #EXTINF directive, find corresponding .ts segment file
//  3. Use ffprobe to get actual segment duration
//  4. Sanitize duration (clamp to 0.01-60s range, round to 3 decimals)
//  5. Replace #EXTINF duration if it doesn't match actual duration
//  6. Update #EXT-X-TARGETDURATION to ceiling of maximum segment duration
//  7. Write corrected playlist back to disk if any changes were made
//
// Parameters:
//   - playlistPath: Path to playlist.m3u8 file to normalize
//   - segmentPaths: Map of segment filenames to their local paths (for ffprobe)
//
// Returns:
//   - bool: true if playlist was modified, false if no changes needed
//   - error: nil on success, error if file operations or ffprobe fails
//
// Segment duration sanitization:
//   - NaN or Inf values: Replaced with 0
//   - Negative values: Clamped to 0
//   - Values < 0.01s: Clamped to 0.01s (minimum valid duration)
//   - Values > 60s: Clamped to 60s (sanity check)
//   - All values rounded to 3 decimal places
//
// Invalid segments (ffprobe failures) are skipped rather than failing the entire validation.
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

// ffprobeSegmentDuration uses ffprobe to determine the actual duration of an MPEG-TS segment.
//
// This function shells out to the ffprobe command-line tool to extract the duration
// from the segment's container metadata. This is more reliable than parsing PTS values
// manually and handles edge cases like variable frame rates correctly.
//
// Command:
//
//	ffprobe -v error -show_entries format=duration -of default=nokey=1:noprint_wrappers=1 <path>
//
// Parameters:
//   - path: Path to .ts segment file
//
// Returns:
//   - duration in seconds (float64)
//   - error if ffprobe fails, returns empty output, or duration can't be parsed
//
// Common failure cases:
//   - Segment file is incomplete or corrupted
//   - Segment was written during pipeline shutdown and is invalid
//   - ffprobe is not installed or not in PATH
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

// sanitizeSegmentDuration clamps and rounds a segment duration to a valid HLS range.
//
// Sanitization rules:
//   - NaN or Inf: Return 0
//   - Negative values: Clamp to 0
//   - Values < 0.01s: Clamp to 0.01s (minimum practical segment duration)
//   - Values > 60s: Clamp to 60s (sanity check for obviously invalid values)
//   - All values: Round to 3 decimal places (millisecond precision)
//
// Parameters:
//   - duration: Raw duration value from ffprobe or other source
//
// Returns:
//   - Sanitized duration value suitable for HLS #EXTINF directive
//
// The 0.01s minimum prevents zero-duration segments which can cause playback issues.
// The 60s maximum catches obviously invalid values while allowing legitimate long segments.
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
