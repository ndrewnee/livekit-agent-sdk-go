package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// ParticipantAgentHandler defines the interface for handling participant agent specific events.
// Participant agents are assigned to monitor and interact with specific participants in a room.
// They receive events when participants join, leave, change state, or publish/unpublish tracks.
//
// This interface extends JobHandler with participant-specific callbacks.
// Implementations should handle these events to provide participant-aware functionality.
type ParticipantAgentHandler interface {
	JobHandler

	// OnParticipantJoined is called when a participant joins the room.
	// This includes both the target participant (if specified) and other participants.
	//
	// For JT_PARTICIPANT jobs, this will be called when the target participant joins.
	// The agent can use this to start interacting with the participant.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant who joined
	OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant)

	// OnParticipantLeft is called when a participant leaves the room.
	// This is called for both graceful disconnections and unexpected drops.
	//
	// If this is the target participant for a JT_PARTICIPANT job,
	// the agent should typically clean up and prepare to terminate.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant who left
	OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant)

	// OnParticipantMetadataChanged is called when participant metadata changes.
	// Metadata is an arbitrary string (often JSON) that can be updated during a session.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant whose metadata changed
	//   - oldMetadata: The previous metadata value
	OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string)

	// OnParticipantNameChanged is called when participant name changes.
	// The name is the display name shown to other participants.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant whose name changed
	//   - oldName: The previous name value
	OnParticipantNameChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldName string)

	// OnParticipantPermissionsChanged is called when participant permissions change.
	// Permissions control what actions a participant can perform in the room.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant whose permissions changed
	//   - oldPermissions: The previous permissions (may be nil)
	OnParticipantPermissionsChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldPermissions *livekit.ParticipantPermission)

	// OnParticipantSpeakingChanged is called when participant speaking state changes.
	// This is based on audio level detection and can be used to implement
	// features like active speaker indication.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant whose speaking state changed
	//   - speaking: true if the participant is now speaking, false otherwise
	OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool)

	// OnParticipantTrackPublished is called when participant publishes a track.
	// A track represents an audio or video stream from the participant.
	//
	// The agent can subscribe to this track to receive the media stream.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant who published the track
	//   - publication: The track publication containing track metadata
	OnParticipantTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)

	// OnParticipantTrackUnpublished is called when participant unpublishes a track.
	// This happens when a participant stops sharing audio/video or disconnects.
	//
	// Any subscriptions to this track will be automatically cleaned up.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - participant: The participant who unpublished the track
	//   - publication: The track publication that was removed
	OnParticipantTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)

	// OnDataReceived is called when data is received from a participant.
	// Data messages provide low-latency communication between participants.
	//
	// Parameters:
	//   - ctx: Context for the operation
	//   - data: The received data payload
	//   - participant: The participant who sent the data
	//   - kind: Whether the data was sent reliably (RELIABLE) or unreliably (LOSSY)
	OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind)
}

// ParticipantAgent extends the base Worker with participant-specific functionality.
// It monitors participants in a room and provides high-level participant management features.
//
// Key features:
//   - Automatic participant state tracking
//   - Permission management and validation
//   - Multi-participant coordination
//   - Event queuing and processing
//   - Targeted data messaging
//
// For JT_PARTICIPANT jobs, the agent is assigned to a specific participant
// and will track that participant throughout their session.
type ParticipantAgent struct {
	*Worker
	handler ParticipantAgentHandler

	mu                        sync.RWMutex
	targetParticipant         *lksdk.RemoteParticipant // The participant this agent is monitoring
	participants              map[string]*ParticipantInfo
	permissionManager         *ParticipantPermissionManager
	coordinationManager       *MultiParticipantCoordinator
	eventProcessor            *ParticipantEventProcessor
	targetParticipantIdentity string
	currentRoom               *lksdk.Room
}

// ParticipantInfo tracks information about a participant.
// This provides a snapshot of participant state and activity metrics.
type ParticipantInfo struct {
	Participant      *lksdk.RemoteParticipant
	JoinedAt         time.Time
	LastActivity     time.Time
	TrackCount       int
	IsSpeaking       bool
	Permissions      *livekit.ParticipantPermission
	InteractionCount int
	DataSent         int64
	DataReceived     int64
}

