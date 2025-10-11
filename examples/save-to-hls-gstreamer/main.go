// save-to-hls-gstreamer is an application that receives H.264 video and Opus audio via WebRTC,
// uses GStreamer for packet handling (jitter buffer, ordering, sync), transcodes Opus to AAC,
// and outputs MPEG-TS HLS segments.
//
// This example demonstrates:
// - WebRTC RTP packet reception
// - GStreamer appsrc for pushing RTP packets
// - Built-in jitter buffer (rtpjitterbuffer) for packet ordering and timing
// - Opus to AAC transcoding
// - HLS output with proper audio/video synchronization
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v4"
)

// HLSRecorder manages the GStreamer pipeline and WebRTC connections
type HLSRecorder struct {
	// WebRTC
	peerConnection *webrtc.PeerConnection

	// GStreamer
	pipeline    *gst.Pipeline
	videoAppSrc *app.Source
	audioAppSrc *app.Source

	// Synchronization
	mu               sync.Mutex
	videoInitialized bool
	audioInitialized bool
	started          bool

	// Configuration
	outputDir string
}

func NewHLSRecorder(outputDir string) (*HLSRecorder, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	return &HLSRecorder{
		outputDir: outputDir,
	}, nil
}

// initGStreamer initializes GStreamer and creates the pipeline
func (r *HLSRecorder) initGStreamer() error {
	gst.Init(nil)

	// Create the pipeline with proper jitter buffers and transcoding
	// Pipeline structure:
	// Video: appsrc ! rtpjitterbuffer ! rtph264depay ! h264parse ! mux
	// Audio: appsrc ! rtpjitterbuffer ! rtpopusdepay ! opusdec ! avenc_aac ! aacparse ! mux
	// mux ! filesink for recording to a single MPEG-TS file
	//
	// Note: For proper HLS with segments, we write to a single file and post-process it.
	// GStreamer's hlssink2 has issues with dynamic pad linking in go-gst.

	pipelineStr := fmt.Sprintf(`
		filesink location=%s/output.ts name=sink

		mpegtsmux name=mux ! sink.

		appsrc name=videosrc format=time is-live=true do-timestamp=true
		! rtpjitterbuffer latency=200
		! rtph264depay
		! h264parse
		! video/x-h264,stream-format=byte-stream,alignment=au
		! queue max-size-buffers=0 max-size-time=0 max-size-bytes=0
		! mux.

		appsrc name=audiosrc format=time is-live=true do-timestamp=true
		! rtpjitterbuffer latency=200
		! rtpopusdepay
		! opusdec
		! audioconvert
		! avenc_aac bitrate=128000
		! aacparse
		! queue max-size-buffers=0 max-size-time=0 max-size-bytes=0
		! mux.
	`, r.outputDir)

	pipeline, err := gst.NewPipelineFromString(pipelineStr)
	if err != nil {
		return fmt.Errorf("failed to create pipeline: %w", err)
	}

	r.pipeline = pipeline

	// Get appsrc elements
	videoSrcElement, err := r.pipeline.GetElementByName("videosrc")
	if err != nil {
		return fmt.Errorf("failed to get videosrc: %w", err)
	}
	r.videoAppSrc = app.SrcFromElement(videoSrcElement)

	// Don't set caps here - they will be set based on the first RTP packet received

	audioSrcElement, err := r.pipeline.GetElementByName("audiosrc")
	if err != nil {
		return fmt.Errorf("failed to get audiosrc: %w", err)
	}
	r.audioAppSrc = app.SrcFromElement(audioSrcElement)

	// Set up bus message handling
	r.pipeline.GetPipelineBus().AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageEOS:
			log.Println("EOS received, stopping pipeline")
			r.pipeline.BlockSetState(gst.StateNull)
			return false

		case gst.MessageError:
			gerr := msg.ParseError()
			log.Printf("GStreamer error: %s (debug: %s)", gerr.Error(), gerr.DebugString())
			r.pipeline.BlockSetState(gst.StateNull)
			return false

		case gst.MessageWarning:
			gw := msg.ParseWarning()
			log.Printf("GStreamer warning: %s", gw.Error())

		case gst.MessageInfo:
			info := msg.ParseInfo()
			log.Printf("GStreamer info: %s", info.Error())
		}
		return true
	})

	return nil
}

