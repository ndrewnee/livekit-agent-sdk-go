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

	// MaxMixerParticipants is the maximum number of participants whose audio
	// can be mixed simultaneously in the web player. This is used for generating
	// the audio manifest metadata.
	// Environment variable: MAX_MIXER_PARTICIPANTS (default: 10)
	MaxMixerParticipants int

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

	// E2EEPassphrase is the passphrase used to derive the E2EE encryption key.
	// When set, the agent will decrypt incoming E2EE-encrypted audio and video tracks.
	// The passphrase must match the one used by the publishing client.
	// Environment variable: E2EE_PASSPHRASE (default: empty, E2EE disabled)
	E2EEPassphrase string

	// ThumbnailsEnabled enables periodic thumbnail extraction from the recorded video.
	// Environment variable: THUMBNAILS_ENABLED (default: false)
	ThumbnailsEnabled bool

	// ThumbnailIntervalSecs is the interval in seconds between thumbnails.
	// Environment variable: THUMBNAIL_INTERVAL_SECS (default: 5)
	ThumbnailIntervalSecs int

	// ThumbnailWidth is the target thumbnail width in pixels.
	// Environment variable: THUMBNAIL_WIDTH (default: 640)
	ThumbnailWidth int

	// ThumbnailHeight is the target thumbnail height in pixels.
	// Environment variable: THUMBNAIL_HEIGHT (default: 320)
	ThumbnailHeight int

	// ThumbnailFormat is the output image format for thumbnails (jpg, jpeg, png, webp).
	// Environment variable: THUMBNAIL_FORMAT (default: jpg)
	ThumbnailFormat string

	// FacesEnabled enables face detection and extraction from thumbnail source frames.
	// This runs before thumbnail downscaling, improving detection accuracy.
	// Environment variable: FACES_ENABLED (default: false)
	FacesEnabled bool

	// FaceCascadePath is the path to an OpenCV CascadeClassifier XML file.
	// When empty, the agent attempts to locate OpenCV's installed haarcascade file.
	// Environment variable: FACE_CASCADE_PATH (default: empty)
	FaceCascadePath string

	// FaceNormalizedWidth is the width (pixels) of saved face crops.
	// Environment variable: FACE_NORMALIZED_WIDTH (default: 160)
	FaceNormalizedWidth int

	// FaceNormalizedHeight is the height (pixels) of saved face crops.
	// Environment variable: FACE_NORMALIZED_HEIGHT (default: 160)
	FaceNormalizedHeight int

	// FaceMaxPerThumbnail limits how many detected faces are processed per thumbnail frame.
	// 0 means unlimited.
	// Environment variable: FACE_MAX_PER_THUMBNAIL (default: 5)
	FaceMaxPerThumbnail int

	// FaceMaxUnique limits how many unique faces are saved per recording session.
	// 0 means unlimited.
	// Environment variable: FACE_MAX_UNIQUE (default: 50)
	FaceMaxUnique int

	// FaceUniquenessThreshold is the maximum dHash Hamming distance considered "same".
	// Lower values keep more unique faces; higher values de-duplicate more aggressively.
	// Environment variable: FACE_UNIQUENESS_THRESHOLD (default: 8)
	FaceUniquenessThreshold int

	// FacePaddingRatio expands the detected face bounding box by this ratio on each side.
	// Example: 0.2 adds ~20% padding on each side.
	// Environment variable: FACE_PADDING_RATIO (default: 0.2)
	FacePaddingRatio float64

	// FaceMinSize is the minimum detected face size (pixels) for CascadeClassifier.
	// Environment variable: FACE_MIN_SIZE (default: 40)
	FaceMinSize int

	// FaceScaleFactor is the CascadeClassifier scale factor (typical range 1.05-1.2).
	// Environment variable: FACE_SCALE_FACTOR (default: 1.1)
	FaceScaleFactor float64

	// FaceMinNeighbors is the CascadeClassifier minNeighbors parameter (typical range 3-6).
	// Environment variable: FACE_MIN_NEIGHBORS (default: 5)
	FaceMinNeighbors int

	// FaceFormat is the output image format for extracted faces (jpg, jpeg, png, webp).
	// Environment variable: FACE_FORMAT (default: jpg)
	FaceFormat string

	// FaceDetector selects the face detector implementation.
	// Supported values: "yunet", "haar".
	// Environment variable: FACE_DETECTOR (default: yunet)
	FaceDetector string

	// FaceYunetModelPath is the path to the YuNet face detection ONNX model.
	// When empty, the agent attempts to download/cache a default model.
	// Environment variable: FACE_YUNET_MODEL (default: empty)
	FaceYunetModelPath string

	// FaceYunetScoreThreshold is the minimum YuNet detection score to accept a face.
	// Environment variable: FACE_YUNET_SCORE_THRESHOLD (default: 0.9)
	FaceYunetScoreThreshold float64

	// FaceYunetNMSThreshold is the YuNet non-maximum suppression threshold.
	// Environment variable: FACE_YUNET_NMS_THRESHOLD (default: 0.3)
	FaceYunetNMSThreshold float64

	// FaceYunetTopK is the number of YuNet boxes preserved before NMS.
	// Environment variable: FACE_YUNET_TOPK (default: 5000)
	FaceYunetTopK int

	// FaceSFaceModelPath is the path to the SFace face recognition ONNX model.
	// When empty, the agent attempts to download/cache a default model.
	// Environment variable: FACE_SFACE_MODEL (default: empty)
	FaceSFaceModelPath string

	// FaceRecognitionThreshold is the cosine similarity threshold (SFace) above which
	// two faces are considered the same identity.
	// Environment variable: FACE_RECOGNITION_THRESHOLD (default: 0.363)
	FaceRecognitionThreshold float64

	// FaceGroupDedupThreshold is the cosine similarity threshold (SFace) above which
	// a face crop is considered too similar to an already-saved face within the same
	// identity group and will be skipped.
	// Set to 0 to disable within-group deduplication.
	// Environment variable: FACE_GROUP_DEDUP_THRESHOLD (default: 0.9)
	FaceGroupDedupThreshold float64
}

