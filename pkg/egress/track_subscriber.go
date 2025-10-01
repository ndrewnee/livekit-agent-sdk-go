package egress

import (
	"sync"
	"sync/atomic"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// TrackSubscriber handles track subscription logic for the egress system
// Implements track selection and subscription management per PLAN.md Milestone 2
type TrackSubscriber struct {
	config          *RecordingConfig
	room            *lksdk.Room
	qualitySettings *QualitySettings

	// Track state management
	subscriptions map[string]*TrackSubscription // Track SID -> subscription
	mu            sync.RWMutex

	// Preferred track selection
	preferredVideoSID string
	preferredAudioSID string

	// Statistics
	stats SubscriberStats

	// Callbacks
	onTrackSubscribed   func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication)
	onTrackUnsubscribed func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication)
}

// TrackSubscription represents a single track subscription
type TrackSubscription struct {
	TrackSID       string                            `json:"track_sid"`
	ParticipantSID string                            `json:"participant_sid"`
	Identity       string                            `json:"identity"`
	Kind           lksdk.TrackKind                   `json:"kind"`
	Source         livekit.TrackSource               `json:"source"`
	MimeType       string                            `json:"mime_type"`
	Subscribed     bool                              `json:"subscribed"`
	SubscribedAt   int64                             `json:"subscribed_at"`
	Track          *webrtc.TrackRemote               `json:"-"`
	Publication    *lksdk.RemoteTrackPublication     `json:"-"`
}

// SubscriberStats holds track subscriber statistics
type SubscriberStats struct {
	TotalTracksPublished   int64 `json:"total_tracks_published"`
	TotalTracksSubscribed  int64 `json:"total_tracks_subscribed"`
	CurrentSubscriptions   int   `json:"current_subscriptions"`
	VideoTracksSubscribed  int64 `json:"video_tracks_subscribed"`
	AudioTracksSubscribed  int64 `json:"audio_tracks_subscribed"`
	ScreenShareSubscribed  int64 `json:"screen_share_subscribed"`
	SubscriptionFailures   int64 `json:"subscription_failures"`
	UnsubscriptionFailures int64 `json:"unsubscription_failures"`
}

// NewTrackSubscriber creates a new track subscriber
func NewTrackSubscriber(config *RecordingConfig, room *lksdk.Room) *TrackSubscriber {
	return &TrackSubscriber{
		config:            config,
		room:              room,
		subscriptions:     make(map[string]*TrackSubscription),
		preferredVideoSID: config.PreferredVideoTrack,
		preferredAudioSID: config.PreferredAudioTrack,
	}
}

// Start initializes the track subscriber
func (ts *TrackSubscriber) Start() error {
	logger.Infow("starting track subscriber",
		"recordVideo", ts.config.RecordVideo,
		"recordAudio", ts.config.RecordAudio,
		"recordScreenShare", ts.config.RecordScreenShare)

	// Subscribe to existing published tracks
	ts.subscribeToExistingTracks()

	return nil
}

// subscribeToExistingTracks subscribes to tracks already published in the room
func (ts *TrackSubscriber) subscribeToExistingTracks() {
	// TODO: SDK v2 doesn't expose GetTracks() on RemoteParticipant
	// Need to track publications through events instead
	/*
	participants := ts.room.GetRemoteParticipants()

	for _, participant := range participants {
		for _, track := range participant.GetTracks() {
			publication := track.(*lksdk.RemoteTrackPublication)

			// Track the publication
			ts.trackPublication(publication, participant)

			// Subscribe if appropriate
			if ts.shouldSubscribe(publication, participant) {
				ts.subscribeToTrack(publication, participant)
			}
		}
	}

	logger.Infow("subscribed to existing tracks",
		"participants", len(participants),
		"subscriptions", len(ts.subscriptions))
	*/
}

