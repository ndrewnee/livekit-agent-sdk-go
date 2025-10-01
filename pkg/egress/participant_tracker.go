package egress

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// ParticipantTracker tracks participants in a room recording session
// Implements participant management requirements from PLAN.md Milestone 2
type ParticipantTracker struct {
	participants map[string]*ParticipantInfo // Participant SID -> info
	mu           sync.RWMutex

	// Statistics
	stats ParticipantStats

	// Callbacks
	onParticipantJoined func(participant *ParticipantInfo)
	onParticipantLeft   func(participant *ParticipantInfo)
}

// ParticipantInfo holds information about a participant
type ParticipantInfo struct {
	SID          string                    `json:"sid"`
	Identity     string                    `json:"identity"`
	Name         string                    `json:"name"`
	JoinedAt     time.Time                 `json:"joined_at"`
	LeftAt       *time.Time                `json:"left_at,omitempty"`
	IsActive     bool                      `json:"is_active"`
	TrackCount   int                       `json:"track_count"`
	VideoTracks  int                       `json:"video_tracks"`
	AudioTracks  int                       `json:"audio_tracks"`
	ScreenShares int                       `json:"screen_shares"`
	Metadata     string                    `json:"metadata,omitempty"`
	Participant  *lksdk.RemoteParticipant `json:"-"`
}

// ParticipantStats holds participant tracking statistics
type ParticipantStats struct {
	TotalParticipantsJoined int64 `json:"total_participants_joined"`
	TotalParticipantsLeft   int64 `json:"total_participants_left"`
	CurrentParticipants     int   `json:"current_participants"`
	PeakParticipants        int   `json:"peak_participants"`
	AverageDuration         int64 `json:"average_duration_seconds"`
	TotalDuration           int64 `json:"total_duration_seconds"`
}

// NewParticipantTracker creates a new participant tracker
func NewParticipantTracker() *ParticipantTracker {
	return &ParticipantTracker{
		participants: make(map[string]*ParticipantInfo),
	}
}

// OnParticipantConnected handles a participant joining the room
func (pt *ParticipantTracker) OnParticipantConnected(participant *lksdk.RemoteParticipant) {
	logger.Infow("participant connected",
		"participantSID", participant.SID(),
		"identity", participant.Identity(),
		"name", participant.Name())

	pt.mu.Lock()
	defer pt.mu.Unlock()

	// Create participant info
	info := &ParticipantInfo{
		SID:         participant.SID(),
		Identity:    participant.Identity(),
		Name:        participant.Name(),
		JoinedAt:    time.Now(),
		IsActive:    true,
		Metadata:    participant.Metadata(),
		Participant: participant,
	}

	// Count tracks
	// TODO: The LiveKit SDK v2 doesn't expose GetTracks() on RemoteParticipant
	// We need to track publications through track events instead
	// For now, skip initial track counting
	/*
	for _, track := range participant.GetTracks() {
		info.TrackCount++
		publication := track.(*lksdk.RemoteTrackPublication)

		switch publication.Kind() {
		case livekit.TrackType_VIDEO:
			if publication.Source() == livekit.TrackSource_SCREEN_SHARE {
				info.ScreenShares++
			} else {
				info.VideoTracks++
			}
		case livekit.TrackType_AUDIO:
			info.AudioTracks++
		}
	}
	*/

	pt.participants[participant.SID()] = info

	// Update statistics
	atomic.AddInt64(&pt.stats.TotalParticipantsJoined, 1)
	currentCount := len(pt.participants)
	if currentCount > pt.stats.PeakParticipants {
		pt.stats.PeakParticipants = currentCount
	}

	// Trigger callback
	if pt.onParticipantJoined != nil {
		pt.onParticipantJoined(info)
	}
}

// OnParticipantDisconnected handles a participant leaving the room
func (pt *ParticipantTracker) OnParticipantDisconnected(participant *lksdk.RemoteParticipant) {
	logger.Infow("participant disconnected",
		"participantSID", participant.SID(),
		"identity", participant.Identity())

	pt.mu.Lock()
	defer pt.mu.Unlock()

	info, exists := pt.participants[participant.SID()]
	if !exists {
		logger.Debugw("participant not tracked",
			"participantSID", participant.SID())
		return
	}

	// Mark as inactive
	now := time.Now()
	info.IsActive = false
	info.LeftAt = &now

	// Calculate duration
	duration := now.Sub(info.JoinedAt).Seconds()
	atomic.AddInt64(&pt.stats.TotalDuration, int64(duration))
	atomic.AddInt64(&pt.stats.TotalParticipantsLeft, 1)

	// Update average duration
	totalLeft := atomic.LoadInt64(&pt.stats.TotalParticipantsLeft)
	if totalLeft > 0 {
		pt.stats.AverageDuration = pt.stats.TotalDuration / totalLeft
	}

	// Remove from active participants
	delete(pt.participants, participant.SID())

	// Trigger callback
	if pt.onParticipantLeft != nil {
		pt.onParticipantLeft(info)
	}
}

