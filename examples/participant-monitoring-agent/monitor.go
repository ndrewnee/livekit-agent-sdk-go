package main

import (
	"context"
	"log"
	"sync"
	"time"
)

type ParticipantMonitor struct {
	mu       sync.RWMutex
	sessions map[string]*ParticipantSession
}

type ParticipantSession struct {
	mu                  sync.RWMutex
	participantIdentity string
	roomName            string
	jobID               string
	startTime           time.Time
	endTime             *time.Time
	connected           bool
	speaking            bool
	tracks              map[string]*TrackInfo
	audioLevel          float32
	frameRate           float64
	lastVideoFrame      time.Time
	connectionQuality   string
	metadata            JobMetadata

	// Statistics
	speakingDuration  time.Duration
	lastSpeakingStart time.Time
	totalFrames       uint64
	qualityChanges    []QualityChange
}

type TrackInfo struct {
	SID         string
	Type        string
	PublishedAt time.Time
	Active      bool
}

type QualityChange struct {
	From      string
	To        string
	Timestamp time.Time
	Reason    string
}

type JobMetadata struct {
	ParticipantIdentity string                 `json:"participant_identity"`
	MonitoringType      string                 `json:"monitoring_type"`
	EndOnDisconnect     bool                   `json:"end_on_disconnect"`
	NotificationPrefs   map[string]interface{} `json:"notification_preferences"`
	CustomSettings      map[string]interface{} `json:"custom_settings"`
	MonitoringFeatures  []string               `json:"monitoring_features"`
}

type ParticipantStats struct {
	Connected         bool
	Speaking          bool
	TrackCount        int
	AudioLevel        float32
	FrameRate         float64
	SpeakingDuration  time.Duration
	ConnectionQuality string
	SessionDuration   time.Duration
}

func NewParticipantMonitor() *ParticipantMonitor {
	return &ParticipantMonitor{
		sessions: make(map[string]*ParticipantSession),
	}
}

func (m *ParticipantMonitor) CreateSession(
	participantIdentity, roomName, jobID string,
	metadata JobMetadata,
) *ParticipantSession {
	session := &ParticipantSession{
		participantIdentity: participantIdentity,
		roomName:            roomName,
		jobID:               jobID,
		startTime:           time.Now(),
		tracks:              make(map[string]*TrackInfo),
		metadata:            metadata,
		qualityChanges:      make([]QualityChange, 0),
		connectionQuality:   "good", // Default quality
	}

	m.mu.Lock()
	m.sessions[participantIdentity] = session
	m.mu.Unlock()

	log.Printf("Created monitoring session for %s in room %s", participantIdentity, roomName)
	return session
}

func (m *ParticipantMonitor) EndSessionByJobID(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and end session with matching job ID
	for identity, session := range m.sessions {
		if session.jobID == jobID {
			session.End()
			delete(m.sessions, identity)
			log.Printf("Ended session for job %s", jobID)
			return
		}
	}
}

func (m *ParticipantMonitor) StartService(ctx context.Context) {
	// Periodic health check and reporting
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.performHealthChecks()
			m.generateReports()
		}
	}
}

func (m *ParticipantMonitor) performHealthChecks() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for identity, session := range m.sessions {
		stats := session.GetStats()

		// Check for connection issues
		if stats.Connected && stats.ConnectionQuality == "poor" {
			log.Printf("WARNING: Poor connection quality for participant %s", identity)
		}

		// Check for inactive video
		if session.hasVideoTrack() && time.Since(session.lastVideoFrame) > 10*time.Second {
			log.Printf("WARNING: No video frames received from %s for >10s", identity)
		}

		// Check session duration
		if stats.SessionDuration > 4*time.Hour {
			log.Printf("INFO: Long session detected for %s: %s", identity, stats.SessionDuration)
		}
	}
}

func (m *ParticipantMonitor) generateReports() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	activeCount := 0
	speakingCount := 0
	poorQualityCount := 0

	for _, session := range m.sessions {
		stats := session.GetStats()
		if stats.Connected {
			activeCount++
		}
		if stats.Speaking {
			speakingCount++
		}
		if stats.ConnectionQuality == "poor" {
			poorQualityCount++
		}
	}

	if activeCount > 0 {
		log.Printf("Monitoring Report - Active: %d, Speaking: %d, Poor Quality: %d",
			activeCount, speakingCount, poorQualityCount)
	}
}

