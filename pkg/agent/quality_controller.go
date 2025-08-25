package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
)

// QualityController manages video quality adaptation for subscribed tracks.
//
// The controller monitors network conditions and track statistics to
// automatically adjust video quality for optimal viewing experience.
// It balances quality against available bandwidth and network conditions.
//
// Key features:
//   - Automatic quality adaptation based on network conditions
//   - Per-track monitoring and adjustment
//   - Configurable adaptation policies
//   - Historical tracking of quality changes
//   - Support for manual quality control
//
// Example usage:
//
//	controller := NewQualityController()
//	controller.SetAdaptationPolicy(customPolicy)
//	controller.StartMonitoring(track, subscription)
//
//	// Quality will be automatically adjusted based on conditions
//	// Or manually control:
//	controller.ApplyQualitySettings(track, livekit.VideoQuality_HIGH)
type QualityController struct {
	mu               sync.RWMutex
	monitors         map[string]*TrackQualityMonitor
	adaptationPolicy QualityAdaptationPolicy
	updateInterval   time.Duration
	stopCh           chan struct{}
	wg               sync.WaitGroup
}

// TrackQualityMonitor monitors quality metrics for a single track.
//
// Each monitored track has its own monitor instance that tracks
// statistics, quality history, and manages adaptation decisions.
type TrackQualityMonitor struct {
	// Track is the WebRTC track being monitored
	Track *webrtc.TrackRemote

	// Subscription contains the track subscription details
	Subscription *PublisherTrackSubscription

	// Stats contains current quality statistics
	Stats *TrackQualityStats

	// LastStatsUpdate is when statistics were last updated
	LastStatsUpdate time.Time

	// QualityHistory tracks all quality changes for this track
	QualityHistory []QualityChange

	// AdaptationEnabled controls whether automatic adaptation is active
	AdaptationEnabled bool
}

// TrackQualityStats contains quality-related statistics.
//
// These metrics are used to make quality adaptation decisions and
// provide visibility into track performance.
type TrackQualityStats struct {
	// CurrentQuality is the current video quality level
	CurrentQuality livekit.VideoQuality

	// PacketsReceived is the total number of packets received
	PacketsReceived uint64

	// PacketsLost is the total number of packets lost
	PacketsLost uint32

	// Bitrate is the current bitrate in bits per second
	Bitrate uint64

	// FrameRate is the current frame rate
	FrameRate float64

	// FrameWidth is the current frame width in pixels
	FrameWidth uint32

	// FrameHeight is the current frame height in pixels
	FrameHeight uint32

	// Jitter is the packet jitter in milliseconds
	Jitter float64

	// RTT is the round-trip time in milliseconds
	RTT float64

	// LastKeyFrame is when the last key frame was received
	LastKeyFrame time.Time

	// FreezeCount is the number of video freezes detected
	FreezeCount uint32

	// PauseCount is the number of video pauses detected
	PauseCount uint32

	// TotalFreezeTime is the cumulative freeze duration
	TotalFreezeTime time.Duration

	// TotalPauseTime is the cumulative pause duration
	TotalPauseTime time.Duration
}

// QualityChange represents a quality level change event.
//
// This records when and why quality was changed, providing an audit
// trail for adaptation decisions.
type QualityChange struct {
	// FromQuality is the quality level before the change
	FromQuality livekit.VideoQuality

	// ToQuality is the quality level after the change
	ToQuality livekit.VideoQuality

	// Reason describes why the quality was changed
	Reason string

	// Timestamp is when the change occurred
	Timestamp time.Time
}

// QualityAdaptationPolicy defines how quality should be adapted.
//
// This policy controls the behavior of automatic quality adaptation,
// including thresholds for making changes and timing constraints.
// Fine-tuning these parameters allows optimization for different
// network conditions and use cases.
type QualityAdaptationPolicy struct {
	// Thresholds for quality changes

	// LossThresholdUp is the packet loss percentage that triggers quality decrease
	LossThresholdUp float64

	// LossThresholdDown is the packet loss percentage threshold to allow quality increase
	LossThresholdDown float64

	// BitrateThresholdUp is the bitrate utilization percentage to allow quality increase
	BitrateThresholdUp float64

	// BitrateThresholdDown is the bitrate utilization percentage that triggers quality decrease
	BitrateThresholdDown float64

	// RTTThresholdHigh is the RTT in milliseconds that triggers quality decrease
	RTTThresholdHigh float64

	// RTTThresholdLow is the RTT in milliseconds threshold to allow quality increase
	RTTThresholdLow float64

	// Timing parameters

	// StableWindowUp is the time to wait before increasing quality
	StableWindowUp time.Duration

	// StableWindowDown is the time to wait before decreasing quality
	StableWindowDown time.Duration

	// MinTimeBetweenChanges is the minimum time between quality changes
	MinTimeBetweenChanges time.Duration

	// Behavior flags

	// PreferTemporalScaling prefers reducing frame rate over resolution
	PreferTemporalScaling bool

	// AllowDynamicFPS allows frame rate adjustments
	AllowDynamicFPS bool

	// MaxQuality is the maximum allowed quality level
	MaxQuality livekit.VideoQuality

	// MinQuality is the minimum allowed quality level
	MinQuality livekit.VideoQuality
}

