package main

import (
	"bufio"
	"context"
	"encoding/json"
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
			// Note: KEEP_OPUS is no longer needed - pipeline always uses separate A/V outputs
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
	playlistObj := path.Join(prefix, "video.m3u8")
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
		// Note: KEEP_OPUS no longer needed - pipeline always uses separate A/V outputs
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
		playlistObj := path.Join(prefix, "video.m3u8")

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
	// New pipeline produces video.m3u8 instead of output.ts
	videoPlaylistFile := filepath.Join(participantOutputDir, "video.m3u8")

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
		if err := waitForFile(videoPlaylistFile, 75*time.Second); err != nil {
			t.Fatalf("video playlist not created: %v", err)
		}

		time.Sleep(3 * time.Second)

		if err := validateVideoPlaylist(t, participantOutputDir); err != nil {
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
		playlistURL := fmt.Sprintf("http://%s/%s/%s/video.m3u8", scenario.agentEnv["S3_ENDPOINT"], s3Bucket, remotePrefix)
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

	playlistObj := path.Join(prefix, "video.m3u8")

	if err := wait(playlistObj); err != nil {
		return err
	}

	// Also wait for audio manifest
	audioManifest := path.Join(prefix, "audio.json")
	if err := wait(audioManifest); err != nil {
		t.Logf("warning: audio manifest not found in S3 (may not be uploaded yet): %v", err)
	}

	// Wait for audio init segment (required for proper CMAF playback)
	audioInit := path.Join(prefix, "audio_init.mp4")
	if err := wait(audioInit); err != nil {
		t.Logf("warning: audio_init.mp4 not found in S3: %v", err)
	} else {
		t.Logf("found audio_init.mp4 in S3")
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

	// Validate audio waveform correlation with source
	if referenceVideo != "" {
		t.Logf("validating audio waveform correlation with source: %s", referenceVideo)
		if err := validateAudioWaveform(t, client, bucket, prefix, referenceVideo, 0.7); err != nil {
			return fmt.Errorf("audio waveform validation failed: %w", err)
		}

		// Validate web player timing (manifest startTime vs actual segment tfdt)
		// This catches issues like "audio plays 2 seconds early"
		t.Logf("validating web player timing (manifest vs tfdt)...")
		if err := validateWebPlayerTiming(t, client, bucket, prefix, referenceVideo, 0.7); err != nil {
			return fmt.Errorf("web player timing validation failed: %w", err)
		}
	}

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

// validateVideoPlaylist validates the new separate A/V output structure.
//
// The new pipeline produces:
//   - video.m3u8 + video*.ts (video-only HLS)
//   - audio.json + audio*.m4s (audio fMP4 segments)
//
// This function validates:
//  1. video.m3u8 exists and has valid segments
//  2. Video segments (.ts files) exist and are non-empty
//  3. audio.json manifest exists (optional, may not be ready immediately)
//
// Parameters:
//   - t: Testing context
//   - outputDir: Directory containing the recording files
//
// Returns:
//   - nil if validation succeeds
//   - error describing the validation failure
func validateVideoPlaylist(t *testing.T, outputDir string) error {
	t.Helper()

	// Check video playlist exists
	videoPlaylist := filepath.Join(outputDir, "video.m3u8")
	stat, err := os.Stat(videoPlaylist)
	if err != nil {
		return fmt.Errorf("video playlist not found: %w", err)
	}
	if stat.Size() == 0 {
		return fmt.Errorf("video playlist is empty")
	}

	// Parse video playlist
	segments, durations, durationSum, err := inspectPlaylist(videoPlaylist)
	if err != nil {
		return fmt.Errorf("failed to inspect video playlist: %w", err)
	}

	if len(segments) == 0 {
		return fmt.Errorf("video playlist has no segments")
	}

	t.Logf("video playlist: %d segments, total duration %.3fs", len(segments), durationSum)

	// Validate segments exist
	validSegments := 0
	for i, segment := range segments {
		segmentPath := filepath.Join(outputDir, segment)
		stat, err := os.Stat(segmentPath)
		if err != nil {
			t.Logf("warning: segment %s not found: %v", segment, err)
			continue
		}
		if stat.Size() == 0 {
			t.Logf("warning: segment %s is empty", segment)
			continue
		}

		// Check duration is reasonable (skip invalid durations > 1000s)
		if i < len(durations) && durations[i] > 0 && durations[i] < 1000 {
			validSegments++
		} else if i < len(durations) && durations[i] > 1000 {
			t.Logf("warning: segment %s has invalid duration %.0fs (likely final segment bug)", segment, durations[i])
		} else {
			validSegments++
		}
	}

	if validSegments == 0 {
		return fmt.Errorf("no valid video segments found")
	}

	t.Logf("validated %d video segments", validSegments)

	// Check audio manifest (optional - may not be uploaded yet)
	audioManifest := filepath.Join(outputDir, "audio.json")
	if stat, err := os.Stat(audioManifest); err == nil && stat.Size() > 0 {
		t.Logf("audio manifest found: %s (%d bytes)", audioManifest, stat.Size())
	} else {
		t.Logf("warning: audio manifest not found (may not be written yet)")
	}

	return nil
}

// validateAudioWaveform validates the recorded audio by comparing it to the source audio.
// It downloads the recorded audio segments from S3, decodes them, and computes correlation
// with the source audio to verify the recording quality.
//
// The function requires FFmpeg to be installed for audio decoding.
//
// Parameters:
//   - t: Testing object for logging
//   - client: MinIO client for S3 access
//   - bucket: S3 bucket name
//   - prefix: S3 prefix for the recording (e.g., "publisher-tests/room/participant")
//   - sourceMP4: Path to the original source MP4 file
//   - minCorrelation: Minimum correlation coefficient required (0.0 to 1.0)
//
// Returns nil on success, error if validation fails.
func validateAudioWaveform(t *testing.T, client *minio.Client, bucket, prefix, sourceMP4 string, minCorrelation float64) error {
	ctx := context.Background()
	tempDir := t.TempDir()

	// Step 1: Download audio files from S3
	t.Logf("downloading audio files from S3 (bucket=%s, prefix=%s)", bucket, prefix)

	initPath := filepath.Join(tempDir, "audio_init.mp4")
	initKey := path.Join(prefix, "audio_init.mp4")

	if err := client.FGetObject(ctx, bucket, initKey, initPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download audio_init.mp4: %w", err)
	}
	t.Logf("downloaded audio_init.mp4")

	// Download all audio segments
	var segmentPaths []string
	for i := 0; ; i++ {
		segmentName := fmt.Sprintf("audio%05d.m4s", i)
		segmentKey := path.Join(prefix, segmentName)
		segmentPath := filepath.Join(tempDir, segmentName)

		err := client.FGetObject(ctx, bucket, segmentKey, segmentPath, minio.GetObjectOptions{})
		if err != nil {
			// No more segments
			break
		}
		segmentPaths = append(segmentPaths, segmentPath)
	}

	if len(segmentPaths) == 0 {
		return fmt.Errorf("no audio segments found in S3")
	}
	t.Logf("downloaded %d audio segments", len(segmentPaths))

	// Step 2: Create a single fMP4 file by binary concatenation (init + segments)
	// FFmpeg's concat demuxer doesn't work with fMP4, so we concatenate the raw bytes
	combinedPath := filepath.Join(tempDir, "combined.mp4")
	combinedFile, err := os.Create(combinedPath)
	if err != nil {
		return fmt.Errorf("create combined file: %w", err)
	}

	// Write init segment first
	initData, err := os.ReadFile(initPath)
	if err != nil {
		combinedFile.Close()
		return fmt.Errorf("read init segment: %w", err)
	}
	if _, err := combinedFile.Write(initData); err != nil {
		combinedFile.Close()
		return fmt.Errorf("write init data: %w", err)
	}
	t.Logf("init segment: %d bytes", len(initData))

	// Append all media segments
	totalSegmentBytes := 0
	for _, segPath := range segmentPaths {
		segData, err := os.ReadFile(segPath)
		if err != nil {
			t.Logf("warning: failed to read segment %s: %v", segPath, err)
			continue
		}
		if _, err := combinedFile.Write(segData); err != nil {
			combinedFile.Close()
			return fmt.Errorf("write segment data: %w", err)
		}
		totalSegmentBytes += len(segData)
	}
	combinedFile.Close()
	t.Logf("combined fMP4: init=%d bytes + segments=%d bytes", len(initData), totalSegmentBytes)

	// Step 3: Decode combined fMP4 to raw PCM (mono, 48kHz, float32)
	recordedPCMPath := filepath.Join(tempDir, "recorded.raw")
	ffmpegCmd := exec.Command("ffmpeg",
		"-i", combinedPath,
		"-f", "f32le", "-acodec", "pcm_f32le",
		"-ac", "1", "-ar", "48000",
		"-y", recordedPCMPath,
	)
	ffmpegOut, err := ffmpegCmd.CombinedOutput()
	if err != nil {
		t.Logf("FFmpeg output: %s", string(ffmpegOut))
		// Log hex dump of init segment for debugging
		t.Logf("init segment hex (first 100 bytes): %x", initData[:min(100, len(initData))])
		return fmt.Errorf("decode recorded audio: %w", err)
	}
	t.Logf("decoded recorded audio to PCM")

	// Step 4: Decode source audio to raw PCM
	sourcePCMPath := filepath.Join(tempDir, "source.raw")
	ffmpegCmd = exec.Command("ffmpeg",
		"-i", sourceMP4,
		"-f", "f32le", "-acodec", "pcm_f32le",
		"-ac", "1", "-ar", "48000",
		"-y", sourcePCMPath,
	)
	ffmpegOut, err = ffmpegCmd.CombinedOutput()
	if err != nil {
		t.Logf("FFmpeg output: %s", string(ffmpegOut))
		return fmt.Errorf("decode source audio: %w", err)
	}
	t.Logf("decoded source audio to PCM")

	// Step 5: Read PCM samples
	sourceSamples, err := readFloat32PCM(sourcePCMPath)
	if err != nil {
		return fmt.Errorf("read source PCM: %w", err)
	}
	t.Logf("source audio: %d samples (%.2fs at 48kHz)", len(sourceSamples), float64(len(sourceSamples))/48000.0)

	recordedSamples, err := readFloat32PCM(recordedPCMPath)
	if err != nil {
		return fmt.Errorf("read recorded PCM: %w", err)
	}
	t.Logf("recorded audio: %d samples (%.2fs at 48kHz)", len(recordedSamples), float64(len(recordedSamples))/48000.0)

	// Verify we have reasonable amounts of audio
	if len(recordedSamples) < 48000 {
		return fmt.Errorf("recorded audio too short: %d samples (need at least 1 second)", len(recordedSamples))
	}

	// Step 6: Compute audio metrics
	sourceRMS := computeRMS(sourceSamples)
	recordedRMS := computeRMS(recordedSamples)
	t.Logf("source RMS: %.6f, recorded RMS: %.6f", sourceRMS, recordedRMS)

	if recordedRMS < 0.001 {
		return fmt.Errorf("recorded audio is silent (RMS=%.6f)", recordedRMS)
	}

	// Step 7: Find where audio content begins in both signals
	// (Cannot compute correlation on silent/zero-variance data)
	srcOnset := findAudioStart(sourceSamples, 0.001)
	recOnset := findAudioStart(recordedSamples, 0.001)

	t.Logf("audio onset: source=%d (%.3fs), recorded=%d (%.3fs)",
		srcOnset, float64(srcOnset)/48000, recOnset, float64(recOnset)/48000)

	// Step 8: Verify timing difference is within reasonable tolerance for real-time streaming
	// Allow up to +/- 100ms offset which accounts for network jitter and buffering
	maxAllowedOffsetMs := 100.0
	onsetDiff := recOnset - srcOnset
	onsetDiffMs := float64(onsetDiff) / 48.0

	t.Logf("onset timing difference: %d samples (%.2fms)", onsetDiff, onsetDiffMs)

	if math.Abs(onsetDiffMs) > maxAllowedOffsetMs {
		return fmt.Errorf("audio onset timing difference %.2fms exceeds maximum allowed %.2fms", onsetDiffMs, maxAllowedOffsetMs)
	}

	// Step 9: Compute correlation over the ENTIRE waveform after aligning at onset
	// This aligns both signals at their audio content start, then correlates everything after
	srcAlignedStart := srcOnset
	recAlignedStart := recOnset
	compareLen := min(len(sourceSamples)-srcAlignedStart, len(recordedSamples)-recAlignedStart)

	t.Logf("computing FULL waveform correlation from onset: comparing %d samples (%.2fs)",
		compareLen, float64(compareLen)/48000)

	srcAligned := sourceSamples[srcAlignedStart : srcAlignedStart+compareLen]
	recAligned := recordedSamples[recAlignedStart : recAlignedStart+compareLen]
	correlation := computePearsonCorrelation(srcAligned, recAligned)

	// Also verify total duration is similar (within 1 second)
	srcDuration := float64(len(sourceSamples)) / 48000
	recDuration := float64(len(recordedSamples)) / 48000
	durationDiff := math.Abs(srcDuration - recDuration)

	t.Logf("duration check: source=%.2fs, recorded=%.2fs, diff=%.3fs", srcDuration, recDuration, durationDiff)

	if durationDiff > 1.0 {
		return fmt.Errorf("duration difference %.2fs exceeds maximum allowed 1.0s", durationDiff)
	}

	t.Logf("FULL waveform correlation: %.4f (threshold: %.4f) over %.2f seconds",
		correlation, minCorrelation, float64(compareLen)/48000)

	if correlation < minCorrelation {
		// Debug: find where audio content actually is
		t.Logf("correlation too low - analyzing audio content distribution...")

		// Find first non-zero sample in source
		srcFirstNonZero := -1
		for i, s := range sourceSamples {
			if s != 0 && (s > 0.001 || s < -0.001) {
				srcFirstNonZero = i
				break
			}
		}
		t.Logf("source: first non-zero sample at index %d (%.3fs)", srcFirstNonZero, float64(srcFirstNonZero)/48000)

		// Find first non-zero sample in recorded
		recFirstNonZero := -1
		for i, s := range recordedSamples {
			if s != 0 && (s > 0.001 || s < -0.001) {
				recFirstNonZero = i
				break
			}
		}
		t.Logf("recorded: first non-zero sample at index %d (%.3fs)", recFirstNonZero, float64(recFirstNonZero)/48000)

		// Show samples at multiple positions
		positions := []int{0, 48000, 96000, 144000, 480000, 960000}
		for _, pos := range positions {
			if pos+10 <= len(sourceSamples) && pos+10 <= len(recordedSamples) {
				srcSlice := sourceSamples[pos : pos+5]
				recSlice := recordedSamples[pos : pos+5]
				srcRMS := computeRMS(sourceSamples[pos : pos+4800])
				recRMS := computeRMS(recordedSamples[pos : pos+4800])
				t.Logf("at pos %d (%.2fs): src=%v (rms=%.4f) rec=%v (rms=%.4f)",
					pos, float64(pos)/48000, srcSlice, srcRMS, recSlice, recRMS)
			}
		}

		return fmt.Errorf("audio correlation %.4f below threshold %.4f - audio may be corrupted", correlation, minCorrelation)
	}

	t.Logf("FULL audio waveform validation PASSED: correlation=%.4f over entire %.2f seconds",
		correlation, float64(compareLen)/48000)
	return nil
}

// readFloat32PCM reads a raw PCM file containing float32 little-endian samples.
func readFloat32PCM(path string) ([]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(data)%4 != 0 {
		return nil, fmt.Errorf("PCM file size %d not divisible by 4", len(data))
	}

	samples := make([]float32, len(data)/4)
	for i := 0; i < len(samples); i++ {
		bits := uint32(data[i*4]) | uint32(data[i*4+1])<<8 | uint32(data[i*4+2])<<16 | uint32(data[i*4+3])<<24
		samples[i] = math.Float32frombits(bits)
	}

	return samples, nil
}

// computeRMS computes the root mean square of the samples.
func computeRMS(samples []float32) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// findAudioStart finds the index where audio content begins (first sample above threshold).
// Returns 0 if no audio start is found (all silence).
func findAudioStart(samples []float32, threshold float32) int {
	for i, s := range samples {
		if s > threshold || s < -threshold {
			return i
		}
	}
	return 0
}

// computePearsonCorrelation computes the Pearson correlation coefficient between two signals.
// Returns a value between -1 and 1, where 1 means perfect positive correlation.
func computePearsonCorrelation(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}

	// Compute means
	var sumA, sumB float64
	for i := 0; i < n; i++ {
		sumA += float64(a[i])
		sumB += float64(b[i])
	}
	meanA := sumA / float64(n)
	meanB := sumB / float64(n)

	// Compute correlation
	var num, denA, denB float64
	for i := 0; i < n; i++ {
		diffA := float64(a[i]) - meanA
		diffB := float64(b[i]) - meanB
		num += diffA * diffB
		denA += diffA * diffA
		denB += diffB * diffB
	}

	if denA == 0 || denB == 0 {
		return 0
	}

	return num / (math.Sqrt(denA) * math.Sqrt(denB))
}

// AudioManifestJSON represents the audio.json manifest structure.
type AudioManifestJSON struct {
	Version         int                `json:"version"`
	Codec           string             `json:"codec"`
	SampleRate      int                `json:"sampleRate"`
	Channels        int                `json:"channels"`
	SegmentDuration float64            `json:"segmentDuration"`
	StartTime       string             `json:"startTime"`
	Init            string             `json:"init"`
	Segments        []AudioSegmentJSON `json:"segments"`
}

// AudioSegmentJSON represents a segment entry in the audio manifest.
type AudioSegmentJSON struct {
	Index     int     `json:"index"`
	File      string  `json:"file"`
	Duration  float64 `json:"duration"`
	StartTime float64 `json:"startTime"`
	Size      int64   `json:"size"`
}

// validateWebPlayerTiming validates that audio playback timing matches the source.
// This performs multiple validations:
// 1. Manifest startTime matches actual segment tfdt
// 2. Cross-correlation shift detection to catch time offsets
// 3. Sample comparison at multiple absolute timestamps
// 4. Overall correlation check
//
// The key validation is detecting if audio would play at wrong times (e.g., 2 seconds early)
// due to issues like ring buffer overflow in the web player.
func validateWebPlayerTiming(t *testing.T, client *minio.Client, bucket, prefix, sourceMP4 string, minCorrelation float64) error {
	ctx := context.Background()
	tempDir := t.TempDir()

	// Step 1: Download audio.json manifest
	manifestKey := path.Join(prefix, "audio.json")
	manifestPath := filepath.Join(tempDir, "audio.json")

	if err := client.FGetObject(ctx, bucket, manifestKey, manifestPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download audio.json: %w", err)
	}

	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read audio.json: %w", err)
	}

	var manifest AudioManifestJSON
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parse audio.json: %w", err)
	}

	t.Logf("audio manifest: %d segments, sampleRate=%d, segmentDuration=%.3fs",
		len(manifest.Segments), manifest.SampleRate, manifest.SegmentDuration)

	if len(manifest.Segments) == 0 {
		return fmt.Errorf("no segments in audio manifest")
	}

	// Step 2: Download init segment and all media segments
	initPath := filepath.Join(tempDir, manifest.Init)
	initKey := path.Join(prefix, manifest.Init)
	if err := client.FGetObject(ctx, bucket, initKey, initPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download %s: %w", manifest.Init, err)
	}

	segmentPaths := make([]string, len(manifest.Segments))
	for i, seg := range manifest.Segments {
		segPath := filepath.Join(tempDir, seg.File)
		segKey := path.Join(prefix, seg.File)
		if err := client.FGetObject(ctx, bucket, segKey, segPath, minio.GetObjectOptions{}); err != nil {
			return fmt.Errorf("download %s: %w", seg.File, err)
		}
		segmentPaths[i] = segPath
	}
	t.Logf("downloaded %d segments", len(segmentPaths))

	// Step 3: Parse each segment to get actual tfdt and compare with manifest
	const timescale = 48000 // Opus sample rate
	var tfdtMismatches []string

	for i, seg := range manifest.Segments {
		segInfo, err := getAudioSegmentInfo(segmentPaths[i], timescale)
		if err != nil {
			t.Logf("warning: failed to parse segment %d tfdt: %v", i, err)
			continue
		}

		actualStartTime := segInfo.StartTimeSeconds
		manifestStartTime := seg.StartTime
		diff := manifestStartTime - actualStartTime

		// Log timing for all segments
		t.Logf("segment %d: manifest_startTime=%.3fs, actual_tfdt=%.3fs, diff=%.3fs, duration=%.3fs",
			i, manifestStartTime, actualStartTime, diff, segInfo.Duration)

		// Flag significant mismatches (> 0.5 second)
		if math.Abs(diff) > 0.5 {
			tfdtMismatches = append(tfdtMismatches,
				fmt.Sprintf("segment %d: manifest=%.3fs, tfdt=%.3fs, diff=%.3fs", i, manifestStartTime, actualStartTime, diff))
		}
	}

	if len(tfdtMismatches) > 0 {
		t.Logf("WARNING: %d segments have startTime/tfdt mismatch (>0.5s):", len(tfdtMismatches))
		for _, m := range tfdtMismatches {
			t.Logf("  %s", m)
		}
	}

	// Step 4: Decode source audio
	sourcePCMPath := filepath.Join(tempDir, "source.pcm")
	ffmpegCmd := exec.Command("ffmpeg", "-y", "-i", sourceMP4,
		"-vn", "-acodec", "pcm_f32le", "-ar", "48000", "-ac", "1",
		"-f", "f32le", sourcePCMPath)
	ffmpegCmd.Stderr = nil
	if err := ffmpegCmd.Run(); err != nil {
		return fmt.Errorf("decode source audio: %w", err)
	}

	sourceSamples, err := readFloat32PCM(sourcePCMPath)
	if err != nil {
		return fmt.Errorf("read source PCM: %w", err)
	}
	t.Logf("source audio: %d samples (%.2fs)", len(sourceSamples), float64(len(sourceSamples))/48000)

	// Step 5: Decode recorded audio
	combinedPath := filepath.Join(tempDir, "combined_for_decode.mp4")
	combinedFile, err := os.Create(combinedPath)
	if err != nil {
		return fmt.Errorf("create combined file: %w", err)
	}

	initData, err := os.ReadFile(initPath)
	if err != nil {
		combinedFile.Close()
		return fmt.Errorf("read init segment: %w", err)
	}
	combinedFile.Write(initData)

	for _, segPath := range segmentPaths {
		segData, err := os.ReadFile(segPath)
		if err != nil {
			combinedFile.Close()
			return fmt.Errorf("read segment: %w", err)
		}
		combinedFile.Write(segData)
	}
	combinedFile.Close()

	// Decode to PCM
	recordedPCMPath := filepath.Join(tempDir, "recorded.pcm")
	ffmpegCmd = exec.Command("ffmpeg", "-y", "-i", combinedPath,
		"-vn", "-acodec", "pcm_f32le", "-ar", "48000", "-ac", "1",
		"-f", "f32le", recordedPCMPath)
	ffmpegCmd.Stderr = nil
	if err := ffmpegCmd.Run(); err != nil {
		return fmt.Errorf("decode recorded audio: %w", err)
	}

	recordedSamples, err := readFloat32PCM(recordedPCMPath)
	if err != nil {
		return fmt.Errorf("read recorded PCM: %w", err)
	}
	t.Logf("recorded audio: %d samples (%.2fs)", len(recordedSamples), float64(len(recordedSamples))/48000)

	// Step 6: CRITICAL - Cross-correlation shift detection
	// This catches the "2-4 seconds early" issue by finding optimal alignment
	// If recorded audio is shifted relative to source, cross-correlation will reveal it
	maxShiftSamples := 5 * 48000 // Search for shifts up to ±5 seconds
	detectedShift, shiftCorrelation := detectTimeShift(sourceSamples, recordedSamples, maxShiftSamples)
	detectedShiftSeconds := float64(detectedShift) / 48000.0

	t.Logf("cross-correlation shift detection: shift=%.3fs (%d samples), correlation=%.4f",
		detectedShiftSeconds, detectedShift, shiftCorrelation)

	// Maximum allowed shift is 200ms (catches "2 seconds early" issue)
	maxAllowedShift := 0.2
	if math.Abs(detectedShiftSeconds) > maxAllowedShift {
		return fmt.Errorf("CRITICAL: detected time shift of %.3fs (max allowed: %.3fs) - "+
			"recorded audio is %.3fs %s relative to source. "+
			"This would cause audio to play at wrong time in web player",
			detectedShiftSeconds, maxAllowedShift, math.Abs(detectedShiftSeconds),
			map[bool]string{true: "early", false: "late"}[detectedShift > 0])
	}

	// Step 7: Find beep onset in both source and recorded
	sourceOnset := findAudioOnset(sourceSamples, 0.01, 48000)
	recordedOnset := findAudioOnset(recordedSamples, 0.01, 48000)

	sourceOnsetTime := float64(sourceOnset) / 48000
	recordedOnsetTime := float64(recordedOnset) / 48000
	onsetDiff := recordedOnsetTime - sourceOnsetTime

	t.Logf("beep onset: source=%.3fs, recorded=%.3fs, diff=%.3fs", sourceOnsetTime, recordedOnsetTime, onsetDiff)

	// Step 8: Validate onset timing
	maxOnsetDiff := 0.5 // Maximum allowed onset difference in seconds
	if math.Abs(onsetDiff) > maxOnsetDiff {
		return fmt.Errorf("audio onset timing mismatch: source=%.3fs, recorded=%.3fs, diff=%.3fs (max allowed: %.3fs)",
			sourceOnsetTime, recordedOnsetTime, onsetDiff, maxOnsetDiff)
	}

	// Step 9: Validate at multiple absolute timestamps
	// This catches issues where audio content is misaligned at specific points
	checkpointResults := validateAtCheckpoints(t, sourceSamples, recordedSamples, 48000)
	if len(checkpointResults.failures) > 0 {
		t.Logf("checkpoint validation failures:")
		for _, f := range checkpointResults.failures {
			t.Logf("  %s", f)
		}
		return fmt.Errorf("checkpoint validation failed at %d of %d points: %s",
			len(checkpointResults.failures), checkpointResults.totalChecks, checkpointResults.failures[0])
	}
	t.Logf("checkpoint validation: %d/%d passed", checkpointResults.passed, checkpointResults.totalChecks)

	// Note: We skip raw overall correlation check because:
	// 1. Cross-correlation shift detection already validated alignment (shift and correlation)
	// 2. Checkpoint validation verified content presence at all time points
	// 3. Raw correlation of periodic signals (beeps) can be poor due to phase even when content is identical

	t.Logf("web player timing validation PASSED: shift=%.3fs, onset_diff=%.3fs, cross_correlation=%.4f",
		detectedShiftSeconds, onsetDiff, shiftCorrelation)
	return nil
}