// NewParticipantAgent creates a new participant agent.
//
// The agent will automatically set JobType to JT_PARTICIPANT.
// When assigned a job, it will monitor the specified participant
// and all other participants in the room.
//
// Parameters:
//   - serverURL: WebSocket URL of the LiveKit server
//   - apiKey: API key for authentication
//   - apiSecret: API secret for authentication
//   - handler: Implementation of ParticipantAgentHandler
//   - opts: Worker options (JobType will be overridden)
//
// Returns a configured ParticipantAgent ready to be started.
func NewParticipantAgent(serverURL, apiKey, apiSecret string, handler ParticipantAgentHandler, opts WorkerOptions) (*ParticipantAgent, error) {
	// Set job type to Participant
	opts.JobType = livekit.JobType_JT_PARTICIPANT

	// Create base worker
	worker := NewWorker(serverURL, apiKey, apiSecret, handler, opts)

	pa := &ParticipantAgent{
		Worker:              worker,
		handler:             handler,
		participants:        make(map[string]*ParticipantInfo),
		permissionManager:   NewParticipantPermissionManager(),
		coordinationManager: NewMultiParticipantCoordinator(),
		eventProcessor:      NewParticipantEventProcessor(),
	}

	// Set up internal event handlers
	pa.setupEventHandlers()

	return pa, nil
}

// setupEventHandlers configures internal event handlers for participant tracking
func (pa *ParticipantAgent) setupEventHandlers() {
	// Override the base OnJobAssigned to set up participant-specific handling
	originalHandler := pa.Worker.handler
	wrappedHandler := &participantJobHandler{
		JobHandler:       originalHandler,
		participantAgent: pa,
	}
	pa.Worker.handler = wrappedHandler
}

// participantJobHandler wraps the user's handler to add participant-specific functionality
type participantJobHandler struct {
	JobHandler
	participantAgent *ParticipantAgent
}

func (h *participantJobHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	// Extract target participant identity from job
	if job.Participant != nil {
		h.participantAgent.targetParticipantIdentity = job.Participant.Identity
	}

	// Store the room reference
	h.participantAgent.mu.Lock()
	h.participantAgent.currentRoom = room
	h.participantAgent.mu.Unlock()

	// Call original handler first
	err := h.JobHandler.OnJobAssigned(ctx, job, room)
	if err != nil {
		return err
	}

	// Set up room event handlers
	h.participantAgent.setupRoomEventHandlers(ctx, room)

	// Start monitoring participants
	go h.participantAgent.monitorParticipants(ctx, room)

	return nil
}

// setupRoomEventHandlers monitors room events and participants
// Since we can't modify the room's callbacks after connection, we need to poll for changes
func (pa *ParticipantAgent) setupRoomEventHandlers(ctx context.Context, room *lksdk.Room) {
	// Start monitoring existing participants
	for _, participant := range room.GetRemoteParticipants() {
		pa.handleParticipantJoined(ctx, participant)
	}
}

