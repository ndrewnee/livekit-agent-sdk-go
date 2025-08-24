package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestResourcePool tests basic pool operations
func TestResourcePool(t *testing.T) {
	factory := &WorkerResourceFactory{}
	pool, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize:     2,
		MaxSize:     5,
		MaxIdleTime: 1 * time.Minute,
	})
	assert.NoError(t, err)
	defer func(pool *ResourcePool) {
		err := pool.Close()
		if err != nil {
			t.Errorf("Failed to close pool: %v", err)
		}
	}(pool)

	// Check initial state
	assert.Equal(t, 2, pool.Size())
	assert.Equal(t, 2, pool.Available())
	assert.Equal(t, 0, pool.InUse())

	// Acquire a resource
	ctx := context.Background()
	resource, err := pool.Acquire(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, resource)
	assert.Equal(t, 2, pool.Size())
	assert.Equal(t, 1, pool.Available())
	assert.Equal(t, 1, pool.InUse())

	// Release the resource
	pool.Release(resource)
	assert.Equal(t, 2, pool.Size())
	assert.Equal(t, 2, pool.Available())
	assert.Equal(t, 0, pool.InUse())
}

// TestResourcePoolMaxSize tests pool size limits
func TestResourcePoolMaxSize(t *testing.T) {
	factory := &WorkerResourceFactory{}
	pool, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize:     1,
		MaxSize:     3,
		MaxIdleTime: 1 * time.Minute,
	})
	assert.NoError(t, err)
	defer func(pool *ResourcePool) {
		err := pool.Close()
		if err != nil {
			t.Errorf("Failed to close pool: %v", err)
		}
	}(pool)

	ctx := context.Background()
	resources := make([]Resource, 0)

	// Acquire up to max size
	for i := 0; i < 3; i++ {
		resource, err := pool.Acquire(ctx)
		assert.NoError(t, err)
		resources = append(resources, resource)
	}

	assert.Equal(t, 3, pool.Size())
	assert.Equal(t, 0, pool.Available())
	assert.Equal(t, 3, pool.InUse())

	// Try to acquire one more (should fail)
	_, err = pool.Acquire(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maximum capacity")

	// Release one and try again
	pool.Release(resources[0])
	resource, err := pool.Acquire(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, resource)
}

// TestResourcePoolValidation tests resource validation
func TestResourcePoolValidation(t *testing.T) {
	factory := &WorkerResourceFactory{}
	pool, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize:     1,
		MaxSize:     5,
		MaxIdleTime: 1 * time.Minute,
	})
	assert.NoError(t, err)
	defer func(pool *ResourcePool) {
		err := pool.Close()
		if err != nil {
			t.Errorf("Failed to close pool: %v", err)
		}
	}(pool)

	ctx := context.Background()

	// Acquire and modify resource to make it unhealthy
	resource, err := pool.Acquire(ctx)
	assert.NoError(t, err)

	workerRes := resource.(*WorkerResource)
	workerRes.mu.Lock()
	workerRes.healthy = false
	workerRes.mu.Unlock()

	// Release unhealthy resource
	initialDestroyed := pool.Stats()["destroyed"]
	pool.Release(resource)

	// Should have been destroyed
	assert.Equal(t, initialDestroyed+1, pool.Stats()["destroyed"])

	// Pool should maintain minimum size
	time.Sleep(100 * time.Millisecond) // Allow async creation
	assert.GreaterOrEqual(t, pool.Size(), 1)
}

// TestResourcePoolConcurrency tests concurrent pool operations
func TestResourcePoolConcurrency(t *testing.T) {
	factory := &WorkerResourceFactory{}
	pool, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize:     2,
		MaxSize:     10,
		MaxIdleTime: 1 * time.Minute,
	})
	assert.NoError(t, err)
	defer func(pool *ResourcePool) {
		err := pool.Close()
		if err != nil {
			t.Errorf("Failed to close pool: %v", err)
		}
	}(pool)

	ctx := context.Background()
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	// Concurrent acquire and release
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Add some jitter to avoid all goroutines hitting at once
			time.Sleep(time.Duration(id) * time.Millisecond)

			resource, err := pool.Acquire(ctx)
			if err != nil {
				// It's OK if we hit capacity limits in concurrent test
				if err.Error() != "pool at maximum capacity (10)" {
					errors <- err
				}
				return
			}

			// Simulate work
			time.Sleep(10 * time.Millisecond)

			pool.Release(resource)
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		assert.NoError(t, err)
	}

	// Pool should still be functional
	assert.GreaterOrEqual(t, pool.Size(), 2)
	assert.LessOrEqual(t, pool.Size(), 10)
}