// DefaultQualityAdaptationPolicy returns a default quality adaptation policy.
//
// The default policy provides balanced settings suitable for most use cases:
//   - 2% packet loss triggers quality decrease
//   - <1% packet loss allows quality increase
//   - 80% bitrate usage allows increase
//   - 95% bitrate usage triggers decrease
//   - 200ms RTT triggers decrease
//   - <100ms RTT allows increase
//   - 10 second stability window for increases
//   - 2 second window for decreases
//
// These defaults prioritize stability while remaining responsive to
// degraded network conditions.
func DefaultQualityAdaptationPolicy() QualityAdaptationPolicy {
	return QualityAdaptationPolicy{
		LossThresholdUp:       0.02, // 2% loss triggers quality decrease
		LossThresholdDown:     0.01, // <1% loss allows quality increase
		BitrateThresholdUp:    0.8,  // 80% bitrate usage allows increase
		BitrateThresholdDown:  0.95, // 95% bitrate usage triggers decrease
		RTTThresholdHigh:      200,  // 200ms RTT triggers decrease
		RTTThresholdLow:       100,  // <100ms RTT allows increase
		StableWindowUp:        10 * time.Second,
		StableWindowDown:      2 * time.Second,
		MinTimeBetweenChanges: 3 * time.Second,
		PreferTemporalScaling: true,
		AllowDynamicFPS:       true,
		MaxQuality:            livekit.VideoQuality_HIGH,
		MinQuality:            livekit.VideoQuality_LOW,
	}
}

// NewQualityController creates a new quality controller.
//
// The controller is initialized with:
//   - Default adaptation policy
//   - 1 second update interval
//   - Empty monitor map
//
// The monitoring loop starts automatically when the first track is added.
func NewQualityController() *QualityController {
	return &QualityController{
		monitors:         make(map[string]*TrackQualityMonitor),
		adaptationPolicy: DefaultQualityAdaptationPolicy(),
		updateInterval:   time.Second,
		stopCh:           make(chan struct{}),
	}
}

// SetAdaptationPolicy sets a custom adaptation policy.
//
// This allows fine-tuning the quality adaptation behavior for specific
// use cases or network conditions. The new policy takes effect immediately
// for all monitored tracks.
func (qc *QualityController) SetAdaptationPolicy(policy QualityAdaptationPolicy) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.adaptationPolicy = policy
}

// StartMonitoring starts monitoring a track's quality.
//
// Once monitoring begins, the controller will:
//   - Collect statistics at regular intervals
//   - Evaluate network conditions
//   - Automatically adjust quality based on the adaptation policy
//   - Record quality changes in history
//
// If the track is already being monitored, this method does nothing.
// The monitoring loop starts automatically with the first track.
func (qc *QualityController) StartMonitoring(track *webrtc.TrackRemote, subscription *PublisherTrackSubscription) {
	qc.mu.Lock()
	defer qc.mu.Unlock()

	if _, exists := qc.monitors[track.ID()]; exists {
		return
	}

	monitor := &TrackQualityMonitor{
		Track:             track,
		Subscription:      subscription,
		Stats:             &TrackQualityStats{CurrentQuality: subscription.CurrentQuality},
		LastStatsUpdate:   time.Now(),
		QualityHistory:    make([]QualityChange, 0),
		AdaptationEnabled: true,
	}

	qc.monitors[track.ID()] = monitor

	// Start monitoring if this is the first track
	if len(qc.monitors) == 1 {
		qc.wg.Add(1)
		go qc.monitorLoop()
	}

	getLogger := logger.GetLogger()
	getLogger.Infow("started quality monitoring", "trackID", track.ID())
}

