package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestTrackQualityMonitor_HelperDecisions(t *testing.T) {
	policy := DefaultQualityAdaptationPolicy()
	mon := &TrackQualityMonitor{
		Stats:        &TrackQualityStats{CurrentQuality: livekit.VideoQuality_MEDIUM},
		Subscription: &PublisherTrackSubscription{LastQualityChange: time.Now().Add(-time.Hour)},
	}

	// shouldDecreaseQuality
	mon.Stats.FreezeCount = 3
	assert.True(t, mon.shouldDecreaseQuality(policy))
	mon.Stats.FreezeCount = 0
	mon.Stats.Bitrate = mon.getTargetBitrate() + 1 // over threshold
	policy.BitrateThresholdDown = 0.95
	assert.True(t, mon.shouldDecreaseQuality(policy))
	mon.Stats.Bitrate = 0
	mon.Stats.Jitter = 60
	assert.True(t, mon.shouldDecreaseQuality(policy))

	// shouldIncreaseQuality
	mon.Stats = &TrackQualityStats{CurrentQuality: livekit.VideoQuality_LOW}
	mon.Subscription.LastQualityChange = time.Now().Add(-policy.StableWindowUp - time.Second)
	assert.True(t, mon.shouldIncreaseQuality(policy, 0.0))
	// Disqualify by loss
	assert.False(t, mon.shouldIncreaseQuality(policy, policy.LossThresholdDown+0.1))
}

func TestQualityController_UpdatePathsNoChange(t *testing.T) {
	qc := NewQualityController()
	mon := &TrackQualityMonitor{
		Stats: &TrackQualityStats{
			CurrentQuality:  livekit.VideoQuality_MEDIUM,
			PacketsReceived: 100,
			PacketsLost:     0,
			Bitrate:         0,
			RTT:             0,
			FreezeCount:     0,
		},
		Subscription:      &PublisherTrackSubscription{LastQualityChange: time.Now().Add(-time.Hour)},
		AdaptationEnabled: true,
	}

	// Place monitor into controller and update all
	qc.mu.Lock()
	qc.monitors["t1"] = mon
	qc.mu.Unlock()
	qc.updateAllMonitors()

	// Call updateTrackQuality directly (no change expected)
	qc.updateTrackQuality(mon, qc.adaptationPolicy)

	// getTargetBitrate sanity
	mon.Stats.CurrentQuality = livekit.VideoQuality_HIGH
	assert.Equal(t, uint64(2_500_000), mon.getTargetBitrate())
	mon.Stats.CurrentQuality = livekit.VideoQuality_LOW
	assert.Equal(t, uint64(500_000), mon.getTargetBitrate())
}
