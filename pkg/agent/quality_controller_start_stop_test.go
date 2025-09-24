package agent

import (
	"github.com/livekit/protocol/livekit"
	"github.com/pion/webrtc/v4"
	"testing"
)

func TestQualityController_StartStopMonitoring(t *testing.T) {
	qc := NewQualityController()
	var fakeTrack webrtc.TrackRemote
	sub := &PublisherTrackSubscription{CurrentQuality: livekit.VideoQuality_LOW}

	qc.StartMonitoring(&fakeTrack, sub)
	qc.StopMonitoring(&fakeTrack)
}
