package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// ParticipantRecorder records a participant's audio and video streams to HLS format.
//
// It creates a GStreamer pipeline that:
//   - Receives RTP packets (H.264 video, Opus audio)
//   - Optionally transcodes audio to AAC (or keeps Opus)
//   - Muxes to MPEG-TS format
//   - Generates HLS playlists and segments via hlssink
//
// The recorder implements delayed pipeline start to ensure all HLS segments
// begin with valid H.264 keyframes containing SPS/PPS headers.
type ParticipantRecorder struct {
	participant string
	room        string
	outputDir   string
	keepOpus    bool

	pipeline    *gst.Pipeline
	videoAppSrc *app.Source
	audioAppSrc *app.Source
	videoDepay  *gst.Element
	audioDepay  *gst.Element

	videoInitialized bool
	audioInitialized bool
	videoEnded       bool
	audioEnded       bool

	videoReadyOnce           sync.Once
	videoReadyCh             chan struct{}
	videoReady               atomic.Bool
	handshakeReady           atomic.Bool
	recordingActive          atomic.Bool
	recordingKeyframePending atomic.Bool
	pipelineStarted          atomic.Bool
	onVideoReady             func()

	videoPacketCount      int
	videoEmptyPacketCount int
	videoKeyframeCount    int
	videoSPSCount         int
	videoPPSCount         int
	videoPLIRequests      int
	videoTimestampBase    uint32
	videoTimestampInit    bool
	videoLastPTS          gst.ClockTime
	videoLastTimestamp    uint32
	audioPacketCount      int
	audioEmptyPacketCount int
	videoBytesReceived    int64
	audioBytesReceived    int64
	audioTimestampBase    uint32
	audioTimestampInit    bool
	audioLastPTS          gst.ClockTime
	audioLastTimestamp    uint32

	mu        sync.Mutex
	wg        sync.WaitGroup
	stopOnce  sync.Once
	startTime time.Time

	preVideoMu      sync.Mutex
	preVideoPackets []*rtp.Packet
	preAudioMu      sync.Mutex
	preAudioPackets []*rtp.Packet

	s3Uploader *RealtimeS3Uploader // Real-time S3 uploader (nil if disabled)
}

const (
	preVideoBufferMax = 300
	preAudioBufferMax = 500
)

// RecordingSummary contains statistics and metadata about a completed recording.
type RecordingSummary struct {
	Participant  string        // Participant identity
	Room         string        // Room name
	OutputFile   string        // Local path to output.ts file
	SizeBytes    int64         // Size of output.ts in bytes
	Duration     time.Duration // Recording duration
	Err          error         // Error if recording failed
	VideoPackets int           // Number of video RTP packets processed
	AudioPackets int           // Number of audio RTP packets processed
	Remote       string        // S3 URL if uploaded, empty otherwise
}

