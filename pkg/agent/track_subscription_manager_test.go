package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

func TestTrackSubscriptionManager_Basics(t *testing.T) {
	tm := NewTrackSubscriptionManager()

	// Toggle flags
	tm.SetAutoSubscribe(false)
	tm.SetSubscribeAudio(false)
	tm.SetSubscribeVideo(true)
	tm.SetSourcePriority(livekit.TrackSource_SCREEN_SHARE, 42)

	// Add & clear filters
	called := 0
	tm.AddFilter(func(pub *lksdk.RemoteTrackPublication) bool { called++; return true })
	tm.ClearFilters()

	// ShouldAutoSubscribe true with defaults and a zero-valued publication
	tm.SetAutoSubscribe(true)
	pub := &lksdk.RemoteTrackPublication{}
	ok := tm.ShouldAutoSubscribe(pub)
	assert.True(t, ok)

	// With a filter that blocks
	tm.AddFilter(func(pub *lksdk.RemoteTrackPublication) bool { return false })
	assert.False(t, tm.ShouldAutoSubscribe(pub))

	// Priority fetch
	assert.Equal(t, 42, tm.GetSourcePriority(livekit.TrackSource_SCREEN_SHARE))
	// Default for UNKNOWN is 50 per manager defaults
	assert.Equal(t, 50, tm.GetSourcePriority(livekit.TrackSource_UNKNOWN))
	_ = called // ensure referenced
}

func TestConnectionQualityMonitor_Flow(t *testing.T) {
	cm := NewConnectionQualityMonitor()

	// Start with a zero-valued participant (Identity() is empty but safe)
	rp := &lksdk.RemoteParticipant{}
	cm.StartMonitoring(rp)
	defer cm.StopMonitoring()

	// Register callback and update quality
	changed := false
	cm.SetQualityChangeCallback(func(oldQ, newQ livekit.ConnectionQuality) { changed = true })
	cm.UpdateQuality(livekit.ConnectionQuality_GOOD)
	cm.UpdateQuality(livekit.ConnectionQuality_POOR)

	// Read current and history
	q := cm.GetCurrentQuality()
	hist := cm.GetQualityHistory()
	assert.GreaterOrEqual(t, len(hist), 1)
	assert.True(t, q == livekit.ConnectionQuality_GOOD || q == livekit.ConnectionQuality_POOR)

	// Average over recent interval
	avg := cm.GetAverageQuality(1 * time.Minute)
	assert.True(t, int(avg) >= int(livekit.ConnectionQuality_POOR) && int(avg) <= int(livekit.ConnectionQuality_EXCELLENT))

	// Stability check
	_ = cm.IsStable(1 * time.Second)

	// Callback should have fired at least once when quality changed
	assert.True(t, changed)
}
