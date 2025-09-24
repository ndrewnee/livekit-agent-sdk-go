package agent

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestCoordinator_RemoveParticipantFromGroup(t *testing.T) {
	c := NewMultiParticipantCoordinator()
	c.RegisterParticipant("p1", nil)
	_, err := c.CreateGroup("g1", "grp", nil)
	assert.NoError(t, err)
	assert.NoError(t, c.AddParticipantToGroup("p1", "g1"))
	assert.NoError(t, c.RemoveParticipantFromGroup("p1", "g1"))
}
