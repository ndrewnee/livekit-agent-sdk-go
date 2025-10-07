//go:build e2e
// +build e2e

package egress

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
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
	t.Logf("Extracted video: %s", h264File)

	// Publish video track using AGGREGATED NALs (SPS+PPS+IDR together)
	// This fixes the LiveKit Server forwarding issue where separate NALs cause blank packets
	t.Logf("Publishing video track with aggregated NALs...")
	videoPub, err := publishH264WithAggregatedNALs(t, h264File, room, durationSeconds)
	if err != nil {
		return fmt.Errorf("failed to publish video: %w", err)
	}
	t.Logf("✓ Video track published: %s", videoPub.SID())

	// Extract and publish Opus audio stream
	opusFile := filepath.Join(os.TempDir(), fmt.Sprintf("audio-%d.ogg", time.Now().Unix()))
	t.Logf("Extracting Opus audio...")
	cmd = exec.Command("ffmpeg",
		"-i", mp4File,
		"-t", fmt.Sprintf("%d", durationSeconds), // Limit duration
		"-vn",             // No video
		"-c:a", "libopus", // Encode to Opus
		"-b:a", "128k", // Bitrate
		"-f", "ogg",
		opusFile,
		"-y")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Logf("ffmpeg output: %s", string(output))
		return fmt.Errorf("audio extraction failed: %w", err)
	}
	t.Logf("Extracted audio: %s", opusFile)

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

// publishH264NALUnits publishes H.264 video by parsing NAL units and sending each individually
// This mimics LiveKit SDK's approach: send individual NAL units WITHOUT start codes
func publishH264NALUnits(t *testing.T, h264File string, room *lksdk.Room, durationSeconds int) (*lksdk.LocalTrackPublication, error) {
	// Read entire H.264 file
	h264Data, err := os.ReadFile(h264File)
	if err != nil {
		return nil, fmt.Errorf("failed to read H.264 file: %w", err)
	}

	// Parse into individual NAL units (WITHOUT start codes)
	nalUnits := parseH264AnnexBWithoutStartCodes(h264Data)
	t.Logf("Loaded H.264 file: %d bytes, parsed into %d NAL units", len(h264Data), len(nalUnits))

	// Create track
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  "video/H264",
		ClockRate: 90000,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create sample track: %w", err)
	}

	// Publish the track first
	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "camera",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to publish track: %w", err)
	}

	t.Logf("Track published: %s, waiting for track to be ready...", pub.SID())

	// Wait a moment for the track to be fully bound and subscribed
	time.Sleep(500 * time.Millisecond)

	t.Logf("Starting to publish %d NAL units", len(nalUnits))

	// Publish NAL units in a background goroutine
	go func() {
		frameDuration := 33 * time.Millisecond // ~30fps
		clockRate := uint32(90000)
		timestampIncrement := uint32(float64(clockRate) * frameDuration.Seconds()) // 2970 for 33ms

		// Start with non-zero timestamp (SDK treats 0 as "not set")
		currentTimestamp := timestampIncrement

		startTime := time.Now()
		maxDuration := time.Duration(durationSeconds) * time.Second

		nalIndex := 0
		lastFrameTime := time.Now()

		for {
			// Check if we've exceeded duration
			if time.Since(startTime) > maxDuration {
				t.Logf("Reached max duration, stopping NAL publishing")
				return
			}

			// Get next NAL unit (loop if needed)
			if nalIndex >= len(nalUnits) {
				nalIndex = 0
			}

			nalData := nalUnits[nalIndex]
			nalIndex++

			// Determine NAL type
			nalType := nalData[0] & 0x1F
			isFrame := isH264Frame(nalType)

			// Determine duration (frame NALs get 33ms, others get 0)
			var duration time.Duration
			if isFrame {
				duration = frameDuration
			} else {
				duration = 0
			}

			// Create sample
			sample := media.Sample{
				Data:            nalData,
				Duration:        duration,
				PacketTimestamp: currentTimestamp,
			}

			// Debug log first 10 NALs
			if nalIndex <= 10 {
				t.Logf("    [NAL #%d] type=%d, size=%d bytes, ts=%d, duration=%v, isFrame=%v",
					nalIndex, nalType, len(nalData), currentTimestamp, duration, isFrame)
			}

			// Write sample
			if err := track.WriteSample(sample, nil); err != nil {
				if nalIndex <= 10 {
					t.Logf("    [ERROR] WriteSample NAL #%d: %v", nalIndex, err)
				}
			}

			// Increment timestamp for frame NALs
			if isFrame {
				currentTimestamp += timestampIncrement

				// Pace frame sending at ~30fps
				elapsed := time.Since(lastFrameTime)
				if elapsed < frameDuration {
					time.Sleep(frameDuration - elapsed)
				}
				lastFrameTime = time.Now()
			}
			// Non-frame NALs (SPS/PPS) are sent immediately without waiting
		}
	}()

	return pub, nil
}

