//go:build e2e
// +build e2e

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/interceptor/pkg/jitterbuffer"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
)

const (
	testMP4Path   = "../../examples/egress-agent/test-data/test.mp4"
	testOutputDir = "hls-output"
)

// TestSaveToHLS tests the complete HLS recording workflow
// This test creates a WebRTC publisher and receiver, connects them,
// publishes test.mp4, and validates the HLS output
func TestSaveToHLS(t *testing.T) {
	// Clean up
	os.RemoveAll(testOutputDir)
	os.RemoveAll(testOutputDir + "_final")

	t.Log("=== Step 1: Verify test.mp4 exists ===")
	if _, err := os.Stat(testMP4Path); err != nil {
		t.Fatalf("Test video not found: %v", err)
	}
	t.Log("✓ Test video exists")

	t.Log("\n=== Step 2: Create HLS saver ===")
	// Use 2 second segments for testing (we only publish ~3 seconds of data)
	hlsFile, err := newHLSSaver(testOutputDir, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to create HLS saver: %v", err)
	}
	// Note: We call Close() explicitly at the end, no defer needed
	t.Log("✓ HLS saver created")

	t.Log("\n=== Step 3: Create WebRTC peer connections ===")
	publisher, receiver, err := createPeerConnections(t, hlsFile)
	if err != nil {
		t.Fatalf("Failed to create peer connections: %v", err)
	}
	defer publisher.Close()
	defer receiver.Close()
	t.Log("✓ Peer connections created")

	t.Log("\n=== Step 4: Create and add video and audio tracks ===")
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		"video",
		"pion")
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	videoSender, err := publisher.AddTrack(videoTrack)
	if err != nil {
		t.Fatalf("Failed to add video track: %v", err)
	}
	t.Logf("✓ Video track added to publisher (sender: %v)", videoSender != nil)

	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio",
		"pion")
	if err != nil {
		t.Fatalf("Failed to create audio track: %v", err)
	}

	if _, err = publisher.AddTrack(audioTrack); err != nil {
		t.Fatalf("Failed to add audio track: %v", err)
	}
	t.Log("✓ Audio track added to publisher")

	t.Log("\n=== Step 5: Exchange offers and establish connection ===")
	if err := exchangeSDPAndConnect(t, publisher, receiver); err != nil {
		t.Fatalf("Failed to establish connection: %v", err)
	}
	t.Log("✓ WebRTC connection established")

	t.Log("\n=== Step 6: Publish video and audio from test.mp4 ===")
	// Publish in background with synchronized start time
	videoDone := make(chan bool)
	audioDone := make(chan bool)
	videoReady := make(chan bool)
	audioReady := make(chan bool)
	startTime := make(chan time.Time, 2)

	go func() {
		publishVideoFromMP4(t, videoTrack, testMP4Path, videoReady, startTime)
		videoDone <- true
	}()

	go func() {
		publishAudioFromMP4(t, audioTrack, testMP4Path, audioReady, startTime)
		audioDone <- true
	}()

	// Wait for both goroutines to finish extraction and be ready
	<-videoReady
	<-audioReady
	t.Log("✓ Both video and audio extraction complete, starting synchronized publish...")

	// Now broadcast start time to both goroutines
	now := time.Now()
	startTime <- now
	startTime <- now
	close(startTime)

	t.Log("✓ Publishing video and audio...")

	t.Log("\n=== Step 7: Record full audio+video (or timeout after 5min) ===")
	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	videoComplete := false
	audioComplete := false

