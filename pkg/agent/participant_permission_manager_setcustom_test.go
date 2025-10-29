package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Covers SetCustomRestriction path where participant record does not yet exist
func TestParticipantPermissionManager_SetCustomRestriction_NewParticipant(t *testing.T) {
	m := NewParticipantPermissionManager()

	// No prior state for this identity
	m.SetCustomRestriction("fresh-user", "no_data_receive", true)

	// Verify it was recorded
	hist := m.GetPermissionHistory("fresh-user")
	// No history changes should be recorded by SetCustomRestriction itself
	assert.Empty(t, hist)

	// But CanSendDataTo should respect the custom restriction when capabilities allow
	m.SetAgentCapabilities(&AgentCapabilities{CanSendData: true, CanManagePermissions: true})
	assert.False(t, m.CanSendDataTo("fresh-user"))
}
