package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// AudioMode defines how audio is processed
type AudioMode string

const (
	AudioPassThrough  AudioMode = "passthrough"
	AudioTranscodeAAC AudioMode = "transcode_aac"
	AudioTranscodeMP3 AudioMode = "transcode_mp3"
)

// Config holds the complete egress configuration
type Config struct {
	Output       OutputConfig       `yaml:"output"`
	Pipeline     PipelineConfig     `yaml:"pipeline"`
	Audio        AudioConfig        `yaml:"audio"`
	Screenshots  ScreenshotConfig   `yaml:"screenshots"`
	S3           S3Config           `yaml:"s3"`
	Agent        AgentConfig        `yaml:"agent"`
}

// OutputConfig defines output settings
type OutputConfig struct {
	Dir             string `yaml:"dir"`
	SegmentDuration int    `yaml:"segment_duration"`
}

// PipelineConfig defines GStreamer pipeline settings
type PipelineConfig struct {
	VideoPort      int `yaml:"video_port"`
	AudioPort      int `yaml:"audio_port"`
	JitterBufferMs int `yaml:"jitter_buffer_ms"`
}

// AudioConfig defines audio processing settings
type AudioConfig struct {
	Mode       AudioMode `yaml:"mode"`
	AACBitrate int       `yaml:"aac_bitrate"`
	MP3Bitrate int       `yaml:"mp3_bitrate"`
}

// ScreenshotConfig defines screenshot extraction settings
type ScreenshotConfig struct {
	Enabled  bool `yaml:"enabled"`
	Interval int  `yaml:"interval"`
}

// S3Config defines S3 upload settings
type S3Config struct {
	Enabled    bool   `yaml:"enabled"`
	Endpoint   string `yaml:"endpoint"`
	Bucket     string `yaml:"bucket"`
	Region     string `yaml:"region"`
	AccessKey  string `yaml:"access_key"`
	SecretKey  string `yaml:"secret_key"`
	PathPrefix string `yaml:"path_prefix"`
}

// AgentConfig defines agent settings
type AgentConfig struct {
	MaxJobs  int    `yaml:"max_jobs"`
	LogLevel string `yaml:"log_level"`
}

// Load loads configuration from a YAML file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Set defaults
	cfg.setDefaults()

	// Validate configuration
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, nil
}

// Default returns a default configuration
func Default() *Config {
	cfg := &Config{}
	cfg.setDefaults()
	return cfg
}

func (c *Config) setDefaults() {
	if c.Output.Dir == "" {
		c.Output.Dir = "/tmp/recordings"
	}
	if c.Output.SegmentDuration == 0 {
		c.Output.SegmentDuration = 4
	}
	if c.Pipeline.VideoPort == 0 {
		c.Pipeline.VideoPort = 5004
	}
	if c.Pipeline.AudioPort == 0 {
		c.Pipeline.AudioPort = 5006
	}
	if c.Pipeline.JitterBufferMs == 0 {
		c.Pipeline.JitterBufferMs = 200
	}
	if c.Audio.Mode == "" {
		c.Audio.Mode = AudioPassThrough
	}
	if c.Audio.AACBitrate == 0 {
		c.Audio.AACBitrate = 192
	}
	if c.Audio.MP3Bitrate == 0 {
		c.Audio.MP3Bitrate = 192
	}
	if c.Screenshots.Interval == 0 {
		c.Screenshots.Interval = 5
	}
	if c.Agent.MaxJobs == 0 {
		c.Agent.MaxJobs = 10
	}
	if c.Agent.LogLevel == "" {
		c.Agent.LogLevel = "info"
	}
}

func (c *Config) validate() error {
	// Validate audio mode
	switch c.Audio.Mode {
	case AudioPassThrough, AudioTranscodeAAC, AudioTranscodeMP3:
		// Valid
	default:
		return fmt.Errorf("invalid audio mode: %s", c.Audio.Mode)
	}

	// Validate ports
	if c.Pipeline.VideoPort <= 0 || c.Pipeline.VideoPort > 65535 {
		return fmt.Errorf("invalid video port: %d", c.Pipeline.VideoPort)
	}
	if c.Pipeline.AudioPort <= 0 || c.Pipeline.AudioPort > 65535 {
		return fmt.Errorf("invalid audio port: %d", c.Pipeline.AudioPort)
	}
	if c.Pipeline.VideoPort == c.Pipeline.AudioPort {
		return fmt.Errorf("video and audio ports must be different")
	}

	// Validate S3 config if enabled
	if c.S3.Enabled {
		if c.S3.Bucket == "" {
			return fmt.Errorf("S3 bucket is required when S3 is enabled")
		}
		if c.S3.AccessKey == "" || c.S3.SecretKey == "" {
			return fmt.Errorf("S3 credentials are required when S3 is enabled")
		}
	}

	return nil
}