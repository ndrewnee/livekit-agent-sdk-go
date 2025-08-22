package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockParticipantAgentHandler implements ParticipantAgentHandler for testing
type MockParticipantAgentHandler struct {
	SimpleJobHandler
	participantJoined      int
	participantLeft        int
	metadataChanged        int
	nameChanged            int
	permissionsChanged     int
	speakingChanged        int
	trackPublished         int
	trackUnpublished       int
	dataReceived           int
	
	lastParticipant        *lksdk.RemoteParticipant
	lastOldMetadata        string
	lastOldName            string
	lastOldPermissions     *livekit.ParticipantPermission
	lastSpeaking           bool
	lastPublication        *lksdk.RemoteTrackPublication
	lastData               []byte
	lastDataKind           livekit.DataPacket_Kind
}

func NewMockParticipantAgentHandler() *MockParticipantAgentHandler {
	h := &MockParticipantAgentHandler{}
	// Set up default behavior
	h.OnJob = func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
		return nil
	}
	return h
}

func (h *MockParticipantAgentHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	h.participantJoined++
	h.lastParticipant = participant
}

func (h *MockParticipantAgentHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	h.participantLeft++
	h.lastParticipant = participant
}

func (h *MockParticipantAgentHandler) OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
	h.metadataChanged++
	h.lastParticipant = participant
	h.lastOldMetadata = oldMetadata
}

func (h *MockParticipantAgentHandler) OnParticipantNameChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldName string) {
	h.nameChanged++
	h.lastParticipant = participant
	h.lastOldName = oldName
}

func (h *MockParticipantAgentHandler) OnParticipantPermissionsChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldPermissions *livekit.ParticipantPermission) {
	h.permissionsChanged++
	h.lastParticipant = participant
	h.lastOldPermissions = oldPermissions
}

func (h *MockParticipantAgentHandler) OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool) {
	h.speakingChanged++
	h.lastParticipant = participant
	h.lastSpeaking = speaking
}

func (h *MockParticipantAgentHandler) OnParticipantTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	h.trackPublished++
	h.lastParticipant = participant
	h.lastPublication = publication
}

func (h *MockParticipantAgentHandler) OnParticipantTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	h.trackUnpublished++
	h.lastParticipant = participant
	h.lastPublication = publication
}

func (h *MockParticipantAgentHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	h.dataReceived++
	h.lastParticipant = participant
	h.lastData = data
	h.lastDataKind = kind
}

func TestParticipantAgent_Creation(t *testing.T) {
	handler := NewMockParticipantAgentHandler()
	
	opts := WorkerOptions{}
	
	agent, err := NewParticipantAgent("ws://localhost:7880", "test-key", "test-secret", handler, opts)
	require.NoError(t, err)
	require.NotNil(t, agent)
	
	// Verify job type is set correctly
	require.Equal(t, livekit.JobType_JT_PARTICIPANT, agent.Worker.opts.JobType)
	
	// Verify components are initialized
	require.NotNil(t, agent.permissionManager)
	require.NotNil(t, agent.coordinationManager)
	require.NotNil(t, agent.eventProcessor)
	require.NotNil(t, agent.participants)
}

func TestParticipantPermissionManager(t *testing.T) {
	manager := NewParticipantPermissionManager()
	
	// Test default permissions
	perms := manager.GetParticipantPermissions("test-participant")
	assert.True(t, perms.CanSubscribe)
	assert.False(t, perms.CanPublish)
	assert.True(t, perms.CanPublishData)
	
	// Test permission update
	newPerms := &livekit.ParticipantPermission{
		CanSubscribe:   true,
		CanPublish:     true,
		CanPublishData: true,
	}
	manager.UpdateParticipantPermissions("test-participant", newPerms)
	
	updatedPerms := manager.GetParticipantPermissions("test-participant")
	assert.True(t, updatedPerms.CanPublish)
	
	// Test permission validation
	err := manager.ValidatePermissions(newPerms)
	assert.NoError(t, err)
	
	// Test invalid permissions
	err = manager.ValidatePermissions(nil)
	assert.Error(t, err)
	
	// Test can send data
	assert.True(t, manager.CanSendDataTo("test-participant"))
	
	// Test custom restriction
	manager.SetCustomRestriction("test-participant", "no_data_receive", true)
	assert.False(t, manager.CanSendDataTo("test-participant"))
	
	// Test permission request
	requestedPerms := &livekit.ParticipantPermission{
		CanSubscribe:   true,
		CanPublish:     false,
		CanPublishData: false,
	}
	approved, err := manager.RequestPermissionChange("test-participant", requestedPerms)
	assert.NoError(t, err)
	assert.True(t, approved)
	
	// Test permission history
	history := manager.GetPermissionHistory("test-participant")
	assert.NotEmpty(t, history)
}

