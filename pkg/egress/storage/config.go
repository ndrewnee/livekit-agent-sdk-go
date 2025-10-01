package storage

import (
	"fmt"
	"time"
)

// Config holds the complete storage configuration for egress recordings
// Implements storage requirements from PLAN.md Milestone 3
type Config struct {
	// Storage type: "local", "s3", "gcs", "hybrid"
	Type StorageType `yaml:"type" json:"type"`

	// Local storage configuration
	Local LocalConfig `yaml:"local" json:"local"`

	// S3 storage configuration
	S3 S3Config `yaml:"s3" json:"s3"`

	// GCS (Google Cloud Storage) configuration
	GCS GCSConfig `yaml:"gcs" json:"gcs"`

	// Upload configuration
	Upload UploadConfig `yaml:"upload" json:"upload"`

	// Retention configuration
	Retention RetentionConfig `yaml:"retention" json:"retention"`

	// HLS-specific configuration
	HLS HLSConfig `yaml:"hls" json:"hls"`

	// Screenshot configuration
	Screenshot ScreenshotConfig `yaml:"screenshot" json:"screenshot"`
}

// StorageType represents the type of storage backend
type StorageType string

const (
	StorageTypeLocal  StorageType = "local"
	StorageTypeS3     StorageType = "s3"
	StorageTypeGCS    StorageType = "gcs"
	StorageTypeHybrid StorageType = "hybrid" // Local + Cloud
)

// LocalConfig holds local storage configuration
type LocalConfig struct {
	// Base path for local storage
	Path string `yaml:"path" json:"path"`

	// Maximum storage size in bytes (0 = unlimited)
	MaxSize int64 `yaml:"max_size" json:"max_size"`

	// Enable local buffering during cloud outages
	BufferOnFailure bool `yaml:"buffer_on_failure" json:"buffer_on_failure"`

	// Buffer directory (separate from main storage)
	BufferPath string `yaml:"buffer_path" json:"buffer_path"`

	// Maximum buffer size in bytes
	MaxBufferSize int64 `yaml:"max_buffer_size" json:"max_buffer_size"`
}

// S3Config holds Amazon S3 configuration
// SECURITY: Never hardcode credentials. Use environment variables or IAM roles
type S3Config struct {
	// S3 endpoint (e.g., "s3.amazonaws.com" or MinIO endpoint)
	Endpoint string `yaml:"endpoint" json:"endpoint"`

	// S3 bucket name
	Bucket string `yaml:"bucket" json:"bucket"`

	// AWS region
	Region string `yaml:"region" json:"region"`

	// AWS access key ID (can use IAM role if empty)
	AccessKeyID string `yaml:"access_key_id" json:"access_key_id"`

	// AWS secret access key
	// SECURITY: Load from environment (AWS_SECRET_ACCESS_KEY) or secrets manager
	SecretAccessKey string `yaml:"secret_access_key" json:"-"` // Exclude from JSON marshaling

	// Session token for temporary credentials
	SessionToken string `yaml:"session_token" json:"-"` // Exclude from JSON marshaling

	// Path prefix in bucket
	Prefix string `yaml:"prefix" json:"prefix"`

	// Use path-style addressing (for MinIO)
	ForcePathStyle bool `yaml:"force_path_style" json:"force_path_style"`

	// Enable SSL/TLS
	UseSSL bool `yaml:"use_ssl" json:"use_ssl"`

	// Storage class (STANDARD, REDUCED_REDUNDANCY, GLACIER, etc.)
	StorageClass string `yaml:"storage_class" json:"storage_class"`

	// Enable server-side encryption
	SSEEnabled bool `yaml:"sse_enabled" json:"sse_enabled"`

	// KMS key ID for SSE-KMS
	KMSKeyID string `yaml:"kms_key_id" json:"kms_key_id"`

	// ACL for uploaded objects
	ACL string `yaml:"acl" json:"acl"`
}

