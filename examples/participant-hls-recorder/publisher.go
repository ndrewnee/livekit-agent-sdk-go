package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4/pkg/media"
)

// GStreamerPublisher uses GStreamer to read a video file and stream it to WebRTC tracks
type GStreamerPublisher struct {
	pipeline   *gst.Pipeline
	videoTrack *lksdk.LocalTrack
	audioTrack *lksdk.LocalTrack
	startTime  time.Time
	mu         sync.Mutex
	stopped    bool
}

// NewGStreamerPublisher creates a new publisher that streams from a file to WebRTC tracks
func NewGStreamerPublisher(filePath string, videoTrack, audioTrack *lksdk.LocalTrack) (*GStreamerPublisher, error) {
	gst.Init(nil)

	p := &GStreamerPublisher{
		videoTrack: videoTrack,
		audioTrack: audioTrack,
		startTime:  time.Now(),
	}

	// Build pipeline - exactly like save-to-hls-gstreamer example
	// filesrc ! qtdemux ! h264parse ! appsink (video)
	//                 ! opusparse ! appsink (audio)
	// Use byte-stream format (Annex-B) for H.264 with AU (access unit) alignment
	pipelineStr := fmt.Sprintf(`
		filesrc location="%s" ! qtdemux name=demux
		demux.video_0 ! queue ! h264parse ! video/x-h264,stream-format=byte-stream,alignment=au ! appsink name=videosink emit-signals=true
		demux.audio_0 ! queue ! opusparse ! audio/x-opus ! appsink name=audiosink emit-signals=true
	`, filePath)

	pipeline, err := gst.NewPipelineFromString(pipelineStr)
	if err != nil {
		return nil, fmt.Errorf("failed to create pipeline: %w", err)
	}

	p.pipeline = pipeline

	// Set up video appsink
	videoSink, err := pipeline.GetElementByName("videosink")
	if err != nil {
		return nil, fmt.Errorf("failed to get video sink: %w", err)
	}

	videoSampleCount := 0
	app.SinkFromElement(videoSink).SetCallbacks(&app.SinkCallbacks{
		NewSampleFunc: func(sink *app.Sink) gst.FlowReturn {
			sample := sink.PullSample()
			if sample == nil {
				log.Printf("Video: EOS received")
				return gst.FlowEOS
			}

			buffer := sample.GetBuffer()
			if buffer == nil {
				log.Printf("Video: buffer is nil")
				return gst.FlowError
			}

			// Get buffer data (H.264 access unit with Annex-B format)
			data := buffer.Map(gst.MapRead).Bytes()
			defer buffer.Unmap()

			// Get duration from buffer
			duration := time.Duration(buffer.Duration())

			videoSampleCount++
			if videoSampleCount%30 == 1 {
				log.Printf("Video: extracted sample %d (size=%d, duration=%v, first_bytes=%02x %02x %02x %02x)",
					videoSampleCount, len(data), duration,
					data[0], data[1], data[2], data[3])
			}

			// Write complete access unit to WebRTC track (with Annex-B start codes)
			// This is the same approach as save-to-hls-gstreamer example
			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()

			if !stopped && p.videoTrack != nil {
				if err := p.videoTrack.WriteSample(media.Sample{
					Data:      append([]byte{}, data...), // Copy complete access unit with start codes
					Duration:  duration,
					Timestamp: time.Now(),
				}, nil); err != nil {
					log.Printf("Failed to write video sample %d: %v", videoSampleCount, err)
					return gst.FlowError
				}
			}

			return gst.FlowOK
		},
	})

	// Set up audio appsink
	audioSink, err := pipeline.GetElementByName("audiosink")
	if err != nil {
		return nil, fmt.Errorf("failed to get audio sink: %w", err)
	}

	app.SinkFromElement(audioSink).SetCallbacks(&app.SinkCallbacks{
		NewSampleFunc: func(sink *app.Sink) gst.FlowReturn {
			sample := sink.PullSample()
			if sample == nil {
				return gst.FlowEOS
			}

			buffer := sample.GetBuffer()
			if buffer == nil {
				return gst.FlowError
			}

			// Get buffer data
			data := buffer.Map(gst.MapRead).Bytes()
			defer buffer.Unmap()

			// Get duration from buffer (convert ClockTime to time.Duration)
			duration := time.Duration(buffer.Duration())

			// Write to WebRTC track
			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()

			if !stopped && p.audioTrack != nil {
				if err := p.audioTrack.WriteSample(media.Sample{
					Data:     append([]byte{}, data...), // Copy data
					Duration: duration,
				}, nil); err != nil {
					log.Printf("Failed to write audio sample: %v", err)
					return gst.FlowError
				}
			}

			return gst.FlowOK
		},
	})

	return p, nil
}

// Start starts the pipeline
func (p *GStreamerPublisher) Start() error {
	return p.pipeline.SetState(gst.StatePlaying)
}

// Stop stops the pipeline
func (p *GStreamerPublisher) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()

	p.pipeline.SetState(gst.StateNull)
}

// Wait waits for the pipeline to finish (EOS)
func (p *GStreamerPublisher) Wait() error {
	bus := p.pipeline.GetBus()
	for {
		msg := bus.TimedPop(gst.ClockTimeNone)
		if msg == nil {
			break
		}

		switch msg.Type() {
		case gst.MessageEOS:
			return nil
		case gst.MessageError:
			return msg.ParseError()
		}
	}
	return nil
}
