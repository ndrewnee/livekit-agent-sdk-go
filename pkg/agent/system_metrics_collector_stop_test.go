package agent

import "testing"

func TestSystemMetricsCollector_Stop(t *testing.T) {
	c := NewSystemMetricsCollector()
	// Call Stop shortly after start to cover Stop path
	c.Stop()
}
