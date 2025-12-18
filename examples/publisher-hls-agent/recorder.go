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

// ParticipantRecorder records a participant's audio and video streams to separate HLS outputs.
//
// It creates a GStreamer pipeline that:
//   - Receives RTP packets (H.264 or AV1 video, Opus audio)
//   - Outputs video-only HLS (H.264 in MPEG-TS segments, or AV1 in CMAF/fMP4 segments)
//   - Outputs audio-only fMP4 segments (Opus in CMAF format)
//   - Creates audio.json manifest for web player consumption
//
// The separate A/V output enables:
//   - iOS-compatible playback with single video tag
//   - WebAudio-based mixing of multiple participants' audio
//   - Efficient participant switching without re-buffering audio
//
// The recorder implements delayed pipeline start to ensure all HLS segments
// begin with valid keyframes.
type ParticipantRecorder struct {
	participant string
	room        string
	outputDir   string

	pipeline *gst.Pipeline

	segmentDuration int

	videoCodec string

	// H.264 RTP path (appsrc receives RTP packets).
	videoAppSrc *app.Source
	videoDepay  *gst.Element

	// AV1 path (appsrc receives AV1 OBU stream frames, not RTP).
	av1VideoAppSrc *app.Source

	audioAppSrc *app.Source

	// Audio manifest writer for fMP4 segments
	audioManifest     *AudioManifestWriter
	audioSegmentIndex int
	audioSegmentStart float64

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

	e2eeCtx          *E2EEContext    // E2EE decryption context (nil if disabled)
	frameAssembler   *FrameAssembler // Frame-level assembler/decryptor for E2EE video
	e2eeVideoAppSrc  *app.Source     // Separate appsrc for E2EE video (receives Annex B, not RTP)
	e2eeVideoEnabled bool            // True when using E2EE video pipeline
}

// Pre-buffer configuration for video and audio packets.
//
// These buffers store incoming RTP packets before recording activation,
// ensuring the first HLS segment contains both audio and video streams
// when the pipeline starts.
//
// Buffer sizes are chosen to accommodate typical streaming scenarios:
//   - preVideoBufferMax: 300 packets (approximately 10 seconds at 30fps)
//   - preAudioBufferMax: 500 packets (approximately 10 seconds at 50 packets/sec for Opus)
//
// When a buffer fills up, the oldest packet is discarded (FIFO behavior).
// This prevents unbounded memory growth while waiting for recording activation.
//
// The buffers are critical for ensuring HLS segment synchronization:
//  1. Video and audio packets arrive before recording starts
//  2. Packets are buffered until ActivateRecording() is called
//  3. On first keyframe, buffered packets prime the GStreamer pipeline
//  4. This ensures segment 0 contains synchronized audio and video
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

	startTime := time.Now()

	pipeline, err := gst.NewPipeline("participant-recorder")
	if err != nil {
		return nil, fmt.Errorf("failed to create GStreamer pipeline: %w", err)
	}

	segmentDuration := cfg.SegmentDurationSecs
	if segmentDuration <= 0 {
		segmentDuration = 2
	}

	// Video pipeline is initialized lazily when the video track is attached
	// (we need to know whether the participant published H.264 or AV1).

	// =========================================================================
	// AUDIO PIPELINE: appsrc (raw Opus) → opusparse → queue → splitmuxsink
	//
	// We bypass the jitterbuffer and rtpopusdepay entirely by pushing raw Opus
	// payloads directly. This eliminates jitterbuffer-related blocking issues
	// that occur when pre-buffered audio drains quickly followed by real-time.
	// =========================================================================

	audioSrc, err := gst.NewElement("appsrc")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio appsrc: %w", err)
	}
	_ = audioSrc.SetProperty("is-live", true)
	_ = audioSrc.SetProperty("format", gst.FormatTime)
	// Use explicit PTS based on RTP timestamps to preserve A/V sync.
	_ = audioSrc.SetProperty("do-timestamp", false)
	_ = audioSrc.SetProperty("emit-signals", true)
	_ = audioSrc.SetProperty("block", false)
	_ = audioSrc.SetProperty("stream-type", 0)
	_ = audioSrc.SetProperty("max-bytes", uint64(2*1024*1024))
	// Set caps for raw Opus audio (not RTP)
	audioCaps := gst.NewCapsFromString("audio/x-opus,rate=48000,channels=2")
	_ = audioSrc.SetProperty("caps", audioCaps)

	opusParse, err := gst.NewElement("opusparse")
	if err != nil {
		return nil, fmt.Errorf("failed to create opusparse: %w", err)
	}

	audioQueue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio queue: %w", err)
	}
	// Set reasonable limits but don't make it leaky (leaky causes gaps that break segmentation)
	_ = audioQueue.SetProperty("max-size-buffers", uint(500))           // ~10 seconds of audio at 50 packets/sec
	_ = audioQueue.SetProperty("max-size-bytes", uint(1024*1024))       // 1MB
	_ = audioQueue.SetProperty("max-size-time", uint64(10*time.Second)) // 10 seconds

	// Audio fMP4 segmenter using splitmuxsink with cmafmux
	// Note: splitmuxsink segments based on RUNNING TIME, not buffer PTS.
	// Let splitmuxsink handle all segmentation via max-size-time, don't configure cmafmux fragment-duration.
	audioSplitMux, err := gst.NewElement("splitmuxsink")
	if err != nil {
		return nil, fmt.Errorf("failed to create audio splitmuxsink: %w", err)
	}
	_ = audioSplitMux.SetProperty("location", filepath.Join(absDir, "audio%05d.m4s"))
	_ = audioSplitMux.SetProperty("max-size-time", uint64(segmentDuration)*uint64(time.Second))
	_ = audioSplitMux.SetProperty("muxer-factory", "cmafmux")
	_ = audioSplitMux.SetProperty("async-finalize", false)         // Ensure segments are finalized synchronously
	_ = audioSplitMux.SetProperty("send-keyframe-requests", false) // Audio has no keyframes
	_ = audioSplitMux.SetProperty("use-robust-muxing", true)       // Handle discontinuities gracefully
	_ = audioSplitMux.SetProperty("start-time-selection", 0)       // Use first buffer timestamp as start

	// =========================================================================
	// ADD ELEMENTS TO PIPELINE
	// =========================================================================

	elements := []*gst.Element{
		// Audio path (raw Opus, bypasses jitterbuffer and RTP depayloader)
		audioSrc, opusParse, audioQueue, audioSplitMux,
	}

	for _, elem := range elements {
		if err := pipeline.Add(elem); err != nil {
			return nil, fmt.Errorf("failed to add %s to pipeline: %w", elem.GetName(), err)
		}
	}

	// =========================================================================
	// LINK AUDIO PIPELINE
	// Direct path: raw Opus → opusparse → queue (no jitterbuffer or depayloader)
	// This avoids jitterbuffer blocking issues with pre-buffered audio.
	// =========================================================================

	if err := gst.ElementLinkMany(audioSrc, opusParse, audioQueue); err != nil {
		return nil, fmt.Errorf("failed to link audio processing chain: %w", err)
	}

	// Link audio queue to splitmuxsink (request pad)
	audioSplitPad := audioSplitMux.GetRequestPad("audio_%u")
	if audioSplitPad == nil {
		// Try alternative pad name
		audioSplitPad = audioSplitMux.GetRequestPad("audio")
	}
	if audioSplitPad == nil {
		return nil, fmt.Errorf("failed to get request pad from audio splitmuxsink")
	}
	audioQueueSrc := audioQueue.GetStaticPad("src")
	if audioQueueSrc == nil {
		return nil, fmt.Errorf("failed to get src pad from audio queue")
	}
	if linkRet := audioQueueSrc.Link(audioSplitPad); linkRet != gst.PadLinkOK {
		return nil, fmt.Errorf("failed to link audio queue to splitmuxsink: %s", linkRet.String())
	}

	_ = os.Setenv("GST_DEBUG_DUMP_DOT_DIR", absDir)

	// Initialize audio manifest writer
	audioManifest := NewAudioManifestWriter(absDir, cfg, startTime)

	recorder := &ParticipantRecorder{
		participant:     participant,
		room:            roomName,
		outputDir:       absDir,
		pipeline:        pipeline,
		segmentDuration: segmentDuration,
		audioAppSrc:     app.SrcFromElement(audioSrc),
		startTime:       startTime,
		videoReadyCh:    make(chan struct{}),
		s3Uploader:      s3Uploader,
		audioManifest:   audioManifest,
	}

	// Connect to splitmuxsink signals for audio manifest tracking
	// Note: format-location-full signal has 3 parameters (element, fragment_id, first_sample)
	// and returns the filename - we don't receive the filename as a parameter
	audioSplitMux.Connect("format-location-full", func(self *gst.Element, fragmentID uint, firstSample *gst.Sample) string {
		segmentFile := fmt.Sprintf("audio%05d.m4s", fragmentID)
		segmentStartTime := float64(fragmentID) * float64(segmentDuration)

		recorder.mu.Lock()
		recorder.audioSegmentIndex = int(fragmentID)
		recorder.audioSegmentStart = segmentStartTime
		recorder.mu.Unlock()

		log.Printf("[%s] audio segment %d started at %.2fs", recorder.logPrefix(), fragmentID, segmentStartTime)
		return filepath.Join(absDir, segmentFile)
	})

	log.Printf("[%s/%s] recorder initialized with separate A/V (video TBD + Opus audio fMP4)", roomName, participant)

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

