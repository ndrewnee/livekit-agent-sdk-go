package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestShouldIncreaseQuality_AdditionalBranches(t *testing.T) {
	policy := DefaultQualityAdaptationPolicy()
	mon := &TrackQualityMonitor{
		Stats:        &TrackQualityStats{CurrentQuality: livekit.VideoQuality_MEDIUM},
		Subscription: &PublisherTrackSubscription{LastQualityChange: time.Now()}, // within stable window
	}
	// Within stable window → false
	assert.False(t, mon.shouldIncreaseQuality(policy, 0.0))

	// Satisfy stable window and block on bandwidth headroom
	mon.Subscription.LastQualityChange = time.Now().Add(-policy.StableWindowUp - time.Second)
	mon.Stats.RTT = 0
	mon.Stats.FreezeCount = 0
	policy.BitrateThresholdUp = 0.5
	// Set bitrate above threshold (target for MEDIUM is 1_000_000)
	mon.Stats.Bitrate = uint64(float64(mon.getTargetBitrate())*policy.BitrateThresholdUp) + 1
	assert.False(t, mon.shouldIncreaseQuality(policy, 0.0))
}