// monitorParticipants monitors participants in the room
func (pa *ParticipantAgent) monitorParticipants(ctx context.Context, room *lksdk.Room) {
	ticker := time.NewTicker(500 * time.Millisecond) // Poll more frequently for better responsiveness
	defer ticker.Stop()

	// Keep track of known participants
	knownParticipants := make(map[string]*lksdk.RemoteParticipant)
	participantStates := make(map[string]participantState)

	// Also monitor room connection state
	lastUpdateTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Get current participants and build a map
			currentParticipantsList := room.GetRemoteParticipants()
			currentParticipants := make(map[string]*lksdk.RemoteParticipant)

			// Check if we've received any updates recently
			if time.Since(lastUpdateTime) > 30*time.Second {
				logger := logger.GetLogger()
				logger.Debugw("no participant updates received for 30s, participant data may be stale")
			}

			// Log participant count periodically for debugging
			if len(knownParticipants) > 0 && time.Now().Unix()%10 == 0 {
				logger := logger.GetLogger()
				identities := make([]string, 0, len(currentParticipantsList))
				for _, p := range currentParticipantsList {
					identities = append(identities, p.Identity())
				}
				logger.Debugw("current participants from SDK",
					"count", len(currentParticipantsList),
					"identities", identities)
			}

			for _, participant := range currentParticipantsList {
				currentParticipants[participant.Identity()] = participant
			}

			// Check for new participants and changes
			for _, participant := range currentParticipantsList {
				identity := participant.Identity()
				if _, exists := knownParticipants[identity]; !exists {
					// New participant joined
					knownParticipants[identity] = participant
					participantStates[identity] = participantState{
						metadata: participant.Metadata(),
						name:     participant.Name(),
						tracks:   make(map[string]*lksdk.RemoteTrackPublication),
					}
					pa.handleParticipantJoined(ctx, participant)
				} else {
					// Check for changes in existing participant
					state := participantStates[identity]

					// Check metadata change
					currentMetadata := participant.Metadata()
					if currentMetadata != state.metadata {
						oldMetadata := state.metadata
						state.metadata = currentMetadata
						participantStates[identity] = state
						pa.handleMetadataChanged(ctx, participant, oldMetadata)

						logger := logger.GetLogger()
						logger.Infow("detected metadata change",
							"identity", identity,
							"old", oldMetadata,
							"new", currentMetadata)
					}

					// Check name change
					if participant.Name() != state.name {
						oldName := state.name
						state.name = participant.Name()
						participantStates[identity] = state
						pa.handleNameChanged(ctx, participant, oldName)
					}

					// Check track changes
					pa.checkTrackChanges(ctx, participant, &state)
					participantStates[identity] = state
				}
			}

			// Check for participants who left
			for identity, participant := range knownParticipants {
				if _, exists := currentParticipants[identity]; !exists {
					// Participant left
					delete(knownParticipants, identity)
					delete(participantStates, identity)
					pa.handleParticipantLeft(ctx, participant)

					logger := logger.GetLogger()
					logger.Infow("detected participant left",
						"identity", identity,
						"knownCount", len(knownParticipants),
						"currentCount", len(currentParticipants))
				}
			}

			// Check for target participant if specified
			if pa.targetParticipantIdentity != "" {
				pa.checkTargetParticipant(room)
			}

			// Update participant permissions
			pa.updateParticipantPermissions(room)

			// Process pending events
			pa.eventProcessor.ProcessPendingEvents()
		}
	}
}

// checkTargetParticipant checks if the target participant has joined
func (pa *ParticipantAgent) checkTargetParticipant(room *lksdk.Room) {
	pa.mu.Lock()
	if pa.targetParticipant != nil {
		pa.mu.Unlock()
		return
	}
	pa.mu.Unlock()

	// Look for target participant
	for _, participant := range room.GetRemoteParticipants() {
		if participant.Identity() == pa.targetParticipantIdentity {
			pa.mu.Lock()
			pa.targetParticipant = participant
			pa.mu.Unlock()

			logger := logger.GetLogger()
			logger.Infow("found target participant",
				"identity", participant.Identity(),
				"name", participant.Name())

			// Notify handler
			pa.handler.OnParticipantJoined(context.Background(), participant)
			break
		}
	}
}

// handleParticipantJoined handles when a participant joins
func (pa *ParticipantAgent) handleParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	pa.mu.Lock()
	pa.participants[participant.Identity()] = &ParticipantInfo{
		Participant:  participant,
		JoinedAt:     time.Now(),
		LastActivity: time.Now(),
		Permissions:  participant.Permissions(),
	}
	pa.mu.Unlock()

	// Update permission manager
	pa.permissionManager.UpdateParticipantPermissions(participant.Identity(), participant.Permissions())

	// Register with coordinator
	pa.coordinationManager.RegisterParticipant(participant.Identity(), participant)

	// Process event
	pa.eventProcessor.QueueEvent(ParticipantEvent{
		Type:        EventTypeParticipantJoined,
		Participant: participant,
		Timestamp:   time.Now(),
	})

	// Notify handler
	pa.handler.OnParticipantJoined(ctx, participant)
}

// handleParticipantLeft handles when a participant leaves
func (pa *ParticipantAgent) handleParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	pa.mu.Lock()
	delete(pa.participants, participant.Identity())
	if pa.targetParticipant != nil && pa.targetParticipant.Identity() == participant.Identity() {
		pa.targetParticipant = nil
	}
	pa.mu.Unlock()

	// Remove from managers
	pa.permissionManager.RemoveParticipant(participant.Identity())
	pa.coordinationManager.UnregisterParticipant(participant.Identity())

	// Process event
	pa.eventProcessor.QueueEvent(ParticipantEvent{
		Type:        EventTypeParticipantLeft,
		Participant: participant,
		Timestamp:   time.Now(),
	})

	// Notify handler
	pa.handler.OnParticipantLeft(ctx, participant)
}

