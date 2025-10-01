package pipeline

import (
	"sync"
	"sync/atomic"
	"time"
)

// GapMetrics tracks gap detection and filling statistics
// Per REQUIREMENTS.md: Zero data loss with proper gap filling
type GapMetrics struct {
	mu sync.RWMutex

	// Video gaps
	videoGapsDetected   uint64
	videoGapsFilled     uint64
	videoGapsUnfilled   uint64
	totalVideoGapMs     uint64
	maxVideoGapMs       uint64
	lastVideoGapTime    time.Time

	// Audio gaps
	audioGapsDetected   uint64
	audioGapsFilled     uint64
	audioGapsUnfilled   uint64
	totalAudioGapMs     uint64
	maxAudioGapMs       uint64
	lastAudioGapTime    time.Time

	// Filling methods used
	videoFillMethods map[string]uint64
	audioFillMethods map[string]uint64
}

// NewGapMetrics creates a new gap metrics tracker
func NewGapMetrics() *GapMetrics {
	return &GapMetrics{
		videoFillMethods: make(map[string]uint64),
		audioFillMethods: make(map[string]uint64),
	}
}

// DetectVideoGap records a detected video gap
func (m *GapMetrics) DetectVideoGap(gapDurationMs int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	atomic.AddUint64(&m.videoGapsDetected, 1)
	atomic.AddUint64(&m.totalVideoGapMs, uint64(gapDurationMs))
	m.lastVideoGapTime = time.Now()

	if uint64(gapDurationMs) > m.maxVideoGapMs {
		m.maxVideoGapMs = uint64(gapDurationMs)
	}
}

// DetectAudioGap records a detected audio gap
func (m *GapMetrics) DetectAudioGap(gapDurationMs int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	atomic.AddUint64(&m.audioGapsDetected, 1)
	atomic.AddUint64(&m.totalAudioGapMs, uint64(gapDurationMs))
	m.lastAudioGapTime = time.Now()

	if uint64(gapDurationMs) > m.maxAudioGapMs {
		m.maxAudioGapMs = uint64(gapDurationMs)
	}
}

// FillVideoGap records a filled video gap
func (m *GapMetrics) FillVideoGap(method string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	atomic.AddUint64(&m.videoGapsFilled, 1)
	m.videoFillMethods[method]++
}

// FillAudioGap records a filled audio gap
func (m *GapMetrics) FillAudioGap(method string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	atomic.AddUint64(&m.audioGapsFilled, 1)
	m.audioFillMethods[method]++
}

// MarkVideoGapUnfilled marks a video gap as unfilled
func (m *GapMetrics) MarkVideoGapUnfilled() {
	atomic.AddUint64(&m.videoGapsUnfilled, 1)
}

// MarkAudioGapUnfilled marks an audio gap as unfilled
func (m *GapMetrics) MarkAudioGapUnfilled() {
	atomic.AddUint64(&m.audioGapsUnfilled, 1)
}

// GetVideoGapRate returns the video gap rate as percentage of gaps
func (m *GapMetrics) GetVideoGapRate(totalPackets uint64) float64 {
	if totalPackets == 0 {
		return 0
	}
	gaps := atomic.LoadUint64(&m.videoGapsDetected)
	return float64(gaps) / float64(totalPackets) * 100.0
}

// GetAudioGapRate returns the audio gap rate as percentage of gaps
func (m *GapMetrics) GetAudioGapRate(totalPackets uint64) float64 {
	if totalPackets == 0 {
		return 0
	}
	gaps := atomic.LoadUint64(&m.audioGapsDetected)
	return float64(gaps) / float64(totalPackets) * 100.0
}

// GetVideoFillRate returns the percentage of video gaps that were filled
func (m *GapMetrics) GetVideoFillRate() float64 {
	detected := atomic.LoadUint64(&m.videoGapsDetected)
	if detected == 0 {
		return 100.0 // No gaps, perfect fill rate
	}
	filled := atomic.LoadUint64(&m.videoGapsFilled)
	return float64(filled) / float64(detected) * 100.0
}

// GetAudioFillRate returns the percentage of audio gaps that were filled
func (m *GapMetrics) GetAudioFillRate() float64 {
	detected := atomic.LoadUint64(&m.audioGapsDetected)
	if detected == 0 {
		return 100.0 // No gaps, perfect fill rate
	}
	filled := atomic.LoadUint64(&m.audioGapsFilled)
	return float64(filled) / float64(detected) * 100.0
}

