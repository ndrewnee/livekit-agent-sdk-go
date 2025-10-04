// +build e2e

package egress

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// connectParticipant connects a participant to a LiveKit room
func connectParticipant(lkURL, apiKey, apiSecret, roomName, identity string) (*lksdk.Room, error) {
	room, err := lksdk.ConnectToRoom(lkURL, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: identity,
	}, &lksdk.RoomCallback{})
	return room, err
}

// publishFromMP4File publishes both video and audio tracks from a single MP4 file
// This is useful for testing with real media files that have both audio and video
func publishFromMP4File(t *testing.T, room *lksdk.Room, mp4File string, durationSeconds int) error {
	// Check file exists
	if _, err := os.Stat(mp4File); err != nil {
		return fmt.Errorf("MP4 file not found: %w", err)
	}

	t.Logf("Publishing from MP4: %s (duration: %d seconds)", mp4File, durationSeconds)

	// Extract H.264 video stream
	h264File := filepath.Join(os.TempDir(), fmt.Sprintf("video-%d.h264", time.Now().Unix()))
	t.Logf("Extracting H.264 video...")
	cmd := exec.Command("ffmpeg",
		"-i", mp4File,
		"-t", fmt.Sprintf("%d", durationSeconds), // Limit duration
		"-c:v", "copy",
		"-bsf:v", "h264_mp4toannexb",
		"-f", "h264",
		h264File,
		"-y")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Logf("ffmpeg output: %s", string(output))
		return fmt.Errorf("video extraction failed: %w", err)
	}

	// Extract Opus audio stream
	opusFile := filepath.Join(os.TempDir(), fmt.Sprintf("audio-%d.ogg", time.Now().Unix()))
	t.Logf("Extracting Opus audio...")
	cmd = exec.Command("ffmpeg",
		"-i", mp4File,
		"-t", fmt.Sprintf("%d", durationSeconds), // Limit duration
		"-vn",           // No video
		"-c:a", "libopus", // Encode to Opus
		"-b:a", "128k",  // Bitrate
		"-f", "ogg",
		opusFile,
		"-y")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Logf("ffmpeg output: %s", string(output))
		return fmt.Errorf("audio extraction failed: %w", err)
	}

	t.Logf("Extracted video: %s", h264File)
	t.Logf("Extracted audio: %s", opusFile)

	// Publish video track
	t.Logf("Publishing video track...")
	videoTrack, err := lksdk.NewLocalFileTrack(h264File,
		lksdk.ReaderTrackWithFrameDuration(33*time.Millisecond), // ~30fps
	)
	if err != nil {
		return fmt.Errorf("failed to create video track: %w", err)
	}

	videoPub, err := room.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "camera",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		return fmt.Errorf("failed to publish video: %w", err)
	}
	t.Logf("✓ Video track published: %s", videoPub.SID())

	// Publish audio track
	t.Logf("Publishing audio track...")
	audioTrack, err := lksdk.NewLocalFileTrack(opusFile,
		lksdk.ReaderTrackWithFrameDuration(20*time.Millisecond), // 20ms Opus frames
	)
	if err != nil {
		return fmt.Errorf("failed to create audio track: %w", err)
	}

	audioPub, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name:   "microphone",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		return fmt.Errorf("failed to publish audio: %w", err)
	}
	t.Logf("✓ Audio track published: %s", audioPub.SID())

	return nil
}