func (r *ParticipantRecorder) initH264VideoPipeline() error {
	r.mu.Lock()
	if r.videoCodec != "" && !strings.EqualFold(r.videoCodec, webrtc.MimeTypeH264) {
		codec := r.videoCodec
		r.mu.Unlock()
		return fmt.Errorf("video pipeline already initialized for codec %s", codec)
	}
	if r.videoAppSrc != nil || r.e2eeVideoAppSrc != nil {
		r.mu.Unlock()
		return nil
	}
	pipeline := r.pipeline
	outputDir := r.outputDir
	segmentDuration := r.segmentDuration
	r.videoCodec = webrtc.MimeTypeH264
	r.mu.Unlock()

	if pipeline == nil {
		return fmt.Errorf("pipeline not initialized")
	}
	if segmentDuration <= 0 {
		segmentDuration = 2
	}

	// =========================================================================
	// VIDEO PIPELINE: appsrc → jitter → depay → h264parse → capsfilter → queue
	//                 → mpegtsmux (video-only) → hlssink (video.m3u8)
	// =========================================================================

	videoSrc, err := gst.NewElement("appsrc")
	if err != nil {
		return fmt.Errorf("failed to create video appsrc: %w", err)
	}
	_ = videoSrc.SetProperty("is-live", true)
	_ = videoSrc.SetProperty("format", gst.FormatTime)
	_ = videoSrc.SetProperty("do-timestamp", false)
	_ = videoSrc.SetProperty("emit-signals", true)
	_ = videoSrc.SetProperty("block", false)
	_ = videoSrc.SetProperty("stream-type", 0)
	_ = videoSrc.SetProperty("max-bytes", uint64(10*1024*1024))

	videoJitter, err := gst.NewElement("rtpjitterbuffer")
	if err != nil {
		return fmt.Errorf("failed to create video jitterbuffer: %w", err)
	}
	_ = videoJitter.SetProperty("latency", uint(200))
	_ = videoJitter.SetProperty("mode", int(1))

	videoDepay, err := gst.NewElement("rtph264depay")
	if err != nil {
		return fmt.Errorf("failed to create rtph264depay: %w", err)
	}

	h264parse, err := gst.NewElement("h264parse")
	if err != nil {
		return fmt.Errorf("failed to create h264parse: %w", err)
	}
	_ = h264parse.SetProperty("disable-passthrough", true)
	_ = h264parse.SetProperty("config-interval", int32(-1))

	videoCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return fmt.Errorf("failed to create video capsfilter: %w", err)
	}
	videoCaps := gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au")
	_ = videoCapsFilter.SetProperty("caps", videoCaps)

	videoQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create video queue: %w", err)
	}
	_ = videoQueue.SetProperty("max-size-buffers", uint(0))
	_ = videoQueue.SetProperty("max-size-bytes", uint(0))
	_ = videoQueue.SetProperty("max-size-time", uint64(0))

	videoMux, err := gst.NewElement("mpegtsmux")
	if err != nil {
		return fmt.Errorf("failed to create video mpegtsmux: %w", err)
	}
	_ = videoMux.SetProperty("alignment", int64(7))
	_ = videoMux.SetProperty("start-time-selection", int64(0))
	_ = videoMux.SetProperty("start-time", uint64(0))

	videoMuxQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create video mux queue: %w", err)
	}

	videoHlsSink, err := gst.NewElement("hlssink")
	if err != nil {
		return fmt.Errorf("failed to create video hlssink: %w", err)
	}
	_ = videoHlsSink.SetProperty("location", filepath.Join(outputDir, "video%05d.ts"))
	_ = videoHlsSink.SetProperty("playlist-location", filepath.Join(outputDir, "video.m3u8"))
	_ = videoHlsSink.SetProperty("target-duration", uint(segmentDuration))
	_ = videoHlsSink.SetProperty("max-files", uint(0))
	_ = videoHlsSink.SetProperty("playlist-length", uint(0))

	// =========================================================================
	// E2EE VIDEO PIPELINE: appsrc → h264parse → capsfilter → queue
	// Receives decrypted Annex B frames directly (bypassing RTP processing).
	// =========================================================================

	e2eeVideoSrc, err := gst.NewElement("appsrc")
	if err != nil {
		return fmt.Errorf("failed to create E2EE video appsrc: %w", err)
	}
	_ = e2eeVideoSrc.SetProperty("is-live", true)
	_ = e2eeVideoSrc.SetProperty("format", gst.FormatTime)
	_ = e2eeVideoSrc.SetProperty("do-timestamp", false)
	_ = e2eeVideoSrc.SetProperty("emit-signals", true)
	_ = e2eeVideoSrc.SetProperty("block", false)
	_ = e2eeVideoSrc.SetProperty("stream-type", 0)
	_ = e2eeVideoSrc.SetProperty("max-bytes", uint64(10*1024*1024))

	e2eeH264Parse, err := gst.NewElement("h264parse")
	if err != nil {
		return fmt.Errorf("failed to create E2EE h264parse: %w", err)
	}
	_ = e2eeH264Parse.SetProperty("disable-passthrough", true)
	_ = e2eeH264Parse.SetProperty("config-interval", int32(-1))

	e2eeCapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return fmt.Errorf("failed to create E2EE video capsfilter: %w", err)
	}
	e2eeCaps := gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au")
	_ = e2eeCapsFilter.SetProperty("caps", e2eeCaps)

	e2eeVideoQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create E2EE video queue: %w", err)
	}
	_ = e2eeVideoQueue.SetProperty("max-size-buffers", uint(0))
	_ = e2eeVideoQueue.SetProperty("max-size-bytes", uint(0))
	_ = e2eeVideoQueue.SetProperty("max-size-time", uint64(0))

	elements := []*gst.Element{
		videoSrc, videoJitter, videoDepay, h264parse, videoCapsFilter, videoQueue, videoMux, videoMuxQueue, videoHlsSink,
		e2eeVideoSrc, e2eeH264Parse, e2eeCapsFilter, e2eeVideoQueue,
	}
	for _, elem := range elements {
		if err := pipeline.Add(elem); err != nil {
			// Reset codec on failure so another init attempt can be made.
			r.mu.Lock()
			r.videoCodec = ""
			r.mu.Unlock()
			return fmt.Errorf("failed to add %s to pipeline: %w", elem.GetName(), err)
		}
	}

	if err := gst.ElementLinkMany(videoSrc, videoJitter, videoDepay, h264parse, videoCapsFilter, videoQueue); err != nil {
		return fmt.Errorf("failed to link video processing chain: %w", err)
	}

	videoMuxPad := videoMux.GetRequestPad("sink_%d")
	if videoMuxPad == nil {
		return fmt.Errorf("failed to get request pad from video mpegtsmux")
	}
	videoQueueSrc := videoQueue.GetStaticPad("src")
	if videoQueueSrc == nil {
		return fmt.Errorf("failed to get src pad from video queue")
	}
	if linkRet := videoQueueSrc.Link(videoMuxPad); linkRet != gst.PadLinkOK {
		return fmt.Errorf("failed to link video queue to mux: %s", linkRet.String())
	}

	if err := gst.ElementLinkMany(videoMux, videoMuxQueue, videoHlsSink); err != nil {
		return fmt.Errorf("failed to link video mux to hlssink: %w", err)
	}

	if err := gst.ElementLinkMany(e2eeVideoSrc, e2eeH264Parse, e2eeCapsFilter, e2eeVideoQueue); err != nil {
		return fmt.Errorf("failed to link E2EE video processing chain: %w", err)
	}

	e2eeMuxPad := videoMux.GetRequestPad("sink_%d")
	if e2eeMuxPad == nil {
		return fmt.Errorf("failed to get request pad from video mpegtsmux for E2EE")
	}
	e2eeQueueSrc := e2eeVideoQueue.GetStaticPad("src")
	if e2eeQueueSrc == nil {
		return fmt.Errorf("failed to get src pad from E2EE video queue")
	}
	if linkRet := e2eeQueueSrc.Link(e2eeMuxPad); linkRet != gst.PadLinkOK {
		return fmt.Errorf("failed to link E2EE video queue to mux: %s", linkRet.String())
	}

	r.mu.Lock()
	r.videoAppSrc = app.SrcFromElement(videoSrc)
	r.e2eeVideoAppSrc = app.SrcFromElement(e2eeVideoSrc)
	r.videoDepay = videoDepay
	r.mu.Unlock()

	log.Printf("[%s] initialized H.264 video pipeline (MPEG-TS + hlssink)", r.logPrefix())
	return nil
}