func TestRoleBasedPolicy(t *testing.T) {
	policy := NewRoleBasedPolicy()
	
	current := &livekit.ParticipantPermission{
		CanSubscribe: true,
		CanPublish:   false,
	}
	
	requested := &livekit.ParticipantPermission{
		CanSubscribe: true,
		CanPublish:   true,
	}
	
	approved, reason := policy.EvaluatePermissionRequest("test-participant", current, requested)
	assert.True(t, approved)
	assert.Equal(t, "role_based_approval", reason)
	
	defaultPerms := policy.GetDefaultPermissions("test-participant")
	assert.NotNil(t, defaultPerms)
	assert.True(t, defaultPerms.CanSubscribe)
	assert.False(t, defaultPerms.CanPublish)
}

func TestTimeBasedPolicy(t *testing.T) {
	// Create policy that allows publishing only during current hour
	currentHour := time.Now().Hour()
	policy := NewTimeBasedPolicy(currentHour, currentHour+1)
	
	current := &livekit.ParticipantPermission{
		CanSubscribe: true,
		CanPublish:   false,
	}
	
	requested := &livekit.ParticipantPermission{
		CanSubscribe: true,
		CanPublish:   true,
	}
	
	// Should approve during allowed hours
	approved, _ := policy.EvaluatePermissionRequest("test-participant", current, requested)
	assert.True(t, approved)
	
	// Test outside allowed hours
	restrictivePolicy := NewTimeBasedPolicy(25, 26) // Invalid hours
	approved, reason := restrictivePolicy.EvaluatePermissionRequest("test-participant", current, requested)
	assert.False(t, approved)
	assert.Contains(t, reason, "not allowed outside hours")
}

func TestMultiParticipantCoordinator(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()
	
	// Mock participant
	participant := &lksdk.RemoteParticipant{}
	
	// Test participant registration
	coordinator.RegisterParticipant("participant1", participant)
	
	// Test activity update
	coordinator.UpdateParticipantActivity("participant1", ActivityTypeJoined)
	
	// Test getting active participants
	active := coordinator.GetActiveParticipants()
	assert.Len(t, active, 1)
	
	// Test group creation
	group, err := coordinator.CreateGroup("group1", "Test Group", map[string]interface{}{
		"type": "test",
	})
	assert.NoError(t, err)
	assert.NotNil(t, group)
	
	// Test adding participant to group
	err = coordinator.AddParticipantToGroup("participant1", "group1")
	assert.NoError(t, err)
	
	// Test getting participant groups
	groups := coordinator.GetParticipantGroups("participant1")
	assert.Len(t, groups, 1)
	assert.Equal(t, "group1", groups[0].ID)
	
	// Test interaction recording
	coordinator.RecordInteraction("participant1", "participant2", "chat", nil)
	
	// Test interaction graph
	graph := coordinator.GetInteractionGraph()
	assert.NotNil(t, graph)
	
	// Test removing participant from group
	err = coordinator.RemoveParticipantFromGroup("participant1", "group1")
	assert.NoError(t, err)
	
	// Test unregistering participant
	coordinator.UnregisterParticipant("participant1")
	active = coordinator.GetActiveParticipants()
	assert.Len(t, active, 0)
}

