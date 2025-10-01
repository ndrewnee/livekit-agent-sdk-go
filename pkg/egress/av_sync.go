package egress

import (
	"fmt"
	"sync"
	"time"
)

// AVSyncMonitor tracks audio/video synchronization
// Required by REQUIREMENTS.md: < 50ms A/V synchronization drift
type AVSyncMonitor struct {
	mu sync.RWMutex

	// Last presentation timestamps
	lastVideoPTS uint64
	lastAudioPTS uint64

	// Track times when PTS were received
	lastVideoTime time.Time
	lastAudioTime time.Time

	// Track if we have a complete pair of timestamps
	hasVideoPTS bool
	hasAudioPTS bool

	// Statistics
	maxDriftMs     int64
	currentDriftMs int64
	driftSamples   []int64
	maxSamples     int

	// Thresholds
	maxAllowedDriftMs int64
}

// NewAVSyncMonitor creates a new A/V sync monitor
func NewAVSyncMonitor() *AVSyncMonitor {
	return &AVSyncMonitor{
		maxAllowedDriftMs: 50, // REQUIREMENTS.md: < 50ms drift
		maxSamples:        100, // Keep last 100 samples for averaging
		driftSamples:      make([]int64, 0, 100),
	}
}

// UpdateVideoPTS updates the video presentation timestamp
func (m *AVSyncMonitor) UpdateVideoPTS(pts uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastVideoPTS = pts
	m.lastVideoTime = time.Now()
	m.hasVideoPTS = true

	// Only calculate drift if we have both timestamps
	if m.hasAudioPTS {
		m.calculateDrift()
		// Reset flags for next pair
		m.hasVideoPTS = false
		m.hasAudioPTS = false
	}
}

// UpdateAudioPTS updates the audio presentation timestamp
func (m *AVSyncMonitor) UpdateAudioPTS(pts uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastAudioPTS = pts
	m.lastAudioTime = time.Now()
	m.hasAudioPTS = true

	// Only calculate drift if we have both timestamps
	if m.hasVideoPTS {
		m.calculateDrift()
		// Reset flags for next pair
		m.hasVideoPTS = false
		m.hasAudioPTS = false
	}
}

// calculateDrift calculates the current A/V drift in milliseconds
func (m *AVSyncMonitor) calculateDrift() {
	// Need both timestamps to calculate drift
	if m.lastVideoPTS == 0 || m.lastAudioPTS == 0 {
		return
	}

	// Calculate absolute drift in nanoseconds (PTS is in nanoseconds in GStreamer)
	var drift int64
	if m.lastVideoPTS > m.lastAudioPTS {
		drift = int64(m.lastVideoPTS - m.lastAudioPTS)
	} else {
		drift = int64(m.lastAudioPTS - m.lastVideoPTS)
	}

	// Convert to milliseconds (absolute value)
	driftMs := drift / 1000000
	if driftMs < 0 {
		driftMs = -driftMs
	}

	// Update current drift
	m.currentDriftMs = driftMs

	// Track maximum drift
	if driftMs > m.maxDriftMs {
		m.maxDriftMs = driftMs
	}

	// Add to samples
	m.driftSamples = append(m.driftSamples, driftMs)
	if len(m.driftSamples) > m.maxSamples {
		m.driftSamples = m.driftSamples[1:]
	}
}

// GetCurrentDrift returns the current A/V drift in milliseconds
func (m *AVSyncMonitor) GetCurrentDrift() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentDriftMs
}

// GetMaxDrift returns the maximum observed drift in milliseconds
func (m *AVSyncMonitor) GetMaxDrift() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxDriftMs
}

// GetAverageDrift returns the average drift over recent samples
func (m *AVSyncMonitor) GetAverageDrift() float64 {
	if len(m.driftSamples) == 0 {
		return 0
	}

	var sum int64
	for _, d := range m.driftSamples {
		sum += d
	}

	return float64(sum) / float64(len(m.driftSamples))
}

// IsInSync returns true if A/V sync is within acceptable limits
func (m *AVSyncMonitor) IsInSync() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentDriftMs <= m.maxAllowedDriftMs
}

// GetSyncStatus returns a detailed sync status
func (m *AVSyncMonitor) GetSyncStatus() AVSyncStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Calculate average inline to avoid deadlock
	var avgDrift float64
	if len(m.driftSamples) > 0 {
		var sum int64
		for _, d := range m.driftSamples {
			sum += d
		}
		avgDrift = float64(sum) / float64(len(m.driftSamples))
	}

	status := AVSyncStatus{
		CurrentDriftMs: m.currentDriftMs,
		MaxDriftMs:     m.maxDriftMs,
		AverageDriftMs: avgDrift,
		IsInSync:       m.currentDriftMs <= m.maxAllowedDriftMs,
		LastVideoPTS:   m.lastVideoPTS,
		LastAudioPTS:   m.lastAudioPTS,
		SampleCount:    len(m.driftSamples),
	}

	// Determine status level
	if m.currentDriftMs <= m.maxAllowedDriftMs {
		status.Status = "OK"
		status.Message = fmt.Sprintf("A/V sync within limits (<%dms)", m.maxAllowedDriftMs)
	} else if m.currentDriftMs <= m.maxAllowedDriftMs*2 {
		status.Status = "WARNING"
		status.Message = fmt.Sprintf("A/V drift slightly high: %dms", m.currentDriftMs)
	} else {
		status.Status = "CRITICAL"
		status.Message = fmt.Sprintf("A/V drift exceeds limits: %dms (max allowed: %dms)",
			m.currentDriftMs, m.maxAllowedDriftMs)
	}

	return status
}

// Reset resets the monitor state
func (m *AVSyncMonitor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastVideoPTS = 0
	m.lastAudioPTS = 0
	m.currentDriftMs = 0
	m.maxDriftMs = 0
	m.driftSamples = m.driftSamples[:0]
}

// AVSyncStatus represents the current A/V sync status
type AVSyncStatus struct {
	CurrentDriftMs int64   `json:"current_drift_ms"`
	MaxDriftMs     int64   `json:"max_drift_ms"`
	AverageDriftMs float64 `json:"average_drift_ms"`
	IsInSync       bool    `json:"is_in_sync"`
	LastVideoPTS   uint64  `json:"last_video_pts"`
	LastAudioPTS   uint64  `json:"last_audio_pts"`
	SampleCount    int     `json:"sample_count"`
	Status         string  `json:"status"`   // OK, WARNING, CRITICAL
	Message        string  `json:"message"`
}