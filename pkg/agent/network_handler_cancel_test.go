package agent

import (
	"context"
	"testing"
)

func TestNetworkHandler_ResolveDNSWithRetry_Cancel(t *testing.T) {
	nh := NewNetworkHandler()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := nh.ResolveDNSWithRetry(ctx, "localhost"); err == nil {
		t.Fatalf("expected context error")
	}
}