func (m *ParticipantMonitor) PrintSummary() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	log.Println("=== Participant Monitoring Summary ===")
	for identity, session := range m.sessions {
		stats := session.GetStats()
		log.Printf("Participant: %s", identity)
		log.Printf("  Session Duration: %s", stats.SessionDuration.Round(time.Second))
		log.Printf("  Speaking Time: %s", stats.SpeakingDuration.Round(time.Second))
		log.Printf("  Track Count: %d", stats.TrackCount)
		log.Printf("  Final Quality: %s", stats.ConnectionQuality)
	}
	log.Println("=====================================")
}

// ParticipantSession methods

func (s *ParticipantSession) End() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	s.endTime = &now

	// Finalize speaking duration
	if s.speaking && !s.lastSpeakingStart.IsZero() {
		s.speakingDuration += now.Sub(s.lastSpeakingStart)
	}
}

func (s *ParticipantSession) SetConnected(connected bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = connected
}

func (s *ParticipantSession) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected
}

func (s *ParticipantSession) SetSpeaking(speaking bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if speaking && !s.speaking {
		// Started speaking
		s.speaking = true
		s.lastSpeakingStart = now
	} else if !speaking && s.speaking {
		// Stopped speaking
		s.speaking = false
		if !s.lastSpeakingStart.IsZero() {
			s.speakingDuration += now.Sub(s.lastSpeakingStart)
		}
	}
}

func (s *ParticipantSession) IsSpeaking() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.speaking
}

func (s *ParticipantSession) AddTrack(sid, trackType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tracks[sid] = &TrackInfo{
		SID:         sid,
		Type:        trackType,
		PublishedAt: time.Now(),
		Active:      true,
	}
}

func (s *ParticipantSession) RemoveTrack(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if track, exists := s.tracks[sid]; exists {
		track.Active = false
	}
}

func (s *ParticipantSession) UpdateAudioLevel(level float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audioLevel = level
}

func (s *ParticipantSession) UpdateFrameRate(fps float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frameRate = fps
	s.totalFrames++
}

func (s *ParticipantSession) UpdateVideoFrameRate(fps float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frameRate = float64(fps)
	s.totalFrames++
}

func (s *ParticipantSession) StartSpeaking() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.speaking {
		s.speaking = true
		s.lastSpeakingStart = time.Now()
	}
}

func (s *ParticipantSession) StopSpeaking() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.speaking {
		s.speaking = false
		if !s.lastSpeakingStart.IsZero() {
			s.speakingDuration += time.Since(s.lastSpeakingStart)
		}
	}
}

func (s *ParticipantSession) UpdateLastVideoFrame(timestamp time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastVideoFrame = timestamp
}

func (s *ParticipantSession) UpdateConnectionQuality(quality string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.connectionQuality != quality {
		s.qualityChanges = append(s.qualityChanges, QualityChange{
			From:      s.connectionQuality,
			To:        quality,
			Timestamp: time.Now(),
			Reason:    "network_conditions",
		})
		s.connectionQuality = quality
	}
}

func (s *ParticipantSession) hasVideoTrack() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, track := range s.tracks {
		if track.Type == "video" && track.Active {
			return true
		}
	}
	return false
}

func (s *ParticipantSession) GetStats() ParticipantStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	activeTrackCount := 0
	for _, track := range s.tracks {
		if track.Active {
			activeTrackCount++
		}
	}

	duration := time.Since(s.startTime)
	if s.endTime != nil {
		duration = s.endTime.Sub(s.startTime)
	}

	speakingDuration := s.speakingDuration
	if s.speaking && !s.lastSpeakingStart.IsZero() {
		speakingDuration += time.Since(s.lastSpeakingStart)
	}

	return ParticipantStats{
		Connected:         s.connected,
		Speaking:          s.speaking,
		TrackCount:        activeTrackCount,
		AudioLevel:        s.audioLevel,
		FrameRate:         s.frameRate,
		SpeakingDuration:  speakingDuration,
		ConnectionQuality: s.connectionQuality,
		SessionDuration:   duration,
	}
}
