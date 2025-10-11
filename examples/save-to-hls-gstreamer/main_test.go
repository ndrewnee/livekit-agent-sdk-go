package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestHLSRecorderGStreamer(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping e2e test in short mode")
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	t.Log("=== Step 1: Verify test video exists ===")
	testVideo := "../../examples/egress-agent/test-data/test.mp4"
	if _, err := os.Stat(testVideo); os.IsNotExist(err) {
		t.Fatalf("Test video not found: %s", testVideo)
	}
	t.Log("✓ Test video exists")

	t.Log("\n=== Step 2: Create HLS recorder ===")
	outputDir := "hls-gst-output"
	os.RemoveAll(outputDir) // Clean up from previous runs

	recorder, err := NewHLSRecorder(outputDir)
	if err != nil {
		t.Fatalf("Failed to create recorder: %v", err)
	}
	t.Log("✓ HLS recorder created")

	t.Log("\n=== Step 3: Initialize GStreamer pipeline ===")
	if err := recorder.initGStreamer(); err != nil {
		t.Fatalf("Failed to initialize GStreamer: %v", err)
	}
	t.Log("✓ GStreamer pipeline initialized")

	t.Log("\n=== Step 4: Set up WebRTC ===")
	if err := recorder.setupWebRTC(); err != nil {
		t.Fatalf("Failed to setup WebRTC: %v", err)
	}
	t.Log("✓ WebRTC setup complete")

	t.Log("\n=== Step 5: Create publisher peer connection ===")
	publisher, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}
	defer publisher.Close()

	// Track ICE connection state
	iceConnected := make(chan struct{})
	var iceOnce sync.Once
	publisher.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		t.Logf("  → Publisher ICE state: %s", state.String())
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			iceOnce.Do(func() {
				close(iceConnected)
			})
		}
	})

	// Also track recorder ICE state
	recorder.peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		t.Logf("  → Recorder ICE state: %s", state.String())
	})

	// Exchange ICE candidates between publisher and recorder
	// Publisher -> Recorder
	publisher.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			t.Logf("  → Publisher ICE candidate: %s", candidate.String())
			if err := recorder.peerConnection.AddICECandidate(candidate.ToJSON()); err != nil {
				t.Logf("  → Failed to add ICE candidate to recorder: %v", err)
			}
		}
	})

	// Recorder -> Publisher
	recorder.peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			t.Logf("  → Recorder ICE candidate: %s", candidate.String())
			if err := publisher.AddICECandidate(candidate.ToJSON()); err != nil {
				t.Logf("  → Failed to add ICE candidate to publisher: %v", err)
			}
		}
	})

	t.Log("\n=== Step 6: Add video and audio tracks ===")

	// Create H.264 video track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		"video",
		"pion-video",
	)
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	if _, err = publisher.AddTrack(videoTrack); err != nil {
		t.Fatalf("Failed to add video track: %v", err)
	}
	t.Log("✓ Video track added")

	// Create Opus audio track
	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000},
		"audio",
		"pion-audio",
	)
	if err != nil {
		t.Fatalf("Failed to create audio track: %v", err)
	}

	if _, err = publisher.AddTrack(audioTrack); err != nil {
		t.Fatalf("Failed to add audio track: %v", err)
	}
	t.Log("✓ Audio track added")

	t.Log("\n=== Step 7: Exchange SDP ===")

	// Create offer
	offer, err := publisher.CreateOffer(nil)
	if err != nil {
		t.Fatalf("Failed to create offer: %v", err)
	}

	if err = publisher.SetLocalDescription(offer); err != nil {
		t.Fatalf("Failed to set local description: %v", err)
	}

	// Set offer on recorder
	if err = recorder.peerConnection.SetRemoteDescription(offer); err != nil {
		t.Fatalf("Failed to set remote description on recorder: %v", err)
	}

	// Create answer
	answer, err := recorder.peerConnection.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("Failed to create answer: %v", err)
	}

	if err = recorder.peerConnection.SetLocalDescription(answer); err != nil {
		t.Fatalf("Failed to set local description on recorder: %v", err)
	}

	// Set answer on publisher
	if err = publisher.SetRemoteDescription(*recorder.peerConnection.LocalDescription()); err != nil {
		t.Fatalf("Failed to set remote description on publisher: %v", err)
	}

	t.Log("✓ SDP exchange complete")

	t.Log("\n=== Step 8: Start GStreamer pipeline ===")
	if err := recorder.Start(); err != nil {
		t.Fatalf("Failed to start recorder: %v", err)
	}
	t.Log("✓ Pipeline started")

	t.Log("\n=== Step 9: Wait for ICE connection ===")
	select {
	case <-iceConnected:
		t.Log("✓ ICE connected")
	case <-time.After(10 * time.Second):
		t.Fatal("ICE connection timeout")
	}

	t.Log("\n=== Step 10: Create GStreamer publisher ===")

	gstPublisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("Failed to create GStreamer publisher: %v", err)
	}
	t.Log("✓ GStreamer publisher created")

	t.Log("\n=== Step 11: Start publishing ===")
	if err := gstPublisher.Start(); err != nil {
		t.Fatalf("Failed to start publisher: %v", err)
	}
	t.Log("✓ Publisher started")

	t.Log("\n=== Step 12: Wait for publishing to complete ===")
	if err := gstPublisher.Wait(); err != nil {
		t.Fatalf("Publisher error: %v", err)
	}
	t.Log("✓ Publishing complete")

	t.Log("\n=== Step 13: Allow time for segments to finalize ===")
	time.Sleep(5 * time.Second)

	t.Log("\n=== Step 14: Stop publisher and recorder ===")
	gstPublisher.Stop()
	recorder.Stop()
	t.Log("  → Waiting for GStreamer pipeline to flush...")
	time.Sleep(10 * time.Second) // Give pipeline time to flush all buffered data
	t.Log("✓ Recorder stopped")

	t.Log("\n=== Step 15: Validate HLS output ===")
	if err := validateHLSOutput(t, outputDir); err != nil {
		t.Fatalf("HLS validation failed: %v", err)
	}
	t.Log("✓ HLS output is valid")

	t.Log("\n✅ All tests passed!")
}

func validateHLSOutput(t *testing.T, outputDir string) error {
	// Check if the output.ts file exists
	outputFile := fmt.Sprintf("%s/output.ts", outputDir)
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

	// Try to get duration and validate with ffprobe
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

	// Create HLS segments from the output.ts file using ffmpeg
	t.Log("  → Creating HLS segments from output.ts...")
	playlistPath := fmt.Sprintf("%s/playlist.m3u8", outputDir)
	segmentPattern := fmt.Sprintf("%s/segment_%%05d.ts", outputDir)

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
	segments, err := os.ReadDir(outputDir)
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
