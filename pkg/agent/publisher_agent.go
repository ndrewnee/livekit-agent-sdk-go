package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// PublisherAgentHandler defines the interface for handling publisher agent specific events.
// Publisher agents are assigned when participants start publishing media (audio/video) to a room.
// They provide specialized functionality for monitoring and controlling media streams.
//
// This interface extends JobHandler with media-specific callbacks for track lifecycle,
// quality control, and connection monitoring.
type PublisherAgentHandler interface {
	JobHandler

	// OnPublisherTrackPublished is called when the publisher publishes a new track.
	// This is the initial notification that a track is available for subscription.
	//
	// The agent can choose to subscribe to this track to receive the media stream.
	// Track metadata like source type, dimensions, and simulcast layers are available.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The publisher participant
	//   - publication: The newly published track's metadata
	OnPublisherTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)

	// OnPublisherTrackUnpublished is called when the publisher unpublishes a track.
	// This happens when the publisher stops sharing a media source or disconnects.
	//
	// Any active subscriptions to this track will be automatically terminated.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The publisher participant
	//   - publication: The track that was unpublished
	OnPublisherTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)

	// OnPublisherTrackSubscribed is called when successfully subscribed to a track.
	// After subscription, the agent receives the actual media stream.
	//
	// The TrackRemote provides access to the media packets and can be used
	// for processing, analysis, or forwarding.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The publisher participant
	//   - publication: The track publication metadata
	//   - track: The subscribed media track for receiving data
	OnPublisherTrackSubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote)

	// OnPublisherTrackUnsubscribed is called when unsubscribed from a track.
	// This can happen due to manual unsubscription, track unpublishing,
	// or connection issues.
	//
	// After this callback, the track should no longer be used.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The publisher participant
	//   - track: The track that was unsubscribed
	OnPublisherTrackUnsubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote)

	// OnPublisherQualityChanged is called when track quality changes.
	// This occurs in adaptive streaming scenarios where video quality
	// is adjusted based on network conditions.
	//
	// Quality levels include LOW, MEDIUM, and HIGH, corresponding to
	// different bitrates and resolutions.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The publisher participant
	//   - track: The affected track
	//   - oldQuality: Previous quality level
	//   - newQuality: New quality level
	OnPublisherQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote, oldQuality, newQuality livekit.VideoQuality)

	// OnPublisherConnectionQualityChanged is called when publisher's connection quality changes.
	// Connection quality is determined by packet loss, jitter, and round-trip time.
	//
	// Quality levels: POOR, GOOD, EXCELLENT
	// Agents can use this to adapt processing or notify about potential issues.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The publisher participant
	//   - quality: The new connection quality level
	OnPublisherConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality)
}

// PublisherAgent extends the base Worker with publisher-specific functionality.
// It monitors participants who publish media and provides advanced media control features.
//
// Key features:
//   - Automatic track subscription management
//   - Adaptive quality control based on network conditions
//   - Connection quality monitoring
//   - Fine-grained media control (quality, dimensions, framerate)
//   - Track enable/disable without unsubscribing
//
// Publisher agents are ideal for recording, transcription, or media processing applications.
type PublisherAgent struct {
	*Worker
	handler PublisherAgentHandler

	mu                    sync.RWMutex
	publisherParticipant  *lksdk.RemoteParticipant
	subscribedTracks      map[string]*PublisherTrackSubscription
	qualityController     *QualityController
	connectionMonitor     *ConnectionQualityMonitor
	subscriptionManager   *TrackSubscriptionManager
}

// PublisherTrackSubscription represents a subscribed track from the publisher.
// It maintains state and statistics for each subscribed media stream.
type PublisherTrackSubscription struct {
	Track             *webrtc.TrackRemote
	Publication       *lksdk.RemoteTrackPublication
	SubscribedAt      time.Time
	CurrentQuality    livekit.VideoQuality
	PreferredQuality  livekit.VideoQuality
	Dimensions        *VideoDimensions
	FrameRate         float64
	Bitrate           uint64
	PacketsLost       uint32
	LastQualityChange time.Time
}