// OnTrackPublished handles new track publications
func (ts *TrackSubscriber) OnTrackPublished(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Debugw("track published",
		"trackSID", publication.SID(),
		"participantSID", participant.SID(),
		"identity", participant.Identity(),
		"kind", publication.Kind(),
		"source", publication.Source())

	atomic.AddInt64(&ts.stats.TotalTracksPublished, 1)

	// Track the publication
	ts.trackPublication(publication, participant)

	// Auto-subscribe if configured and appropriate
	if ts.config.AutoStart && ts.shouldSubscribe(publication, participant) {
		ts.subscribeToTrack(publication, participant)
	}
}

// OnTrackUnpublished handles track unpublishing
func (ts *TrackSubscriber) OnTrackUnpublished(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Debugw("track unpublished",
		"trackSID", publication.SID(),
		"participantSID", participant.SID())

	ts.mu.Lock()
	delete(ts.subscriptions, publication.SID())
	ts.mu.Unlock()
}

// shouldSubscribe determines if we should subscribe to a track
func (ts *TrackSubscriber) shouldSubscribe(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) bool {
	// Check if already subscribed
	if publication.IsSubscribed() {
		return false
	}

	// Check track kind and recording configuration
	switch publication.Kind() {
	case lksdk.TrackKindVideo:
		if !ts.config.RecordVideo {
			return false
		}

		// Check if it's screen share
		if publication.Source() == livekit.TrackSource_SCREEN_SHARE {
			if !ts.config.RecordScreenShare {
				return false
			}
		}

		// Check for preferred video track
		if ts.preferredVideoSID != "" {
			// Match by track SID
			if publication.SID() == ts.preferredVideoSID {
				return true
			}
			// Match by participant identity
			if participant.Identity() == ts.preferredVideoSID {
				// This is the preferred participant's video
				if publication.Source() == livekit.TrackSource_CAMERA {
					return true
				}
			}
			// Don't subscribe to other video tracks if we have a preference
			return false
		}

	case lksdk.TrackKindAudio:
		if !ts.config.RecordAudio {
			return false
		}

		// Check for preferred audio track
		if ts.preferredAudioSID != "" {
			// Match by track SID
			if publication.SID() == ts.preferredAudioSID {
				return true
			}
			// Match by participant identity
			if participant.Identity() == ts.preferredAudioSID {
				// This is the preferred participant's audio
				if publication.Source() == livekit.TrackSource_MICROPHONE {
					return true
				}
			}
			// Don't subscribe to other audio tracks if we have a preference
			return false
		}

	default:
		// Unknown track kind
		return false
	}

	// Check minimum participants requirement
	if ts.config.MinParticipants > 0 {
		participantCount := len(ts.room.GetRemoteParticipants())
		if participantCount < ts.config.MinParticipants {
			logger.Debugw("not subscribing, minimum participants not met",
				"current", participantCount,
				"required", ts.config.MinParticipants)
			return false
		}
	}

	return true
}

// subscribeToTrack subscribes to a specific track
func (ts *TrackSubscriber) subscribeToTrack(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Infow("subscribing to track",
		"trackSID", publication.SID(),
		"participantSID", participant.SID(),
		"identity", participant.Identity(),
		"kind", publication.Kind(),
		"source", publication.Source())

	// Apply quality settings if available
	if ts.qualitySettings != nil {
		if err := ts.qualitySettings.ApplyToPublication(publication); err != nil {
			logger.Debugw("failed to apply quality settings",
				"trackSID", publication.SID(),
				"error", err.Error())
		}
	}

	// Set subscription options based on quality preferences
	err := publication.SetSubscribed(true)
	if err != nil {
		logger.Errorw("failed to subscribe to track", err,
			"trackSID", publication.SID())
		atomic.AddInt64(&ts.stats.SubscriptionFailures, 1)
		return
	}

	// Track successful subscription
	atomic.AddInt64(&ts.stats.TotalTracksSubscribed, 1)

	switch publication.Kind() {
	case lksdk.TrackKindVideo:
		if publication.Source() == livekit.TrackSource_SCREEN_SHARE {
			atomic.AddInt64(&ts.stats.ScreenShareSubscribed, 1)
		} else {
			atomic.AddInt64(&ts.stats.VideoTracksSubscribed, 1)
		}
	case lksdk.TrackKindAudio:
		atomic.AddInt64(&ts.stats.AudioTracksSubscribed, 1)
	}
}

