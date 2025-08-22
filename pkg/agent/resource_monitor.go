package agent

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// ResourceMonitor monitors system resources and detects issues
type ResourceMonitor struct {
	mu                     sync.RWMutex
	logger                 *zap.Logger
	checkInterval          time.Duration
	memoryLimit            uint64 // bytes
	goroutineLimit         int
	goroutineLeakThreshold int // Number of consecutive increases to consider a leak
	stopChan               chan struct{}
	wg                     sync.WaitGroup
	
	// Metrics
	lastMemory          uint64
	lastGoroutineCount  int
	goroutineIncreases  int
	oomDetected         bool
	leakDetected        bool
	circularDepDetected bool
	
	// Callbacks
	oomCallback      func()
	leakCallback     func(count int)
	circularCallback func(deps []string)
	
	// Dependency tracking for circular detection
	dependencies     map[string][]string
	dependencyMutex  sync.RWMutex
}

// ResourceMonitorOptions configures the resource monitor
type ResourceMonitorOptions struct {
	CheckInterval          time.Duration
	MemoryLimitMB          int // Memory limit in MB (0 = 80% of system memory)
	GoroutineLimit         int // Max goroutines (0 = 10000)
	GoroutineLeakThreshold int // Consecutive increases to detect leak (0 = 5)
}

// NewResourceMonitor creates a new resource monitor
func NewResourceMonitor(logger *zap.Logger, opts ResourceMonitorOptions) *ResourceMonitor {
	if opts.CheckInterval == 0 {
		opts.CheckInterval = 10 * time.Second
	}
	if opts.GoroutineLimit == 0 {
		opts.GoroutineLimit = 10000
	}
	if opts.GoroutineLeakThreshold == 0 {
		opts.GoroutineLeakThreshold = 5
	}
	
	memLimit := uint64(opts.MemoryLimitMB) * 1024 * 1024
	if memLimit == 0 {
		// Default to 80% of system memory
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		memLimit = uint64(float64(m.Sys) * 0.8)
	}
	
	return &ResourceMonitor{
		logger:                 logger,
		checkInterval:          opts.CheckInterval,
		memoryLimit:            memLimit,
		goroutineLimit:         opts.GoroutineLimit,
		goroutineLeakThreshold: opts.GoroutineLeakThreshold,
		stopChan:               make(chan struct{}),
		dependencies:           make(map[string][]string),
	}
}

// Start begins resource monitoring
func (m *ResourceMonitor) Start(ctx context.Context) {
	m.wg.Add(1)
	go m.monitor(ctx)
}

// Stop stops the resource monitor
func (m *ResourceMonitor) Stop() {
	close(m.stopChan)
	m.wg.Wait()
}

// SetOOMCallback sets the callback for OOM detection
func (m *ResourceMonitor) SetOOMCallback(cb func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.oomCallback = cb
}

// SetLeakCallback sets the callback for goroutine leak detection
func (m *ResourceMonitor) SetLeakCallback(cb func(count int)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leakCallback = cb
}

// SetCircularDependencyCallback sets the callback for circular dependency detection
func (m *ResourceMonitor) SetCircularDependencyCallback(cb func(deps []string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.circularCallback = cb
}

// monitor runs the monitoring loop
func (m *ResourceMonitor) monitor(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.checkResources()
		}
	}
}

// checkResources performs resource checks
func (m *ResourceMonitor) checkResources() {
	// Check memory usage
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	m.mu.Lock()
	previousMemory := m.lastMemory
	m.lastMemory = memStats.Alloc
	m.mu.Unlock()
	
	// Check for OOM condition
	if memStats.Alloc > m.memoryLimit {
		m.handleOOM(memStats.Alloc)
	} else if previousMemory > m.memoryLimit && memStats.Alloc < m.memoryLimit {
		// Recovered from OOM
		m.mu.Lock()
		m.oomDetected = false
		m.mu.Unlock()
		m.logger.Info("Recovered from OOM condition",
			zap.Uint64("current_memory_mb", memStats.Alloc/1024/1024),
			zap.Uint64("limit_mb", m.memoryLimit/1024/1024),
		)
	}
	
	// Check goroutine count
	goroutineCount := runtime.NumGoroutine()
	m.checkGoroutineLeaks(goroutineCount)
	
	// Log current resource usage
	m.logger.Debug("Resource check",
		zap.Uint64("memory_mb", memStats.Alloc/1024/1024),
		zap.Uint64("memory_limit_mb", m.memoryLimit/1024/1024),
		zap.Int("goroutines", goroutineCount),
		zap.Int("goroutine_limit", m.goroutineLimit),
	)
}

