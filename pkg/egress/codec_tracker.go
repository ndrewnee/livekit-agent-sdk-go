package egress

import (
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"
)

// CodecTracker tracks and enforces codec consistency during a recording session
// Per REQUIREMENTS.md: No codec changes mid-stream allowed
type CodecTracker struct {
	mu            sync.RWMutex
	videoCodec    *webrtc.RTPCodecParameters
	audioCodec    *webrtc.RTPCodecParameters
	videoLocked   bool
	audioLocked   bool
	sessionID     string
	rejectCount   int64
}

// NewCodecTracker creates a new codec tracker for a session
func NewCodecTracker(sessionID string) *CodecTracker {
	return &CodecTracker{
		sessionID: sessionID,
	}
}

// ValidateVideoCodec validates and locks the video codec for the session
func (ct *CodecTracker) ValidateVideoCodec(codec webrtc.RTPCodecParameters) error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// First codec locks the session
	if !ct.videoLocked {
		// Check if codec is supported
		if !isVideoCodecSupported(codec.MimeType) {
			return fmt.Errorf("unsupported video codec: %s", codec.MimeType)
		}

		ct.videoCodec = &codec
		ct.videoLocked = true
		return nil
	}

	// Check if codec matches the locked one
	if ct.videoCodec.MimeType != codec.MimeType ||
		ct.videoCodec.PayloadType != codec.PayloadType {
		ct.rejectCount++
		return fmt.Errorf("video codec change detected (was: %s, got: %s) - not allowed mid-stream",
			ct.videoCodec.MimeType, codec.MimeType)
	}

	return nil
}

// ValidateAudioCodec validates and locks the audio codec for the session
func (ct *CodecTracker) ValidateAudioCodec(codec webrtc.RTPCodecParameters) error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	// First codec locks the session
	if !ct.audioLocked {
		// Check if codec is supported
		if !isAudioCodecSupported(codec.MimeType) {
			return fmt.Errorf("unsupported audio codec: %s", codec.MimeType)
		}

		ct.audioCodec = &codec
		ct.audioLocked = true
		return nil
	}

	// Check if codec matches the locked one
	if ct.audioCodec.MimeType != codec.MimeType ||
		ct.audioCodec.PayloadType != codec.PayloadType {
		ct.rejectCount++
		return fmt.Errorf("audio codec change detected (was: %s, got: %s) - not allowed mid-stream",
			ct.audioCodec.MimeType, codec.MimeType)
	}

	return nil
}

// GetVideoCodec returns the locked video codec
func (ct *CodecTracker) GetVideoCodec() *webrtc.RTPCodecParameters {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.videoCodec
}

// GetAudioCodec returns the locked audio codec
func (ct *CodecTracker) GetAudioCodec() *webrtc.RTPCodecParameters {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.audioCodec
}

// GetRejectCount returns the number of codec changes rejected
func (ct *CodecTracker) GetRejectCount() int64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.rejectCount
}

// Reset clears the codec locks (use only when starting a new recording)
func (ct *CodecTracker) Reset() {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.videoCodec = nil
	ct.audioCodec = nil
	ct.videoLocked = false
	ct.audioLocked = false
	ct.rejectCount = 0
}

// isVideoCodecSupported checks if a video codec is supported for zero-transcode
func isVideoCodecSupported(mimeType string) bool {
	switch mimeType {
	case "video/H264":
		return true
	default:
		return false
	}
}

// isAudioCodecSupported checks if an audio codec is supported for zero-transcode
func isAudioCodecSupported(mimeType string) bool {
	switch mimeType {
	case "audio/opus":
		return true
	case "audio/mpeg": // MP3
		return true
	default:
		return false
	}
}

// CodecInfo provides information about the locked codecs
type CodecInfo struct {
	VideoCodec  string `json:"video_codec,omitempty"`
	AudioCodec  string `json:"audio_codec,omitempty"`
	RejectCount int64  `json:"reject_count"`
}

// GetCodecInfo returns information about the current codec state
func (ct *CodecTracker) GetCodecInfo() CodecInfo {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	info := CodecInfo{
		RejectCount: ct.rejectCount,
	}

	if ct.videoCodec != nil {
		info.VideoCodec = ct.videoCodec.MimeType
	}

	if ct.audioCodec != nil {
		info.AudioCodec = ct.audioCodec.MimeType
	}

	return info
}