// GetStats returns comprehensive gap statistics
func (m *GapMetrics) GetStats() GapStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Copy fill methods maps
	videoMethods := make(map[string]uint64)
	for k, v := range m.videoFillMethods {
		videoMethods[k] = v
	}

	audioMethods := make(map[string]uint64)
	for k, v := range m.audioFillMethods {
		audioMethods[k] = v
	}

	return GapStats{
		VideoGapsDetected:   atomic.LoadUint64(&m.videoGapsDetected),
		VideoGapsFilled:     atomic.LoadUint64(&m.videoGapsFilled),
		VideoGapsUnfilled:   atomic.LoadUint64(&m.videoGapsUnfilled),
		TotalVideoGapMs:     atomic.LoadUint64(&m.totalVideoGapMs),
		MaxVideoGapMs:       m.maxVideoGapMs,
		LastVideoGapTime:    m.lastVideoGapTime,
		VideoFillRate:       m.GetVideoFillRate(),
		VideoFillMethods:    videoMethods,

		AudioGapsDetected:   atomic.LoadUint64(&m.audioGapsDetected),
		AudioGapsFilled:     atomic.LoadUint64(&m.audioGapsFilled),
		AudioGapsUnfilled:   atomic.LoadUint64(&m.audioGapsUnfilled),
		TotalAudioGapMs:     atomic.LoadUint64(&m.totalAudioGapMs),
		MaxAudioGapMs:       m.maxAudioGapMs,
		LastAudioGapTime:    m.lastAudioGapTime,
		AudioFillRate:       m.GetAudioFillRate(),
		AudioFillMethods:    audioMethods,
	}
}

// IsHealthy checks if gap metrics indicate healthy operation
// Per REQUIREMENTS.md: Zero data loss with proper gap filling
func (m *GapMetrics) IsHealthy() bool {
	// Check video fill rate
	videoFillRate := m.GetVideoFillRate()
	if videoFillRate < 99.0 { // Allow 1% unfilled gaps
		return false
	}

	// Check audio fill rate
	audioFillRate := m.GetAudioFillRate()
	if audioFillRate < 99.0 { // Allow 1% unfilled gaps
		return false
	}

	// Check for excessive gaps
	videoGaps := atomic.LoadUint64(&m.videoGapsDetected)
	audioGaps := atomic.LoadUint64(&m.audioGapsDetected)

	// If we have too many gaps, something is wrong
	if videoGaps > 1000 || audioGaps > 1000 {
		return false
	}

	return true
}

// Reset resets all gap metrics
func (m *GapMetrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	atomic.StoreUint64(&m.videoGapsDetected, 0)
	atomic.StoreUint64(&m.videoGapsFilled, 0)
	atomic.StoreUint64(&m.videoGapsUnfilled, 0)
	atomic.StoreUint64(&m.totalVideoGapMs, 0)
	m.maxVideoGapMs = 0
	m.lastVideoGapTime = time.Time{}

	atomic.StoreUint64(&m.audioGapsDetected, 0)
	atomic.StoreUint64(&m.audioGapsFilled, 0)
	atomic.StoreUint64(&m.audioGapsUnfilled, 0)
	atomic.StoreUint64(&m.totalAudioGapMs, 0)
	m.maxAudioGapMs = 0
	m.lastAudioGapTime = time.Time{}

	m.videoFillMethods = make(map[string]uint64)
	m.audioFillMethods = make(map[string]uint64)
}

// GapStats holds comprehensive gap statistics
type GapStats struct {
	// Video gaps
	VideoGapsDetected   uint64            `json:"video_gaps_detected"`
	VideoGapsFilled     uint64            `json:"video_gaps_filled"`
	VideoGapsUnfilled   uint64            `json:"video_gaps_unfilled"`
	TotalVideoGapMs     uint64            `json:"total_video_gap_ms"`
	MaxVideoGapMs       uint64            `json:"max_video_gap_ms"`
	LastVideoGapTime    time.Time         `json:"last_video_gap_time"`
	VideoFillRate       float64           `json:"video_fill_rate_percent"`
	VideoFillMethods    map[string]uint64 `json:"video_fill_methods"`

	// Audio gaps
	AudioGapsDetected   uint64            `json:"audio_gaps_detected"`
	AudioGapsFilled     uint64            `json:"audio_gaps_filled"`
	AudioGapsUnfilled   uint64            `json:"audio_gaps_unfilled"`
	TotalAudioGapMs     uint64            `json:"total_audio_gap_ms"`
	MaxAudioGapMs       uint64            `json:"max_audio_gap_ms"`
	LastAudioGapTime    time.Time         `json:"last_audio_gap_time"`
	AudioFillRate       float64           `json:"audio_fill_rate_percent"`
	AudioFillMethods    map[string]uint64 `json:"audio_fill_methods"`
}