func (r *ParticipantRecorder) initAV1VideoPipeline() error {
	r.mu.Lock()
	if r.videoCodec != "" && !strings.EqualFold(r.videoCodec, webrtc.MimeTypeAV1) {
		codec := r.videoCodec
		r.mu.Unlock()
		return fmt.Errorf("video pipeline already initialized for codec %s", codec)
	}
	if r.av1VideoAppSrc != nil {
		r.mu.Unlock()
		return nil
	}
	pipeline := r.pipeline
	outputDir := r.outputDir
	segmentDuration := r.segmentDuration
	r.videoCodec = webrtc.MimeTypeAV1
	r.mu.Unlock()

	if pipeline == nil {
		return fmt.Errorf("pipeline not initialized")
	}
	if segmentDuration <= 0 {
		segmentDuration = 2
	}

	av1Src, err := gst.NewElement("appsrc")
	if err != nil {
		return fmt.Errorf("failed to create AV1 video appsrc: %w", err)
	}
	_ = av1Src.SetProperty("is-live", true)
	_ = av1Src.SetProperty("format", gst.FormatTime)
	_ = av1Src.SetProperty("do-timestamp", false)
	_ = av1Src.SetProperty("emit-signals", true)
	_ = av1Src.SetProperty("block", false)
	_ = av1Src.SetProperty("stream-type", 0)
	_ = av1Src.SetProperty("max-bytes", uint64(10*1024*1024))
	_ = av1Src.SetProperty("caps", gst.NewCapsFromString("video/x-av1,stream-format=obu-stream,alignment=tu"))

	av1Parse, err := gst.NewElement("av1parse")
	if err != nil {
		return fmt.Errorf("failed to create av1parse: %w", err)
	}

	av1CapsFilter, err := gst.NewElement("capsfilter")
	if err != nil {
		return fmt.Errorf("failed to create AV1 capsfilter: %w", err)
	}
	_ = av1CapsFilter.SetProperty("caps", gst.NewCapsFromString("video/x-av1,stream-format=obu-stream,alignment=tu"))

	videoQueue, err := gst.NewElement("queue")
	if err != nil {
		return fmt.Errorf("failed to create AV1 video queue: %w", err)
	}
	_ = videoQueue.SetProperty("max-size-buffers", uint(0))
	_ = videoQueue.SetProperty("max-size-bytes", uint(0))
	_ = videoQueue.SetProperty("max-size-time", uint64(0))

	videoSplitMux, err := gst.NewElement("splitmuxsink")
	if err != nil {
		return fmt.Errorf("failed to create video splitmuxsink: %w", err)
	}
	_ = videoSplitMux.SetProperty("location", filepath.Join(outputDir, "video%05d.m4s"))
	_ = videoSplitMux.SetProperty("max-size-time", uint64(segmentDuration)*uint64(time.Second))
	_ = videoSplitMux.SetProperty("muxer-factory", "cmafmux")
	_ = videoSplitMux.SetProperty("async-finalize", false)
	_ = videoSplitMux.SetProperty("send-keyframe-requests", false)
	_ = videoSplitMux.SetProperty("use-robust-muxing", true)

	// Ensure monotonic timestamps across segments by forcing a higher track timescale.
	// splitmuxsink+cmafmux/mp4mux default to a low (1/10000) timebase which introduces
	// rounding errors at segment boundaries; this can cause players/ffmpeg to drop a
	// frame at the first split keyframe (t≈13s) due to non-monotonic PTS.
	//
	// 60000 aligns exactly with 60000/1001 fps (frame duration = 1001 ticks).
	const av1TrackTimescale uint = 60000
	videoSplitMux.Connect("muxer-added", func(_ *gst.Element, muxer *gst.Element) {
		if muxer == nil {
			return
		}
		_ = muxer.SetProperty("movie-timescale", av1TrackTimescale)
		_ = muxer.SetProperty("trak-timescale", av1TrackTimescale)
		if sinkPad := muxer.GetStaticPad("sink"); sinkPad != nil {
			_ = sinkPad.SetProperty("trak-timescale", av1TrackTimescale)
		}
	})

	elements := []*gst.Element{av1Src, av1Parse, av1CapsFilter, videoQueue, videoSplitMux}
	for _, elem := range elements {
		if err := pipeline.Add(elem); err != nil {
			r.mu.Lock()
			r.videoCodec = ""
			r.mu.Unlock()
			return fmt.Errorf("failed to add %s to pipeline: %w", elem.GetName(), err)
		}
	}

	if err := gst.ElementLinkMany(av1Src, av1Parse, av1CapsFilter, videoQueue); err != nil {
		return fmt.Errorf("failed to link AV1 video processing chain: %w", err)
	}

	videoSplitPad := videoSplitMux.GetRequestPad("video_%u")
	if videoSplitPad == nil {
		videoSplitPad = videoSplitMux.GetRequestPad("video")
	}
	if videoSplitPad == nil {
		return fmt.Errorf("failed to get request pad from video splitmuxsink")
	}
	videoQueueSrc := videoQueue.GetStaticPad("src")
	if videoQueueSrc == nil {
		return fmt.Errorf("failed to get src pad from AV1 video queue")
	}
	if linkRet := videoQueueSrc.Link(videoSplitPad); linkRet != gst.PadLinkOK {
		return fmt.Errorf("failed to link AV1 video queue to splitmuxsink: %s", linkRet.String())
	}

	r.mu.Lock()
	r.av1VideoAppSrc = app.SrcFromElement(av1Src)
	r.mu.Unlock()

	log.Printf("[%s] initialized AV1 video pipeline (CMAF/fMP4 + splitmuxsink)", r.logPrefix())
	return nil
}

// logPrefix returns a standardized log prefix for this recorder.
//
// Format: "room/participant"
//
// This prefix is prepended to all log messages from this recorder instance,
// making it easy to correlate log entries with specific recording sessions
// when multiple participants are being recorded simultaneously.
//
// Example output: "test-room/participant-123"
func (r *ParticipantRecorder) logPrefix() string {
	return fmt.Sprintf("%s/%s", r.room, r.participant)
}

// SetE2EEContext sets the E2EE decryption context for this recorder.
// When set, incoming audio and video RTP payloads will be decrypted
// before being passed to the GStreamer pipeline.
//
// This must be called before tracks are attached. The context should
// be created using NewE2EEContext with the room's SIF trailer.
//
// For E2EE video, we use a frame-level decryption approach:
//  1. RTP packets are collected by timestamp using FrameAssembler
//  2. Packets are sorted by sequence number and assembled into Annex B frames
//  3. Complete frames are decrypted using AES-GCM
//  4. Decrypted Annex B data is pushed to a separate GStreamer pipeline
//     that bypasses RTP jitterbuffer/depayloader
func (r *ParticipantRecorder) SetE2EEContext(ctx *E2EEContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.e2eeCtx = ctx
	// Note: FrameAssembler is created in AttachVideoTrack when the track arrives,
	// because we need the cipher block from the E2EE context at that point.
	// The e2eeVideoEnabled flag and e2eeVideoAppSrc are set up in NewParticipantRecorder.
}

// E2EEEnabled returns true if E2EE decryption is enabled for this recorder.
func (r *ParticipantRecorder) E2EEEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.e2eeCtx != nil && r.e2eeCtx.Enabled()
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

	// Clear pre-buffers to discard warm-up packets
	// Fresh packets will be buffered while waiting for the next keyframe
	// This ensures segment 0 only contains packets from the actual recording
	r.clearPreVideoBuffer()
	r.clearPreAudioBuffer()
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

// H.264 NAL unit types used to detect keyframes and parameter sets.
//
// These constants define NAL unit type values as specified in ITU-T H.264 / ISO/IEC 14496-10.
//
// Parameter sets (must precede video data for decoding):
//   - nalUnitTypeSPS (7): Sequence Parameter Set
//     Contains global codec parameters (resolution, profile, level, etc.)
//   - nalUnitTypePPS (8): Picture Parameter Set
//     Contains picture-specific parameters (entropy coding mode, slice groups, etc.)
//
// Video data:
//   - nalUnitTypeIDR (5): IDR (Instantaneous Decoder Refresh) keyframe
//     Intra-coded frame that can be decoded independently (no dependencies on previous frames)
//
// RTP packetization (RFC 6184):
//   - nalUnitTypeSTAPA (24): Single-Time Aggregation Packet Type A
//     Multiple NAL units in a single RTP packet (used to reduce overhead)
//   - nalUnitTypeFUA (28): Fragmentation Unit Type A
//     Large NAL unit fragmented across multiple RTP packets
//
// Keyframe detection logic:
// A keyframe is identified by the presence of SPS, PPS, or IDR NAL units.
// For HLS recording, we must wait for a keyframe before starting the pipeline
// to ensure all segments begin with decodable video (no P-frame dependencies).
//
// The NAL unit type is encoded in the lower 5 bits of the first payload byte:
//
//	nalType = payload[0] & 0x1F
const (
	nalUnitTypeSPS   = 7  // Sequence Parameter Set
	nalUnitTypePPS   = 8  // Picture Parameter Set
	nalUnitTypeIDR   = 5  // IDR (Instantaneous Decoder Refresh) keyframe
	nalUnitTypeSTAPA = 24 // Single-time Aggregation Packet Type A (multiple NAL units)
	nalUnitTypeFUA   = 28 // Fragmentation Unit Type A (fragmented NAL unit)

	// rtpMTU is the maximum payload size for RTP packets before fragmentation is needed.
	// This is chosen to fit within typical UDP MTU (~1400 bytes) after IP/UDP/RTP headers.
	// NAL units larger than this should be fragmented using FU-A.
	rtpMTU = 1200
)

// fragmentNALToFUA fragments a large NAL unit into FU-A (Fragmentation Unit Type A) packets.
// This is required for NAL units that exceed the RTP MTU size.
//
// FU-A packet format (RFC 6184):
//   - Byte 0: FU indicator: F=0, NRI=from original NAL, Type=28 (FU-A)
//   - Byte 1: FU header: S=start flag, E=end flag, R=0, NAL type
//   - Bytes 2+: Fragment of NAL unit data (without the NAL header byte)
//
// Parameters:
//   - nalData: Complete NAL unit data including NAL header byte
//   - timestamp: RTP timestamp for all fragments
//   - ssrc: SSRC for the RTP packets
//   - payloadType: RTP payload type
//   - startSeqNum: Starting sequence number for the fragments
//   - isLastNAL: Whether this is the last NAL in the access unit (for marker bit)
//
// Returns:
//   - Slice of RTP packets representing the fragmented NAL
//   - The next sequence number to use
func fragmentNALToFUA(nalData []byte, timestamp uint32, ssrc uint32, payloadType uint8, startSeqNum uint16, isLastNAL bool) ([]*rtp.Packet, uint16) {
	if len(nalData) <= rtpMTU {
		// No fragmentation needed
		return nil, startSeqNum
	}

	var packets []*rtp.Packet
	seqNum := startSeqNum

	// Extract NAL header and data
	nalHeader := nalData[0]
	nalType := nalHeader & 0x1F
	nri := nalHeader & 0x60 // NRI bits (bits 5-6)
	nalBody := nalData[1:]  // NAL data without header

	// FU indicator: F=0, NRI=from original, Type=28 (FU-A)
	fuIndicator := nri | nalUnitTypeFUA

	// Fragment the NAL body (without header) into chunks
	// Each chunk can be at most rtpMTU - 2 bytes (for FU indicator and FU header)
	maxChunkSize := rtpMTU - 2
	offset := 0
	fragmentIndex := 0
	totalFragments := (len(nalBody) + maxChunkSize - 1) / maxChunkSize

	for offset < len(nalBody) {
		chunkEnd := offset + maxChunkSize
		if chunkEnd > len(nalBody) {
			chunkEnd = len(nalBody)
		}
		chunk := nalBody[offset:chunkEnd]

		// FU header: S=start, E=end, R=0, Type=original NAL type
		var fuHeader byte
		if fragmentIndex == 0 {
			fuHeader = 0x80 | nalType // S=1, E=0, R=0, Type
		} else if chunkEnd >= len(nalBody) {
			fuHeader = 0x40 | nalType // S=0, E=1, R=0, Type
		} else {
			fuHeader = nalType // S=0, E=0, R=0, Type
		}

		// Build FU-A payload
		payload := make([]byte, 2+len(chunk))
		payload[0] = fuIndicator
		payload[1] = fuHeader
		copy(payload[2:], chunk)

		// Marker bit only on last fragment of last NAL
		marker := (chunkEnd >= len(nalBody)) && isLastNAL

		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Padding:        false,
				Extension:      false,
				Marker:         marker,
				PayloadType:    payloadType,
				SequenceNumber: seqNum,
				Timestamp:      timestamp,
				SSRC:           ssrc,
			},
			Payload: payload,
		}
		packets = append(packets, pkt)

		seqNum++
		offset = chunkEnd
		fragmentIndex++
	}

	// Log fragmentation for debugging (only first few)
	if len(packets) > 0 && totalFragments > 1 {
		log.Printf("[FU-A] fragmented NAL type=%d (%d bytes) into %d fragments, seq=%d-%d",
			nalType, len(nalData), totalFragments, startSeqNum, seqNum-1)
	}

	return packets, seqNum
}

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

