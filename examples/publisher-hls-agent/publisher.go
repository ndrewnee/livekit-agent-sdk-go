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

// GStreamerPublisher streams an MP4 file into LiveKit local tracks.
//
// This publisher reads an MP4 file containing H.264 video and Opus audio,
// demuxes it with qtdemux, parses the streams, and writes samples to LiveKit
// local tracks for transmission. It's primarily used for testing the HLS recorder
// with known media content.
//
// The GStreamer pipeline:
//
//	filesrc → qtdemux → video: h264parse → appsink (H.264 byte-stream)
//	                  → audio: opusparse → appsink (Opus)
//
// Video samples are tagged with keyframe metadata to help downstream recorders
// identify I-frames for proper HLS segmentation.
type GStreamerPublisher struct {
	pipeline   *gst.Pipeline
	videoTrack *lksdk.LocalTrack
	audioTrack *lksdk.LocalTrack
	mu         sync.Mutex
	stopped    bool
	videoTotal time.Duration
	audioTotal time.Duration
}

// NewGStreamerPublisher creates a new GStreamer-based publisher for the given MP4 file.
//
// The publisher demuxes the MP4 file and configures appsinks to pull H.264 video
// and Opus audio samples, which are then written to the provided LiveKit local tracks.
//
// Parameters:
//   - filePath: Path to MP4 file containing H.264 video and Opus audio
//   - videoTrack: LiveKit local video track to publish H.264 samples
//   - audioTrack: LiveKit local audio track to publish Opus samples
//
// Returns a configured GStreamerPublisher ready to Start(), or an error if
// pipeline creation fails.
func NewGStreamerPublisher(filePath string, videoTrack, audioTrack *lksdk.LocalTrack) (*GStreamerPublisher, error) {
	gst.Init(nil)

	p := &GStreamerPublisher{
		videoTrack: videoTrack,
		audioTrack: audioTrack,
	}

	pipelineStr := fmt.Sprintf(`
		filesrc location="%s" ! qtdemux name=demux
	demux.video_0 ! queue ! h264parse config-interval=1 ! video/x-h264,stream-format=byte-stream,alignment=au ! appsink name=videosink emit-signals=true
		demux.audio_0 ! queue ! opusparse ! audio/x-opus ! appsink name=audiosink emit-signals=true
	`, filePath)

	pipeline, err := gst.NewPipelineFromString(pipelineStr)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher pipeline: %w", err)
	}
	p.pipeline = pipeline

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

			data := buffer.Map(gst.MapRead).Bytes()
			defer buffer.Unmap()

			duration := time.Duration(buffer.Duration())

			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()

			if !stopped && p.videoTrack != nil {
				flags := buffer.GetFlags()
				isKeyFrame := (flags & gst.BufferFlagDeltaUnit) == 0

				sample := media.Sample{
					Data:     append([]byte{}, data...),
					Duration: duration,
				}
				if isKeyFrame {
					sample.Metadata = map[string]any{"keyframe": true}
				}

				if err := p.videoTrack.WriteSample(sample, nil); err != nil {
					log.Printf("failed to write video sample: %v", err)
					return gst.FlowError
				}
				p.mu.Lock()
				p.videoTotal += duration
				p.mu.Unlock()
			}
			return gst.FlowOK
		},
	})

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

			data := buffer.Map(gst.MapRead).Bytes()
			defer buffer.Unmap()

			duration := time.Duration(buffer.Duration())

			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()

			if !stopped && p.audioTrack != nil {
				if err := p.audioTrack.WriteSample(media.Sample{
					Data:     append([]byte{}, data...),
					Duration: duration,
				}, nil); err != nil {
					log.Printf("failed to write audio sample: %v", err)
					return gst.FlowError
				}
				p.mu.Lock()
				p.audioTotal += duration
				p.mu.Unlock()
			}
			return gst.FlowOK
		},
	})

	return p, nil
}

// Start begins playing the MP4 file and streaming samples to LiveKit tracks.
// The GStreamer pipeline transitions to the PLAYING state.
func (p *GStreamerPublisher) Start() error {
	return p.pipeline.SetState(gst.StatePlaying)
}

// Stop halts the publisher and stops writing samples to LiveKit tracks.
// The pipeline transitions to the NULL state. Total video and audio
// durations are logged.
func (p *GStreamerPublisher) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	_ = p.pipeline.SetState(gst.StateNull)
	p.mu.Lock()
	log.Printf("publisher totals: video=%.3fs audio=%.3fs", p.videoTotal.Seconds(), p.audioTotal.Seconds())
	p.mu.Unlock()
}

// Restart seeks the pipeline back to the beginning and resumes playback.
// This allows re-publishing the same MP4 file without recreating the pipeline.
// Video and audio duration counters are reset to zero.
//
// Returns an error if the pipeline cannot be paused, seeked, or resumed.
func (p *GStreamerPublisher) Restart() error {
	// Check pipeline exists (with lock)
	p.mu.Lock()
	pipeline := p.pipeline
	p.mu.Unlock()

	if pipeline == nil {
		return fmt.Errorf("pipeline not initialized")
	}

	// Pause, seek, and resume pipeline WITHOUT holding lock
	// (GStreamer callbacks may still be running and need to acquire the lock)
	if err := pipeline.SetState(gst.StatePaused); err != nil {
		return fmt.Errorf("failed to pause pipeline: %w", err)
	}

	// Seek to beginning
	flags := gst.SeekFlagFlush | gst.SeekFlagKeyUnit
	if ok := pipeline.SeekSimple(0, gst.FormatTime, flags); !ok {
		return fmt.Errorf("failed to seek pipeline to start")
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to resume pipeline: %w", err)
	}

	// Reset counters (with lock)
	p.mu.Lock()
	p.videoTotal = 0
	p.audioTotal = 0
	p.mu.Unlock()

	return nil
}

// Wait blocks until the pipeline reaches EOS (end of stream) or encounters an error.
// This is useful for synchronizing with the completion of file playback.
//
// Returns nil on EOS, or the pipeline error if one occurs.
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
