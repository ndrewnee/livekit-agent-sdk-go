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
	VideoPort          int
	AudioPort          int
	OutputDir          string
	SegmentDuration    int
	JitterBufferMs     int
	EnableScreenshots  bool
	ScreenshotInterval int
	AudioMode          AudioMode
	AACBitrate         int
	MP3Bitrate         int
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
	if c.VideoPort <= 0 || c.VideoPort > 65535 {
		return fmt.Errorf("invalid video port: %d", c.VideoPort)
	}
	if c.AudioPort <= 0 || c.AudioPort > 65535 {
		return fmt.Errorf("invalid audio port: %d", c.AudioPort)
	}
	if c.VideoPort == c.AudioPort {
		return fmt.Errorf("video and audio ports must be different")
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
		VideoPort:          5004,
		AudioPort:          5006,
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