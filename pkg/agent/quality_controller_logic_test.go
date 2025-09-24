package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestTrackQualityMonitor_DecreaseAndIncreaseChecks(t *testing.T) {
	sub := &PublisherTrackSubscription{CurrentQuality: livekit.VideoQuality_MEDIUM, LastQualityChange: time.Now().Add(-time.Hour)}
	m := &TrackQualityMonitor{
		Subscription: sub,
		Stats: &TrackQualityStats{
			CurrentQuality: livekit.VideoQuality_MEDIUM,
			Bitrate:        100_000,
			Jitter:         10,
			RTT:            50,
			FreezeCount:    0,
		},
	}
	policy := DefaultQualityAdaptationPolicy()

	// No decrease when conditions fine
	assert.False(t, m.shouldDecreaseQuality(policy))

	// Decrease triggered by jitter
	m.Stats.Jitter = 60
	assert.True(t, m.shouldDecreaseQuality(policy))
	m.Stats.Jitter = 0

	// Decrease triggered by freeze count
	m.Stats.FreezeCount = 3
	assert.True(t, m.shouldDecreaseQuality(policy))
	m.Stats.FreezeCount = 0

	// Increase checks
	lossRatio := 0.0
	// Stable window satisfied by LastQualityChange in past
	m.Stats.Bitrate = 100_000 // well below headroom
	m.Stats.RTT = 10
	assert.True(t, m.shouldIncreaseQuality(policy, lossRatio))

	// Blocked by loss ratio
	assert.False(t, m.shouldIncreaseQuality(policy, policy.LossThresholdDown+0.1))

	// Blocked by RTT
	m.Stats.RTT = policy.RTTThresholdLow + 10
	assert.False(t, m.shouldIncreaseQuality(policy, 0.0))
}