// NewParticipantRecorder creates a new recorder for a participant.
//
// It initializes the GStreamer pipeline but does not start it.
// The pipeline will start when the first recording keyframe arrives
// (after ActivateRecording is called and a keyframe is received).
//
// Returns an error if the output directory cannot be created or
// if any GStreamer element fails to initialize.
//
// Each recorder creates a unique temporary directory to support concurrent recordings.
// Directory format: OUTPUT_DIR/room_participant_timestamp
func NewParticipantRecorder(cfg *Config, roomName, participant string) (*ParticipantRecorder, error) {
	// Create single unique directory for this recording session
	// Format: room_participant_timestamp (flat structure for easy cleanup)
	timestamp := time.Now().Format("20060102-150405.000")
	sessionDir := fmt.Sprintf("%s_%s_%s", roomName, participant, timestamp)
	baseDir := filepath.Join(cfg.OutputDir, sessionDir)
	absDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve output directory: %w", err)
	}

	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory %s: %w", absDir, err)
	}

	log.Printf("[%s/%s] created session directory: %s", roomName, participant, absDir)

	// Initialize real-time S3 uploader if enabled
	var s3Uploader *RealtimeS3Uploader
	if cfg.S3RealTimeUpload && cfg.S3.Enabled() {
		uploader, err := NewRealtimeS3Uploader(cfg.S3, roomName, participant, absDir)
		if err != nil {
			return nil, fmt.Errorf("failed to create real-time S3 uploader: %w", err)
		}
		s3Uploader = uploader
	}

	gst.Init(nil)

	pipeline, err := gst.NewPipeline("participant-recorder")
	if err != nil {
		return nil, fmt.Errorf("failed to create GStreamer pipeline: %w", err)
	}

	videoSrc, err := gst.NewElement("appsrc")
	if err != nil {
		return nil, fmt.Errorf("failed to create video appsrc: %w", err)
	}
	videoSrc.SetProperty("is-live", true)
	videoSrc.SetProperty("format", gst.FormatTime)
	videoSrc.SetProperty("do-timestamp", false)
	videoSrc.SetProperty("emit-signals", true)
	videoSrc.SetProperty("block", false)
	videoSrc.SetProperty("stream-type", 0)
	videoSrc.SetProperty("max-bytes", uint64(10*1024*1024))

	videoJitter, err := gst.NewElement("rtpjitterbuffer")
	if err != nil {
		return nil, fmt.Errorf("failed to create video jitterbuffer: %w", err)
	}
	videoJitter.SetProperty("latency", uint(200))
	videoJitter.SetProperty("mode", int(1)) // RTP_JITTER_BUFFER_MODE_NONE to keep RTP timestamps unmodified

	videoDepay, err := gst.NewElement("rtph264depay")
	if err != nil {
		return nil, fmt.Errorf("failed to create rtph264depay: %w", err)
	}

	h264parse, err := gst.NewElement("h264parse")
	if err != nil {
		return nil, fmt.Errorf("failed to create h264parse: %w", err)
	}
	// force SPS/PPS before every keyframe so the resulting HLS segments stay decodable
	h264parse.SetProperty("disable-passthrough", true)
	h264parse.SetProperty("config-interval", int32(-1))

	videoCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, fmt.Errorf("failed to create video capsfilter: %w", err)
	}
	videoCaps := gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au")
	videoCapsFilter.SetProperty("caps", videoCaps)

	videoQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create video queue: %w", err)
	}
	videoQueue.SetProperty("max-size-buffers", uint(0))
	videoQueue.SetProperty("max-size-bytes", uint(0))
	videoQueue.SetProperty("max-size-time", uint64(0))

	audioSrc, err := gst.NewElement("appsrc")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio appsrc: %w", err)
	}
	audioSrc.SetProperty("is-live", true)
	audioSrc.SetProperty("format", gst.FormatTime)
	audioSrc.SetProperty("do-timestamp", false)
	audioSrc.SetProperty("emit-signals", true)
	audioSrc.SetProperty("block", false)
	audioSrc.SetProperty("stream-type", 0)
	audioSrc.SetProperty("max-bytes", uint64(2*1024*1024))

	audioJitter, err := gst.NewElement("rtpjitterbuffer")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio jitterbuffer: %w", err)
	}
	audioJitter.SetProperty("latency", uint(200))
	audioJitter.SetProperty("mode", int(1)) // Disable jitterbuffer resync to preserve relative timestamps

	audioDepay, err := gst.NewElement("rtpopusdepay")
	if err != nil {
		return nil, fmt.Errorf("failed to create rtpopusdepay: %w", err)
	}

	// Audio processing pipeline depends on KeepOpus configuration
	var opusDec, audioConvert, aacEnc, aacParse, opusParse *gst.Element
	if cfg.KeepOpus {
		// Keep Opus without transcoding
		opusParse, err = gst.NewElement("opusparse")
		if err != nil {
			return nil, fmt.Errorf("failed to create opusparse: %w", err)
		}
	} else {
		// Transcode Opus to AAC
		opusDec, err = gst.NewElement("opusdec")
		if err != nil {
			return nil, fmt.Errorf("failed to create opusdec: %w", err)
		}

		audioConvert, err = gst.NewElement("audioconvert")
		if err != nil {
			return nil, fmt.Errorf("failed to create audioconvert: %w", err)
		}

		aacEnc, err = gst.NewElement("avenc_aac")
		if err != nil {
			return nil, fmt.Errorf("failed to create avenc_aac: %w", err)
		}
		aacEnc.SetProperty("bitrate", uint(128000))

		aacParse, err = gst.NewElement("aacparse")
		if err != nil {
			return nil, fmt.Errorf("failed to create aacparse: %w", err)
		}
	}

	audioQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio queue: %w", err)
	}
	audioQueue.SetProperty("max-size-buffers", uint(0))
	audioQueue.SetProperty("max-size-bytes", uint(0))
	audioQueue.SetProperty("max-size-time", uint64(0))

	mpegtsmux, err := gst.NewElement("mpegtsmux")
	if err != nil {
		return nil, fmt.Errorf("failed to create mpegtsmux: %w", err)
	}
	mpegtsmux.SetProperty("alignment", int64(7))
	mpegtsmux.SetProperty("start-time-selection", int64(0)) // Force timestamps to start at zero
	mpegtsmux.SetProperty("start-time", uint64(0))

	muxQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create mux queue: %w", err)
	}

	outputTee, err := gst.NewElement("tee")
	if err != nil {
		return nil, fmt.Errorf("failed to create tee: %w", err)
	}

	tsQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create ts queue: %w", err)
	}

	tsSink, err := gst.NewElement("filesink")
	if err != nil {
		return nil, fmt.Errorf("failed to create filesink: %w", err)
	}
	tsSink.SetProperty("location", filepath.Join(absDir, "output.ts"))
	tsSink.SetProperty("sync", false)
	tsSink.SetProperty("async", false)

	hlsQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create hls queue: %w", err)
	}

	hlsSink, err := gst.NewElement("hlssink")
	if err != nil {
		return nil, fmt.Errorf("failed to create hlssink: %w", err)
	}
	hlsSink.SetProperty("location", filepath.Join(absDir, "segment%05d.ts"))
	hlsSink.SetProperty("playlist-location", filepath.Join(absDir, "playlist.m3u8"))
	segmentDuration := cfg.SegmentDurationSecs
	if segmentDuration <= 0 {
		segmentDuration = 2
	}
	hlsSink.SetProperty("target-duration", uint(segmentDuration))
	hlsSink.SetProperty("max-files", uint(0))
	hlsSink.SetProperty("playlist-length", uint(0))

	// Build elements list based on audio codec configuration
	elements := []*gst.Element{
		videoSrc, videoJitter, videoDepay, h264parse, videoCapsFilter, videoQueue,
		audioSrc, audioJitter, audioDepay,
	}
	if cfg.KeepOpus {
		elements = append(elements, opusParse)
	} else {
		elements = append(elements, opusDec, audioConvert, aacEnc, aacParse)
	}
	elements = append(elements,
		audioQueue,
		mpegtsmux, muxQueue, outputTee,
		tsQueue, tsSink,
		hlsQueue, hlsSink,
	)

	for _, elem := range elements {
		if err := pipeline.Add(elem); err != nil {
			return nil, fmt.Errorf("failed to add %s to pipeline: %w", elem.GetName(), err)
		}
	}

	if err := gst.ElementLinkMany(videoSrc, videoJitter, videoDepay, h264parse, videoCapsFilter, videoQueue); err != nil {
		return nil, fmt.Errorf("failed to link video chain: %w", err)
	}

	// Link audio pipeline based on codec configuration
	if cfg.KeepOpus {
		if err := gst.ElementLinkMany(audioSrc, audioJitter, audioDepay, opusParse, audioQueue); err != nil {
			return nil, fmt.Errorf("failed to link audio chain (opus): %w", err)
		}
	} else {
		if err := gst.ElementLinkMany(audioSrc, audioJitter, audioDepay, opusDec, audioConvert, aacEnc, aacParse, audioQueue); err != nil {
			return nil, fmt.Errorf("failed to link audio chain (aac): %w", err)
		}
	}

	// mpegtsmux requires request pads, cannot use ElementLinkMany
	videoMuxPad := mpegtsmux.GetRequestPad("sink_%d")
	if videoMuxPad == nil {
		return nil, fmt.Errorf("failed to get request pad from mpegtsmux for video")
	}
	videoQueueSrc := videoQueue.GetStaticPad("src")
	if videoQueueSrc == nil {
		return nil, fmt.Errorf("failed to get src pad from video queue")
	}
	if linkRet := videoQueueSrc.Link(videoMuxPad); linkRet != gst.PadLinkOK {
		return nil, fmt.Errorf("failed to link video queue to mux: %s", linkRet.String())
	}

	audioMuxPad := mpegtsmux.GetRequestPad("sink_%d")
	if audioMuxPad == nil {
		return nil, fmt.Errorf("failed to get request pad from mpegtsmux for audio")
	}
	audioQueueSrc := audioQueue.GetStaticPad("src")
	if audioQueueSrc == nil {
		return nil, fmt.Errorf("failed to get src pad from audio queue")
	}
	if linkRet := audioQueueSrc.Link(audioMuxPad); linkRet != gst.PadLinkOK {
		return nil, fmt.Errorf("failed to link audio queue to mux: %s", linkRet.String())
	}

	if err := gst.ElementLinkMany(mpegtsmux, muxQueue, outputTee); err != nil {
		return nil, fmt.Errorf("failed to link mux branch: %w", err)
	}

	if err := gst.ElementLinkMany(tsQueue, tsSink); err != nil {
		return nil, fmt.Errorf("failed to link ts branch: %w", err)
	}
	if err := gst.ElementLinkMany(hlsQueue, hlsSink); err != nil {
		return nil, fmt.Errorf("failed to link hls branch: %w", err)
	}

	teePad1 := outputTee.GetRequestPad("src_%u")
	tsQueueSink := tsQueue.GetStaticPad("sink")
	if linkRet := teePad1.Link(tsQueueSink); linkRet != gst.PadLinkOK {
		return nil, fmt.Errorf("failed to link tee to ts queue: %s", linkRet.String())
	}

	teePad2 := outputTee.GetRequestPad("src_%u")
	hlsQueueSink := hlsQueue.GetStaticPad("sink")
	if linkRet := teePad2.Link(hlsQueueSink); linkRet != gst.PadLinkOK {
		return nil, fmt.Errorf("failed to link tee to hls queue: %s", linkRet.String())
	}

	_ = os.Setenv("GST_DEBUG_DUMP_DOT_DIR", absDir)

	recorder := &ParticipantRecorder{
		participant:  participant,
		room:         roomName,
		outputDir:    absDir,
		keepOpus:     cfg.KeepOpus,
		pipeline:     pipeline,
		videoAppSrc:  app.SrcFromElement(videoSrc),
		audioAppSrc:  app.SrcFromElement(audioSrc),
		videoDepay:   videoDepay,
		audioDepay:   audioDepay,
		startTime:    time.Now(),
		videoReadyCh: make(chan struct{}),
		s3Uploader:   s3Uploader,
	}

	audioCodec := "AAC"
	if cfg.KeepOpus {
		audioCodec = "Opus"
	}
	log.Printf("[%s/%s] recorder initialized with H.264 + %s", roomName, participant, audioCodec)

	bus := pipeline.GetPipelineBus()
	bus.AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageError:
			gerr := msg.ParseError()
			log.Printf("[%s] GStreamer error: %v (debug: %s)", recorder.logPrefix(), gerr.Error(), gerr.DebugString())
			return false
		case gst.MessageWarning:
			gw := msg.ParseWarning()
			log.Printf("[%s] GStreamer warning: %v", recorder.logPrefix(), gw.Error())
		case gst.MessageEOS:
			log.Printf("[%s] GStreamer EOS received", recorder.logPrefix())
			return false
		}
		return true
	})

	return recorder, nil
}

