package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type ParticipantMonitoringHandler struct {
	agent.BaseHandler // Embed base handler for default implementations
	monitor           *ParticipantMonitor
	config            *Config
}

// OnJobRequest implements agent.UniversalHandler
func (h *ParticipantMonitoringHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	log.Printf("Job request received: %s for participant job", job.Id)

	// Validate job type
	if job.Type != livekit.JobType_JT_PARTICIPANT {
		log.Printf("Rejecting non-participant job type: %v", job.Type)
		return false, nil
	}

	// Extract target participant from job metadata
	var jobMeta JobMetadata
	if err := json.Unmarshal([]byte(job.Metadata), &jobMeta); err != nil {
		log.Printf("Failed to parse job metadata: %v", err)
		return false, nil
	}

	if jobMeta.ParticipantIdentity == "" {
		log.Printf("Participant identity not specified in job metadata")
		return false, nil
	}

	// Accept the job
	return true, &agent.JobMetadata{
		ParticipantIdentity: "participant-monitor",
		ParticipantName:     "Participant Monitor",
		ParticipantMetadata: `{"agent_type": "monitor"}`,
	}
}

// OnJobAssigned implements agent.UniversalHandler
func (h *ParticipantMonitoringHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	// Extract target participant from job metadata
	var jobMeta JobMetadata
	if err := json.Unmarshal([]byte(jobCtx.Job.Metadata), &jobMeta); err != nil {
		return fmt.Errorf("failed to parse job metadata: %w", err)
	}

	log.Printf("Starting monitoring for participant: %s in room: %s (Job ID: %s)",
		jobMeta.ParticipantIdentity, jobCtx.Room.Name(), jobCtx.Job.Id)

	// Create monitoring session
	session := h.monitor.CreateSession(
		jobMeta.ParticipantIdentity,
		jobCtx.Room.Name(),
		jobCtx.Job.Id,
		jobMeta,
	)
	defer func() {
		session.End()
		log.Printf("Monitoring ended for participant: %s", jobMeta.ParticipantIdentity)
	}()

	// Start monitoring participant
	go h.monitorParticipant(ctx, jobCtx.Room, jobMeta.ParticipantIdentity, session, jobMeta)

	// Block until context is done
	<-ctx.Done()
	return nil
}

// OnJobTerminated implements agent.UniversalHandler
func (h *ParticipantMonitoringHandler) OnJobTerminated(ctx context.Context, jobID string) {
	log.Printf("Job terminated: %s", jobID)
	h.monitor.EndSessionByJobID(jobID)
}

// monitorParticipant polls for participant and track changes
func (h *ParticipantMonitoringHandler) monitorParticipant(
	ctx context.Context,
	room *lksdk.Room,
	targetIdentity string,
	session *ParticipantSession,
	jobMeta JobMetadata,
) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var currentParticipant *lksdk.RemoteParticipant
	knownTracks := make(map[string]bool)
	inactivityDeadline := time.Now().Add(h.config.InactivityTimeout)
	welcomeSent := false

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Find target participant
			var found bool
			for _, p := range room.GetRemoteParticipants() {
				if p.Identity() == targetIdentity {
					currentParticipant = p
					found = true
					break
				}
			}

			// Handle participant connection/disconnection
			if found && !session.IsConnected() {
				// Participant connected
				log.Printf("Target participant connected: %s", targetIdentity)
				session.SetConnected(true)
				inactivityDeadline = time.Now().Add(h.config.InactivityTimeout)

				// Send welcome message
				if h.config.EnableNotifications && !welcomeSent {
					h.sendWelcomeMessage(room, currentParticipant, jobMeta)
					welcomeSent = true
				}
			} else if !found && session.IsConnected() {
				// Participant disconnected
				log.Printf("Target participant disconnected: %s", targetIdentity)
				session.SetConnected(false)
				currentParticipant = nil
				knownTracks = make(map[string]bool)

				if jobMeta.EndOnDisconnect {
					log.Printf("Ending monitoring due to participant disconnect")
					return
				}
			}

			// Check inactivity timeout
			if !found && time.Now().After(inactivityDeadline) {
				log.Printf("Participant %s not found within timeout", targetIdentity)
				return
			}

			// Monitor tracks if participant is connected
			if currentParticipant != nil {
				h.monitorTracks(session, currentParticipant, knownTracks, room)
			}
		}
	}
}

