package pipeline

import (
	"sync"
	"sync/atomic"
	"time"
)

// AVSyncMonitor tracks audio/video synchronization
// Per REQUIREMENTS.md: < 50ms A/V synchronization drift
type AVSyncMonitor struct {
	mu sync.RWMutex

	// PTS tracking (presentation timestamps in nanoseconds)
	lastVideoPTS uint64
	lastAudioPTS uint64
	videoStartPTS uint64
	audioStartPTS uint64
	hasVideoStart bool
	hasAudioStart bool

	// Drift calculation
	currentDriftMs int64
	maxDriftMs     int64
	totalDriftMs   int64
	driftSamples   int64

	// Thresholds
	maxAllowedDriftMs int64 // 50ms per spec

	// Statistics
	measurements int64
	violations   int64
	lastCheckTime time.Time
}

// NewAVSyncMonitor creates a new A/V sync monitor
func NewAVSyncMonitor() *AVSyncMonitor {
	return &AVSyncMonitor{
		maxAllowedDriftMs: 50, // Per REQUIREMENTS.md
		lastCheckTime:     time.Now(),
	}
}

// UpdateVideoPTS updates the video presentation timestamp
func (m *AVSyncMonitor) UpdateVideoPTS(pts uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.hasVideoStart {
		m.videoStartPTS = pts
		m.hasVideoStart = true
	}

	m.lastVideoPTS = pts
	m.calculateDrift()
}

// UpdateAudioPTS updates the audio presentation timestamp
func (m *AVSyncMonitor) UpdateAudioPTS(pts uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.hasAudioStart {
		m.audioStartPTS = pts
		m.hasAudioStart = true
	}

	m.lastAudioPTS = pts
	m.calculateDrift()
}

// calculateDrift calculates the current A/V drift
func (m *AVSyncMonitor) calculateDrift() {
	if !m.hasVideoStart || !m.hasAudioStart {
		return // Need both streams to calculate drift
	}

	// Calculate relative timestamps from start
	videoRelative := int64(m.lastVideoPTS - m.videoStartPTS)
	audioRelative := int64(m.lastAudioPTS - m.audioStartPTS)

	// Calculate drift in nanoseconds, then convert to milliseconds
	driftNs := videoRelative - audioRelative
	driftMs := driftNs / 1000000

	m.currentDriftMs = driftMs

	// Track maximum drift
	if abs(driftMs) > abs(m.maxDriftMs) {
		m.maxDriftMs = driftMs
	}

	// Update statistics
	atomic.AddInt64(&m.measurements, 1)
	m.totalDriftMs += abs(driftMs)
	m.driftSamples++

	// Check for violations
	if abs(driftMs) > m.maxAllowedDriftMs {
		atomic.AddInt64(&m.violations, 1)
	}

	m.lastCheckTime = time.Now()
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

// GetAverageDrift returns the average drift in milliseconds
func (m *AVSyncMonitor) GetAverageDrift() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.driftSamples == 0 {
		return 0
	}

	return float64(m.totalDriftMs) / float64(m.driftSamples)
}

// GetViolationRate returns the percentage of measurements that violated the threshold
func (m *AVSyncMonitor) GetViolationRate() float64 {
	measurements := atomic.LoadInt64(&m.measurements)
	if measurements == 0 {
		return 0
	}

	violations := atomic.LoadInt64(&m.violations)
	return float64(violations) / float64(measurements) * 100.0
}

// IsWithinSpec returns true if the current drift is within specification
func (m *AVSyncMonitor) IsWithinSpec() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return abs(m.currentDriftMs) <= m.maxAllowedDriftMs
}

// GetStatus returns the sync status
func (m *AVSyncMonitor) GetStatus() AVSyncStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.hasVideoStart || !m.hasAudioStart {
		return AVSyncStatus{
			Status:      "INITIALIZING",
			CurrentDriftMs: 0,
			MaxDriftMs:     0,
			WithinSpec:     true,
		}
	}

	status := "OK"
	if abs(m.currentDriftMs) > m.maxAllowedDriftMs {
		status = "CRITICAL"
	} else if abs(m.currentDriftMs) > m.maxAllowedDriftMs/2 {
		status = "WARNING"
	}

	return AVSyncStatus{
		Status:         status,
		CurrentDriftMs: m.currentDriftMs,
		MaxDriftMs:     m.maxDriftMs,
		AverageDriftMs: m.GetAverageDrift(),
		ViolationRate:  m.GetViolationRate(),
		WithinSpec:     abs(m.currentDriftMs) <= m.maxAllowedDriftMs,
		Measurements:   atomic.LoadInt64(&m.measurements),
		LastCheckTime:  m.lastCheckTime,
	}
}

// Reset resets all sync measurements
func (m *AVSyncMonitor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastVideoPTS = 0
	m.lastAudioPTS = 0
	m.videoStartPTS = 0
	m.audioStartPTS = 0
	m.hasVideoStart = false
	m.hasAudioStart = false
	m.currentDriftMs = 0
	m.maxDriftMs = 0
	m.totalDriftMs = 0
	m.driftSamples = 0
	atomic.StoreInt64(&m.measurements, 0)
	atomic.StoreInt64(&m.violations, 0)
	m.lastCheckTime = time.Now()
}

// AVSyncStatus represents the current A/V sync status
type AVSyncStatus struct {
	Status         string    `json:"status"` // OK, WARNING, CRITICAL, INITIALIZING
	CurrentDriftMs int64     `json:"current_drift_ms"`
	MaxDriftMs     int64     `json:"max_drift_ms"`
	AverageDriftMs float64   `json:"average_drift_ms"`
	ViolationRate  float64   `json:"violation_rate_percent"`
	WithinSpec     bool      `json:"within_spec"`
	Measurements   int64     `json:"measurements"`
	LastCheckTime  time.Time `json:"last_check_time"`
}

// Helper function for absolute value
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}