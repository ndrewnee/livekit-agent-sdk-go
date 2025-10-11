package main

import (
	"log"
	"os"
	"strconv"
	"time"
)

type Config struct {
	LiveKitURL        string
	APIKey            string
	APISecret         string
	OutputDir         string
	InactivityTimeout time.Duration
	EnableAudio       bool
	EnableVideo       bool
}

func loadConfig() *Config {
	return &Config{
		LiveKitURL:        getEnv("LIVEKIT_URL", "ws://localhost:7880"),
		APIKey:            mustGetEnv("LIVEKIT_API_KEY"),
		APISecret:         mustGetEnv("LIVEKIT_API_SECRET"),
		OutputDir:         getEnv("OUTPUT_DIR", "recordings"),
		InactivityTimeout: getEnvDuration("INACTIVITY_TIMEOUT", 30*time.Second),
		EnableAudio:       getEnvBool("ENABLE_AUDIO", true),
		EnableVideo:       getEnvBool("ENABLE_VIDEO", true),
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
		log.Fatalf("Environment variable %s is required", key)
	}
	return value
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if str := os.Getenv(key); str != "" {
		if value, err := time.ParseDuration(str); err == nil {
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