// unsubscribeFromTrack unsubscribes from a specific track
func (ts *TrackSubscriber) unsubscribeFromTrack(publication *lksdk.RemoteTrackPublication) {
	logger.Infow("unsubscribing from track", "trackSID", publication.SID())

	err := publication.SetSubscribed(false)
	if err != nil {
		logger.Errorw("failed to unsubscribe from track", err,
			"trackSID", publication.SID())
		atomic.AddInt64(&ts.stats.UnsubscriptionFailures, 1)
	}
}

// trackPublication records a track publication
func (ts *TrackSubscriber) trackPublication(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	subscription := &TrackSubscription{
		TrackSID:       publication.SID(),
		ParticipantSID: participant.SID(),
		Identity:       participant.Identity(),
		Kind:           publication.Kind(),
		Source:         publication.Source(),
		MimeType:       publication.MimeType(),
		Publication:    publication,
	}

	ts.subscriptions[publication.SID()] = subscription
}

// OnTrackSubscribed handles successful track subscription
func (ts *TrackSubscriber) OnTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Infow("track subscribed successfully",
		"trackSID", publication.SID(),
		"codec", track.Codec().MimeType)

	ts.mu.Lock()
	if subscription, exists := ts.subscriptions[publication.SID()]; exists {
		subscription.Subscribed = true
		subscription.Track = track
	}
	ts.mu.Unlock()

	// Trigger callback
	if ts.onTrackSubscribed != nil {
		ts.onTrackSubscribed(track, publication)
	}
}

// OnTrackUnsubscribed handles track unsubscription
func (ts *TrackSubscriber) OnTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	logger.Infow("track unsubscribed",
		"trackSID", publication.SID())

	ts.mu.Lock()
	if subscription, exists := ts.subscriptions[publication.SID()]; exists {
		subscription.Subscribed = false
		subscription.Track = nil
	}
	ts.mu.Unlock()

	// Trigger callback
	if ts.onTrackUnsubscribed != nil {
		ts.onTrackUnsubscribed(track, publication)
	}
}

// SetPreferredTracks updates the preferred track selections
func (ts *TrackSubscriber) SetPreferredTracks(videoTrack, audioTrack string) {
	ts.mu.Lock()
	oldVideoPreference := ts.preferredVideoSID
	oldAudioPreference := ts.preferredAudioSID
	ts.preferredVideoSID = videoTrack
	ts.preferredAudioSID = audioTrack
	ts.mu.Unlock()

	logger.Infow("preferred tracks updated",
		"videoTrack", videoTrack,
		"audioTrack", audioTrack)

	// Re-evaluate subscriptions if preferences changed
	if oldVideoPreference != videoTrack || oldAudioPreference != audioTrack {
		ts.reEvaluateSubscriptions()
	}
}

// reEvaluateSubscriptions re-evaluates all subscriptions based on current preferences
func (ts *TrackSubscriber) reEvaluateSubscriptions() {
	// TODO: SDK v2 doesn't expose GetTracks() on RemoteParticipant
	// Need to track publications through events instead
	/*
	participants := ts.room.GetRemoteParticipants()

	for _, participant := range participants {
		for _, track := range participant.GetTracks() {
			publication := track.(*lksdk.RemoteTrackPublication)

			shouldSub := ts.shouldSubscribe(publication, participant)
			isSubscribed := publication.IsSubscribed()

			if shouldSub && !isSubscribed {
				// Should be subscribed but isn't
				ts.subscribeToTrack(publication, participant)
			} else if !shouldSub && isSubscribed {
				// Shouldn't be subscribed but is
				ts.unsubscribeFromTrack(publication)
			}
		}
	}
	*/
}

