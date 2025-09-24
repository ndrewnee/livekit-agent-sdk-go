package agent

import "go.uber.org/zap"
import "testing"

func TestRaceProtector_CompleteTermination_Unknown(t *testing.T) {
	rp := NewRaceProtector(zap.NewNop())
	// Should not panic when completing untracked termination
	rp.CompleteTermination("unknown", nil)
}
