package main

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/livekit/server-sdk-go/v2/pkg/samplebuilder"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

// RecorderManager manages multiple participant recorders
type RecorderManager struct {
	mu        sync.RWMutex
	recorders map[string]*ParticipantRecorder
	config    *Config
}

func NewRecorderManager(config *Config) *RecorderManager {
	return &RecorderManager{
		recorders: make(map[string]*ParticipantRecorder),
		config:    config,
	}
}

func (rm *RecorderManager) CreateRecorder(participantIdentity, roomName string) (*ParticipantRecorder, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Check if recorder already exists
	if _, exists := rm.recorders[participantIdentity]; exists {
		return nil, fmt.Errorf("recorder already exists for participant: %s", participantIdentity)
	}

	// Create output directory
	outputDir := fmt.Sprintf("%s/%s/%s", rm.config.OutputDir, roomName, participantIdentity)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	recorder := &ParticipantRecorder{
		participantIdentity: participantIdentity,
		roomName:            roomName,
		outputDir:           outputDir,
		config:              rm.config,
		startTime:           time.Now(),
		videoReady:          make(chan struct{}),
		audioReady:          make(chan struct{}),
		done:                make(chan struct{}),
	}

	rm.recorders[participantIdentity] = recorder
	log.Printf("Created recorder for participant %s in room %s, output: %s", participantIdentity, roomName, outputDir)

	return recorder, nil
}

func (rm *RecorderManager) GetRecorder(participantIdentity string) *ParticipantRecorder {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.recorders[participantIdentity]
}

func (rm *RecorderManager) RemoveRecorder(participantIdentity string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	delete(rm.recorders, participantIdentity)
	log.Printf("Removed recorder for participant: %s", participantIdentity)
}

func (rm *RecorderManager) PrintSummary() {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	log.Println("=== Recording Summary ===")
	for identity, recorder := range rm.recorders {
		duration := time.Since(recorder.startTime)
		log.Printf("Participant: %s", identity)
		log.Printf("  Room: %s", recorder.roomName)
		log.Printf("  Output: %s", recorder.outputDir)
		log.Printf("  Duration: %s", duration.Round(time.Second))
		log.Printf("  Video initialized: %v", recorder.videoInitialized)
		log.Printf("  Audio initialized: %v", recorder.audioInitialized)
	}
	log.Println("========================")
}

// ParticipantRecorder records a single participant's tracks
type ParticipantRecorder struct {
	participantIdentity string
	roomName            string
	outputDir           string
	config              *Config
	startTime           time.Time

	// GStreamer
	pipeline    *gst.Pipeline
	videoAppSrc *app.Source
	audioAppSrc *app.Source

	// Synchronization
	mu               sync.Mutex
	videoInitialized bool
	audioInitialized bool
	videoReady       chan struct{} // Signals when first video keyframe received
	audioReady       chan struct{} // Signals when first audio packet received
	done             chan struct{} // Signals recorder shutdown

	// Statistics
	videoPacketCount      int
	videoEmptyPacketCount int
	videoKeyframeCount    int
	audioPacketCount      int
	audioEmptyPacketCount int
	videoBytesReceived    int64
	audioBytesReceived    int64
}

