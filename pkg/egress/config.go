package egress

import (
	"time"

	"github.com/livekit/protocol/livekit"
)

// Config holds the configuration for the egress handler and recording sessions
type Config struct {
	// Agent configuration
	MaxConcurrentSessions int `yaml:"max_concurrent_sessions"` // Maximum number of concurrent recording sessions (0 = unlimited)

	// LiveKit connection settings
	VideoQuality livekit.VideoQuality `yaml:"video_quality"` // Quality to subscribe at (HIGH, MEDIUM, LOW)

	// Pipeline configuration
	PipelineConfig PipelineConfig `yaml:"pipeline"`

	// Storage configuration
	StorageConfig StorageConfig `yaml:"storage"`

	// Recording settings
	RecordingConfig RecordingConfig `yaml:"recording"`

	// Network configuration
	NetworkConfig NetworkConfig `yaml:"network"`

	// Monitoring configuration
	MonitoringConfig MonitoringConfig `yaml:"monitoring"`
}

// PipelineConfig holds GStreamer pipeline configuration
type PipelineConfig struct {
	// Output directory for recordings
	OutputDir string `yaml:"output_dir"`

	// HLS segment settings
	SegmentDuration int `yaml:"segment_duration"` // Duration of each segment in seconds (default: 4)
	PlaylistType    string `yaml:"playlist_type"`  // HLS playlist type: "event" or "vod"
	MaxSegments     int `yaml:"max_segments"`      // Maximum number of segments to keep (0 = unlimited)
	TargetDuration  int `yaml:"target_duration"`   // Target duration for HLS playlist

	// Pipeline startup behavior
	AllowAsyncStart bool `yaml:"allow_async_start"` // If true, Start() returns immediately without waiting for pipeline PLAYING state

	// Audio/Video settings
	VideoFramerate int    `yaml:"video_framerate"` // Target video framerate (default: 30)
	AudioCodec     string `yaml:"audio_codec"`     // Audio codec: "opus", "aac", "mp3"
	VideoCodec     string `yaml:"video_codec"`     // Video codec: "h264", "vp8", "vp9"

	// Buffer settings
	VideoBufferMs int `yaml:"video_buffer_ms"` // Video buffer in milliseconds
	AudioBufferMs int `yaml:"audio_buffer_ms"` // Audio buffer in milliseconds
	JitterBufferMs int `yaml:"jitter_buffer_ms"` // Jitter buffer for RTP

	// Gap filling settings
	EnableGapFilling bool `yaml:"enable_gap_filling"` // Enable gap filling for missing packets
	VideoGapMode     string `yaml:"video_gap_mode"`   // "duplicate" or "black"
	AudioGapMode     string `yaml:"audio_gap_mode"`   // "silence" or "repeat"
}

// StorageConfig holds storage configuration
type StorageConfig struct {
	Type string `yaml:"type"` // Storage type: "local", "s3", "gcs"

	// Local storage settings
	LocalPath      string `yaml:"local_path"`       // Local directory for recordings
	RetentionHours int    `yaml:"retention_hours"`  // How long to keep recordings (0 = forever)

	// S3 configuration
	S3Config S3StorageConfig `yaml:"s3"`

	// GCS configuration
	GCSConfig GCSStorageConfig `yaml:"gcs"`

	// Upload settings
	EnableUpload    bool          `yaml:"enable_upload"`     // Enable uploading to remote storage
	UploadInterval  time.Duration `yaml:"upload_interval"`   // How often to upload segments
	RetryAttempts   int          `yaml:"retry_attempts"`     // Number of retry attempts for failed uploads
	RetryDelay      time.Duration `yaml:"retry_delay"`       // Delay between retry attempts
}

