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
	"github.com/pion/rtp"
)

// DirectPipeline manages a GStreamer pipeline using direct appsrc injection
// This eliminates the need for UDP ports and allows unlimited concurrent workers
type DirectPipeline struct {
	config    *Config
	sessionID string
	outputDir string

	pipeline  *gst.Pipeline
	videoSrc  *gst.Element // appsrc element
	audioSrc  *gst.Element // appsrc element

	bus      *gst.Bus
	mainLoop *glib.MainLoop

	state   State
	stateMu sync.RWMutex
	ctx     context.Context
	cancel  context.CancelFunc

	// Statistics
	stats     PipelineStats
	statsTime time.Time
}

// NewDirectPipeline creates a new GStreamer pipeline with direct RTP injection
func NewDirectPipeline(config *Config, sessionID string) (*DirectPipeline, error) {
	// Initialize GStreamer
	gst.Init(nil)

	ctx, cancel := context.WithCancel(context.Background())

	outputDir := fmt.Sprintf("%s/%s", config.OutputDir, sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	p := &DirectPipeline{
		config:    config,
		sessionID: sessionID,
		outputDir: outputDir,
		ctx:       ctx,
		cancel:    cancel,
		state:     StateStopped,
		statsTime: time.Now(),
	}

	if err := p.createPipeline(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create pipeline: %w", err)
	}

	return p, nil
}

// createPipeline creates the GStreamer pipeline with appsrc elements
func (p *DirectPipeline) createPipeline() error {
	var err error

	// Create pipeline
	p.pipeline, err = gst.NewPipeline("egress-direct-pipeline")
	if err != nil {
		return fmt.Errorf("failed to create pipeline: %w", err)
	}

	// Create appsrc for video RTP injection
	p.videoSrc, err = gst.NewElement("appsrc")
	if err != nil {
		return fmt.Errorf("failed to create video appsrc: %w", err)
	}
	p.videoSrc.SetProperty("is-live", true)
	p.videoSrc.SetProperty("format", gst.FormatTime)
	p.videoSrc.SetProperty("do-timestamp", false) // We'll set timestamps ourselves
	p.videoSrc.SetProperty("emit-signals", true) // Enable signal emission
	p.videoSrc.SetProperty("block", false) // Don't block when buffer is full
	p.videoSrc.SetProperty("max-bytes", uint64(10*1024*1024)) // 10MB buffer
	caps := gst.NewCapsFromString("application/x-rtp,media=video,clock-rate=90000,encoding-name=H264")
	p.videoSrc.SetProperty("caps", caps)

	// Create appsrc for audio RTP injection
	p.audioSrc, err = gst.NewElement("appsrc")
	if err != nil {
		return fmt.Errorf("failed to create audio appsrc: %w", err)
	}
	p.audioSrc.SetProperty("is-live", true)
	p.audioSrc.SetProperty("format", gst.FormatTime)
	p.audioSrc.SetProperty("do-timestamp", false) // We'll set timestamps ourselves
	p.audioSrc.SetProperty("emit-signals", true) // Enable signal emission
	p.audioSrc.SetProperty("block", false) // Don't block when buffer is full
	p.audioSrc.SetProperty("max-bytes", uint64(2*1024*1024)) // 2MB buffer
	caps = gst.NewCapsFromString("application/x-rtp,media=audio,clock-rate=48000,encoding-name=OPUS")
	p.audioSrc.SetProperty("caps", caps)

	// Video chain: appsrc -> jitterbuffer -> depay -> parse -> rate -> mux
	videoJitterBuffer, err := gst.NewElement("rtpjitterbuffer")
	if err != nil {
		return fmt.Errorf("failed to create video jitterbuffer: %w", err)
	}
	videoJitterBuffer.SetProperty("latency", uint(p.config.JitterBufferMs))
	videoJitterBuffer.SetProperty("do-lost", true)
	videoJitterBuffer.SetProperty("drop-on-latency", false)

	rtph264depay, err := gst.NewElement("rtph264depay")
	if err != nil {
		return fmt.Errorf("failed to create rtph264depay: %w", err)
	}

	h264parse, err := gst.NewElement("h264parse")
	if err != nil {
		return fmt.Errorf("failed to create h264parse: %w", err)
	}
	h264parse.SetProperty("config-interval", -1)

	// Gap filling for video
	videorate, err := gst.NewElement("videorate")
	if err != nil {
		return fmt.Errorf("failed to create videorate: %w", err)
	}
	videorate.SetProperty("drop-only", false)
	videorate.SetProperty("duplicate-on-gap", true)
	videorate.SetProperty("skip-to-first", true)

	videoCaps, err := gst.NewElement("capsfilter")
	if err != nil {
		return fmt.Errorf("failed to create video capsfilter: %w", err)
	}
	videoCaps.SetProperty("caps", gst.NewCapsFromString("video/x-h264,framerate=30/1"))

	videoQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create video queue: %w", err)
	}
	videoQueue.SetProperty("max-size-time", uint64(2000000000)) // 2 seconds
	videoQueue.SetProperty("leaky", 2)                           // downstream

	// Audio chain: appsrc -> jitterbuffer -> depay -> parse -> [processing] -> mux
	audioJitterBuffer, err := gst.NewElement("rtpjitterbuffer")
	if err != nil {
		return fmt.Errorf("failed to create audio jitterbuffer: %w", err)
	}
	audioJitterBuffer.SetProperty("latency", uint(p.config.JitterBufferMs))
	audioJitterBuffer.SetProperty("do-lost", true)
	audioJitterBuffer.SetProperty("drop-on-latency", false)

	rtpopusdepay, err := gst.NewElement("rtpopusdepay")
	if err != nil {
		return fmt.Errorf("failed to create rtpopusdepay: %w", err)
	}

	// Audio processing based on mode
	var audioChainElements []*gst.Element

	switch p.config.AudioMode {
	case AudioTranscodeAAC:
		// Transcode to AAC - needs opusparse -> opusdec
		opusparse, _ := gst.NewElement("opusparse")
		opusdec, _ := gst.NewElement("opusdec")
		audioconvert, _ := gst.NewElement("audioconvert")
		audioresample, _ := gst.NewElement("audioresample")
		avenc_aac, _ := gst.NewElement("avenc_aac")
		avenc_aac.SetProperty("bitrate", p.config.AACBitrate*1000)
		avenc_aac.SetProperty("compliance", -2)
		aacparse, _ := gst.NewElement("aacparse")

		audioChainElements = []*gst.Element{opusparse, opusdec, audioconvert, audioresample, avenc_aac, aacparse}

	case AudioTranscodeMP3:
		// Transcode to MP3 - needs opusparse -> opusdec
		opusparse, _ := gst.NewElement("opusparse")
		opusdec, _ := gst.NewElement("opusdec")
		audioconvert, _ := gst.NewElement("audioconvert")
		audioresample, _ := gst.NewElement("audioresample")
		lamemp3enc, _ := gst.NewElement("lamemp3enc")
		lamemp3enc.SetProperty("target", 1) // bitrate
		lamemp3enc.SetProperty("bitrate", p.config.MP3Bitrate)
		mpegaudioparse, _ := gst.NewElement("mpegaudioparse")

		audioChainElements = []*gst.Element{opusparse, opusdec, audioconvert, audioresample, lamemp3enc, mpegaudioparse}

	default: // AudioPassThrough
		// For passthrough, we just need opusparse (no decoding)
		opusparse, _ := gst.NewElement("opusparse")
		audioChainElements = []*gst.Element{opusparse}
	}

	audioQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create audio queue: %w", err)
	}
	audioQueue.SetProperty("max-size-time", uint64(2000000000)) // 2 seconds
	audioQueue.SetProperty("leaky", 2)                           // downstream

	// Muxer
	mpegtsmux, err := gst.NewElement("mpegtsmux")
	if err != nil {
		return fmt.Errorf("failed to create mpegtsmux: %w", err)
	}
	mpegtsmux.SetProperty("alignment", 7)

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
	hlssink2.SetProperty("async-handling", true) // Allow async state changes

	// Add all elements to pipeline
	elements := []*gst.Element{
		p.videoSrc,
		videoJitterBuffer,
		rtph264depay,
		h264parse,
		videorate,
		videoCaps,
		videoQueue,
		p.audioSrc,
		audioJitterBuffer,
		rtpopusdepay,
	}

	// Add audio processing elements
	elements = append(elements, audioChainElements...)
	elements = append(elements, audioQueue, mpegtsmux, hlssink2)

	for _, elem := range elements {
		if err := p.pipeline.Add(elem); err != nil {
			return fmt.Errorf("failed to add element to pipeline: %w", err)
		}
	}

	// Link video chain
	// appsrc -> jitterbuffer -> depay -> parse -> rate -> caps -> queue -> mux
	p.videoSrc.Link(videoJitterBuffer)
	videoJitterBuffer.Link(rtph264depay)
	rtph264depay.Link(h264parse)
	h264parse.Link(videorate)
	videorate.Link(videoCaps)
	videoCaps.Link(videoQueue)
	videoQueue.Link(mpegtsmux)

	// Link audio chain
	// appsrc -> jitterbuffer -> depay -> [processing] -> queue -> mux
	p.audioSrc.Link(audioJitterBuffer)
	audioJitterBuffer.Link(rtpopusdepay)

	// Link audio processing chain
	prevElem := rtpopusdepay
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