// StopMonitoring stops monitoring a track's quality.
//
// This removes the track from monitoring and cleans up associated resources.
// If this was the last monitored track, the monitoring loop is also stopped.
//
// Quality history and statistics for the track are discarded.
func (qc *QualityController) StopMonitoring(track *webrtc.TrackRemote) {
	qc.mu.Lock()
	defer qc.mu.Unlock()

	delete(qc.monitors, track.ID())

	// Stop monitoring loop if no more tracks
	if len(qc.monitors) == 0 {
		close(qc.stopCh)
		qc.wg.Wait()
		qc.stopCh = make(chan struct{})
	}

	getLogger := logger.GetLogger()
	getLogger.Infow("stopped quality monitoring", "trackID", track.ID())
}

// monitorLoop continuously monitors track quality and adapts as needed
func (qc *QualityController) monitorLoop() {
	defer qc.wg.Done()

	ticker := time.NewTicker(qc.updateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-qc.stopCh:
			return
		case <-ticker.C:
			qc.updateAllMonitors()
		}
	}
}

// updateAllMonitors updates quality metrics for all monitored tracks
func (qc *QualityController) updateAllMonitors() {
	qc.mu.RLock()
	monitors := make([]*TrackQualityMonitor, 0, len(qc.monitors))
	for _, monitor := range qc.monitors {
		monitors = append(monitors, monitor)
	}
	policy := qc.adaptationPolicy
	qc.mu.RUnlock()

	for _, monitor := range monitors {
		if monitor.AdaptationEnabled {
			qc.updateTrackQuality(monitor, policy)
		}
	}
}

// updateTrackQuality updates quality for a single track based on current stats
func (qc *QualityController) updateTrackQuality(monitor *TrackQualityMonitor, policy QualityAdaptationPolicy) {
	// Get current stats (in real implementation, would get from WebRTC stats)
	stats := monitor.Stats
	now := time.Now()

	// Check if enough time has passed since last change
	if len(monitor.QualityHistory) > 0 {
		lastChange := monitor.QualityHistory[len(monitor.QualityHistory)-1]
		if now.Sub(lastChange.Timestamp) < policy.MinTimeBetweenChanges {
			return
		}
	}

	// Calculate packet loss ratio
	var lossRatio float64
	if stats.PacketsReceived > 0 {
		lossRatio = float64(stats.PacketsLost) / float64(stats.PacketsReceived+uint64(stats.PacketsLost))
	}

	currentQuality := stats.CurrentQuality
	targetQuality := currentQuality
	reason := ""

	// Check if we should decrease quality
	if lossRatio > policy.LossThresholdUp {
		targetQuality = qc.decreaseQuality(currentQuality)
		reason = fmt.Sprintf("high packet loss: %.2f%%", lossRatio*100)
	} else if stats.RTT > policy.RTTThresholdHigh {
		targetQuality = qc.decreaseQuality(currentQuality)
		reason = fmt.Sprintf("high RTT: %.0fms", stats.RTT)
	} else if monitor.shouldDecreaseQuality(policy) {
		targetQuality = qc.decreaseQuality(currentQuality)
		reason = "poor network conditions"
	} else if monitor.shouldIncreaseQuality(policy, lossRatio) {
		// Check if we can increase quality
		targetQuality = qc.increaseQuality(currentQuality)
		reason = "network conditions improved"
	}

	// Apply quality limits
	if targetQuality > policy.MaxQuality {
		targetQuality = policy.MaxQuality
	}
	if targetQuality < policy.MinQuality {
		targetQuality = policy.MinQuality
	}

	// Apply quality change if needed
	if targetQuality != currentQuality {
		if err := qc.ApplyQualitySettings(monitor.Track, targetQuality); err == nil {
			// Record the change
			change := QualityChange{
				FromQuality: currentQuality,
				ToQuality:   targetQuality,
				Reason:      reason,
				Timestamp:   now,
			}
			monitor.QualityHistory = append(monitor.QualityHistory, change)
			monitor.Stats.CurrentQuality = targetQuality
			monitor.Subscription.CurrentQuality = targetQuality
			monitor.Subscription.LastQualityChange = now

			getLogger := logger.GetLogger()
			getLogger.Infow("quality adapted",
				"trackID", monitor.Track.ID(),
				"from", currentQuality.String(),
				"to", targetQuality.String(),
				"reason", reason)
		}
	}
}

// shouldDecreaseQuality checks if quality should be decreased
func (monitor *TrackQualityMonitor) shouldDecreaseQuality(policy QualityAdaptationPolicy) bool {
	stats := monitor.Stats

	// Check freeze events
	if stats.FreezeCount > 2 {
		return true
	}

	// Check bandwidth utilization
	if stats.Bitrate > 0 && float64(stats.Bitrate) > policy.BitrateThresholdDown*float64(monitor.getTargetBitrate()) {
		return true
	}

	// Check jitter
	if stats.Jitter > 50 { // 50ms jitter threshold
		return true
	}

	return false
}

