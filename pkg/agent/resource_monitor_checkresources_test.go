package agent

import (
	"go.uber.org/zap"
	"testing"
)

func TestResourceMonitor_CheckResources_Direct(t *testing.T) {
	m := NewResourceMonitor(zap.NewNop(), ResourceMonitorOptions{})
	m.checkResources()
}
