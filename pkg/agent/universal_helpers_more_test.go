package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestUniversalWorker_UnsubscribeAndTrackStats(t *testing.T) {
	w := &UniversalWorker{
		subscribedTracks: make(map[string]*PublisherTrackSubscription),
	}

	// Unsubscribe missing
	assert.Error(t, w.UnsubscribeTrack("missing"))

	// Existing without publication
	w.subscribedTracks["ts"] = &PublisherTrackSubscription{Enabled: true}
	assert.NoError(t, w.UnsubscribeTrack("ts"))

	// Stats missing
	_, err := w.GetTrackStats("none")
	assert.Error(t, err)

	// Stats present with dimensions
	w.subscribedTracks["ts2"] = &PublisherTrackSubscription{
		CurrentQuality: livekit.VideoQuality_MEDIUM,
		Enabled:        true,
		Dimensions:     &VideoDimensions{Width: 640, Height: 480},
		FrameRate:      30.0,
	}
	stats, err := w.GetTrackStats("ts2")
	assert.NoError(t, err)
	assert.Equal(t, uint32(640), stats["width"])
	assert.Equal(t, uint32(480), stats["height"])
	assert.Equal(t, float32(30.0), stats["frame_rate"])
}