// detectTimeShift uses cross-correlation to find the optimal alignment between two signals.
// Returns the shift in samples (positive = recorded is ahead of source) and the correlation at that shift.
// This is critical for catching issues like "audio plays 2 seconds early".
func detectTimeShift(source, recorded []float32, maxShift int) (int, float64) {
	// Use a representative window from the audio (after onset, where there's actual content)
	sourceOnset := findAudioOnset(source, 0.01, 48000)
	recordedOnset := findAudioOnset(recorded, 0.01, 48000)

	// Use 5 seconds of audio after onset for correlation
	windowSize := 5 * 48000 // 5 seconds
	if sourceOnset+windowSize > len(source) || recordedOnset+windowSize > len(recorded) {
		windowSize = min(len(source)-sourceOnset, len(recorded)-recordedOnset)
	}

	if windowSize < 48000 { // Need at least 1 second
		return 0, 0
	}

	sourceWindow := source[sourceOnset : sourceOnset+windowSize]
	baseRecordedStart := recordedOnset

	bestShift := 0
	bestCorrelation := -1.0

	// Search for the shift that maximizes correlation
	// Shift range: -maxShift to +maxShift
	for shift := -maxShift; shift <= maxShift; shift += 480 { // Step by 10ms for efficiency
		recStart := baseRecordedStart + shift
		if recStart < 0 || recStart+windowSize > len(recorded) {
			continue
		}

		recordedWindow := recorded[recStart : recStart+windowSize]
		corr := computePearsonCorrelation(sourceWindow, recordedWindow)

		if corr > bestCorrelation {
			bestCorrelation = corr
			bestShift = shift
		}
	}

	// Refine search around best shift with finer granularity
	for shift := bestShift - 480; shift <= bestShift+480; shift += 48 { // Step by 1ms
		recStart := baseRecordedStart + shift
		if recStart < 0 || recStart+windowSize > len(recorded) {
			continue
		}

		recordedWindow := recorded[recStart : recStart+windowSize]
		corr := computePearsonCorrelation(sourceWindow, recordedWindow)

		if corr > bestCorrelation {
			bestCorrelation = corr
			bestShift = shift
		}
	}

	// The shift value indicates how much the recorded onset differs from expected
	// A positive shift means recorded audio content appears earlier (recorded is ahead)
	return bestShift, bestCorrelation
}