loop:
	for {
		select {
		case <-timeout:
			t.Log("  → Recording timeout reached")
			break loop
		case <-videoDone:
			t.Log("  → Video publishing complete")
			videoComplete = true
			if audioComplete {
				t.Log("  → Both tracks complete, waiting for segments to finalize...")
				time.Sleep(8 * time.Second) // Give gohlslib time to finalize all segments including last one
				break loop
			}
		case <-audioDone:
			t.Log("  → Audio publishing complete")
			audioComplete = true
			if videoComplete {
				t.Log("  → Both tracks complete, waiting for segments to finalize...")
				time.Sleep(8 * time.Second) // Give gohlslib time to finalize all segments including last one
				break loop
			}
		case <-ticker.C:
			t.Log("  → Recording in progress...")
		}
	}

	// Wait for both to finish (in case timeout was hit)
	select {
	case <-videoDone:
	case <-time.After(1 * time.Second):
	}
	select {
	case <-audioDone:
	case <-time.After(1 * time.Second):
	}

	t.Log("\n=== Step 8: Finalize HLS output ===")
	hlsFile.Close()
	t.Log("✓ HLS output finalized")

	t.Log("\n=== Step 9: Validate HLS output ===")
	// Output is saved to _final directory after muxer close
	if err := validateHLS(t, testOutputDir+"_final"); err != nil {
		t.Fatalf("HLS validation failed: %v", err)
	}
	t.Log("✓ HLS output is valid")

	t.Log("\n✅ All tests passed!")
}

