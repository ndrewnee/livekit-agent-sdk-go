package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
)

// GstPipeline manages a GStreamer pipeline using go-gst bindings
type GstPipeline struct {
	config     *Config
	sessionID  string
	outputDir  string

	pipeline   *gst.Pipeline
	videoSrc   *app.Source
	audioSrc   *app.Source

	bus        *gst.Bus
	mainLoop   *glib.MainLoop

	state      State
	stateMu    sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc

	// Statistics
	stats      PipelineStats
	statsTime  time.Time
}

// PipelineStats holds pipeline statistics
type PipelineStats struct {
	VideoPacketsReceived uint64
	AudioPacketsReceived uint64
	SegmentsWritten      uint64
	BytesWritten         uint64
	DroppedFrames        uint64
	LastSegmentTime      time.Time
}

// NewGstPipeline creates a new GStreamer pipeline using go-gst
func NewGstPipeline(config *Config, sessionID string) (*GstPipeline, error) {
	// Initialize GStreamer
	gst.Init(nil)

	ctx, cancel := context.WithCancel(context.Background())

	outputDir := fmt.Sprintf("%s/%s", config.OutputDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	p := &GstPipeline{
		config:     config,
		sessionID:  sessionID,
		outputDir:  outputDir,
		ctx:        ctx,
		cancel:     cancel,
		state:      StateStopped,
		statsTime:  time.Now(),
	}

	if err := p.createPipeline(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create pipeline: %w", err)
	}

	return p, nil
}

// createPipeline creates the GStreamer pipeline elements
func (p *GstPipeline) createPipeline() error {
	var err error

	// Create pipeline
	p.pipeline, err = gst.NewPipeline("egress-pipeline")
	if err != nil {
		return fmt.Errorf("failed to create pipeline: %w", err)
	}

	// Create elements
	elements := make(map[string]*gst.Element)

	// RTP bin for jitter buffering and synchronization
	rtpbin, err := gst.NewElement("rtpbin")
	if err != nil {
		return fmt.Errorf("failed to create rtpbin: %w", err)
	}
	rtpbin.SetProperty("latency", uint(p.config.JitterBufferMs))
	rtpbin.SetProperty("do-lost", true)
	rtpbin.SetProperty("drop-on-latency", false)
	elements["rtpbin"] = rtpbin

	// Video chain elements
	videoUdpSrc, err := gst.NewElement("udpsrc")
	if err != nil {
		return fmt.Errorf("failed to create video udpsrc: %w", err)
	}
	videoUdpSrc.SetProperty("port", p.config.VideoPort)
	videoUdpSrc.SetProperty("caps", gst.NewCapsFromString(
		fmt.Sprintf("application/x-rtp,media=video,clock-rate=90000,encoding-name=H264")))
	elements["video_udpsrc"] = videoUdpSrc

	rtph264depay, err := gst.NewElement("rtph264depay")
	if err != nil {
		return fmt.Errorf("failed to create rtph264depay: %w", err)
	}
	elements["rtph264depay"] = rtph264depay

	h264parse, err := gst.NewElement("h264parse")
	if err != nil {
		return fmt.Errorf("failed to create h264parse: %w", err)
	}
	h264parse.SetProperty("config-interval", -1)
	elements["h264parse"] = h264parse

	// Gap filling for video
	videorate, err := gst.NewElement("videorate")
	if err != nil {
		return fmt.Errorf("failed to create videorate: %w", err)
	}
	videorate.SetProperty("drop-only", false)
	videorate.SetProperty("duplicate-on-gap", true)
	videorate.SetProperty("skip-to-first", true)
	elements["videorate"] = videorate

	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return fmt.Errorf("failed to create video capsfilter: %w", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-h264,framerate=30/1"))
	elements["video_caps"] = videoCaps

	videoQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create video queue: %w", err)
	}
	videoQueue.SetProperty("max-size-time", uint64(2000000000)) // 2 seconds
	videoQueue.SetProperty("leaky", 2) // downstream
	elements["video_queue"] = videoQueue

	// Audio chain elements
	audioUdpSrc, err := gst.NewElement("udpsrc")
	if err != nil {
		return fmt.Errorf("failed to create audio udpsrc: %w", err)
	}
	audioUdpSrc.SetProperty("port", p.config.AudioPort)
	audioUdpSrc.SetProperty("caps", gst.NewCapsFromString(
		"application/x-rtp,media=audio,clock-rate=48000,encoding-name=OPUS"))
	elements["audio_udpsrc"] = audioUdpSrc

	rtpopusdepay, err := gst.NewElement("rtpopusdepay")
	if err != nil {
		return fmt.Errorf("failed to create rtpopusdepay: %w", err)
	}
	elements["rtpopusdepay"] = rtpopusdepay

	opusparse, err := gst.NewElement("opusparse")
	if err != nil {
		return fmt.Errorf("failed to create opusparse: %w", err)
	}
	elements["opusparse"] = opusparse

	// Audio processing based on mode
	var audioChainElements []*gst.Element

	switch p.config.AudioMode {
	case AudioTranscodeAAC:
		// Transcode to AAC
		opusdec, _ := gst.NewElement("opusdec")
		audioconvert, _ := gst.NewElement("audioconvert")
		audioresample, _ := gst.NewElement("audioresample")
		avenc_aac, _ := gst.NewElement("avenc_aac")
		avenc_aac.SetProperty("bitrate", p.config.AACBitrate*1000)
		avenc_aac.SetProperty("compliance", -2)
		aacparse, _ := gst.NewElement("aacparse")

		audioChainElements = []*gst.Element{opusdec, audioconvert, audioresample, avenc_aac, aacparse}

	case AudioTranscodeMP3:
		// Transcode to MP3
		opusdec, _ := gst.NewElement("opusdec")
		audioconvert, _ := gst.NewElement("audioconvert")
		audioresample, _ := gst.NewElement("audioresample")
		lamemp3enc, _ := gst.NewElement("lamemp3enc")
		lamemp3enc.SetProperty("target", 1) // bitrate
		lamemp3enc.SetProperty("bitrate", p.config.MP3Bitrate)
		mpegaudioparse, _ := gst.NewElement("mpegaudioparse")

		audioChainElements = []*gst.Element{opusdec, audioconvert, audioresample, lamemp3enc, mpegaudioparse}

	default: // AudioPassThrough
		// Gap filling for audio (zero-transcode)
		audiorate, _ := gst.NewElement("audiorate")
		audiorate.SetProperty("tolerance", uint64(40000000)) // 40ms
		audiorate.SetProperty("add", true)
		audiorate.SetProperty("silent", false)

		audioCaps, _ := gst.NewElement("capsfilter")
		audioCaps.SetProperty("caps", gst.NewCapsFromString("audio/x-opus,rate=48000,channels=2"))

		audioChainElements = []*gst.Element{audiorate, audioCaps}
	}

	audioQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create audio queue: %w", err)
	}
	audioQueue.SetProperty("max-size-time", uint64(2000000000)) // 2 seconds
	audioQueue.SetProperty("leaky", 2) // downstream
	elements["audio_queue"] = audioQueue

	// Muxer
	mpegtsmux, err := gst.NewElement("mpegtsmux")
	if err != nil {
		return fmt.Errorf("failed to create mpegtsmux: %w", err)
	}
	mpegtsmux.SetProperty("alignment", 7)
	elements["mux"] = mpegtsmux

	// HLS sink
	hlssink2, err := gst.NewElement("hlssink2")
	if err != nil {
		return fmt.Errorf("failed to create hlssink2: %w", err)
	}
	hlssink2.SetProperty("location", fmt.Sprintf("%s/segment%%05d.ts", p.outputDir))
	hlssink2.SetProperty("playlist-location", fmt.Sprintf("%s/playlist.m3u8", p.outputDir))
	hlssink2.SetProperty("target-duration", uint(p.config.SegmentDuration))
	hlssink2.SetProperty("max-files", uint(0)) // Keep all segments
	hlssink2.SetProperty("send-keyframe-requests", false)
	elements["hlssink2"] = hlssink2

	// Add all elements to pipeline
	for _, elem := range elements {
		if err := p.pipeline.Add(elem); err != nil {
			return fmt.Errorf("failed to add element to pipeline: %w", err)
		}
	}

	// Add audio chain elements
	for _, elem := range audioChainElements {
		if err := p.pipeline.Add(elem); err != nil {
			return fmt.Errorf("failed to add audio element to pipeline: %w", err)
		}
	}

	// Link video chain
	// udpsrc -> rtpbin
	videoUdpSrc.GetStaticPad("src").Link(rtpbin.GetRequestPad("recv_rtp_sink_0"))

	// rtpbin -> rtph264depay -> h264parse -> videorate -> caps -> queue -> mux
	rtpbin.Connect("pad-added", func(element *gst.Element, pad *gst.Pad) {
		if pad.GetName() == "recv_rtp_src_0_*" {
			pad.Link(rtph264depay.GetStaticPad("sink"))
		}
	})

	rtph264depay.Link(h264parse)
	h264parse.Link(videorate)
	videorate.Link(videoCaps)
	videoCaps.Link(videoQueue)
	videoQueue.Link(mpegtsmux)

	// Link audio chain
	// udpsrc -> rtpbin
	audioUdpSrc.GetStaticPad("src").Link(rtpbin.GetRequestPad("recv_rtp_sink_1"))

	// rtpbin -> rtpopusdepay -> opusparse -> [audio processing] -> queue -> mux
	rtpbin.Connect("pad-added", func(element *gst.Element, pad *gst.Pad) {
		if pad.GetName() == "recv_rtp_src_1_*" {
			pad.Link(rtpopusdepay.GetStaticPad("sink"))
		}
	})

	rtpopusdepay.Link(opusparse)

	// Link audio processing chain
	prevElem := opusparse
	for _, elem := range audioChainElements {
		if err := prevElem.Link(elem); err != nil {
			return fmt.Errorf("failed to link audio chain: %w", err)
		}
		prevElem = elem
	}
	prevElem.Link(audioQueue)
	audioQueue.Link(mpegtsmux)

	// Link mux to HLS sink
	mpegtsmux.Link(hlssink2)

	// Set up bus watch
	p.bus = p.pipeline.GetBus()
	p.bus.AddWatch(func(msg *gst.Message) bool {
		return p.handleBusMessage(msg)
	})

	// Create main loop for message handling
	p.mainLoop = glib.NewMainLoop(glib.MainContextDefault(), false)

	return nil
}

