package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// ActivityType represents the type of participant activity
type ActivityType string

const (
	ActivityTypeJoined           ActivityType = "joined"
	ActivityTypeLeft             ActivityType = "left"
	ActivityTypeTrackPublished   ActivityType = "track_published"
	ActivityTypeTrackUnpublished ActivityType = "track_unpublished"
	ActivityTypeDataReceived     ActivityType = "data_received"
	ActivityTypeSpeaking         ActivityType = "speaking"
	ActivityTypeMetadataChanged  ActivityType = "metadata_changed"
)

// MultiParticipantCoordinator coordinates activities across multiple participants
type MultiParticipantCoordinator struct {
	mu                sync.RWMutex
	participants      map[string]*CoordinatedParticipant
	groups            map[string]*ParticipantGroup
	interactions      []ParticipantInteraction
	coordinationRules []CoordinationRule
	eventHandlers     map[string][]CoordinationEventHandler
	activityThreshold time.Duration
	interactionWindow time.Duration
}

// CoordinatedParticipant represents a participant in the coordination system
type CoordinatedParticipant struct {
	Identity        string
	Participant     *lksdk.RemoteParticipant
	JoinedAt        time.Time
	LastActivity    time.Time
	ActivityCount   int
	Groups          []string
	State           map[string]interface{}
	ActivityHistory []ParticipantActivity
}

// ParticipantActivity represents an activity by a participant
type ParticipantActivity struct {
	Type      ActivityType
	Timestamp time.Time
	Details   map[string]interface{}
}

// ParticipantGroup represents a group of participants
type ParticipantGroup struct {
	ID           string
	Name         string
	Participants map[string]bool
	Created      time.Time
	Metadata     map[string]interface{}
	Rules        []GroupRule
}

// ParticipantInteraction represents an interaction between participants
type ParticipantInteraction struct {
	From          string
	To            string
	Type          string
	Timestamp     time.Time
	Bidirectional bool
	Data          interface{}
}

// CoordinationRule defines rules for participant coordination
type CoordinationRule interface {
	// Evaluate evaluates if the rule applies to the given participants
	Evaluate(participants map[string]*CoordinatedParticipant) (bool, []CoordinationAction)

	// GetName returns the rule name
	GetName() string
}

// CoordinationAction represents an action to take based on coordination rules
type CoordinationAction struct {
	Type       string
	Target     string
	Parameters map[string]interface{}
}

// GroupRule defines rules for participant groups
type GroupRule interface {
	// CanJoinGroup checks if a participant can join the group
	CanJoinGroup(participant *CoordinatedParticipant, group *ParticipantGroup) bool

	// OnGroupChange is called when group membership changes
	OnGroupChange(group *ParticipantGroup, added, removed []string)
}

// CoordinationEventHandler handles coordination events
type CoordinationEventHandler func(event CoordinationEvent)

// CoordinationEvent represents a coordination event
type CoordinationEvent struct {
	Type         string
	Timestamp    time.Time
	Participants []string
	Data         map[string]interface{}
}

// NewMultiParticipantCoordinator creates a new coordinator
func NewMultiParticipantCoordinator() *MultiParticipantCoordinator {
	return &MultiParticipantCoordinator{
		participants:      make(map[string]*CoordinatedParticipant),
		groups:            make(map[string]*ParticipantGroup),
		interactions:      make([]ParticipantInteraction, 0),
		coordinationRules: make([]CoordinationRule, 0),
		eventHandlers:     make(map[string][]CoordinationEventHandler),
		activityThreshold: 30 * time.Second,
		interactionWindow: 5 * time.Minute,
	}
}

// RegisterParticipant registers a participant with the coordinator
func (c *MultiParticipantCoordinator) RegisterParticipant(identity string, participant *lksdk.RemoteParticipant) {
	c.mu.Lock()
	defer c.mu.Unlock()

	coordParticipant := &CoordinatedParticipant{
		Identity:        identity,
		Participant:     participant,
		JoinedAt:        time.Now(),
		LastActivity:    time.Now(),
		Groups:          make([]string, 0),
		State:           make(map[string]interface{}),
		ActivityHistory: make([]ParticipantActivity, 0),
	}

	c.participants[identity] = coordParticipant

	// Check auto-grouping rules
	c.checkAutoGrouping(coordParticipant)

	// Emit event
	c.emitEvent("participant_registered", CoordinationEvent{
		Type:         "participant_registered",
		Timestamp:    time.Now(),
		Participants: []string{identity},
		Data:         map[string]interface{}{"participant": participant},
	})
}

