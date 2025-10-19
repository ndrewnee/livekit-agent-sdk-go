package agent

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestCoordinator_UnregisterCleansGroups(t *testing.T) {
	c := NewMultiParticipantCoordinator()
	// Register participant and create group
	c.RegisterParticipant("p1", nil)
	g, err := c.CreateGroup("g1", "group1", nil)
	assert.NoError(t, err)
	assert.NotNil(t, g)
	// Add to group then unregister
	assert.NoError(t, c.AddParticipantToGroup("p1", "g1"))
	c.UnregisterParticipant("p1")
	// Ensure group updated or removed if empty
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Either group deleted or does not contain p1
	if grp, exists := c.groups["g1"]; exists {
		_, still := grp.Participants["p1"]
		assert.False(t, still)
	}
}

func TestCoordinator_GetInteractionGraph(t *testing.T) {
	c := NewMultiParticipantCoordinator()
	c.RecordInteraction("a", "b", "msg", nil)
	c.RecordInteraction("a", "b", "msg", nil)
	// Simulate bidirectional by recording reverse quickly
	c.RecordInteraction("b", "a", "msg", nil)
	g := c.GetInteractionGraph()
	if g["a"]["b"] == 0 {
		t.Fatalf("expected interaction count from a->b")
	}
}
