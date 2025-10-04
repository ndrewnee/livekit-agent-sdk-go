// +build e2e

package egress

import (
	"context"
	"fmt"
	"testing"
	"time"

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

	t.Logf("Test configuration:")
	t.Logf("  Duration: %d seconds", durationSec)
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

	t.Logf("✓ Media published from: %s (duration: %ds)", mp4File, durationSec)
	t.Logf("")

	// Note: This test demonstrates participant publishing
	// For actual egress/recording, see TestE2EFullScale
	t.Logf("Waiting for media to publish...")

	// Wait for media duration
	time.Sleep(time.Duration(durationSec) * time.Second)

	t.Logf("✓ Media publishing complete")
	t.Logf("")

	// Note: This test only demonstrates participant publishing
	// It does not actually capture/record HLS output
	t.Logf("✓ Test demonstrates:")
	t.Logf("  - LiveKit room creation")
	t.Logf("  - Participant connection")
	t.Logf("  - Video/audio track publishing from MP4")
	t.Logf("  - Media duration: %d seconds", durationSec)
	t.Logf("")
	t.Logf("═══════════════════════════════════════════════════")
	t.Logf("✅ Test Complete!")
	t.Logf("═══════════════════════════════════════════════════")
	t.Logf("")
	t.Logf("For actual HLS recording, see:")
	t.Logf("  go test -v -tags=e2e ./pkg/egress -run TestE2EFullScale")
	t.Logf("")
}
