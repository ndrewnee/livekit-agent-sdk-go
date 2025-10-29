package agent

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestParticipantPermissionManager_Flow(t *testing.T) {
	m := NewParticipantPermissionManager()

	// ValidatePermissions with invalid source when publishing
	perms := &livekit.ParticipantPermission{
		CanSubscribe:      true,
		CanPublish:        true,
		CanPublishData:    true,
		CanPublishSources: []livekit.TrackSource{livekit.TrackSource(999)},
	}
	err := m.ValidatePermissions(perms)
	assert.Error(t, err)

	// UpdateParticipantPermissions and fetch (twice to hit change history)
	good := &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true}
	m.UpdateParticipantPermissions("u1", good)
	got := m.GetParticipantPermissions("u1")
	assert.Equal(t, good, got)
	// Second update to exercise history append logic
	good2 := &livekit.ParticipantPermission{CanSubscribe: false, CanPublish: true, CanPublishData: false}
	m.UpdateParticipantPermissions("u1", good2)

	// CanSendDataTo honoring allowed recipients and custom restriction
	m.SetAgentCapabilities(&AgentCapabilities{CanSendData: true, CanManagePermissions: true, AllowedDataRecipients: []string{"u2"}})
	assert.False(t, m.CanSendDataTo("u1")) // not in allowed list
	assert.True(t, m.CanSendDataTo("u2"))  // allowed
	m.SetCustomRestriction("u1", "no_data_receive", true)
	assert.False(t, m.CanSendDataTo("u1"))

	// RequestPermissionChange with policies
	m.AddPolicy(NewRoleBasedPolicy())
	m.AddPolicy(NewTimeBasedPolicy(time.Now().Hour()+1, time.Now().Hour()+2)) // likely disallow now
	req := &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: true, CanPublishData: true}
	approved, err := m.RequestPermissionChange("u1", req)
	// Should not error with CanManagePermissions=true
	assert.NoError(t, err)
	_ = approved // may be true/false depending on time-based policy

	hist := m.GetPermissionHistory("u1")
	assert.GreaterOrEqual(t, len(hist), 1)

	// Role/time default permissions helpers
	rb := NewRoleBasedPolicy()
	assert.NotNil(t, rb.GetDefaultPermissions("any"))
	tb := NewTimeBasedPolicy(0, 23)
	assert.NotNil(t, tb.GetDefaultPermissions("any"))

	// Remove participant
	m.RemoveParticipant("u1")
	assert.NotNil(t, m.GetParticipantPermissions("missing")) // returns defaults
}
