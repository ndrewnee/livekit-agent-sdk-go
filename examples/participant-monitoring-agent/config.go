package main

import (
	"time"
)

type Config struct {
	LiveKitURL                 string
	APIKey                     string
	APISecret                  string
	ConnectionQualityThreshold float64
	InactivityTimeout          time.Duration
	SpeakingThreshold          float64
	EnableNotifications        bool
}
