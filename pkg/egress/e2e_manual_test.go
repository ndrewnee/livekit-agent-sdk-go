// +build e2e

package egress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/require"
)

// TestE2EManualVerification is designed for manual quality verification
// Uses real media file (test.mp4) and generates longer HLS output
// Output is saved to /tmp/hls-manual-test for inspection
func TestE2EManualVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping manual E2E test in short mode")
	}

	// Configuration
	lkURL := getEnvOrDefault("LIVEKIT_URL", "ws://localhost:7880")
	lkAPIKey := getEnvOrDefault("LIVEKIT_API_KEY", "devkey")
	lkAPISecret := getEnvOrDefault("LIVEKIT_API_SECRET", "secret")
	outputDir := getEnvOrDefault("TEST_OUTPUT_DIR", "/tmp/hls-manual-test")
	durationSec := 10 // Capture 10 seconds for good quality assessment

	t.Logf("═══════════════════════════════════════════════════")
	t.Logf("Manual E2E Verification Test")
	t.Logf("═══════════════════════════════════════════════════")
	t.Logf("LiveKit URL: %s", lkURL)
	t.Logf("Output: %s", outputDir)
	t.Logf("Duration: %d seconds", durationSec)
	t.Logf("")

	// Create output directory
	sessionID := fmt.Sprintf("manual-test-%d", time.Now().Unix())
	sessionDir := filepath.Join(outputDir, sessionID)
	err := os.MkdirAll(sessionDir, 0755)
	require.NoError(t, err, "Failed to create output directory")

	t.Logf("Session: %s", sessionID)
	t.Logf("Output directory: %s", sessionDir)
	t.Logf("")

	ctx := context.Background()

	// Create unique room
	roomName := fmt.Sprintf("manual-test-%d", time.Now().Unix())
	t.Logf("Creating LiveKit room: %s", roomName)

	roomClient := lksdk.NewRoomServiceClient(lkURL, lkAPIKey, lkAPISecret)
	room, err := roomClient.CreateRoom(ctx, &livekit.CreateRoomRequest{
		Name: roomName,
	})
	require.NoError(t, err, "Failed to create room")
	defer roomClient.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: roomName})

	t.Logf("✓ Room created: %s (SID: %s)", room.Name, room.Sid)
	t.Logf("")

	// Connect participant and publish tracks from test.mp4
	t.Logf("Connecting participant and publishing media...")
	participant, err := connectParticipant(lkURL, lkAPIKey, lkAPISecret, roomName, "publisher")
	require.NoError(t, err, "Failed to connect participant")
	defer participant.Disconnect()

	t.Logf("✓ Participant connected: %s", participant.LocalParticipant.Identity())

	// Publish from test.mp4
	mp4File := "../../examples/egress-agent/test-data/test.mp4"
	err = publishFromMP4File(t, participant, mp4File, durationSec)
	require.NoError(t, err, "Failed to publish tracks")

	t.Logf("✓ Media published from: %s", mp4File)
	t.Logf("")

	// Wait for tracks to be ready
	time.Sleep(2 * time.Second)

	// Start egress agent to capture the room
	t.Logf("Starting egress agent...")

	// Create egress configuration
	config := &Config{
		MaxConcurrentSessions: 1,
		VideoQuality:          livekit.VideoQuality_HIGH,
		PipelineConfig: PipelineConfig{
			OutputDir:          sessionDir,
			SegmentDuration:    4,
			JitterBufferMs:     200,
			StateChangeTimeout: 10 * time.Second,
			IsLiveSource:       true,
		},
		StorageConfig: StorageConfig{
			Type:      "local",
			LocalPath: sessionDir,
		},
		RecordingConfig: RecordingConfig{
			AutoStart:       true,
			MinParticipants: 1,
			RecordVideo:     true,
			RecordAudio:     true,
		},
	}

	// Create egress worker
	worker := agent.NewWorker(lkURL, lkAPIKey, lkAPISecret, config)
	handler := NewHandler(config)

	// Register handler
	worker.RegisterHandler(handler)

	// Start worker
	go func() {
		if err := worker.Start(ctx); err != nil {
			t.Logf("Worker error: %v", err)
		}
	}()
	defer worker.Stop()

	t.Logf("✓ Egress agent started")
	t.Logf("")

	// Wait for capture duration + buffer
	captureDuration := time.Duration(durationSec+5) * time.Second
	t.Logf("Capturing for %v...", captureDuration)

	time.Sleep(captureDuration)

	t.Logf("✓ Capture complete")
	t.Logf("")

	// Verify HLS output
	t.Logf("Verifying HLS output...")

	files, err := os.ReadDir(sessionDir)
	require.NoError(t, err, "Failed to read output directory")

	var playlistFound bool
	var segmentCount int

	for _, file := range files {
		if file.IsDir() {
			// Check subdirectories for session output
			subDir := filepath.Join(sessionDir, file.Name())
			subFiles, _ := os.ReadDir(subDir)
			for _, subFile := range subFiles {
				ext := filepath.Ext(subFile.Name())
				if ext == ".m3u8" {
					playlistFound = true
				} else if ext == ".ts" {
					segmentCount++
				}
			}
		} else {
			ext := filepath.Ext(file.Name())
			if ext == ".m3u8" {
				playlistFound = true
			} else if ext == ".ts" {
				segmentCount++
			}
		}
	}

	require.True(t, playlistFound, "HLS playlist not found")
	require.Greater(t, segmentCount, 0, "No HLS segments found")

	t.Logf("✓ HLS output verified:")
	t.Logf("  - Playlist: found")
	t.Logf("  - Segments: %d", segmentCount)
	t.Logf("")
	t.Logf("═══════════════════════════════════════════════════")
	t.Logf("✅ Test Complete!")
	t.Logf("═══════════════════════════════════════════════════")
	t.Logf("")
	t.Logf("Output location: %s", sessionDir)
	t.Logf("")
	t.Logf("To play:")
	t.Logf("1. Start player: ./tools/hls-player/play.sh")
	t.Logf("2. Open: http://localhost:8080/tools/hls-player/player.html")
	t.Logf("3. Load: %s/playlist.m3u8", sessionDir)
	t.Logf("")
}