// initVideoCaps initializes the GStreamer video appsrc element's capabilities.
//
// This function must be called before pushing video packets to the appsrc.
// It configures the RTP stream parameters that GStreamer needs to properly
// decode the incoming H.264 video stream.
//
// Caps format:
//
//	application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,payload=<PT>
//
// Parameters explained:
//   - media=video: Indicates this is a video stream
//   - encoding-name=H264: Codec is H.264 (AVC)
//   - clock-rate=90000: RTP timestamp clock rate in Hz (H.264 standard)
//   - payload=<PT>: RTP payload type from the track's codec parameters
//
// Initialization is idempotent (only occurs once per recorder instance).
// Subsequent calls are no-ops to prevent re-initializing the pipeline.
//
// Thread-safety: Protected by r.mu mutex.
//
// Parameters:
//   - payloadType: RTP payload type number from the WebRTC track
func (r *ParticipantRecorder) initVideoCaps(payloadType uint8) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.videoInitialized {
		return
	}
	// IMPORTANT: packetization-mode=1 is required for rtph264depay to accept STAP-A packets
	// Without it, rtph264depay assumes mode 0 (single NAL unit) and drops STAP-A/FU-A packets
	// This is critical for E2EE video where we bundle SPS/PPS in STAP-A format
	capsStr := fmt.Sprintf("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,payload=%d,packetization-mode=(string)1", payloadType)
	caps := gst.NewCapsFromString(capsStr)
	_ = r.videoAppSrc.SetProperty("caps", caps)
	r.videoInitialized = true
	log.Printf("[%s] video caps initialized: %s", r.logPrefix(), capsStr)
}

// enqueuePreVideoPacket adds a video RTP packet to the pre-recording buffer.
//
// This buffer stores video packets that arrive before recording is activated,
// ensuring the first HLS segment contains synchronized audio and video when
// the GStreamer pipeline starts.
//
// Buffering strategy:
//  1. Clone the incoming packet (to avoid mutation by the caller)
//  2. If buffer is full (300 packets), discard oldest packet (FIFO)
//  3. Append new packet to buffer
//
// The pre-buffer is used during two critical phases:
//   - Pre-handshake: Buffering while waiting for first keyframe
//   - Pre-recording: Buffering while waiting for ActivateRecording() call
//
// When recording starts, buffered packets are drained via dequeuePreVideoPacket()
// to prime the GStreamer pipeline before live packets are pushed.
//
// Thread-safety: Protected by r.preVideoMu mutex.
//
// Parameters:
//   - pkt: RTP packet to buffer (nil packets are ignored)
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

// dequeuePreVideoPacket removes and returns the oldest video packet from the pre-buffer.
//
// This function is called during recording activation to drain the pre-buffer
// and prime the GStreamer pipeline with buffered packets. By pushing buffered
// packets first, we ensure the first HLS segment contains synchronized audio
// and video streams.
//
// Dequeue behavior:
//   - Returns oldest packet (FIFO ordering)
//   - Removes packet from buffer
//   - Sets removed slot to nil (helps GC)
//   - Returns nil if buffer is empty
//
// Typical usage pattern:
//
//	for {
//	    pkt := r.dequeuePreVideoPacket()
//	    if pkt == nil {
//	        break
//	    }
//	    r.pushVideoPacket(pkt)
//	}
//
// Thread-safety: Protected by r.preVideoMu mutex.
//
// Returns:
//   - RTP packet from buffer, or nil if buffer is empty
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

// clearPreVideoBuffer discards all buffered video packets.
//
// This function is called during cleanup to free memory and prevent
// stale packets from being used in future recordings.
//
// Memory management:
//   - Sets all packet references to nil (helps GC reclaim memory)
//   - Resets slice to nil
//
// Calling this function is typically done:
//   - During Stop() to release resources
//   - After ActivateRecording() if starting fresh (currently commented out)
//
// Thread-safety: Protected by r.preVideoMu mutex.
func (r *ParticipantRecorder) clearPreVideoBuffer() {
	r.preVideoMu.Lock()
	for i := range r.preVideoPackets {
		r.preVideoPackets[i] = nil
	}
	r.preVideoPackets = nil
	r.preVideoMu.Unlock()
}

// enqueuePreAudioPacket adds an audio RTP packet to the pre-recording buffer.
//
// This buffer stores audio packets that arrive before recording is activated.
// Audio buffering is critical because:
//   - Audio packets arrive continuously during handshake/warmup phase
//   - Recording can only start on a video keyframe
//   - Without buffering, early audio would be lost
//
// Buffering strategy (same as video):
//  1. Clone the incoming packet (to avoid mutation by the caller)
//  2. If buffer is full (500 packets), discard oldest packet (FIFO)
//  3. Append new packet to buffer
//
// The audio pre-buffer complements the video pre-buffer:
//   - Video buffer: Ensures keyframe-aligned start
//   - Audio buffer: Ensures no audio gaps when recording starts
//
// Thread-safety: Protected by r.preAudioMu mutex.
//
// Parameters:
//   - pkt: RTP packet to buffer (nil packets are ignored)
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

// dequeuePreAudioPacket removes and returns the oldest audio packet from the pre-buffer.
//
// This function is called during recording activation to drain the audio pre-buffer
// after the video pipeline has been primed with keyframe packets. The audio packets
// are pushed to GStreamer only after video packets to ensure proper stream alignment
// in the muxer.
//
// Sequencing with video:
//  1. First recording keyframe arrives
//  2. Video pipeline is primed with buffered video packets
//  3. THEN audio pipeline starts (this function is called)
//  4. Result: First HLS segment has both audio and video
//
// Thread-safety: Protected by r.preAudioMu mutex.
//
// Returns:
//   - RTP packet from buffer, or nil if buffer is empty
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

// clearPreAudioBuffer discards all buffered audio packets.
//
// This function mirrors clearPreVideoBuffer and is called during cleanup
// to free memory and prevent stale audio packets from being used.
//
// Memory management:
//   - Sets all packet references to nil (helps GC reclaim memory)
//   - Resets slice to nil
//
// Thread-safety: Protected by r.preAudioMu mutex.
func (r *ParticipantRecorder) clearPreAudioBuffer() {
	r.preAudioMu.Lock()
	for i := range r.preAudioPackets {
		r.preAudioPackets[i] = nil
	}
	r.preAudioPackets = nil
	r.preAudioMu.Unlock()
}

// pushVideoPacket converts an RTP packet to a GStreamer buffer and pushes it to the video appsrc.
//
// This function performs four critical operations:
//  1. Timestamp normalization: Converts absolute RTP timestamp to relative (base-0)
//  2. PTS calculation: Converts RTP timestamp to GStreamer ClockTime (nanoseconds)
//  3. RTP marshaling: Serializes the RTP packet to wire format
//  4. Buffer push: Sends the buffer to GStreamer's video appsrc element
//
// Timestamp handling:
// RTP timestamps are 32-bit values that increment continuously. For HLS recording,
// we need timestamps to start at 0 for the first packet. This function calls
// videoClockTime() to normalize timestamps and convert from 90kHz clock to nanoseconds.
//
// Example:
//
//	First packet:  RTP TS = 3840000000 → Relative TS = 0 → PTS = 0ns
//	Second packet: RTP TS = 3840003000 → Relative TS = 3000 → PTS = 33333333ns (33.3ms at 30fps)
//
// GStreamer buffer properties:
//   - Data: Marshaled RTP packet (header + payload)
//   - PTS: Presentation timestamp in nanoseconds (for muxer synchronization)
//
// Error handling:
//   - Marshal failure: Returns error (malformed RTP packet)
//   - FlowFlushing: Pipeline is shutting down (expected during Stop())
//   - Other flow errors: Unexpected pipeline state (logged and returned)
//
// Thread-safety:
//   - videoClockTime() is protected by r.mu
//   - videoLastPTS/videoLastTimestamp updates are protected by r.mu
//
// Parameters:
//   - pkt: RTP packet to push (modified in-place: Timestamp field normalized)
//
// Returns:
//   - nil on success
//   - error if marshaling fails or appsrc rejects the buffer
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

// initE2EEVideoCaps sets the capabilities on the E2EE video appsrc.
// This must be called before pushing E2EE video frames.
//
// The E2EE video pipeline receives decrypted Annex B H.264 data directly,
// bypassing the RTP jitterbuffer and depayloader. The caps specify:
//   - stream-format=byte-stream: Annex B format with start codes
//   - alignment=au: Each buffer contains complete access units (frames)
func (r *ParticipantRecorder) initE2EEVideoCaps() {
	if r.e2eeVideoAppSrc == nil {
		return
	}
	caps := gst.NewCapsFromString("video/x-h264,stream-format=byte-stream,alignment=au")
	r.e2eeVideoAppSrc.SetCaps(caps)
	log.Printf("[%s] E2EE video appsrc caps initialized", r.logPrefix())
}

// pushE2EEVideoFrame pushes a decrypted Annex B video frame to the E2EE video pipeline.
//
// Unlike pushVideoPacket which handles RTP packets, this method receives raw H.264
// Annex B data (with start codes) that has already been decrypted by the FrameAssembler.
// The data is pushed directly to h264parse, bypassing the RTP jitterbuffer/depayloader.
//
// Parameters:
//   - annexBData: Complete H.264 access unit in Annex B format (with start codes)
//   - rtpTimestamp: Original RTP timestamp for PTS calculation
//   - isKeyframe: True if this frame contains an IDR slice (for logging)
//
// Returns:
//   - nil on success
//   - error if appsrc rejects the buffer
func (r *ParticipantRecorder) pushE2EEVideoFrame(annexBData []byte, rtpTimestamp uint32, isKeyframe bool) error {
	if r.e2eeVideoAppSrc == nil {
		return fmt.Errorf("E2EE video appsrc not initialized")
	}

	pts, _ := r.videoClockTime(rtpTimestamp)

	buffer := gst.NewBufferFromBytes(annexBData)
	buffer.SetPresentationTimestamp(pts)

	// Set buffer flags for proper HLS segmentation:
	// - Keyframes (IDR) should NOT have DELTA_UNIT flag (hlssink segments at these)
	// - Non-keyframes (P/B frames) MUST have DELTA_UNIT flag
	if !isKeyframe {
		buffer.SetFlags(gst.BufferFlagDeltaUnit)
	}

	r.mu.Lock()
	r.videoLastPTS = pts
	r.videoPacketCount++ // Count frames for E2EE path
	if isKeyframe {
		r.videoKeyframeCount++
	}
	r.videoBytesReceived += int64(len(annexBData))
	r.mu.Unlock()

	if flow := r.e2eeVideoAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
		if flow == gst.FlowFlushing {
			return fmt.Errorf("E2EE video appsrc flushing")
		}
		return fmt.Errorf("E2EE video appsrc push failed: %s", flow.String())
	}
	return nil
}