// InitGStreamer initializes the GStreamer pipeline
func (r *ParticipantRecorder) InitGStreamer() error {
	gst.Init(nil)

	// Create pipeline based on configuration
	// Similar to save-to-hls-gstreamer but with configurable audio/video
	pipelineStr := fmt.Sprintf(`
		filesink location=%s/output.ts name=sink

		mpegtsmux name=mux ! sink.
	`, r.outputDir)

	if r.config.EnableVideo {
		pipelineStr += `
		appsrc name=videosrc format=time is-live=true do-timestamp=true
		! rtpjitterbuffer latency=200
		! rtph264depay
		! h264parse
		! video/x-h264,stream-format=byte-stream,alignment=au
		! queue max-size-buffers=0 max-size-time=0 max-size-bytes=0
		! mux.
		`
	}

	if r.config.EnableAudio {
		pipelineStr += `
		appsrc name=audiosrc format=time is-live=true do-timestamp=true
		! rtpjitterbuffer latency=200
		! rtpopusdepay
		! opusdec
		! audioconvert
		! avenc_aac bitrate=128000
		! aacparse
		! queue max-size-buffers=0 max-size-time=0 max-size-bytes=0
		! mux.
		`
	}

	log.Printf("[%s] Creating GStreamer pipeline:\n%s", r.participantIdentity, pipelineStr)

	pipeline, err := gst.NewPipelineFromString(pipelineStr)
	if err != nil {
		return fmt.Errorf("failed to create pipeline: %w", err)
	}

	r.pipeline = pipeline

	// Get appsrc elements
	if r.config.EnableVideo {
		videoSrcElement, err := r.pipeline.GetElementByName("videosrc")
		if err != nil {
			return fmt.Errorf("failed to get videosrc: %w", err)
		}
		r.videoAppSrc = app.SrcFromElement(videoSrcElement)
	}

	if r.config.EnableAudio {
		audioSrcElement, err := r.pipeline.GetElementByName("audiosrc")
		if err != nil {
			return fmt.Errorf("failed to get audiosrc: %w", err)
		}
		r.audioAppSrc = app.SrcFromElement(audioSrcElement)
	}

	// Set up bus message handling
	r.pipeline.GetPipelineBus().AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageEOS:
			log.Printf("[%s] EOS received, stopping pipeline", r.participantIdentity)
			r.pipeline.BlockSetState(gst.StateNull)
			return false

		case gst.MessageError:
			gerr := msg.ParseError()
			log.Printf("[%s] GStreamer error: %s (debug: %s)", r.participantIdentity, gerr.Error(), gerr.DebugString())
			r.pipeline.BlockSetState(gst.StateNull)
			return false

		case gst.MessageWarning:
			gw := msg.ParseWarning()
			log.Printf("[%s] GStreamer warning: %s", r.participantIdentity, gw.Error())

		case gst.MessageInfo:
			info := msg.ParseInfo()
			log.Printf("[%s] GStreamer info: %s", r.participantIdentity, info.Error())
		}
		return true
	})

	// Start pipeline immediately in PLAYING state like save-to-hls-gstreamer
	// The mux needs to be running BEFORE any data arrives to properly link pads
	if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to start pipeline: %w", err)
	}

	log.Printf("[%s] GStreamer pipeline initialized and started", r.participantIdentity)
	return nil
}

// H.264 NAL unit types
const (
	nalUnitTypeSPS   = 7  // Sequence Parameter Set
	nalUnitTypePPS   = 8  // Picture Parameter Set
	nalUnitTypeIDR   = 5  // IDR (Instantaneous Decoder Refresh) - keyframe
	nalUnitTypeSTAPA = 24 // STAP-A (aggregation packet)
	nalUnitTypeFUA   = 28 // FU-A (fragmentation unit)
)

// isH264Keyframe checks if an H.264 RTP payload contains a keyframe
func isH264Keyframe(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	nalType := payload[0] & 0x1F

	switch nalType {
	case nalUnitTypeSPS, nalUnitTypePPS, nalUnitTypeIDR:
		return true
	case nalUnitTypeSTAPA:
		// STAP-A: check if it contains SPS/PPS/IDR
		offset := 1
		for offset < len(payload) {
			if offset+2 > len(payload) {
				break
			}
			nalSize := int(payload[offset])<<8 | int(payload[offset+1])
			offset += 2

			if offset+nalSize > len(payload) {
				break
			}

			if nalSize > 0 {
				innerNalType := payload[offset] & 0x1F
				if innerNalType == nalUnitTypeSPS || innerNalType == nalUnitTypePPS || innerNalType == nalUnitTypeIDR {
					return true
				}
			}
			offset += nalSize
		}
		return false
	case nalUnitTypeFUA:
		// FU-A: check if it's the start of an IDR
		if len(payload) < 2 {
			return false
		}
		fuHeader := payload[1]
		startBit := (fuHeader & 0x80) != 0
		nalType := fuHeader & 0x1F
		return startBit && nalType == nalUnitTypeIDR
	default:
		return false
	}
}

