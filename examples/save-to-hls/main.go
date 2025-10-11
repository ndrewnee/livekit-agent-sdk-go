// save-to-hls is a simple application that shows how to receive
// video using Pion and then save to HLS format.
// Based on save-to-webm.go but outputs HLS segments instead.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gohlslib/v2"
	hlscodecs "github.com/bluenviron/gohlslib/v2/pkg/codecs"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4"
	"github.com/bluenviron/mediacommon/v2/pkg/formats/fmp4/seekablebuffer"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

const (
	naluTypeBitmask = 0x1F
	naluTypeSPS     = 7
	naluTypePPS     = 8
	naluTypeIDR     = 5
)

type hlsSaver struct {
	h264Builder       *samplebuilder.SampleBuilder
	sampleCount       int
	videoTimestamp    time.Duration
	lastVideoTS       uint32
	lastFrameDuration time.Duration

	// Frame accumulation (to handle SampleBuilder combining multiple frames)
	currentFrameNALs [][]byte
	currentFrameTS   uint32

	// Audio
	opusBuilder      *samplebuilder.SampleBuilder
	audioSampleCount int
	audioTimestamp   time.Duration
	lastAudioTS      uint32

	// HLS output
	outputDir         string
	muxer             *gohlslib.Muxer
	videoTrack        *gohlslib.Track
	audioTrack        *gohlslib.Track
	muxerStarted      bool
	firstVideoWritten bool
	segmentDuration   time.Duration

	// Synchronization
	mu sync.Mutex

	// SPS/PPS cache
	sps    []byte
	pps    []byte
	hasSPS bool
	hasPPS bool

	// Segment tracking for playlist generation
	segments     []string
	segmentCount int
}

func newHLSSaver(outputDir string, segmentDuration time.Duration) (*hlsSaver, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	return &hlsSaver{
		h264Builder:     samplebuilder.New(100, &codecs.H264Packet{}, 90000),
		opusBuilder:     samplebuilder.New(100, &codecs.OpusPacket{}, 48000),
		outputDir:       outputDir,
		segmentDuration: segmentDuration,
		segments:        make([]string, 0),
	}, nil
}

func (s *hlsSaver) WriteRTP(pkt *rtp.Packet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.h264Builder.Push(pkt)

	for {
		sample := s.h264Builder.Pop()
		if sample == nil {
			return nil
		}

		data := sample.Data
		if len(data) == 0 {
			continue
		}

		// Extract all NAL units from this sample
		nalUnits := s.parseAnnexBNALs(data)

		// Process NAL units
		for _, nalData := range nalUnits {
			if len(nalData) == 0 {
				continue
			}

			nalType := nalData[0] & naluTypeBitmask

			// Cache SPS/PPS
			if nalType == naluTypeSPS {
				s.sps = make([]byte, len(nalData))
				copy(s.sps, nalData)
				s.hasSPS = true
				log.Printf("Cached SPS (%d bytes)", len(s.sps))
			} else if nalType == naluTypePPS {
				s.pps = make([]byte, len(nalData))
				copy(s.pps, nalData)
				s.hasPPS = true
				log.Printf("Cached PPS (%d bytes)", len(s.pps))
			}

			// Initialize muxer on first keyframe with SPS/PPS
			if !s.muxerStarted && s.hasSPS && s.hasPPS {
				if err := s.initMuxer(); err != nil {
					return fmt.Errorf("failed to initialize muxer: %w", err)
				}
			}

			// Detect frame boundary by RTP timestamp change
			// If timestamp changed, we're starting a new frame
			if s.currentFrameTS != 0 && sample.PacketTimestamp != s.currentFrameTS {
				// Write accumulated frame from previous timestamp
				if len(s.currentFrameNALs) > 0 {
					if err := s.writeFrame(s.currentFrameNALs); err != nil {
						return err
					}
				}
				// Reset for new frame
				s.currentFrameNALs = nil
			}

			// Accumulate NAL for current frame
			nalCopy := make([]byte, len(nalData))
			copy(nalCopy, nalData)
			s.currentFrameNALs = append(s.currentFrameNALs, nalCopy)
			s.currentFrameTS = sample.PacketTimestamp
		}
	}

	return nil
}

