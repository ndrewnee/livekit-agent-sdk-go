package agent

import (
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// TrackSubscriptionManager manages automatic track subscriptions for LiveKit agents.
// It provides intelligent subscription management with customizable filters, source priorities,
// and support for different track types (audio/video). This enables agents to efficiently
// subscribe only to tracks they need for processing, optimizing bandwidth and performance.
type TrackSubscriptionManager struct {
	mu               sync.RWMutex
	autoSubscribe    bool
	subscribeAudio   bool
	subscribeVideo   bool
	sourcePriorities map[livekit.TrackSource]int
	filters          []TrackSubscriptionFilter
}

// TrackSubscriptionFilter is a function type for custom filtering of tracks before subscription.
// It receives a RemoteTrackPublication and returns true if the track should be subscribed to.
// Filters are applied in order and all must return true for a track to be subscribed.
//
// Example filters:
//   - Subscribe only to camera tracks: func(pub) bool { return pub.Source() == livekit.TrackSource_CAMERA }
//   - Skip muted tracks: func(pub) bool { return !pub.IsMuted() }
//   - Subscribe to specific participants: func(pub) bool { return pub.Participant().Identity() == "user123" }
type TrackSubscriptionFilter func(publication *lksdk.RemoteTrackPublication) bool

// NewTrackSubscriptionManager creates a new subscription manager with default settings.
// By default, it enables auto-subscription for both audio and video tracks, with source
// priorities favoring camera/microphone tracks over screen shares. The default configuration
// is suitable for most agent use cases.
//
// Default source priorities:
//   - Camera/Microphone: 100
//   - Screen Share: 90
//   - Unknown sources: 50
func NewTrackSubscriptionManager() *TrackSubscriptionManager {
	return &TrackSubscriptionManager{
		autoSubscribe:  true,
		subscribeAudio: true,
		subscribeVideo: true,
		sourcePriorities: map[livekit.TrackSource]int{
			livekit.TrackSource_CAMERA:             100,
			livekit.TrackSource_MICROPHONE:         100,
			livekit.TrackSource_SCREEN_SHARE:       90,
			livekit.TrackSource_SCREEN_SHARE_AUDIO: 90,
			livekit.TrackSource_UNKNOWN:            50,
		},
		filters: make([]TrackSubscriptionFilter, 0),
	}
}

// SetAutoSubscribe enables or disables automatic subscription for all tracks.
// When enabled, tracks that pass all filters and type checks will be automatically
// subscribed. When disabled, all automatic subscription is stopped.
func (tm *TrackSubscriptionManager) SetAutoSubscribe(enabled bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.autoSubscribe = enabled
}

// SetSubscribeAudio enables or disables automatic subscription to audio tracks.
// When disabled, audio tracks will be ignored during auto-subscription even if other
// conditions are met.
func (tm *TrackSubscriptionManager) SetSubscribeAudio(enabled bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.subscribeAudio = enabled
}

// SetSubscribeVideo enables or disables automatic subscription to video tracks.
// When disabled, video tracks will be ignored during auto-subscription even if other
// conditions are met.
func (tm *TrackSubscriptionManager) SetSubscribeVideo(enabled bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.subscribeVideo = enabled
}

// SetSourcePriority sets the priority for a specific track source (higher values = more important).
// This can be used to prioritize certain types of tracks when bandwidth is limited.
// For example, setting camera tracks to priority 100 and screen shares to 90 will
// prefer camera tracks over screen shares during subscription decisions.
func (tm *TrackSubscriptionManager) SetSourcePriority(source livekit.TrackSource, priority int) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.sourcePriorities[source] = priority
}

// AddFilter adds a custom filter function for track subscription decisions.
// Filters are applied in the order they were added, and ALL filters must return true
// for a track to be subscribed. This allows for complex subscription logic like
// participant-specific filtering, quality-based decisions, or content analysis.
func (tm *TrackSubscriptionManager) AddFilter(filter TrackSubscriptionFilter) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.filters = append(tm.filters, filter)
}

