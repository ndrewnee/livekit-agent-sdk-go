package agent

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiParticipantCoordinatorCreation tests coordinator creation
func TestMultiParticipantCoordinatorCreation(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()
	require.NotNil(t, coordinator)

	assert.NotNil(t, coordinator.participants)
	assert.NotNil(t, coordinator.groups)
	assert.NotNil(t, coordinator.interactions)
	assert.NotNil(t, coordinator.coordinationRules)
	assert.NotNil(t, coordinator.eventHandlers)
	assert.Equal(t, 30*time.Second, coordinator.activityThreshold)
	assert.Equal(t, 5*time.Minute, coordinator.interactionWindow)
}

// TestMultiParticipantCoordinatorRegisterParticipant tests registering participants
func TestMultiParticipantCoordinatorRegisterParticipant(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participant with nil RemoteParticipant (allowed)
	coordinator.RegisterParticipant("user1", nil)

	// Verify participant was registered
	coordinator.mu.RLock()
	coordParticipant, exists := coordinator.participants["user1"]
	coordinator.mu.RUnlock()

	assert.True(t, exists)
	assert.Equal(t, "user1", coordParticipant.Identity)
	assert.Nil(t, coordParticipant.Participant)
}

// TestMultiParticipantCoordinatorUnregisterParticipant tests unregistering participants
func TestMultiParticipantCoordinatorUnregisterParticipant(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register then unregister
	coordinator.RegisterParticipant("user2", nil)
	coordinator.UnregisterParticipant("user2")

	// Verify participant was removed
	coordinator.mu.RLock()
	_, exists := coordinator.participants["user2"]
	coordinator.mu.RUnlock()

	assert.False(t, exists)
}

// TestMultiParticipantCoordinatorCreateGroup tests group creation
func TestMultiParticipantCoordinatorCreateGroup(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Create group
	metadata := map[string]interface{}{
		"type":       "discussion",
		"maxMembers": 10,
	}

	group, err := coordinator.CreateGroup("discussion-group", "Discussion Group", metadata)
	assert.NoError(t, err)
	assert.NotNil(t, group)

	assert.Equal(t, "discussion-group", group.ID)
	assert.Equal(t, "Discussion Group", group.Name)
	assert.Equal(t, metadata, group.Metadata)
}

// TestMultiParticipantCoordinatorAddParticipantToGroup tests adding participants to groups
func TestMultiParticipantCoordinatorAddParticipantToGroup(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participants
	for i := 1; i <= 3; i++ {
		coordinator.RegisterParticipant(fmt.Sprintf("user%d", i), nil)
	}

	// Create group
	group, _ := coordinator.CreateGroup("test-group", "Test Group", nil)

	// Add participants to group
	err := coordinator.AddParticipantToGroup("user1", group.ID)
	assert.NoError(t, err)

	err = coordinator.AddParticipantToGroup("user2", group.ID)
	assert.NoError(t, err)

	// Get group members
	members := coordinator.GetGroupMembers(group.ID)
	assert.Len(t, members, 2)
	assert.Contains(t, members, "user1")
	assert.Contains(t, members, "user2")
}

// TestMultiParticipantCoordinatorRemoveParticipantFromGroup tests removing from groups
func TestMultiParticipantCoordinatorRemoveParticipantFromGroup(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participant
	coordinator.RegisterParticipant("user1", nil)

	// Create group and add participant
	group, _ := coordinator.CreateGroup("temp-group", "Temp Group", nil)
	coordinator.AddParticipantToGroup("user1", group.ID)

	// Remove participant from group
	err := coordinator.RemoveParticipantFromGroup("user1", group.ID)
	assert.NoError(t, err)

	// Verify participant was removed
	members := coordinator.GetGroupMembers(group.ID)
	assert.Empty(t, members)
}

