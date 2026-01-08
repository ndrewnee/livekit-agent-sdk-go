package main

import (
	"fmt"
	"sync"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type bindablePionSampleTrack struct {
	*webrtc.TrackLocalStaticSample

	mu     sync.Mutex
	onBind func()
}

func newBindablePionSampleTrack(codec webrtc.RTPCodecCapability) (*bindablePionSampleTrack, error) {
	trackID := fmt.Sprintf("TR_%d", time.Now().UnixNano())
	streamID := fmt.Sprintf("ST_%d", time.Now().UnixNano())

	track, err := webrtc.NewTrackLocalStaticSample(codec, trackID, streamID)
	if err != nil {
		return nil, err
	}
	return &bindablePionSampleTrack{TrackLocalStaticSample: track}, nil
}

func (t *bindablePionSampleTrack) OnBind(cb func()) {
	t.mu.Lock()
	t.onBind = cb
	t.mu.Unlock()
}

func (t *bindablePionSampleTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	codec, err := t.TrackLocalStaticSample.Bind(ctx)
	if err != nil {
		return codec, err
	}

	t.mu.Lock()
	cb := t.onBind
	t.mu.Unlock()
	if cb != nil {
		cb()
	}
	return codec, nil
}

func (t *bindablePionSampleTrack) WriteSample(sample media.Sample, _ *lksdk.SampleWriteOptions) error {
	return t.TrackLocalStaticSample.WriteSample(sample)
}