// pushAV1VideoFrame pushes an AV1 OBU stream (one temporal unit per buffer) to the AV1 video pipeline.
func (r *ParticipantRecorder) pushAV1VideoFrame(obuStream []byte, rtpTimestamp uint32, isKeyframe bool) error {
	if r.av1VideoAppSrc == nil {
		return fmt.Errorf("AV1 video appsrc not initialized")
	}

	pts, relative := r.videoClockTime(rtpTimestamp)

	buffer := gst.NewBufferFromBytes(obuStream)
	buffer.SetPresentationTimestamp(pts)

	if !isKeyframe {
		buffer.SetFlags(gst.BufferFlagDeltaUnit)
	}

	r.mu.Lock()
	r.videoLastPTS = pts
	r.videoLastTimestamp = relative
	r.videoPacketCount++
	if isKeyframe {
		r.videoKeyframeCount++
	}
	r.videoBytesReceived += int64(len(obuStream))
	r.mu.Unlock()

	if flow := r.av1VideoAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
		if flow == gst.FlowFlushing {
			return fmt.Errorf("AV1 video appsrc flushing")
		}
		return fmt.Errorf("AV1 video appsrc push failed: %s", flow.String())
	}
	return nil
}

// injectParameterSets creates a synthetic STAP-A RTP packet containing H.264 parameter sets.
//
// This function is called when recording starts but no SPS/PPS NAL units have been
// received in the RTP stream. It uses parameter sets from the SDP fmtp line
// (sprop-parameter-sets) to ensure decoders have the required codec information.
//
// Algorithm:
//  1. Validate input NAL units (non-empty, size < 64KB)
//  2. Create STAP-A payload header (0x78 = F:0, NRI:3, Type:24)
//  3. For each NAL unit:
//     - Classify as SPS or PPS
//     - Append 2-byte length prefix (big-endian)
//     - Append NAL unit bytes
//  4. Create synthetic RTP packet with modified sequence number
//  5. Push to GStreamer via pushVideoPacket()
//
// STAP-A packet format (RFC 6184 Section 5.7.1):
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|F|NRI|  Type   |         NALU 1 Size           | NALU 1 HDR    |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                         NALU 1 Data...                        |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|         NALU 2 Size           | NALU 2 HDR    | NALU 2 Data...|
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//
// Synthetic RTP header construction:
//   - Version: 2 (RTP version)
//   - PayloadType: Copied from reference packet (maintains stream consistency)
//   - SequenceNumber: reference.SequenceNumber - 1 (inserted before keyframe)
//   - Timestamp: Same as reference (parameter sets have no duration)
//   - SSRC: Copied from reference (maintains stream identity)
//   - Marker: false (not end of frame)
//
// Why inject before keyframe:
// Parameter sets must arrive before the IDR frame to allow decoding. By using
// sequence number (reference - 1), we ensure proper ordering in the GStreamer
// jitterbuffer and rtph264depay elements.
//
// Parameters:
//   - reference: RTP packet to use as template (typically the first keyframe)
//   - nalUnits: List of NAL units to inject (from SDP sprop-parameter-sets)
//
// Returns:
//   - spsInjected: true if at least one SPS was included
//   - ppsInjected: true if at least one PPS was included
//   - error: if no valid NAL units or pushVideoPacket fails
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

// videoClockTime converts an RTP timestamp to GStreamer ClockTime (nanoseconds).
//
// This function performs two critical timestamp transformations:
//  1. Normalization: Converts absolute RTP timestamp to relative (base-0)
//  2. Clock conversion: Converts from 90kHz RTP clock to nanoseconds
//
// Algorithm:
//  1. On first call: Initialize timestamp base (videoTimestampBase = ts)
//  2. Subtract base from current timestamp to get relative value
//  3. Convert from 90kHz clock to nanoseconds using formula:
//     nanoseconds = (ticks * 1_000_000_000) / 90000
//
// H.264 RTP timestamp clock (RFC 6184):
// H.264 video uses a 90kHz RTP timestamp clock, meaning each timestamp unit
// represents 1/90000 of a second (approximately 11.1 microseconds).
//
// Example conversions (30fps video, 3000 ticks per frame):
//
//	First packet:   RTP TS = 3840000000 → Base = 3840000000 → Relative = 0       → PTS = 0ns
//	Second packet:  RTP TS = 3840003000 → Base = 3840000000 → Relative = 3000    → PTS = 33333333ns (33.3ms)
//	Third packet:   RTP TS = 3840006000 → Base = 3840000000 → Relative = 6000    → PTS = 66666666ns (66.7ms)
//	Frame at 1sec:  RTP TS = 3840090000 → Base = 3840000000 → Relative = 90000   → PTS = 1000000000ns (1.0s)
//
// Timestamp wraparound handling:
// This function uses uint32 subtraction which automatically handles wraparound
// correctly due to modular arithmetic. For example:
//
//	Base = 4294960000, Current = 10000 (wrapped around)
//	Relative = uint32(10000 - 4294960000) = uint32(-4294950000) = 17296 (correct)
//
// Thread-safety:
//   - Protected by r.mu mutex
//   - videoTimestampInit and videoTimestampBase are accessed under lock
//
// Parameters:
//   - ts: RTP timestamp from video packet (32-bit, 90kHz clock)
//
// Returns:
//   - ClockTime: GStreamer presentation timestamp in nanoseconds
//   - uint32: Normalized relative timestamp (for stats/logging)
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

// audioClockTime converts an RTP timestamp to GStreamer ClockTime (nanoseconds).
//
// This function is the audio counterpart to videoClockTime(). It performs the same
// two transformations but uses the Opus audio clock rate (48kHz) instead of the
// H.264 video clock rate (90kHz).
//
// Algorithm:
//  1. On first call: Initialize timestamp base (audioTimestampBase = ts)
//  2. Subtract base from current timestamp to get relative value
//  3. Convert from 48kHz clock to nanoseconds using formula:
//     nanoseconds = (ticks * 1_000_000_000) / 48000
//
// Opus RTP timestamp clock (RFC 7587):
// Opus audio uses a 48kHz RTP timestamp clock, meaning each timestamp unit
// represents 1/48000 of a second (approximately 20.8 microseconds).
//
// Example conversions (20ms audio frames, 960 samples per frame):
//
//	First packet:   RTP TS = 2160000000 → Base = 2160000000 → Relative = 0       → PTS = 0ns
//	Second packet:  RTP TS = 2160000960 → Base = 2160000000 → Relative = 960     → PTS = 20000000ns (20ms)
//	Third packet:   RTP TS = 2160001920 → Base = 2160000000 → Relative = 1920    → PTS = 40000000ns (40ms)
//	Frame at 1sec:  RTP TS = 2160048000 → Base = 2160000000 → Relative = 48000   → PTS = 1000000000ns (1.0s)
//
// Why separate audio and video timestamp bases:
// Audio and video RTP streams have independent timestamp origins. For HLS muxing,
// both streams must start at PTS=0, so we normalize each stream independently.
// The muxer (mpegtsmux) synchronizes the streams based on their PTS values.
//
// Thread-safety:
//   - Protected by r.mu mutex (same mutex as video, safe because we lock)
//   - audioTimestampInit and audioTimestampBase are accessed under lock
//
// Parameters:
//   - ts: RTP timestamp from audio packet (32-bit, 48kHz clock)
//
// Returns:
//   - ClockTime: GStreamer presentation timestamp in nanoseconds
//   - uint32: Normalized relative timestamp (for stats/logging)
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

// signalVideoReady marks the video stream as ready and triggers callbacks.
//
// This function is called when the first video keyframe is received during the
// handshake/warm-up phase. It performs four critical state transitions:
//  1. Set videoReady flag (atomic, prevents duplicate signals)
//  2. Set handshakeReady flag (indicates SPS/PPS established)
//  3. Close videoReadyCh channel (unblocks goroutines waiting for video)
//  4. Invoke onVideoReady callback (notifies handler that recording can start)
//
// State management:
// The function uses CompareAndSwap to ensure it only executes once, even if
// called multiple times (which can happen if multiple keyframe packets arrive
// in quick succession).
//
// Handshake ready vs video ready:
//   - videoReady: Internal flag indicating first keyframe processed
//   - handshakeReady: Exported flag (via HandshakeReady()) for external checks
//   - Both are set atomically by this function
//
// Channel closure:
// Closing videoReadyCh is a Go idiom for broadcasting to multiple goroutines
// that video is ready. Any goroutine blocking on <-r.videoReadyCh will unblock.
// The sync.Once ensures the channel is closed exactly once (closing twice panics).
//
// Callback invocation:
// The onVideoReady callback (set via SetOnVideoReady) is typically used by the
// agent handler to transition from handshake publisher to recording publisher.
//
// Thread-safety:
//   - videoReady: atomic.Bool (lock-free)
//   - handshakeReady: atomic.Bool (lock-free)
//   - videoReadyOnce: sync.Once (ensures single channel close)
//   - onVideoReady callback access: protected by r.mu mutex
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