// VideoDimensions represents video dimensions.
// Used for specifying preferred video resolution.
type VideoDimensions struct {
	Width  uint32
	Height uint32
}

// NewPublisherAgent creates a new publisher agent.
//
// The agent will automatically set JobType to JT_PUBLISHER.
// When assigned a job, it will monitor the participant who triggered
// the job by publishing media.
//
// Parameters:
//   - serverURL: WebSocket URL of the LiveKit server
//   - apiKey: API key for authentication
//   - apiSecret: API secret for authentication
//   - handler: Implementation of PublisherAgentHandler
//   - opts: Worker options (JobType will be overridden)
//
// Returns a configured PublisherAgent ready to be started.
func NewPublisherAgent(serverURL, apiKey, apiSecret string, handler PublisherAgentHandler, opts WorkerOptions) (*PublisherAgent, error) {
	// Set job type to Publisher
	opts.JobType = livekit.JobType_JT_PUBLISHER

	// Create base worker
	worker := NewWorker(serverURL, apiKey, apiSecret, handler, opts)

	pa := &PublisherAgent{
		Worker:              worker,
		handler:             handler,
		subscribedTracks:    make(map[string]*PublisherTrackSubscription),
		qualityController:   NewQualityController(),
		connectionMonitor:   NewConnectionQualityMonitor(),
		subscriptionManager: NewTrackSubscriptionManager(),
	}

	// Set up internal event handlers
	pa.setupEventHandlers()

	return pa, nil
}

// setupEventHandlers configures internal event handlers for publisher tracking
func (pa *PublisherAgent) setupEventHandlers() {
	// Override the base OnJobAssigned to set up publisher-specific handling
	originalHandler := pa.Worker.handler
	wrappedHandler := &publisherJobHandler{
		JobHandler:    originalHandler,
		publisherAgent: pa,
	}
	pa.Worker.handler = wrappedHandler
}

// publisherJobHandler wraps the user's handler to add publisher-specific functionality
type publisherJobHandler struct {
	JobHandler
	publisherAgent *PublisherAgent
}

func (h *publisherJobHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	// Call original handler first
	err := h.JobHandler.OnJobAssigned(ctx, job, room)
	if err != nil {
		return err
	}

	// Set up publisher-specific tracking
	// Extract publisher identity from job
	var publisherIdentity string
	if job.Participant != nil {
		publisherIdentity = job.Participant.Identity
	}
	
	if publisherIdentity != "" {
		go h.publisherAgent.trackPublisher(ctx, room, publisherIdentity)
	}
	
	return nil
}

// trackPublisher monitors the publisher participant and their tracks
func (pa *PublisherAgent) trackPublisher(ctx context.Context, room *lksdk.Room, publisherIdentity string) {
	logger := logger.GetLogger()
	logger.Infow("starting publisher tracking", "publisher", publisherIdentity)

	// Poll for publisher participant and track events
	// Since we can't modify room callbacks after connection, we'll use a polling approach
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastSeenTracks = make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Find publisher participant
			var publisher *lksdk.RemoteParticipant
			for _, participant := range room.GetRemoteParticipants() {
				if participant.Identity() == publisherIdentity {
					publisher = participant
					break
				}
			}

			if publisher == nil {
				continue
			}

			// Check if this is first time seeing publisher
			pa.mu.Lock()
			if pa.publisherParticipant == nil {
				pa.publisherParticipant = publisher
				pa.mu.Unlock()
				pa.onPublisherConnected(ctx, publisher)
			} else {
				pa.mu.Unlock()
			}

			// Check for new tracks
			for _, publication := range publisher.TrackPublications() {
				if remotePub, ok := publication.(*lksdk.RemoteTrackPublication); ok {
					sid := remotePub.SID()
					if !lastSeenTracks[sid] {
						lastSeenTracks[sid] = true
						pa.onTrackPublished(ctx, publisher, remotePub)
						
						// Check if already subscribed
						if remotePub.IsSubscribed() && remotePub.TrackRemote() != nil {
							pa.onTrackSubscribed(ctx, publisher, remotePub, remotePub.TrackRemote())
						}
					}
				}
			}
		}
	}
}