// writeFrame writes an accumulated frame (all NALs with same RTP timestamp) to HLS
func (s *hlsSaver) writeFrame(nalUnits [][]byte) error {
	if !s.muxerStarted || s.muxer == nil || s.videoTrack == nil {
		return nil
	}

	// Set baseline RTP timestamp from first frame
	if s.lastVideoTS == 0 {
		s.lastVideoTS = s.currentFrameTS
	}

	// Calculate PTS directly from RTP timestamp difference (90kHz clock)
	// PTS is the time since the first frame
	pts := int64(s.currentFrameTS - s.lastVideoTS)

	// Debug logging for first 10 frames
	if s.sampleCount < 10 {
		duration := time.Duration(float64(pts)/90000.0*1000) * time.Millisecond
		log.Printf("[Video Frame %d] RTP_TS=%d, baseline=%d, pts=%d (%v), nalCount=%d",
			s.sampleCount, s.currentFrameTS, s.lastVideoTS, pts, duration, len(nalUnits))
	}

	if err := s.muxer.WriteH264(s.videoTrack, time.Now(), pts, nalUnits); err != nil {
		return fmt.Errorf("failed to write H.264 to HLS: %w", err)
	}

	s.sampleCount++
	s.firstVideoWritten = true
	return nil
}

func (s *hlsSaver) WriteAudioRTP(pkt *rtp.Packet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.opusBuilder.Push(pkt)

	for {
		sample := s.opusBuilder.Pop()
		if sample == nil {
			return nil
		}

		if len(sample.Data) == 0 {
			continue
		}

		// Calculate duration from RTP timestamp difference (48kHz clock for Opus)
		// Skip first sample since we need at least 2 timestamps to calculate duration
		if s.lastAudioTS == 0 {
			s.lastAudioTS = sample.PacketTimestamp
			continue // Skip first sample, will use timestamp diff for subsequent samples
		}

		samples := sample.PacketTimestamp - s.lastAudioTS
		duration := time.Duration(float64(samples)/48000.0*1000) * time.Millisecond
		s.lastAudioTS = sample.PacketTimestamp

		// Write to HLS if muxer is ready AND first video has been written
		// (fMP4 muxer needs video initialization before audio)
		if s.muxerStarted && s.muxer != nil && s.audioTrack != nil && s.firstVideoWritten {
			pts := int64(s.audioTimestamp.Seconds() * 48000)

			if err := s.muxer.WriteOpus(s.audioTrack, time.Now(), pts, [][]byte{sample.Data}); err != nil {
				return fmt.Errorf("failed to write Opus to HLS: %w", err)
			}

			s.audioSampleCount++
			s.audioTimestamp += duration
		}
	}
}

func (s *hlsSaver) initMuxer() error {
	// Create video track with cached SPS/PPS
	s.videoTrack = &gohlslib.Track{
		Codec: &hlscodecs.H264{
			SPS: s.sps,
			PPS: s.pps,
		},
		ClockRate: 90000,
	}

	// Create audio track for Opus
	s.audioTrack = &gohlslib.Track{
		Codec: &hlscodecs.Opus{
			ChannelCount: 2,
		},
		ClockRate: 48000,
	}

	// Create HLS muxer with both audio and video tracks
	// Use fMP4 variant to support Opus audio (MPEG-TS only supports AAC)
	s.muxer = &gohlslib.Muxer{
		Variant:            gohlslib.MuxerVariantFMP4,
		SegmentCount:       100,
		SegmentMinDuration: 2 * time.Second,
		Directory:          s.outputDir,
		Tracks:             []*gohlslib.Track{s.videoTrack, s.audioTrack},
	}

	if err := s.muxer.Start(); err != nil {
		return err
	}

	s.muxerStarted = true
	log.Printf("HLS muxer started in %s", s.outputDir)
	return nil
}