func (r *ParticipantRecorder) logPrefix() string {
	return fmt.Sprintf("%s/%s", r.room, r.participant)
}

// SetOnVideoReady sets a callback to be invoked when the first video keyframe
// is received (handshake ready). If handshake is already complete, the callback
// is invoked immediately.
func (r *ParticipantRecorder) SetOnVideoReady(cb func()) {
	r.mu.Lock()
	r.onVideoReady = cb
	r.mu.Unlock()
	if cb != nil && r.handshakeReady.Load() {
		cb()
	}
}

// HandshakeReady returns true if the recorder has received the first video keyframe.
// This indicates that H.264 parameters (SPS/PPS) are established and recording can begin.
func (r *ParticipantRecorder) HandshakeReady() bool {
	return r.handshakeReady.Load()
}

// ActivateRecording enables recording. The GStreamer pipeline will start
// at the next keyframe, and all subsequent packets will be recorded to HLS.
//
// This resets packet counters and timestamp bases, and clears pre-buffers
// to ensure recording starts from the next keyframe (not a stale buffered one).
func (r *ParticipantRecorder) ActivateRecording() {
	r.recordingActive.Store(true)
	r.recordingKeyframePending.Store(true)
	r.mu.Lock()
	r.videoTimestampInit = false
	r.audioTimestampInit = false
	r.videoPacketCount = 0
	r.videoEmptyPacketCount = 0
	r.videoKeyframeCount = 0
	r.videoSPSCount = 0
	r.videoPPSCount = 0
	r.videoPLIRequests = 0
	r.videoBytesReceived = 0
	r.videoLastPTS = 0
	r.videoLastTimestamp = 0
	r.audioPacketCount = 0
	r.audioEmptyPacketCount = 0
	r.audioBytesReceived = 0
	r.audioLastPTS = 0
	r.audioLastTimestamp = 0
	r.mu.Unlock()

	// Keep pre-buffers to have packets available for pipeline priming
	// This ensures segment 0 contains both audio and video
	// Note: We rely on keyframe detection to prevent stale P-frames
	// r.clearPreVideoBuffer()
	// r.clearPreAudioBuffer()
}

