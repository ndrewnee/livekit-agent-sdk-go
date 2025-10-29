package agent

import (
	"testing"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

func TestUniversalWorker_SimpleAccessorsAndErrors(t *testing.T) {
	w := &UniversalWorker{
		activeJobs:          make(map[string]*JobContext),
		subscribedTracks:    make(map[string]*PublisherTrackSubscription),
		participantTrackers: make(map[string]*ParticipantTracker),
	}

	// Getters return zero values
	assert.Nil(t, w.GetPermissionManager())
	assert.Nil(t, w.GetCoordinationManager())
	assert.Nil(t, w.GetEventProcessor())

	// getJobContext not found
	_, ok := w.getJobContext("missing")
	assert.False(t, ok)

	// getParticipantTracker error paths
	_, err := w.getParticipantTracker("nojob")
	assert.Error(t, err)

	w.activeJobs["j1"] = &JobContext{Room: nil}
	_, err = w.getParticipantTracker("j1")
	assert.Error(t, err)

	// With empty room name and existing tracker
	w.activeJobs["j2"] = &JobContext{Room: &lksdk.Room{}}
	w.participantTrackers[""] = &ParticipantTracker{}
	tr, err := w.getParticipantTracker("j2")
	assert.NoError(t, err)
	assert.NotNil(t, tr)

	// RequestPermissionChange error paths
	err = w.RequestPermissionChange("nojob", "id", nil)
	assert.Error(t, err)
	err = w.RequestPermissionChange("j1", "id", nil)
	assert.Error(t, err)

	// SetTrackQuality without publication (should not call SDK)
	w.subscribedTracks["tsid"] = &PublisherTrackSubscription{}
	assert.NoError(t, w.SetTrackQuality("tsid", 0))

	// SetTrackDimensions & SetTrackFrameRate
	assert.NoError(t, w.SetTrackDimensions("tsid", 640, 480))
	assert.NoError(t, w.SetTrackFrameRate("tsid", 30.0))

	// GetSubscribedTracks returns a copy
	sts := w.GetSubscribedTracks()
	assert.Contains(t, sts, "tsid")

	// GetPublisherInfo with no rooms
	assert.Nil(t, w.GetPublisherInfo())
}
