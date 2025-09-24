package agent

import (
	"testing"
	"time"
)

func TestResourcePool_CreateAsync(t *testing.T) {
	pool, err := NewResourcePool(&okFactory{}, ResourcePoolOptions{MinSize: 0, MaxSize: 2, MaxIdleTime: time.Second})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	pool.createAsync()
	time.Sleep(10 * time.Millisecond)
	if pool.Available() == 0 {
		t.Fatalf("expected resource available after async create")
	}
}
