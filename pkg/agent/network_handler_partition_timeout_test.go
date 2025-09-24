package agent

import (
	"testing"
	"time"
)

func TestNetworkHandler_DetectNetworkPartition_Inactivity(t *testing.T) {
	nh := NewNetworkHandler()
	nh.mu.Lock()
	nh.lastNetworkActivity = time.Now().Add(-time.Hour)
	nh.mu.Unlock()
	if !nh.DetectNetworkPartition() {
		t.Fatalf("expected partition due to inactivity")
	}
}
