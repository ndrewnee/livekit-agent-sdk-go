package agent

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQualityControllerCreation tests controller creation and initialization
func TestQualityControllerCreation(t *testing.T) {
	controller := NewQualityController()

	require.NotNil(t, controller)
	assert.NotNil(t, controller.monitors)
	assert.Empty(t, controller.monitors)
	assert.NotNil(t, controller.adaptationPolicy)
	assert.Equal(t, time.Second, controller.updateInterval)
	assert.NotNil(t, controller.stopCh)
}

// TestQualityControllerAdaptationPolicy tests setting custom adaptation policy
func TestQualityControllerAdaptationPolicy(t *testing.T) {
	controller := NewQualityController()

	// Create custom policy
	policy := QualityAdaptationPolicy{
		LossThresholdUp:      5.0,
		LossThresholdDown:    2.0,
		BitrateThresholdUp:   80.0,
		BitrateThresholdDown: 30.0,
		RTTThresholdHigh:     300,
		RTTThresholdLow:      100,
		MinQuality:           livekit.VideoQuality_LOW,
		MaxQuality:           livekit.VideoQuality_HIGH,
	}

	controller.SetAdaptationPolicy(policy)

	controller.mu.RLock()
	assert.Equal(t, policy, controller.adaptationPolicy)
	controller.mu.RUnlock()
}

// TestQualityControllerUpdateInterval tests setting update interval
func TestQualityControllerUpdateInterval(t *testing.T) {
	controller := NewQualityController()

	controller.SetUpdateInterval(5 * time.Second)

	controller.mu.RLock()
	assert.Equal(t, 5*time.Second, controller.updateInterval)
	controller.mu.RUnlock()
}

// TestQualityControllerCalculateOptimalQuality tests quality calculation
func TestQualityControllerCalculateOptimalQuality(t *testing.T) {
	controller := NewQualityController()

	tests := []struct {
		name          string
		connQuality   livekit.ConnectionQuality
		expectedRange []livekit.VideoQuality
	}{
		{
			name:        "Excellent connection",
			connQuality: livekit.ConnectionQuality_EXCELLENT,
			expectedRange: []livekit.VideoQuality{
				livekit.VideoQuality_HIGH,
			},
		},
		{
			name:        "Good connection",
			connQuality: livekit.ConnectionQuality_GOOD,
			expectedRange: []livekit.VideoQuality{
				livekit.VideoQuality_HIGH,
				livekit.VideoQuality_MEDIUM,
			},
		},
		{
			name:        "Poor connection",
			connQuality: livekit.ConnectionQuality_POOR,
			expectedRange: []livekit.VideoQuality{
				livekit.VideoQuality_LOW,
				livekit.VideoQuality_MEDIUM,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quality := controller.CalculateOptimalQuality(tt.connQuality, nil)
			assert.Contains(t, tt.expectedRange, quality)
		})
	}
}

// TestQualityControllerQualityTransitions tests quality increase/decrease
func TestQualityControllerQualityTransitions(t *testing.T) {
	controller := NewQualityController()

	// Test decrease
	assert.Equal(t, livekit.VideoQuality_MEDIUM, controller.decreaseQuality(livekit.VideoQuality_HIGH))
	assert.Equal(t, livekit.VideoQuality_LOW, controller.decreaseQuality(livekit.VideoQuality_MEDIUM))
	assert.Equal(t, livekit.VideoQuality_LOW, controller.decreaseQuality(livekit.VideoQuality_LOW)) // Returns LOW as minimum
	assert.Equal(t, livekit.VideoQuality_LOW, controller.decreaseQuality(livekit.VideoQuality_OFF)) // Returns LOW as default

	// Test increase
	assert.Equal(t, livekit.VideoQuality_HIGH, controller.increaseQuality(livekit.VideoQuality_OFF)) // Returns HIGH as default
	assert.Equal(t, livekit.VideoQuality_MEDIUM, controller.increaseQuality(livekit.VideoQuality_LOW))
	assert.Equal(t, livekit.VideoQuality_HIGH, controller.increaseQuality(livekit.VideoQuality_MEDIUM))
	assert.Equal(t, livekit.VideoQuality_HIGH, controller.increaseQuality(livekit.VideoQuality_HIGH))
}

// TestDefaultQualityAdaptationPolicy tests default policy creation
func TestDefaultQualityAdaptationPolicy(t *testing.T) {
	policy := DefaultQualityAdaptationPolicy()

	assert.Equal(t, livekit.VideoQuality_LOW, policy.MinQuality)
	assert.Equal(t, livekit.VideoQuality_HIGH, policy.MaxQuality)
	assert.Equal(t, 0.02, policy.LossThresholdUp)      // 2% loss
	assert.Equal(t, 0.01, policy.LossThresholdDown)    // 1% loss
	assert.Equal(t, 0.8, policy.BitrateThresholdUp)    // 80% usage
	assert.Equal(t, 0.95, policy.BitrateThresholdDown) // 95% usage
}

