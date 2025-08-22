package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

// ParticipantPermissionManager manages permissions for participants
type ParticipantPermissionManager struct {
	mu          sync.RWMutex
	permissions map[string]*ParticipantPermissions
	policies    []PermissionPolicy
	defaults    *livekit.ParticipantPermission
	agentCaps   *AgentCapabilities
}

// ParticipantPermissions tracks permissions for a participant
type ParticipantPermissions struct {
	Identity           string
	Current            *livekit.ParticipantPermission
	Requested          *livekit.ParticipantPermission
	LastUpdated        time.Time
	LastRequested      time.Time
	ChangeHistory      []PermissionChange
	CustomRestrictions map[string]bool
}

// PermissionChange represents a permission change event
type PermissionChange struct {
	Timestamp time.Time
	From      *livekit.ParticipantPermission
	To        *livekit.ParticipantPermission
	Reason    string
	Approved  bool
}

// PermissionPolicy defines a policy for permission management
type PermissionPolicy interface {
	// EvaluatePermissionRequest evaluates if a permission change should be allowed
	EvaluatePermissionRequest(identity string, current, requested *livekit.ParticipantPermission) (bool, string)
	
	// GetDefaultPermissions returns default permissions for a participant
	GetDefaultPermissions(identity string) *livekit.ParticipantPermission
}

// AgentCapabilities defines what the agent is allowed to do
type AgentCapabilities struct {
	CanManagePermissions   bool
	CanSendData           bool
	CanSubscribeToTracks  bool
	CanPublishTracks      bool
	CanKickParticipants   bool
	CanMuteParticipants   bool
	MaxDataMessageSize    int
	AllowedDataRecipients []string // Empty means all
}

// NewParticipantPermissionManager creates a new permission manager
func NewParticipantPermissionManager() *ParticipantPermissionManager {
	return &ParticipantPermissionManager{
		permissions: make(map[string]*ParticipantPermissions),
		policies:    make([]PermissionPolicy, 0),
		defaults: &livekit.ParticipantPermission{
			CanSubscribe:   true,
			CanPublish:     false,
			CanPublishData: true,
		},
		agentCaps: &AgentCapabilities{
			CanManagePermissions: true,
			CanSendData:         true,
			CanSubscribeToTracks: true,
			CanPublishTracks:    true,
			MaxDataMessageSize:  15 * 1024, // 15KB default
		},
	}
}

// UpdateParticipantPermissions updates permissions for a participant
func (m *ParticipantPermissionManager) UpdateParticipantPermissions(identity string, perms *livekit.ParticipantPermission) {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, exists := m.permissions[identity]
	if !exists {
		existing = &ParticipantPermissions{
			Identity:           identity,
			ChangeHistory:      make([]PermissionChange, 0),
			CustomRestrictions: make(map[string]bool),
		}
		m.permissions[identity] = existing
	}

	// Record change
	if existing.Current != nil {
		change := PermissionChange{
			Timestamp: time.Now(),
			From:      existing.Current,
			To:        perms,
			Reason:    "permission_update",
			Approved:  true,
		}
		existing.ChangeHistory = append(existing.ChangeHistory, change)
		
		// Keep only last 10 changes
		if len(existing.ChangeHistory) > 10 {
			existing.ChangeHistory = existing.ChangeHistory[len(existing.ChangeHistory)-10:]
		}
	}

	existing.Current = perms
	existing.LastUpdated = time.Now()
}

// RemoveParticipant removes a participant from tracking
func (m *ParticipantPermissionManager) RemoveParticipant(identity string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.permissions, identity)
}