// Start prepares the recorder for operation.
//
// Note: This does NOT start the GStreamer pipeline. The pipeline will be started
// automatically when the first recording keyframe arrives after ActivateRecording.
// This delayed start ensures all HLS segments begin with valid H.264 keyframes.
func (r *ParticipantRecorder) Start() error {
	log.Printf("[%s] GStreamer pipeline ready (will start on first recording keyframe)", r.logPrefix())
	// Pipeline will be started when first recording keyframe arrives
	// This prevents invalid HLS segments from being created before recording begins
	return nil
}

// H.264 NAL unit types used to detect keyframes.
const (
	nalUnitTypeSPS   = 7  // Sequence Parameter Set
	nalUnitTypePPS   = 8  // Picture Parameter Set
	nalUnitTypeIDR   = 5  // IDR (Instantaneous Decoder Refresh) keyframe
	nalUnitTypeSTAPA = 24 // Single-time Aggregation Packet Type A (multiple NAL units)
	nalUnitTypeFUA   = 28 // Fragmentation Unit Type A (fragmented NAL unit)
)

// isH264Keyframe detects whether an RTP payload contains or references an H.264 keyframe.
//
// This function inspects the NAL unit type indicator byte and handles:
//   - Single NAL units: SPS (7), PPS (8), IDR (5)
//   - STAP-A packets: Searches aggregated NAL units for SPS/PPS/IDR
//   - FU-A packets: Checks if the start fragment contains an IDR NAL unit
//
// Returns true if the payload contains keyframe data (SPS, PPS, or IDR).
func isH264Keyframe(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	nalType := payload[0] & 0x1F

	switch nalType {
	case nalUnitTypeSPS, nalUnitTypePPS, nalUnitTypeIDR:
		return true
	case nalUnitTypeSTAPA:
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

// parseSpropParameterSets extracts H.264 parameter sets (SPS/PPS) from the SDP fmtp line.
//
// The fmtp string (e.g., "sprop-parameter-sets=Z0IAH...=,aM4G8g==") contains base64-encoded
// SPS and PPS NAL units. This function decodes them and returns the raw NAL unit bytes.
//
// Returns nil if fmtp is empty or if sprop-parameter-sets is not present.
func parseSpropParameterSets(fmtp string) [][]byte {
	if fmtp == "" {
		return nil
	}
	params := strings.Split(fmtp, ";")
	for _, param := range params {
		param = strings.TrimSpace(param)
		if !strings.HasPrefix(param, "sprop-parameter-sets=") {
			continue
		}
		value := strings.TrimPrefix(param, "sprop-parameter-sets=")
		parts := strings.Split(value, ",")
		var nalUnits [][]byte
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(part)
			if err != nil {
				log.Printf("failed to decode sprop parameter set %q: %v", part, err)
				continue
			}
			nalUnits = append(nalUnits, data)
		}
		return nalUnits
	}
	return nil
}

// classifyNALUnit determines if a NAL unit is an SPS or PPS.
//
// Returns:
//   - (true, false) if the NAL unit is SPS
//   - (false, true) if the NAL unit is PPS
//   - (false, false) otherwise
func classifyNALUnit(nal []byte) (bool, bool) {
	if len(nal) == 0 {
		return false, false
	}
	switch nal[0] & 0x1F {
	case nalUnitTypeSPS:
		return true, false
	case nalUnitTypePPS:
		return false, true
	default:
		return false, false
	}
}

// detectParameterSets scans an RTP payload for H.264 parameter sets (SPS/PPS).
//
// This function handles:
//   - Single NAL units (SPS=7, PPS=8)
//   - STAP-A packets containing aggregated NAL units
//   - FU-A packets containing fragmented NAL units (only start fragments)
//
// Returns:
//   - (sps bool, pps bool) indicating whether SPS and/or PPS were detected
func detectParameterSets(payload []byte) (bool, bool) {
	if len(payload) == 0 {
		return false, false
	}
	switch payload[0] & 0x1F {
	case nalUnitTypeSPS:
		return true, false
	case nalUnitTypePPS:
		return false, true
	case nalUnitTypeSTAPA:
		var sps, pps bool
		offset := 1
		for offset+2 <= len(payload) {
			nalSize := int(payload[offset])<<8 | int(payload[offset+1])
			offset += 2
			if nalSize <= 0 || offset+nalSize > len(payload) {
				break
			}
			nsps, npps := classifyNALUnit(payload[offset : offset+nalSize])
			sps = sps || nsps
			pps = pps || npps
			offset += nalSize
		}
		return sps, pps
	case nalUnitTypeFUA:
		if len(payload) < 2 {
			return false, false
		}
		fuHeader := payload[1]
		startBit := (fuHeader & 0x80) != 0
		if !startBit {
			return false, false
		}
		nalType := fuHeader & 0x1F
		switch nalType {
		case nalUnitTypeSPS:
			return true, false
		case nalUnitTypePPS:
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func (r *ParticipantRecorder) initVideoCaps(payloadType uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.videoInitialized {
		return
	}
	capsStr := fmt.Sprintf("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,payload=%d", payloadType)
	caps := gst.NewCapsFromString(capsStr)
	r.videoAppSrc.SetProperty("caps", caps)
	r.videoInitialized = true
	log.Printf("[%s] video caps initialized: %s", r.logPrefix(), capsStr)
}

func (r *ParticipantRecorder) enqueuePreVideoPacket(pkt *rtp.Packet) {
	if pkt == nil {
		return
	}
	clone := pkt.Clone()
	if clone == nil {
		return
	}
	r.preVideoMu.Lock()
	if len(r.preVideoPackets) >= preVideoBufferMax {
		r.preVideoPackets[0] = nil
		r.preVideoPackets = r.preVideoPackets[1:]
	}
	r.preVideoPackets = append(r.preVideoPackets, clone)
	r.preVideoMu.Unlock()
}

func (r *ParticipantRecorder) dequeuePreVideoPacket() *rtp.Packet {
	r.preVideoMu.Lock()
	defer r.preVideoMu.Unlock()
	if len(r.preVideoPackets) == 0 {
		return nil
	}
	pkt := r.preVideoPackets[0]
	r.preVideoPackets[0] = nil
	r.preVideoPackets = r.preVideoPackets[1:]
	return pkt
}

func (r *ParticipantRecorder) clearPreVideoBuffer() {
	r.preVideoMu.Lock()
	for i := range r.preVideoPackets {
		r.preVideoPackets[i] = nil
	}
	r.preVideoPackets = nil
	r.preVideoMu.Unlock()
}

func (r *ParticipantRecorder) enqueuePreAudioPacket(pkt *rtp.Packet) {
	if pkt == nil {
		return
	}
	clone := pkt.Clone()
	if clone == nil {
		return
	}
	r.preAudioMu.Lock()
	if len(r.preAudioPackets) >= preAudioBufferMax {
		r.preAudioPackets[0] = nil
		r.preAudioPackets = r.preAudioPackets[1:]
	}
	r.preAudioPackets = append(r.preAudioPackets, clone)
	r.preAudioMu.Unlock()
}

func (r *ParticipantRecorder) dequeuePreAudioPacket() *rtp.Packet {
	r.preAudioMu.Lock()
	defer r.preAudioMu.Unlock()
	if len(r.preAudioPackets) == 0 {
		return nil
	}
	pkt := r.preAudioPackets[0]
	r.preAudioPackets[0] = nil
	r.preAudioPackets = r.preAudioPackets[1:]
	return pkt
}

func (r *ParticipantRecorder) clearPreAudioBuffer() {
	r.preAudioMu.Lock()
	for i := range r.preAudioPackets {
		r.preAudioPackets[i] = nil
	}
	r.preAudioPackets = nil
	r.preAudioMu.Unlock()
}

func (r *ParticipantRecorder) pushVideoPacket(pkt *rtp.Packet) error {
	pts, relative := r.videoClockTime(pkt.Timestamp)
	pkt.Timestamp = relative

	data, err := pkt.Marshal()
	if err != nil {
		return fmt.Errorf("marshal video RTP failed: %w", err)
	}

	buffer := gst.NewBufferFromBytes(data)
	buffer.SetPresentationTimestamp(pts)
	r.mu.Lock()
	r.videoLastPTS = pts
	r.videoLastTimestamp = relative
	r.mu.Unlock()

	if flow := r.videoAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
		if flow == gst.FlowFlushing {
			return fmt.Errorf("video appsrc flushing")
		}
		return fmt.Errorf("video appsrc push failed: %s", flow.String())
	}
	return nil
}

func (r *ParticipantRecorder) injectParameterSets(reference *rtp.Packet, nalUnits [][]byte) (bool, bool, error) {
	if len(nalUnits) == 0 {
		return false, false, fmt.Errorf("no parameter sets to inject")
	}

	payload := []byte{0x78} // F=0, NRI=3, Type=24 (STAP-A)
	var spsInjected, ppsInjected bool

	for _, nal := range nalUnits {
		if len(nal) == 0 {
			continue
		}
		if len(nal) > 0xFFFF {
			return false, false, fmt.Errorf("parameter set too large (%d bytes)", len(nal))
		}
		if sps, pps := classifyNALUnit(nal); sps || pps {
			spsInjected = spsInjected || sps
			ppsInjected = ppsInjected || pps
		}
		payload = append(payload, byte(len(nal)>>8), byte(len(nal)))
		payload = append(payload, nal...)
	}

	if len(payload) <= 1 {
		return false, false, fmt.Errorf("no valid parameter sets to inject")
	}

	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    reference.PayloadType,
			SequenceNumber: reference.SequenceNumber - 1,
			Timestamp:      reference.Timestamp,
			SSRC:           reference.SSRC,
			Marker:         false,
		},
		Payload: payload,
	}

	if err := r.pushVideoPacket(packet); err != nil {
		return false, false, err
	}

	log.Printf("[%s] injected SPS/PPS via STAP-A (seq=%d)", r.logPrefix(), packet.SequenceNumber)
	return spsInjected, ppsInjected, nil
}

func (r *ParticipantRecorder) videoClockTime(ts uint32) (gst.ClockTime, uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.videoTimestampInit {
		r.videoTimestampInit = true
		r.videoTimestampBase = ts
	}
	relative := uint32(ts - r.videoTimestampBase)
	ptsNs := uint64(relative) * 1_000_000_000 / 90000
	return gst.ClockTime(ptsNs), relative
}

func (r *ParticipantRecorder) audioClockTime(ts uint32) (gst.ClockTime, uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.audioTimestampInit {
		r.audioTimestampInit = true
		r.audioTimestampBase = ts
	}
	relative := uint32(ts - r.audioTimestampBase)
	ptsNs := uint64(relative) * 1_000_000_000 / 48000
	return gst.ClockTime(ptsNs), relative
}

func (r *ParticipantRecorder) signalVideoReady() {
	if r.videoReady.CompareAndSwap(false, true) {
		r.handshakeReady.Store(true)
		r.videoReadyOnce.Do(func() {
			close(r.videoReadyCh)
		})
		var cb func()
		r.mu.Lock()
		cb = r.onVideoReady
		r.mu.Unlock()
		if cb != nil {
			cb()
		}
	}
}

func (r *ParticipantRecorder) requestPLI(writer func(webrtc.SSRC), ssrc webrtc.SSRC) {
	if writer == nil {
		return
	}
	r.mu.Lock()
	r.videoPLIRequests++
	r.mu.Unlock()
	writer(ssrc)
}

// AttachVideoTrack attaches a video track for recording.
//
// It spawns a goroutine that:
//   - Requests keyframes via PLI
//   - Waits for handshake keyframe (to establish SPS/PPS)
//   - Waits for recording activation
//   - Starts pipeline on first recording keyframe
//   - Pushes RTP packets to GStreamer
//
// The pliWriter function is called to request keyframes when needed.
func (r *ParticipantRecorder) AttachVideoTrack(ctx context.Context, track *webrtc.TrackRemote, pliWriter func(webrtc.SSRC)) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		log.Printf("[%s] video track subscribed (sid=%s, codec=%s, payloadType=%d, ssrc=%d)",
			r.logPrefix(), track.ID(), track.Codec().MimeType, track.PayloadType(), track.SSRC())

		if pliWriter == nil {
			pliWriter = func(webrtc.SSRC) {}
		}

		var handshakeWait, recordingWait int
		firstPacket := true

		fmtp := track.Codec().SDPFmtpLine
		log.Printf("[%s] track fmtp: %q", r.logPrefix(), fmtp)
		spropNALs := parseSpropParameterSets(fmtp)
		if len(spropNALs) > 0 {
			log.Printf("[%s] codec fmtp provided %d parameter set(s)", r.logPrefix(), len(spropNALs))
		}
		var spsSeen, ppsSeen bool
		injectedSprop := false

		for {
			var rtpPacket *rtp.Packet
			var fromBuffer bool

			if r.recordingActive.Load() {
				if buffered := r.dequeuePreVideoPacket(); buffered != nil {
					rtpPacket = buffered
					fromBuffer = true
				}
			}

			if rtpPacket == nil {
				select {
				case <-ctx.Done():
					return
				default:
				}

				var err error
				rtpPacket, _, err = track.ReadRTP()
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("[%s] video track read error: %v", r.logPrefix(), err)
					}
					return
				}

				if firstPacket {
					firstPacket = false
					log.Printf("[%s] requesting initial keyframe via PLI (seq=%d)", r.logPrefix(), rtpPacket.SequenceNumber)
					r.requestPLI(pliWriter, track.SSRC())
				}
			}

			if rtpPacket == nil {
				continue
			}

			if len(rtpPacket.Payload) == 0 {
				if !fromBuffer {
					continue
				}
				continue
			}

			if sps, pps := detectParameterSets(rtpPacket.Payload); sps || pps {
				if sps {
					spsSeen = true
				}
				if pps {
					ppsSeen = true
				}
				log.Printf("[%s] detected parameter set packet seq=%d sps=%v pps=%v", r.logPrefix(), rtpPacket.SequenceNumber, sps, pps)
				if r.recordingActive.Load() {
					r.mu.Lock()
					if sps {
						r.videoSPSCount++
					}
					if pps {
						r.videoPPSCount++
					}
					r.mu.Unlock()
				}
			}

			isKeyframe := isH264Keyframe(rtpPacket.Payload)

			if !r.handshakeReady.Load() {
				if isKeyframe {
					if !fromBuffer {
						r.enqueuePreVideoPacket(rtpPacket)
					}
					r.mu.Lock()
					r.videoKeyframeCount++
					r.mu.Unlock()
					r.signalVideoReady()
					log.Printf("[%s] received warm-up keyframe (seq=%d ts=%d)", r.logPrefix(), rtpPacket.SequenceNumber, rtpPacket.Timestamp)
				} else {
					handshakeWait++
					if handshakeWait == 1 || handshakeWait%200 == 0 {
						log.Printf("[%s] warm-up waiting for keyframe, sending PLI (seq=%d)", r.logPrefix(), rtpPacket.SequenceNumber)
						r.requestPLI(pliWriter, track.SSRC())
					}
				}
				continue
			}

			if !r.recordingActive.Load() {
				if !fromBuffer {
					r.enqueuePreVideoPacket(rtpPacket)
				}
				continue
			}

			if r.recordingKeyframePending.Load() {
				if isKeyframe {
					if (!spsSeen || !ppsSeen) && len(spropNALs) > 0 && !injectedSprop {
						r.initVideoCaps(rtpPacket.PayloadType)
						if injectedSPS, injectedPPS, injErr := r.injectParameterSets(rtpPacket, spropNALs); injErr != nil {
							log.Printf("[%s] failed to inject codec parameter sets: %v", r.logPrefix(), injErr)
						} else {
							injectedSprop = true
							if injectedSPS {
								spsSeen = true
								r.mu.Lock()
								r.videoSPSCount++
								r.mu.Unlock()
							}
							if injectedPPS {
								ppsSeen = true
								r.mu.Lock()
								r.videoPPSCount++
								r.mu.Unlock()
							}
						}
					}
					r.mu.Lock()
					r.videoKeyframeCount++
					r.mu.Unlock()

					// Start pipeline on first recording keyframe to avoid invalid HLS segments
					if !r.pipelineStarted.Load() {
						log.Printf("[%s] starting GStreamer pipeline with first recording keyframe seq=%d ts=%d", r.logPrefix(), rtpPacket.SequenceNumber, rtpPacket.Timestamp)
						r.initVideoCaps(rtpPacket.PayloadType)
						if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
							log.Printf("[%s] failed to start pipeline: %v", r.logPrefix(), err)
							return
						}
						r.pipeline.DebugBinToDotFileWithTs(gst.DebugGraphShowAll, "publisher_recorder")
						r.pipelineStarted.Store(true)
					} else {
						log.Printf("[%s] starting active recording with keyframe seq=%d ts=%d", r.logPrefix(), rtpPacket.SequenceNumber, rtpPacket.Timestamp)
					}

					// Push first video keyframe BEFORE allowing audio to start
					// This ensures first HLS segment contains both audio and video
					r.initVideoCaps(rtpPacket.PayloadType)
					if err := r.pushVideoPacket(rtpPacket); err != nil {
						log.Printf("[%s] video push error: %v", r.logPrefix(), err)
						return
					}
					r.mu.Lock()
					r.videoPacketCount++
					r.videoBytesReceived += int64(len(rtpPacket.Payload))
					r.mu.Unlock()

					// Push several more video packets from pre-buffer to prime the pipeline
					// This ensures video reaches the muxer before audio starts flooding in
					primeCount := 0
					for {
						buffered := r.dequeuePreVideoPacket()
						if buffered == nil {
							break
						}
						r.initVideoCaps(buffered.PayloadType)
						if err := r.pushVideoPacket(buffered); err != nil {
							log.Printf("[%s] video push error while priming: %v", r.logPrefix(), err)
							return
						}
						r.mu.Lock()
						r.videoPacketCount++
						r.videoBytesReceived += int64(len(buffered.Payload))
						r.mu.Unlock()
						primeCount++
					}
					log.Printf("[%s] primed pipeline with keyframe + %d video packets", r.logPrefix(), primeCount)

					// NOW allow audio to start (video pipeline is primed)
					r.recordingKeyframePending.Store(false)
					continue
				} else {
					recordingWait++
					if recordingWait == 1 || recordingWait%200 == 0 {
						log.Printf("[%s] waiting for keyframe to begin recording, sending PLI (seq=%d)", r.logPrefix(), rtpPacket.SequenceNumber)
						r.requestPLI(pliWriter, track.SSRC())
					}
					if !fromBuffer {
						r.enqueuePreVideoPacket(rtpPacket)
					}
					continue
				}
			}

			r.initVideoCaps(rtpPacket.PayloadType)
			if err := r.pushVideoPacket(rtpPacket); err != nil {
				log.Printf("[%s] video push error: %v", r.logPrefix(), err)
				return
			}

			r.mu.Lock()
			r.videoPacketCount++
			r.videoBytesReceived += int64(len(rtpPacket.Payload))
			r.mu.Unlock()
		}
	}()
}