// handleOOM handles out-of-memory condition
func (m *ResourceMonitor) handleOOM(currentMemory uint64) {
	m.mu.Lock()
	alreadyDetected := m.oomDetected
	m.oomDetected = true
	callback := m.oomCallback
	m.mu.Unlock()
	
	if !alreadyDetected {
		m.logger.Error("OOM condition detected",
			zap.Uint64("current_memory_mb", currentMemory/1024/1024),
			zap.Uint64("limit_mb", m.memoryLimit/1024/1024),
		)
		
		// Force garbage collection
		runtime.GC()
		debug.FreeOSMemory()
		
		// Call callback if set
		if callback != nil {
			callback()
		}
	}
}

// checkGoroutineLeaks checks for potential goroutine leaks
func (m *ResourceMonitor) checkGoroutineLeaks(currentCount int) {
	m.mu.Lock()
	previousCount := m.lastGoroutineCount
	m.lastGoroutineCount = currentCount
	
	// Check if goroutines are increasing
	if currentCount > previousCount && previousCount > 0 {
		m.goroutineIncreases++
	} else if currentCount <= previousCount {
		m.goroutineIncreases = 0 // Reset counter
	}
	
	// Check for leak pattern
	leakDetected := m.goroutineIncreases >= m.goroutineLeakThreshold ||
		currentCount > m.goroutineLimit
	
	wasLeakDetected := m.leakDetected
	m.leakDetected = leakDetected
	callback := m.leakCallback
	m.mu.Unlock()
	
	if leakDetected && !wasLeakDetected {
		m.logger.Error("Goroutine leak detected",
			zap.Int("count", currentCount),
			zap.Int("limit", m.goroutineLimit),
			zap.Int("consecutive_increases", m.goroutineIncreases),
		)
		
		if callback != nil {
			callback(currentCount)
		}
	} else if !leakDetected && wasLeakDetected {
		m.logger.Info("Goroutine leak resolved",
			zap.Int("count", currentCount),
		)
	}
}

// AddDependency adds a dependency relationship for circular detection
func (m *ResourceMonitor) AddDependency(from, to string) {
	m.dependencyMutex.Lock()
	defer m.dependencyMutex.Unlock()
	
	if m.dependencies[from] == nil {
		m.dependencies[from] = make([]string, 0)
	}
	m.dependencies[from] = append(m.dependencies[from], to)
	
	// Check for circular dependency
	if cycle := m.detectCycle(from); len(cycle) > 0 {
		m.handleCircularDependency(cycle)
	}
}

// detectCycle uses DFS to detect circular dependencies
func (m *ResourceMonitor) detectCycle(start string) []string {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	path := []string{}
	
	var dfs func(node string) []string
	dfs = func(node string) []string {
		visited[node] = true
		recStack[node] = true
		path = append(path, node)
		
		for _, neighbor := range m.dependencies[node] {
			if !visited[neighbor] {
				if cycle := dfs(neighbor); len(cycle) > 0 {
					return cycle
				}
			} else if recStack[neighbor] {
				// Found cycle - extract the cycle path
				cycleStart := 0
				for i, n := range path {
					if n == neighbor {
						cycleStart = i
						break
					}
				}
				return append(path[cycleStart:], neighbor)
			}
		}
		
		path = path[:len(path)-1]
		recStack[node] = false
		return nil
	}
	
	return dfs(start)
}

// handleCircularDependency handles detected circular dependencies
func (m *ResourceMonitor) handleCircularDependency(cycle []string) {
	m.mu.Lock()
	alreadyDetected := m.circularDepDetected
	m.circularDepDetected = true
	callback := m.circularCallback
	m.mu.Unlock()
	
	if !alreadyDetected {
		m.logger.Error("Circular dependency detected",
			zap.Any("cycle", cycle),
		)
		
		if callback != nil {
			callback(cycle)
		}
	}
}

// GetMetrics returns current resource metrics
func (m *ResourceMonitor) GetMetrics() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	metrics := map[string]interface{}{
		"memory_alloc_mb":       memStats.Alloc / 1024 / 1024,
		"memory_sys_mb":         memStats.Sys / 1024 / 1024,
		"memory_limit_mb":       m.memoryLimit / 1024 / 1024,
		"goroutine_count":       runtime.NumGoroutine(),
		"goroutine_limit":       m.goroutineLimit,
		"oom_detected":          m.oomDetected,
		"leak_detected":         m.leakDetected,
		"circular_dep_detected": m.circularDepDetected,
		"gc_runs":              memStats.NumGC,
		"gc_pause_ms":          float64(memStats.PauseNs[(memStats.NumGC+255)%256]) / 1e6,
	}
	
	return metrics
}

