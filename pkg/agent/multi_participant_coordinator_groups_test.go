package agent

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestCoordinator_UnregisterKeepsNonEmptyGroup(t *testing.T) {
	c := NewMultiParticipantCoordinator()
	c.RegisterParticipant("p1", nil)
	c.RegisterParticipant("p2", nil)
	_, err := c.CreateGroup("g1", "group1", nil)
	assert.NoError(t, err)
	assert.NoError(t, c.AddParticipantToGroup("p1", "g1"))
	assert.NoError(t, c.AddParticipantToGroup("p2", "g1"))

	// Unregister only p1; group should remain and contain p2
	c.UnregisterParticipant("p1")

	groups := c.GetParticipantGroups("p2")
	assert.Equal(t, 1, len(groups))
	members := c.GetGroupMembers("g1")
	assert.Contains(t, members, "p2")

	// Unregister non-existing participant (no panic)
	c.UnregisterParticipant("none")
}
