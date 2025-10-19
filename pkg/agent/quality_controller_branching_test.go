package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/pion/webrtc/v4"
)

// Drives updateTrackQuality branches without requiring a real TrackRemote.
func TestQualityController_UpdateTrackQuality_Branches(t *testing.T) {
	qc := NewQualityController()
	policy := DefaultQualityAdaptationPolicy()

	m := &TrackQualityMonitor{
		Subscription:      &PublisherTrackSubscription{CurrentQuality: livekit.VideoQuality_MEDIUM, LastQualityChange: time.Now().Add(-time.Hour)},
		Stats:             &TrackQualityStats{CurrentQuality: livekit.VideoQuality_MEDIUM},
		AdaptationEnabled: true,
	}

	// Case 1: High loss triggers decrease path. Track is nil → ApplyQualitySettings errors, but branch executes.
	m.Stats.PacketsReceived = 100
	m.Stats.PacketsLost = 10 // 10% > LossThresholdUp
	qc.updateTrackQuality(m, policy)

	// Case 2: High RTT triggers alternate decrease path
	m.Stats.PacketsLost = 0
	m.Stats.RTT = policy.RTTThresholdHigh + 10
	qc.updateTrackQuality(m, policy)

	// Case 3: shouldDecreaseQuality via bitrate/jitter/freeze
	m.Stats.RTT = 0
	m.Stats.Jitter = 60 // jitter triggers decrease
	qc.updateTrackQuality(m, policy)

	// Case 4: shouldIncreaseQuality path (stable, low loss, low RTT, headroom)
	m.Stats.Jitter = 0
	m.Stats.Bitrate = 100_000
	m.Stats.RTT = policy.RTTThresholdLow - 10
	// Ensure stable window satisfied by LastQualityChange
	qc.updateTrackQuality(m, policy)
}

// Attempt record-change path by providing a zero-value TrackRemote.
func TestQualityController_UpdateTrackQuality_RecordsChange(t *testing.T) {
	qc := NewQualityController()
	policy := DefaultQualityAdaptationPolicy()

	// zero-value TrackRemote to avoid nil; methods like ID() should return defaults
	var fakeTrack webrtc.TrackRemote
	m := &TrackQualityMonitor{
		Track:             &fakeTrack,
		Subscription:      &PublisherTrackSubscription{CurrentQuality: livekit.VideoQuality_HIGH, LastQualityChange: time.Now().Add(-time.Hour)},
		Stats:             &TrackQualityStats{CurrentQuality: livekit.VideoQuality_HIGH},
		AdaptationEnabled: true,
	}

	// Force decrease condition (high loss)
	m.Stats.PacketsReceived = 100
	m.Stats.PacketsLost = 50
	// Should not panic and should attempt to apply; even if ApplyQualitySettings logs, function runs
	qc.updateTrackQuality(m, policy)
}