// AttachAudioTrack attaches an audio track for recording.
//
// Audio packets are only pushed to GStreamer after recording is activated
// and the pipeline has started (triggered by the first video keyframe).
func (r *ParticipantRecorder) AttachAudioTrack(ctx context.Context, track *webrtc.TrackRemote) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		log.Printf("[%s] audio track subscribed (sid=%s, codec=%s, payloadType=%d)",
			r.logPrefix(), track.ID(), track.Codec().MimeType, track.PayloadType())
		for {
			var rtpPacket *rtp.Packet
			var fromBuffer bool

			if r.recordingActive.Load() && !r.recordingKeyframePending.Load() {
				if buffered := r.dequeuePreAudioPacket(); buffered != nil {
					rtpPacket = buffered
					fromBuffer = true
				}
			}

			if rtpPacket == nil {
				select {
				case <-ctx.Done():
					return
				default:
				}

				var err error
				rtpPacket, _, err = track.ReadRTP()
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("[%s] audio track read error: %v", r.logPrefix(), err)
					}
					return
				}
			}

			if rtpPacket == nil {
				continue
			}

			if !r.recordingActive.Load() || r.recordingKeyframePending.Load() {
				if !fromBuffer {
					r.enqueuePreAudioPacket(rtpPacket)
				}
				continue
			}
			r.mu.Lock()
			r.audioPacketCount++
			if len(rtpPacket.Payload) == 0 {
				r.audioEmptyPacketCount++
				r.mu.Unlock()
				continue
			}
			r.audioBytesReceived += int64(len(rtpPacket.Payload))
			if !r.audioInitialized {
				capsStr := fmt.Sprintf("application/x-rtp,media=audio,encoding-name=OPUS,clock-rate=48000,payload=%d", rtpPacket.PayloadType)
				caps := gst.NewCapsFromString(capsStr)
				r.audioAppSrc.SetProperty("caps", caps)
				r.audioInitialized = true
				log.Printf("[%s] audio caps initialized: %s", r.logPrefix(), capsStr)
			}
			r.mu.Unlock()

			pts, relative := r.audioClockTime(rtpPacket.Timestamp)
			rtpPacket.Timestamp = relative

			data, err := rtpPacket.Marshal()
			if err != nil {
				log.Printf("[%s] marshal audio RTP failed: %v", r.logPrefix(), err)
				continue
			}

			buffer := gst.NewBufferFromBytes(data)
			buffer.SetPresentationTimestamp(pts)
			r.mu.Lock()
			r.audioLastPTS = pts
			r.audioLastTimestamp = relative
			r.mu.Unlock()

			if flow := r.audioAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
				if flow != gst.FlowFlushing {
					log.Printf("[%s] audio appsrc push returned %s", r.logPrefix(), flow.String())
				}
				return
			}
		}
	}()
}

