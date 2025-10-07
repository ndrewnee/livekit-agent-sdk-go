// +build e2e

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const (
	testMP4Path     = "../../examples/egress-agent/test-data/test.mp4"
	testOutputPath  = "output.h264"
)

// TestSaveToH264 tests the complete H.264 recording workflow
// This test creates a WebRTC publisher and receiver, connects them,
// publishes test.mp4, and validates the H.264 output
func TestSaveToH264(t *testing.T) {
	// Clean up
	os.Remove(testOutputPath)
	// Keep output file for inspection
	// defer os.Remove(testOutputPath)

	t.Log("=== Step 1: Verify test.mp4 exists ===")
	if _, err := os.Stat(testMP4Path); err != nil {
		t.Fatalf("Test video not found: %v", err)
	}
	t.Log("✓ Test video exists")

	t.Log("\n=== Step 2: Create H.264 writer ===")
	h264File, err := newH264Saver(testOutputPath)
	if err != nil {
		t.Fatalf("Failed to create H.264 writer: %v", err)
	}
	defer h264File.Close()
	t.Log("✓ H.264 writer created")

	t.Log("\n=== Step 3: Create WebRTC peer connections ===")
	publisher, receiver, err := createPeerConnections(t, h264File)
	if err != nil {
		t.Fatalf("Failed to create peer connections: %v", err)
	}
	defer publisher.Close()
	defer receiver.Close()
	t.Log("✓ Peer connections created")

	t.Log("\n=== Step 4: Create and add video track ===")
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		"video",
		"pion")
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	if _, err = publisher.AddTrack(videoTrack); err != nil {
		t.Fatalf("Failed to add track: %v", err)
	}
	t.Log("✓ Video track added to publisher")

	t.Log("\n=== Step 5: Exchange offers and establish connection ===")
	if err := exchangeSDPAndConnect(t, publisher, receiver); err != nil {
		t.Fatalf("Failed to establish connection: %v", err)
	}
	t.Log("✓ WebRTC connection established")

	t.Log("\n=== Step 6: Publish video from test.mp4 ===")
	// Publish in background
	done := make(chan bool)
	go func() {
		publishVideoFromMP4(t, videoTrack, testMP4Path)
		done <- true
	}()

	t.Log("✓ Publishing video...")

	t.Log("\n=== Step 7: Record full video (or timeout after 30s) ===")
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-timeout:
			t.Log("  → Recording timeout reached")
			break loop
		case <-done:
			t.Log("  → Publishing complete, waiting for final packets...")
			time.Sleep(1 * time.Second)
			break loop
		case <-ticker.C:
			t.Log("  → Recording in progress...")
		}
	}

	// Wait for publishing to finish
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	t.Log("\n=== Step 8: Finalize H.264 output ===")
	h264File.Close()
	t.Log("✓ H.264 output finalized")

	t.Log("\n=== Step 9: Validate H.264 file ===")
	if err := validateH264(t, testOutputPath); err != nil {
		t.Fatalf("H.264 validation failed: %v", err)
	}
	t.Log("✓ H.264 file is valid")

	t.Log("\n✅ All tests passed!")
}

// createPeerConnections creates publisher and receiver peer connections
func createPeerConnections(t *testing.T, h264File *h264Saver) (*webrtc.PeerConnection, *webrtc.PeerConnection, error) {
	// Create media engine
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		PayloadType:        102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, nil, err
	}

	// Create SettingEngine for local connection
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetICETimeouts(5*time.Second, 5*time.Second, 2*time.Second)

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithSettingEngine(settingEngine),
	)

	// Configuration for local testing
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	}

	// Create publisher
	publisher, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, nil, err
	}

	// Create receiver
	receiver, err := api.NewPeerConnection(config)
	if err != nil {
		publisher.Close()
		return nil, nil, err
	}

	// Setup receiver to forward to H.264 writer
	receiver.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		t.Logf("  → Track received: %s", track.Codec().MimeType)

		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}

			if track.Codec().MimeType == webrtc.MimeTypeH264 {
				if err := h264File.WriteRTP(pkt); err != nil {
					t.Logf("  → Error writing H.264: %v", err)
				}
			}
		}
	})

	return publisher, receiver, nil
}

// exchangeSDPAndConnect exchanges SDP and establishes connection
func exchangeSDPAndConnect(t *testing.T, publisher, receiver *webrtc.PeerConnection) error {
	// Setup ICE candidate exchange
	publisherCandidates := make(chan *webrtc.ICECandidate, 10)
	receiverCandidates := make(chan *webrtc.ICECandidate, 10)

	publisher.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			publisherCandidates <- c
		}
	})

	receiver.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			receiverCandidates <- c
		}
	})

	// Wait for connection
	connected := make(chan bool, 1)
	receiver.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		t.Logf("  → ICE connection state: %s", state)
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			connected <- true
		}
	})

	// Create offer from publisher
	offer, err := publisher.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("failed to create offer: %w", err)
	}

	if err := publisher.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("failed to set local description: %w", err)
	}

	// Set offer on receiver
	if err := receiver.SetRemoteDescription(offer); err != nil {
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	// Create answer from receiver
	answer, err := receiver.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("failed to create answer: %w", err)
	}

	if err := receiver.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("failed to set local description: %w", err)
	}

	// Set answer on publisher
	if err := publisher.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	// Exchange ICE candidates
	go func() {
		for c := range publisherCandidates {
			if err := receiver.AddICECandidate(c.ToJSON()); err != nil {
				t.Logf("  → Error adding publisher ICE candidate: %v", err)
			}
		}
	}()

	go func() {
		for c := range receiverCandidates {
			if err := publisher.AddICECandidate(c.ToJSON()); err != nil {
				t.Logf("  → Error adding receiver ICE candidate: %v", err)
			}
		}
	}()

	select {
	case <-connected:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("connection timeout")
	}
}