// InjectVideoRTP injects an RTP packet directly into the video pipeline
func (p *DirectPipeline) InjectVideoRTP(packet *rtp.Packet) error {
	if p.state != StatePlaying {
		return fmt.Errorf("pipeline not playing")
	}

	// Marshal RTP packet to bytes
	data, err := packet.Marshal()
	if err != nil {
		p.stats.DroppedFrames++
		return fmt.Errorf("failed to marshal RTP packet: %w", err)
	}

	// Create GStreamer buffer
	buffer := gst.NewBufferFromBytes(data)

	// Set presentation timestamp from RTP timestamp
	// For H.264, clock rate is 90000 Hz
	pts := gst.ClockTime(uint64(packet.Timestamp) * uint64(gst.ClockTime(1000000000)) / 90000)
	buffer.SetPresentationTimestamp(pts)

	// Push buffer to video appsrc using the Emit signal method
	ret, err := p.videoSrc.Emit("push-buffer", buffer)
	if err != nil {
		p.stats.DroppedFrames++
		return fmt.Errorf("failed to push video buffer: %w", err)
	}
	_ = ret

	p.stats.VideoPacketsReceived++
	return nil
}

// InjectAudioRTP injects an RTP packet directly into the audio pipeline
func (p *DirectPipeline) InjectAudioRTP(packet *rtp.Packet) error {
	if p.state != StatePlaying {
		return fmt.Errorf("pipeline not playing")
	}

	// Marshal RTP packet to bytes
	data, err := packet.Marshal()
	if err != nil {
		p.stats.DroppedFrames++
		return fmt.Errorf("failed to marshal RTP packet: %w", err)
	}

	// Create GStreamer buffer
	buffer := gst.NewBufferFromBytes(data)

	// Set presentation timestamp from RTP timestamp
	// For Opus, clock rate is 48000 Hz
	pts := gst.ClockTime(uint64(packet.Timestamp) * uint64(gst.ClockTime(1000000000)) / 48000)
	buffer.SetPresentationTimestamp(pts)

	// Push buffer to audio appsrc using the Emit signal method
	ret, err := p.audioSrc.Emit("push-buffer", buffer)
	if err != nil {
		p.stats.DroppedFrames++
		return fmt.Errorf("failed to push audio buffer: %w", err)
	}
	_ = ret

	p.stats.AudioPacketsReceived++
	return nil
}