// shouldIncreaseQuality checks if quality can be increased
func (monitor *TrackQualityMonitor) shouldIncreaseQuality(policy QualityAdaptationPolicy, lossRatio float64) bool {
	stats := monitor.Stats
	now := time.Now()

	// Check stability window
	if monitor.Subscription.LastQualityChange.Add(policy.StableWindowUp).After(now) {
		return false
	}

	// Check packet loss
	if lossRatio > policy.LossThresholdDown {
		return false
	}

	// Check RTT
	if stats.RTT > policy.RTTThresholdLow {
		return false
	}

	// Check bandwidth headroom
	if stats.Bitrate > 0 && float64(stats.Bitrate) > policy.BitrateThresholdUp*float64(monitor.getTargetBitrate()) {
		return false
	}

	// No recent freezes
	if stats.FreezeCount > 0 {
		return false
	}

	return true
}

// getTargetBitrate returns the target bitrate for current quality
func (monitor *TrackQualityMonitor) getTargetBitrate() uint64 {
	// Simplified bitrate targets
	switch monitor.Stats.CurrentQuality {
	case livekit.VideoQuality_HIGH:
		return 2_500_000 // 2.5 Mbps
	case livekit.VideoQuality_MEDIUM:
		return 1_000_000 // 1 Mbps
	case livekit.VideoQuality_LOW:
		return 500_000 // 500 Kbps
	default:
		return 1_000_000
	}
}

// decreaseQuality returns the next lower quality level
func (qc *QualityController) decreaseQuality(current livekit.VideoQuality) livekit.VideoQuality {
	switch current {
	case livekit.VideoQuality_HIGH:
		return livekit.VideoQuality_MEDIUM
	case livekit.VideoQuality_MEDIUM:
		return livekit.VideoQuality_LOW
	default:
		return livekit.VideoQuality_LOW
	}
}

// increaseQuality returns the next higher quality level
func (qc *QualityController) increaseQuality(current livekit.VideoQuality) livekit.VideoQuality {
	switch current {
	case livekit.VideoQuality_LOW:
		return livekit.VideoQuality_MEDIUM
	case livekit.VideoQuality_MEDIUM:
		return livekit.VideoQuality_HIGH
	default:
		return livekit.VideoQuality_HIGH
	}
}

// CalculateOptimalQuality calculates optimal quality based on connection quality.
//
// This provides a simple mapping from connection quality indicators to
// video quality levels, respecting the configured max/min quality limits.
//
// Connection quality mapping:
//   - EXCELLENT: Maximum allowed quality
//   - GOOD: One level below maximum (or maximum if max is MEDIUM or lower)
//   - POOR: Minimum allowed quality
//   - Unknown: MEDIUM quality as safe default
//
// This method is useful for setting initial quality or when manual
// quality selection is needed based on connection assessment.
func (qc *QualityController) CalculateOptimalQuality(connQuality livekit.ConnectionQuality, subscription *PublisherTrackSubscription) livekit.VideoQuality {
	qc.mu.RLock()
	policy := qc.adaptationPolicy
	qc.mu.RUnlock()

	// Map connection quality to video quality
	switch connQuality {
	case livekit.ConnectionQuality_EXCELLENT:
		return policy.MaxQuality
	case livekit.ConnectionQuality_GOOD:
		if policy.MaxQuality == livekit.VideoQuality_HIGH {
			return livekit.VideoQuality_MEDIUM
		}
		return policy.MaxQuality
	case livekit.ConnectionQuality_POOR:
		return policy.MinQuality
	default:
		return livekit.VideoQuality_MEDIUM
	}
}

// ApplyQualitySettings applies quality settings to a track.
//
// This method sets the preferred quality level for a subscribed track.
// In a full implementation, this would communicate with the LiveKit server
// to adjust the quality of the incoming stream.
//
// Note: Currently logs the intent. Full implementation requires server
// support for dynamic quality adjustment.
func (qc *QualityController) ApplyQualitySettings(track *webrtc.TrackRemote, quality livekit.VideoQuality) error {
	if track == nil {
		return fmt.Errorf("track cannot be nil")
	}

	// In the SDK, this would translate to sending UpdateTrackSettings
	// For now, we'll log the intent
	getLogger := logger.GetLogger()
	getLogger.Infow("applying quality settings",
		"trackID", track.ID(),
		"quality", quality.String())

	// TODO: When SDK supports it, this would call something like:
	// track.SetPreferredQuality(quality)

	return nil
}