// TestMultiParticipantCoordinatorAddCoordinationRule tests adding rules
func TestMultiParticipantCoordinatorAddCoordinationRule(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Use the built-in ActivityBasedRule
	rule := NewActivityBasedRule(5, 1*time.Minute)

	coordinator.AddCoordinationRule(rule)

	coordinator.mu.RLock()
	assert.Len(t, coordinator.coordinationRules, 1)
	assert.Equal(t, "activity_based", coordinator.coordinationRules[0].GetName())
	coordinator.mu.RUnlock()
}

// TestMultiParticipantCoordinatorRecordInteraction tests interaction recording
func TestMultiParticipantCoordinatorRecordInteraction(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participants
	coordinator.RegisterParticipant("user1", nil)
	coordinator.RegisterParticipant("user2", nil)

	// Record interaction
	coordinator.RecordInteraction("user1", "user2", "speaking", nil)

	coordinator.mu.RLock()
	assert.Len(t, coordinator.interactions, 1)
	interaction := coordinator.interactions[0]
	coordinator.mu.RUnlock()

	assert.Equal(t, "user1", interaction.From)
	assert.Equal(t, "user2", interaction.To)
	assert.Equal(t, "speaking", interaction.Type)
}

// TestMultiParticipantCoordinatorGetActiveParticipants tests getting active participants
func TestMultiParticipantCoordinatorGetActiveParticipants(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participants
	coordinator.RegisterParticipant("active_user", nil)
	coordinator.RegisterParticipant("inactive_user", nil)

	// Update activity for active participant
	coordinator.UpdateParticipantActivity("active_user", ActivityTypeSpeaking)

	// Set inactive participant's last activity to old time
	coordinator.mu.Lock()
	if p, exists := coordinator.participants["inactive_user"]; exists {
		p.LastActivity = time.Now().Add(-2 * time.Hour)
	}
	coordinator.mu.Unlock()

	active := coordinator.GetActiveParticipants()

	// Should only include recently active participant
	hasActive := false
	for _, p := range active {
		if p.Identity == "active_user" {
			hasActive = true
		}
	}
	assert.True(t, hasActive)
}

// TestMultiParticipantCoordinatorGetParticipantGroups tests getting participant's groups
func TestMultiParticipantCoordinatorGetParticipantGroups(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participants
	for i := 1; i <= 3; i++ {
		coordinator.RegisterParticipant(fmt.Sprintf("user%d", i), nil)
	}

	// Create groups and add participants
	group1, _ := coordinator.CreateGroup("group1", "Group 1", nil)
	group2, _ := coordinator.CreateGroup("group2", "Group 2", nil)

	coordinator.AddParticipantToGroup("user1", group1.ID)
	coordinator.AddParticipantToGroup("user1", group2.ID)
	coordinator.AddParticipantToGroup("user2", group1.ID)

	// Get groups for user1
	groups := coordinator.GetParticipantGroups("user1")
	assert.Len(t, groups, 2)

	// Get groups for user2
	groups = coordinator.GetParticipantGroups("user2")
	assert.Len(t, groups, 1)

	// Get groups for non-existent participant
	groups = coordinator.GetParticipantGroups("user999")
	assert.Empty(t, groups)
}

// TestMultiParticipantCoordinatorRegisterEventHandler tests event handler registration
func TestMultiParticipantCoordinatorRegisterEventHandler(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	var handlerCalled atomic.Bool
	handler := func(event CoordinationEvent) {
		handlerCalled.Store(true)
	}

	coordinator.RegisterEventHandler("group_created", handler)

	coordinator.mu.RLock()
	handlers, exists := coordinator.eventHandlers["group_created"]
	coordinator.mu.RUnlock()

	assert.True(t, exists)
	assert.Len(t, handlers, 1)
}

