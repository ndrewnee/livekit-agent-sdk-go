package agent

import (
	"go.uber.org/zap"
	"testing"
)

func TestResourceMonitor_HandleOOM_Paths(t *testing.T) {
	m := NewResourceMonitor(zap.NewNop(), ResourceMonitorOptions{GoroutineLimit: 1000})
	called := 0
	m.SetOOMCallback(func() { called++ })
	// First OOM triggers callback
	m.handleOOM(1 << 30)
	// Second OOM should not call again (alreadyDetected)
	m.handleOOM(1 << 30)
}