// HandleVideoTrack processes incoming video RTP packets with all 4 best practices:
// 1. Continuously read packets (until done channel closes)
// 2. Skip empty packets
// 3. Wait for first keyframe before processing
// 4. Use proper synchronization (videoReady channel)
func (r *ParticipantRecorder) HandleVideoTrack(track *webrtc.TrackRemote, pliWriter func(webrtc.SSRC)) {
	if !r.config.EnableVideo || r.videoAppSrc == nil {
		log.Printf("[%s] Video recording disabled or not initialized", r.participantIdentity)
		return
	}

	log.Printf("[%s] 📹 Starting video track handler (SSRC=%d, Codec=%s, ID=%s)",
		r.participantIdentity, track.SSRC(), track.Codec().MimeType, track.ID())

	// Create SampleBuilder for H.264 to reassemble fragmented RTP packets
	const maxVideoLate = 1000
	sb := samplebuilder.New(maxVideoLate, &codecs.H264Packet{}, track.Codec().ClockRate,
		samplebuilder.WithPacketDroppedHandler(func() {
			log.Printf("[%s] Video packet dropped, requesting keyframe via PLI for SSRC=%d", r.participantIdentity, track.SSRC())
			pliWriter(track.SSRC())
		}))

	videoReadySignaled := false
	firstPacket := true

	// 1. ✅ CONTINUOUSLY read packets (don't stop after N packets)
	for {
		// Check if recorder is stopped
		select {
		case <-r.done:
			log.Printf("[%s] 📹 Video handler stopping (done signal received)", r.participantIdentity)
			return
		default:
		}

		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("[%s] 📹 Video track read error: %v", r.participantIdentity, err)
			return
		}

		r.mu.Lock()
		r.videoPacketCount++
		packetNum := r.videoPacketCount
		r.mu.Unlock()

		// Request PLI after first packet
		if firstPacket {
			firstPacket = false
			log.Printf("[%s] 🔑 First video packet received (seq=%d), requesting keyframe via PLI",
				r.participantIdentity, rtpPacket.SequenceNumber)
			pliWriter(track.SSRC())
		}

		// 2. ✅ SKIP empty packets
		if len(rtpPacket.Payload) == 0 {
			r.mu.Lock()
			r.videoEmptyPacketCount++
			emptyCount := r.videoEmptyPacketCount
			r.mu.Unlock()

			if emptyCount <= 3 {
				log.Printf("[%s] ⏭️  Video packet %d EMPTY (seq=%d) - skipping",
					r.participantIdentity, packetNum, rtpPacket.SequenceNumber)
			}
			continue // ← CRITICAL: Skip processing
		}

		// Packet has payload
		r.mu.Lock()
		r.videoBytesReceived += int64(len(rtpPacket.Payload))
		r.mu.Unlock()

		// 3. ✅ WAIT for first H.264 keyframe before processing
		if !videoReadySignaled {
			isKeyframe := isH264Keyframe(rtpPacket.Payload)

			if isKeyframe {
				r.mu.Lock()
				r.videoKeyframeCount++
				r.mu.Unlock()

				log.Printf("[%s] 🔑 KEYFRAME received at packet %d (seq=%d, payload=%d bytes)",
					r.participantIdentity, packetNum, rtpPacket.SequenceNumber, len(rtpPacket.Payload))
				log.Printf("[%s] ✅ Video receiver ready for processing!", r.participantIdentity)

				// 4. ✅ PROPER synchronization (no hardcoded delays)
				close(r.videoReady)
				videoReadySignaled = true
			} else {
				// Still waiting for keyframe
				if packetNum <= 3 {
					log.Printf("[%s] ⏳ Video packet %d has payload (%d bytes) but NOT a keyframe - waiting...",
						r.participantIdentity, packetNum, len(rtpPacket.Payload))
				}
				continue // Skip non-keyframe packets until we get first keyframe
			}
		}

		// Check if this is a keyframe (after first one)
		if videoReadySignaled && isH264Keyframe(rtpPacket.Payload) {
			r.mu.Lock()
			r.videoKeyframeCount++
			r.mu.Unlock()
		}

		// Push to SampleBuilder to reassemble fragmented packets
		sb.Push(rtpPacket)

		// Pop reassembled packets from SampleBuilder
		for _, p := range sb.PopPackets() {
			r.mu.Lock()
			if !r.videoInitialized {
				// Set caps on first packet (H.264 video)
				capsStr := fmt.Sprintf("application/x-rtp,media=(string)video,encoding-name=(string)H264,clock-rate=(int)90000,payload=(int)%d", p.PayloadType)
				caps := gst.NewCapsFromString(capsStr)
				r.videoAppSrc.SetProperty("caps", caps)
				r.videoInitialized = true
				log.Printf("[%s] Video stream initialized with caps: %s", r.participantIdentity, capsStr)
			}
			packetsPushed := r.videoPacketCount
			r.mu.Unlock()

			if packetsPushed%100 == 0 {
				log.Printf("[%s] 📦 Video: processed %d packets", r.participantIdentity, packetsPushed)
			}

			// Marshal the reassembled RTP packet
			data, err := p.Marshal()
			if err != nil {
				log.Printf("[%s] Failed to marshal video RTP packet: %v", r.participantIdentity, err)
				continue
			}

			// Create GStreamer buffer with RTP timestamp as PTS
			buffer := gst.NewBufferFromBytes(data)
			ptsNs := uint64(p.Timestamp) * 1000000000 / 90000
			buffer.SetPresentationTimestamp(gst.ClockTime(ptsNs))

			// Push to appsrc
			if flow := r.videoAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
				if flow != gst.FlowFlushing {
					log.Printf("[%s] Video appsrc push failed: %s", r.participantIdentity, flow.String())
				}
				return
			}
		}
	}
}