// onPublisherConnected handles when the publisher joins the room
func (pa *PublisherAgent) onPublisherConnected(ctx context.Context, participant *lksdk.RemoteParticipant) {
	pa.mu.Lock()
	pa.publisherParticipant = participant
	pa.mu.Unlock()

	logger := logger.GetLogger()
	logger.Infow("publisher connected", "identity", participant.Identity(), "metadata", participant.Metadata())

	// Start monitoring connection quality
	pa.connectionMonitor.StartMonitoring(participant)
}

// onPublisherDisconnected handles when the publisher leaves the room
func (pa *PublisherAgent) onPublisherDisconnected(ctx context.Context, participant *lksdk.RemoteParticipant) {
	pa.mu.Lock()
	pa.publisherParticipant = nil
	pa.mu.Unlock()

	logger := logger.GetLogger()
	logger.Infow("publisher disconnected", "identity", participant.Identity())

	// Stop monitoring
	pa.connectionMonitor.StopMonitoring()

	// Clean up all subscriptions
	pa.cleanupAllSubscriptions()
}

// onTrackPublished handles when the publisher publishes a new track
func (pa *PublisherAgent) onTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	logger := logger.GetLogger()
	logger.Infow("publisher track published",
		"identity", participant.Identity(),
		"trackSID", publication.SID(),
		"kind", publication.Kind(),
		"source", publication.Source(),
		"muted", publication.IsMuted())

	// Notify handler
	pa.handler.OnPublisherTrackPublished(ctx, participant, publication)

	// Auto-subscribe based on configuration
	if pa.subscriptionManager.ShouldAutoSubscribe(publication) {
		if err := publication.SetSubscribed(true); err != nil {
			logger.Errorw("failed to subscribe to track", err, "trackSID", publication.SID())
		}
	}
}

// onTrackUnpublished handles when the publisher unpublishes a track
func (pa *PublisherAgent) onTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	logger := logger.GetLogger()
	logger.Infow("publisher track unpublished",
		"identity", participant.Identity(),
		"trackSID", publication.SID())

	// Clean up subscription
	pa.mu.Lock()
	delete(pa.subscribedTracks, publication.SID())
	pa.mu.Unlock()

	// Notify handler
	pa.handler.OnPublisherTrackUnpublished(ctx, participant, publication)
}

// onTrackSubscribed handles successful track subscription
func (pa *PublisherAgent) onTrackSubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote) {
	logger := logger.GetLogger()
	logger.Infow("subscribed to publisher track",
		"identity", participant.Identity(),
		"trackSID", publication.SID(),
		"trackID", track.ID())

	// Create subscription record
	subscription := &PublisherTrackSubscription{
		Track:             track,
		Publication:       publication,
		SubscribedAt:      time.Now(),
		CurrentQuality:    livekit.VideoQuality_HIGH,
		PreferredQuality:  livekit.VideoQuality_HIGH,
		LastQualityChange: time.Now(),
	}

	pa.mu.Lock()
	pa.subscribedTracks[publication.SID()] = subscription
	pa.mu.Unlock()

	// Set up quality monitoring for video tracks
	// Use publication kind to determine track type
	if publication.Kind() == lksdk.TrackKindVideo {
		pa.qualityController.StartMonitoring(track, subscription)
	}

	// Notify handler
	pa.handler.OnPublisherTrackSubscribed(ctx, participant, publication, track)
}

// onTrackUnsubscribed handles track unsubscription
func (pa *PublisherAgent) onTrackUnsubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote) {
	logger := logger.GetLogger()
	logger.Infow("unsubscribed from publisher track",
		"identity", participant.Identity(),
		"trackID", track.ID())

	// Stop quality monitoring
	// We'll stop monitoring for all tracks since we can't easily determine type from TrackRemote
	pa.qualityController.StopMonitoring(track)

	// Notify handler
	pa.handler.OnPublisherTrackUnsubscribed(ctx, participant, track)
}

