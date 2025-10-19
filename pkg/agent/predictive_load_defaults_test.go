package agent

import "testing"

func TestPredictiveLoadCalculator_DefaultWindowSize(t *testing.T) {
	_ = NewPredictiveLoadCalculator(&DefaultLoadCalculator{}, 0)
}
