package agent

import "testing"

func TestResourcePool_ReleaseNil(t *testing.T) {
	// Ensure no panic on nil release
	pool, err := NewResourcePool(&okFactory{}, ResourcePoolOptions{MinSize: 0, MaxSize: 1})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	pool.Release(nil)
}
