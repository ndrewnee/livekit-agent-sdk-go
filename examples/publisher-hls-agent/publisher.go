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
type GStreamerPublisher struct {
	pipeline   *gst.Pipeline
	videoTrack *lksdk.LocalTrack
	audioTrack *lksdk.LocalTrack
	mu         sync.Mutex
	stopped    bool
}

func NewGStreamerPublisher(filePath string, videoTrack, audioTrack *lksdk.LocalTrack) (*GStreamerPublisher, error) {
	gst.Init(nil)

	p := &GStreamerPublisher{
		videoTrack: videoTrack,
		audioTrack: audioTrack,
	}

	pipelineStr := fmt.Sprintf(`
		filesrc location="%s" ! qtdemux name=demux
		demux.video_0 ! queue ! h264parse ! video/x-h264,stream-format=byte-stream,alignment=au ! appsink name=videosink emit-signals=true
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

				if isKeyFrame && len(data) > 0 {
					preview := len(data)
					if preview > 16 {
						preview = 16
					}
					log.Printf("keyframe sample first bytes: % x", data[:preview])
				}

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
			}
			return gst.FlowOK
		},
	})

	return p, nil
}

func (p *GStreamerPublisher) Start() error {
	return p.pipeline.SetState(gst.StatePlaying)
}

func (p *GStreamerPublisher) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.pipeline.SetState(gst.StateNull)
}

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
