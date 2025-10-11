package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type ParticipantHLSHandler struct {
	agent.BaseHandler // Embed base handler for default implementations
	recorderManager   *RecorderManager
	config            *Config

	// Track subscription management
	mu               sync.RWMutex
	subscribedTracks map[string]*webrtc.TrackRemote // Track SID -> TrackRemote
	directRoom       *lksdk.Room                    // Our direct room connection (not agent connection)
}

type JobMetadata struct {
	ParticipantIdentity string `json:"participant_identity"`
	RecordAudio         bool   `json:"record_audio"`
	RecordVideo         bool   `json:"record_video"`
	EndOnDisconnect     bool   `json:"end_on_disconnect"`
}

// OnJobRequest implements agent.UniversalHandler
func (h *ParticipantHLSHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	log.Printf("Job request received: %s for publisher job", job.Id)

	// Validate job type - use JT_PUBLISHER to ensure tracks are already publishing
	if job.Type != livekit.JobType_JT_PUBLISHER {
		log.Printf("Rejecting non-publisher job type: %v", job.Type)
		return false, nil
	}

	// For JT_PUBLISHER jobs, participant identity comes from job.Participant.Identity
	// not from metadata
	if job.Participant == nil || job.Participant.Identity == "" {
		log.Printf("Participant identity not found in job")
		return false, nil
	}

	participantIdentity := job.Participant.Identity
	log.Printf("Accepting JT_PUBLISHER job for participant: %s", participantIdentity)

	// Accept the job - but DON'T provide participant identity
	// The agent framework connection will use a generated identity
	// Our handler will establish its OWN separate direct connection
	return true, &agent.JobMetadata{
		ParticipantIdentity: "", // Empty - let agent framework generate one
		ParticipantName:     "Agent Worker",
		ParticipantMetadata: `{"agent_type": "worker_only"}`,
	}
}

// OnJobAssigned implements agent.UniversalHandler
func (h *ParticipantHLSHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	// Initialize subscribed tracks map
	h.mu.Lock()
	h.subscribedTracks = make(map[string]*webrtc.TrackRemote)
	h.mu.Unlock()

	// For JT_PUBLISHER jobs, get participant identity from job.Participant
	if jobCtx.Job.Participant == nil || jobCtx.Job.Participant.Identity == "" {
		return fmt.Errorf("participant identity not found in job")
	}

	participantIdentity := jobCtx.Job.Participant.Identity
	roomName := jobCtx.Job.Room.Name

	log.Printf("Starting HLS recording for participant: %s in room: %s (Job ID: %s)",
		participantIdentity, roomName, jobCtx.Job.Id)

	// CRITICAL: Establish a SEPARATE direct room connection with API key
	// The agent dispatch only delivers the job assignment - the handler must
	// connect to the room independently to receive full media data
	log.Printf("[%s] Establishing direct room connection with API key (separate from agent connection)", participantIdentity)

	// Create recorder for this participant
	recorder, err := h.recorderManager.CreateRecorder(participantIdentity, roomName)
	if err != nil {
		return fmt.Errorf("failed to create recorder: %w", err)
	}
	defer func() {
		recorder.Stop()
		h.recorderManager.RemoveRecorder(participantIdentity)
		log.Printf("HLS recording ended for participant: %s", participantIdentity)
	}()

	// Set up room callbacks for direct connection
	roomCallback := h.createRoomCallback(participantIdentity, recorder)

	// Establish direct room connection with API key (NOT using agent connection)
	// CRITICAL: Disable auto-subscribe to ensure we subscribe at the right time (after participant is ready)
	directRoom, err := lksdk.ConnectToRoom(h.config.LiveKitURL, lksdk.ConnectInfo{
		APIKey:              h.config.APIKey,
		APISecret:           h.config.APISecret,
		RoomName:            roomName,
		ParticipantIdentity: fmt.Sprintf("recorder-%s", participantIdentity),
		ParticipantName:     "HLS Recorder",
		ParticipantMetadata: `{"type":"recorder"}`,
	}, roomCallback, lksdk.WithAutoSubscribe(false)) // Disable auto-subscribe!
	if err != nil {
		return fmt.Errorf("failed to connect to room with API key: %w", err)
	}
	defer directRoom.Disconnect()

	// Store the direct room connection so we can verify callbacks are from it
	h.mu.Lock()
	h.directRoom = directRoom
	h.mu.Unlock()

	log.Printf("[%s] ✅ Connected to room with direct API key connection", participantIdentity)

	// Initialize GStreamer pipeline (starts automatically in PLAYING state)
	if err := recorder.InitGStreamer(); err != nil {
		return fmt.Errorf("failed to initialize GStreamer: %w", err)
	}

	// Default job metadata
	jobMeta := JobMetadata{
		ParticipantIdentity: participantIdentity,
		RecordAudio:         h.config.EnableAudio,
		RecordVideo:         h.config.EnableVideo,
		EndOnDisconnect:     false,
	}

	// Start monitoring - track handlers will push data to the running pipeline
	if err := h.monitorAndRecord(ctx, directRoom, participantIdentity, recorder, jobMeta); err != nil {
		return fmt.Errorf("monitoring error: %w", err)
	}

	return nil
}