// TestMultiParticipantCoordinatorGetInteractionGraph tests interaction graph
func TestMultiParticipantCoordinatorGetInteractionGraph(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participants
	for i := 1; i <= 3; i++ {
		coordinator.RegisterParticipant(fmt.Sprintf("user%d", i), nil)
	}

	// Record interactions
	coordinator.RecordInteraction("user1", "user2", "speaking", nil)
	coordinator.RecordInteraction("user1", "user3", "speaking", nil)
	coordinator.RecordInteraction("user2", "user3", "chat", nil)
	coordinator.RecordInteraction("user1", "user2", "speaking", nil) // Duplicate

	graph := coordinator.GetInteractionGraph()
	assert.NotNil(t, graph)

	// Check interaction counts
	assert.Equal(t, 2, graph["user1"]["user2"]) // Two speaking interactions
	assert.Equal(t, 1, graph["user1"]["user3"])
	assert.Equal(t, 1, graph["user2"]["user3"])
}

// TestMultiParticipantCoordinatorUpdateParticipantActivity tests activity updates
func TestMultiParticipantCoordinatorUpdateParticipantActivity(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	coordinator.RegisterParticipant("user_active", nil)

	// Update activity
	coordinator.UpdateParticipantActivity("user_active", ActivityTypeSpeaking)

	coordinator.mu.RLock()
	p, exists := coordinator.participants["user_active"]
	coordinator.mu.RUnlock()

	assert.True(t, exists)
	assert.WithinDuration(t, time.Now(), p.LastActivity, time.Second)
	assert.Equal(t, 1, p.ActivityCount)
	assert.Len(t, p.ActivityHistory, 1)
	assert.Equal(t, ActivityTypeSpeaking, p.ActivityHistory[0].Type)
}

// TestMultiParticipantCoordinatorConcurrentOperations tests concurrent access
func TestMultiParticipantCoordinatorConcurrentOperations(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	var wg sync.WaitGroup
	numGoroutines := 20

	// Concurrent participant registrations
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			coordinator.RegisterParticipant(fmt.Sprintf("user%d", id), nil)
		}(i)
	}

	// Concurrent group creations
	wg.Add(numGoroutines / 2)
	for i := 0; i < numGoroutines/2; i++ {
		go func(id int) {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond) // Let participants be registered first
			coordinator.CreateGroup(fmt.Sprintf("group_%d", id), fmt.Sprintf("Group %d", id), nil)
		}(i)
	}

	// Concurrent interaction recordings
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			time.Sleep(5 * time.Millisecond) // Let participants be registered first
			fromID := fmt.Sprintf("user%d", id%numGoroutines)
			toID := fmt.Sprintf("user%d", (id+1)%numGoroutines)
			coordinator.RecordInteraction(fromID, toID, "speaking", nil)
		}(i)
	}

	wg.Wait()

	// Verify state consistency
	coordinator.mu.RLock()
	assert.GreaterOrEqual(t, len(coordinator.participants), numGoroutines)
	assert.GreaterOrEqual(t, len(coordinator.groups), numGoroutines/2)
	assert.GreaterOrEqual(t, len(coordinator.interactions), numGoroutines)
	coordinator.mu.RUnlock()
}

// TestMultiParticipantCoordinatorGetActivityMetrics tests metrics collection
func TestMultiParticipantCoordinatorGetActivityMetrics(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participants and update activities
	for i := 1; i <= 5; i++ {
		coordinator.RegisterParticipant(fmt.Sprintf("user%d", i), nil)

		// Update activity with different types
		if i%2 == 0 {
			coordinator.UpdateParticipantActivity(fmt.Sprintf("user%d", i), ActivityTypeSpeaking)
		} else {
			coordinator.UpdateParticipantActivity(fmt.Sprintf("user%d", i), ActivityTypeJoined)
		}
	}

	metrics := coordinator.GetActivityMetrics()

	assert.Equal(t, int64(5), metrics.TotalActivities)
	assert.Equal(t, 5, metrics.ActiveParticipants)
	assert.Equal(t, int64(2), metrics.ActivitiesByType[ActivityTypeSpeaking])
	assert.Equal(t, int64(3), metrics.ActivitiesByType[ActivityTypeJoined])
}

