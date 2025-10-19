package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
)

// Covers monitorLoop and updateAllMonitors without requiring a real WebRTC track
func TestQualityController_MonitorLoop_NoChange(t *testing.T) {
	qc := NewQualityController()
	qc.SetUpdateInterval(20 * time.Millisecond)

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
	qc.mu.Lock()
	qc.monitors["t1"] = mon
	qc.mu.Unlock()

	// Run monitor loop briefly
	done := make(chan struct{})
	qc.wg.Add(1)
	go func() {
		qc.monitorLoop()
		close(done)
	}()
	// Let it tick a couple of times
	time.Sleep(50 * time.Millisecond)
	// Stop loop
	close(qc.stopCh)
	<-done
}