// handleBusMessage handles GStreamer bus messages
func (p *DirectPipeline) handleBusMessage(msg *gst.Message) bool {
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
func (p *DirectPipeline) updateState(gstState gst.State) {
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
func (p *DirectPipeline) Start() error {
	p.stateMu.Lock()
	if p.state != StateStopped {
		p.stateMu.Unlock()
		return fmt.Errorf("pipeline already running")
	}
	p.stateMu.Unlock()

	log.Printf("Starting GStreamer direct pipeline for session %s", p.sessionID)

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
func (p *DirectPipeline) Stop() error {
	p.stateMu.Lock()
	if p.state == StateStopped {
		p.stateMu.Unlock()
		return nil
	}
	p.stateMu.Unlock()

	log.Printf("Stopping GStreamer direct pipeline for session %s", p.sessionID)

	// Send EOS to both appsrc elements
	p.videoSrc.Emit("end-of-stream")
	p.audioSrc.Emit("end-of-stream")

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
func (p *DirectPipeline) GetState() State {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	return p.state
}

// GetStats returns pipeline statistics
func (p *DirectPipeline) GetStats() PipelineStats {
	return p.stats
}

// monitorStatistics monitors pipeline performance
func (p *DirectPipeline) monitorStatistics() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			// Log statistics
			log.Printf("Pipeline stats [%s] - Video: %d, Audio: %d, Segments: %d, Dropped: %d",
				p.sessionID,
				p.stats.VideoPacketsReceived,
				p.stats.AudioPacketsReceived,
				p.stats.SegmentsWritten,
				p.stats.DroppedFrames)
		}
	}
}

// handleError handles pipeline errors with recovery
func (p *DirectPipeline) handleError(err error) {
	log.Printf("Pipeline error for session %s: %v", p.sessionID, err)

	// Implement crash recovery with exponential backoff
	go p.recoverFromCrash()
}

// recoverFromCrash attempts to recover from a pipeline crash
func (p *DirectPipeline) recoverFromCrash() {
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