// handleBusMessage handles GStreamer bus messages
func (p *GstPipeline) handleBusMessage(msg *gst.Message) bool {
	switch msg.Type() {
	case gst.MessageEOS:
		log.Println("GStreamer: End of stream")
		p.mainLoop.Quit()
		return false

	case gst.MessageError:
		gerr := msg.ParseError()
		log.Printf("GStreamer error: %s", gerr.Error())
		p.handleError(gerr)
		return false

	case gst.MessageWarning:
		gerr := msg.ParseWarning()
		log.Printf("GStreamer warning: %s", gerr.Error())

	case gst.MessageStateChanged:
		oldState, newState := msg.ParseStateChanged()
		if msg.Source() == p.pipeline.GetName() {
			log.Printf("Pipeline state changed: %s -> %s",
				oldState.String(), newState.String())
			p.updateState(newState)
		}

	case gst.MessageElement:
		// Handle element-specific messages (e.g., from hlssink2)
		// HLS segment completion tracking
		p.stats.SegmentsWritten++
		p.stats.LastSegmentTime = time.Now()
	}

	return true
}

// updateState updates the pipeline state
func (p *GstPipeline) updateState(gstState gst.State) {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	switch gstState {
	case gst.StatePlaying:
		p.state = StatePlaying
	case gst.StatePaused:
		p.state = StatePaused
	case gst.StateReady, gst.StateNull:
		p.state = StateStopped
	}
}

