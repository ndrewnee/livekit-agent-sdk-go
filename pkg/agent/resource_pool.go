package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Resource represents a pooled resource that can be acquired and released
type Resource interface {
	// IsHealthy checks if the resource is still usable
	IsHealthy() bool
	// Close cleans up the resource
	Close() error
	// Reset prepares the resource for reuse
	Reset() error
}

// ResourceFactory creates new resources for the pool
type ResourceFactory interface {
	Create(ctx context.Context) (Resource, error)
	Validate(resource Resource) error
}

// ResourcePool manages a pool of reusable resources
type ResourcePool struct {
	factory     ResourceFactory
	resources   chan Resource
	minSize     int
	maxSize     int
	maxIdleTime time.Duration
	
	// Metrics
	created     int64
	inUse       int64
	available   int64
	destroyed   int64
	
	mu          sync.Mutex
	closed      bool
	cleanupStop chan struct{}
	wg          sync.WaitGroup
}

// ResourcePoolOptions configures the resource pool
type ResourcePoolOptions struct {
	MinSize     int           // Minimum number of resources to maintain
	MaxSize     int           // Maximum number of resources in pool
	MaxIdleTime time.Duration // Maximum time a resource can be idle
}

// NewResourcePool creates a new resource pool
func NewResourcePool(factory ResourceFactory, opts ResourcePoolOptions) (*ResourcePool, error) {
	if opts.MinSize < 0 {
		return nil, fmt.Errorf("minimum size cannot be negative")
	}
	if opts.MaxSize < opts.MinSize {
		return nil, fmt.Errorf("maximum size must be >= minimum size")
	}
	if opts.MaxIdleTime == 0 {
		opts.MaxIdleTime = 5 * time.Minute
	}

	pool := &ResourcePool{
		factory:     factory,
		resources:   make(chan Resource, opts.MaxSize),
		minSize:     opts.MinSize,
		maxSize:     opts.MaxSize,
		maxIdleTime: opts.MaxIdleTime,
		cleanupStop: make(chan struct{}),
	}

	// Pre-warm the pool with minimum resources
	ctx := context.Background()
	for i := 0; i < opts.MinSize; i++ {
		resource, err := factory.Create(ctx)
		if err != nil {
			// Clean up any created resources
			pool.Close()
			return nil, fmt.Errorf("failed to create initial resource: %w", err)
		}
		pool.resources <- resource
		atomic.AddInt64(&pool.created, 1)
		atomic.AddInt64(&pool.available, 1)
	}

	// Start cleanup goroutine
	pool.wg.Add(1)
	go pool.cleanupIdleResources()

	return pool, nil
}

// Acquire gets a resource from the pool or creates a new one
func (pool *ResourcePool) Acquire(ctx context.Context) (Resource, error) {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil, fmt.Errorf("pool is closed")
	}
	pool.mu.Unlock()

	// Try to get an existing resource
	select {
	case resource := <-pool.resources:
		atomic.AddInt64(&pool.available, -1)
		
		// Validate the resource
		if err := pool.factory.Validate(resource); err != nil {
			// Resource is invalid, destroy it
			resource.Close()
			atomic.AddInt64(&pool.destroyed, 1)
			
			// Try to create a new one
			return pool.createNew(ctx)
		}
		
		// Reset for reuse
		if err := resource.Reset(); err != nil {
			resource.Close()
			atomic.AddInt64(&pool.destroyed, 1)
			return pool.createNew(ctx)
		}
		
		atomic.AddInt64(&pool.inUse, 1)
		return resource, nil
		
	default:
		// No resources available, try to create new one
		return pool.createNew(ctx)
	}
}

// Release returns a resource to the pool
func (pool *ResourcePool) Release(resource Resource) {
	if resource == nil {
		return
	}

	pool.mu.Lock()
	defer pool.mu.Unlock()

	atomic.AddInt64(&pool.inUse, -1)

	if pool.closed {
		resource.Close()
		atomic.AddInt64(&pool.destroyed, 1)
		return
	}

	// Check if resource is still healthy
	if !resource.IsHealthy() {
		resource.Close()
		atomic.AddInt64(&pool.destroyed, 1)
		
		// Try to maintain minimum pool size
		if pool.Size() < pool.minSize {
			go pool.createAsync()
		}
		return
	}

	// Try to return to pool
	select {
	case pool.resources <- resource:
		atomic.AddInt64(&pool.available, 1)
	default:
		// Pool is full, close the resource
		resource.Close()
		atomic.AddInt64(&pool.destroyed, 1)
	}
}