// parseAnnexBNALs parses Annex B formatted data and returns NAL units (without start codes)
func (s *hlsSaver) parseAnnexBNALs(data []byte) [][]byte {
	var nalUnits [][]byte

	debugFirst10 := s.sampleCount < 10
	if debugFirst10 {
		log.Printf("[parseAnnexBNALs Sample %d] Input data size: %d bytes", s.sampleCount, len(data))
	}

	offset := 0
	for offset < len(data) {
		// Look for start code (0x000001 or 0x00000001)
		startCodeLen := 0
		if offset+4 <= len(data) && data[offset] == 0x00 && data[offset+1] == 0x00 && data[offset+2] == 0x00 && data[offset+3] == 0x01 {
			startCodeLen = 4
		} else if offset+3 <= len(data) && data[offset] == 0x00 && data[offset+1] == 0x00 && data[offset+2] == 0x01 {
			startCodeLen = 3
		} else {
			offset++
			continue
		}

		nalStart := offset + startCodeLen
		if nalStart >= len(data) {
			break
		}

		// Find next start code
		nalEnd := len(data)
		for i := nalStart + 1; i < len(data)-2; i++ {
			if data[i] == 0x00 && data[i+1] == 0x00 {
				if i+2 < len(data) && data[i+2] == 0x01 {
					nalEnd = i
					break
				} else if i+3 < len(data) && data[i+2] == 0x00 && data[i+3] == 0x01 {
					nalEnd = i
					break
				}
			}
		}

		if nalEnd > nalStart {
			nalUnit := make([]byte, nalEnd-nalStart)
			copy(nalUnit, data[nalStart:nalEnd])
			nalUnits = append(nalUnits, nalUnit)
		}

		offset = nalEnd
	}

	return nalUnits
}

func (s *hlsSaver) Close() error {
	s.mu.Lock()
	// Flush any remaining accumulated frame
	if len(s.currentFrameNALs) > 0 {
		if err := s.writeFrame(s.currentFrameNALs); err != nil {
			s.mu.Unlock()
			return fmt.Errorf("failed to flush last frame: %w", err)
		}
		s.currentFrameNALs = nil
	}

	// Force muxer to finalize all buffered segments by writing a frame far in the future
	// This ensures Part N is written to disk before we copy files
	if s.muxerStarted && s.muxer != nil && s.videoTrack != nil && s.lastVideoTS != 0 {
		// Write a dummy frame 10 seconds in the future to trigger segment finalization
		futurePTS := int64(s.currentFrameTS - s.lastVideoTS + 900000) // +10 seconds at 90kHz
		// Create a minimal P-frame NAL
		dummyNAL := []byte{0x41} // P-frame NAL type
		if err := s.muxer.WriteH264(s.videoTrack, time.Now(), futurePTS, [][]byte{dummyNAL}); err != nil {
			log.Printf("Warning: failed to write finalizing frame: %v", err)
		}
	}
	s.mu.Unlock()

	log.Printf("Wrote %d H.264 samples, %d Opus samples", s.sampleCount, s.audioSampleCount)

	if s.muxer != nil && s.muxerStarted {
		// Wait for final segments to be written
		// The muxer buffers data and writes segments asynchronously
		time.Sleep(2 * time.Second)

		// Copy all files BEFORE closing (muxer.Close() deletes temp files)
		finalDir := s.outputDir + "_final"
		if err := os.MkdirAll(finalDir, 0755); err != nil {
			return fmt.Errorf("failed to create final directory: %w", err)
		}

		// Generate init files (gohlslib doesn't write them to disk)
		if err := s.generateInitFiles(finalDir); err != nil {
			log.Printf("Error generating init files: %v", err)
		}

		// Collect segment info while copying
		var videoSegments, audioSegments []string

		// Copy all segment files from output directory
		files, err := os.ReadDir(s.outputDir)
		if err != nil {
			log.Printf("Error reading directory: %v", err)
		} else {
			copiedCount := 0
			for _, file := range files {
				if file.IsDir() {
					continue
				}

				// Only copy non-empty files
				info, _ := file.Info()
				if info.Size() == 0 {
					continue
				}

				srcPath := filepath.Join(s.outputDir, file.Name())
				dstPath := filepath.Join(finalDir, file.Name())

				data, err := os.ReadFile(srcPath)
				if err != nil {
					log.Printf("Error reading %s: %v", file.Name(), err)
					continue
				}

				if err := os.WriteFile(dstPath, data, 0644); err != nil {
					log.Printf("Error writing %s: %v", file.Name(), err)
					continue
				}

				// Track segments for playlist generation
				name := file.Name()
				if strings.Contains(name, "video") && strings.HasSuffix(name, ".mp4") {
					videoSegments = append(videoSegments, name)
				} else if strings.Contains(name, "audio") && strings.HasSuffix(name, ".mp4") {
					audioSegments = append(audioSegments, name)
				}

				copiedCount++
			}
			log.Printf("Copied %d segment files to %s", copiedCount, finalDir)

			// Generate HLS playlists
			if err := s.generatePlaylists(finalDir, videoSegments, audioSegments); err != nil {
				log.Printf("Error generating playlists: %v", err)
			}
		}

		// Now close the muxer (this deletes temp files)
		s.muxer.Close()
		s.muxer = nil
		s.muxerStarted = false

		log.Printf("HLS recording saved to %s", finalDir)
		log.Printf("Note: Use HTTP server with muxer.Handle() for dynamic playlist serving during live streaming")
	}

	return nil
}

