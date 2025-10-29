package agent

import (
	"testing"
	"time"
)

func TestNetworkMonitor_DefaultInterval(t *testing.T) {
	nh := NewNetworkHandler()
	m := NewNetworkMonitor(nh, 0)
	fired := 0
	m.Start(func() { fired++ })
	time.Sleep(12 * time.Millisecond)
	m.Stop()
}