// S3StorageConfig holds S3-specific storage configuration
type S3StorageConfig struct {
	Endpoint        string `yaml:"endpoint"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	PathPrefix      string `yaml:"path_prefix"`
	UseSSL          bool   `yaml:"use_ssl"`
}

// GCSStorageConfig holds Google Cloud Storage configuration
type GCSStorageConfig struct {
	Bucket          string `yaml:"bucket"`
	PathPrefix      string `yaml:"path_prefix"`
	CredentialsFile string `yaml:"credentials_file"`
}

// RecordingConfig holds recording-specific configuration
type RecordingConfig struct {
	// Recording control
	AutoStart         bool          `yaml:"auto_start"`          // Automatically start recording when tracks are available
	MinParticipants   int          `yaml:"min_participants"`    // Minimum participants to start recording
	MaxDuration       time.Duration `yaml:"max_duration"`        // Maximum recording duration (0 = unlimited)
	IdleTimeout       time.Duration `yaml:"idle_timeout"`        // Stop recording after idle time

	// Track selection
	RecordVideo       bool   `yaml:"record_video"`        // Record video tracks
	RecordAudio       bool   `yaml:"record_audio"`        // Record audio tracks
	RecordScreenShare bool   `yaml:"record_screenshare"`  // Record screen share tracks
	PreferredVideoTrack string `yaml:"preferred_video_track"` // Preferred video track ID or participant
	PreferredAudioTrack string `yaml:"preferred_audio_track"` // Preferred audio track ID or participant

	// Screenshot settings
	EnableScreenshots  bool   `yaml:"enable_screenshots"`   // Enable periodic screenshots
	ScreenshotInterval int    `yaml:"screenshot_interval"`  // Screenshot interval in seconds
	ScreenshotFormat   string `yaml:"screenshot_format"`    // Screenshot format: "jpeg" or "png"
	ScreenshotQuality  int    `yaml:"screenshot_quality"`   // JPEG quality (1-100)
}

// NetworkConfig holds network-related configuration
type NetworkConfig struct {
	// Bind address for RTP listeners
	BindAddress string `yaml:"bind_address"` // Address to bind RTP listeners (default: 127.0.0.1)

	// RTP forwarding ports (for UDP mode)
	VideoRTPPort int `yaml:"video_rtp_port"` // UDP port for video RTP (default: 5004)
	AudioRTPPort int `yaml:"audio_rtp_port"` // UDP port for audio RTP (default: 5006)

	// Direct injection mode (recommended)
	UseDirectInjection bool `yaml:"use_direct_injection"` // Use appsrc direct injection instead of UDP

	// Connection settings
	ReconnectAttempts int           `yaml:"reconnect_attempts"` // Number of reconnection attempts
	ReconnectDelay    time.Duration `yaml:"reconnect_delay"`    // Delay between reconnection attempts

	// Quality settings
	AdaptiveBitrate bool `yaml:"adaptive_bitrate"` // Enable adaptive bitrate
	MaxBitrate      int  `yaml:"max_bitrate"`      // Maximum bitrate in kbps
	MinBitrate      int  `yaml:"min_bitrate"`      // Minimum bitrate in kbps
}

// MonitoringConfig holds monitoring and metrics configuration
type MonitoringConfig struct {
	// Metrics collection
	EnableMetrics   bool   `yaml:"enable_metrics"`    // Enable metrics collection
	MetricsInterval int    `yaml:"metrics_interval"`  // Metrics collection interval in seconds
	MetricsEndpoint string `yaml:"metrics_endpoint"`  // Prometheus metrics endpoint

	// Health checks
	EnableHealthCheck bool   `yaml:"enable_health_check"` // Enable health check endpoint
	HealthCheckPort   int    `yaml:"health_check_port"`   // Health check HTTP port
	HealthCheckPath   string `yaml:"health_check_path"`   // Health check path

	// Logging
	LogLevel string `yaml:"log_level"` // Log level: "debug", "info", "warn", "error"
	LogFile  string `yaml:"log_file"`  // Log file path (empty = stdout)
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		MaxConcurrentSessions: 10,
		VideoQuality:         livekit.VideoQuality_HIGH,

		PipelineConfig: PipelineConfig{
			OutputDir:        "/tmp/recordings",
			SegmentDuration:  4,
			PlaylistType:     "event",
			MaxSegments:      0,
			TargetDuration:   4,
			VideoFramerate:   30,
			AudioCodec:       "opus",
			VideoCodec:       "h264",
			VideoBufferMs:    2000,
			AudioBufferMs:    2000,
			JitterBufferMs:   200,
			EnableGapFilling: true,
			VideoGapMode:     "duplicate",
			AudioGapMode:     "silence",
		},

		StorageConfig: StorageConfig{
			Type:           "local",
			LocalPath:      "/tmp/recordings",
			RetentionHours: 24,
			EnableUpload:   false,
			UploadInterval: 30 * time.Second,
			RetryAttempts:  3,
			RetryDelay:     5 * time.Second,
		},

		RecordingConfig: RecordingConfig{
			AutoStart:          true,
			MinParticipants:    0,
			MaxDuration:        0,
			IdleTimeout:        5 * time.Minute,
			RecordVideo:        true,
			RecordAudio:        true,
			RecordScreenShare:  true,
			EnableScreenshots:  false,
			ScreenshotInterval: 10,
			ScreenshotFormat:   "jpeg",
			ScreenshotQuality:  85,
		},

		NetworkConfig: NetworkConfig{
			VideoRTPPort:       5004,
			AudioRTPPort:       5006,
			UseDirectInjection: true, // Recommended per SPECS.md
			ReconnectAttempts:  5,
			ReconnectDelay:     2 * time.Second,
			AdaptiveBitrate:    false,
			MaxBitrate:         5000,
			MinBitrate:         500,
		},

		MonitoringConfig: MonitoringConfig{
			EnableMetrics:     true,
			MetricsInterval:   10,
			MetricsEndpoint:   "/metrics",
			EnableHealthCheck: true,
			HealthCheckPort:   8080,
			HealthCheckPath:   "/health",
			LogLevel:         "info",
			LogFile:          "",
		},
	}
}

// Validate validates the configuration
func (c *Config) Validate() error {
	// Validate pipeline config
	if c.PipelineConfig.SegmentDuration <= 0 {
		c.PipelineConfig.SegmentDuration = 4
	}

	// Validate network config
	if !c.NetworkConfig.UseDirectInjection {
		if c.NetworkConfig.VideoRTPPort <= 0 || c.NetworkConfig.VideoRTPPort > 65535 {
			c.NetworkConfig.VideoRTPPort = 5004
		}
		if c.NetworkConfig.AudioRTPPort <= 0 || c.NetworkConfig.AudioRTPPort > 65535 {
			c.NetworkConfig.AudioRTPPort = 5006
		}
	}

	// Validate recording config
	if c.RecordingConfig.ScreenshotQuality <= 0 || c.RecordingConfig.ScreenshotQuality > 100 {
		c.RecordingConfig.ScreenshotQuality = 85
	}

	return nil
}