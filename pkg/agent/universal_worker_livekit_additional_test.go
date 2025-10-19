package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUniversalWorkerTrackSubscriptionsLiveKit exercises Subscribe/Unsubscribe/Enable and SetTrackQuality on real publications.
func TestUniversalWorkerTrackSubscriptionsLiveKit(t *testing.T) {
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	pubRoom, subRoom, err := manager.CreateConnectedRooms("uw-subscribe")
	require.NoError(t, err)
	defer pubRoom.Disconnect()
	defer subRoom.Disconnect()

	// Publish synthetic audio track
	audioPub, err := manager.PublishSyntheticAudioTrack(pubRoom, "uw-audio")
	require.NoError(t, err)
	require.NotNil(t, audioPub)

	// Also publish a synthetic video track for quality operations
	videoTrack, err := NewSyntheticVideoTrack("uw-video", 640, 360, 10.0)
	require.NoError(t, err)
	videoTrack.StartGenerating()
	defer videoTrack.StopGenerating()
	_, err = pubRoom.LocalParticipant.PublishTrack(videoTrack.TrackLocalStaticSample, &lksdk.TrackPublicationOptions{
		Name:   "uw-video",
		Source: livekit.TrackSource_CAMERA,
	})
	require.NoError(t, err)

	// Wait for the subscriber to receive tracks
	// Resolve audio remote track
	remoteAudio, err := manager.WaitForRemoteTrack(subRoom, "uw-audio", 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, remoteAudio)

	// Resolve video remote track
	remoteVideo, err := manager.WaitForRemoteTrack(subRoom, "uw-video", 5*time.Second)
	require.NoError(t, err)
	require.NotNil(t, remoteVideo)
	_ = remoteAudio

	// Find corresponding RemoteTrackPublications on subscriber side
	var audioPubRemote, videoPubRemote *lksdk.RemoteTrackPublication
	for _, participant := range subRoom.GetRemoteParticipants() {
		for _, pub := range participant.TrackPublications() {
			if pub.Track() != nil {
				if rt, ok := pub.Track().(*webrtc.TrackRemote); ok {
					if rt == remoteAudio {
						audioPubRemote = pub.(*lksdk.RemoteTrackPublication)
					} else if rt == remoteVideo {
						videoPubRemote = pub.(*lksdk.RemoteTrackPublication)
					}
				}
			}
		}
	}
	require.NotNil(t, audioPubRemote)
	require.NotNil(t, videoPubRemote)

	// Create a minimal worker and exercise track methods
	w := &UniversalWorker{
		subscribedTracks: make(map[string]*PublisherTrackSubscription),
		activeJobs:       make(map[string]*JobContext),
	}

	// Subscribe to audio
	err = w.SubscribeToTrack(audioPubRemote)
	assert.NoError(t, err)
	// Enable/disable via worker
	err = w.EnableTrack(audioPubRemote.SID(), true)
	assert.NoError(t, err)
	err = w.EnableTrack(audioPubRemote.SID(), false)
	assert.NoError(t, err)

	// Set video quality on video publication
	err = w.SubscribeToTrack(videoPubRemote)
	assert.NoError(t, err)
	err = w.SetTrackQuality(videoPubRemote.SID(), livekit.VideoQuality_HIGH)
	// Set dimensions and frame rate (no-ops in SDK v2 but exercises code)
	assert.NoError(t, err)
	assert.NoError(t, w.SetTrackDimensions(videoPubRemote.SID(), 640, 360))
	assert.NoError(t, w.SetTrackFrameRate(videoPubRemote.SID(), 15.0))

	// Unsubscribe audio
	err = w.UnsubscribeFromTrack(audioPubRemote)
	assert.NoError(t, err)

	// Verify state
	tracks := w.GetSubscribedTracks()
	// Audio removed, video present
	_, audioExists := tracks[audioPubRemote.SID()]
	_, videoExists := tracks[videoPubRemote.SID()]
	assert.False(t, audioExists)
	assert.True(t, videoExists)

	_ = videoTrack
}

// TestQualityControllerStartStopLiveKit covers StartMonitoring/StopMonitoring with real track.
func TestQualityControllerStartStopLiveKit(t *testing.T) {
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	pubRoom, subRoom, err := manager.CreateConnectedRooms("qc-monitor")
	require.NoError(t, err)
	defer pubRoom.Disconnect()
	defer subRoom.Disconnect()

	// Publish audio track (sufficient for monitoring loop to run)
	_, err = manager.PublishSyntheticAudioTrack(pubRoom, "qc-audio")
	require.NoError(t, err)

	remoteTrack, err := manager.WaitForRemoteTrack(subRoom, "qc-audio", 5*time.Second)
	require.NoError(t, err)

	qc := NewQualityController()
	sub := &PublisherTrackSubscription{CurrentQuality: livekit.VideoQuality_MEDIUM}

	qc.StartMonitoring(remoteTrack, sub)
	time.Sleep(150 * time.Millisecond)
	qc.StopMonitoring(remoteTrack)
}
