package agent

import (
	"go.uber.org/zap"
	"testing"
)

func TestResourceMonitor_CheckResources_OOM(t *testing.T) {
	m := NewResourceMonitor(zap.NewNop(), ResourceMonitorOptions{})
	// Force a very low memory limit to trigger OOM path
	m.memoryLimit = 1 // bytes
	m.checkResources()
}