// ApplyDimensionSettings applies dimension preferences to a track.
//
// This method sets the preferred video dimensions (resolution) for a
// subscribed track. This allows fine-grained control over bandwidth
// usage by requesting specific resolutions.
//
// Note: Currently logs the intent. Full implementation requires server
// support for dynamic dimension adjustment.
func (qc *QualityController) ApplyDimensionSettings(track *webrtc.TrackRemote, width, height uint32) error {
	if track == nil {
		return fmt.Errorf("track cannot be nil")
	}

	getLogger := logger.GetLogger()
	getLogger.Infow("applying dimension settings",
		"trackID", track.ID(),
		"width", width,
		"height", height)

	// TODO: When SDK supports it, this would call something like:
	// track.SetPreferredDimensions(width, height)

	return nil
}

// ApplyFrameRateSettings applies frame rate preferences to a track.
//
// This method sets the preferred frame rate for a subscribed track.
// Lowering frame rate can significantly reduce bandwidth usage while
// maintaining resolution, useful for screen share or presentation content.
//
// Note: Currently logs the intent. Full implementation requires server
// support for dynamic frame rate adjustment.
func (qc *QualityController) ApplyFrameRateSettings(track *webrtc.TrackRemote, fps float64) error {
	if track == nil {
		return fmt.Errorf("track cannot be nil")
	}

	getLogger := logger.GetLogger()
	getLogger.Infow("applying frame rate settings",
		"trackID", track.ID(),
		"fps", fps)

	// TODO: When SDK supports it, this would call something like:
	// track.SetPreferredFrameRate(fps)

	return nil
}

// EnableAdaptation enables or disables quality adaptation for a track.
//
// When disabled, the track's quality remains fixed at its current level
// regardless of network conditions. This is useful when you want manual
// control over quality or need consistent quality for specific use cases.
//
// Adaptation can be re-enabled at any time to resume automatic adjustment.
func (qc *QualityController) EnableAdaptation(trackID string, enabled bool) {
	qc.mu.Lock()
	defer qc.mu.Unlock()

	if monitor, exists := qc.monitors[trackID]; exists {
		monitor.AdaptationEnabled = enabled
		getLogger := logger.GetLogger()
		getLogger.Infow("quality adaptation state changed",
			"trackID", trackID,
			"enabled", enabled)
	}
}

// GetTrackStats returns current quality statistics for a track.
//
// Returns a copy of the track's current statistics and true if the
// track is being monitored, or nil and false if not found.
//
// The returned statistics provide insight into:
//   - Current quality level
//   - Network metrics (packet loss, bitrate, RTT)
//   - Video metrics (resolution, frame rate)
//   - Quality issues (freezes, pauses)
func (qc *QualityController) GetTrackStats(trackID string) (*TrackQualityStats, bool) {
	qc.mu.RLock()
	defer qc.mu.RUnlock()

	if monitor, exists := qc.monitors[trackID]; exists {
		// Return a copy to prevent external modification
		statsCopy := *monitor.Stats
		return &statsCopy, true
	}
	return nil, false
}

// GetQualityHistory returns the quality change history for a track.
//
// Returns a copy of all quality changes for the specified track,
// ordered chronologically. Returns nil if the track is not found.
//
// The history provides an audit trail showing:
//   - When quality changed
//   - What the quality changed from and to
//   - Why the change was made
//
// This is useful for debugging adaptation behavior and understanding
// quality patterns over time.
func (qc *QualityController) GetQualityHistory(trackID string) []QualityChange {
	qc.mu.RLock()
	defer qc.mu.RUnlock()

	if monitor, exists := qc.monitors[trackID]; exists {
		// Return a copy
		history := make([]QualityChange, len(monitor.QualityHistory))
		copy(history, monitor.QualityHistory)
		return history
	}
	return nil
}

// SetUpdateInterval sets how often quality is evaluated.
//
// The interval controls the frequency of:
//   - Statistics collection
//   - Quality evaluation
//   - Adaptation decisions
//
// Shorter intervals provide more responsive adaptation but increase
// processing overhead. Longer intervals reduce overhead but may miss
// short-term network issues.
//
// Default: 1 second. Recommended range: 500ms - 5s.
func (qc *QualityController) SetUpdateInterval(interval time.Duration) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.updateInterval = interval
}
