package main

import (
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// MediaPublisher manages media publishing sessions
type MediaPublisher struct {
	audioService AudioGeneratorService
	videoService VideoGeneratorService

	mu       sync.RWMutex
	sessions map[string]*PublishingSession
}

// PublishingSession represents an active publishing session
type PublishingSession struct {
	mu               sync.RWMutex
	roomName         string
	jobID            string
	metadata         PublisherJobMetadata
	startTime        time.Time
	endTime          *time.Time
	tracks           map[string]string // type -> track ID
	volume           float32
	toneFrequency    float64
	videoPattern     string
	interactiveTrack *webrtc.TrackLocalStaticSample

	// Statistics
	audioFrameCount uint64
	videoFrameCount uint64
}

// PublishingStats contains session statistics
type PublishingStats struct {
	AudioFrameCount uint64
	VideoFrameCount uint64
	Duration        time.Duration
	TrackCount      int
}

// NewMediaPublisher creates a new media publisher
func NewMediaPublisher(audio AudioGeneratorService, video VideoGeneratorService) *MediaPublisher {
	return &MediaPublisher{
		audioService: audio,
		videoService: video,
		sessions:     make(map[string]*PublishingSession),
	}
}

// CreateSession creates a new publishing session
func (p *MediaPublisher) CreateSession(
	roomName, jobID string,
	metadata PublisherJobMetadata,
) *PublishingSession {
	session := &PublishingSession{
		roomName:      roomName,
		jobID:         jobID,
		metadata:      metadata,
		startTime:     time.Now(),
		tracks:        make(map[string]string),
		volume:        metadata.Volume,
		toneFrequency: metadata.ToneFrequency,
		videoPattern:  metadata.VideoPattern,
	}

	// Set defaults
	if session.volume == 0 {
		session.volume = 1.0 // Default volume
	}
	if session.toneFrequency == 0 {
		session.toneFrequency = 440.0 // A4 note
	}
	if session.videoPattern == "" {
		session.videoPattern = "color_bars"
	}

	p.mu.Lock()
	p.sessions[roomName] = session
	p.mu.Unlock()

	return session
}

// GetSession retrieves a session by room name
func (p *MediaPublisher) GetSession(roomName string) *PublishingSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sessions[roomName]
}

// RemoveSession removes a session
func (p *MediaPublisher) RemoveSession(roomName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, roomName)
}

// PublishingSession methods

// End marks the session as ended
func (s *PublishingSession) End() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.endTime = &now
}

// AddTrack adds a track to the session
func (s *PublishingSession) AddTrack(trackType, trackID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracks[trackType] = trackID
}

// SetVolume sets the audio volume
func (s *PublishingSession) SetVolume(volume float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volume = volume
}

// GetVolume gets the current audio volume
func (s *PublishingSession) GetVolume() float32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.volume
}

// SetToneFrequency sets the audio tone frequency
func (s *PublishingSession) SetToneFrequency(frequency float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toneFrequency = frequency
}

// GetToneFrequency gets the current tone frequency
func (s *PublishingSession) GetToneFrequency() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.toneFrequency
}

// SetVideoPattern sets the video pattern type
func (s *PublishingSession) SetVideoPattern(pattern string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.videoPattern = pattern
}

// GetVideoPattern gets the current video pattern
func (s *PublishingSession) GetVideoPattern() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.videoPattern
}

// SetInteractiveTrack sets the track for interactive mode
func (s *PublishingSession) SetInteractiveTrack(track *webrtc.TrackLocalStaticSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interactiveTrack = track
}

// GetInteractiveTrack gets the interactive mode track
func (s *PublishingSession) GetInteractiveTrack() *webrtc.TrackLocalStaticSample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.interactiveTrack
}

// IsInteractive checks if the session is in interactive mode
func (s *PublishingSession) IsInteractive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.metadata.Mode == "interactive"
}

// IncrementFrameCount increments the frame counter for a track type
func (s *PublishingSession) IncrementFrameCount(trackType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch trackType {
	case "audio":
		s.audioFrameCount++
	case "video":
		s.videoFrameCount++
	}
}

// UpdateAudioFrameCount updates the audio frame count
func (s *PublishingSession) UpdateAudioFrameCount(count uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audioFrameCount = uint64(count)
}

// UpdateVideoFrameCount updates the video frame count
func (s *PublishingSession) UpdateVideoFrameCount(count uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.videoFrameCount = uint64(count)
}

// GetStats returns session statistics
func (s *PublishingSession) GetStats() PublishingStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	duration := time.Since(s.startTime)
	if s.endTime != nil {
		duration = s.endTime.Sub(s.startTime)
	}

	return PublishingStats{
		AudioFrameCount: s.audioFrameCount,
		VideoFrameCount: s.videoFrameCount,
		Duration:        duration,
		TrackCount:      len(s.tracks),
	}
}

// EndSessionByJobID ends a session by job ID
func (p *MediaPublisher) EndSessionByJobID(jobID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Find and end the session with matching job ID
	for roomName, session := range p.sessions {
		if session.jobID == jobID {
			session.End()
			delete(p.sessions, roomName)
			break
		}
	}
}
