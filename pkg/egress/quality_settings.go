package egress

import (
	"fmt"
	"sync"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// AudioQuality represents audio quality levels
type AudioQuality int32

const (
	AudioQualityLow    AudioQuality = 0
	AudioQualityMedium AudioQuality = 1
	AudioQualityHigh   AudioQuality = 2
)

// QualitySettings manages quality preferences for track subscriptions
// Implements quality control requirements from PLAN.md Milestone 2
type QualitySettings struct {
	videoQuality livekit.VideoQuality
	audioQuality AudioQuality

	// Adaptive quality settings
	enableAdaptiveStream bool
	maxVideoBitrate      uint32
	minVideoBitrate      uint32
	maxAudioBitrate      uint32

	// Preferred dimensions
	preferredWidth  uint32
	preferredHeight uint32
	preferredFPS    uint32

	mu sync.RWMutex
}

// NewQualitySettings creates new quality settings with defaults
func NewQualitySettings(videoQuality livekit.VideoQuality, audioQuality AudioQuality) *QualitySettings {
	qs := &QualitySettings{
		videoQuality:         videoQuality,
		audioQuality:         audioQuality,
		enableAdaptiveStream: false, // Disabled by default for consistent recording
		maxVideoBitrate:      5000000, // 5 Mbps default
		minVideoBitrate:      500000,  // 500 Kbps minimum
		maxAudioBitrate:      128000,  // 128 Kbps for audio
	}

	// Set preferred dimensions based on quality level
	qs.setPreferredDimensions(videoQuality)

	return qs
}

// setPreferredDimensions sets dimensions based on video quality
func (qs *QualitySettings) setPreferredDimensions(quality livekit.VideoQuality) {
	switch quality {
	case livekit.VideoQuality_HIGH:
		qs.preferredWidth = 1920
		qs.preferredHeight = 1080
		qs.preferredFPS = 30
		qs.maxVideoBitrate = 5000000 // 5 Mbps

	case livekit.VideoQuality_MEDIUM:
		qs.preferredWidth = 1280
		qs.preferredHeight = 720
		qs.preferredFPS = 30
		qs.maxVideoBitrate = 2500000 // 2.5 Mbps

	case livekit.VideoQuality_LOW:
		qs.preferredWidth = 640
		qs.preferredHeight = 480
		qs.preferredFPS = 15
		qs.maxVideoBitrate = 1000000 // 1 Mbps

	default:
		// Default to medium
		qs.preferredWidth = 1280
		qs.preferredHeight = 720
		qs.preferredFPS = 30
		qs.maxVideoBitrate = 2500000
	}

	logger.Debugw("quality dimensions set",
		"quality", quality,
		"width", qs.preferredWidth,
		"height", qs.preferredHeight,
		"fps", qs.preferredFPS,
		"maxBitrate", qs.maxVideoBitrate)
}

// ApplyToPublication applies quality settings to a track publication
func (qs *QualitySettings) ApplyToPublication(publication *lksdk.RemoteTrackPublication) error {
	qs.mu.RLock()
	defer qs.mu.RUnlock()

	if publication == nil {
		return fmt.Errorf("publication is nil")
	}

	// Set video quality if it's a video track
	if publication.Kind() == lksdk.TrackKindVideo {
		// Set quality preference
		publication.SetVideoQuality(qs.videoQuality)

		// Set dimensions if supported
		// TODO: SetDimensions doesn't exist in SDK v2
		// if qs.preferredWidth > 0 && qs.preferredHeight > 0 {
		//	publication.SetDimensions(qs.preferredWidth, qs.preferredHeight)
		// }

		logger.Debugw("applied video quality settings",
			"trackSID", publication.SID(),
			"quality", qs.videoQuality,
			"dimensions", fmt.Sprintf("%dx%d", qs.preferredWidth, qs.preferredHeight))
	}

	// Enable/disable adaptive stream
	publication.SetEnabled(!qs.enableAdaptiveStream)

	return nil
}

// SetVideoQuality updates the video quality preference
func (qs *QualitySettings) SetVideoQuality(quality livekit.VideoQuality) {
	qs.mu.Lock()
	defer qs.mu.Unlock()

	qs.videoQuality = quality
	qs.setPreferredDimensions(quality)

	logger.Infow("video quality updated", "quality", quality)
}

// SetAudioQuality updates the audio quality preference
func (qs *QualitySettings) SetAudioQuality(quality AudioQuality) {
	qs.mu.Lock()
	defer qs.mu.Unlock()

	qs.audioQuality = quality

	// Adjust audio bitrate based on quality
	switch quality {
	case AudioQualityHigh:
		qs.maxAudioBitrate = 128000 // 128 Kbps
	case AudioQualityMedium:
		qs.maxAudioBitrate = 96000 // 96 Kbps
	case AudioQualityLow:
		qs.maxAudioBitrate = 64000 // 64 Kbps
	}

	logger.Infow("audio quality updated", "quality", quality, "bitrate", qs.maxAudioBitrate)
}

// SetAdaptiveStream enables or disables adaptive streaming
func (qs *QualitySettings) SetAdaptiveStream(enabled bool) {
	qs.mu.Lock()
	defer qs.mu.Unlock()

	qs.enableAdaptiveStream = enabled
	logger.Infow("adaptive stream setting updated", "enabled", enabled)
}

// SetBitrateRange sets the bitrate range for video
func (qs *QualitySettings) SetBitrateRange(minBitrate, maxBitrate uint32) {
	qs.mu.Lock()
	defer qs.mu.Unlock()

	if minBitrate > 0 {
		qs.minVideoBitrate = minBitrate
	}
	if maxBitrate > 0 && maxBitrate >= minBitrate {
		qs.maxVideoBitrate = maxBitrate
	}

	logger.Infow("bitrate range updated",
		"min", qs.minVideoBitrate,
		"max", qs.maxVideoBitrate)
}

// SetPreferredDimensions sets custom preferred dimensions
func (qs *QualitySettings) SetPreferredDimensions(width, height, fps uint32) {
	qs.mu.Lock()
	defer qs.mu.Unlock()

	if width > 0 && height > 0 {
		qs.preferredWidth = width
		qs.preferredHeight = height
	}
	if fps > 0 {
		qs.preferredFPS = fps
	}

	logger.Infow("preferred dimensions updated",
		"width", qs.preferredWidth,
		"height", qs.preferredHeight,
		"fps", qs.preferredFPS)
}

// GetVideoQuality returns the current video quality setting
func (qs *QualitySettings) GetVideoQuality() livekit.VideoQuality {
	qs.mu.RLock()
	defer qs.mu.RUnlock()
	return qs.videoQuality
}

// GetAudioQuality returns the current audio quality setting
func (qs *QualitySettings) GetAudioQuality() AudioQuality {
	qs.mu.RLock()
	defer qs.mu.RUnlock()
	return qs.audioQuality
}

// GetDimensions returns the preferred dimensions
func (qs *QualitySettings) GetDimensions() (width, height, fps uint32) {
	qs.mu.RLock()
	defer qs.mu.RUnlock()
	return qs.preferredWidth, qs.preferredHeight, qs.preferredFPS
}

// GetBitrateRange returns the configured bitrate range
func (qs *QualitySettings) GetBitrateRange() (min, max uint32) {
	qs.mu.RLock()
	defer qs.mu.RUnlock()
	return qs.minVideoBitrate, qs.maxVideoBitrate
}

// IsAdaptiveStreamEnabled returns if adaptive streaming is enabled
func (qs *QualitySettings) IsAdaptiveStreamEnabled() bool {
	qs.mu.RLock()
	defer qs.mu.RUnlock()
	return qs.enableAdaptiveStream
}

// QualityConfig represents the full quality configuration
type QualityConfig struct {
	VideoQuality         livekit.VideoQuality `json:"video_quality"`
	AudioQuality         AudioQuality `json:"audio_quality"`
	EnableAdaptiveStream bool                 `json:"enable_adaptive_stream"`
	MaxVideoBitrate      uint32               `json:"max_video_bitrate"`
	MinVideoBitrate      uint32               `json:"min_video_bitrate"`
	MaxAudioBitrate      uint32               `json:"max_audio_bitrate"`
	PreferredWidth       uint32               `json:"preferred_width"`
	PreferredHeight      uint32               `json:"preferred_height"`
	PreferredFPS         uint32               `json:"preferred_fps"`
}

// GetConfig returns the current quality configuration
func (qs *QualitySettings) GetConfig() QualityConfig {
	qs.mu.RLock()
	defer qs.mu.RUnlock()

	return QualityConfig{
		VideoQuality:         qs.videoQuality,
		AudioQuality:         qs.audioQuality,
		EnableAdaptiveStream: qs.enableAdaptiveStream,
		MaxVideoBitrate:      qs.maxVideoBitrate,
		MinVideoBitrate:      qs.minVideoBitrate,
		MaxAudioBitrate:      qs.maxAudioBitrate,
		PreferredWidth:       qs.preferredWidth,
		PreferredHeight:      qs.preferredHeight,
		PreferredFPS:         qs.preferredFPS,
	}
}

// RecommendedBitrateForResolution returns recommended bitrate for a resolution
func RecommendedBitrateForResolution(width, height uint32) uint32 {
	pixels := width * height

	// Based on common encoding guidelines
	if pixels >= 1920*1080 { // 1080p
		return 5000000 // 5 Mbps
	} else if pixels >= 1280*720 { // 720p
		return 2500000 // 2.5 Mbps
	} else if pixels >= 854*480 { // 480p
		return 1200000 // 1.2 Mbps
	} else if pixels >= 640*360 { // 360p
		return 800000 // 800 Kbps
	}

	return 500000 // 500 Kbps minimum
}

// QualityPreset represents a predefined quality configuration
type QualityPreset string

const (
	QualityPresetUltraHigh QualityPreset = "ultra_high"
	QualityPresetHigh      QualityPreset = "high"
	QualityPresetMedium    QualityPreset = "medium"
	QualityPresetLow       QualityPreset = "low"
	QualityPresetAudioOnly QualityPreset = "audio_only"
)

// ApplyPreset applies a predefined quality preset
func (qs *QualitySettings) ApplyPreset(preset QualityPreset) {
	qs.mu.Lock()
	defer qs.mu.Unlock()

	switch preset {
	case QualityPresetUltraHigh:
		qs.videoQuality = livekit.VideoQuality_HIGH
		qs.audioQuality = AudioQualityHigh
		qs.preferredWidth = 1920
		qs.preferredHeight = 1080
		qs.preferredFPS = 60
		qs.maxVideoBitrate = 8000000 // 8 Mbps
		qs.minVideoBitrate = 2000000 // 2 Mbps

	case QualityPresetHigh:
		qs.videoQuality = livekit.VideoQuality_HIGH
		qs.audioQuality = AudioQualityHigh
		qs.setPreferredDimensions(livekit.VideoQuality_HIGH)

	case QualityPresetMedium:
		qs.videoQuality = livekit.VideoQuality_MEDIUM
		qs.audioQuality = AudioQualityMedium
		qs.setPreferredDimensions(livekit.VideoQuality_MEDIUM)

	case QualityPresetLow:
		qs.videoQuality = livekit.VideoQuality_LOW
		qs.audioQuality = AudioQualityLow
		qs.setPreferredDimensions(livekit.VideoQuality_LOW)

	case QualityPresetAudioOnly:
		qs.videoQuality = livekit.VideoQuality_OFF
		qs.audioQuality = AudioQualityHigh
		qs.maxVideoBitrate = 0
		qs.minVideoBitrate = 0
	}

	logger.Infow("quality preset applied", "preset", preset)
}