// CanSendDataTo checks if the agent can send data to a participant
func (m *ParticipantPermissionManager) CanSendDataTo(identity string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check agent capabilities
	if !m.agentCaps.CanSendData {
		return false
	}

	// Check allowed recipients
	if len(m.agentCaps.AllowedDataRecipients) > 0 {
		allowed := false
		for _, recipient := range m.agentCaps.AllowedDataRecipients {
			if recipient == identity {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}

	// Check participant permissions
	if perms, exists := m.permissions[identity]; exists {
		if perms.Current != nil && !perms.Current.CanPublishData {
			return false
		}
		// Check custom restrictions
		if restricted, exists := perms.CustomRestrictions["no_data_receive"]; exists && restricted {
			return false
		}
	}

	return true
}

// CanManagePermissions checks if the agent can manage permissions
func (m *ParticipantPermissionManager) CanManagePermissions() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agentCaps.CanManagePermissions
}

// ValidatePermissions validates a permission set
func (m *ParticipantPermissionManager) ValidatePermissions(perms *livekit.ParticipantPermission) error {
	if perms == nil {
		return fmt.Errorf("permissions cannot be nil")
	}

	// Validate source types if publishing is allowed
	if perms.CanPublish && perms.CanPublishSources != nil {
		validSources := map[livekit.TrackSource]bool{
			livekit.TrackSource_CAMERA:             true,
			livekit.TrackSource_MICROPHONE:         true,
			livekit.TrackSource_SCREEN_SHARE:       true,
			livekit.TrackSource_SCREEN_SHARE_AUDIO: true,
		}
		
		for _, source := range perms.CanPublishSources {
			if !validSources[source] {
				return fmt.Errorf("invalid track source: %v", source)
			}
		}
	}

	return nil
}

// RequestPermissionChange processes a permission change request
func (m *ParticipantPermissionManager) RequestPermissionChange(identity string, requested *livekit.ParticipantPermission) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.agentCaps.CanManagePermissions {
		return false, fmt.Errorf("agent does not have permission management capability")
	}

	// Get current permissions
	participant := m.permissions[identity]
	if participant == nil {
		participant = &ParticipantPermissions{
			Identity:           identity,
			Current:            m.defaults,
			ChangeHistory:      make([]PermissionChange, 0),
			CustomRestrictions: make(map[string]bool),
		}
		m.permissions[identity] = participant
	}

	participant.Requested = requested
	participant.LastRequested = time.Now()

	// Evaluate against policies
	approved := true
	var reason string
	
	for _, policy := range m.policies {
		policyApproved, policyReason := policy.EvaluatePermissionRequest(identity, participant.Current, requested)
		if !policyApproved {
			approved = false
			reason = policyReason
			break
		}
	}

	// Record the request
	change := PermissionChange{
		Timestamp: time.Now(),
		From:      participant.Current,
		To:        requested,
		Reason:    reason,
		Approved:  approved,
	}
	participant.ChangeHistory = append(participant.ChangeHistory, change)

	if approved {
		logger := logger.GetLogger()
		logger.Infow("permission change approved",
			"identity", identity,
			"changes", describePermissionChanges(participant.Current, requested))
	}

	return approved, nil
}

// AddPolicy adds a permission policy
func (m *ParticipantPermissionManager) AddPolicy(policy PermissionPolicy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies = append(m.policies, policy)
}

// SetDefaultPermissions sets default permissions for new participants
func (m *ParticipantPermissionManager) SetDefaultPermissions(perms *livekit.ParticipantPermission) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaults = perms
}

// SetAgentCapabilities sets what the agent is allowed to do
func (m *ParticipantPermissionManager) SetAgentCapabilities(caps *AgentCapabilities) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agentCaps = caps
}

// GetParticipantPermissions returns current permissions for a participant
func (m *ParticipantPermissionManager) GetParticipantPermissions(identity string) *livekit.ParticipantPermission {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if participant, exists := m.permissions[identity]; exists && participant.Current != nil {
		return participant.Current
	}
	return m.defaults
}

// SetCustomRestriction sets a custom restriction for a participant
func (m *ParticipantPermissionManager) SetCustomRestriction(identity, restriction string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	participant := m.permissions[identity]
	if participant == nil {
		participant = &ParticipantPermissions{
			Identity:           identity,
			CustomRestrictions: make(map[string]bool),
		}
		m.permissions[identity] = participant
	}

	participant.CustomRestrictions[restriction] = enabled
}

