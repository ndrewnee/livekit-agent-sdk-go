package agent

import (
	"context"
	"go.uber.org/zap"
	"testing"
	"time"
)

func TestResourceMonitor_StartStop(t *testing.T) {
	m := NewResourceMonitor(zap.NewNop(), ResourceMonitorOptions{CheckInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go m.Start(ctx)
	time.Sleep(12 * time.Millisecond)
	cancel()
	m.Stop()
}