// UnregisterParticipant removes a participant from coordination
func (c *MultiParticipantCoordinator) UnregisterParticipant(identity string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	participant, exists := c.participants[identity]
	if !exists {
		return
	}

	// Remove from all groups
	for _, groupID := range participant.Groups {
		if group, exists := c.groups[groupID]; exists {
			delete(group.Participants, identity)

			// Check if group should be disbanded
			if len(group.Participants) == 0 {
				delete(c.groups, groupID)
			}
		}
	}

	delete(c.participants, identity)

	// Emit event
	c.emitEvent("participant_unregistered", CoordinationEvent{
		Type:         "participant_unregistered",
		Timestamp:    time.Now(),
		Participants: []string{identity},
	})
}

// UpdateParticipantActivity updates activity for a participant
func (c *MultiParticipantCoordinator) UpdateParticipantActivity(identity string, activityType ActivityType) {
	c.mu.Lock()
	defer c.mu.Unlock()

	participant, exists := c.participants[identity]
	if !exists {
		return
	}

	participant.LastActivity = time.Now()
	participant.ActivityCount++

	// Record activity
	activity := ParticipantActivity{
		Type:      activityType,
		Timestamp: time.Now(),
		Details:   make(map[string]interface{}),
	}
	participant.ActivityHistory = append(participant.ActivityHistory, activity)

	// Keep only recent history (last 50 activities)
	if len(participant.ActivityHistory) > 50 {
		participant.ActivityHistory = participant.ActivityHistory[len(participant.ActivityHistory)-50:]
	}

	// Check coordination rules
	c.evaluateRules()
}

// RecordInteraction records an interaction between participants
func (c *MultiParticipantCoordinator) RecordInteraction(from, to, interactionType string, data interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	interaction := ParticipantInteraction{
		From:      from,
		To:        to,
		Type:      interactionType,
		Timestamp: time.Now(),
		Data:      data,
	}

	c.interactions = append(c.interactions, interaction)

	// Check for bidirectional interaction
	for i := len(c.interactions) - 2; i >= 0 && i >= len(c.interactions)-10; i-- {
		prev := c.interactions[i]
		if prev.From == to && prev.To == from && prev.Type == interactionType &&
			time.Since(prev.Timestamp) < c.interactionWindow {
			interaction.Bidirectional = true
			prev.Bidirectional = true
			break
		}
	}

	// Trim old interactions
	cutoff := time.Now().Add(-c.interactionWindow)
	newInteractions := make([]ParticipantInteraction, 0)
	for _, inter := range c.interactions {
		if inter.Timestamp.After(cutoff) {
			newInteractions = append(newInteractions, inter)
		}
	}
	c.interactions = newInteractions

	// Emit event
	c.emitEvent("interaction_recorded", CoordinationEvent{
		Type:         "interaction_recorded",
		Timestamp:    time.Now(),
		Participants: []string{from, to},
		Data: map[string]interface{}{
			"type":          interactionType,
			"bidirectional": interaction.Bidirectional,
		},
	})
}

// CreateGroup creates a new participant group
func (c *MultiParticipantCoordinator) CreateGroup(id, name string, metadata map[string]interface{}) (*ParticipantGroup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.groups[id]; exists {
		return nil, fmt.Errorf("group %s already exists", id)
	}

	group := &ParticipantGroup{
		ID:           id,
		Name:         name,
		Participants: make(map[string]bool),
		Created:      time.Now(),
		Metadata:     metadata,
		Rules:        make([]GroupRule, 0),
	}

	c.groups[id] = group

	getLogger := logger.GetLogger()
	getLogger.Infow("created participant group",
		"id", id,
		"name", name)

	return group, nil
}

// AddParticipantToGroup adds a participant to a group
func (c *MultiParticipantCoordinator) AddParticipantToGroup(identity, groupID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	participant, exists := c.participants[identity]
	if !exists {
		return fmt.Errorf("participant %s not found", identity)
	}

	group, exists := c.groups[groupID]
	if !exists {
		return fmt.Errorf("group %s not found", groupID)
	}

	// Check group rules
	for _, rule := range group.Rules {
		if !rule.CanJoinGroup(participant, group) {
			return fmt.Errorf("participant %s cannot join group %s due to rules", identity, groupID)
		}
	}

	// Add to group
	group.Participants[identity] = true
	participant.Groups = append(participant.Groups, groupID)

	// Notify rules
	for _, rule := range group.Rules {
		rule.OnGroupChange(group, []string{identity}, nil)
	}

	// Emit event
	c.emitEvent("participant_joined_group", CoordinationEvent{
		Type:         "participant_joined_group",
		Timestamp:    time.Now(),
		Participants: []string{identity},
		Data: map[string]interface{}{
			"group_id":   groupID,
			"group_size": len(group.Participants),
		},
	})

	return nil
}