// handleMetadataChanged handles metadata changes
func (pa *ParticipantAgent) handleMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
	pa.mu.Lock()
	if info, exists := pa.participants[participant.Identity()]; exists {
		info.LastActivity = time.Now()
	}
	pa.mu.Unlock()

	// Process event
	pa.eventProcessor.QueueEvent(ParticipantEvent{
		Type:        EventTypeMetadataChanged,
		Participant: participant,
		OldValue:    oldMetadata,
		NewValue:    participant.Metadata(),
		Timestamp:   time.Now(),
	})

	// Notify handler
	pa.handler.OnParticipantMetadataChanged(ctx, participant, oldMetadata)
}

// handleNameChanged handles name changes
func (pa *ParticipantAgent) handleNameChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldName string) {
	pa.mu.Lock()
	if info, exists := pa.participants[participant.Identity()]; exists {
		info.LastActivity = time.Now()
	}
	pa.mu.Unlock()

	// Process event
	pa.eventProcessor.QueueEvent(ParticipantEvent{
		Type:        EventTypeNameChanged,
		Participant: participant,
		OldValue:    oldName,
		NewValue:    participant.Name(),
		Timestamp:   time.Now(),
	})

	// Notify handler
	pa.handler.OnParticipantNameChanged(ctx, participant, oldName)
}

// handleTrackPublished handles track publication
func (pa *ParticipantAgent) handleTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	pa.mu.Lock()
	if info, exists := pa.participants[participant.Identity()]; exists {
		info.TrackCount++
		info.LastActivity = time.Now()
	}
	pa.mu.Unlock()

	// Update coordinator
	pa.coordinationManager.UpdateParticipantActivity(participant.Identity(), ActivityTypeTrackPublished)

	// Process event
	pa.eventProcessor.QueueEvent(ParticipantEvent{
		Type:        EventTypeTrackPublished,
		Participant: participant,
		Track:       publication,
		Timestamp:   time.Now(),
	})

	// Notify handler
	pa.handler.OnParticipantTrackPublished(ctx, participant, publication)
}

// handleTrackUnpublished handles track unpublication
func (pa *ParticipantAgent) handleTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	pa.mu.Lock()
	if info, exists := pa.participants[participant.Identity()]; exists {
		info.TrackCount--
		info.LastActivity = time.Now()
	}
	pa.mu.Unlock()

	// Update coordinator
	pa.coordinationManager.UpdateParticipantActivity(participant.Identity(), ActivityTypeTrackUnpublished)

	// Process event
	pa.eventProcessor.QueueEvent(ParticipantEvent{
		Type:        EventTypeTrackUnpublished,
		Participant: participant,
		Track:       publication,
		Timestamp:   time.Now(),
	})

	// Notify handler
	pa.handler.OnParticipantTrackUnpublished(ctx, participant, publication)
}

// handleDataReceived handles data messages
func (pa *ParticipantAgent) handleDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	pa.mu.Lock()
	if info, exists := pa.participants[participant.Identity()]; exists {
		info.DataReceived += int64(len(data))
		info.LastActivity = time.Now()
		info.InteractionCount++
	}
	pa.mu.Unlock()

	// Update coordinator
	pa.coordinationManager.UpdateParticipantActivity(participant.Identity(), ActivityTypeDataReceived)

	// Process event
	pa.eventProcessor.QueueEvent(ParticipantEvent{
		Type:        EventTypeDataReceived,
		Participant: participant,
		Data:        data,
		DataKind:    kind,
		Timestamp:   time.Now(),
	})

	// Notify handler
	pa.handler.OnDataReceived(ctx, data, participant, kind)
}

// handleSpeakingChanged handles speaking state changes
func (pa *ParticipantAgent) handleSpeakingChanged(ctx context.Context, speakers []lksdk.Participant) {
	// Update speaking state for all participants
	pa.mu.Lock()
	// First, mark all as not speaking
	for _, info := range pa.participants {
		info.IsSpeaking = false
	}
	// Then mark speakers
	for _, speaker := range speakers {
		if info, exists := pa.participants[speaker.Identity()]; exists {
			wasSpeaking := info.IsSpeaking
			info.IsSpeaking = true
			info.LastActivity = time.Now()

			// Notify if state changed
			if !wasSpeaking {
				if remoteParticipant, ok := speaker.(*lksdk.RemoteParticipant); ok {
					pa.mu.Unlock()
					pa.handler.OnParticipantSpeakingChanged(ctx, remoteParticipant, true)
					pa.mu.Lock()
				}
			}
		}
	}
	pa.mu.Unlock()
}

