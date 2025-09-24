package agent

import (
	"go.uber.org/zap"
	"testing"
)

func TestResourceMonitor_GetMetricsAndStatus(t *testing.T) {
	m := NewResourceMonitor(zap.NewNop(), ResourceMonitorOptions{})
	_ = m.GetMetrics()
	_ = m.GetResourceStatus()
}