// createPeerConnections creates publisher and receiver peer connections
func createPeerConnections(t *testing.T, hlsFile *hlsSaver) (*webrtc.PeerConnection, *webrtc.PeerConnection, error) {
	// Create media engine
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		PayloadType:        102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, nil, err
	}

	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
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

	// Create buffers for proper synchronization
	// Use JitterBuffer for both audio and video to ensure packets are written in sequence order
	opusJitterBuffer := jitterbuffer.New()
	h264JitterBuffer := jitterbuffer.New()

	// Setup receiver to forward to HLS saver
	audioPacketCount := 0
	receiver.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		t.Logf("  → Track received: %s", track.Codec().MimeType)

		if track.Codec().MimeType == webrtc.MimeTypeH264 {
			// H264: use jitter buffer exactly like save-to-webm.go (lines 99-155)
			for {
				rtpPacket, _, readErr := track.ReadRTP()
				if readErr != nil {
					return
				}

				h264JitterBuffer.Push(rtpPacket)

				// Try to peek at head packet
				pkt, err := h264JitterBuffer.Peek(true)
				if err != nil {
					continue
				}

				// Collect all packets with same timestamp (one frame)
				pkts := []*rtp.Packet{pkt}
				for {
					nextPkt, err := h264JitterBuffer.PeekAtSequence(pkts[len(pkts)-1].SequenceNumber + 1)
					if err != nil {
						break
					}

					// Different timestamp means next frame
					if pkts[0].Timestamp != nextPkt.Timestamp {
						break
					}

					pkts = append(pkts, nextPkt)
				}

				// Pop and write all packets in this frame
				for _, p := range pkts {
					if _, err := h264JitterBuffer.PopAtSequence(p.SequenceNumber); err != nil {
						break
					}
					if err := hlsFile.WriteRTP(p); err != nil {
						t.Logf("  → Error writing H.264: %v", err)
					}
				}
			}
		} else if track.Codec().MimeType == webrtc.MimeTypeOpus {
			// Opus: use jitter buffer to ensure packets are written in sequence order
			for {
				rtpPacket, _, readErr := track.ReadRTP()
				if readErr != nil {
					return
				}

				audioPacketCount++
				if audioPacketCount <= 3 {
					t.Logf("  → Audio RTP packet %d: payload size=%d, timestamp=%d",
						audioPacketCount, len(rtpPacket.Payload), rtpPacket.Timestamp)
				}

				opusJitterBuffer.Push(rtpPacket)

				// Pop all available packets from jitter buffer
				for {
					pkt, err := opusJitterBuffer.Pop()
					if err != nil {
						break
					}
					if err := hlsFile.WriteAudioRTP(pkt); err != nil {
						t.Logf("  → Error writing Opus: %v", err)
					}
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

	t.Logf("  → Offer created with %d media sections", len(strings.Split(offer.SDP, "m=")))
	// Count tracks in SDP
	videoCount := strings.Count(offer.SDP, "m=video")
	audioCount := strings.Count(offer.SDP, "m=audio")
	t.Logf("  → SDP contains: %d video, %d audio", videoCount, audioCount)

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

// publishVideoFromMP4 publishes video from MP4 file using time-synchronized pacing
func publishVideoFromMP4(t *testing.T, track *webrtc.TrackLocalStaticSample, mp4Path string, readyChan chan<- bool, startTimeChan <-chan time.Time) {
	t.Logf("  → Detecting frame rate from: %s", mp4Path)

	// Detect frame rate using ffprobe
	fpsCmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=r_frame_rate", "-of", "default=noprint_wrappers=1:nokey=1", mp4Path)
	fpsOutput, err := fpsCmd.Output()
	if err != nil {
		t.Fatalf("ffprobe failed to detect frame rate: %v", err)
	}

	// Parse frame rate (format: "30/1" or "30000/1001")
	fpsStr := strings.TrimSpace(string(fpsOutput))
	fpsParts := strings.Split(fpsStr, "/")
	if len(fpsParts) != 2 {
		t.Fatalf("Unexpected frame rate format: %s", fpsStr)
	}

	fpsNum, err := strconv.ParseFloat(fpsParts[0], 64)
	if err != nil {
		t.Fatalf("Failed to parse frame rate numerator: %v", err)
	}

	fpsDen, err := strconv.ParseFloat(fpsParts[1], 64)
	if err != nil {
		t.Fatalf("Failed to parse frame rate denominator: %v", err)
	}

	fps := fpsNum / fpsDen
	frameDuration := time.Duration(float64(time.Second) / fps)
	t.Logf("  → Detected frame rate: %.3f fps (frame duration: %v)", fps, frameDuration)

	t.Logf("  → Extracting H.264 via ffmpeg from: %s", mp4Path)

	// Use ffmpeg to extract H.264 in Annex B format
	tmpFile := "/tmp/video_h264_" + time.Now().Format("20060102150405") + ".h264"
	defer os.Remove(tmpFile)

	// Extract video to H.264 Annex B format
	cmd := exec.Command("ffmpeg", "-i", mp4Path, "-an", "-vcodec", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "h264", tmpFile)
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg video extraction failed: %v", err)
	}

	// Open extracted H.264 file
	h264File, err := os.Open(tmpFile)
	if err != nil {
		t.Fatalf("Failed to open H.264 file: %v", err)
	}
	defer h264File.Close()

	// Use Pion H.264 reader
	h264Reader, err := h264reader.NewReader(h264File)
	if err != nil {
		t.Fatalf("Failed to create H.264 reader: %v", err)
	}

	// Signal ready (extraction complete)
	t.Logf("  → Video extraction complete, ready to publish")
	readyChan <- true

	// Wait for synchronized start time
	startTime := <-startTimeChan
	t.Logf("  → Publishing H.264 frames with synchronized timing at %.3f fps...", fps)

	frameCount := 0

	// Track NALs to group them by frame
	currentFrameNALs := [][]byte{}
	const (
		nalTypeSlice = 1
		nalTypeIDR   = 5
		nalTypeSPS   = 7
		nalTypePPS   = 8
	)

	for {
		nal, err := h264Reader.NextNAL()
		if err != nil {
			if err == io.EOF {
				// Write any remaining NALs
				if len(currentFrameNALs) > 0 {
					for i, nalData := range currentFrameNALs {
						duration := time.Duration(0)
						if i == len(currentFrameNALs)-1 {
							duration = frameDuration
						}
						if err := track.WriteSample(media.Sample{
							Data:     nalData,
							Duration: duration,
						}); err != nil {
							t.Logf("Error writing final video sample: %v", err)
						}
					}
					frameCount++
				}
				break
			}
			t.Logf("Error reading NAL: %v", err)
			break
		}

		// Pion h264reader strips start codes, add them back
		nalWithStartCode := append([]byte{0x00, 0x00, 0x00, 0x01}, nal.Data...)

		// Get NAL type from first byte (bits 0-4)
		nalType := nal.Data[0] & 0x1F

		// Check if this is a new frame (slice or IDR)
		isFrameStart := (nalType == nalTypeSlice || nalType == nalTypeIDR)

		// If we have accumulated NALs and this is a new frame, write the previous frame
		if isFrameStart && len(currentFrameNALs) > 0 {
			// Combine all NALs into a single buffer (Annex B format with start codes)
			var frameData []byte
			for _, nalData := range currentFrameNALs {
				frameData = append(frameData, nalData...)
			}

			// Debug first 10 frames
			if frameCount < 10 {
				t.Logf("[Test Frame %d] Combining %d NALs, total size: %d bytes", frameCount, len(currentFrameNALs), len(frameData))
			}

			// Write combined frame as a single sample
			if err := track.WriteSample(media.Sample{
				Data:     frameData,
				Duration: frameDuration,
			}); err != nil {
				t.Logf("Error writing video sample: %v", err)
				return
			}

			frameCount++

			// Sleep until the exact time for next frame (eliminates drift)
			nextFrameTime := startTime.Add(time.Duration(frameCount) * frameDuration)
			sleepDuration := time.Until(nextFrameTime)
			if sleepDuration > 0 {
				time.Sleep(sleepDuration)
			}

			// Start new frame
			currentFrameNALs = [][]byte{nalWithStartCode}
		} else {
			// Add NAL to current frame
			currentFrameNALs = append(currentFrameNALs, nalWithStartCode)
		}
	}

	t.Logf("  → Finished publishing %d video frames", frameCount)
}

// getOpusFrameDuration extracts frame duration from Opus TOC byte
// Opus frame durations: 2.5, 5, 10, 20, 40, 60 ms
func getOpusFrameDuration(opusPacket []byte) time.Duration {
	if len(opusPacket) == 0 {
		return 20 * time.Millisecond // Fallback
	}

	// TOC byte is first byte: bits 3-5 encode frame duration
	toc := opusPacket[0]
	config := (toc >> 3) & 0x1F

	// Frame duration table (from RFC 6716)
	frameSizes := []float64{10, 20, 40, 60} // ms
	frameSizeIndex := config & 0x03

	var durationMs float64
	switch {
	case config < 12: // SILK-only or Hybrid
		durationMs = frameSizes[frameSizeIndex]
	case config < 16: // CELT-only
		durationMs = frameSizes[frameSizeIndex]
	default: // Frame size in TOC
		durationMs = frameSizes[frameSizeIndex]
	}

	return time.Duration(durationMs * float64(time.Millisecond))
}

// publishAudioFromMP4 publishes audio from MP4 file using Ogg
// Properly parses Ogg segment table to extract individual Opus packets
func publishAudioFromMP4(t *testing.T, track *webrtc.TrackLocalStaticSample, mp4Path string, readyChan chan<- bool, startTimeChan <-chan time.Time) {
	t.Logf("  → Extracting Opus to Ogg via ffmpeg from: %s", mp4Path)

	tmpFile := "/tmp/audio_opus_" + time.Now().Format("20060102150405") + ".ogg"
	defer os.Remove(tmpFile)

	// Extract audio to Ogg Opus
	cmd := exec.Command("ffmpeg", "-i", mp4Path, "-vn", "-c:a", "libopus", "-f", "ogg", tmpFile)
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg Ogg extraction failed: %v", err)
	}

	file, err := os.Open(tmpFile)
	if err != nil {
		t.Fatalf("Failed to open Ogg file: %v", err)
	}
	defer file.Close()

	// Create custom packet reader that parses segment table and skips header pages
	reader, err := newOggPacketReader(file)
	if err != nil {
		t.Fatalf("Failed to create Ogg packet reader: %v", err)
	}

	// Signal ready (extraction and parsing complete)
	t.Logf("  → Audio extraction complete, ready to publish")
	readyChan <- true

	// Wait for synchronized start time
	startTime := <-startTimeChan
	t.Logf("  → Publishing individual Opus packets with synchronized timing...")

	pageCount := 0
	packetCount := 0
	var lastTimestamp time.Time = startTime

	for {
		packets, err := reader.readNextPackets()
		if err != nil {
			if err == io.EOF {
				// End of audio file
				break
			}
			t.Logf("Error reading Ogg packets: %v", err)
			break
		}

		pageCount++

		// Send each individual Opus packet
		for _, opusPacket := range packets {
			if len(opusPacket) == 0 {
				continue
			}

			// Detect frame duration from Opus TOC byte
			frameDuration := getOpusFrameDuration(opusPacket)

			// Send individual Opus packet with proper timing
			if err := track.WriteSample(media.Sample{Data: opusPacket, Duration: frameDuration}); err != nil {
				t.Logf("Error writing Opus packet: %v", err)
				return
			}

			packetCount++
			if packetCount%1000 == 0 {
				t.Logf("    Published %d Opus packets (packet size: %d bytes)", packetCount, len(opusPacket))
			}

			// Sleep until the exact time for next packet (eliminates drift)
			nextPacketTime := lastTimestamp.Add(frameDuration)
			sleepDuration := time.Until(nextPacketTime)
			if sleepDuration > 0 {
				time.Sleep(sleepDuration)
			}
			lastTimestamp = nextPacketTime
		}
	}

	t.Logf("  → Finished publishing %d Opus packets from %d Ogg pages", packetCount, pageCount)
}

// parseOggPackets manually parses Ogg pages and extracts individual Opus packets
// using the segment table to determine packet boundaries
type oggPacketReader struct {
	file             *os.File
	leftoverSegments []byte // Incomplete packet from previous page
}

func newOggPacketReader(file *os.File) (*oggPacketReader, error) {
	reader := &oggPacketReader{file: file}

	// Skip the first two Ogg pages: OpusHead (identification) and OpusTags (comments)
	// These are metadata pages, not audio data
	for i := 0; i < 2; i++ {
		if err := reader.skipPage(); err != nil {
			return nil, fmt.Errorf("failed to skip header page %d: %w", i+1, err)
		}
	}

	return reader, nil
}

func (r *oggPacketReader) skipPage() error {
	// Read page header (27 bytes)
	header := make([]byte, 27)
	if _, err := io.ReadFull(r.file, header); err != nil {
		return err
	}

	// Parse segment count from byte 26
	segmentsCount := header[26]

	// Read segment table
	segmentTable := make([]byte, segmentsCount)
	if _, err := io.ReadFull(r.file, segmentTable); err != nil {
		return err
	}

	// Calculate total payload size
	payloadSize := 0
	for _, segLen := range segmentTable {
		payloadSize += int(segLen)
	}

	// Skip the payload
	if _, err := r.file.Seek(int64(payloadSize), io.SeekCurrent); err != nil {
		return err
	}

	return nil
}

func (r *oggPacketReader) readNextPackets() ([][]byte, error) {
	// Read page header (27 bytes)
	header := make([]byte, 27)
	if _, err := io.ReadFull(r.file, header); err != nil {
		return nil, err
	}

	// Parse segment count from byte 26
	segmentsCount := header[26]

	// Read segment table
	segmentTable := make([]byte, segmentsCount)
	if _, err := io.ReadFull(r.file, segmentTable); err != nil {
		return nil, err
	}

	// Calculate total payload size
	payloadSize := 0
	for _, segLen := range segmentTable {
		payloadSize += int(segLen)
	}

	// Read entire payload
	payload := make([]byte, payloadSize)
	if _, err := io.ReadFull(r.file, payload); err != nil {
		return nil, err
	}

	// Extract individual packets using segment table
	var packets [][]byte
	payloadOffset := 0

	// Start with leftover segments from previous page if any
	currentPacket := make([]byte, 0, 4096)
	if len(r.leftoverSegments) > 0 {
		currentPacket = append(currentPacket, r.leftoverSegments...)
		r.leftoverSegments = nil
	}

	for _, segLen := range segmentTable {
		segmentData := payload[payloadOffset : payloadOffset+int(segLen)]
		currentPacket = append(currentPacket, segmentData...)
		payloadOffset += int(segLen)

		// If segment < 255, packet is complete
		if segLen < 255 {
			if len(currentPacket) > 0 {
				packets = append(packets, currentPacket)
				currentPacket = make([]byte, 0, 4096)
			}
		}
		// If segment == 255, packet continues in next segment/page
	}

	// Save incomplete packet for next page
	if len(currentPacket) > 0 {
		r.leftoverSegments = currentPacket
	}

	return packets, nil
}

// validateHLS validates the HLS output
func validateHLS(t *testing.T, outputDir string) error {
	// Check output directory exists
	if _, err := os.Stat(outputDir); err != nil {
		return fmt.Errorf("output directory not found: %w", err)
	}
	t.Logf("  → Output directory: %s", outputDir)

	// Check index.m3u8 exists (master playlist)
	masterPlaylistPath := filepath.Join(outputDir, "index.m3u8")
	if _, err := os.Stat(masterPlaylistPath); err != nil {
		return fmt.Errorf("master playlist not found: %w", err)
	}
	t.Logf("  → Master playlist exists")

	// Check video media playlist
	videoPlaylistPath := filepath.Join(outputDir, "video1_stream.m3u8")
	if _, err := os.Stat(videoPlaylistPath); err != nil {
		return fmt.Errorf("video playlist not found: %w", err)
	}
	videoPlaylistData, err := os.ReadFile(videoPlaylistPath)
	if err != nil {
		return fmt.Errorf("failed to read video playlist: %w", err)
	}
	videoSegments := parseM3U8Segments(string(videoPlaylistData))

	// Check audio media playlist
	audioPlaylistPath := filepath.Join(outputDir, "audio2_stream.m3u8")
	if _, err := os.Stat(audioPlaylistPath); err != nil {
		return fmt.Errorf("audio playlist not found: %w", err)
	}
	audioPlaylistData, err := os.ReadFile(audioPlaylistPath)
	if err != nil {
		return fmt.Errorf("failed to read audio playlist: %w", err)
	}
	audioSegments := parseM3U8Segments(string(audioPlaylistData))

	// Combine all segments
	segments := append(videoSegments, audioSegments...)
	if len(segments) == 0 {
		return fmt.Errorf("no segments found in playlists")
	}
	t.Logf("  → Total segments: %d (video: %d, audio: %d)", len(segments), len(videoSegments), len(audioSegments))

	// Verify each segment exists
	var totalSize int64
	for _, seg := range segments {
		segPath := filepath.Join(outputDir, seg)
		info, err := os.Stat(segPath)
		if err != nil {
			return fmt.Errorf("segment %s not found: %w", seg, err)
		}
		if info.Size() == 0 {
			return fmt.Errorf("segment %s is empty", seg)
		}
		totalSize += info.Size()
	}
	t.Logf("  → All segments exist, total size: %.2f MB", float64(totalSize)/1024/1024)

	// Validation already done above - videoSegments and audioSegments are slices
	t.Logf("  ✓ HLS output contains both audio and video")

	return nil
}

// parseM3U8Segments extracts segment filenames from M3U8 playlist
func parseM3U8Segments(playlist string) []string {
	var segments []string
	lines := strings.Split(playlist, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// This is a segment filename (support MPEG-TS and fMP4 variants)
		if strings.HasSuffix(line, ".ts") || strings.HasSuffix(line, ".m4s") || strings.HasSuffix(line, ".mp4") {
			segments = append(segments, line)
		}
	}

	return segments
}