// HandleAudioTrack processes incoming audio RTP packets with all 4 best practices:
// 1. Continuously read packets (until done channel closes)
// 2. Skip empty packets
// 3. Signal ready on first valid packet (audio doesn't need keyframe detection)
// 4. Use proper synchronization (audioReady channel)
func (r *ParticipantRecorder) HandleAudioTrack(track *webrtc.TrackRemote) {
	if !r.config.EnableAudio || r.audioAppSrc == nil {
		log.Printf("[%s] Audio recording disabled or not initialized", r.participantIdentity)
		return
	}

	log.Printf("[%s] 🔊 Starting audio track handler (SSRC=%d, Codec=%s, ID=%s)",
		r.participantIdentity, track.SSRC(), track.Codec().MimeType, track.ID())

	audioReadySignaled := false

	// 1. ✅ CONTINUOUSLY read packets (don't stop after N packets)
	for {
		// Check if recorder is stopped
		select {
		case <-r.done:
			log.Printf("[%s] 🔊 Audio handler stopping (done signal received)", r.participantIdentity)
			return
		default:
		}

		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("[%s] 🔊 Audio track read error: %v", r.participantIdentity, err)
			return
		}

		r.mu.Lock()
		r.audioPacketCount++
		packetNum := r.audioPacketCount
		r.mu.Unlock()

		// 2. ✅ SKIP empty packets
		if len(rtpPacket.Payload) == 0 {
			r.mu.Lock()
			r.audioEmptyPacketCount++
			emptyCount := r.audioEmptyPacketCount
			r.mu.Unlock()

			if emptyCount <= 3 {
				log.Printf("[%s] ⏭️  Audio packet %d EMPTY (seq=%d) - skipping",
					r.participantIdentity, packetNum, rtpPacket.SequenceNumber)
			}
			continue // ← CRITICAL: Skip processing
		}

		// Packet has payload
		r.mu.Lock()
		r.audioBytesReceived += int64(len(rtpPacket.Payload))
		r.mu.Unlock()

		// 3. ✅ Signal ready on first packet with data (audio doesn't need keyframe)
		if !audioReadySignaled {
			log.Printf("[%s] 🔊 First audio packet with data at packet %d (seq=%d, payload=%d bytes)",
				r.participantIdentity, packetNum, rtpPacket.SequenceNumber, len(rtpPacket.Payload))
			log.Printf("[%s] ✅ Audio receiver ready for processing!", r.participantIdentity)

			// 4. ✅ PROPER synchronization (no hardcoded delays)
			close(r.audioReady)
			audioReadySignaled = true
		}

		r.mu.Lock()
		if !r.audioInitialized {
			// Set caps on first packet
			capsStr := fmt.Sprintf("application/x-rtp,media=(string)audio,encoding-name=(string)OPUS,clock-rate=(int)48000,payload=(int)%d", rtpPacket.PayloadType)
			caps := gst.NewCapsFromString(capsStr)
			r.audioAppSrc.SetProperty("caps", caps)
			r.audioInitialized = true
			log.Printf("[%s] Audio stream initialized with caps: %s", r.participantIdentity, capsStr)
		}
		packetsPushed := r.audioPacketCount
		r.mu.Unlock()

		if packetsPushed%100 == 0 {
			log.Printf("[%s] 📦 Audio: processed %d packets", r.participantIdentity, packetsPushed)
		}

		// Marshal RTP packet
		data, err := rtpPacket.Marshal()
		if err != nil {
			log.Printf("[%s] Failed to marshal audio RTP packet: %v", r.participantIdentity, err)
			continue
		}

		// Create GStreamer buffer with RTP timestamp as PTS
		buffer := gst.NewBufferFromBytes(data)
		ptsNs := uint64(rtpPacket.Timestamp) * 1000000000 / 48000
		buffer.SetPresentationTimestamp(gst.ClockTime(ptsNs))

		// Push to appsrc
		if flow := r.audioAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
			if flow != gst.FlowFlushing {
				log.Printf("[%s] Audio appsrc push failed: %s", r.participantIdentity, flow.String())
			}
			return
		}
	}
}