// monitorTracks monitors track changes for a participant
func (h *ParticipantMonitoringHandler) monitorTracks(
	session *ParticipantSession,
	participant *lksdk.RemoteParticipant,
	knownTracks map[string]bool,
	room *lksdk.Room,
) {
	currentTracks := make(map[string]bool)

	// Check all current tracks
	for _, pub := range participant.TrackPublications() {
		trackSID := pub.SID()
		currentTracks[trackSID] = true

		// New track published
		if !knownTracks[trackSID] {
			knownTracks[trackSID] = true

			remoteTrack, ok := pub.(*lksdk.RemoteTrackPublication)
			if !ok {
				continue
			}

			log.Printf("Track published by %s: %s (%s)",
				participant.Identity(), trackSID, remoteTrack.Kind())

			session.AddTrack(trackSID, remoteTrack.Kind().String())

			// Subscribe to track
			if err := remoteTrack.SetSubscribed(true); err != nil {
				log.Printf("Failed to subscribe to track %s: %v", trackSID, err)
			} else {
				// Monitor track quality
				go h.monitorTrackQuality(session, remoteTrack, participant, room)
			}
		}
	}

	// Check for unpublished tracks
	for trackSID := range knownTracks {
		if !currentTracks[trackSID] {
			delete(knownTracks, trackSID)
			log.Printf("Track unpublished by %s: %s", participant.Identity(), trackSID)
			session.RemoveTrack(trackSID)
		}
	}
}

// monitorTrackQuality monitors the quality of a specific track
func (h *ParticipantMonitoringHandler) monitorTrackQuality(
	session *ParticipantSession,
	pub *lksdk.RemoteTrackPublication,
	participant *lksdk.RemoteParticipant,
	room *lksdk.Room,
) {
	track := pub.Track()
	if track == nil {
		return
	}

	// Monitor based on track type
	if pub.Kind() == lksdk.TrackKindAudio {
		h.monitorAudioTrack(session, track.(*webrtc.TrackRemote), participant, room)
	} else if pub.Kind() == lksdk.TrackKindVideo {
		h.monitorVideoTrack(session, track.(*webrtc.TrackRemote), participant, room)
	}
}

// monitorAudioTrack monitors audio levels and quality
func (h *ParticipantMonitoringHandler) monitorAudioTrack(
	session *ParticipantSession,
	track *webrtc.TrackRemote,
	participant *lksdk.RemoteParticipant,
	room *lksdk.Room,
) {
	// This is a simplified version - in production you would decode RTP packets
	// and calculate actual audio levels

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	wasSpeaking := false

	for {
		select {
		case <-ticker.C:
			// Simulate audio level (in real implementation, decode RTP and calculate RMS)
			audioLevel := math.Sin(float64(time.Now().UnixNano())/1e9)*0.5 + 0.5
			audioLevel *= 100 // Convert to percentage

			session.UpdateAudioLevel(float32(audioLevel))

			// Check speaking threshold
			isSpeaking := audioLevel > float64(h.config.SpeakingThreshold)

			if isSpeaking && !wasSpeaking {
				log.Printf("Participant %s started speaking", participant.Identity())
				session.StartSpeaking()

				// Send notification
				if h.config.EnableNotifications {
					msg := ParticipantMessage{
						Type:      "speaking_started",
						Timestamp: time.Now(),
					}
					if data, err := json.Marshal(msg); err == nil {
						room.LocalParticipant.PublishData(data, nil)
					}
				}
			} else if !isSpeaking && wasSpeaking {
				log.Printf("Participant %s stopped speaking", participant.Identity())
				session.StopSpeaking()
			}

			wasSpeaking = isSpeaking
		}
	}
}

// monitorVideoTrack monitors video frame rate and quality
func (h *ParticipantMonitoringHandler) monitorVideoTrack(
	session *ParticipantSession,
	track *webrtc.TrackRemote,
	participant *lksdk.RemoteParticipant,
	room *lksdk.Room,
) {
	// Simplified frame rate monitoring
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// In real implementation, you would count actual frames received
			frameRate := 25 + (time.Now().Unix() % 5) // Simulate varying frame rate
			session.UpdateVideoFrameRate(float32(frameRate))

			log.Printf("Video track %s - Frame rate: %.1f fps", track.ID(), float32(frameRate))
		}
	}
}

// sendWelcomeMessage sends a welcome message to the participant
func (h *ParticipantMonitoringHandler) sendWelcomeMessage(
	room *lksdk.Room,
	participant *lksdk.RemoteParticipant,
	jobMeta JobMetadata,
) {
	msg := ParticipantMessage{
		Type:      "welcome",
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"message":    "Welcome! Your session is being monitored for quality.",
			"monitor_id": room.LocalParticipant.Identity(),
			"features":   jobMeta.MonitoringFeatures,
		},
	}

	if data, err := json.Marshal(msg); err == nil {
		if err := room.LocalParticipant.PublishData(data, nil); err != nil {
			log.Printf("Failed to send welcome message: %v", err)
		} else {
			log.Printf("Sent welcome message to %s", participant.Identity())
		}
	}
}

// ParticipantMessage represents a message sent to participants
type ParticipantMessage struct {
	Type      string                 `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}