// GCSConfig holds Google Cloud Storage configuration
type GCSConfig struct {
	// GCS bucket name
	Bucket string `yaml:"bucket" json:"bucket"`

	// Path prefix in bucket
	Prefix string `yaml:"prefix" json:"prefix"`

	// Service account credentials file path
	CredentialsFile string `yaml:"credentials_file" json:"credentials_file"`

	// Service account credentials JSON (alternative to file)
	CredentialsJSON string `yaml:"credentials_json" json:"credentials_json"`

	// Project ID
	ProjectID string `yaml:"project_id" json:"project_id"`

	// Storage class (STANDARD, NEARLINE, COLDLINE, ARCHIVE)
	StorageClass string `yaml:"storage_class" json:"storage_class"`

	// Enable customer-managed encryption keys
	KMSKeyName string `yaml:"kms_key_name" json:"kms_key_name"`
}

// UploadConfig holds upload strategy configuration
type UploadConfig struct {
	// Enable concurrent uploads
	Concurrent bool `yaml:"concurrent" json:"concurrent"`

	// Maximum concurrent uploads
	MaxConcurrent int `yaml:"max_concurrent" json:"max_concurrent"`

	// Upload timeout per segment
	UploadTimeout time.Duration `yaml:"upload_timeout" json:"upload_timeout"`

	// Retry configuration
	Retry RetryConfig `yaml:"retry" json:"retry"`

	// Circuit breaker configuration
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker" json:"circuit_breaker"`

	// Upload segments as they're created
	UploadOnCreate bool `yaml:"upload_on_create" json:"upload_on_create"`

	// Batch upload configuration
	Batch BatchConfig `yaml:"batch" json:"batch"`

	// Compression before upload
	Compression CompressionConfig `yaml:"compression" json:"compression"`
}

// RetryConfig holds retry configuration
type RetryConfig struct {
	// Enable retries
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Maximum retry attempts
	MaxAttempts int `yaml:"max_attempts" json:"max_attempts"`

	// Initial backoff duration
	InitialBackoff time.Duration `yaml:"initial_backoff" json:"initial_backoff"`

	// Maximum backoff duration
	MaxBackoff time.Duration `yaml:"max_backoff" json:"max_backoff"`

	// Backoff multiplier
	Multiplier float64 `yaml:"multiplier" json:"multiplier"`

	// Add jitter to backoff
	Jitter bool `yaml:"jitter" json:"jitter"`
}

// CircuitBreakerConfig holds circuit breaker configuration
type CircuitBreakerConfig struct {
	// Enable circuit breaker
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Failure threshold to open circuit
	FailureThreshold int `yaml:"failure_threshold" json:"failure_threshold"`

	// Success threshold to close circuit
	SuccessThreshold int `yaml:"success_threshold" json:"success_threshold"`

	// Timeout in open state before trying half-open
	OpenTimeout time.Duration `yaml:"open_timeout" json:"open_timeout"`

	// Maximum requests in half-open state
	HalfOpenRequests int `yaml:"half_open_requests" json:"half_open_requests"`
}

// BatchConfig holds batch upload configuration
type BatchConfig struct {
	// Enable batch uploads
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Maximum batch size
	MaxSize int `yaml:"max_size" json:"max_size"`

	// Maximum wait time before flushing batch
	MaxWait time.Duration `yaml:"max_wait" json:"max_wait"`
}

// CompressionConfig holds compression configuration
type CompressionConfig struct {
	// Enable compression
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Compression algorithm: "gzip", "zstd", "lz4"
	Algorithm string `yaml:"algorithm" json:"algorithm"`

	// Compression level (algorithm-specific)
	Level int `yaml:"level" json:"level"`
}