// GetPermissionHistory returns permission change history for a participant
func (m *ParticipantPermissionManager) GetPermissionHistory(identity string) []PermissionChange {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if participant, exists := m.permissions[identity]; exists {
		// Return a copy
		history := make([]PermissionChange, len(participant.ChangeHistory))
		copy(history, participant.ChangeHistory)
		return history
	}
	return nil
}

// Built-in Permission Policies

// RoleBasedPolicy implements role-based permission management
type RoleBasedPolicy struct {
	rolePermissions map[string]*livekit.ParticipantPermission
}

// NewRoleBasedPolicy creates a new role-based policy
func NewRoleBasedPolicy() *RoleBasedPolicy {
	return &RoleBasedPolicy{
		rolePermissions: map[string]*livekit.ParticipantPermission{
			"viewer": {
				CanSubscribe:   true,
				CanPublish:     false,
				CanPublishData: false,
			},
			"participant": {
				CanSubscribe:   true,
				CanPublish:     true,
				CanPublishData: true,
			},
			"moderator": {
				CanSubscribe:      true,
				CanPublish:        true,
				CanPublishData:    true,
				CanUpdateMetadata: true,
			},
		},
	}
}

func (p *RoleBasedPolicy) EvaluatePermissionRequest(identity string, current, requested *livekit.ParticipantPermission) (bool, string) {
	// For demo, approve all requests
	// In production, this would check participant roles
	return true, "role_based_approval"
}

func (p *RoleBasedPolicy) GetDefaultPermissions(identity string) *livekit.ParticipantPermission {
	// Default to viewer permissions
	return p.rolePermissions["viewer"]
}

// TimeBasedPolicy restricts permissions based on time
type TimeBasedPolicy struct {
	allowedHours [2]int // Start and end hour (24h format)
}

// NewTimeBasedPolicy creates a time-based policy
func NewTimeBasedPolicy(startHour, endHour int) *TimeBasedPolicy {
	return &TimeBasedPolicy{
		allowedHours: [2]int{startHour, endHour},
	}
}

func (p *TimeBasedPolicy) EvaluatePermissionRequest(identity string, current, requested *livekit.ParticipantPermission) (bool, string) {
	currentHour := time.Now().Hour()
	if currentHour < p.allowedHours[0] || currentHour >= p.allowedHours[1] {
		// Only allow subscribe permissions outside allowed hours
		if requested.CanPublish || requested.CanPublishData {
			return false, fmt.Sprintf("publishing not allowed outside hours %d-%d", p.allowedHours[0], p.allowedHours[1])
		}
	}
	return true, "time_based_approval"
}

func (p *TimeBasedPolicy) GetDefaultPermissions(identity string) *livekit.ParticipantPermission {
	return &livekit.ParticipantPermission{
		CanSubscribe: true,
		CanPublish:   false,
	}
}

// describePermissionChanges describes what changed between two permission sets
func describePermissionChanges(from, to *livekit.ParticipantPermission) string {
	changes := []string{}
	
	if from.CanSubscribe != to.CanSubscribe {
		changes = append(changes, fmt.Sprintf("subscribe: %v->%v", from.CanSubscribe, to.CanSubscribe))
	}
	if from.CanPublish != to.CanPublish {
		changes = append(changes, fmt.Sprintf("publish: %v->%v", from.CanPublish, to.CanPublish))
	}
	if from.CanPublishData != to.CanPublishData {
		changes = append(changes, fmt.Sprintf("publish_data: %v->%v", from.CanPublishData, to.CanPublishData))
	}
	if from.Hidden != to.Hidden {
		changes = append(changes, fmt.Sprintf("hidden: %v->%v", from.Hidden, to.Hidden))
	}
	
	if len(changes) == 0 {
		return "no changes"
	}
	return fmt.Sprintf("%v", changes)
}