// onConnectionQualityChanged handles connection quality updates
func (pa *PublisherAgent) onConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
	logger := logger.GetLogger()
	logger.Infow("publisher connection quality changed",
		"identity", participant.Identity(),
		"quality", quality.String())

	// Update connection monitor
	pa.connectionMonitor.UpdateQuality(quality)

	// Notify handler
	pa.handler.OnPublisherConnectionQualityChanged(ctx, participant, quality)

	// Adjust quality preferences based on connection
	pa.adjustQualityForConnection(quality)
}

// adjustQualityForConnection adjusts track quality based on connection quality
func (pa *PublisherAgent) adjustQualityForConnection(quality livekit.ConnectionQuality) {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	for _, subscription := range pa.subscribedTracks {
		// Check if this is a video track by looking at the publication
		if subscription.Publication != nil && subscription.Publication.Kind() == lksdk.TrackKindVideo {
			newQuality := pa.qualityController.CalculateOptimalQuality(quality, subscription)
			if newQuality != subscription.PreferredQuality {
				subscription.PreferredQuality = newQuality
				pa.qualityController.ApplyQualitySettings(subscription.Track, newQuality)
			}
		}
	}
}

// GetPublisherInfo returns information about the tracked publisher.
//
// Returns the RemoteParticipant who is publishing media,
// or nil if no publisher is currently being tracked.
//
// The returned participant reference should not be modified.
func (pa *PublisherAgent) GetPublisherInfo() *lksdk.RemoteParticipant {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.publisherParticipant
}

// GetSubscribedTracks returns all currently subscribed tracks.
//
// Returns a map of track SID to PublisherTrackSubscription.
// The returned map is a copy and safe to iterate over.
//
// This includes all tracks the agent is currently receiving media from.
func (pa *PublisherAgent) GetSubscribedTracks() map[string]*PublisherTrackSubscription {
	pa.mu.RLock()
	defer pa.mu.RUnlock()

	// Return a copy to prevent external modification
	result := make(map[string]*PublisherTrackSubscription)
	for k, v := range pa.subscribedTracks {
		result[k] = v
	}
	return result
}

// SubscribeToTrack manually subscribes to a specific track.
//
// By default, the agent may auto-subscribe based on configuration.
// This method allows explicit subscription control.
//
// Parameters:
//   - publication: The track publication to subscribe to
//
// Returns an error if:
//   - The publication is nil
//   - The subscription fails
//
// After successful subscription, OnPublisherTrackSubscribed will be called.
func (pa *PublisherAgent) SubscribeToTrack(publication *lksdk.RemoteTrackPublication) error {
	if publication == nil {
		return fmt.Errorf("publication cannot be nil")
	}

	logger := logger.GetLogger()
	logger.Infow("manually subscribing to track", "trackSID", publication.SID())

	return publication.SetSubscribed(true)
}

// UnsubscribeFromTrack manually unsubscribes from a specific track.
//
// This stops receiving media from the track while keeping it published.
// Useful for temporarily pausing media reception.
//
// Parameters:
//   - publication: The track publication to unsubscribe from
//
// Returns an error if:
//   - The publication is nil
//   - The unsubscription fails
//
// After successful unsubscription, OnPublisherTrackUnsubscribed will be called.
func (pa *PublisherAgent) UnsubscribeFromTrack(publication *lksdk.RemoteTrackPublication) error {
	if publication == nil {
		return fmt.Errorf("publication cannot be nil")
	}

	logger := logger.GetLogger()
	logger.Infow("manually unsubscribing from track", "trackSID", publication.SID())

	return publication.SetSubscribed(false)
}

// SetTrackQuality sets the preferred quality for a video track.
//
// For simulcast video tracks, this controls which quality layer to receive.
// The actual quality depends on what the publisher is sending.
//
// Parameters:
//   - trackSID: The track's server ID
//   - quality: Desired quality level (LOW, MEDIUM, HIGH)
//
// Returns an error if:
//   - The track is not found
//   - The track is not a video track
//   - The quality change fails
func (pa *PublisherAgent) SetTrackQuality(trackSID string, quality livekit.VideoQuality) error {
	pa.mu.Lock()
	subscription, exists := pa.subscribedTracks[trackSID]
	pa.mu.Unlock()

	if !exists {
		return fmt.Errorf("track %s not found in subscriptions", trackSID)
	}

	if subscription.Publication == nil || subscription.Publication.Kind() != lksdk.TrackKindVideo {
		return fmt.Errorf("quality settings only apply to video tracks")
	}

	subscription.PreferredQuality = quality
	return pa.qualityController.ApplyQualitySettings(subscription.Track, quality)
}