// RetentionConfig holds retention policy configuration
type RetentionConfig struct {
	// Enable retention policies
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Local retention in hours (0 = forever)
	LocalHours int `yaml:"local_hours" json:"local_hours"`

	// Cloud retention in hours (0 = forever)
	CloudHours int `yaml:"cloud_hours" json:"cloud_hours"`

	// Delete from local after successful upload
	DeleteAfterUpload bool `yaml:"delete_after_upload" json:"delete_after_upload"`

	// Cleanup interval
	CleanupInterval time.Duration `yaml:"cleanup_interval" json:"cleanup_interval"`

	// Keep minimum number of segments
	MinSegments int `yaml:"min_segments" json:"min_segments"`

	// Keep minimum duration in seconds
	MinDuration int `yaml:"min_duration" json:"min_duration"`
}

// HLSConfig holds HLS-specific configuration
type HLSConfig struct {
	// Segment duration in seconds
	SegmentDuration int `yaml:"segment_duration" json:"segment_duration"`

	// Playlist type: "event" or "vod"
	PlaylistType string `yaml:"playlist_type" json:"playlist_type"`

	// Maximum number of segments (0 = unlimited)
	MaxSegments int `yaml:"max_segments" json:"max_segments"`

	// Target duration for playlist
	TargetDuration int `yaml:"target_duration" json:"target_duration"`

	// Generate master playlist for multi-quality
	MasterPlaylist bool `yaml:"master_playlist" json:"master_playlist"`

	// Enable byte-range segments
	ByteRange bool `yaml:"byte_range" json:"byte_range"`

	// Enable encryption
	Encryption HLSEncryption `yaml:"encryption" json:"encryption"`
}

// HLSEncryption holds HLS encryption configuration
type HLSEncryption struct {
	// Enable encryption
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Encryption method: "AES-128", "SAMPLE-AES"
	Method string `yaml:"method" json:"method"`

	// Key URI
	KeyURI string `yaml:"key_uri" json:"key_uri"`

	// Key rotation interval in segments
	KeyRotationInterval int `yaml:"key_rotation_interval" json:"key_rotation_interval"`
}

// ScreenshotConfig holds screenshot extraction configuration
type ScreenshotConfig struct {
	// Enable screenshot extraction
	Enabled bool `yaml:"enabled" json:"enabled"`

	// Screenshot interval in seconds
	Interval int `yaml:"interval" json:"interval"`

	// Output format: "jpeg", "png", "webp"
	Format string `yaml:"format" json:"format"`

	// Quality (1-100, applies to JPEG/WebP)
	Quality int `yaml:"quality" json:"quality"`

	// Resolution scaling (1.0 = original)
	Scale float64 `yaml:"scale" json:"scale"`

	// Maximum screenshots per recording
	MaxCount int `yaml:"max_count" json:"max_count"`

	// Upload screenshots to cloud storage
	Upload bool `yaml:"upload" json:"upload"`

	// Generate thumbnail playlist
	ThumbnailPlaylist bool `yaml:"thumbnail_playlist" json:"thumbnail_playlist"`
}

