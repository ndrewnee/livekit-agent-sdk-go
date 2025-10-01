package pipeline

import (
	"fmt"
	"time"
)

// State represents the state of the GStreamer pipeline
type State int

const (
	StateStopped State = iota
	StatePlaying
	StatePaused
)

// Config holds pipeline configuration
type Config struct {
	// Output configuration
	OutputDir          string
	SegmentDuration    int

	// Pipeline configuration
	JitterBufferMs     int
	StateChangeTimeout time.Duration // Timeout for pipeline state changes (0 = use default)
	AllowAsyncStart    bool          // If true, Start() returns immediately without waiting for state

	// Audio configuration
	AudioMode          AudioMode
	AACBitrate         int
	MP3Bitrate         int

	// Screenshot configuration
	EnableScreenshots  bool
	ScreenshotInterval int

	// Live source configuration
	IsLiveSource       bool // If true, pipeline can operate in PAUSED state (waiting for data)

	// Deprecated: UDP ports are no longer needed with direct appsrc injection
	VideoPort          int `deprecated:"true"`
	AudioPort          int `deprecated:"true"`
}

// AudioMode defines audio processing mode
//
// IMPORTANT: HLS with MPEG-TS muxer requires AAC or MP3 audio.
// Opus audio (from LiveKit) CANNOT be used directly in MPEG-TS.
// Therefore, ALL modes require transcoding for HLS to work.
//
// The "zero-transcode" goal is fundamentally incompatible with HLS+MPEG-TS.
// Alternative: Use fMP4-HLS which supports Opus, but that's a format change.
type AudioMode string

const (
	// AudioPassThrough - Transcode Opus to AAC at 192kbps (CPU: ~8-12%)
	// Despite the name, this mode MUST transcode for HLS compatibility
	AudioPassThrough  AudioMode = "passthrough"

	// AudioTranscodeAAC - Transcode Opus to AAC at custom bitrate (CPU: ~8-12%)
	AudioTranscodeAAC AudioMode = "transcode_aac"

	// AudioTranscodeMP3 - Transcode Opus to MP3 at custom bitrate (CPU: ~10-15%)
	AudioTranscodeMP3 AudioMode = "transcode_mp3"
)

// IsCompliant checks if the configuration meets SPECS.md requirements
// Returns true only if the configuration will meet <5% CPU target
//
// REALITY: HLS with MPEG-TS requires audio transcoding, so NO mode is truly compliant
// with the <5% CPU target. All modes use 8-15% CPU due to AAC/MP3 encoding.
func (c *Config) IsCompliant() bool {
	// All modes require transcoding for HLS, so none meet the <5% CPU target
	return false
}

// GetComplianceWarning returns a warning message for non-compliant configurations
func (c *Config) GetComplianceWarning() string {
	return "WARNING: HLS with MPEG-TS requires AAC/MP3 audio. All modes transcode Opus, using 8-15% CPU. This exceeds the 5% target but is required for HLS to function."
}

// ValidateConfig validates pipeline configuration
func ValidateConfig(c *Config) error {
	if c.OutputDir == "" {
		return fmt.Errorf("output directory is required")
	}
	if c.SegmentDuration <= 0 {
		return fmt.Errorf("segment duration must be positive")
	}
	if c.JitterBufferMs < 0 {
		return fmt.Errorf("jitter buffer must be non-negative")
	}

	// Warn about non-compliant configuration
	if !c.IsCompliant() {
		// Note: We don't return an error, just log warning
		// The user may have legitimate reasons to use transcoding
		fmt.Printf("COMPLIANCE WARNING: %s\n", c.GetComplianceWarning())
	}

	return nil
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		OutputDir:          "/tmp/recordings",
		SegmentDuration:    4,
		JitterBufferMs:     200,
		StateChangeTimeout: 10 * time.Second, // Default 10 second timeout
		AllowAsyncStart:    false,            // By default, wait for state confirmation
		IsLiveSource:       true,             // Default to live source behavior
		EnableScreenshots:  false,
		ScreenshotInterval: 5,
		AudioMode:          AudioPassThrough,
		AACBitrate:         192,
		MP3Bitrate:         192,
	}
}