// requestPLI sends a Picture Loss Indication (PLI) request to the video sender.
//
// PLI is an RTCP feedback message (RFC 4585) that requests the sender to generate
// a new keyframe (IDR frame). This is used in two scenarios:
//  1. Initial keyframe request: When track is first attached (no video received yet)
//  2. Recovery from packet loss: When waiting for keyframe during recording activation
//
// Why PLI is needed:
// WebRTC senders typically send keyframes infrequently (every few seconds) to
// reduce bandwidth. When the recorder needs a keyframe immediately (e.g., to start
// recording or recover from errors), it sends PLI to request one.
//
// PLI vs FIR:
// PLI (Picture Loss Indication) is preferred over FIR (Full Intra Request) in
// modern WebRTC implementations because it's simpler and doesn't require maintaining
// sequence numbers. Both achieve the same goal: requesting a keyframe.
//
// The writer function:
// The writer function is typically a closure over the WebRTC PeerConnection's
// WriteRTCP method. It sends the PLI RTCP packet to the video sender.
//
// Example usage in AttachVideoTrack:
//
//	if handshakeWait%200 == 0 {
//	    log.Printf("waiting for keyframe, sending PLI")
//	    r.requestPLI(pliWriter, track.SSRC())
//	}
//
// Statistics:
// This function increments videoPLIRequests counter (protected by mutex) for
// debugging and monitoring purposes.
//
// Thread-safety:
//   - videoPLIRequests: protected by r.mu mutex
//   - writer function: assumed to be thread-safe (WebRTC SDK guarantees this)
//
// Parameters:
//   - writer: Function to send PLI (typically PeerConnection.WriteRTCP wrapper)
//   - ssrc: SSRC of the video stream to request keyframe from
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

		switch strings.ToLower(track.Codec().MimeType) {
		case strings.ToLower(webrtc.MimeTypeAV1):
			if err := r.initAV1VideoPipeline(); err != nil {
				log.Printf("[%s] failed to initialize AV1 video pipeline: %v", r.logPrefix(), err)
				return
			}
			r.attachAV1VideoTrack(ctx, track, pliWriter)
			return
		case strings.ToLower(webrtc.MimeTypeH264):
			if err := r.initH264VideoPipeline(); err != nil {
				log.Printf("[%s] failed to initialize H.264 video pipeline: %v", r.logPrefix(), err)
				return
			}
		default:
			log.Printf("[%s] unsupported video codec: %s", r.logPrefix(), track.Codec().MimeType)
			return
		}

		var handshakeWait, recordingWait int
		var videoE2EEErrors, videoE2EEFrames int
		firstPacket := true

		// Frame-level assembler for E2EE video decryption.
		// This replaces the old H264E2EEAssembler per-packet approach.
		// E2EE video requires frame-level decryption because:
		// 1. LiveKit E2EE encrypts complete Annex B frames
		// 2. RTP packetization fragments these encrypted frames (FU-A)
		// 3. We must reassemble and decrypt whole frames
		var frameAssembler *FrameAssembler
		var e2eeVideoInitialized bool

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

			// Check pre-video buffer (for non-E2EE video only)
			// E2EE video uses frame-level processing and bypasses the RTP buffer
			if !e2eeVideoInitialized && r.recordingActive.Load() {
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

				// Request initial keyframe on first packet (before any processing)
				if firstPacket {
					firstPacket = false
					log.Printf("[%s] requesting initial keyframe via PLI (seq=%d)", r.logPrefix(), rtpPacket.SequenceNumber)
					r.requestPLI(pliWriter, track.SSRC())
				}

				// For E2EE video: use FrameAssembler for frame-level decryption
				// This is the CORRECT approach: collect RTP packets, assemble complete
				// Annex B frames, decrypt whole frames, push to e2eeVideoAppSrc
				e2eeEnabled := r.E2EEEnabled()
				if e2eeEnabled && len(rtpPacket.Payload) > 0 {
					// Initialize frame assembler on first encrypted packet
					if frameAssembler == nil {
						r.mu.Lock()
						e2eeCtx := r.e2eeCtx
						r.mu.Unlock()

						if e2eeCtx != nil && e2eeCtx.Enabled() {
							e2eeCtx.mu.RLock()
							cipherBlock := e2eeCtx.cipherBlock
							sifTrailer := e2eeCtx.sifTrailer
							e2eeCtx.mu.RUnlock()

							frameAssembler = NewFrameAssembler(cipherBlock, sifTrailer, r.logPrefix())
							r.mu.Lock()
							r.frameAssembler = frameAssembler
							r.e2eeVideoEnabled = true
							r.mu.Unlock()
							e2eeVideoInitialized = true
							r.initE2EEVideoCaps()
							log.Printf("[%s] initialized E2EE video FrameAssembler (frame-level decryption)", r.logPrefix())
						}
					}

					if frameAssembler != nil {
						// Add packet to assembler - it collects by timestamp and decrypts on frame completion
						frame, err := frameAssembler.AddPacket(rtpPacket)
						if err != nil {
							videoE2EEErrors++
							if videoE2EEErrors <= 10 {
								log.Printf("[%s] video E2EE frame assembler error (seq=%d): %v",
									r.logPrefix(), rtpPacket.SequenceNumber, err)
							}
							continue
						}

						if frame == nil {
							// Packet accumulated, waiting for more packets to complete the frame
							continue
						}

						// Complete frame decrypted - process it
						videoE2EEFrames++
						isKeyframe := frame.IsKeyframe

						if videoE2EEFrames <= 5 {
							log.Printf("[%s] E2EE video frame %d decrypted: %d bytes, keyframe=%v, ts=%d",
								r.logPrefix(), videoE2EEFrames, len(frame.Data), isKeyframe, frame.Timestamp)
						}

						// E2EE video frame processing:
						// Handle handshake (wait for first keyframe) and recording activation
						// using decrypted frame's keyframe status instead of RTP inspection

						if !r.handshakeReady.Load() {
							if isKeyframe {
								r.mu.Lock()
								r.videoKeyframeCount++
								r.mu.Unlock()
								r.signalVideoReady()
								log.Printf("[%s] E2EE: received warm-up keyframe (ts=%d)", r.logPrefix(), frame.Timestamp)

								// If recording was activated before the first keyframe arrived (AUTO_ACTIVATE_RECORDING),
								// use this very first keyframe to start the pipeline so we don't miss frame 0.
								if r.recordingActive.Load() && r.recordingKeyframePending.Load() {
									if !r.pipelineStarted.Load() {
										log.Printf("[%s] E2EE: starting GStreamer pipeline with first recording keyframe ts=%d", r.logPrefix(), frame.Timestamp)
										if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
											log.Printf("[%s] failed to start pipeline: %v", r.logPrefix(), err)
											return
										}
										r.pipeline.DebugBinToDotFileWithTs(gst.DebugGraphShowAll, "publisher_recorder_e2ee")
										r.pipelineStarted.Store(true)
									} else {
										log.Printf("[%s] E2EE: starting active recording with keyframe ts=%d", r.logPrefix(), frame.Timestamp)
									}

									// Push decrypted Annex B frame to E2EE video appsrc.
									if err := r.pushE2EEVideoFrame(frame.Data, frame.Timestamp, isKeyframe); err != nil {
										log.Printf("[%s] E2EE video push error: %v", r.logPrefix(), err)
										return
									}

									// Drop any buffered audio packets from before the recording keyframe.
									// When we wait for a keyframe, we must start both audio and video at
									// that keyframe boundary to avoid A/V desync and timestamp underflow
									// when publishers restart (handshake → main publish).
									r.clearPreAudioBuffer()

									// Allow audio to start now.
									r.recordingKeyframePending.Store(false)
								}
							} else {
								handshakeWait++
								if handshakeWait == 1 || handshakeWait%200 == 0 {
									log.Printf("[%s] E2EE: warm-up waiting for keyframe, sending PLI", r.logPrefix())
									r.requestPLI(pliWriter, track.SSRC())
								}
							}
							continue
						}

						if !r.recordingActive.Load() {
							// Not recording yet - just skip
							continue
						}

						if r.recordingKeyframePending.Load() {
							if isKeyframe {
								r.mu.Lock()
								r.videoKeyframeCount++
								r.mu.Unlock()

								// Start pipeline on first recording keyframe
								if !r.pipelineStarted.Load() {
									log.Printf("[%s] E2EE: starting GStreamer pipeline with first recording keyframe ts=%d", r.logPrefix(), frame.Timestamp)
									if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
										log.Printf("[%s] failed to start pipeline: %v", r.logPrefix(), err)
										return
									}
									r.pipeline.DebugBinToDotFileWithTs(gst.DebugGraphShowAll, "publisher_recorder_e2ee")
									r.pipelineStarted.Store(true)
								} else {
									log.Printf("[%s] E2EE: starting active recording with keyframe ts=%d", r.logPrefix(), frame.Timestamp)
								}

								// Push decrypted Annex B frame to E2EE video appsrc
								if err := r.pushE2EEVideoFrame(frame.Data, frame.Timestamp, isKeyframe); err != nil {
									log.Printf("[%s] E2EE video push error: %v", r.logPrefix(), err)
									return
								}

								// Drop any buffered audio packets from before the recording keyframe.
								r.clearPreAudioBuffer()

								// Allow audio to start now
								r.recordingKeyframePending.Store(false)
								continue
							} else {
								recordingWait++
								if recordingWait == 1 || recordingWait%200 == 0 {
									log.Printf("[%s] E2EE: waiting for keyframe to begin recording, sending PLI", r.logPrefix())
									r.requestPLI(pliWriter, track.SSRC())
								}
								continue
							}
						}

						// Normal E2EE video recording: push decrypted Annex B frame
						if err := r.pushE2EEVideoFrame(frame.Data, frame.Timestamp, isKeyframe); err != nil {
							log.Printf("[%s] E2EE video push error: %v", r.logPrefix(), err)
							return
						}
						continue
					}
				}
			}

			// Skip non-E2EE processing for E2EE-enabled video tracks
			// E2EE video is handled completely above via pushE2EEVideoFrame
			if e2eeVideoInitialized {
				continue
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

					// Drop any buffered audio packets from before the recording keyframe.
					r.clearPreAudioBuffer()

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

func (r *ParticipantRecorder) attachAV1VideoTrack(ctx context.Context, track *webrtc.TrackRemote, pliWriter func(webrtc.SSRC)) {
	assembler := NewAV1FrameAssembler(r.logPrefix())

	var handshakeWait, recordingWait int
	var e2eeErrors, e2eeFrames int
	firstPacket := true

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[%s] AV1 video track read error: %v", r.logPrefix(), err)
			}
			return
		}

		if firstPacket {
			firstPacket = false
			log.Printf("[%s] requesting initial AV1 keyframe via PLI (seq=%d)", r.logPrefix(), rtpPacket.SequenceNumber)
			r.requestPLI(pliWriter, track.SSRC())
		}

		if len(rtpPacket.Payload) == 0 {
			continue
		}

		frame, err := assembler.AddPacket(rtpPacket)
		if err != nil {
			if e2eeErrors < 10 {
				log.Printf("[%s] AV1 depacketize error (seq=%d): %v", r.logPrefix(), rtpPacket.SequenceNumber, err)
			}
			e2eeErrors++
			continue
		}
		if frame == nil {
			continue
		}

		obuStream := frame.Data
		isKeyframe := isAV1OBUStreamKeyframe(obuStream)

		if !r.handshakeReady.Load() {
			if isKeyframe {
				r.mu.Lock()
				r.videoKeyframeCount++
				r.mu.Unlock()
				r.signalVideoReady()
				log.Printf("[%s] received warm-up AV1 keyframe (ts=%d)", r.logPrefix(), frame.Timestamp)

				// If recording was activated before the first keyframe arrived (AUTO_ACTIVATE_RECORDING),
				// use this very first keyframe to start the pipeline so we don't miss frame 0.
				if r.recordingActive.Load() && r.recordingKeyframePending.Load() {
					if !r.pipelineStarted.Load() {
						log.Printf("[%s] starting GStreamer pipeline with first AV1 recording keyframe ts=%d", r.logPrefix(), frame.Timestamp)
						if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
							log.Printf("[%s] failed to start pipeline: %v", r.logPrefix(), err)
							return
						}
						r.pipeline.DebugBinToDotFileWithTs(gst.DebugGraphShowAll, "publisher_recorder_av1")
						r.pipelineStarted.Store(true)
					} else {
						log.Printf("[%s] starting active recording with AV1 keyframe ts=%d", r.logPrefix(), frame.Timestamp)
					}

					frameData := obuStream
					if r.E2EEEnabled() {
						r.mu.Lock()
						e2eeCtx := r.e2eeCtx
						r.mu.Unlock()
						if e2eeCtx != nil && e2eeCtx.Enabled() {
							e2eeCtx.mu.RLock()
							cipherBlock := e2eeCtx.cipherBlock
							sifTrailer := e2eeCtx.sifTrailer
							e2eeCtx.mu.RUnlock()

							decrypted, err := decryptAV1E2EEOBUStream(frameData, cipherBlock, sifTrailer)
							if err != nil {
								if e2eeErrors < 10 {
									log.Printf("[%s] AV1 E2EE decryption error (ts=%d): %v", r.logPrefix(), frame.Timestamp, err)
								}
								e2eeErrors++
								continue
							}
							if decrypted == nil {
								continue
							}
							frameData = decrypted
							e2eeFrames++
						}
					}

					if err := r.pushAV1VideoFrame(frameData, frame.Timestamp, isKeyframe); err != nil {
						log.Printf("[%s] AV1 video push error: %v", r.logPrefix(), err)
						return
					}

					// Drop any buffered audio packets from before the recording keyframe.
					r.clearPreAudioBuffer()

					r.recordingKeyframePending.Store(false)
				}
			} else {
				handshakeWait++
				if handshakeWait == 1 || handshakeWait%200 == 0 {
					log.Printf("[%s] warm-up waiting for AV1 keyframe, sending PLI", r.logPrefix())
					r.requestPLI(pliWriter, track.SSRC())
				}
			}
			continue
		}

		if !r.recordingActive.Load() {
			continue
		}

		if r.recordingKeyframePending.Load() {
			if isKeyframe {
				// Start pipeline on first recording keyframe to avoid invalid HLS segments.
				if !r.pipelineStarted.Load() {
					log.Printf("[%s] starting GStreamer pipeline with first AV1 recording keyframe ts=%d", r.logPrefix(), frame.Timestamp)
					if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
						log.Printf("[%s] failed to start pipeline: %v", r.logPrefix(), err)
						return
					}
					r.pipeline.DebugBinToDotFileWithTs(gst.DebugGraphShowAll, "publisher_recorder_av1")
					r.pipelineStarted.Store(true)
				} else {
					log.Printf("[%s] starting active recording with AV1 keyframe ts=%d", r.logPrefix(), frame.Timestamp)
				}

				// Decrypt (if needed) and push first keyframe BEFORE allowing audio to start.
				frameData := obuStream
				if r.E2EEEnabled() {
					r.mu.Lock()
					e2eeCtx := r.e2eeCtx
					r.mu.Unlock()
					if e2eeCtx != nil && e2eeCtx.Enabled() {
						e2eeCtx.mu.RLock()
						cipherBlock := e2eeCtx.cipherBlock
						sifTrailer := e2eeCtx.sifTrailer
						e2eeCtx.mu.RUnlock()

						decrypted, err := decryptAV1E2EEOBUStream(frameData, cipherBlock, sifTrailer)
						if err != nil {
							if e2eeErrors < 10 {
								log.Printf("[%s] AV1 E2EE decryption error (ts=%d): %v", r.logPrefix(), frame.Timestamp, err)
							}
							e2eeErrors++
							continue
						}
						if decrypted == nil {
							continue
						}
						frameData = decrypted
						e2eeFrames++
					}
				}

				if err := r.pushAV1VideoFrame(frameData, frame.Timestamp, isKeyframe); err != nil {
					log.Printf("[%s] AV1 video push error: %v", r.logPrefix(), err)
					return
				}

				// Drop any buffered audio packets from before the recording keyframe.
				r.clearPreAudioBuffer()

				r.recordingKeyframePending.Store(false)
				continue
			}

			recordingWait++
			if recordingWait == 1 || recordingWait%200 == 0 {
				log.Printf("[%s] waiting for AV1 keyframe to begin recording, sending PLI", r.logPrefix())
				r.requestPLI(pliWriter, track.SSRC())
			}
			continue
		}

		frameData := obuStream
		if r.E2EEEnabled() {
			r.mu.Lock()
			e2eeCtx := r.e2eeCtx
			r.mu.Unlock()
			if e2eeCtx != nil && e2eeCtx.Enabled() {
				e2eeCtx.mu.RLock()
				cipherBlock := e2eeCtx.cipherBlock
				sifTrailer := e2eeCtx.sifTrailer
				e2eeCtx.mu.RUnlock()

				decrypted, err := decryptAV1E2EEOBUStream(frameData, cipherBlock, sifTrailer)
				if err != nil {
					if e2eeErrors < 10 {
						log.Printf("[%s] AV1 E2EE decryption error (ts=%d): %v", r.logPrefix(), frame.Timestamp, err)
					}
					e2eeErrors++
					continue
				}
				if decrypted == nil {
					continue
				}
				frameData = decrypted
				e2eeFrames++
			}
		}

		if err := r.pushAV1VideoFrame(frameData, frame.Timestamp, isKeyframe); err != nil {
			log.Printf("[%s] AV1 video push error: %v", r.logPrefix(), err)
			return
		}
	}
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

				// Decrypt E2EE-encrypted payload if E2EE is enabled
				if r.E2EEEnabled() && len(rtpPacket.Payload) > 0 {
					r.mu.Lock()
					e2eeCtx := r.e2eeCtx
					r.mu.Unlock()

					decrypted, err := e2eeCtx.DecryptAudio(rtpPacket.Payload)
					if err != nil {
						// Decryption failed - log and skip packet
						log.Printf("[%s] audio E2EE decryption error (seq=%d): %v", r.logPrefix(), rtpPacket.SequenceNumber, err)
						continue
					}
					if decrypted == nil {
						// Server Injected Frame - drop it
						continue
					}
					rtpPacket.Payload = decrypted
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
				r.audioInitialized = true
				log.Printf("[%s] audio initialized (raw Opus mode, caps set in constructor)", r.logPrefix())
			}
			r.mu.Unlock()

			// Use RTP timestamps (48kHz) to keep audio timing aligned with video.
			audioPTS, relative := r.audioClockTime(rtpPacket.Timestamp)
			r.mu.Lock()
			r.audioLastPTS = audioPTS
			r.audioLastTimestamp = relative
			r.mu.Unlock()

			// Push raw Opus payload with explicit RTP-derived PTS.
			buffer := gst.NewBufferFromBytes(rtpPacket.Payload)
			buffer.SetPresentationTimestamp(audioPTS)

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
	if r.videoEnded {
		r.mu.Unlock()
		return
	}
	r.videoEnded = true
	videoCodec := r.videoCodec
	e2eeEnabled := r.e2eeVideoEnabled
	videoAppSrc := r.videoAppSrc
	e2eeVideoAppSrc := r.e2eeVideoAppSrc
	av1VideoAppSrc := r.av1VideoAppSrc
	r.mu.Unlock()

	if strings.EqualFold(videoCodec, webrtc.MimeTypeAV1) {
		if av1VideoAppSrc != nil {
			av1VideoAppSrc.EndStream()
		}
		return
	}

	if e2eeEnabled {
		if e2eeVideoAppSrc != nil {
			e2eeVideoAppSrc.EndStream()
		}
		return
	}

	if videoAppSrc != nil {
		videoAppSrc.EndStream()
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
			_ = r.pipeline.SetState(gst.StateNull)
		}

		r.clearPreVideoBuffer()
		r.clearPreAudioBuffer()

		r.mu.Lock()
		videoCodec := r.videoCodec
		r.mu.Unlock()

		if strings.EqualFold(videoCodec, webrtc.MimeTypeH264) {
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
		}

		// Process audio segments to extract init and strip duplicate moov data
		// This converts self-contained segments (with ftyp+moov in each) to proper CMAF format
		// with a separate init segment (audio_init.mp4) and media-only segments
		if err := r.processAudioSegments(); err != nil {
			log.Printf("[%s] failed to process audio segments: %v", r.logPrefix(), err)
		}

		if strings.EqualFold(videoCodec, webrtc.MimeTypeAV1) {
			if err := r.processAV1VideoSegments(); err != nil {
				log.Printf("[%s] failed to process video segments: %v", r.logPrefix(), err)
			}
			if err := r.writeAV1VideoHLSPlaylist(); err != nil {
				log.Printf("[%s] failed to write video HLS playlist: %v", r.logPrefix(), err)
			} else {
				log.Printf("[%s] wrote video.m3u8 HLS playlist", r.logPrefix())
			}
		}

		// Finalize audio manifest with all segments using ACTUAL timing from segment data
		// CRITICAL: With real-time S3 upload, files are deleted after upload, so we need to
		// use the timing info tracked by the uploader. If no uploader, fall back to disk.
		if r.audioManifest != nil {
			const opusTimescale = 48000 // Opus uses 48kHz sample rate
			var segmentsAdded int

			// Try to get segment info from S3 uploader (files may be deleted)
			if r.s3Uploader != nil {
				trackedSegments := r.s3Uploader.GetAudioSegmentsInfo()
				for _, seg := range trackedSegments {
					r.audioManifest.AddSegment(seg.Index, seg.Filename, seg.Duration, seg.StartTime)
					log.Printf("[%s] manifest segment %d (from S3 tracker): file=%s, startTime=%.3fs, duration=%.3fs",
						r.logPrefix(), seg.Index, seg.Filename, seg.StartTime, seg.Duration)
				}
				segmentsAdded = len(trackedSegments)
				log.Printf("[%s] populated manifest from S3 uploader: %d segments", r.logPrefix(), segmentsAdded)
			}

			// If no segments from uploader, try reading from disk (non-realtime upload case)
			if segmentsAdded == 0 {
				var cumulativeStartTime float64 = 0
				for i := 0; ; i++ {
					segmentFile := fmt.Sprintf("audio%05d.m4s", i)
					segmentPath := filepath.Join(r.outputDir, segmentFile)
					if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
						break
					}

					// Parse segment to get actual timing from tfdt and duration from trun
					segInfo, err := getAudioSegmentInfo(segmentPath, opusTimescale)
					if err != nil {
						// Fall back to cumulative duration if parsing fails
						log.Printf("[%s] warning: failed to parse segment %d timing: %v, using cumulative", r.logPrefix(), i, err)
						duration := float64(r.audioManifest.manifest.SegmentDuration)
						r.audioManifest.AddSegment(i, segmentFile, duration, cumulativeStartTime)
						cumulativeStartTime += duration
						segmentsAdded++
						continue
					}

					// Use tfdt-based start time if it's valid (non-zero or first segment)
					// If all segments have tfdt=0 (splitmuxsink resets timing), use cumulative
					startTime := segInfo.StartTimeSeconds
					if i > 0 && segInfo.BaseDecodeTime == 0 {
						// tfdt is 0 for non-first segment, means splitmuxsink resets timing per segment
						// Fall back to cumulative approach
						log.Printf("[%s] segment %d: tfdt=0 (reset), using cumulative startTime=%.3fs, duration=%.3fs",
							r.logPrefix(), i, cumulativeStartTime, segInfo.Duration)
						r.audioManifest.AddSegment(i, segmentFile, segInfo.Duration, cumulativeStartTime)
						cumulativeStartTime += segInfo.Duration
					} else {
						log.Printf("[%s] segment %d: tfdt=%.3fs, duration=%.3fs (tfdt_samples=%d)",
							r.logPrefix(), i, startTime, segInfo.Duration, segInfo.BaseDecodeTime)
						r.audioManifest.AddSegment(i, segmentFile, segInfo.Duration, startTime)
						// Update cumulative for potential fallback
						cumulativeStartTime = startTime + segInfo.Duration
					}
					segmentsAdded++
				}
			}

			if err := r.audioManifest.Write(); err != nil {
				log.Printf("[%s] failed to write audio manifest: %v", r.logPrefix(), err)
			} else {
				log.Printf("[%s] wrote audio manifest with %d segments", r.logPrefix(), r.audioManifest.SegmentCount())
			}

			// Also write HLS playlist for standard player compatibility
			if err := r.audioManifest.WriteHLSPlaylist(); err != nil {
				log.Printf("[%s] failed to write audio HLS playlist: %v", r.logPrefix(), err)
			} else {
				log.Printf("[%s] wrote audio.m3u8 HLS playlist", r.logPrefix())
			}
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
// the recording files (e.g. video.m3u8, video*.ts or video*.m4s, audio.m3u8, audio*.m4s, audio.json).
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

	videoPlaylist := filepath.Join(r.outputDir, "video.m3u8")
	if _, err := os.Stat(videoPlaylist); err == nil {
		summary.OutputFile = videoPlaylist
	} else {
		summary.OutputFile = r.outputDir
	}

	sizeBytes, err := directorySizeBytes(r.outputDir)
	if err != nil {
		summary.Err = fmt.Errorf("failed to stat recording directory: %w", err)
		return summary
	}
	summary.SizeBytes = sizeBytes
	summary.Duration = time.Since(r.startTime)
	summary.VideoPackets = r.videoPacketCount
	summary.AudioPackets = r.audioPacketCount

	return summary
}

