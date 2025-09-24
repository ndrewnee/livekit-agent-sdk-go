package agent

import (
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
)

// Minimal fake TrackRemote with an ID only via zero struct; we only use ID() and Kind()
// We construct via a real webrtc.TrackRemote pointer is non-trivial; instead, test error on nil

func TestMediaPipeline_StartProcessingTrack_Errors(t *testing.T) {
	p := NewMediaPipeline()

	// Nil track should error
	assert.Error(t, p.StartProcessingTrack(nil))

	// Use a fake remote track by creating a TrackLocalStaticRTP and wrapping? Not feasible here;
	// Instead, ensure StopProcessingTrack with unknown ID is no-op
	p.StopProcessingTrack("non-existent")

	// startMediaReceiver switch cover: create a dummy TrackRemote using zero value and set Kind via assumption default=audio
	// This is not possible without full WebRTC stack; skip deeper branch here.
	var _ webrtc.TrackRemote // silence import if unused in other tests
}