func (s *hlsSaver) generateInitFiles(outputDir string) error {
	// Generate video init file
	if s.videoTrack != nil && len(s.sps) > 0 && len(s.pps) > 0 {
		var videoInit fmp4.Init
		videoInit.Tracks = append(videoInit.Tracks, &fmp4.InitTrack{
			ID:        1,
			TimeScale: 90000,
			Codec: &fmp4.CodecH264{
				SPS: s.sps,
				PPS: s.pps,
			},
		})

		var buf seekablebuffer.Buffer
		if err := videoInit.Marshal(&buf); err != nil {
			return fmt.Errorf("failed to marshal video init: %w", err)
		}

		initPath := filepath.Join(outputDir, "video_init.mp4")
		if err := os.WriteFile(initPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write video init: %w", err)
		}
		log.Printf("Generated video_init.mp4 (%d bytes)", len(buf.Bytes()))
	}

	// Generate audio init file
	if s.audioTrack != nil {
		var audioInit fmp4.Init
		audioInit.Tracks = append(audioInit.Tracks, &fmp4.InitTrack{
			ID:        1, // gohlslib uses track ID 1 for each separate stream
			TimeScale: 48000,
			Codec: &fmp4.CodecOpus{
				ChannelCount: 2,
			},
		})

		var buf seekablebuffer.Buffer
		if err := audioInit.Marshal(&buf); err != nil {
			return fmt.Errorf("failed to marshal audio init: %w", err)
		}

		initPath := filepath.Join(outputDir, "audio_init.mp4")
		if err := os.WriteFile(initPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write audio init: %w", err)
		}
		log.Printf("Generated audio_init.mp4 (%d bytes)", len(buf.Bytes()))
	}

	return nil
}

// extractSegmentNumber extracts the segment number from filenames like "abc_video1_seg12.mp4"
func extractSegmentNumber(filename string) int {
	// Find "seg" followed by digits
	idx := strings.LastIndex(filename, "_seg")
	if idx == -1 {
		return 0
	}

	// Extract the number part after "_seg"
	numStr := filename[idx+4:]
	numStr = strings.TrimSuffix(numStr, ".mp4")

	num := 0
	fmt.Sscanf(numStr, "%d", &num)
	return num
}

