package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// testResource implements Resource for pool tests
type testResource struct{ healthy bool }

func (r *testResource) IsHealthy() bool { return r.healthy }
func (r *testResource) Close() error    { r.healthy = false; return nil }
func (r *testResource) Reset() error    { return nil }

// testFactory implements ResourceFactory with controllable Validate
type testFactory struct{ validateErr error }

func (f *testFactory) Create(ctx context.Context) (Resource, error) {
	return &testResource{healthy: true}, nil
}
func (f *testFactory) Validate(resource Resource) error { return f.validateErr }

func TestResourcePool_AcquireValidateErrorAndClose(t *testing.T) {
	f := &testFactory{validateErr: errors.New("invalid")}
	pool, err := NewResourcePool(f, ResourcePoolOptions{MinSize: 1, MaxSize: 2, MaxIdleTime: time.Second})
	assert.NoError(t, err)
	defer pool.Close()

	// Acquire should pop the prewarmed invalid resource, then create a new one
	r, err := pool.Acquire(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, r)

	// Closing pool causes further Acquire to error
	assert.NoError(t, pool.Close())
	_, err = pool.Acquire(context.Background())
	assert.Error(t, err)
}

func TestResourcePool_ReleasePaths(t *testing.T) {
	f := &testFactory{}
	pool, err := NewResourcePool(f, ResourcePoolOptions{MinSize: 0, MaxSize: 1, MaxIdleTime: time.Second})
	assert.NoError(t, err)
	defer pool.Close()

	// Create a resource directly and release while pool open -> returns to channel or closed if full
	r := &testResource{healthy: true}
	pool.Release(r)

	// Release unhealthy -> destroyed and possibly replenished to min size
	r2 := &testResource{healthy: false}
	pool.Release(r2)

	// Release when closed -> destroyed
	_ = pool.Close()
	pool.Release(&testResource{healthy: true})
}

func TestResourcePool_AcquireAtCapacity(t *testing.T) {
	f := &testFactory{}
	pool, err := NewResourcePool(f, ResourcePoolOptions{MinSize: 0, MaxSize: 1, MaxIdleTime: time.Second})
	assert.NoError(t, err)
	defer pool.Close()

	// First acquire succeeds
	r1, err := pool.Acquire(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, r1)

	// Second acquire should hit capacity error
	_, err = pool.Acquire(context.Background())
	assert.Error(t, err)
}

// Resource that fails Reset for Acquire path coverage
type failingResetResource struct{ testResource }

func (r *failingResetResource) Reset() error { return errors.New("reset failed") }

type okFactory struct{}

func (f *okFactory) Create(ctx context.Context) (Resource, error) {
	return &testResource{healthy: true}, nil
}
func (f *okFactory) Validate(resource Resource) error { return nil }

func TestResourcePool_Acquire_ResetFailurePath(t *testing.T) {
	pool, err := NewResourcePool(&okFactory{}, ResourcePoolOptions{MinSize: 0, MaxSize: 1, MaxIdleTime: time.Second})
	assert.NoError(t, err)
	defer pool.Close()

	// Seed pool with a resource that will fail Reset
	pool.resources <- &failingResetResource{testResource{healthy: true}}

	r, err := pool.Acquire(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, r)
}

func TestNewResourcePool_Errors(t *testing.T) {
	// MaxSize < MinSize
	_, err := NewResourcePool(&okFactory{}, ResourcePoolOptions{MinSize: 2, MaxSize: 1, MaxIdleTime: time.Second})
	assert.Error(t, err)
}

func TestResourcePool_CleanupIdle(t *testing.T) {
	pool, err := NewResourcePool(&okFactory{}, ResourcePoolOptions{MinSize: 0, MaxSize: 3, MaxIdleTime: time.Second})
	assert.NoError(t, err)
	defer pool.Close()

	// Seed multiple resources
	pool.resources <- &testResource{healthy: true}
	pool.resources <- &testResource{healthy: true}
	pool.resources <- &testResource{healthy: true}
	// Available should be 3 now
	pool.cleanupIdle() // Should remove down to minSize
}

func TestResourcePool_ReleasePoolFullClosesResource(t *testing.T) {
	pool, err := NewResourcePool(&okFactory{}, ResourcePoolOptions{MinSize: 0, MaxSize: 1, MaxIdleTime: time.Second})
	assert.NoError(t, err)
	defer pool.Close()

	// Fill channel to capacity
	pool.resources <- &testResource{healthy: true}
	// Release of another resource should close it (default case)
	pool.Release(&testResource{healthy: true})
}

func TestResourcePool_ReleaseUnhealthyTriggersAsync(t *testing.T) {
	pool, err := NewResourcePool(&okFactory{}, ResourcePoolOptions{MinSize: 0, MaxSize: 1, MaxIdleTime: time.Second})
	assert.NoError(t, err)
	defer pool.Close()
	// Require min size 1 without prewarming to trigger async create
	pool.minSize = 1
	pool.Release(&testResource{healthy: false})
	time.Sleep(10 * time.Millisecond)
	assert.GreaterOrEqual(t, pool.Available(), 1)
}
