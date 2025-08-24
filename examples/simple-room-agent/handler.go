package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// RoomAnalyticsHandler handles room-level jobs and collects analytics
type RoomAnalyticsHandler struct {
	agent.BaseHandler // Embed base handler for default implementations
	mu                sync.RWMutex
	sessions          map[string]*RoomSession
	analytics         *AnalyticsCollector
}

// NewRoomAnalyticsHandler creates a new handler instance
func NewRoomAnalyticsHandler() *RoomAnalyticsHandler {
	return &RoomAnalyticsHandler{
		sessions:  make(map[string]*RoomSession),
		analytics: NewAnalyticsCollector(),
	}
}

// Shutdown cleanly shuts down the handler
func (h *RoomAnalyticsHandler) Shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, session := range h.sessions {
		session.Close()
	}
	h.analytics.Stop()
}

// OnJobRequest implements agent.UniversalHandler
func (h *RoomAnalyticsHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	log.Printf("Job request received: %s for room %s", job.Id, job.Room.Sid)

	// Accept all room jobs
	return true, &agent.JobMetadata{
		ParticipantIdentity: "room-analytics-agent",
		ParticipantName:     "Room Analytics",
		ParticipantMetadata: `{"agent_type": "analytics"}`,
	}
}

// OnJobAssigned implements agent.UniversalHandler
func (h *RoomAnalyticsHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	log.Printf("Starting new room job: %s for room %s", jobCtx.Job.Id, jobCtx.Job.Room.Sid)

	session := &RoomSession{
		job:       jobCtx.Job,
		room:      jobCtx.Room,
		analytics: h.analytics,
		startTime: time.Now(),
		events:    make(chan RoomEvent, 100),
		done:      make(chan struct{}),
	}

	h.mu.Lock()
	h.sessions[jobCtx.Job.Id] = session
	h.mu.Unlock()

	// Start the session
	return session.Start(ctx)
}

// OnJobTerminated implements agent.UniversalHandler
func (h *RoomAnalyticsHandler) OnJobTerminated(ctx context.Context, jobID string) {
	log.Printf("Job terminated: %s", jobID)

	h.mu.Lock()
	session, exists := h.sessions[jobID]
	if exists {
		delete(h.sessions, jobID)
	}
	h.mu.Unlock()

	if exists && session != nil {
		session.Close()
	}
}

// Note: Optional UniversalHandler methods are provided by BaseHandler embedding.
// This example uses polling pattern for monitoring participants instead of event callbacks.

// RoomSession represents a single room monitoring session
type RoomSession struct {
	job       *livekit.Job
	room      *lksdk.Room
	analytics *AnalyticsCollector
	startTime time.Time
	events    chan RoomEvent
	done      chan struct{}
	once      sync.Once
}

// Start begins the room monitoring session
func (s *RoomSession) Start(ctx context.Context) error {

	log.Printf("Room session started - Room: %s, Name: %s", s.room.SID(), s.room.Name())

	// Track initial room state
	s.analytics.RecordRoomCreated(s.room.SID(), s.room.Name())

	// Start monitoring participants
	go s.monitorParticipants(ctx)

	// Start event processor
	go s.processEvents(ctx)

	// Start periodic status reporter
	go s.reportStatus(ctx)

	// Block until context is done
	<-ctx.Done()

	return nil
}

// monitorParticipants polls for participant changes
func (s *RoomSession) monitorParticipants(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	knownParticipants := make(map[string]*lksdk.RemoteParticipant)
	knownTracks := make(map[string]map[string]bool) // participantID -> trackSID -> exists

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-ticker.C:
			// Get current participants
			currentParticipants := make(map[string]*lksdk.RemoteParticipant)
			for _, p := range s.room.GetRemoteParticipants() {
				currentParticipants[p.Identity()] = p
			}

			// Check for new participants
			for identity, p := range currentParticipants {
				if _, exists := knownParticipants[identity]; !exists {
					knownParticipants[identity] = p
					knownTracks[identity] = make(map[string]bool)
					s.events <- RoomEvent{
						Type:        "participant_connected",
						Participant: p,
						Timestamp:   time.Now(),
					}
				}

				// Check for track changes
				currentTracks := make(map[string]bool)
				tracks := p.TrackPublications()
				for _, track := range tracks {
					remoteTrack, ok := track.(*lksdk.RemoteTrackPublication)
					if !ok {
						continue
					}
					trackSID := remoteTrack.SID()
					currentTracks[trackSID] = true

					if !knownTracks[identity][trackSID] {
						knownTracks[identity][trackSID] = true
						s.events <- RoomEvent{
							Type:        "track_published",
							Participant: p,
							Track:       remoteTrack,
							Timestamp:   time.Now(),
						}
					}
				}

				// Check for unpublished tracks
				for trackSID := range knownTracks[identity] {
					if !currentTracks[trackSID] {
						delete(knownTracks[identity], trackSID)
						s.events <- RoomEvent{
							Type:        "track_unpublished",
							Participant: p,
							Timestamp:   time.Now(),
						}
					}
				}
			}

			// Check for disconnected participants
			for identity, p := range knownParticipants {
				if _, exists := currentParticipants[identity]; !exists {
					delete(knownParticipants, identity)
					delete(knownTracks, identity)
					s.events <- RoomEvent{
						Type:        "participant_disconnected",
						Participant: p,
						Timestamp:   time.Now(),
					}
				}
			}
		}
	}
}

