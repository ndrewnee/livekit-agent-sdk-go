package main

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Config captures runtime configuration for the publisher HLS agent.
type Config struct {
	LiveKitURL          string
	APIKey              string
	APISecret           string
	OutputDir           string
	AutoActivate        bool
	SegmentDurationSecs int
	MaxPlaylistEntries  int
	AgentName           string
	S3                  S3Config
}

func loadConfig() *Config {
	return &Config{
		LiveKitURL:          getEnv("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:              mustGetEnv("LIVEKIT_API_KEY"),
		APISecret:           mustGetEnv("LIVEKIT_API_SECRET"),
		OutputDir:           getEnv("OUTPUT_DIR", "publisher-hls-output"),
		AutoActivate:        getEnvBool("AUTO_ACTIVATE_RECORDING", false),
		SegmentDurationSecs: getEnvInt("HLS_SEGMENT_DURATION", 2),
		MaxPlaylistEntries:  getEnvInt("HLS_MAX_SEGMENTS", 0),
		AgentName:           getEnv("AGENT_NAME", "publisher-hls-recorder"),
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

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func mustGetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("environment variable %s is required", key)
	}
	return value
}

func getEnvInt(key string, defaultValue int) int {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.Atoi(str); err == nil {
			return value
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if str := os.Getenv(key); str != "" {
		if value, err := strconv.ParseBool(str); err == nil {
			return value
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if str := os.Getenv(key); str != "" {
		if value, err := time.ParseDuration(str); err == nil {
			return value
		}
	}
	return defaultValue
}

type S3Config struct {
	Endpoint       string
	Bucket         string
	Region         string
	AccessKey      string
	SecretKey      string
	SessionToken   string
	Prefix         string
	UseSSL         bool
	ForcePathStyle bool
	ACL            string
}

func (s S3Config) Enabled() bool {
	return s.Endpoint != "" && s.Bucket != "" && s.AccessKey != "" && s.SecretKey != ""
}
