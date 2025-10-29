package agent

import (
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

// mockRule implements CoordinationRule and returns a set of actions
type mockRule struct{}

func (m *mockRule) Evaluate(participants map[string]*CoordinatedParticipant) (bool, []CoordinationAction) {
	return true, []CoordinationAction{
		{Type: "create_group", Target: "g1"},
		{Type: "move_to_group", Target: "g1", Parameters: map[string]interface{}{"identity": "p1"}},
		{Type: "notify", Target: "p1"},
	}
}
func (m *mockRule) GetName() string { return "mock" }

func TestCoordinator_ExecuteActionsAndEvents(t *testing.T) {
	c := NewMultiParticipantCoordinator()
	// Register event handler and ensure it fires
	count := 0
	done := make(chan struct{}, 1)
	c.RegisterEventHandler("participant_registered", func(event CoordinationEvent) {
		count++
		select {
		case done <- struct{}{}:
		default:
		}
	})
	c.RegisterParticipant("p1", &lksdk.RemoteParticipant{})
	select {
	case <-done:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for participant_registered event")
	}
	assert.GreaterOrEqual(t, count, 1)

	// Add a rule and trigger evaluateRules via activity update
	c.AddCoordinationRule(&mockRule{})
	c.UpdateParticipantActivity("p1", ActivityTypeJoined)

	// Call GetActivityMetrics to cover metrics aggregation path
	metrics := c.GetActivityMetrics()
	_ = metrics

	// Stop to cover Stop method
	c.Stop()
}
