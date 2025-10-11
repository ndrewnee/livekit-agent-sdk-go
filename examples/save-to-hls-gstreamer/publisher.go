package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// GStreamerPublisher uses GStreamer to read a video file and stream it to WebRTC tracks
type GStreamerPublisher struct {
	pipeline   *gst.Pipeline
	videoTrack *webrtc.TrackLocalStaticSample
	audioTrack *webrtc.TrackLocalStaticSample
	mu         sync.Mutex
	stopped    bool
}

// NewGStreamerPublisher creates a new publisher that streams from a file to WebRTC tracks
func NewGStreamerPublisher(filePath string, videoTrack, audioTrack *webrtc.TrackLocalStaticSample) (*GStreamerPublisher, error) {
	gst.Init(nil)

	p := &GStreamerPublisher{
		videoTrack: videoTrack,
		audioTrack: audioTrack,
	}

	// Build pipeline:
	// filesrc ! qtdemux ! h264parse ! appsink (video)
	//                 ! opusparse ! appsink (audio)
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

	app.SinkFromElement(videoSink).SetCallbacks(&app.SinkCallbacks{
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

			if !stopped && p.videoTrack != nil {
				if err := p.videoTrack.WriteSample(media.Sample{
					Data:     append([]byte{}, data...), // Copy data
					Duration: duration,
				}); err != nil {
					log.Printf("Failed to write video sample: %v", err)
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
				}); err != nil {
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