// OnTrackPublished updates participant track count
func (pt *ParticipantTracker) OnTrackPublished(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	info, exists := pt.participants[participant.SID()]
	if !exists {
		return
	}

	info.TrackCount++

	switch publication.Kind() {
	case lksdk.TrackKindVideo:
		if publication.Source() == livekit.TrackSource_SCREEN_SHARE {
			info.ScreenShares++
		} else {
			info.VideoTracks++
		}
	case lksdk.TrackKindAudio:
		info.AudioTracks++
	}

	logger.Debugw("participant track count updated",
		"participantSID", participant.SID(),
		"totalTracks", info.TrackCount,
		"video", info.VideoTracks,
		"audio", info.AudioTracks,
		"screenShare", info.ScreenShares)
}

// OnTrackUnpublished updates participant track count
func (pt *ParticipantTracker) OnTrackUnpublished(publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	info, exists := pt.participants[participant.SID()]
	if !exists {
		return
	}

	if info.TrackCount > 0 {
		info.TrackCount--
	}

	switch publication.Kind() {
	case lksdk.TrackKindVideo:
		if publication.Source() == livekit.TrackSource_SCREEN_SHARE {
			if info.ScreenShares > 0 {
				info.ScreenShares--
			}
		} else {
			if info.VideoTracks > 0 {
				info.VideoTracks--
			}
		}
	case lksdk.TrackKindAudio:
		if info.AudioTracks > 0 {
			info.AudioTracks--
		}
	}
}

// GetParticipant returns information about a specific participant
func (pt *ParticipantTracker) GetParticipant(sid string) (*ParticipantInfo, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	info, exists := pt.participants[sid]
	if !exists {
		return nil, false
	}

	// Return a copy
	infoCopy := *info
	return &infoCopy, true
}

// GetActiveParticipants returns all currently active participants
func (pt *ParticipantTracker) GetActiveParticipants() []*ParticipantInfo {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make([]*ParticipantInfo, 0, len(pt.participants))
	for _, info := range pt.participants {
		if info.IsActive {
			// Create a copy
			infoCopy := *info
			result = append(result, &infoCopy)
		}
	}

	return result
}

// GetParticipantCount returns the current number of active participants
func (pt *ParticipantTracker) GetParticipantCount() int {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	return len(pt.participants)
}

// HasMinimumParticipants checks if minimum participant requirement is met
func (pt *ParticipantTracker) HasMinimumParticipants(minCount int) bool {
	if minCount <= 0 {
		return true
	}
	return pt.GetParticipantCount() >= minCount
}

// GetStats returns participant tracking statistics
func (pt *ParticipantTracker) GetStats() ParticipantStats {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	return ParticipantStats{
		TotalParticipantsJoined: atomic.LoadInt64(&pt.stats.TotalParticipantsJoined),
		TotalParticipantsLeft:   atomic.LoadInt64(&pt.stats.TotalParticipantsLeft),
		CurrentParticipants:     len(pt.participants),
		PeakParticipants:        pt.stats.PeakParticipants,
		AverageDuration:         pt.stats.AverageDuration,
		TotalDuration:           atomic.LoadInt64(&pt.stats.TotalDuration),
	}
}

// SetCallbacks sets the participant event callbacks
func (pt *ParticipantTracker) SetCallbacks(
	onJoined func(participant *ParticipantInfo),
	onLeft func(participant *ParticipantInfo)) {
	pt.onParticipantJoined = onJoined
	pt.onParticipantLeft = onLeft
}

// FindParticipantByIdentity finds a participant by their identity
func (pt *ParticipantTracker) FindParticipantByIdentity(identity string) (*ParticipantInfo, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	for _, info := range pt.participants {
		if info.Identity == identity {
			// Return a copy
			infoCopy := *info
			return &infoCopy, true
		}
	}

	return nil, false
}

// GetParticipantsWithVideo returns participants that have video tracks
func (pt *ParticipantTracker) GetParticipantsWithVideo() []*ParticipantInfo {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make([]*ParticipantInfo, 0)
	for _, info := range pt.participants {
		if info.IsActive && info.VideoTracks > 0 {
			// Create a copy
			infoCopy := *info
			result = append(result, &infoCopy)
		}
	}

	return result
}

// GetParticipantsWithAudio returns participants that have audio tracks
func (pt *ParticipantTracker) GetParticipantsWithAudio() []*ParticipantInfo {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make([]*ParticipantInfo, 0)
	for _, info := range pt.participants {
		if info.IsActive && info.AudioTracks > 0 {
			// Create a copy
			infoCopy := *info
			result = append(result, &infoCopy)
		}
	}

	return result
}

// GetParticipantsWithScreenShare returns participants sharing their screen
func (pt *ParticipantTracker) GetParticipantsWithScreenShare() []*ParticipantInfo {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make([]*ParticipantInfo, 0)
	for _, info := range pt.participants {
		if info.IsActive && info.ScreenShares > 0 {
			// Create a copy
			infoCopy := *info
			result = append(result, &infoCopy)
		}
	}

	return result
}

// Reset clears all participant tracking data
func (pt *ParticipantTracker) Reset() {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	pt.participants = make(map[string]*ParticipantInfo)
	pt.stats = ParticipantStats{}
}