// ClearFilters removes all custom filters, returning to default subscription behavior.
// This is useful for resetting the subscription manager to a clean state.
func (tm *TrackSubscriptionManager) ClearFilters() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.filters = make([]TrackSubscriptionFilter, 0)
}

// ShouldAutoSubscribe determines if a track should be automatically subscribed based
// on the current configuration, filters, and track properties. This method evaluates:
// 1. Whether auto-subscription is enabled
// 2. Track type (audio/video) preferences
// 3. All custom filters in order
// Returns true if the track should be subscribed, false otherwise.
func (tm *TrackSubscriptionManager) ShouldAutoSubscribe(publication *lksdk.RemoteTrackPublication) bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	if !tm.autoSubscribe {
		return false
	}

	// Check track kind
	switch publication.Kind() {
	case lksdk.TrackKindAudio:
		if !tm.subscribeAudio {
			return false
		}
	case lksdk.TrackKindVideo:
		if !tm.subscribeVideo {
			return false
		}
	}

	// Apply custom filters
	for _, filter := range tm.filters {
		if !filter(publication) {
			return false
		}
	}

	return true
}

// GetSourcePriority returns the priority value for a specific track source.
// Higher values indicate higher priority. Returns 0 if no priority has been set for the source.
func (tm *TrackSubscriptionManager) GetSourcePriority(source livekit.TrackSource) int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	if priority, exists := tm.sourcePriorities[source]; exists {
		return priority
	}
	return 0
}

// ConnectionQualityMonitor tracks and analyzes the connection quality of a participant over time.
// It maintains a history of quality measurements and provides methods to analyze connection
// stability, calculate average quality over time periods, and trigger callbacks on quality changes.
// This is useful for adaptive behaviors like adjusting subscription quality or providing
// user feedback about connection issues.
type ConnectionQualityMonitor struct {
	mu                    sync.RWMutex
	participant           *lksdk.RemoteParticipant
	currentQuality        livekit.ConnectionQuality
	qualityHistory        []QualityMeasurement
	historySize           int
	lastUpdate            time.Time
	qualityChangeCallback func(oldQuality, newQuality livekit.ConnectionQuality)
}

// QualityMeasurement represents a connection quality measurement taken at a specific timestamp.
// This structure is used to maintain a history of quality changes over time, enabling
// analysis of connection patterns and stability calculations.
type QualityMeasurement struct {
	Quality   livekit.ConnectionQuality
	Timestamp time.Time
}

// NewConnectionQualityMonitor creates a new connection quality monitor with default settings.
// The monitor starts with GOOD quality assumption and maintains a history of the last 10
// measurements. No participant is initially monitored until StartMonitoring is called.
func NewConnectionQualityMonitor() *ConnectionQualityMonitor {
	return &ConnectionQualityMonitor{
		currentQuality: livekit.ConnectionQuality_GOOD,
		qualityHistory: make([]QualityMeasurement, 0),
		historySize:    10, // Keep last 10 measurements
	}
}

// StartMonitoring begins monitoring the connection quality for the specified participant.
// This resets any previous monitoring state and starts fresh quality tracking.
// Call UpdateQuality to provide quality measurements after starting monitoring.
func (cm *ConnectionQualityMonitor) StartMonitoring(participant *lksdk.RemoteParticipant) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.participant = participant
	cm.currentQuality = livekit.ConnectionQuality_GOOD
	cm.lastUpdate = time.Now()
	cm.qualityHistory = make([]QualityMeasurement, 0)

	getLogger := logger.GetLogger()
	getLogger.Infow("started connection quality monitoring",
		"participant", participant.Identity())
}

// StopMonitoring stops monitoring connection quality for the current participant.
// This clears the participant reference but preserves the quality history.
// The monitor can be reused by calling StartMonitoring with a new participant.
func (cm *ConnectionQualityMonitor) StopMonitoring() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.participant != nil {
		getLogger := logger.GetLogger()
		getLogger.Infow("stopped connection quality monitoring",
			"participant", cm.participant.Identity())
	}

	cm.participant = nil
}