// publishVideoFromMP4 publishes video from MP4 file
func publishVideoFromMP4(t *testing.T, track *webrtc.TrackLocalStaticSample, mp4Path string) {
	// Extract H.264
	h264File := "/tmp/mp4-test-video.h264"
	cmd := exec.Command("ffmpeg", "-y", "-i", mp4Path,
		"-an", "-vcodec", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "h264", h264File)
	if err := cmd.Run(); err != nil {
		t.Logf("Failed to extract H.264: %v", err)
		return
	}
	defer os.Remove(h264File)

	data, err := os.ReadFile(h264File)
	if err != nil {
		t.Logf("Failed to read H.264: %v", err)
		return
	}

	nalUnits := parseH264(data)
	var aggregatedSamples [][]byte
	var currentGroup []byte

	for _, nal := range nalUnits {
		nalType := nal[0] & 0x1F
		isParameterSet := nalType == 7 || nalType == 8
		isIDR := nalType == 5

		if isParameterSet {
			// Append parameter sets to current group (don't replace!)
			currentGroup = append(currentGroup, 0x00, 0x00, 0x00, 0x01)
			currentGroup = append(currentGroup, nal...)
		} else if isIDR && len(currentGroup) > 0 {
			currentGroup = append(currentGroup, 0x00, 0x00, 0x00, 0x01)
			currentGroup = append(currentGroup, nal...)
			aggregatedSamples = append(aggregatedSamples, currentGroup)
			currentGroup = nil
		} else if isIDR {
			sample := append([]byte{0x00, 0x00, 0x00, 0x01}, nal...)
			aggregatedSamples = append(aggregatedSamples, sample)
		} else {
			sample := append([]byte{0x00, 0x00, 0x00, 0x01}, nal...)
			aggregatedSamples = append(aggregatedSamples, sample)
		}
	}

	for _, sample := range aggregatedSamples {
		if err := track.WriteSample(media.Sample{
			Data:     sample,
			Duration: 33 * time.Millisecond,
		}); err != nil {
			t.Logf("Error writing sample: %v", err)
			return
		}
		// Publish as fast as possible for testing
		// Remove sleep to avoid waiting 3+ minutes for full video
	}
}

// parseH264 parses H.264 Annex B into NAL units
func parseH264(data []byte) [][]byte {
	var nalUnits [][]byte
	offset := 0

	for offset < len(data) {
		startCodeLen := 0
		if offset+3 <= len(data) && data[offset] == 0x00 && data[offset+1] == 0x00 && data[offset+2] == 0x01 {
			startCodeLen = 3
		} else if offset+4 <= len(data) && data[offset] == 0x00 && data[offset+1] == 0x00 && data[offset+2] == 0x00 && data[offset+3] == 0x01 {
			startCodeLen = 4
		} else {
			offset++
			continue
		}

		nalStart := offset + startCodeLen
		if nalStart >= len(data) {
			break
		}

		nalEnd := len(data)
		for i := nalStart + 1; i < len(data)-3; i++ {
			if data[i] == 0x00 && data[i+1] == 0x00 {
				if (i+2 < len(data) && data[i+2] == 0x01) ||
					(i+3 < len(data) && data[i+2] == 0x00 && data[i+3] == 0x01) {
					nalEnd = i
					break
				}
			}
		}

		nalUnit := make([]byte, nalEnd-nalStart)
		copy(nalUnit, data[nalStart:nalEnd])
		nalUnits = append(nalUnits, nalUnit)

		offset = nalEnd
	}

	return nalUnits
}

// validateH264 validates the H.264 file
func validateH264(t *testing.T, h264Path string) error {
	// Check file exists
	info, err := os.Stat(h264Path)
	if err != nil {
		return fmt.Errorf("file not found: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("file is empty")
	}
	t.Logf("  → File size: %.2f MB", float64(info.Size())/1024/1024)

	// Use ffprobe to validate
	cmd := exec.Command("ffprobe", "-v", "error", "-show_format", "-show_streams", "-print_format", "json", h264Path)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe failed: %w", err)
	}

	var result struct {
		Format struct {
			Duration  string `json:"duration"`
			NBStreams int    `json:"nb_streams"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	if result.Format.NBStreams == 0 {
		return fmt.Errorf("no streams detected")
	}

	t.Logf("  → Streams: %d", result.Format.NBStreams)

	// Check for video stream
	hasVideo := false
	for _, stream := range result.Streams {
		if stream.CodecType == "video" {
			hasVideo = true
			t.Logf("  → Video: %s %dx%d", stream.CodecName, stream.Width, stream.Height)
		}
	}

	if !hasVideo {
		return fmt.Errorf("no video stream found")
	}

	if result.Format.Duration != "" {
		duration, _ := strconv.ParseFloat(result.Format.Duration, 64)
		t.Logf("  → Duration: %.2f seconds", duration)

		// Note: May be less than source video (200s) if timeout is reached
		if duration < 5 {
			return fmt.Errorf("duration too short: %.2f seconds", duration)
		}
	}

	return nil
}