// VideoStreamEnded signals that the video stream has ended.
// Sends EOS to the video appsrc element.
func (r *ParticipantRecorder) VideoStreamEnded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.videoEnded {
		return
	}
	r.videoEnded = true
	if r.videoAppSrc != nil {
		r.videoAppSrc.EndStream()
	}
}

// AudioStreamEnded signals that the audio stream has ended.
// Sends EOS to the audio appsrc element.
func (r *ParticipantRecorder) AudioStreamEnded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.audioEnded {
		return
	}
	r.audioEnded = true
	if r.audioAppSrc != nil {
		r.audioAppSrc.EndStream()
	}
}

// Stop stops the recorder and waits for all goroutines to complete.
//
// It sends EOS to both audio and video streams, waits for the GStreamer
// pipeline to process the EOS event, and then sets the pipeline to NULL state.
// Logs detailed statistics about the recording session.
func (r *ParticipantRecorder) Stop() {
	r.stopOnce.Do(func() {
		log.Printf("[%s] stopping recorder", r.logPrefix())
		r.VideoStreamEnded()
		r.AudioStreamEnded()

		if r.pipeline != nil {
			r.pipeline.SendEvent(gst.NewEOSEvent())
			if bus := r.pipeline.GetBus(); bus != nil {
				bus.TimedPopFiltered(gst.ClockTime(5*1_000_000_000), gst.MessageEOS|gst.MessageError)
			}
			r.pipeline.SetState(gst.StateNull)
		}

		r.clearPreVideoBuffer()
		r.clearPreAudioBuffer()

		if err := normalizeHLSTimestamps(r.outputDir); err != nil {
			log.Printf("[%s] failed to normalize HLS timestamps: %v", r.logPrefix(), err)
		} else {
			log.Printf("[%s] normalized HLS timestamps", r.logPrefix())
		}

		if err := fixHLSPlaylist(r.outputDir); err != nil {
			log.Printf("[%s] failed to fix HLS playlist: %v", r.logPrefix(), err)
		} else {
			log.Printf("[%s] fixed HLS playlist final segment duration", r.logPrefix())
		}

		// Close real-time S3 uploader if enabled
		if r.s3Uploader != nil {
			if err := r.s3Uploader.Close(); err != nil {
				log.Printf("[%s] failed to close S3 uploader: %v", r.logPrefix(), err)
			}
		}

		videoSeconds := float64(r.videoLastPTS) / 1_000_000_000
		audioSeconds := float64(r.audioLastPTS) / 1_000_000_000
		log.Printf("[%s] recorder stats: videoPackets=%d emptyVideo=%d videoBytes=%d keyframes=%d sps=%d pps=%d plis=%d audioPackets=%d emptyAudio=%d audioBytes=%d videoPTS=%.3fs audioPTS=%.3fs videoBase=%d audioBase=%d videoLastTS=%d audioLastTS=%d",
			r.logPrefix(),
			r.videoPacketCount,
			r.videoEmptyPacketCount,
			r.videoBytesReceived,
			r.videoKeyframeCount,
			r.videoSPSCount,
			r.videoPPSCount,
			r.videoPLIRequests,
			r.audioPacketCount,
			r.audioEmptyPacketCount,
			r.audioBytesReceived,
			videoSeconds,
			audioSeconds,
			r.videoTimestampBase,
			r.audioTimestampBase,
			r.videoLastTimestamp,
			r.audioLastTimestamp)
	})
	r.wg.Wait()
}

// OutputDirectory returns the absolute path to the directory containing
// the recording files (output.ts, playlist.m3u8, segment*.ts).
func (r *ParticipantRecorder) OutputDirectory() string {
	return r.outputDir
}

// Summary returns a summary of the recording session including
// file size, duration, and packet counts.
func (r *ParticipantRecorder) Summary() RecordingSummary {
	summary := RecordingSummary{
		Participant: r.participant,
		Room:        r.room,
	}

	outputFile := filepath.Join(r.outputDir, "output.ts")
	summary.OutputFile = outputFile

	stat, err := os.Stat(outputFile)
	if err != nil {
		summary.Err = fmt.Errorf("failed to stat recording: %w", err)
		return summary
	}

	summary.SizeBytes = stat.Size()
	summary.Duration = time.Since(r.startTime)
	summary.VideoPackets = r.videoPacketCount
	summary.AudioPackets = r.audioPacketCount

	return summary
}