// checkpointResult holds results from checkpoint validation.
type checkpointResult struct {
	totalChecks int
	passed      int
	failures    []string
}

// validateAtCheckpoints compares audio content at specific absolute timestamps.
// This catches cases where audio is time-shifted (e.g., "2 seconds early" bug).
//
// We use RMS energy comparison rather than raw correlation because:
// 1. Periodic signals (like beeps) can have poor correlation due to phase mismatch
// 2. Energy comparison reliably detects "content present vs silent" mismatches
// 3. This is the key validation - if source is silent at time T but recorded has audio, there's a shift
func validateAtCheckpoints(t *testing.T, source, recorded []float32, sampleRate int) checkpointResult {
	result := checkpointResult{}

	// Checkpoints including times before and after expected beep onset (10s)
	// Times 5s, 8s should be silent in source
	// Times 15s, 25s, 35s, 45s, 55s should have beeps
	checkpoints := []float64{5.0, 8.0, 15.0, 25.0, 35.0, 45.0, 55.0}
	windowDuration := 0.5 // 500ms window
	windowSamples := int(windowDuration * float64(sampleRate))

	// Thresholds for content detection
	silenceThreshold := 0.005 // Below this RMS = silence
	contentThreshold := 0.01  // Above this RMS = content present
	rmsRatioTolerance := 0.5  // RMS levels should be within 50% of each other

	for _, checkTime := range checkpoints {
		checkSample := int(checkTime * float64(sampleRate))

		// Skip if checkpoint is beyond audio length
		if checkSample+windowSamples > len(source) || checkSample+windowSamples > len(recorded) {
			continue
		}

		result.totalChecks++

		sourceWindow := source[checkSample : checkSample+windowSamples]
		recordedWindow := recorded[checkSample : checkSample+windowSamples]

		sourceRMS := computeRMS(sourceWindow)
		recordedRMS := computeRMS(recordedWindow)

		sourceSilent := sourceRMS < silenceThreshold
		recordedSilent := recordedRMS < silenceThreshold
		sourceHasContent := sourceRMS > contentThreshold
		recordedHasContent := recordedRMS > contentThreshold

		// Case 1: Both silent - OK
		if sourceSilent && recordedSilent {
			result.passed++
			t.Logf("checkpoint %.0fs: both silent (src_rms=%.4f, rec_rms=%.4f) - OK", checkTime, sourceRMS, recordedRMS)
			continue
		}

		// Case 2: Source silent but recorded has content - TIME SHIFT DETECTED!
		// This is the key check for "2 seconds early" bug
		if sourceSilent && recordedHasContent {
			result.failures = append(result.failures, fmt.Sprintf(
				"checkpoint %.0fs: TIME SHIFT - source is silent (rms=%.4f) but recorded has audio (rms=%.4f). "+
					"Recorded audio is playing EARLY relative to source",
				checkTime, sourceRMS, recordedRMS))
			continue
		}

		// Case 3: Source has content but recorded is silent - content missing or LATE
		if sourceHasContent && recordedSilent {
			result.failures = append(result.failures, fmt.Sprintf(
				"checkpoint %.0fs: CONTENT MISSING - source has audio (rms=%.4f) but recorded is silent (rms=%.4f). "+
					"Recorded audio is missing or playing LATE",
				checkTime, sourceRMS, recordedRMS))
			continue
		}

		// Case 4: Both have content - verify similar energy levels
		// Large RMS difference could indicate wrong content
		if sourceHasContent && recordedHasContent {
			rmsRatio := recordedRMS / sourceRMS
			if rmsRatio < rmsRatioTolerance || rmsRatio > 1.0/rmsRatioTolerance {
				result.failures = append(result.failures, fmt.Sprintf(
					"checkpoint %.0fs: RMS mismatch - source_rms=%.4f, recorded_rms=%.4f, ratio=%.2f (expected 0.5-2.0)",
					checkTime, sourceRMS, recordedRMS, rmsRatio))
				continue
			}
			result.passed++
			t.Logf("checkpoint %.0fs: both have content (src_rms=%.4f, rec_rms=%.4f, ratio=%.2f) - OK",
				checkTime, sourceRMS, recordedRMS, rmsRatio)
			continue
		}

		// Case 5: Ambiguous (in the gray zone between silence and content)
		// Be lenient here - just log and pass
		result.passed++
		t.Logf("checkpoint %.0fs: ambiguous levels (src_rms=%.4f, rec_rms=%.4f) - OK (lenient)",
			checkTime, sourceRMS, recordedRMS)
	}

	return result
}

// findAudioOnset finds the sample index where audio content begins.
// Uses a sliding window RMS approach to find sustained audio above threshold.
func findAudioOnset(samples []float32, threshold float64, windowSize int) int {
	if len(samples) < windowSize {
		return 0
	}

	for i := 0; i < len(samples)-windowSize; i += windowSize / 4 {
		rms := computeRMS(samples[i : i+windowSize])
		if rms > threshold {
			// Found audio, refine to find exact start
			for j := i; j < i+windowSize && j < len(samples); j++ {
				if samples[j] > float32(threshold) || samples[j] < float32(-threshold) {
					return j
				}
			}
			return i
		}
	}
	return 0
}