// GetSubscriptions returns the current track subscriptions
func (ts *TrackSubscriber) GetSubscriptions() map[string]*TrackSubscription {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	// Create a copy
	result := make(map[string]*TrackSubscription)
	for k, v := range ts.subscriptions {
		// Copy the subscription (shallow copy is fine for our purposes)
		sub := *v
		result[k] = &sub
	}

	return result
}

// GetStats returns subscriber statistics
func (ts *TrackSubscriber) GetStats() SubscriberStats {
	ts.mu.RLock()
	currentSubs := 0
	for _, sub := range ts.subscriptions {
		if sub.Subscribed {
			currentSubs++
		}
	}
	ts.mu.RUnlock()

	return SubscriberStats{
		TotalTracksPublished:   atomic.LoadInt64(&ts.stats.TotalTracksPublished),
		TotalTracksSubscribed:  atomic.LoadInt64(&ts.stats.TotalTracksSubscribed),
		CurrentSubscriptions:   currentSubs,
		VideoTracksSubscribed:  atomic.LoadInt64(&ts.stats.VideoTracksSubscribed),
		AudioTracksSubscribed:  atomic.LoadInt64(&ts.stats.AudioTracksSubscribed),
		ScreenShareSubscribed:  atomic.LoadInt64(&ts.stats.ScreenShareSubscribed),
		SubscriptionFailures:   atomic.LoadInt64(&ts.stats.SubscriptionFailures),
		UnsubscriptionFailures: atomic.LoadInt64(&ts.stats.UnsubscriptionFailures),
	}
}

// SetCallbacks sets the track subscription callbacks
func (ts *TrackSubscriber) SetCallbacks(
	onSubscribed func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication),
	onUnsubscribed func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication)) {
	ts.onTrackSubscribed = onSubscribed
	ts.onTrackUnsubscribed = onUnsubscribed
}

// SetQualitySettings sets the quality settings for track subscriptions
func (ts *TrackSubscriber) SetQualitySettings(settings *QualitySettings) {
	ts.qualitySettings = settings
}

// Stop stops the track subscriber
func (ts *TrackSubscriber) Stop() {
	logger.Infow("stopping track subscriber")

	// Unsubscribe from all tracks
	ts.mu.RLock()
	publications := make([]*lksdk.RemoteTrackPublication, 0, len(ts.subscriptions))
	for _, sub := range ts.subscriptions {
		if sub.Subscribed && sub.Publication != nil {
			publications = append(publications, sub.Publication)
		}
	}
	ts.mu.RUnlock()

	// Unsubscribe from all
	for _, pub := range publications {
		ts.unsubscribeFromTrack(pub)
	}

	// Clear subscriptions
	ts.mu.Lock()
	ts.subscriptions = make(map[string]*TrackSubscription)
	ts.mu.Unlock()

	logger.Infow("track subscriber stopped",
		"totalSubscribed", atomic.LoadInt64(&ts.stats.TotalTracksSubscribed))
}

// GetSubscribedTrackCount returns the number of currently subscribed tracks
func (ts *TrackSubscriber) GetSubscribedTrackCount() (video, audio, screenShare int) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	for _, sub := range ts.subscriptions {
		if !sub.Subscribed {
			continue
		}

		switch sub.Kind {
		case lksdk.TrackKindVideo:
			if sub.Source == livekit.TrackSource_SCREEN_SHARE {
				screenShare++
			} else {
				video++
			}
		case lksdk.TrackKindAudio:
			audio++
		}
	}

	return
}

// HasActiveSubscriptions returns true if there are active subscriptions
func (ts *TrackSubscriber) HasActiveSubscriptions() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	for _, sub := range ts.subscriptions {
		if sub.Subscribed {
			return true
		}
	}

	return false
}

// FindTrackByParticipant finds tracks for a specific participant
func (ts *TrackSubscriber) FindTrackByParticipant(participantIdentity string) []*TrackSubscription {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	var tracks []*TrackSubscription
	for _, sub := range ts.subscriptions {
		if sub.Identity == participantIdentity {
			// Create a copy
			subCopy := *sub
			tracks = append(tracks, &subCopy)
		}
	}

	return tracks
}