func directorySizeBytes(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// processAudioSegments processes all audio segments to create proper CMAF format.
// It extracts the init segment from the first audio segment (audio_init.mp4) and
// strips the duplicate init data from all segments, leaving only media data.
//
// This is necessary because GStreamer's splitmuxsink+cmafmux creates self-contained
// segments where each .m4s has ftyp+moov (init data) + moof+mdat (media). For proper
// HLS/CMAF playback, we need a separate init segment referenced by EXT-X-MAP.
func (r *ParticipantRecorder) processAudioSegments() error {
	initPath := filepath.Join(r.outputDir, "audio_init.mp4")
	segmentsProcessed := 0

	// Process all audio segments
	for i := 0; ; i++ {
		segmentName := fmt.Sprintf("audio%05d.m4s", i)
		segmentPath := filepath.Join(r.outputDir, segmentName)

		// Check if segment exists
		if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
			break // No more segments
		}

		// Process the segment
		mediaData, err := processFMP4Segment(segmentPath, i, initPath)
		if err != nil {
			log.Printf("[%s] failed to process %s: %v (keeping original)", r.logPrefix(), segmentName, err)
			continue
		}

		// Ensure styp prefix for CMAF compliance
		mediaData = ensureStypPrefix(mediaData, "opus")

		// Overwrite the segment file with processed data
		if err := os.WriteFile(segmentPath, mediaData, 0644); err != nil {
			log.Printf("[%s] failed to write processed %s: %v", r.logPrefix(), segmentName, err)
			continue
		}

		segmentsProcessed++
	}

	// Check if init was created
	if info, err := os.Stat(initPath); err == nil {
		log.Printf("[%s] audio segment processing complete: %d segments, init=%d bytes",
			r.logPrefix(), segmentsProcessed, info.Size())
	} else if segmentsProcessed > 0 {
		log.Printf("[%s] warning: processed %d segments but audio_init.mp4 was not created",
			r.logPrefix(), segmentsProcessed)
	}

	return nil
}