// Stop gracefully stops the recorder
func (r *ParticipantRecorder) Stop() {
	log.Printf("[%s] Stopping recorder...", r.participantIdentity)

	// Signal handlers to stop
	close(r.done)

	if r.videoAppSrc != nil {
		r.videoAppSrc.EndStream()
	}
	if r.audioAppSrc != nil {
		r.audioAppSrc.EndStream()
	}

	if r.pipeline != nil {
		r.pipeline.SendEvent(gst.NewEOSEvent())
		// Wait for EOS to propagate
		r.pipeline.GetBus().TimedPopFiltered(gst.ClockTime(5*1000000000), gst.MessageEOS|gst.MessageError)
		r.pipeline.SetState(gst.StateNull)
	}

	// Print statistics
	r.mu.Lock()
	log.Printf("[%s] ═══════════════════════════════════════", r.participantIdentity)
	log.Printf("[%s] RECORDING STATISTICS", r.participantIdentity)
	log.Printf("[%s] ═══════════════════════════════════════", r.participantIdentity)
	if r.config.EnableVideo {
		log.Printf("[%s] Video:", r.participantIdentity)
		log.Printf("[%s]   Total packets: %d", r.participantIdentity, r.videoPacketCount)
		log.Printf("[%s]   Empty packets (skipped): %d", r.participantIdentity, r.videoEmptyPacketCount)
		log.Printf("[%s]   Keyframes detected: %d", r.participantIdentity, r.videoKeyframeCount)
		log.Printf("[%s]   Bytes received: %d", r.participantIdentity, r.videoBytesReceived)
	}
	if r.config.EnableAudio {
		log.Printf("[%s] Audio:", r.participantIdentity)
		log.Printf("[%s]   Total packets: %d", r.participantIdentity, r.audioPacketCount)
		log.Printf("[%s]   Empty packets (skipped): %d", r.participantIdentity, r.audioEmptyPacketCount)
		log.Printf("[%s]   Bytes received: %d", r.participantIdentity, r.audioBytesReceived)
	}
	duration := time.Since(r.startTime)
	log.Printf("[%s] Duration: %v", r.participantIdentity, duration.Round(time.Second))
	log.Printf("[%s] ═══════════════════════════════════════", r.participantIdentity)
	r.mu.Unlock()

	log.Printf("[%s] Recorder stopped", r.participantIdentity)
}

// WaitReady waits for both audio and video to be ready (with timeout)
func (r *ParticipantRecorder) WaitReady(timeout time.Duration) (videoReady, audioReady bool) {
	videoTimer := time.NewTimer(timeout)
	audioTimer := time.NewTimer(timeout)
	defer videoTimer.Stop()
	defer audioTimer.Stop()

	if r.config.EnableVideo {
		select {
		case <-r.videoReady:
			videoReady = true
			log.Printf("[%s] ✅ Video ready (keyframe received)", r.participantIdentity)
		case <-videoTimer.C:
			log.Printf("[%s] ⚠️  Video NOT ready (timeout)", r.participantIdentity)
		}
	} else {
		videoReady = true // Not enabled = ready
	}

	if r.config.EnableAudio {
		select {
		case <-r.audioReady:
			audioReady = true
			log.Printf("[%s] ✅ Audio ready (first packet received)", r.participantIdentity)
		case <-audioTimer.C:
			log.Printf("[%s] ⚠️  Audio NOT ready (timeout)", r.participantIdentity)
		}
	} else {
		audioReady = true // Not enabled = ready
	}

	return
}