// DefaultConfig returns a default storage configuration
func DefaultConfig() *Config {
	return &Config{
		Type: StorageTypeLocal,

		Local: LocalConfig{
			Path:            "/tmp/recordings",
			MaxSize:         0, // Unlimited
			BufferOnFailure: true,
			BufferPath:      "/tmp/recordings/buffer",
			MaxBufferSize:   1024 * 1024 * 1024, // 1GB
		},

		S3: S3Config{
			Endpoint:       "s3.amazonaws.com",
			Region:         "us-east-1",
			UseSSL:         true,
			StorageClass:   "STANDARD",
			ACL:            "private",
			ForcePathStyle: false,
		},

		Upload: UploadConfig{
			Concurrent:     true,
			MaxConcurrent:  5,
			UploadTimeout:  30 * time.Second,
			UploadOnCreate: true,

			Retry: RetryConfig{
				Enabled:        true,
				MaxAttempts:    5,
				InitialBackoff: 1 * time.Second,
				MaxBackoff:     30 * time.Second,
				Multiplier:     2.0,
				Jitter:         true,
			},

			CircuitBreaker: CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: 5,
				SuccessThreshold: 2,
				OpenTimeout:      60 * time.Second,
				HalfOpenRequests: 3,
			},

			Batch: BatchConfig{
				Enabled: false,
				MaxSize: 10,
				MaxWait: 5 * time.Second,
			},

			Compression: CompressionConfig{
				Enabled:   false,
				Algorithm: "gzip",
				Level:     6,
			},
		},

		Retention: RetentionConfig{
			Enabled:           true,
			LocalHours:        168, // 7 days
			CloudHours:        0,   // Forever
			DeleteAfterUpload: false,
			CleanupInterval:   1 * time.Hour,
			MinSegments:       10,
			MinDuration:       60, // 1 minute
		},

		HLS: HLSConfig{
			SegmentDuration: 4,
			PlaylistType:    "event",
			MaxSegments:     0, // Unlimited
			TargetDuration:  4,
			MasterPlaylist:  false,
			ByteRange:       false,

			Encryption: HLSEncryption{
				Enabled:             false,
				Method:              "AES-128",
				KeyRotationInterval: 10,
			},
		},

		Screenshot: ScreenshotConfig{
			Enabled:           false,
			Interval:          10, // Every 10 seconds
			Format:            "jpeg",
			Quality:           85,
			Scale:             1.0,
			MaxCount:          100,
			Upload:            true,
			ThumbnailPlaylist: false,
		},
	}
}

// Validate validates the storage configuration
func (c *Config) Validate() error {
	// Validate storage type
	switch c.Type {
	case StorageTypeLocal, StorageTypeS3, StorageTypeGCS, StorageTypeHybrid:
		// Valid
	default:
		return fmt.Errorf("invalid storage type: %s", c.Type)
	}

	// Validate local config if used
	if c.Type == StorageTypeLocal || c.Type == StorageTypeHybrid {
		if c.Local.Path == "" {
			return fmt.Errorf("local storage path not specified")
		}
	}

	// Validate S3 config if used
	if c.Type == StorageTypeS3 || (c.Type == StorageTypeHybrid && c.S3.Bucket != "") {
		if c.S3.Bucket == "" {
			return fmt.Errorf("S3 bucket not specified")
		}
		if c.S3.Region == "" {
			c.S3.Region = "us-east-1"
		}
	}

	// Validate GCS config if used
	if c.Type == StorageTypeGCS {
		if c.GCS.Bucket == "" {
			return fmt.Errorf("GCS bucket not specified")
		}
	}

	// Validate upload config
	if c.Upload.MaxConcurrent <= 0 {
		c.Upload.MaxConcurrent = 5
	}
	if c.Upload.UploadTimeout <= 0 {
		c.Upload.UploadTimeout = 30 * time.Second
	}

	// Validate retry config
	if c.Upload.Retry.Enabled {
		if c.Upload.Retry.MaxAttempts <= 0 {
			c.Upload.Retry.MaxAttempts = 5
		}
		if c.Upload.Retry.InitialBackoff <= 0 {
			c.Upload.Retry.InitialBackoff = 1 * time.Second
		}
		if c.Upload.Retry.Multiplier <= 1 {
			c.Upload.Retry.Multiplier = 2.0
		}
	}

	// Validate HLS config
	if c.HLS.SegmentDuration <= 0 {
		c.HLS.SegmentDuration = 4
	}
	if c.HLS.PlaylistType != "vod" && c.HLS.PlaylistType != "event" {
		c.HLS.PlaylistType = "event"
	}

	// Validate screenshot config
	if c.Screenshot.Enabled {
		if c.Screenshot.Interval <= 0 {
			c.Screenshot.Interval = 10
		}
		if c.Screenshot.Quality <= 0 || c.Screenshot.Quality > 100 {
			c.Screenshot.Quality = 85
		}
		if c.Screenshot.Format != "jpeg" && c.Screenshot.Format != "png" && c.Screenshot.Format != "webp" {
			c.Screenshot.Format = "jpeg"
		}
	}

	return nil
}