package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// PipelineState represents the state of the GStreamer pipeline
type PipelineState int

const (
	PipelineStateStopped PipelineState = iota
	PipelineStatePlaying
	PipelineStatePaused
)

// PipelineManager manages the GStreamer pipeline
type PipelineManager struct {
	config    *EgressConfig
	process   *exec.Cmd
	state     PipelineState
	stateMu   sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
}

// PipelineBuilder builds GStreamer pipeline strings
type PipelineBuilder struct {
	config *EgressConfig
}

// NewPipelineManager creates a new pipeline manager
func NewPipelineManager(config *EgressConfig) *PipelineManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &PipelineManager{
		config: config,
		state:  PipelineStateStopped,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start starts the GStreamer pipeline
func (pm *PipelineManager) Start() error {
	pm.stateMu.Lock()
	defer pm.stateMu.Unlock()

	if pm.state != PipelineStateStopped {
		return fmt.Errorf("pipeline already running")
	}

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(pm.config.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Build pipeline string
	builder := &PipelineBuilder{config: pm.config}
	pipelineStr := builder.Build()

	log.Printf("Starting GStreamer pipeline: %s", pipelineStr)

	// Start GStreamer process
	pm.process = exec.CommandContext(pm.ctx, "gst-launch-1.0", "-e", pipelineStr)

	// Set environment for debugging
	pm.process.Env = append(os.Environ(),
		"GST_DEBUG=2",
		fmt.Sprintf("GST_DEBUG_FILE=%s/gstreamer.log", pm.config.OutputDir),
	)

	// Capture stderr for debugging
	pm.process.Stderr = os.Stderr

	// Start process
	if err := pm.process.Start(); err != nil {
		return fmt.Errorf("failed to start GStreamer: %w", err)
	}

	pm.state = PipelineStatePlaying

	// Monitor process
	go pm.monitorProcess()

	return nil
}

// Stop stops the GStreamer pipeline
func (pm *PipelineManager) Stop() error {
	pm.stateMu.Lock()
	defer pm.stateMu.Unlock()

	if pm.state == PipelineStateStopped {
		return nil
	}

	pm.cancel() // Cancel context

	if pm.process != nil && pm.process.Process != nil {
		// Send SIGTERM for graceful shutdown
		if err := pm.process.Process.Signal(syscall.SIGTERM); err != nil {
			// Force kill if SIGTERM fails
			pm.process.Process.Kill()
		}

		// Wait for process to exit with timeout
		done := make(chan error, 1)
		go func() {
			done <- pm.process.Wait()
		}()

		select {
		case <-done:
			// Process exited
		case <-time.After(5 * time.Second):
			// Force kill after timeout
			pm.process.Process.Kill()
		}
	}

	pm.state = PipelineStateStopped
	return nil
}

// GetState returns the current pipeline state
func (pm *PipelineManager) GetState() PipelineState {
	pm.stateMu.RLock()
	defer pm.stateMu.RUnlock()
	return pm.state
}

// monitorProcess monitors the GStreamer process
func (pm *PipelineManager) monitorProcess() {
	err := pm.process.Wait()

	pm.stateMu.Lock()
	pm.state = PipelineStateStopped
	pm.stateMu.Unlock()

	if err != nil && pm.ctx.Err() == nil {
		// Process crashed, not intentional stop
		log.Printf("GStreamer process crashed: %v", err)
		// Implement restart logic here
		pm.handleCrash()
	}
}

// handleCrash handles pipeline crashes with exponential backoff restart
func (pm *PipelineManager) handleCrash() {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for attempt := 1; attempt <= 3; attempt++ {
		log.Printf("Attempting to restart GStreamer (attempt %d/3)", attempt)

		time.Sleep(backoff)

		if err := pm.Start(); err == nil {
			log.Println("GStreamer restarted successfully")
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	log.Println("Failed to restart GStreamer after 3 attempts")
}

// Build builds the GStreamer pipeline string
func (pb *PipelineBuilder) Build() string {
	var pipeline strings.Builder

	// RTP bin for jitter buffer and sync
	pipeline.WriteString(pb.buildRTPBin())
	pipeline.WriteString(" ")

	// Video chain
	pipeline.WriteString(pb.buildVideoChain())
	pipeline.WriteString(" ")

	// Audio chain
	pipeline.WriteString(pb.buildAudioChain())

	// Optional screenshot branch
	if pb.config.EnableScreenshots {
		pipeline.WriteString(" ")
		pipeline.WriteString(pb.buildScreenshotBranch())
	}

	return pipeline.String()
}

// buildRTPBin builds the RTP bin configuration
func (pb *PipelineBuilder) buildRTPBin() string {
	return fmt.Sprintf("rtpbin name=rtpbin latency=%d do-lost=true drop-on-latency=false",
		pb.config.JitterBufferMs)
}

// buildVideoChain builds the video processing chain
func (pb *PipelineBuilder) buildVideoChain() string {
	return fmt.Sprintf(`
		udpsrc port=%d caps="application/x-rtp,media=video,clock-rate=90000,encoding-name=H264"
		! rtpbin.recv_rtp_sink_0
		rtpbin. ! rtph264depay ! h264parse config-interval=-1
		! videorate drop-only=false duplicate-on-gap=true skip-to-first=true
		! video/x-h264,framerate=30/1
		! queue max-size-time=2000000000 leaky=downstream
		! mpegtsmux name=mux alignment=7
		! hlssink2
			location=%s/segment%%05d.ts
			playlist-location=%s/playlist.m3u8
			target-duration=%d
			max-files=0
			send-keyframe-requests=false`,
		pb.config.VideoPort,
		pb.config.OutputDir,
		pb.config.OutputDir,
		pb.config.SegmentDuration)
}

// buildAudioChain builds the audio processing chain
func (pb *PipelineBuilder) buildAudioChain() string {
	// Base RTP reception
	base := fmt.Sprintf(`
		udpsrc port=%d caps="application/x-rtp,media=audio,clock-rate=48000,encoding-name=OPUS"
		! rtpbin.recv_rtp_sink_1
		rtpbin. ! rtpopusdepay ! opusparse`,
		pb.config.AudioPort)

	// Choose audio processing based on mode
	switch pb.config.AudioMode {
	case AudioTranscodeAAC:
		// Transcode Opus to AAC for maximum compatibility
		bitrate := pb.config.AACBitrate
		if bitrate == 0 {
			bitrate = 192 // Default
		}
		return base + fmt.Sprintf(`
		! opusdec
		! audioconvert ! audioresample
		! audio/x-raw,rate=48000,channels=2
		! avenc_aac bitrate=%d compliance=-2
		! aacparse
		! queue max-size-time=2000000000 leaky=downstream
		! mux.`, bitrate*1000) // Convert kbps to bps

	case AudioTranscodeMP3:
		// Transcode Opus to MP3 (lower CPU than AAC)
		bitrate := pb.config.MP3Bitrate
		if bitrate == 0 {
			bitrate = 192 // Default
		}
		return base + fmt.Sprintf(`
		! opusdec
		! audioconvert ! audioresample
		! audio/x-raw,rate=48000,channels=2
		! lamemp3enc target=bitrate bitrate=%d
		! mpegaudioparse
		! queue max-size-time=2000000000 leaky=downstream
		! mux.`, bitrate)

	default: // AudioPassThrough
		// Keep Opus as-is (zero-transcode)
		return base + `
		! audiorate tolerance=40000000 add=true silent=false
		! audio/x-opus,rate=48000,channels=2
		! queue max-size-time=2000000000 leaky=downstream
		! mux.`
	}
}

// buildScreenshotBranch builds the screenshot extraction branch
func (pb *PipelineBuilder) buildScreenshotBranch() string {
	interval := pb.config.ScreenshotInterval
	if interval == 0 {
		interval = 5 // Default to 5 seconds
	}

	// Create screenshots directory
	screenshotDir := fmt.Sprintf("%s/screenshots", pb.config.OutputDir)
	os.MkdirAll(screenshotDir, 0755)

	return fmt.Sprintf(`
		h264parse ! tee name=video_tee
		video_tee. ! queue ! videorate ! mux.
		video_tee. ! queue leaky=downstream
		! avdec_h264 ! videorate ! video/x-raw,framerate=1/%d
		! jpegenc quality=85
		! multifilesink location=%s/frame_%%05d.jpg`,
		interval,
		screenshotDir)
}