// updateParticipantPermissions checks and updates participant permissions
func (pa *ParticipantAgent) updateParticipantPermissions(room *lksdk.Room) {
	for _, participant := range room.GetRemoteParticipants() {
		pa.mu.Lock()
		info, exists := pa.participants[participant.Identity()]
		if !exists {
			pa.mu.Unlock()
			continue
		}
		oldPermissions := info.Permissions
		pa.mu.Unlock()

		currentPermissions := participant.Permissions()
		if !permissionsEqual(oldPermissions, currentPermissions) {
			pa.mu.Lock()
			info.Permissions = currentPermissions
			pa.mu.Unlock()

			// Update permission manager
			pa.permissionManager.UpdateParticipantPermissions(participant.Identity(), currentPermissions)

			// Notify handler
			pa.handler.OnParticipantPermissionsChanged(context.Background(), participant, oldPermissions)
		}
	}
}

// GetTargetParticipant returns the target participant being monitored.
//
// For JT_PARTICIPANT jobs, this is the participant that triggered
// the agent assignment. Returns nil if no target participant is set
// or if the target participant has left the room.
func (pa *ParticipantAgent) GetTargetParticipant() *lksdk.RemoteParticipant {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	return pa.targetParticipant
}

// GetParticipantInfo returns information about a specific participant.
//
// Parameters:
//   - identity: The unique identity of the participant
//
// Returns:
//   - info: Participant information including stats and state
//   - exists: true if the participant is currently in the room
//
// The returned ParticipantInfo is a copy and safe to modify.
func (pa *ParticipantAgent) GetParticipantInfo(identity string) (*ParticipantInfo, bool) {
	pa.mu.RLock()
	defer pa.mu.RUnlock()
	info, exists := pa.participants[identity]
	if !exists {
		return nil, false
	}
	// Return a copy to prevent external modification
	infoCopy := *info
	return &infoCopy, true
}

// GetAllParticipants returns information about all participants.
//
// Returns a map of participant identity to ParticipantInfo.
// The returned map and infos are copies and safe to modify.
//
// This includes all participants except the agent itself.
func (pa *ParticipantAgent) GetAllParticipants() map[string]*ParticipantInfo {
	pa.mu.RLock()
	defer pa.mu.RUnlock()

	result := make(map[string]*ParticipantInfo)
	for k, v := range pa.participants {
		// Copy the info
		infoCopy := *v
		result[k] = &infoCopy
	}
	return result
}

// SendDataToParticipant sends data to a specific participant.
//
// Parameters:
//   - identity: The target participant's identity
//   - data: The data payload to send
//   - reliable: If true, uses reliable transmission (may be slower)
//
// Returns an error if:
//   - The participant is not found
//   - The agent lacks permission to send data
//   - The data transmission fails
//
// Reliable transmission ensures delivery but may have higher latency.
// Unreliable transmission is faster but messages may be lost.
func (pa *ParticipantAgent) SendDataToParticipant(identity string, data []byte, reliable bool) error {
	pa.mu.RLock()
	info, exists := pa.participants[identity]
	room := pa.currentRoom
	pa.mu.RUnlock()

	if !exists {
		return fmt.Errorf("participant %s not found", identity)
	}

	if room == nil {
		return fmt.Errorf("not connected to a room")
	}

	// Check permissions
	if !pa.permissionManager.CanSendDataTo(identity) {
		return fmt.Errorf("no permission to send data to participant %s", identity)
	}

	// Update stats
	pa.mu.Lock()
	info.DataSent += int64(len(data))
	info.InteractionCount++
	pa.mu.Unlock()

	// Send data to specific participant
	opts := []lksdk.DataPublishOption{}
	if reliable {
		opts = append(opts, lksdk.WithDataPublishReliable(reliable))
	}
	opts = append(opts, lksdk.WithDataPublishDestination([]string{identity}))

	err := room.LocalParticipant.PublishData(data, opts...)
	if err != nil {
		return fmt.Errorf("failed to send data to participant %s: %w", identity, err)
	}

	return nil
}