func (r *ParticipantRecorder) processAV1VideoSegments() error {
	initPath := filepath.Join(r.outputDir, "video_init.mp4")
	segmentsProcessed := 0

	for i := 0; ; i++ {
		segmentName := fmt.Sprintf("video%05d.m4s", i)
		segmentPath := filepath.Join(r.outputDir, segmentName)

		if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
			break
		}

		mediaData, err := processFMP4Segment(segmentPath, i, initPath)
		if err != nil {
			log.Printf("[%s] failed to process %s: %v (keeping original)", r.logPrefix(), segmentName, err)
			continue
		}

		mediaData = ensureStypPrefix(mediaData, "av01")

		if err := os.WriteFile(segmentPath, mediaData, 0644); err != nil {
			log.Printf("[%s] failed to write processed %s: %v", r.logPrefix(), segmentName, err)
			continue
		}

		segmentsProcessed++
	}

	if info, err := os.Stat(initPath); err == nil {
		log.Printf("[%s] video segment processing complete: %d segments, init=%d bytes",
			r.logPrefix(), segmentsProcessed, info.Size())
	} else if segmentsProcessed > 0 {
		log.Printf("[%s] warning: processed %d segments but video_init.mp4 was not created",
			r.logPrefix(), segmentsProcessed)
	}

	return nil
}

func (r *ParticipantRecorder) writeAV1VideoHLSPlaylist() error {
	initFile := "video_init.mp4"
	initPath := filepath.Join(r.outputDir, initFile)
	timescale, err := getMP4TrackTimescale(initPath)
	if err != nil {
		log.Printf("[%s] warning: failed to parse video init timescale: %v (falling back to 90000)", r.logPrefix(), err)
		timescale = 90000
	}

	type seg struct {
		file     string
		duration float64
	}

	var segments []seg
	for i := 0; ; i++ {
		segmentFile := fmt.Sprintf("video%05d.m4s", i)
		segmentPath := filepath.Join(r.outputDir, segmentFile)
		if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
			break
		}

		segInfo, err := getAudioSegmentInfo(segmentPath, timescale)
		if err != nil {
			log.Printf("[%s] warning: failed to parse video segment %d timing: %v (using target duration)", r.logPrefix(), i, err)
			segments = append(segments, seg{file: segmentFile, duration: float64(r.segmentDuration)})
			continue
		}
		segments = append(segments, seg{file: segmentFile, duration: segInfo.Duration})
	}

	if len(segments) == 0 {
		return fmt.Errorf("no video segments found")
	}

	maxDuration := 0.0
	for _, s := range segments {
		if s.duration > maxDuration {
			maxDuration = s.duration
		}
	}
	targetDuration := int(maxDuration) + 1

	var content string
	content += "#EXTM3U\n"
	content += "#EXT-X-VERSION:7\n"
	content += "#EXT-X-MEDIA-SEQUENCE:0\n"
	content += fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", targetDuration)
	content += "#EXT-X-INDEPENDENT-SEGMENTS\n"
	content += "\n"
	content += fmt.Sprintf("#EXT-X-MAP:URI=\"%s\"\n", initFile)
	content += "\n"

	for _, s := range segments {
		content += fmt.Sprintf("#EXTINF:%.3f,\n", s.duration)
		content += s.file + "\n"
	}

	content += "#EXT-X-ENDLIST\n"

	playlistPath := filepath.Join(r.outputDir, "video.m3u8")
	if err := os.WriteFile(playlistPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write video HLS playlist: %w", err)
	}
	return nil
}