// RemoveParticipantFromGroup removes a participant from a group
func (c *MultiParticipantCoordinator) RemoveParticipantFromGroup(identity, groupID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	participant, exists := c.participants[identity]
	if !exists {
		return fmt.Errorf("participant %s not found", identity)
	}

	group, exists := c.groups[groupID]
	if !exists {
		return fmt.Errorf("group %s not found", groupID)
	}

	// Remove from group
	delete(group.Participants, identity)

	// Remove from participant's group list
	newGroups := make([]string, 0)
	for _, g := range participant.Groups {
		if g != groupID {
			newGroups = append(newGroups, g)
		}
	}
	participant.Groups = newGroups

	// Notify rules
	for _, rule := range group.Rules {
		rule.OnGroupChange(group, nil, []string{identity})
	}

	return nil
}

// GetActiveParticipants returns participants active within the threshold
func (c *MultiParticipantCoordinator) GetActiveParticipants() []*CoordinatedParticipant {
	c.mu.RLock()
	defer c.mu.RUnlock()

	active := make([]*CoordinatedParticipant, 0)
	cutoff := time.Now().Add(-c.activityThreshold)

	for _, participant := range c.participants {
		if participant.LastActivity.After(cutoff) {
			active = append(active, participant)
		}
	}

	return active
}

// GetParticipantGroups returns groups for a participant
func (c *MultiParticipantCoordinator) GetParticipantGroups(identity string) []*ParticipantGroup {
	c.mu.RLock()
	defer c.mu.RUnlock()

	participant, exists := c.participants[identity]
	if !exists {
		return nil
	}

	groups := make([]*ParticipantGroup, 0)
	for _, groupID := range participant.Groups {
		if group, exists := c.groups[groupID]; exists {
			groups = append(groups, group)
		}
	}

	return groups
}

// GetGroupMembers returns the members of a group
func (c *MultiParticipantCoordinator) GetGroupMembers(groupID string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	group, exists := c.groups[groupID]
	if !exists {
		return nil
	}

	members := make([]string, 0, len(group.Participants))
	for identity := range group.Participants {
		members = append(members, identity)
	}

	return members
}

// GetInteractionGraph returns interaction data between participants
func (c *MultiParticipantCoordinator) GetInteractionGraph() map[string]map[string]int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Build interaction graph
	graph := make(map[string]map[string]int)

	for _, interaction := range c.interactions {
		if _, exists := graph[interaction.From]; !exists {
			graph[interaction.From] = make(map[string]int)
		}
		graph[interaction.From][interaction.To]++

		if interaction.Bidirectional {
			if _, exists := graph[interaction.To]; !exists {
				graph[interaction.To] = make(map[string]int)
			}
			graph[interaction.To][interaction.From]++
		}
	}

	return graph
}

// GetParticipantInteractions returns interactions for a specific participant
func (c *MultiParticipantCoordinator) GetParticipantInteractions(identity string) []ParticipantInteraction {
	c.mu.RLock()
	defer c.mu.RUnlock()

	interactions := make([]ParticipantInteraction, 0)
	for _, interaction := range c.interactions {
		if interaction.From == identity || interaction.To == identity {
			interactions = append(interactions, interaction)
		}
	}

	return interactions
}

// GetActivityMetrics returns metrics about participant activities
func (c *MultiParticipantCoordinator) GetActivityMetrics() ActivityMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()

	metrics := ActivityMetrics{
		TotalActivities:    0,
		ActivitiesByType:   make(map[ActivityType]int64),
		ActiveParticipants: 0,
	}

	// Count activities
	for _, participant := range c.participants {
		metrics.TotalActivities += int64(len(participant.ActivityHistory))
		if participant.LastActivity.After(time.Now().Add(-c.activityThreshold)) {
			metrics.ActiveParticipants++
		}

		for _, activity := range participant.ActivityHistory {
			metrics.ActivitiesByType[activity.Type]++
		}
	}

	return metrics
}

// ActivityMetrics represents activity metrics
type ActivityMetrics struct {
	TotalActivities    int64
	ActivitiesByType   map[ActivityType]int64
	ActiveParticipants int
}

// AddCoordinationRule adds a coordination rule
func (c *MultiParticipantCoordinator) AddCoordinationRule(rule CoordinationRule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.coordinationRules = append(c.coordinationRules, rule)
}

// RegisterEventHandler registers an event handler
func (c *MultiParticipantCoordinator) RegisterEventHandler(eventType string, handler CoordinationEventHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.eventHandlers[eventType]; !exists {
		c.eventHandlers[eventType] = make([]CoordinationEventHandler, 0)
	}
	c.eventHandlers[eventType] = append(c.eventHandlers[eventType], handler)
}

// Stop stops the coordinator
func (c *MultiParticipantCoordinator) Stop() {
	// Clean up resources
	c.mu.Lock()
	defer c.mu.Unlock()

	c.participants = make(map[string]*CoordinatedParticipant)
	c.groups = make(map[string]*ParticipantGroup)
	c.interactions = nil
}