// Size returns the current number of resources (available + in use)
func (pool *ResourcePool) Size() int {
	return int(atomic.LoadInt64(&pool.available) + atomic.LoadInt64(&pool.inUse))
}

// Available returns the number of available resources
func (pool *ResourcePool) Available() int {
	return int(atomic.LoadInt64(&pool.available))
}

// InUse returns the number of resources currently in use
func (pool *ResourcePool) InUse() int {
	return int(atomic.LoadInt64(&pool.inUse))
}

// Stats returns pool statistics
func (pool *ResourcePool) Stats() map[string]int64 {
	return map[string]int64{
		"created":   atomic.LoadInt64(&pool.created),
		"available": atomic.LoadInt64(&pool.available),
		"in_use":    atomic.LoadInt64(&pool.inUse),
		"destroyed": atomic.LoadInt64(&pool.destroyed),
		"min_size":  int64(pool.minSize),
		"max_size":  int64(pool.maxSize),
	}
}

// Close closes the pool and all resources
func (pool *ResourcePool) Close() error {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil
	}
	pool.closed = true
	pool.mu.Unlock()

	// Stop cleanup routine
	close(pool.cleanupStop)
	pool.wg.Wait()

	// Close all resources
	close(pool.resources)
	for resource := range pool.resources {
		resource.Close()
		atomic.AddInt64(&pool.destroyed, 1)
	}

	return nil
}

// createNew creates a new resource if under max size
func (pool *ResourcePool) createNew(ctx context.Context) (Resource, error) {
	currentSize := pool.Size()
	if currentSize >= pool.maxSize {
		return nil, fmt.Errorf("pool at maximum capacity (%d)", pool.maxSize)
	}

	resource, err := pool.factory.Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	atomic.AddInt64(&pool.created, 1)
	atomic.AddInt64(&pool.inUse, 1)
	return resource, nil
}

// createAsync creates a resource asynchronously
func (pool *ResourcePool) createAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resource, err := pool.factory.Create(ctx)
	if err != nil {
		return
	}

	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		resource.Close()
		return
	}
	pool.mu.Unlock()

	select {
	case pool.resources <- resource:
		atomic.AddInt64(&pool.created, 1)
		atomic.AddInt64(&pool.available, 1)
	default:
		resource.Close()
		atomic.AddInt64(&pool.destroyed, 1)
	}
}

// cleanupIdleResources periodically cleans up idle resources
func (pool *ResourcePool) cleanupIdleResources() {
	defer pool.wg.Done()
	
	ticker := time.NewTicker(pool.maxIdleTime / 2)
	defer ticker.Stop()

	for {
		select {
		case <-pool.cleanupStop:
			return
		case <-ticker.C:
			pool.cleanupIdle()
		}
	}
}

// cleanupIdle removes excess idle resources
func (pool *ResourcePool) cleanupIdle() {
	available := pool.Available()
	if available <= pool.minSize {
		return
	}

	// Remove excess resources
	toRemove := available - pool.minSize
	for i := 0; i < toRemove; i++ {
		select {
		case resource := <-pool.resources:
			atomic.AddInt64(&pool.available, -1)
			resource.Close()
			atomic.AddInt64(&pool.destroyed, 1)
		default:
			return
		}
	}
}

// WorkerResource represents a pooled worker resource
type WorkerResource struct {
	ID          string
	CreatedAt   time.Time
	LastUsedAt  time.Time
	UseCount    int64
	healthy     bool
	mu          sync.Mutex
}

func (w *WorkerResource) IsHealthy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.healthy
}

func (w *WorkerResource) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.healthy = false
	return nil
}

func (w *WorkerResource) Reset() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.LastUsedAt = time.Now()
	atomic.AddInt64(&w.UseCount, 1)
	return nil
}

// WorkerResourceFactory creates worker resources
type WorkerResourceFactory struct {
	idCounter int64
}

func (f *WorkerResourceFactory) Create(ctx context.Context) (Resource, error) {
	id := atomic.AddInt64(&f.idCounter, 1)
	return &WorkerResource{
		ID:        fmt.Sprintf("worker-%d", id),
		CreatedAt: time.Now(),
		healthy:   true,
	}, nil
}

func (f *WorkerResourceFactory) Validate(resource Resource) error {
	worker, ok := resource.(*WorkerResource)
	if !ok {
		return fmt.Errorf("invalid resource type")
	}
	
	// Check if resource is too old
	if time.Since(worker.CreatedAt) > 1*time.Hour {
		return fmt.Errorf("resource too old")
	}
	
	// Check if resource has been used too many times
	if atomic.LoadInt64(&worker.UseCount) > 1000 {
		return fmt.Errorf("resource exceeded use limit")
	}
	
	return nil
}