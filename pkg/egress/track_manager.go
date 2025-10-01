package egress

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// TrackInfo holds information about a track
type TrackInfo struct {
	TrackID         string
	ParticipantID   string
	ParticipantName string
	Kind            webrtc.RTPCodecType
	Codec           webrtc.RTPCodecParameters
	StartTime       time.Time
	PacketsReceived uint64
	BytesReceived   uint64
	LastPacketTime  time.Time
	Active          bool
}

// PipelineRouter defines the interface for routing packets to a pipeline
type PipelineRouter interface {
	InjectVideoRTP(packet *rtp.Packet) error
	InjectAudioRTP(packet *rtp.Packet) error
}

// TrackManager manages WebRTC tracks and their routing
type TrackManager struct {
	mu         sync.RWMutex
	tracks     map[string]*TrackInfo
	router     PipelineRouter // Real pipeline router
	codec      *CodecTracker
	sessionID  string
	maxTracks  int
}

// NewTrackManager creates a new track manager
func NewTrackManager(sessionID string, router PipelineRouter, codecTracker *CodecTracker) *TrackManager {
	return &TrackManager{
		tracks:    make(map[string]*TrackInfo),
		router:    router,
		codec:     codecTracker,
		sessionID: sessionID,
		maxTracks: 100, // Default max tracks
	}
}

// TrackRemoteInterface defines the interface for a remote track
type TrackRemoteInterface interface {
	ID() string
	Kind() webrtc.RTPCodecType
	Codec() webrtc.RTPCodecParameters
	ReadRTP() (*rtp.Packet, interceptor.Attributes, error)
}

// AddTrack adds a new track to the manager
func (tm *TrackManager) AddTrack(
	track TrackRemoteInterface,
	participantID string,
	participantName string,
) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Check max tracks limit
	if len(tm.tracks) >= tm.maxTracks {
		return fmt.Errorf("maximum track limit (%d) reached", tm.maxTracks)
	}

	trackID := track.ID()

	// Check if track already exists
	if _, exists := tm.tracks[trackID]; exists {
		return fmt.Errorf("track %s already exists", trackID)
	}

	// Validate codec
	codec := track.Codec()
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		if err := tm.codec.ValidateVideoCodec(codec); err != nil {
			return fmt.Errorf("video codec validation failed: %w", err)
		}
	} else {
		if err := tm.codec.ValidateAudioCodec(codec); err != nil {
			return fmt.Errorf("audio codec validation failed: %w", err)
		}
	}

	// Create track info
	info := &TrackInfo{
		TrackID:         trackID,
		ParticipantID:   participantID,
		ParticipantName: participantName,
		Kind:            track.Kind(),
		Codec:           codec,
		StartTime:       time.Now(),
		Active:          true,
	}

	tm.tracks[trackID] = info

	log.Printf("Added track %s (%s) from participant %s (%s)",
		trackID, codec.MimeType, participantName, participantID)

	// Start forwarding RTP packets
	go tm.forwardRTPPackets(track, info)

	return nil
}

// RemoveTrack removes a track from the manager
func (tm *TrackManager) RemoveTrack(trackID string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	info, exists := tm.tracks[trackID]
	if !exists {
		return fmt.Errorf("track %s not found", trackID)
	}

	info.Active = false
	delete(tm.tracks, trackID)

	log.Printf("Removed track %s from participant %s, received %d packets",
		trackID, info.ParticipantName, info.PacketsReceived)

	return nil
}

// forwardRTPPackets reads RTP packets from track and forwards to router
func (tm *TrackManager) forwardRTPPackets(track TrackRemoteInterface, info *TrackInfo) {
	for {
		// Check if track is still active
		tm.mu.RLock()
		active := info.Active
		tm.mu.RUnlock()

		if !active {
			break
		}

		// Read RTP packet
		packet, _, err := track.ReadRTP()
		if err != nil {
			log.Printf("Error reading RTP from track %s: %v", track.ID(), err)
			break
		}

		// Update statistics
		tm.mu.Lock()
		info.PacketsReceived++
		info.BytesReceived += uint64(len(packet.Payload))
		info.LastPacketTime = time.Now()
		tm.mu.Unlock()

		// Route packet to pipeline
		if err := tm.routePacket(packet, track.Kind()); err != nil {
			log.Printf("Failed to route packet from track %s: %v", track.ID(), err)
			// Continue processing even if routing fails
		}
	}

	// Mark track as inactive
	tm.mu.Lock()
	info.Active = false
	tm.mu.Unlock()
}