// createRoomCallback creates room callbacks for the direct room connection
func (h *ParticipantHLSHandler) createRoomCallback(participantIdentity string, recorder *ParticipantRecorder) *lksdk.RoomCallback {
	return &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				// Mark this callback as coming from direct connection
				h.OnTrackSubscribedDirect(context.Background(), track, publication, rp, recorder)
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				h.OnTrackUnsubscribed(context.Background(), track, publication, rp)
			},
		},
	}
}

// OnJobTerminated implements agent.UniversalHandler
func (h *ParticipantHLSHandler) OnJobTerminated(ctx context.Context, jobID string) {
	log.Printf("Job terminated: %s", jobID)
}

// monitorAndRecord waits for the participant and starts recording their tracks
func (h *ParticipantHLSHandler) monitorAndRecord(
	ctx context.Context,
	room *lksdk.Room,
	targetIdentity string,
	recorder *ParticipantRecorder,
	jobMeta JobMetadata,
) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var currentParticipant *lksdk.RemoteParticipant
	knownTracks := make(map[string]bool)
	participantConnected := false
	inactivityDeadline := time.Now().Add(h.config.InactivityTimeout)

	for {
		select {
		case <-ctx.Done():
			return nil

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
			if found && !participantConnected {
				// Participant connected
				log.Printf("[%s] Target participant connected", targetIdentity)
				participantConnected = true
				inactivityDeadline = time.Now().Add(h.config.InactivityTimeout)

			} else if !found && participantConnected {
				// Participant disconnected
				log.Printf("[%s] Target participant disconnected", targetIdentity)
				participantConnected = false
				currentParticipant = nil
				knownTracks = make(map[string]bool)

				if jobMeta.EndOnDisconnect {
					log.Printf("[%s] Ending recording due to participant disconnect", targetIdentity)
					return nil
				}
			}

			// Check inactivity timeout
			if !found && time.Now().After(inactivityDeadline) {
				log.Printf("[%s] Participant not found within timeout", targetIdentity)
				return fmt.Errorf("participant not found within timeout")
			}

			// Monitor tracks if participant is connected
			if currentParticipant != nil {
				h.monitorTracks(currentParticipant, knownTracks, recorder, jobMeta)
			}
		}
	}
}

// monitorTracks monitors track changes for a participant and starts recording
func (h *ParticipantHLSHandler) monitorTracks(
	participant *lksdk.RemoteParticipant,
	knownTracks map[string]bool,
	recorder *ParticipantRecorder,
	jobMeta JobMetadata,
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

			log.Printf("[%s] Track published: %s (%s)",
				participant.Identity(), trackSID, remoteTrack.Kind())

			// Subscribe to track - the OnTrackSubscribed callback will handle the rest
			if err := remoteTrack.SetSubscribed(true); err != nil {
				log.Printf("[%s] Failed to subscribe to track %s: %v", participant.Identity(), trackSID, err)
				continue
			}

			// For video tracks, explicitly request HIGH quality to ensure media delivery
			if remoteTrack.Kind() == lksdk.TrackKindVideo {
				log.Printf("[%s] Requesting HIGH quality for video track %s", participant.Identity(), trackSID)
				if err := remoteTrack.SetVideoQuality(livekit.VideoQuality_HIGH); err != nil {
					log.Printf("[%s] Failed to set video quality: %v", participant.Identity(), err)
				}
			}

			log.Printf("[%s] Subscribed to track %s, waiting for OnTrackSubscribed callback...", participant.Identity(), trackSID)
		}
	}

	// Check for unpublished tracks
	for trackSID := range knownTracks {
		if !currentTracks[trackSID] {
			delete(knownTracks, trackSID)
			log.Printf("[%s] Track unpublished: %s", participant.Identity(), trackSID)
		}
	}
}