// parseH264AnnexBWithoutStartCodes parses H.264 Annex B format and returns individual NAL units WITHOUT start codes
// This mimics how Pion's h264reader.NextNAL() works
func parseH264AnnexBWithoutStartCodes(data []byte) [][]byte {
	var nalUnits [][]byte
	var currentNALStart int = -1

	i := 0
	for i < len(data) {
		// Check for 4-byte start code (0x00 0x00 0x00 0x01)
		if i+3 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			// Save previous NAL unit (WITHOUT start code)
			if currentNALStart >= 0 {
				nalData := data[currentNALStart:i]
				if len(nalData) > 0 {
					nalUnits = append(nalUnits, nalData)
				}
			}
			// Mark start of next NAL (AFTER start code)
			currentNALStart = i + 4
			i += 4
			continue
		}

		// Check for 3-byte start code (0x00 0x00 0x01)
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			// Save previous NAL unit (WITHOUT start code)
			if currentNALStart >= 0 {
				nalData := data[currentNALStart:i]
				if len(nalData) > 0 {
					nalUnits = append(nalUnits, nalData)
				}
			}
			// Mark start of next NAL (AFTER start code)
			currentNALStart = i + 3
			i += 3
			continue
		}

		i++
	}

	// Save last NAL unit (WITHOUT start code)
	if currentNALStart >= 0 && currentNALStart < len(data) {
		nalData := data[currentNALStart:]
		if len(nalData) > 0 {
			nalUnits = append(nalUnits, nalData)
		}
	}

	return nalUnits
}

// isH264Frame checks if a NAL unit type represents an actual frame slice
// Based on h264reader NAL unit type definitions
func isH264Frame(nalType byte) bool {
	// NAL types 1-5 are frame slices
	// 1: Non-IDR slice
	// 2-4: Coded slice data partitions (A, B, C)
	// 5: IDR slice
	switch nalType {
	case 1, 2, 3, 4, 5:
		return true
	default:
		return false
	}
}

// parseH264AnnexB parses H.264 Annex B format and returns individual NAL units WITH start codes
// Pion's H264Payloader expects Annex B format (WITH start codes) and handles fragmentation itself
func parseH264AnnexB(data []byte) [][]byte {
	var nalUnits [][]byte
	var currentNALStart int = -1

	i := 0
	for i < len(data) {
		// Check for 4-byte start code (0x00 0x00 0x00 0x01)
		if i+3 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			// Save previous NAL unit (INCLUDING start code)
			if currentNALStart >= 0 {
				nalData := data[currentNALStart:i]
				if len(nalData) > 0 {
					nalUnits = append(nalUnits, nalData)
				}
			}
			// Mark start of next NAL (at start code, not after)
			currentNALStart = i
			i += 4
			continue
		}

		// Check for 3-byte start code (0x00 0x00 0x01)
		if i+2 < len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			// Save previous NAL unit (INCLUDING start code)
			if currentNALStart >= 0 {
				nalData := data[currentNALStart:i]
				if len(nalData) > 0 {
					nalUnits = append(nalUnits, nalData)
				}
			}
			// Mark start of next NAL (at start code, not after)
			currentNALStart = i
			i += 3
			continue
		}

		i++
	}

	// Save last NAL unit (INCLUDING start code)
	if currentNALStart >= 0 && currentNALStart < len(data) {
		nalData := data[currentNALStart:]
		if len(nalData) > 0 {
			nalUnits = append(nalUnits, nalData)
		}
	}

	return nalUnits
}