func TestProximityRule(t *testing.T) {
	rule := NewProximityRule(5, 1*time.Minute)
	assert.Equal(t, "proximity_grouping", rule.GetName())
	
	participants := make(map[string]*CoordinatedParticipant)
	applies, actions := rule.Evaluate(participants)
	assert.False(t, applies)
	assert.Empty(t, actions)
}

func TestActivityBasedRule(t *testing.T) {
	rule := NewActivityBasedRule(10, 5*time.Minute)
	
	// Create participant with high activity
	participant := &CoordinatedParticipant{
		Identity:        "active-participant",
		ActivityHistory: make([]ParticipantActivity, 0),
	}
	
	// Add recent activities
	for i := 0; i < 15; i++ {
		participant.ActivityHistory = append(participant.ActivityHistory, ParticipantActivity{
			Type:      ActivityTypeSpeaking,
			Timestamp: time.Now(),
		})
	}
	
	participants := map[string]*CoordinatedParticipant{
		"active-participant": participant,
	}
	
	applies, actions := rule.Evaluate(participants)
	assert.True(t, applies)
	assert.NotEmpty(t, actions)
	assert.Equal(t, "notify", actions[0].Type)
}

func TestSizeBasedGroupRule(t *testing.T) {
	rule := NewSizeBasedGroupRule(5)
	
	participant := &CoordinatedParticipant{Identity: "test"}
	
	group := &ParticipantGroup{
		ID:           "test-group",
		Participants: make(map[string]bool),
	}
	
	// Should allow joining empty group
	assert.True(t, rule.CanJoinGroup(participant, group))
	
	// Fill group to capacity
	for i := 0; i < 5; i++ {
		group.Participants[string(rune('a'+i))] = true
	}
	
	// Should not allow joining full group
	assert.False(t, rule.CanJoinGroup(participant, group))
}

func TestParticipantEventProcessor(t *testing.T) {
	processor := NewParticipantEventProcessor()
	defer processor.Stop()
	
	// Test event handler registration
	handlerCalled := false
	processor.RegisterHandler(EventTypeParticipantJoined, func(event ParticipantEvent) error {
		handlerCalled = true
		return nil
	})
	
	// Queue event
	event := ParticipantEvent{
		Type:        EventTypeParticipantJoined,
		Participant: &lksdk.RemoteParticipant{},
		Timestamp:   time.Now(),
	}
	processor.QueueEvent(event)
	
	// Process pending events
	processor.ProcessPendingEvents()
	
	// Give a bit of time for the background processor to handle it
	time.Sleep(50 * time.Millisecond)
	assert.True(t, handlerCalled)
	
	// Test event history
	history := processor.GetEventHistory(10)
	assert.Len(t, history, 1)
	
	// Test metrics
	metrics := processor.GetMetrics()
	assert.Equal(t, int64(1), metrics["total_events"])
	assert.Equal(t, int64(1), metrics["processed_events"])
}

func TestThrottleFilter(t *testing.T) {
	filter := NewThrottleFilter(5) // Max 5 events per minute
	
	participant := &lksdk.RemoteParticipant{}
	
	// First 5 events should pass
	for i := 0; i < 5; i++ {
		event := ParticipantEvent{
			Type:        EventTypeDataReceived,
			Participant: participant,
			Timestamp:   time.Now(),
		}
		assert.True(t, filter.Filter(event))
	}
	
	// 6th event should be throttled
	event := ParticipantEvent{
		Type:        EventTypeDataReceived,
		Participant: participant,
		Timestamp:   time.Now(),
	}
	assert.False(t, filter.Filter(event))
}

