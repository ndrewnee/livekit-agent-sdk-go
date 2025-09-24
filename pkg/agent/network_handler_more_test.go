package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNetworkHandler_ResolveDNSWithRetry_AndMonitor(t *testing.T) {
	nh := NewNetworkHandler()
	// Make retries quick
	nh.maxDNSRetries = 1

	// Invalid host should error quickly (not found)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := nh.ResolveDNSWithRetry(ctx, "invalid.invalid")
	assert.Error(t, err)

	// NetworkMonitor callback on partition
	nh.SetNetworkPartition(true)
	m := NewNetworkMonitor(nh, 10*time.Millisecond)
	fired := 0
	m.Start(func() { fired++ })
	time.Sleep(30 * time.Millisecond)
	m.Stop()
	assert.GreaterOrEqual(t, fired, 1)
}

func TestIsNetworkError_Heuristics(t *testing.T) {
	assert.True(t, isNetworkError(errors.New("connection refused")))
	assert.False(t, isNetworkError(nil))
}
