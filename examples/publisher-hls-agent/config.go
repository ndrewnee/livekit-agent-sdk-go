package main

import (
	"log"
	"os"
	"strconv"
)

// Config captures runtime configuration for the publisher HLS agent.
// All fields are populated from environment variables via loadConfig.
type Config struct {
	// LiveKitURL is the WebSocket URL of the LiveKit server.
	// Environment variable: LIVEKIT_URL (default: ws://localhost:7880)
	LiveKitURL string

	// APIKey is the LiveKit API key for authentication.
	// Environment variable: LIVEKIT_API_KEY (required)
	APIKey string

	// APISecret is the LiveKit API secret for authentication.
	// Environment variable: LIVEKIT_API_SECRET (required)
	APISecret string

	// OutputDir is the local directory where recordings are saved.
	// Environment variable: OUTPUT_DIR (default: publisher-hls-output)
	OutputDir string

	// AutoActivate determines whether recording starts automatically
	// when video and audio tracks are ready.
	// Environment variable: AUTO_ACTIVATE_RECORDING (default: false)
	AutoActivate bool

	// SegmentDurationSecs is the target duration of each HLS segment in seconds.
	// Environment variable: HLS_SEGMENT_DURATION (default: 2)
	SegmentDurationSecs int

	// MaxPlaylistEntries limits the number of segments in the HLS playlist.
	// 0 means unlimited (keeps all segments).
	// Environment variable: HLS_MAX_SEGMENTS (default: 0)
	MaxPlaylistEntries int

	// KeepOpus determines whether to preserve Opus audio without transcoding.
	// When true, HLS output contains H.264 + Opus (no AAC transcoding).
	// When false, audio is transcoded from Opus to AAC.
	// Note: Not all HLS players support Opus audio in MPEG-TS containers.
	// Environment variable: KEEP_OPUS (default: false)
	KeepOpus bool

	// AgentName is the name used for job matching and identification.
	// Environment variable: AGENT_NAME (default: publisher-hls-recorder)
	AgentName string

	// S3RealTimeUpload enables real-time upload of HLS segments to S3 as they're created.
	// When enabled, segments are uploaded during recording instead of after completion.
	// Requires S3 configuration (S3.Enabled() must return true).
	// Environment variable: S3_REALTIME_UPLOAD (default: false)
	S3RealTimeUpload bool

	// S3 contains S3-compatible storage configuration for uploads.
	S3 S3Config
}

// loadConfig reads all configuration from environment variables.
// It uses getEnv/mustGetEnv helpers to populate the Config struct.
// Panics via log.Fatalf if required variables (API key/secret) are missing.
func loadConfig() *Config {
	return &Config{
		LiveKitURL:          getEnv("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:              mustGetEnv("LIVEKIT_API_KEY"),
		APISecret:           mustGetEnv("LIVEKIT_API_SECRET"),
		OutputDir:           getEnv("OUTPUT_DIR", "publisher-hls-output"),
		AutoActivate:        getEnvBool("AUTO_ACTIVATE_RECORDING", false),
		SegmentDurationSecs: getEnvInt("HLS_SEGMENT_DURATION", 2),
		MaxPlaylistEntries:  getEnvInt("HLS_MAX_SEGMENTS", 0),
		KeepOpus:            getEnvBool("KEEP_OPUS", false),
		AgentName:           getEnv("AGENT_NAME", "publisher-hls-recorder"),
		S3RealTimeUpload:    getEnvBool("S3_REALTIME_UPLOAD", false),
		S3: S3Config{
			Endpoint:       getEnv("S3_ENDPOINT", ""),
			Bucket:         getEnv("S3_BUCKET", ""),
			Region:         getEnv("S3_REGION", "us-east-1"),
			AccessKey:      getEnv("S3_ACCESS_KEY", ""),
			SecretKey:      getEnv("S3_SECRET_KEY", ""),
			SessionToken:   getEnv("S3_SESSION_TOKEN", ""),
			Prefix:         getEnv("S3_PREFIX", ""),
			UseSSL:         getEnvBool("S3_USE_SSL", false),
			ForcePathStyle: getEnvBool("S3_FORCE_PATH_STYLE", true),
			ACL:            getEnv("S3_OBJECT_ACL", ""),
		},
	}
}

// getEnv retrieves an environment variable or returns the default value if unset.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// mustGetEnv retrieves a required environment variable.
// Calls log.Fatalf if the variable is not set.
func mustGetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("environment variable %s is required", key)
	}
	return value
}

// getEnvInt retrieves an integer environment variable or returns the default value.
// Returns defaultValue if the variable is unset or cannot be parsed as an integer.
func getEnvInt(key string, defaultValue int) int {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.Atoi(str); err == nil {
			return value
		}
	}
	return defaultValue
}

// getEnvBool retrieves a boolean environment variable or returns the default value.
// Returns defaultValue if the variable is unset or cannot be parsed as a boolean.
// Accepts: 1, t, T, TRUE, true, True, 0, f, F, FALSE, false, False.
func getEnvBool(key string, defaultValue bool) bool {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.ParseBool(str); err == nil {
			return value
		}
	}
	return defaultValue
}

// S3Config contains configuration for S3-compatible storage uploads.
// Recordings are uploaded to S3 at the end of each session if Enabled() returns true.
type S3Config struct {
	// Endpoint is the S3 endpoint URL (e.g., s3.amazonaws.com, localhost:9000).
	// Environment variable: S3_ENDPOINT
	Endpoint string

	// Bucket is the S3 bucket name where recordings are stored.
	// Environment variable: S3_BUCKET
	Bucket string

	// Region is the S3 region (e.g., us-east-1, us-west-2).
	// Environment variable: S3_REGION (default: us-east-1)
	Region string

	// AccessKey is the S3 access key ID for authentication.
	// Environment variable: S3_ACCESS_KEY
	AccessKey string

	// SecretKey is the S3 secret access key for authentication.
	// Environment variable: S3_SECRET_KEY
	SecretKey string

	// SessionToken is an optional STS session token.
	// Environment variable: S3_SESSION_TOKEN
	SessionToken string

	// Prefix is prepended to all S3 object keys (e.g., "recordings/").
	// Environment variable: S3_PREFIX
	Prefix string

	// UseSSL determines whether to use HTTPS for S3 connections.
	// Environment variable: S3_USE_SSL (default: false)
	UseSSL bool

	// ForcePathStyle uses path-style URLs (http://endpoint/bucket/key)
	// instead of virtual-hosted-style (http://bucket.endpoint/key).
	// Required for MinIO and some S3-compatible services.
	// Environment variable: S3_FORCE_PATH_STYLE (default: true)
	ForcePathStyle bool

	// ACL is the canned ACL applied to uploaded objects (e.g., "public-read").
	// Environment variable: S3_OBJECT_ACL
	ACL string
}

// Enabled returns true if S3 upload is configured with minimum required fields.
// Requires Endpoint, Bucket, AccessKey, and SecretKey to be non-empty.
func (s S3Config) Enabled() bool {
	return s.Endpoint != "" && s.Bucket != "" && s.AccessKey != "" && s.SecretKey != ""
}
