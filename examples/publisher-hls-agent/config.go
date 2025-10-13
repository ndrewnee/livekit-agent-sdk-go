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