// TestMultiParticipantCoordinatorGetParticipantInteractions tests getting participant interactions
func TestMultiParticipantCoordinatorGetParticipantInteractions(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register participants
	for i := 1; i <= 3; i++ {
		coordinator.RegisterParticipant(fmt.Sprintf("user%d", i), nil)
	}

	// Record interactions
	coordinator.RecordInteraction("user1", "user2", "speaking", nil)
	coordinator.RecordInteraction("user1", "user3", "chat", nil)
	coordinator.RecordInteraction("user2", "user1", "response", nil)

	// Get interactions for user1
	interactions := coordinator.GetParticipantInteractions("user1")

	// Should include interactions where user1 is either sender or receiver
	assert.Equal(t, 3, len(interactions))
}

// TestMultiParticipantCoordinatorStop tests stopping the coordinator
func TestMultiParticipantCoordinatorStop(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Register some participants
	coordinator.RegisterParticipant("user1", nil)

	// Stop the coordinator
	coordinator.Stop()

	// Verify cleanup
	coordinator.mu.RLock()
	assert.Empty(t, coordinator.participants)
	assert.Empty(t, coordinator.groups)
	assert.Nil(t, coordinator.interactions)
	coordinator.mu.RUnlock()
}

// TestMultiParticipantCoordinatorBidirectionalInteraction tests bidirectional interactions
func TestMultiParticipantCoordinatorBidirectionalInteraction(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	coordinator.RegisterParticipant("user1", nil)
	coordinator.RegisterParticipant("user2", nil)

	// Record interaction from user1 to user2
	coordinator.RecordInteraction("user1", "user2", "message", nil)

	// Record reverse interaction within window
	coordinator.RecordInteraction("user2", "user1", "message", nil)

	coordinator.mu.RLock()
	interactions := coordinator.interactions
	coordinator.mu.RUnlock()

	// Should have recorded both interactions
	assert.Len(t, interactions, 2)
	assert.Equal(t, "user1", interactions[0].From)
	assert.Equal(t, "user2", interactions[0].To)
	assert.Equal(t, "user2", interactions[1].From)
	assert.Equal(t, "user1", interactions[1].To)
}

// TestMultiParticipantCoordinatorInteractionWindow tests interaction window cleanup
func TestMultiParticipantCoordinatorInteractionWindow(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()
	coordinator.interactionWindow = 100 * time.Millisecond

	coordinator.RegisterParticipant("user1", nil)
	coordinator.RegisterParticipant("user2", nil)

	// Record old interaction
	coordinator.RecordInteraction("user1", "user2", "old", nil)

	// Make it old
	coordinator.mu.Lock()
	if len(coordinator.interactions) > 0 {
		coordinator.interactions[0].Timestamp = time.Now().Add(-200 * time.Millisecond)
	}
	coordinator.mu.Unlock()

	// Record new interaction (should trigger cleanup)
	coordinator.RecordInteraction("user1", "user2", "new", nil)

	coordinator.mu.RLock()
	defer coordinator.mu.RUnlock()

	// Old interaction should be removed
	assert.Len(t, coordinator.interactions, 1)
	assert.Equal(t, "new", coordinator.interactions[0].Type)
}

// TestMultiParticipantCoordinatorActivityHistory tests activity history limit
func TestMultiParticipantCoordinatorActivityHistory(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()
	coordinator.RegisterParticipant("user1", nil)

	// Add more than 50 activities
	for i := 0; i < 60; i++ {
		coordinator.UpdateParticipantActivity("user1", ActivityTypeSpeaking)
	}

	coordinator.mu.RLock()
	participant := coordinator.participants["user1"]
	coordinator.mu.RUnlock()

	// Should keep only last 50
	assert.Len(t, participant.ActivityHistory, 50)
	assert.Equal(t, 60, participant.ActivityCount)
}