func (s *hlsSaver) generatePlaylists(outputDir string, videoSegments, audioSegments []string) error {
	// Sort segments numerically by segment number
	sortSegments := func(segments []string) {
		sort.Slice(segments, func(i, j int) bool {
			// Extract segment numbers from filenames like "abc_video1_seg12.mp4"
			iNum := extractSegmentNumber(segments[i])
			jNum := extractSegmentNumber(segments[j])
			return iNum < jNum
		})
	}
	sortSegments(videoSegments)
	sortSegments(audioSegments)

	segDuration := int(s.segmentDuration.Seconds())

	// Generate video media playlist
	if len(videoSegments) > 0 {
		var videoPlaylist strings.Builder
		videoPlaylist.WriteString("#EXTM3U\n")
		videoPlaylist.WriteString("#EXT-X-VERSION:7\n")
		videoPlaylist.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", segDuration+1))
		videoPlaylist.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
		videoPlaylist.WriteString("#EXT-X-MAP:URI=\"video_init.mp4\"\n")

		for _, seg := range videoSegments {
			videoPlaylist.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", s.segmentDuration.Seconds()))
			videoPlaylist.WriteString(seg + "\n")
		}

		videoPlaylist.WriteString("#EXT-X-ENDLIST\n")

		playlistPath := filepath.Join(outputDir, "video1_stream.m3u8")
		if err := os.WriteFile(playlistPath, []byte(videoPlaylist.String()), 0644); err != nil {
			return fmt.Errorf("failed to write video playlist: %w", err)
		}
		log.Printf("Generated video1_stream.m3u8 with %d segments", len(videoSegments))
	}

	// Generate audio media playlist
	if len(audioSegments) > 0 {
		var audioPlaylist strings.Builder
		audioPlaylist.WriteString("#EXTM3U\n")
		audioPlaylist.WriteString("#EXT-X-VERSION:7\n")
		audioPlaylist.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", segDuration+1))
		audioPlaylist.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
		audioPlaylist.WriteString("#EXT-X-MAP:URI=\"audio_init.mp4\"\n")

		for _, seg := range audioSegments {
			audioPlaylist.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", s.segmentDuration.Seconds()))
			audioPlaylist.WriteString(seg + "\n")
		}

		audioPlaylist.WriteString("#EXT-X-ENDLIST\n")

		playlistPath := filepath.Join(outputDir, "audio2_stream.m3u8")
		if err := os.WriteFile(playlistPath, []byte(audioPlaylist.String()), 0644); err != nil {
			return fmt.Errorf("failed to write audio playlist: %w", err)
		}
		log.Printf("Generated audio2_stream.m3u8 with %d segments", len(audioSegments))
	}

	// Calculate total bandwidth from video segments
	totalBandwidth := 2000000 // Default 2Mbps
	if len(videoSegments) > 0 {
		// Calculate approximate bandwidth from segment sizes
		totalSize := int64(0)
		for _, seg := range videoSegments {
			segPath := filepath.Join(outputDir, seg)
			if info, err := os.Stat(segPath); err == nil {
				totalSize += info.Size()
			}
		}
		if totalSize > 0 && s.segmentDuration.Seconds() > 0 {
			// bandwidth = bits per second = (bytes * 8) / (duration * segment_count)
			totalBandwidth = int((totalSize * 8) / int64(s.segmentDuration.Seconds()*float64(len(videoSegments))))
		}
	}

	// Generate master playlist
	var masterPlaylist strings.Builder
	masterPlaylist.WriteString("#EXTM3U\n")
	masterPlaylist.WriteString("#EXT-X-VERSION:6\n") // v6 for fMP4 compatibility
	masterPlaylist.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")

	if len(audioSegments) > 0 {
		masterPlaylist.WriteString("#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"Audio\",DEFAULT=YES,AUTOSELECT=YES,URI=\"audio2_stream.m3u8\"\n")
	}

	if len(videoSegments) > 0 {
		// Primary variant with audio (for players that support Opus in fMP4)
		if len(audioSegments) > 0 {
			masterPlaylist.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.64001f,opus\",AUDIO=\"audio\"\n", totalBandwidth))
			masterPlaylist.WriteString("video1_stream.m3u8\n")
		}

		// Video-only variant (fallback for VLC and players with limited Opus support)
		// Use video-only bandwidth
		videoOnlyBandwidth := totalBandwidth * 95 / 100 // Approximate video-only bandwidth
		masterPlaylist.WriteString(fmt.Sprintf("#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.64001f\"\n", videoOnlyBandwidth))
		masterPlaylist.WriteString("video1_stream.m3u8\n")
	}

	playlistPath := filepath.Join(outputDir, "index.m3u8")
	if err := os.WriteFile(playlistPath, []byte(masterPlaylist.String()), 0644); err != nil {
		return fmt.Errorf("failed to write master playlist: %w", err)
	}
	log.Printf("Generated index.m3u8 (master playlist)")

	return nil
}

