package main

import (
	"log"
	"sync"
	"time"
)

// AnalyticsCollector collects and reports room analytics
type AnalyticsCollector struct {
	mu     sync.RWMutex
	rooms  map[string]*RoomAnalytics
	ticker *time.Ticker
	done   chan struct{}
}

// NewAnalyticsCollector creates a new analytics collector
func NewAnalyticsCollector() *AnalyticsCollector {
	ac := &AnalyticsCollector{
		rooms:  make(map[string]*RoomAnalytics),
		ticker: time.NewTicker(60 * time.Second),
		done:   make(chan struct{}),
	}

	go ac.reportingLoop()
	return ac
}

// Stop stops the analytics collector
func (ac *AnalyticsCollector) Stop() {
	close(ac.done)
	ac.ticker.Stop()

	// Print final report
	ac.generateReport()
}

// RecordRoomCreated records a new room creation
func (ac *AnalyticsCollector) RecordRoomCreated(roomSID, roomName string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	ac.rooms[roomSID] = &RoomAnalytics{
		RoomSID:              roomSID,
		RoomName:             roomName,
		CreatedAt:            time.Now(),
		Participants:         make(map[string]*ParticipantAnalytics),
		TotalParticipants:    0,
		TotalMessages:        0,
		TotalTracksPublished: 0,
	}
}

// RecordRoomClosed records room closure
func (ac *AnalyticsCollector) RecordRoomClosed(roomSID string, duration time.Duration) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if room, exists := ac.rooms[roomSID]; exists {
		room.ClosedAt = time.Now()
		room.Duration = duration
		room.IsActive = false
	}
}

// RecordParticipantJoined records a participant joining
func (ac *AnalyticsCollector) RecordParticipantJoined(roomSID, participantSID, identity string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if room, exists := ac.rooms[roomSID]; exists {
		room.TotalParticipants++
		room.Participants[participantSID] = &ParticipantAnalytics{
			SID:      participantSID,
			Identity: identity,
			JoinedAt: time.Now(),
			IsActive: true,
		}
		room.CurrentParticipants++
	}
}

// RecordParticipantLeft records a participant leaving
func (ac *AnalyticsCollector) RecordParticipantLeft(roomSID, participantSID string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if room, exists := ac.rooms[roomSID]; exists {
		if participant, pExists := room.Participants[participantSID]; pExists {
			participant.LeftAt = time.Now()
			participant.IsActive = false
			participant.Duration = participant.LeftAt.Sub(participant.JoinedAt)
		}
		if room.CurrentParticipants > 0 {
			room.CurrentParticipants--
		}
	}
}

// RecordTrackPublished records a track being published
func (ac *AnalyticsCollector) RecordTrackPublished(roomSID, participantSID, trackSID, kind string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if room, exists := ac.rooms[roomSID]; exists {
		room.TotalTracksPublished++

		if participant, pExists := room.Participants[participantSID]; pExists {
			participant.TracksPublished++

			switch kind {
			case "audio":
				participant.AudioTracksPublished++
			case "video":
				participant.VideoTracksPublished++
			}
		}

		switch kind {
		case "audio":
			room.CurrentAudioTracks++
		case "video":
			room.CurrentVideoTracks++
		}
	}
}

// RecordTrackUnpublished records a track being unpublished
func (ac *AnalyticsCollector) RecordTrackUnpublished(roomSID, trackSID string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if room, exists := ac.rooms[roomSID]; exists {
		// In a real implementation, we'd track which type of track this was
		// For simplicity, we'll just decrement if counts are positive
		if room.CurrentAudioTracks > 0 {
			room.CurrentAudioTracks--
		} else if room.CurrentVideoTracks > 0 {
			room.CurrentVideoTracks--
		}
	}
}

// UpdateRoomStats updates current room statistics
func (ac *AnalyticsCollector) UpdateRoomStats(roomSID string, participants, audioTracks, videoTracks int) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if room, exists := ac.rooms[roomSID]; exists {
		room.CurrentParticipants = participants
		room.CurrentAudioTracks = audioTracks
		room.CurrentVideoTracks = videoTracks
		room.LastUpdated = time.Now()

		// Update peak values
		if participants > room.PeakParticipants {
			room.PeakParticipants = participants
		}
	}
}

// reportingLoop periodically generates analytics reports
func (ac *AnalyticsCollector) reportingLoop() {
	for {
		select {
		case <-ac.done:
			return
		case <-ac.ticker.C:
			ac.generateReport()
		}
	}
}

// generateReport generates and logs an analytics report
func (ac *AnalyticsCollector) generateReport() {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	log.Println("========== Analytics Report ==========")
	log.Printf("Total Rooms: %d", len(ac.rooms))

	activeRooms := 0
	totalParticipants := 0
	totalTracks := 0

	for _, room := range ac.rooms {
		if room.IsActive || room.CurrentParticipants > 0 {
			activeRooms++
			totalParticipants += room.CurrentParticipants
			totalTracks += room.CurrentAudioTracks + room.CurrentVideoTracks
		}
	}

	log.Printf("Active Rooms: %d", activeRooms)
	log.Printf("Total Participants: %d", totalParticipants)
	log.Printf("Total Active Tracks: %d", totalTracks)

	// Detailed room stats
	for _, room := range ac.rooms {
		if room.IsActive || room.CurrentParticipants > 0 {
			log.Printf("\nRoom: %s (%s)", room.RoomName, room.RoomSID)
			log.Printf("  - Participants: %d (Peak: %d, Total: %d)",
				room.CurrentParticipants,
				room.PeakParticipants,
				room.TotalParticipants,
			)
			log.Printf("  - Tracks: Audio=%d, Video=%d",
				room.CurrentAudioTracks,
				room.CurrentVideoTracks,
			)

			uptime := time.Since(room.CreatedAt)
			log.Printf("  - Uptime: %s", uptime.Round(time.Second))

			// Active participants
			activeParticipants := 0
			for _, p := range room.Participants {
				if p.IsActive {
					activeParticipants++
				}
			}
			if activeParticipants > 0 {
				log.Printf("  - Active Participants:")
				for _, p := range room.Participants {
					if p.IsActive {
						duration := time.Since(p.JoinedAt)
						log.Printf("    * %s (SID: %s, Duration: %s, Tracks: %d)",
							p.Identity,
							p.SID,
							duration.Round(time.Second),
							p.TracksPublished,
						)
					}
				}
			}
		}
	}

	log.Println("=====================================")
}

// RoomAnalytics contains analytics data for a room
type RoomAnalytics struct {
	RoomSID   string
	RoomName  string
	CreatedAt time.Time
	ClosedAt  time.Time
	Duration  time.Duration
	IsActive  bool

	Participants        map[string]*ParticipantAnalytics
	TotalParticipants   int
	CurrentParticipants int
	PeakParticipants    int

	TotalMessages        int
	TotalTracksPublished int
	CurrentAudioTracks   int
	CurrentVideoTracks   int

	LastUpdated time.Time
}

// ParticipantAnalytics contains analytics data for a participant
type ParticipantAnalytics struct {
	SID      string
	Identity string
	JoinedAt time.Time
	LeftAt   time.Time
	Duration time.Duration
	IsActive bool

	TracksPublished      int
	AudioTracksPublished int
	VideoTracksPublished int
	MessagessSent        int
}