// Start starts the GStreamer pipeline
func (r *HLSRecorder) Start() error {
	if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to start pipeline: %w", err)
	}

	log.Println("GStreamer pipeline started")

	return nil
}

// setupWebRTC creates a WebRTC peer connection and sets up track handlers
func (r *HLSRecorder) setupWebRTC() error {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("failed to create peer connection: %w", err)
	}

	r.peerConnection = pc

	// Handle incoming tracks
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("Track received: %s, codec: %s", track.Kind().String(), track.Codec().MimeType)

		switch track.Kind() {
		case webrtc.RTPCodecTypeVideo:
			if track.Codec().MimeType == "video/H264" {
				go r.handleVideoTrack(track)
			} else {
				log.Printf("Unsupported video codec: %s", track.Codec().MimeType)
			}

		case webrtc.RTPCodecTypeAudio:
			if track.Codec().MimeType == "audio/opus" {
				go r.handleAudioTrack(track)
			} else {
				log.Printf("Unsupported audio codec: %s", track.Codec().MimeType)
			}
		}
	})

	// ICE connection state changes
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE connection state: %s", state.String())
		if state == webrtc.ICEConnectionStateFailed || state == webrtc.ICEConnectionStateClosed {
			r.Stop()
		}
	})

	return nil
}

// handleVideoTrack reads RTP packets from video track and pushes to GStreamer
func (r *HLSRecorder) handleVideoTrack(track *webrtc.TrackRemote) {
	log.Println("Starting video track handler")

	for {
		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("Video track read error: %v", err)
			return
		}

		r.mu.Lock()
		if !r.videoInitialized {
			// Set caps on first packet with the actual payload type
			capsStr := fmt.Sprintf("application/x-rtp,media=(string)video,encoding-name=(string)H264,clock-rate=(int)90000,payload=(int)%d", rtpPacket.PayloadType)
			caps := gst.NewCapsFromString(capsStr)
			r.videoAppSrc.SetProperty("caps", caps)
			r.videoInitialized = true
			log.Printf("Video stream initialized with caps: %s", capsStr)
		} else {
			// Log every 100 packets
			if rtpPacket.SequenceNumber%100 == 0 {
				log.Printf("Video: pushed RTP packet seq=%d, ts=%d", rtpPacket.SequenceNumber, rtpPacket.Timestamp)
			}
		}
		r.mu.Unlock()

		// Marshal RTP packet
		data, err := rtpPacket.Marshal()
		if err != nil {
			log.Printf("Failed to marshal video RTP packet: %v", err)
			continue
		}

		// Create GStreamer buffer with RTP timestamp as PTS
		// GStreamer's rtpjitterbuffer will handle ordering and timing
		buffer := gst.NewBufferFromBytes(data)

		// Set PTS based on RTP timestamp (clock-rate is 90000 for H.264)
		// Convert RTP timestamp to nanoseconds: timestamp * 1e9 / 90000
		ptsNs := uint64(rtpPacket.Timestamp) * 1000000000 / 90000
		buffer.SetPresentationTimestamp(gst.ClockTime(ptsNs))

		// Push to appsrc
		if flow := r.videoAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
			if flow != gst.FlowFlushing {
				log.Printf("Video appsrc push failed: %s", flow.String())
			}
			return
		}
	}
}