// SetTrackDimensions sets preferred dimensions for a video track.
//
// This requests specific video dimensions from adaptive streams.
// The actual dimensions depend on available encodings.
//
// Parameters:
//   - trackSID: The track's server ID
//   - width: Desired width in pixels
//   - height: Desired height in pixels
//
// Returns an error if:
//   - The track is not found
//   - The track is not a video track
//   - The dimension change fails
func (pa *PublisherAgent) SetTrackDimensions(trackSID string, width, height uint32) error {
	pa.mu.Lock()
	subscription, exists := pa.subscribedTracks[trackSID]
	pa.mu.Unlock()

	if !exists {
		return fmt.Errorf("track %s not found in subscriptions", trackSID)
	}

	if subscription.Publication == nil || subscription.Publication.Kind() != lksdk.TrackKindVideo {
		return fmt.Errorf("dimension settings only apply to video tracks")
	}

	subscription.Dimensions = &VideoDimensions{
		Width:  width,
		Height: height,
	}

	return pa.qualityController.ApplyDimensionSettings(subscription.Track, width, height)
}

// SetTrackFrameRate sets preferred frame rate for a video track.
//
// This requests a specific frame rate from adaptive streams.
// Useful for optimizing bandwidth or processing requirements.
//
// Parameters:
//   - trackSID: The track's server ID
//   - fps: Desired frames per second
//
// Returns an error if:
//   - The track is not found
//   - The track is not a video track
//   - The frame rate change fails
func (pa *PublisherAgent) SetTrackFrameRate(trackSID string, fps float64) error {
	pa.mu.Lock()
	subscription, exists := pa.subscribedTracks[trackSID]
	pa.mu.Unlock()

	if !exists {
		return fmt.Errorf("track %s not found in subscriptions", trackSID)
	}

	if subscription.Publication == nil || subscription.Publication.Kind() != lksdk.TrackKindVideo {
		return fmt.Errorf("frame rate settings only apply to video tracks")
	}

	subscription.FrameRate = fps
	return pa.qualityController.ApplyFrameRateSettings(subscription.Track, fps)
}

// EnableTrack enables or disables a track.
//
// Disabling a track pauses media flow without unsubscribing.
// This is more efficient than subscribe/unsubscribe for temporary pauses.
//
// Parameters:
//   - trackSID: The track's server ID
//   - enabled: true to enable (resume), false to disable (pause)
//
// Returns an error if the track is not found.
func (pa *PublisherAgent) EnableTrack(trackSID string, enabled bool) error {
	pa.mu.Lock()
	subscription, exists := pa.subscribedTracks[trackSID]
	pa.mu.Unlock()

	if !exists {
		return fmt.Errorf("track %s not found in subscriptions", trackSID)
	}

	subscription.Publication.SetEnabled(enabled)
	return nil
}

// cleanupAllSubscriptions cleans up all track subscriptions
func (pa *PublisherAgent) cleanupAllSubscriptions() {
	pa.mu.Lock()
	defer pa.mu.Unlock()

	for _, subscription := range pa.subscribedTracks {
		// Check if this is a video track by looking at the publication
		if subscription.Publication != nil && subscription.Publication.Kind() == lksdk.TrackKindVideo {
			pa.qualityController.StopMonitoring(subscription.Track)
		}
	}

	pa.subscribedTracks = make(map[string]*PublisherTrackSubscription)
}

// Stop gracefully stops the publisher agent.
//
// This method:
//   - Stops connection quality monitoring
//   - Cleans up all track subscriptions
//   - Calls the base Worker.Stop()
//
// Returns nil on success or an error if cleanup fails.
func (pa *PublisherAgent) Stop() error {
	// Clean up monitoring
	pa.connectionMonitor.StopMonitoring()
	pa.cleanupAllSubscriptions()

	// Call base stop
	return pa.Worker.Stop()
}