// processEvents handles incoming room events
func (s *RoomSession) processEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case event := <-s.events:
			s.handleEvent(event)
		}
	}
}

// handleEvent processes individual room events
func (s *RoomSession) handleEvent(event RoomEvent) {
	switch event.Type {
	case "participant_connected":
		log.Printf("Participant connected: %s (%s)", event.Participant.Identity(), event.Participant.SID())
		s.analytics.RecordParticipantJoined(s.room.SID(), event.Participant.SID(), event.Participant.Identity())

	case "participant_disconnected":
		log.Printf("Participant disconnected: %s", event.Participant.Identity())
		s.analytics.RecordParticipantLeft(s.room.SID(), event.Participant.SID())

	case "track_published":
		if event.Track != nil {
			log.Printf("Track published: %s by %s", event.Track.SID(), event.Participant.Identity())
			kind := "unknown"
			if event.Track.Kind() == lksdk.TrackKindAudio {
				kind = "audio"
			} else if event.Track.Kind() == lksdk.TrackKindVideo {
				kind = "video"
			}
			s.analytics.RecordTrackPublished(s.room.SID(), event.Participant.SID(), event.Track.SID(), kind)
		} else {
			log.Printf("Track published by %s", event.Participant.Identity())
		}

	case "track_unpublished":
		if event.Track != nil {
			log.Printf("Track unpublished: %s", event.Track.SID())
			s.analytics.RecordTrackUnpublished(s.room.SID(), event.Track.SID())
		} else {
			log.Printf("Track unpublished")
		}

	case "data_received":
		log.Printf("Data received from %s: %d bytes", event.Participant.Identity(), len(event.Data.Payload))

	case "metadata_update":
		log.Printf("Room metadata updated: %s", event.Metadata)
	}
}

// reportStatus periodically logs room status
func (s *RoomSession) reportStatus(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-ticker.C:
			s.logRoomStatus()
		}
	}
}

// logRoomStatus logs current room statistics
func (s *RoomSession) logRoomStatus() {
	participants := s.room.GetRemoteParticipants()

	audioTracks := 0
	videoTracks := 0

	for _, p := range participants {
		tracks := p.TrackPublications()
		for _, pub := range tracks {
			if pub.Kind() == lksdk.TrackKindAudio {
				audioTracks++
			} else if pub.Kind() == lksdk.TrackKindVideo {
				videoTracks++
			}
		}
	}

	duration := time.Since(s.startTime)

	log.Printf("Room Status - SID: %s, Participants: %d, Audio: %d, Video: %d, Duration: %s",
		s.room.SID(),
		len(participants),
		audioTracks,
		videoTracks,
		duration.Round(time.Second),
	)

	// Report to analytics
	s.analytics.UpdateRoomStats(s.room.SID(), len(participants), audioTracks, videoTracks)
}

// Close terminates the room session
func (s *RoomSession) Close() error {
	s.once.Do(func() {
		close(s.done)

		duration := time.Since(s.startTime)
		log.Printf("Room session ended - Duration: %s", duration.Round(time.Second))

		s.analytics.RecordRoomClosed(s.room.SID(), duration)

		if s.room != nil {
			s.room.Disconnect()
		}
	})

	return nil
}

// RoomEvent represents an event that occurred in the room
type RoomEvent struct {
	Type        string
	Participant *lksdk.RemoteParticipant
	Track       *lksdk.RemoteTrackPublication
	Data        *livekit.UserPacket
	Metadata    string
	Timestamp   time.Time
}