// IsHealthy returns true if no resource issues are detected
func (m *ResourceMonitor) IsHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	return !m.oomDetected && !m.leakDetected && !m.circularDepDetected
}

// ResourceThresholds defines resource usage thresholds
type ResourceThresholds struct {
	MemoryWarningPercent  float64 // Warn at this % of limit (default 80%)
	MemoryCriticalPercent float64 // Critical at this % of limit (default 90%)
	GoroutineWarning      int     // Warn at this count (default 5000)
	GoroutineCritical     int     // Critical at this count (default 8000)
}

// GetResourceStatus returns detailed resource status
func (m *ResourceMonitor) GetResourceStatus() ResourceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	memoryPercent := float64(memStats.Alloc) / float64(m.memoryLimit) * 100
	goroutineCount := runtime.NumGoroutine()
	
	status := ResourceStatus{
		MemoryUsageMB:      memStats.Alloc / 1024 / 1024,
		MemoryLimitMB:      m.memoryLimit / 1024 / 1024,
		MemoryPercent:      memoryPercent,
		GoroutineCount:     goroutineCount,
		GoroutineLimit:     m.goroutineLimit,
		OOMDetected:        m.oomDetected,
		LeakDetected:       m.leakDetected,
		CircularDepDetected: m.circularDepDetected,
		Timestamp:          time.Now(),
	}
	
	// Determine health level
	if m.oomDetected || m.leakDetected || m.circularDepDetected {
		status.HealthLevel = ResourceHealthCritical
	} else if memoryPercent > 90 || goroutineCount > 8000 {
		status.HealthLevel = ResourceHealthCritical
	} else if memoryPercent > 80 || goroutineCount > 5000 {
		status.HealthLevel = ResourceHealthWarning
	} else {
		status.HealthLevel = ResourceHealthGood
	}
	
	return status
}

// ResourceStatus represents current resource status
type ResourceStatus struct {
	MemoryUsageMB       uint64
	MemoryLimitMB       uint64
	MemoryPercent       float64
	GoroutineCount      int
	GoroutineLimit      int
	OOMDetected         bool
	LeakDetected        bool
	CircularDepDetected bool
	HealthLevel         ResourceHealthLevel
	Timestamp           time.Time
}

// ResourceHealthLevel represents resource health status
type ResourceHealthLevel int

const (
	ResourceHealthGood ResourceHealthLevel = iota
	ResourceHealthWarning
	ResourceHealthCritical
)

func (r ResourceHealthLevel) String() string {
	switch r {
	case ResourceHealthGood:
		return "good"
	case ResourceHealthWarning:
		return "warning"
	case ResourceHealthCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// ResourceGuard provides automatic resource protection
type ResourceGuard struct {
	monitor      *ResourceMonitor
	maxRetries   int
	backoffMs    int64
	abortOnOOM   bool
	panicOnLeak  bool
}

// NewResourceGuard creates a new resource guard
func NewResourceGuard(monitor *ResourceMonitor) *ResourceGuard {
	return &ResourceGuard{
		monitor:     monitor,
		maxRetries:  3,
		backoffMs:   100,
		abortOnOOM:  false,
		panicOnLeak: false,
	}
}

// ExecuteWithProtection executes a function with resource protection
func (g *ResourceGuard) ExecuteWithProtection(fn func() error) error {
	retries := 0
	
	for retries < g.maxRetries {
		// Check resources before execution
		if !g.monitor.IsHealthy() {
			status := g.monitor.GetResourceStatus()
			
			if status.OOMDetected && g.abortOnOOM {
				return fmt.Errorf("execution aborted: OOM detected")
			}
			
			if status.LeakDetected && g.panicOnLeak {
				panic(fmt.Sprintf("goroutine leak detected: %d goroutines", status.GoroutineCount))
			}
			
			// Wait with exponential backoff
			backoff := time.Duration(atomic.AddInt64(&g.backoffMs, g.backoffMs)) * time.Millisecond
			time.Sleep(backoff)
			retries++
			continue
		}
		
		// Execute function
		return fn()
	}
	
	return fmt.Errorf("execution failed after %d retries due to resource constraints", retries)
}