// TestResourcePoolIdleCleanup tests idle resource cleanup
func TestResourcePoolIdleCleanup(t *testing.T) {
	factory := &WorkerResourceFactory{}
	pool, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize:     2,
		MaxSize:     5,
		MaxIdleTime: 200 * time.Millisecond, // Very short for testing
	})
	assert.NoError(t, err)
	defer func(pool *ResourcePool) {
		err := pool.Close()
		if err != nil {
			t.Errorf("Failed to close pool: %v", err)
		}
	}(pool)

	ctx := context.Background()

	// Acquire and release extra resources
	resources := make([]Resource, 3)
	for i := 0; i < 3; i++ {
		resources[i], _ = pool.Acquire(ctx)
	}
	// Pool may or may not have created all 5 resources
	assert.GreaterOrEqual(t, pool.Size(), 3)

	// Release all
	for _, res := range resources {
		pool.Release(res)
	}

	// Wait for cleanup
	time.Sleep(300 * time.Millisecond)

	// Should be back to minimum size
	assert.Equal(t, 2, pool.Size())
	assert.Equal(t, 2, pool.Available())
}

// TestWorkerResource tests the worker resource implementation
func TestWorkerResource(t *testing.T) {
	resource := &WorkerResource{
		ID:        "test-1",
		CreatedAt: time.Now(),
		healthy:   true,
	}

	// Test healthy state
	assert.True(t, resource.IsHealthy())

	// Test reset
	err := resource.Reset()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), atomic.LoadInt64(&resource.UseCount))

	// Test close
	err = resource.Close()
	assert.NoError(t, err)
	assert.False(t, resource.IsHealthy())
}

// TestWorkerResourceFactory tests the factory implementation
func TestWorkerResourceFactory(t *testing.T) {
	factory := &WorkerResourceFactory{}
	ctx := context.Background()

	// Create multiple resources
	resources := make([]Resource, 3)
	for i := 0; i < 3; i++ {
		res, err := factory.Create(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, res)
		resources[i] = res

		// Check unique IDs
		workerRes := res.(*WorkerResource)
		assert.Contains(t, workerRes.ID, "worker-")
	}

	// Test validation
	newResource := resources[0].(*WorkerResource)
	err := factory.Validate(newResource)
	assert.NoError(t, err)

	// Test old resource validation
	oldResource := &WorkerResource{
		CreatedAt: time.Now().Add(-2 * time.Hour),
		healthy:   true,
	}
	err = factory.Validate(oldResource)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "too old")
	}

	// Test overused resource validation
	overusedResource := &WorkerResource{
		CreatedAt: time.Now(),
		UseCount:  1001,
		healthy:   true,
	}
	err = factory.Validate(overusedResource)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "exceeded use limit")
	}
}

// TestResourcePoolStats tests pool statistics
func TestResourcePoolStats(t *testing.T) {
	factory := &WorkerResourceFactory{}
	pool, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize:     2,
		MaxSize:     5,
		MaxIdleTime: 1 * time.Minute,
	})
	assert.NoError(t, err)
	defer func(pool *ResourcePool) {
		err := pool.Close()
		if err != nil {
			t.Errorf("Failed to close pool: %v", err)
		}
	}(pool)

	ctx := context.Background()
	stats := pool.Stats()

	// Initial stats
	assert.Equal(t, int64(2), stats["created"])
	assert.Equal(t, int64(2), stats["available"])
	assert.Equal(t, int64(0), stats["in_use"])
	assert.Equal(t, int64(0), stats["destroyed"])
	assert.Equal(t, int64(2), stats["min_size"])
	assert.Equal(t, int64(5), stats["max_size"])

	// Acquire and check stats
	resource, _ := pool.Acquire(ctx)
	stats = pool.Stats()
	assert.Equal(t, int64(1), stats["available"])
	assert.Equal(t, int64(1), stats["in_use"])

	// Make unhealthy and release
	workerRes := resource.(*WorkerResource)
	workerRes.mu.Lock()
	workerRes.healthy = false
	workerRes.mu.Unlock()

	pool.Release(resource)
	stats = pool.Stats()
	assert.Equal(t, int64(1), stats["destroyed"])
}

// TestResourcePoolClose tests pool closure
func TestResourcePoolClose(t *testing.T) {
	factory := &WorkerResourceFactory{}
	pool, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize:     2,
		MaxSize:     5,
		MaxIdleTime: 1 * time.Minute,
	})
	assert.NoError(t, err)

	ctx := context.Background()

	// Acquire a resource
	resource, err := pool.Acquire(ctx)
	assert.NoError(t, err)

	// Close the pool
	err = pool.Close()
	assert.NoError(t, err)

	// Release should handle closed pool
	pool.Release(resource)

	// Stats should show all destroyed
	stats := pool.Stats()
	assert.Equal(t, stats["created"], stats["destroyed"])

	// Acquire should fail
	_, err = pool.Acquire(ctx)
	assert.Error(t, err)
}

// TestResourcePoolInvalidOptions tests invalid pool options
func TestResourcePoolInvalidOptions(t *testing.T) {
	factory := &WorkerResourceFactory{}

	// Negative min size
	_, err := NewResourcePool(factory, ResourcePoolOptions{
		MinSize: -1,
		MaxSize: 5,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "minimum size cannot be negative")

	// Max size less than min size
	_, err = NewResourcePool(factory, ResourcePoolOptions{
		MinSize: 5,
		MaxSize: 2,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maximum size must be >= minimum size")
}