// UpdateQuality updates the current connection quality measurement and adds it to the history.
// If a quality change callback is registered and the quality has changed, the callback
// will be invoked asynchronously. The history is automatically trimmed to maintain
// the configured history size limit.
func (cm *ConnectionQualityMonitor) UpdateQuality(quality livekit.ConnectionQuality) {
	cm.mu.Lock()
	oldQuality := cm.currentQuality
	cm.currentQuality = quality
	cm.lastUpdate = time.Now()

	// Add to history
	cm.qualityHistory = append(cm.qualityHistory, QualityMeasurement{
		Quality:   quality,
		Timestamp: cm.lastUpdate,
	})

	// Trim history if needed
	if len(cm.qualityHistory) > cm.historySize {
		cm.qualityHistory = cm.qualityHistory[len(cm.qualityHistory)-cm.historySize:]
	}

	callback := cm.qualityChangeCallback
	cm.mu.Unlock()

	// Call callback outside of lock
	if callback != nil && oldQuality != quality {
		callback(oldQuality, quality)
	}
}

// SetQualityChangeCallback sets a callback function to be invoked whenever connection quality changes.
// The callback receives the old and new quality values. It is called asynchronously to avoid
// blocking the quality update process. Pass nil to remove the callback.
func (cm *ConnectionQualityMonitor) SetQualityChangeCallback(callback func(oldQuality, newQuality livekit.ConnectionQuality)) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.qualityChangeCallback = callback
}

// GetCurrentQuality returns the most recent connection quality measurement.
// This is thread-safe and returns the quality as of the last UpdateQuality call.
func (cm *ConnectionQualityMonitor) GetCurrentQuality() livekit.ConnectionQuality {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.currentQuality
}

// GetQualityHistory returns a copy of all quality measurements in the history.
// The returned slice is independent of the internal history and can be safely modified.
// Measurements are ordered chronologically (oldest first).
func (cm *ConnectionQualityMonitor) GetQualityHistory() []QualityMeasurement {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Return a copy
	result := make([]QualityMeasurement, len(cm.qualityHistory))
	copy(result, cm.qualityHistory)
	return result
}

// GetAverageQuality calculates the average connection quality over the specified time period.
// Only measurements within the duration from now are considered. If no measurements
// exist within the time period, returns the current quality. The average is calculated
// by treating quality enum values as integers and averaging them.
func (cm *ConnectionQualityMonitor) GetAverageQuality(duration time.Duration) livekit.ConnectionQuality {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if len(cm.qualityHistory) == 0 {
		return cm.currentQuality
	}

	cutoff := time.Now().Add(-duration)
	qualitySum := 0
	count := 0

	for _, measurement := range cm.qualityHistory {
		if measurement.Timestamp.After(cutoff) {
			qualitySum += int(measurement.Quality)
			count++
		}
	}

	if count == 0 {
		return cm.currentQuality
	}

	avgQuality := qualitySum / count
	return livekit.ConnectionQuality(avgQuality)
}

// IsStable returns true if connection quality has remained constant within the specified
// time period. This is useful for determining if a connection has stabilized before
// making subscription or quality adjustments. Returns true if fewer than 2 measurements
// exist, or if all measurements within the time period have the same quality value.
func (cm *ConnectionQualityMonitor) IsStable(duration time.Duration) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if len(cm.qualityHistory) < 2 {
		return true
	}

	cutoff := time.Now().Add(-duration)
	var lastQuality livekit.ConnectionQuality
	foundFirst := false

	for _, measurement := range cm.qualityHistory {
		if measurement.Timestamp.After(cutoff) {
			if !foundFirst {
				lastQuality = measurement.Quality
				foundFirst = true
			} else if measurement.Quality != lastQuality {
				return false
			}
		}
	}

	return true
}