// RequestPermissionChange requests a permission change for a participant.
//
// This is an administrative action that requires appropriate permissions.
// The actual permission change is performed by the LiveKit server.
//
// Parameters:
//   - identity: The target participant's identity
//   - permissions: The new permissions to apply
//
// Returns an error if:
//   - The agent lacks permission management capability
//   - The requested permissions are invalid
//   - The server rejects the change
//
// Note: This may not be immediately reflected in participant state.
func (pa *ParticipantAgent) RequestPermissionChange(identity string, permissions *livekit.ParticipantPermission) error {
	if !pa.permissionManager.CanManagePermissions() {
		return fmt.Errorf("agent does not have permission management capability")
	}

	// Validate permissions
	if err := pa.permissionManager.ValidatePermissions(permissions); err != nil {
		return fmt.Errorf("invalid permissions: %w", err)
	}

	// This would typically make an API call to the LiveKit server
	logger := logger.GetLogger()
	logger.Infow("requesting permission change",
		"identity", identity,
		"permissions", permissions)

	return nil
}

// GetPermissionManager returns the permission manager.
//
// The permission manager handles permission validation and access control
// for participant operations. It can be used to check what actions
// are allowed for specific participants.
func (pa *ParticipantAgent) GetPermissionManager() *ParticipantPermissionManager {
	return pa.permissionManager
}

// GetCoordinationManager returns the coordination manager.
//
// The coordination manager helps coordinate actions across multiple
// participants, useful for implementing features like turn-taking,
// exclusive operations, or synchronized actions.
func (pa *ParticipantAgent) GetCoordinationManager() *MultiParticipantCoordinator {
	return pa.coordinationManager
}

// GetEventProcessor returns the event processor.
//
// The event processor queues and processes participant events
// in order, ensuring consistent event handling even under
// high event rates.
func (pa *ParticipantAgent) GetEventProcessor() *ParticipantEventProcessor {
	return pa.eventProcessor
}

// Stop gracefully stops the participant agent.
//
// This method:
//   - Stops event processing
//   - Cleans up coordination resources
//   - Calls the base Worker.Stop()
//
// Returns nil on success or an error if cleanup fails.
func (pa *ParticipantAgent) Stop() error {
	// Stop event processor
	pa.eventProcessor.Stop()

	// Clean up coordinators
	pa.coordinationManager.Stop()

	// Call base stop
	return pa.Worker.Stop()
}

// participantState tracks the state of a participant for change detection
type participantState struct {
	metadata string
	name     string
	tracks   map[string]*lksdk.RemoteTrackPublication
}

// checkTrackChanges checks for track publication changes
func (pa *ParticipantAgent) checkTrackChanges(ctx context.Context, participant *lksdk.RemoteParticipant, state *participantState) {
	currentTracks := participant.TrackPublications()

	// Check for new tracks
	for _, pub := range currentTracks {
		if remotePub, ok := pub.(*lksdk.RemoteTrackPublication); ok {
			if _, exists := state.tracks[pub.SID()]; !exists {
				// New track published
				state.tracks[pub.SID()] = remotePub
				pa.handleTrackPublished(ctx, participant, remotePub)
			}
		}
	}

	// Check for removed tracks
	for sid, pub := range state.tracks {
		found := false
		for _, currentPub := range currentTracks {
			if currentPub.SID() == sid {
				found = true
				break
			}
		}
		if !found {
			// Track unpublished
			delete(state.tracks, sid)
			pa.handleTrackUnpublished(ctx, participant, pub)
		}
	}
}

// permissionsEqual compares two permission sets
func permissionsEqual(a, b *livekit.ParticipantPermission) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// Compare all fields except CanPublishSources
	if a.CanSubscribe != b.CanSubscribe ||
		a.CanPublish != b.CanPublish ||
		a.CanPublishData != b.CanPublishData ||
		a.Hidden != b.Hidden ||
		a.Recorder != b.Recorder ||
		a.CanUpdateMetadata != b.CanUpdateMetadata ||
		a.Agent != b.Agent {
		return false
	}

	// Compare CanPublishSources slices
	if len(a.CanPublishSources) != len(b.CanPublishSources) {
		return false
	}
	for i, source := range a.CanPublishSources {
		if source != b.CanPublishSources[i] {
			return false
		}
	}
	return true
}