// OnTrackSubscribed implements agent.UniversalHandler
// This callback is called by the agent framework's room connection
// We IGNORE these callbacks because the agent connection has restricted permissions
// and delivers empty video packets. Only OnTrackSubscribedDirect should be used.
func (h *ParticipantHLSHandler) OnTrackSubscribed(
	ctx context.Context,
	track *webrtc.TrackRemote,
	publication *lksdk.RemoteTrackPublication,
	participant *lksdk.RemoteParticipant,
) {
	trackSID := publication.SID()
	participantIdentity := participant.Identity()

	log.Printf("[%s] OnTrackSubscribed from AGENT connection (IGNORING): track %s (%s), SSRC=%d",
		participantIdentity, trackSID, publication.Kind(), track.SSRC())
	log.Printf("[%s] Agent connection callbacks are ignored - only direct API connection is used", participantIdentity)
}

// OnTrackSubscribedDirect handles track subscriptions from the direct API connection
// This is called ONLY by the direct room connection established in OnJobAssigned
func (h *ParticipantHLSHandler) OnTrackSubscribedDirect(
	ctx context.Context,
	track *webrtc.TrackRemote,
	publication *lksdk.RemoteTrackPublication,
	participant *lksdk.RemoteParticipant,
	recorder *ParticipantRecorder,
) {
	trackSID := publication.SID()
	participantIdentity := participant.Identity()

	log.Printf("[%s] OnTrackSubscribedDirect from DIRECT API connection: track %s (%s) is ready (SSRC=%d, Codec=%s)",
		participantIdentity, trackSID, publication.Kind(), track.SSRC(), track.Codec().MimeType)

	// Check if we've already handled this track
	h.mu.Lock()
	if _, exists := h.subscribedTracks[trackSID]; exists {
		h.mu.Unlock()
		log.Printf("[%s] Track %s already subscribed, ignoring duplicate callback", participantIdentity, trackSID)
		return
	}
	h.subscribedTracks[trackSID] = track
	h.mu.Unlock()

	// Wait for GStreamer to be initialized (with timeout)
	// The OnJobAssigned function initializes GStreamer, and this callback can fire before that completes
	maxWait := 50 // 50 * 100ms = 5 seconds max wait
	for i := 0; i < maxWait; i++ {
		if publication.Kind() == lksdk.TrackKindVideo && recorder.videoAppSrc != nil {
			break
		} else if publication.Kind() == lksdk.TrackKindAudio && recorder.audioAppSrc != nil {
			break
		}
		if i == 0 {
			log.Printf("[%s] GStreamer not ready yet for track %s, waiting...", participantIdentity, trackSID)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Start recording based on track type
	if publication.Kind() == lksdk.TrackKindVideo && h.config.EnableVideo {
		log.Printf("[%s] Starting video recording for track %s via OnTrackSubscribedDirect callback", participantIdentity, trackSID)

		// CRITICAL: Pass WritePLI function for video tracks
		// PLI will be sent after the FIRST packet is received (like LiveKit egress does)
		// See SOLUTION.md for details
		go recorder.HandleVideoTrack(track, participant.WritePLI)
	} else if publication.Kind() == lksdk.TrackKindAudio && h.config.EnableAudio {
		log.Printf("[%s] Starting audio recording for track %s via OnTrackSubscribedDirect callback", participantIdentity, trackSID)
		go recorder.HandleAudioTrack(track)
	}
}

// OnTrackUnsubscribed implements agent.UniversalHandler
func (h *ParticipantHLSHandler) OnTrackUnsubscribed(
	ctx context.Context,
	track *webrtc.TrackRemote,
	publication *lksdk.RemoteTrackPublication,
	participant *lksdk.RemoteParticipant,
) {
	trackSID := publication.SID()
	log.Printf("[%s] Track unsubscribed: %s", participant.Identity(), trackSID)

	// Remove from subscribed tracks map
	h.mu.Lock()
	delete(h.subscribedTracks, trackSID)
	h.mu.Unlock()
}