// TestProximityRule tests proximity-based grouping rule
func TestProximityRule(t *testing.T) {
	rule := NewProximityRule(5, 1*time.Minute)
	assert.Equal(t, "proximity_grouping", rule.GetName())

	// Test evaluation (simplified - returns false in current implementation)
	participants := make(map[string]*CoordinatedParticipant)
	applies, actions := rule.Evaluate(participants)
	assert.False(t, applies)
	assert.Nil(t, actions)
}

// TestActivityBasedRule tests activity-based rule
func TestActivityBasedRule(t *testing.T) {
	rule := NewActivityBasedRule(2, 1*time.Minute)
	assert.Equal(t, "activity_based", rule.GetName())

	participants := map[string]*CoordinatedParticipant{
		"user1": {
			Identity: "user1",
			ActivityHistory: []ParticipantActivity{
				{Type: ActivityTypeSpeaking, Timestamp: time.Now()},
				{Type: ActivityTypeSpeaking, Timestamp: time.Now()},
				{Type: ActivityTypeSpeaking, Timestamp: time.Now()},
			},
		},
	}

	applies, actions := rule.Evaluate(participants)
	assert.True(t, applies)
	assert.Len(t, actions, 1)
	assert.Equal(t, "notify", actions[0].Type)
	assert.Equal(t, "high_activity", actions[0].Parameters["reason"])
}

// TestSizeBasedGroupRule tests size-based group rule
func TestSizeBasedGroupRule(t *testing.T) {
	rule := NewSizeBasedGroupRule(3)

	participant := &CoordinatedParticipant{Identity: "user1"}

	// Group with space
	group := &ParticipantGroup{
		ID:           "group1",
		Participants: map[string]bool{"user2": true, "user3": true},
	}
	assert.True(t, rule.CanJoinGroup(participant, group))

	// Full group
	group.Participants["user4"] = true
	assert.False(t, rule.CanJoinGroup(participant, group))

	// Test OnGroupChange (just for coverage)
	rule.OnGroupChange(group, []string{"user5"}, []string{"user1"})
}

// TestMultiParticipantCoordinatorGroupRules tests group rules
func TestMultiParticipantCoordinatorGroupRules(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	coordinator.RegisterParticipant("user1", nil)
	coordinator.RegisterParticipant("user2", nil)
	coordinator.RegisterParticipant("user3", nil)

	group, _ := coordinator.CreateGroup("limited", "Limited Group", nil)

	// Add size-based rule
	rule := NewSizeBasedGroupRule(2)
	group.Rules = append(group.Rules, rule)

	// Add first two participants (should succeed)
	err := coordinator.AddParticipantToGroup("user1", "limited")
	assert.NoError(t, err)

	err = coordinator.AddParticipantToGroup("user2", "limited")
	assert.NoError(t, err)

	// Third should fail due to size limit
	err = coordinator.AddParticipantToGroup("user3", "limited")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot join group")
}

// TestMultiParticipantCoordinatorDuplicateGroup tests duplicate group creation
func TestMultiParticipantCoordinatorDuplicateGroup(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Create first group
	_, err := coordinator.CreateGroup("group1", "Group 1", nil)
	assert.NoError(t, err)

	// Try to create duplicate
	_, err = coordinator.CreateGroup("group1", "Another Group", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

// TestMultiParticipantCoordinatorInvalidOperations tests error cases
func TestMultiParticipantCoordinatorInvalidOperations(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()

	// Try to add unknown participant to group
	coordinator.CreateGroup("group1", "Group 1", nil)
	err := coordinator.AddParticipantToGroup("unknown", "group1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Try to add participant to unknown group
	coordinator.RegisterParticipant("user1", nil)
	err = coordinator.AddParticipantToGroup("user1", "unknown")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Try to remove unknown participant from group
	err = coordinator.RemoveParticipantFromGroup("unknown", "group1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
