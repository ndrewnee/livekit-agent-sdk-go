package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestGetTargetBitrate_MediumAndDefault(t *testing.T) {
	mon := &TrackQualityMonitor{Stats: &TrackQualityStats{}}

	mon.Stats.CurrentQuality = livekit.VideoQuality_MEDIUM
	assert.Equal(t, uint64(1_000_000), mon.getTargetBitrate())

	// Default/fallback
	mon.Stats.CurrentQuality = livekit.VideoQuality_OFF
	assert.Equal(t, uint64(1_000_000), mon.getTargetBitrate())
}