// loadConfig reads all configuration from environment variables.
// It uses getEnv/mustGetEnv helpers to populate the Config struct.
// Panics via log.Fatalf if required variables (API key/secret) are missing.
func loadConfig() *Config {
	return &Config{
		LiveKitURL:              getEnv("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:                  mustGetEnv("LIVEKIT_API_KEY"),
		APISecret:               mustGetEnv("LIVEKIT_API_SECRET"),
		OutputDir:               getEnv("OUTPUT_DIR", "publisher-hls-output"),
		AutoActivate:            getEnvBool("AUTO_ACTIVATE_RECORDING", false),
		SegmentDurationSecs:     getEnvInt("HLS_SEGMENT_DURATION", 2),
		MaxPlaylistEntries:      getEnvInt("HLS_MAX_SEGMENTS", 0),
		MaxMixerParticipants:    getEnvInt("MAX_MIXER_PARTICIPANTS", 10),
		AgentName:               getEnv("AGENT_NAME", "publisher-hls-recorder"),
		S3RealTimeUpload:        getEnvBool("S3_REALTIME_UPLOAD", false),
		E2EEPassphrase:          getEnv("E2EE_PASSPHRASE", ""),
		ThumbnailsEnabled:       getEnvBool("THUMBNAILS_ENABLED", false),
		ThumbnailIntervalSecs:   getEnvInt("THUMBNAIL_INTERVAL_SECS", 5),
		ThumbnailWidth:          getEnvInt("THUMBNAIL_WIDTH", 640),
		ThumbnailHeight:         getEnvInt("THUMBNAIL_HEIGHT", 320),
		ThumbnailFormat:         getEnv("THUMBNAIL_FORMAT", "jpg"),
		FacesEnabled:            getEnvBool("FACES_ENABLED", false),
		FaceCascadePath:         getEnv("FACE_CASCADE_PATH", ""),
		FaceNormalizedWidth:     getEnvInt("FACE_NORMALIZED_WIDTH", 160),
		FaceNormalizedHeight:    getEnvInt("FACE_NORMALIZED_HEIGHT", 160),
		FaceMaxPerThumbnail:     getEnvInt("FACE_MAX_PER_THUMBNAIL", 5),
		FaceMaxUnique:           getEnvInt("FACE_MAX_UNIQUE", 50),
		FaceUniquenessThreshold: getEnvInt("FACE_UNIQUENESS_THRESHOLD", 8),
		FacePaddingRatio:        getEnvFloat64("FACE_PADDING_RATIO", 0.2),
		FaceMinSize:             getEnvInt("FACE_MIN_SIZE", 40),
		FaceScaleFactor:         getEnvFloat64("FACE_SCALE_FACTOR", 1.1),
		FaceMinNeighbors:        getEnvInt("FACE_MIN_NEIGHBORS", 5),
		FaceFormat:              getEnv("FACE_FORMAT", "jpg"),
		FaceDetector:            getEnv("FACE_DETECTOR", "yunet"),
		FaceYunetModelPath:      getEnv("FACE_YUNET_MODEL", ""),
		FaceYunetScoreThreshold: getEnvFloat64("FACE_YUNET_SCORE_THRESHOLD", 0.9),
		FaceYunetNMSThreshold:   getEnvFloat64("FACE_YUNET_NMS_THRESHOLD", 0.3),
		FaceYunetTopK:           getEnvInt("FACE_YUNET_TOPK", 5000),
		FaceSFaceModelPath:      getEnv("FACE_SFACE_MODEL", ""),
		FaceRecognitionThreshold: getEnvFloat64("FACE_RECOGNITION_THRESHOLD",
			0.363),
		FaceGroupDedupThreshold: getEnvFloat64("FACE_GROUP_DEDUP_THRESHOLD", 0.8),
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

// getEnvFloat64 retrieves a float64 environment variable or returns the default value.
// Returns defaultValue if the variable is unset or cannot be parsed as a float64.
func getEnvFloat64(key string, defaultValue float64) float64 {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.ParseFloat(str, 64); err == nil {
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

// E2EEEnabled returns true if E2EE decryption is configured with a passphrase.
func (c *Config) E2EEEnabled() bool {
	return c.E2EEPassphrase != ""
}