// publishH264WithAggregatedNALs publishes H.264 with SPS+PPS+IDR aggregated in single samples
// This is the CORRECT way to publish H.264 to LiveKit Server to avoid blank packet issue
func publishH264WithAggregatedNALs(t *testing.T, h264File string, room *lksdk.Room, durationSeconds int) (*lksdk.LocalTrackPublication, error) {
	// Read H.264 file
	h264Data, err := os.ReadFile(h264File)
	if err != nil {
		return nil, fmt.Errorf("failed to read H.264 file: %w", err)
	}

	// Parse NAL units with start codes
	nalUnitsWithStartCodes := parseH264AnnexB(h264Data)
	t.Logf("Parsed %d NAL units from H.264 file", len(nalUnitsWithStartCodes))

	// Create track
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  "video/H264",
		ClockRate: 90000,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create sample track: %w", err)
	}

	// Publish track
	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "camera",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to publish track: %w", err)
	}

	// Start publishing in background - AGGREGATE SPS+PPS+IDR
	go func() {
		frameDuration := 33 * time.Millisecond // 30fps
		clockRate := uint32(90000)
		timestampIncrement := uint32(float64(clockRate) * frameDuration.Seconds())
		currentTimestamp := timestampIncrement

		var sps, pps []byte
		var aggregatedData []byte

		for i := 0; i < len(nalUnitsWithStartCodes); i++ {
			nalWithStartCode := nalUnitsWithStartCodes[i]

			// Extract NAL type (skip start code)
			nalData := nalWithStartCode
			if len(nalData) >= 4 && nalData[0] == 0 && nalData[1] == 0 && nalData[2] == 0 && nalData[3] == 1 {
				nalData = nalData[4:]
			} else if len(nalData) >= 3 && nalData[0] == 0 && nalData[1] == 0 && nalData[2] == 1 {
				nalData = nalData[3:]
			}

			if len(nalData) == 0 {
				continue
			}

			nalType := nalData[0] & 0x1F

			// Aggregate SPS, PPS, IDR
			if nalType == 7 { // SPS
				sps = nalWithStartCode
			} else if nalType == 8 { // PPS
				pps = nalWithStartCode
			} else if nalType == 5 || nalType == 1 { // IDR or P-frame
				// Aggregate: SPS + PPS + IDR (if we have them)
				aggregatedData = nil
				if len(sps) > 0 {
					aggregatedData = append(aggregatedData, sps...)
				}
				if len(pps) > 0 {
					aggregatedData = append(aggregatedData, pps...)
				}
				aggregatedData = append(aggregatedData, nalWithStartCode...)

				// Write aggregated sample
				sample := media.Sample{
					Data:            aggregatedData,
					Duration:        frameDuration,
					PacketTimestamp: currentTimestamp,
				}

				if err := track.WriteSample(sample, nil); err != nil {
					t.Logf("[ERROR] WriteSample: %v", err)
					return
				}

				currentTimestamp += timestampIncrement
				time.Sleep(frameDuration)
			}
		}
	}()

	return pub, nil
}

// OLD FUNCTION - kept for reference
// publishH264WithTimestamps publishes H.264 video with proper RTP timestamp increments
// This fixes the issue where NewLocalFileTrack doesn't increment timestamps
func publishH264WithTimestamps_OLD(t *testing.T, h264File string, room *lksdk.Room, durationSeconds int) (*lksdk.LocalTrackPublication, error) {
	// Create track with proper codec
	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  "video/H264",
		ClockRate: 90000,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create sample track: %w", err)
	}

	// Publish the track first
	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "camera",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to publish track: %w", err)
	}

	// Start publishing samples in background
	go func() {
		file, err := os.Open(h264File)
		if err != nil {
			t.Logf("Failed to open H.264 file: %v", err)
			return
		}
		defer file.Close()

		frameDuration := 33 * time.Millisecond // ~30fps
		clockRate := uint32(90000)
		timestampIncrement := uint32(float64(clockRate) * frameDuration.Seconds()) // 3000 for 30fps

		var currentTimestamp uint32 = 0
		ticker := time.NewTicker(frameDuration)
		defer ticker.Stop()

		buf := make([]byte, 1024*50) // 50KB chunks

		startTime := time.Now()
		maxDuration := time.Duration(durationSeconds) * time.Second

		for {
			select {
			case <-ticker.C:
				// Check if we've exceeded duration
				if time.Since(startTime) > maxDuration {
					return
				}

				// Read next chunk
				n, err := file.Read(buf)
				if err != nil {
					if err == io.EOF {
						// Loop the file
						file.Seek(0, io.SeekStart)
						continue
					}
					t.Logf("Error reading H.264: %v", err)
					return
				}

				if n == 0 {
					continue
				}

				// Create sample with proper PacketTimestamp
				sample := media.Sample{
					Data:            buf[:n],
					Duration:        frameDuration,
					PacketTimestamp: currentTimestamp, // KEY: Set proper RTP timestamp!
				}

				// Write sample
				if err := track.WriteSample(sample, nil); err != nil {
					t.Logf("Error writing sample: %v", err)
					return
				}

				// Increment timestamp for next frame
				currentTimestamp += timestampIncrement

				// Debug: log first few timestamps
				if currentTimestamp <= timestampIncrement*5 {
					t.Logf("    [TIMESTAMP FIX] Written sample with RTP ts=%d (increment=%d)", currentTimestamp, timestampIncrement)
				}
			}
		}
	}()

	return pub, nil
}