// Start starts the pipeline
func (p *GstPipeline) Start() error {
	p.stateMu.Lock()
	if p.state != StateStopped {
		p.stateMu.Unlock()
		return fmt.Errorf("pipeline already running")
	}
	p.stateMu.Unlock()

	log.Printf("Starting GStreamer pipeline for session %s", p.sessionID)

	// Set pipeline to playing state
	if err := p.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to start pipeline: %w", err)
	}

	// Start main loop in goroutine
	go func() {
		p.mainLoop.Run()
	}()

	// Start statistics monitoring
	go p.monitorStatistics()

	return nil
}

// Stop stops the pipeline
func (p *GstPipeline) Stop() error {
	p.stateMu.Lock()
	if p.state == StateStopped {
		p.stateMu.Unlock()
		return nil
	}
	p.stateMu.Unlock()

	log.Printf("Stopping GStreamer pipeline for session %s", p.sessionID)

	// Send EOS to pipeline for graceful shutdown
	p.pipeline.SendEvent(gst.NewEOSEvent())

	// Wait for EOS with timeout
	done := make(chan bool, 1)
	go func() {
		time.Sleep(5 * time.Second)
		done <- false
	}()

	go func() {
		// Wait for EOS message on bus
		for {
			msg := p.bus.Pop()
			if msg != nil && msg.Type() == gst.MessageEOS {
				done <- true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()

	graceful := <-done
	if !graceful {
		log.Println("Timeout waiting for EOS, forcing shutdown")
	}

	// Stop the pipeline
	p.pipeline.SetState(gst.StateNull)

	// Quit main loop
	if p.mainLoop != nil {
		p.mainLoop.Quit()
	}

	// Cancel context
	p.cancel()

	return nil
}

// GetState returns the current pipeline state
func (p *GstPipeline) GetState() State {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.state
}

// GetStats returns pipeline statistics
func (p *GstPipeline) GetStats() PipelineStats {
	return p.stats
}

// monitorStatistics monitors pipeline performance
func (p *GstPipeline) monitorStatistics() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			// Log statistics
			log.Printf("Pipeline stats - Segments: %d, Bytes: %d, Last segment: %s ago",
				p.stats.SegmentsWritten,
				p.stats.BytesWritten,
				time.Since(p.stats.LastSegmentTime))
		}
	}
}

// handleError handles pipeline errors with recovery
func (p *GstPipeline) handleError(err error) {
	log.Printf("Pipeline error for session %s: %v", p.sessionID, err)

	// Implement crash recovery with exponential backoff
	go p.recoverFromCrash()
}

// recoverFromCrash attempts to recover from a pipeline crash
func (p *GstPipeline) recoverFromCrash() {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for attempt := 1; attempt <= 3; attempt++ {
		log.Printf("Attempting to restart pipeline (attempt %d/3)", attempt)

		time.Sleep(backoff)

		// Stop the current pipeline
		p.Stop()

		// Recreate pipeline
		if err := p.createPipeline(); err != nil {
			log.Printf("Failed to recreate pipeline: %v", err)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Start pipeline
		if err := p.Start(); err != nil {
			log.Printf("Failed to restart pipeline: %v", err)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		log.Println("Pipeline recovered successfully")
		return
	}

	log.Printf("Failed to recover pipeline after 3 attempts for session %s", p.sessionID)
}