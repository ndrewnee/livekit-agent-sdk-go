package pipeline

import "fmt"

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

	// Audio configuration
	AudioMode          AudioMode
	AACBitrate         int
	MP3Bitrate         int

	// Screenshot configuration
	EnableScreenshots  bool
	ScreenshotInterval int

	// Deprecated: UDP ports are no longer needed with direct appsrc injection
	VideoPort          int `deprecated:"true"`
	AudioPort          int `deprecated:"true"`
}

// AudioMode defines audio processing mode
type AudioMode string

const (
	AudioPassThrough  AudioMode = "passthrough"
	AudioTranscodeAAC AudioMode = "transcode_aac"
	AudioTranscodeMP3 AudioMode = "transcode_mp3"
)

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
	return nil
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		OutputDir:          "/tmp/recordings",
		SegmentDuration:    4,
		JitterBufferMs:     200,
		EnableScreenshots:  false,
		ScreenshotInterval: 5,
		AudioMode:          AudioPassThrough,
		AACBitrate:         192,
		MP3Bitrate:         192,
	}
}