// Private methods

// checkAutoGrouping checks if a participant should be auto-grouped
func (c *MultiParticipantCoordinator) checkAutoGrouping(participant *CoordinatedParticipant) {
	// Example: Auto-group based on metadata
	if participant.Participant != nil && participant.Participant.Metadata() != "" {
		// Parse metadata and check for group tags
		// This is a simplified example
		getLogger := logger.GetLogger()
		getLogger.Debugw("checking auto-grouping for participant",
			"identity", participant.Identity,
			"metadata", participant.Participant.Metadata())
	}
}

// evaluateRules evaluates all coordination rules
func (c *MultiParticipantCoordinator) evaluateRules() {
	for _, rule := range c.coordinationRules {
		if applies, actions := rule.Evaluate(c.participants); applies {
			for _, action := range actions {
				c.executeAction(action)
			}
		}
	}
}

// executeAction executes a coordination action
func (c *MultiParticipantCoordinator) executeAction(action CoordinationAction) {
	getLogger := logger.GetLogger()
	getLogger.Debugw("executing coordination action",
		"type", action.Type,
		"target", action.Target,
		"parameters", action.Parameters)

	// Implementation would depend on action types
	switch action.Type {
	case "create_group":
		// Create a new group
	case "move_to_group":
		// Move participant to group
	case "notify":
		// Send notification
	}
}

// emitEvent emits a coordination event
func (c *MultiParticipantCoordinator) emitEvent(eventType string, event CoordinationEvent) {
	handlers, exists := c.eventHandlers[eventType]
	if !exists {
		return
	}

	// Call handlers in goroutines to avoid blocking
	for _, handler := range handlers {
		go handler(event)
	}
}

// Built-in Coordination Rules

// ProximityRule groups participants based on interaction frequency
type ProximityRule struct {
	name                 string
	interactionThreshold int
	timeWindow           time.Duration
}

// NewProximityRule creates a proximity-based grouping rule
func NewProximityRule(threshold int, window time.Duration) *ProximityRule {
	return &ProximityRule{
		name:                 "proximity_grouping",
		interactionThreshold: threshold,
		timeWindow:           window,
	}
}

func (r *ProximityRule) GetName() string {
	return r.name
}

func (r *ProximityRule) Evaluate(participants map[string]*CoordinatedParticipant) (bool, []CoordinationAction) {
	// Implementation would analyze interaction patterns
	// and suggest groupings based on proximity
	return false, nil
}

// ActivityBasedRule triggers actions based on activity patterns
type ActivityBasedRule struct {
	name              string
	activityThreshold int
	timeWindow        time.Duration
}

// NewActivityBasedRule creates an activity-based rule
func NewActivityBasedRule(threshold int, window time.Duration) *ActivityBasedRule {
	return &ActivityBasedRule{
		name:              "activity_based",
		activityThreshold: threshold,
		timeWindow:        window,
	}
}

func (r *ActivityBasedRule) GetName() string {
	return r.name
}

func (r *ActivityBasedRule) Evaluate(participants map[string]*CoordinatedParticipant) (bool, []CoordinationAction) {
	// Check for participants with high activity
	actions := make([]CoordinationAction, 0)
	cutoff := time.Now().Add(-r.timeWindow)

	for _, participant := range participants {
		activityCount := 0
		for _, activity := range participant.ActivityHistory {
			if activity.Timestamp.After(cutoff) {
				activityCount++
			}
		}

		if activityCount > r.activityThreshold {
			// Suggest action for highly active participant
			actions = append(actions, CoordinationAction{
				Type:   "notify",
				Target: participant.Identity,
				Parameters: map[string]interface{}{
					"reason": "high_activity",
					"count":  activityCount,
				},
			})
		}
	}

	return len(actions) > 0, actions
}

// SizeBasedGroupRule limits group sizes
type SizeBasedGroupRule struct {
	maxSize int
}

// NewSizeBasedGroupRule creates a size-based group rule
func NewSizeBasedGroupRule(maxSize int) *SizeBasedGroupRule {
	return &SizeBasedGroupRule{maxSize: maxSize}
}

func (r *SizeBasedGroupRule) CanJoinGroup(participant *CoordinatedParticipant, group *ParticipantGroup) bool {
	return len(group.Participants) < r.maxSize
}

func (r *SizeBasedGroupRule) OnGroupChange(group *ParticipantGroup, added, removed []string) {
	// Log group changes
	if len(added) > 0 || len(removed) > 0 {
		getLogger := logger.GetLogger()
		getLogger.Debugw("group membership changed",
			"group", group.ID,
			"size", len(group.Participants),
			"added", added,
			"removed", removed)
	}
}