// routePacket routes an RTP packet to the appropriate destination
func (tm *TrackManager) routePacket(packet *rtp.Packet, kind webrtc.RTPCodecType) error {
	if tm.router == nil {
		return fmt.Errorf("no router configured")
	}

	if packet == nil {
		return fmt.Errorf("nil packet")
	}

	// Route packet based on track kind
	if kind == webrtc.RTPCodecTypeVideo {
		return tm.router.InjectVideoRTP(packet)
	} else if kind == webrtc.RTPCodecTypeAudio {
		return tm.router.InjectAudioRTP(packet)
	}

	return fmt.Errorf("unknown codec type: %v", kind)
}

// GetTrack returns information about a specific track
func (tm *TrackManager) GetTrack(trackID string) (*TrackInfo, error) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	info, exists := tm.tracks[trackID]
	if !exists {
		return nil, fmt.Errorf("track %s not found", trackID)
	}

	// Return a copy to avoid race conditions
	trackCopy := *info
	return &trackCopy, nil
}

// GetAllTracks returns information about all tracks
func (tm *TrackManager) GetAllTracks() []TrackInfo {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	tracks := make([]TrackInfo, 0, len(tm.tracks))
	for _, info := range tm.tracks {
		tracks = append(tracks, *info)
	}

	return tracks
}

// GetActiveTrackCount returns the number of active tracks
func (tm *TrackManager) GetActiveTrackCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	count := 0
	for _, info := range tm.tracks {
		if info.Active {
			count++
		}
	}

	return count
}

// GetStatistics returns track statistics
func (tm *TrackManager) GetStatistics() TrackStatistics {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	stats := TrackStatistics{
		TotalTracks:   len(tm.tracks),
		ActiveTracks:  0,
		VideoTracks:   0,
		AudioTracks:   0,
		TotalPackets:  0,
		TotalBytes:    0,
		Participants:  make(map[string]bool),
	}

	for _, info := range tm.tracks {
		if info.Active {
			stats.ActiveTracks++
		}

		if info.Kind == webrtc.RTPCodecTypeVideo {
			stats.VideoTracks++
		} else {
			stats.AudioTracks++
		}

		stats.TotalPackets += info.PacketsReceived
		stats.TotalBytes += info.BytesReceived
		stats.Participants[info.ParticipantID] = true
	}

	stats.UniqueParticipants = len(stats.Participants)

	return stats
}

// RemoveParticipantTracks removes all tracks for a participant
func (tm *TrackManager) RemoveParticipantTracks(participantID string) int {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	removedCount := 0
	for trackID, info := range tm.tracks {
		if info.ParticipantID == participantID {
			info.Active = false
			delete(tm.tracks, trackID)
			removedCount++
		}
	}

	if removedCount > 0 {
		log.Printf("Removed %d tracks for participant %s", removedCount, participantID)
	}

	return removedCount
}

// SetMaxTracks sets the maximum number of tracks allowed
func (tm *TrackManager) SetMaxTracks(max int) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.maxTracks = max
}

// Close shuts down the track manager
func (tm *TrackManager) Close() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Mark all tracks as inactive
	for _, info := range tm.tracks {
		info.Active = false
	}

	// Clear tracks
	tm.tracks = make(map[string]*TrackInfo)

	log.Printf("Track manager closed for session %s", tm.sessionID)
}

// TrackStatistics holds aggregate track statistics
type TrackStatistics struct {
	TotalTracks        int
	ActiveTracks       int
	VideoTracks        int
	AudioTracks        int
	TotalPackets       uint64
	TotalBytes         uint64
	UniqueParticipants int
	Participants       map[string]bool `json:"-"` // Hidden from JSON
}