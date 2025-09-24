package agent

import (
	"context"
	"testing"
	"time"
)

func TestNetworkHandler_ResolveDNSWithRetry_Success(t *testing.T) {
	nh := NewNetworkHandler()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	addrs, err := nh.ResolveDNSWithRetry(ctx, "localhost")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(addrs) == 0 {
		t.Fatalf("expected some addresses")
	}
}