func main() {
	// Create HLS saver
	outputDir := "./hls-output"
	hlsFile, err := newHLSSaver(outputDir, 6*time.Second)
	if err != nil {
		panic(err)
	}

	// Start HTTP server to serve HLS stream (required for proper playback)
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>HLS Stream</title></head>
<body>
<h1>HLS Stream</h1>
<p>Open in VLC: <a href="http://localhost:8080/index.m3u8">http://localhost:8080/index.m3u8</a></p>
<video controls width="640"><source src="/index.m3u8" type="application/x-mpegURL"></video>
<script src="https://cdn.jsdelivr.net/npm/hls.js@latest"></script>
<script>
if(Hls.isSupported()) {
  var video = document.querySelector('video');
  var hls = new Hls();
  hls.loadSource('/index.m3u8');
  hls.attachMedia(video);
}
</script>
</body>
</html>`))
				return
			}
			hlsFile.muxer.Handle(w, r)
		})
		log.Println("HTTP server started on :8080")
		log.Println("Open http://localhost:8080 in your browser or VLC")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	peerConnection := createWebRTCConn(hlsFile)

	fmt.Printf("\n✅ HLS streaming started\n")
	fmt.Printf("📺 Open in VLC: http://localhost:8080/index.m3u8\n")
	fmt.Printf("🌐 Web player: http://localhost:8080\n")
	fmt.Printf("\nPress Ctrl+C to stop\n\n")

	closed := make(chan os.Signal, 1)
	signal.Notify(closed, os.Interrupt)
	<-closed

	fmt.Printf("\nShutting down...\n")
	if err := peerConnection.Close(); err != nil {
		panic(err)
	}

	if err := hlsFile.Close(); err != nil {
		panic(err)
	}

	fmt.Printf("✅ Stopped\n")
}

func createWebRTCConn(hlsFile *hlsSaver) *webrtc.PeerConnection {
	// Everything below is the Pion WebRTC API! Thanks for using it ❤️.

	// Prepare the configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	// Create a MediaEngine object to configure the supported codec
	mediaEngine := &webrtc.MediaEngine{}

	// Setup the codecs you want to use.
	// We support H264 for video
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		PayloadType:        102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}

	// We support Opus for audio
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	// Set a handler for when a new remote track starts
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			// Send a PLI on an interval so that the publisher is pushing a keyframe every 3 seconds
			go func() {
				ticker := time.NewTicker(time.Second * 3)
				defer ticker.Stop()
				for range ticker.C {
					if rtcpSendErr := peerConnection.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}}); rtcpSendErr != nil {
						fmt.Println(rtcpSendErr)
					}
				}
			}()
		}

		fmt.Printf("Track has started, of type %d: %s \n", track.PayloadType(), track.Codec().RTPCodecCapability.MimeType)
		for {
			// Read RTP packets being sent to Pion
			rtpPkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return
				}
				panic(readErr)
			}

			if track.Codec().MimeType == webrtc.MimeTypeH264 {
				if err := hlsFile.WriteRTP(rtpPkt); err != nil {
					fmt.Printf("Error writing H.264: %v\n", err)
				}
			} else if track.Codec().MimeType == webrtc.MimeTypeOpus {
				if err := hlsFile.WriteAudioRTP(rtpPkt); err != nil {
					fmt.Printf("Error writing Opus: %v\n", err)
				}
			}
		}
	})

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
	})

	// Wait for the offer to be pasted
	offer := webrtc.SessionDescription{}
	decode(readUntilNewline(), &offer)

	// Set the remote SessionDescription
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		panic(err)
	}

	// Create an answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and starts our UDP listeners
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	// Output the answer in base64 so we can paste it in browser
	fmt.Println(encode(peerConnection.LocalDescription()))

	return peerConnection
}

// Read from stdin until we get a newline.
func readUntilNewline() (in string) {
	var err error

	r := bufio.NewReader(os.Stdin)
	for {
		in, err = r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			panic(err)
		}

		if in = strings.TrimSpace(in); len(in) > 0 {
			break
		}
	}

	fmt.Println("")

	return
}

// JSON encode + base64 a SessionDescription.
func encode(obj *webrtc.SessionDescription) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return base64.StdEncoding.EncodeToString(b)
}

// Decode a base64 and unmarshal JSON into a SessionDescription.
func decode(in string, obj *webrtc.SessionDescription) {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}

	if err = json.Unmarshal(b, obj); err != nil {
		panic(err)
	}
}