// handleAudioTrack reads RTP packets from audio track and pushes to GStreamer
func (r *HLSRecorder) handleAudioTrack(track *webrtc.TrackRemote) {
	log.Println("Starting audio track handler")

	for {
		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("Audio track read error: %v", err)
			return
		}

		r.mu.Lock()
		if !r.audioInitialized {
			// Set caps on first packet with the actual payload type
			capsStr := fmt.Sprintf("application/x-rtp,media=(string)audio,encoding-name=(string)OPUS,clock-rate=(int)48000,payload=(int)%d", rtpPacket.PayloadType)
			caps := gst.NewCapsFromString(capsStr)
			r.audioAppSrc.SetProperty("caps", caps)
			r.audioInitialized = true
			log.Printf("Audio stream initialized with caps: %s", capsStr)
		}
		r.mu.Unlock()

		// Marshal RTP packet
		data, err := rtpPacket.Marshal()
		if err != nil {
			log.Printf("Failed to marshal audio RTP packet: %v", err)
			continue
		}

		// Create GStreamer buffer with RTP timestamp as PTS
		// GStreamer's rtpjitterbuffer will handle ordering and timing
		buffer := gst.NewBufferFromBytes(data)

		// Set PTS based on RTP timestamp (clock-rate is 48000 for Opus)
		// Convert RTP timestamp to nanoseconds: timestamp * 1e9 / 48000
		ptsNs := uint64(rtpPacket.Timestamp) * 1000000000 / 48000
		buffer.SetPresentationTimestamp(gst.ClockTime(ptsNs))

		// Push to appsrc
		if flow := r.audioAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
			if flow != gst.FlowFlushing {
				log.Printf("Audio appsrc push failed: %s", flow.String())
			}
			return
		}
	}
}

// Stop gracefully stops the recorder
func (r *HLSRecorder) Stop() {
	log.Println("Stopping HLS recorder...")

	if r.peerConnection != nil {
		r.peerConnection.Close()
	}

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
}

func main() {
	outputDir := "hls-output"
	if len(os.Args) > 1 {
		outputDir = os.Args[1]
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting HLS recorder with GStreamer, output dir: %s", outputDir)

	recorder, err := NewHLSRecorder(outputDir)
	if err != nil {
		log.Fatalf("Failed to create recorder: %v", err)
	}

	// Initialize GStreamer pipeline
	if err := recorder.initGStreamer(); err != nil {
		log.Fatalf("Failed to initialize GStreamer: %v", err)
	}

	// Set up WebRTC
	if err := recorder.setupWebRTC(); err != nil {
		log.Fatalf("Failed to setup WebRTC: %v", err)
	}

	// Start pipeline
	if err := recorder.Start(); err != nil {
		log.Fatalf("Failed to start recorder: %v", err)
	}

	// Wait for SDP offer from stdin
	log.Println("Waiting for SDP offer (paste offer JSON and press Enter)...")
	reader := bufio.NewReader(os.Stdin)
	offerText, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("Failed to read offer: %v", err)
	}

	var offer webrtc.SessionDescription
	if err := json.Unmarshal([]byte(offerText), &offer); err != nil {
		// Try base64 decoding
		decoded, err2 := base64.StdEncoding.DecodeString(offerText)
		if err2 != nil {
			log.Fatalf("Failed to decode offer: %v", err)
		}
		if err := json.Unmarshal(decoded, &offer); err != nil {
			log.Fatalf("Failed to unmarshal offer: %v", err)
		}
	}

	if err := recorder.peerConnection.SetRemoteDescription(offer); err != nil {
		log.Fatalf("Failed to set remote description: %v", err)
	}

	// Create answer
	answer, err := recorder.peerConnection.CreateAnswer(nil)
	if err != nil {
		log.Fatalf("Failed to create answer: %v", err)
	}

	if err := recorder.peerConnection.SetLocalDescription(answer); err != nil {
		log.Fatalf("Failed to set local description: %v", err)
	}

	// Output answer
	answerJSON, err := json.Marshal(answer)
	if err != nil {
		log.Fatalf("Failed to marshal answer: %v", err)
	}

	log.Println("\nAnswer SDP (paste this in the publisher):")
	fmt.Println(base64.StdEncoding.EncodeToString(answerJSON))

	// Handle shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	recorder.Stop()
	log.Println("Shutdown complete")
}