// TestQualityControllerEnableAdaptation tests enabling/disabling adaptation
func TestQualityControllerEnableAdaptation(t *testing.T) {
	controller := NewQualityController()

	// Create a monitor
	controller.mu.Lock()
	monitor := &TrackQualityMonitor{
		AdaptationEnabled: true,
		Stats: &TrackQualityStats{
			CurrentQuality: livekit.VideoQuality_HIGH,
		},
	}
	controller.monitors["track-1"] = monitor
	controller.mu.Unlock()

	// Disable adaptation
	controller.EnableAdaptation("track-1", false)
	assert.False(t, monitor.AdaptationEnabled)

	// Enable adaptation
	controller.EnableAdaptation("track-1", true)
	assert.True(t, monitor.AdaptationEnabled)

	// Test with non-existent track (should not panic)
	controller.EnableAdaptation("nonexistent", false)
}

// TestQualityControllerGetTrackStats tests getting track statistics
func TestQualityControllerGetTrackStats(t *testing.T) {
	controller := NewQualityController()

	// Create a monitor with stats
	controller.mu.Lock()
	monitor := &TrackQualityMonitor{
		Stats: &TrackQualityStats{
			CurrentQuality:  livekit.VideoQuality_HIGH,
			PacketsReceived: 100,
			PacketsLost:     5,
			Bitrate:         1000000,
			Jitter:          0.05,
		},
	}
	controller.monitors["track-1"] = monitor
	controller.mu.Unlock()

	// Get stats
	stats, exists := controller.GetTrackStats("track-1")
	assert.True(t, exists)
	assert.NotNil(t, stats)
	assert.Equal(t, uint64(100), stats.PacketsReceived)
	assert.Equal(t, uint32(5), stats.PacketsLost)

	// Get stats for non-existent track
	stats, exists = controller.GetTrackStats("nonexistent")
	assert.False(t, exists)
	assert.Nil(t, stats)
}

// TestQualityControllerGetQualityHistory tests getting quality change history
func TestQualityControllerGetQualityHistory(t *testing.T) {
	controller := NewQualityController()

	// Create a monitor with history
	controller.mu.Lock()
	monitor := &TrackQualityMonitor{
		Stats: &TrackQualityStats{
			CurrentQuality: livekit.VideoQuality_LOW,
		},
		QualityHistory: []QualityChange{
			{
				FromQuality: livekit.VideoQuality_HIGH,
				ToQuality:   livekit.VideoQuality_MEDIUM,
				Timestamp:   time.Now().Add(-2 * time.Minute),
				Reason:      "High packet loss",
			},
			{
				FromQuality: livekit.VideoQuality_MEDIUM,
				ToQuality:   livekit.VideoQuality_LOW,
				Timestamp:   time.Now().Add(-1 * time.Minute),
				Reason:      "Network congestion",
			},
		},
	}
	controller.monitors["track-1"] = monitor
	controller.mu.Unlock()

	// Get history
	history := controller.GetQualityHistory("track-1")
	assert.Len(t, history, 2)
	assert.Equal(t, livekit.VideoQuality_HIGH, history[0].FromQuality)
	assert.Equal(t, livekit.VideoQuality_MEDIUM, history[0].ToQuality)
	assert.Equal(t, livekit.VideoQuality_MEDIUM, history[1].FromQuality)
	assert.Equal(t, livekit.VideoQuality_LOW, history[1].ToQuality)

	// Get history for non-existent track
	history = controller.GetQualityHistory("nonexistent")
	assert.Nil(t, history)
}

// TestQualityControllerApplySettings tests applying quality settings
func TestQualityControllerApplySettings(t *testing.T) {
	// This test requires LiveKit server - use TestQualityControllerWithLiveKit instead
	t.Log("Use TestQualityControllerWithLiveKit for quality controller tests with LiveKit server")

	// Test nil handling directly
	controller := NewQualityController()
	err := controller.ApplyQualitySettings(nil, livekit.VideoQuality_HIGH)
	assert.Error(t, err) // Should return error for nil track

	err = controller.ApplyDimensionSettings(nil, 1920, 1080)
	assert.Error(t, err) // Should return error for nil track

	err = controller.ApplyFrameRateSettings(nil, 30.0)
	assert.Error(t, err) // Should return error for nil track
}

// TestQualityControllerConcurrency tests concurrent operations on controller
func TestQualityControllerConcurrency(t *testing.T) {
	controller := NewQualityController()

	var wg sync.WaitGroup
	numGoroutines := 50

	// Concurrent monitor operations
	wg.Add(numGoroutines * 3)
	for i := 0; i < numGoroutines; i++ {
		// Add monitors
		go func(id int) {
			defer wg.Done()
			controller.mu.Lock()
			trackID := fmt.Sprintf("track-%d", id)
			controller.monitors[trackID] = &TrackQualityMonitor{
				Stats: &TrackQualityStats{
					CurrentQuality: livekit.VideoQuality_HIGH,
				},
				AdaptationEnabled: true,
			}
			controller.mu.Unlock()
		}(i)

		// Update stats
		go func(id int) {
			defer wg.Done()
			trackID := fmt.Sprintf("track-%d", id)
			time.Sleep(time.Millisecond) // Give time for monitor creation
			stats, exists := controller.GetTrackStats(trackID)
			if exists && stats != nil {
				// Just read the stats
				_ = stats.PacketsReceived
			}
		}(i)

		// Get history
		go func(id int) {
			defer wg.Done()
			trackID := fmt.Sprintf("track-%d", id)
			controller.GetQualityHistory(trackID)
		}(i)
	}

	wg.Wait()

	// Verify no panic occurred
	assert.NotNil(t, controller)
	controller.mu.RLock()
	assert.GreaterOrEqual(t, len(controller.monitors), 0)
	controller.mu.RUnlock()
}