func TestMetricsBatchProcessor(t *testing.T) {
	batchReceived := false
	var receivedEvents []ParticipantEvent
	
	processor := NewMetricsBatchProcessor(func(events []ParticipantEvent) {
		batchReceived = true
		receivedEvents = events
	})
	
	// Test configuration
	assert.Equal(t, 50, processor.GetBatchSize())
	assert.Equal(t, 5*time.Second, processor.GetBatchTimeout())
	
	// Test should batch
	event := ParticipantEvent{
		Type:      EventTypeDataReceived,
		Timestamp: time.Now(),
	}
	assert.True(t, processor.ShouldBatch(event))
	
	// Test batch processing
	events := []ParticipantEvent{event}
	err := processor.ProcessBatch(events)
	assert.NoError(t, err)
	assert.True(t, batchReceived)
	assert.Len(t, receivedEvents, 1)
}

func TestParticipantAgent_Operations(t *testing.T) {
	handler := NewMockParticipantAgentHandler()
	
	opts := WorkerOptions{}
	
	agent, err := NewParticipantAgent("ws://localhost:7880", "test-key", "test-secret", handler, opts)
	require.NoError(t, err)
	
	// Test getting participant info
	info, exists := agent.GetParticipantInfo("non-existent")
	assert.False(t, exists)
	assert.Nil(t, info)
	
	// Test send data (would fail without real participant)
	err = agent.SendDataToParticipant("non-existent", []byte("test"), true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	
	// Test permission request
	perms := &livekit.ParticipantPermission{
		CanPublish: true,
	}
	err = agent.RequestPermissionChange("test-participant", perms)
	assert.NoError(t, err)
	
	// Test getting all participants
	all := agent.GetAllParticipants()
	assert.Empty(t, all)
}

func TestParticipantAgent_EventHandling(t *testing.T) {
	handler := NewMockParticipantAgentHandler()
	
	opts := WorkerOptions{}
	
	agent, err := NewParticipantAgent("ws://localhost:7880", "test-key", "test-secret", handler, opts)
	require.NoError(t, err)
	
	ctx := context.Background()
	participant := &lksdk.RemoteParticipant{}
	
	// Simulate participant joined
	agent.handleParticipantJoined(ctx, participant)
	assert.Equal(t, 1, handler.participantJoined)
	
	// Simulate metadata change
	agent.handleMetadataChanged(ctx, participant, "old-metadata")
	assert.Equal(t, 1, handler.metadataChanged)
	assert.Equal(t, "old-metadata", handler.lastOldMetadata)
	
	// Simulate name change
	agent.handleNameChanged(ctx, participant, "old-name")
	assert.Equal(t, 1, handler.nameChanged)
	assert.Equal(t, "old-name", handler.lastOldName)
	
	// Simulate data received
	data := []byte("test data")
	agent.handleDataReceived(ctx, data, participant, livekit.DataPacket_RELIABLE)
	assert.Equal(t, 1, handler.dataReceived)
	assert.Equal(t, data, handler.lastData)
	assert.Equal(t, livekit.DataPacket_RELIABLE, handler.lastDataKind)
	
	// Simulate participant left
	agent.handleParticipantLeft(ctx, participant)
	assert.Equal(t, 1, handler.participantLeft)
}

func TestParticipantAgent_Cleanup(t *testing.T) {
	handler := NewMockParticipantAgentHandler()
	
	opts := WorkerOptions{}
	
	agent, err := NewParticipantAgent("ws://localhost:7880", "test-key", "test-secret", handler, opts)
	require.NoError(t, err)
	
	// Test stop
	err = agent.Stop()
	assert.NoError(t, err)
}

func TestPermissionsEqual(t *testing.T) {
	// Test nil cases
	assert.True(t, permissionsEqual(nil, nil))
	assert.False(t, permissionsEqual(nil, &livekit.ParticipantPermission{}))
	assert.False(t, permissionsEqual(&livekit.ParticipantPermission{}, nil))
	
	// Test equal permissions
	p1 := &livekit.ParticipantPermission{
		CanSubscribe:   true,
		CanPublish:     true,
		CanPublishData: true,
	}
	p2 := &livekit.ParticipantPermission{
		CanSubscribe:   true,
		CanPublish:     true,
		CanPublishData: true,
	}
	assert.True(t, permissionsEqual(p1, p2))
	
	// Test different permissions
	p2.CanPublish = false
	assert.False(t, permissionsEqual(p1, p2))
}