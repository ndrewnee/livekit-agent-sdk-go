package agent

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"
)

// ResourceLimiter enforces hard limits on system resources to prevent agents from consuming
// excessive memory, CPU, or file descriptors. It monitors resource usage periodically and
// can take corrective actions like forcing garbage collection, CPU throttling, or logging
// warnings when limits are exceeded. This helps ensure stable operation in production environments.
type ResourceLimiter struct {
	mu     sync.RWMutex
	logger *zap.Logger
	
	// Configuration
	memoryLimitBytes    uint64
	cpuQuotaPercent     int // 100 = 1 CPU core
	maxFileDescriptors  int
	checkInterval       time.Duration
	
	// State
	enforcing           atomic.Bool
	memoryViolations    int64
	cpuViolations       int64
	fdViolations        int64
	lastCPUTime         int64
	lastCheckTime       time.Time
	
	// CPU throttling
	cpuThrottler        *CPUThrottler
	
	// File descriptor tracking
	fdTracker           *FileDescriptorTracker
	
	// Callbacks
	onMemoryLimitExceeded func(usage, limit uint64)
	onCPULimitExceeded    func(usage float64)
	onFDLimitExceeded     func(usage, limit int)
}

// ResourceLimiterOptions configures the behavior and limits of a ResourceLimiter.
// All limits are optional and will use sensible defaults if not specified.
// Setting EnforceHardLimits to true enables active enforcement like garbage collection
// and CPU throttling, while false only logs violations.
type ResourceLimiterOptions struct {
	MemoryLimitMB      int           // Memory limit in MB
	CPUQuotaPercent    int           // CPU quota as percentage (100 = 1 core)
	MaxFileDescriptors int           // Max file descriptors
	CheckInterval      time.Duration // How often to check limits
	EnforceHardLimits  bool          // If true, will force GC and throttle
}

// NewResourceLimiter creates a new resource limiter with the specified options.
// Default values are applied for any unspecified limits:
//   - Memory: 1GB
//   - CPU: 200% (2 cores)
//   - File descriptors: 1024
//   - Check interval: 1 second
func NewResourceLimiter(logger *zap.Logger, opts ResourceLimiterOptions) *ResourceLimiter {
	if opts.CheckInterval == 0 {
		opts.CheckInterval = 1 * time.Second
	}
	if opts.MemoryLimitMB == 0 {
		opts.MemoryLimitMB = 1024 // 1GB default
	}
	if opts.CPUQuotaPercent == 0 {
		opts.CPUQuotaPercent = 200 // 2 cores default
	}
	if opts.MaxFileDescriptors == 0 {
		opts.MaxFileDescriptors = 1024
	}
	
	rl := &ResourceLimiter{
		logger:             logger,
		memoryLimitBytes:   uint64(opts.MemoryLimitMB) * 1024 * 1024,
		cpuQuotaPercent:    opts.CPUQuotaPercent,
		maxFileDescriptors: opts.MaxFileDescriptors,
		checkInterval:      opts.CheckInterval,
		lastCheckTime:      time.Now(),
	}
	
	// Initialize CPU throttler
	rl.cpuThrottler = NewCPUThrottler(opts.CPUQuotaPercent)
	
	// Initialize FD tracker
	rl.fdTracker = NewFileDescriptorTracker()
	
	rl.enforcing.Store(opts.EnforceHardLimits)
	
	return rl
}

// Start begins periodic resource monitoring and enforcement in a background goroutine.
// The monitoring continues until the provided context is cancelled. This should be
// called once when the agent starts up.
func (rl *ResourceLimiter) Start(ctx context.Context) {
	go rl.enforce(ctx)
}

// enforce runs the enforcement loop
func (rl *ResourceLimiter) enforce(ctx context.Context) {
	ticker := time.NewTicker(rl.checkInterval)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.checkAndEnforce()
		}
	}
}

// checkAndEnforce checks resource usage and enforces limits
func (rl *ResourceLimiter) checkAndEnforce() {
	// Check memory
	memUsage := rl.checkMemoryUsage()
	
	// Check CPU
	cpuUsage := rl.checkCPUUsage()
	
	// Check file descriptors
	fdUsage := rl.checkFileDescriptors()
	
	// Log current usage
	rl.logger.Debug("Resource usage check",
		zap.Uint64("memory_mb", memUsage/1024/1024),
		zap.Uint64("memory_limit_mb", rl.memoryLimitBytes/1024/1024),
		zap.Float64("cpu_percent", cpuUsage),
		zap.Int("cpu_limit_percent", rl.cpuQuotaPercent),
		zap.Int("file_descriptors", fdUsage),
		zap.Int("fd_limit", rl.maxFileDescriptors),
	)
}

// checkMemoryUsage checks and enforces memory limits
func (rl *ResourceLimiter) checkMemoryUsage() uint64 {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	currentUsage := memStats.Alloc
	
	if currentUsage > rl.memoryLimitBytes {
		atomic.AddInt64(&rl.memoryViolations, 1)
		
		rl.logger.Warn("Memory limit exceeded",
			zap.Uint64("usage_mb", currentUsage/1024/1024),
			zap.Uint64("limit_mb", rl.memoryLimitBytes/1024/1024),
			zap.Int64("violations", atomic.LoadInt64(&rl.memoryViolations)),
		)
		
		// Call callback
		if rl.onMemoryLimitExceeded != nil {
			rl.onMemoryLimitExceeded(currentUsage, rl.memoryLimitBytes)
		}
		
		// Enforce limit if enabled
		if rl.enforcing.Load() {
			rl.enforceMemoryLimit()
		}
	}
	
	return currentUsage
}

// enforceMemoryLimit attempts to reduce memory usage
func (rl *ResourceLimiter) enforceMemoryLimit() {
	rl.logger.Info("Enforcing memory limit - triggering aggressive GC")
	
	// Force multiple GC cycles
	for i := 0; i < 3; i++ {
		runtime.GC()
		debug.FreeOSMemory()
		time.Sleep(100 * time.Millisecond)
	}
	
	// Check if we're still over limit
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	if memStats.Alloc > rl.memoryLimitBytes {
		// Still over limit, set memory limit at OS level if possible
		rl.setOSMemoryLimit()
	}
}

// setOSMemoryLimit attempts to set OS-level memory limit
func (rl *ResourceLimiter) setOSMemoryLimit() {
	// This is platform-specific
	// On Unix systems, we can use setrlimit
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_AS, &rLimit)
	if err == nil {
		rLimit.Cur = rl.memoryLimitBytes
		if err := syscall.Setrlimit(syscall.RLIMIT_AS, &rLimit); err != nil {
			rl.logger.Error("Failed to set OS memory limit", zap.Error(err))
		} else {
			rl.logger.Info("Set OS memory limit", zap.Uint64("limit_bytes", rl.memoryLimitBytes))
		}
	}
}

// checkCPUUsage checks and enforces CPU limits
func (rl *ResourceLimiter) checkCPUUsage() float64 {
	now := time.Now()
	
	// Get current process CPU time
	var rusage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &rusage); err != nil {
		rl.logger.Error("Failed to get CPU usage", zap.Error(err))
		return 0
	}
	
	// Calculate CPU time in nanoseconds
	currentCPUTime := rusage.Utime.Nano() + rusage.Stime.Nano()
	
	// Calculate CPU usage percentage
	if rl.lastCPUTime > 0 {
		cpuDelta := currentCPUTime - rl.lastCPUTime
		timeDelta := now.Sub(rl.lastCheckTime).Nanoseconds()
		
		if timeDelta > 0 {
			// CPU usage as percentage (multiplied by number of cores for multi-core)
			cpuPercent := float64(cpuDelta) / float64(timeDelta) * 100.0 * float64(runtime.NumCPU())
			
			if cpuPercent > float64(rl.cpuQuotaPercent) {
				atomic.AddInt64(&rl.cpuViolations, 1)
				
				rl.logger.Warn("CPU limit exceeded",
					zap.Float64("usage_percent", cpuPercent),
					zap.Int("limit_percent", rl.cpuQuotaPercent),
					zap.Int64("violations", atomic.LoadInt64(&rl.cpuViolations)),
				)
				
				// Call callback
				if rl.onCPULimitExceeded != nil {
					rl.onCPULimitExceeded(cpuPercent)
				}
				
				// Enforce limit if enabled
				if rl.enforcing.Load() {
					rl.cpuThrottler.Throttle(cpuPercent)
				}
			}
			
			rl.lastCPUTime = currentCPUTime
			rl.lastCheckTime = now
			
			return cpuPercent
		}
	} else {
		// First measurement
		rl.lastCPUTime = currentCPUTime
		rl.lastCheckTime = now
	}
	
	return 0
}

// checkFileDescriptors checks file descriptor usage
func (rl *ResourceLimiter) checkFileDescriptors() int {
	currentFDs := rl.fdTracker.GetCurrentCount()
	
	if currentFDs > rl.maxFileDescriptors {
		atomic.AddInt64(&rl.fdViolations, 1)
		
		rl.logger.Warn("File descriptor limit exceeded",
			zap.Int("usage", currentFDs),
			zap.Int("limit", rl.maxFileDescriptors),
			zap.Int64("violations", atomic.LoadInt64(&rl.fdViolations)),
		)
		
		// Call callback
		if rl.onFDLimitExceeded != nil {
			rl.onFDLimitExceeded(currentFDs, rl.maxFileDescriptors)
		}
		
		// Log open files for debugging
		if rl.enforcing.Load() {
			rl.fdTracker.LogOpenFiles(rl.logger)
		}
	}
	
	return currentFDs
}

// SetMemoryLimitCallback sets a callback function to be invoked when memory limits are exceeded.
// The callback receives the current memory usage and limit in bytes. This can be used
// to implement custom behaviors like alerting or adaptive resource management.
func (rl *ResourceLimiter) SetMemoryLimitCallback(cb func(usage, limit uint64)) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.onMemoryLimitExceeded = cb
}

// SetCPULimitCallback sets a callback function to be invoked when CPU limits are exceeded.
// The callback receives the current CPU usage as a percentage. This can be used for
// adaptive behaviors like reducing job concurrency or triggering alerts.
func (rl *ResourceLimiter) SetCPULimitCallback(cb func(usage float64)) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.onCPULimitExceeded = cb
}

// SetFDLimitCallback sets a callback function to be invoked when file descriptor limits are exceeded.
// The callback receives the current usage and limit counts. This can be used to trigger
// connection cleanup or implement connection pooling strategies.
func (rl *ResourceLimiter) SetFDLimitCallback(cb func(usage, limit int)) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.onFDLimitExceeded = cb
}

// GetMetrics returns a snapshot of current resource usage and violation counts.
// This is useful for monitoring, alerting, and debugging resource-related issues.
// The returned map contains current usage, limits, and violation counts for all tracked resources.
func (rl *ResourceLimiter) GetMetrics() map[string]interface{} {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	
	fdCount := rl.fdTracker.GetCurrentCount()
	
	return map[string]interface{}{
		"memory_usage_mb":      memStats.Alloc / 1024 / 1024,
		"memory_limit_mb":      rl.memoryLimitBytes / 1024 / 1024,
		"memory_violations":    atomic.LoadInt64(&rl.memoryViolations),
		"cpu_quota_percent":    rl.cpuQuotaPercent,
		"cpu_violations":       atomic.LoadInt64(&rl.cpuViolations),
		"file_descriptors":     fdCount,
		"fd_limit":            rl.maxFileDescriptors,
		"fd_violations":       atomic.LoadInt64(&rl.fdViolations),
		"enforcing":           rl.enforcing.Load(),
	}
}

// CPUThrottler implements CPU usage throttling by applying sleep delays and temporarily
// reducing GOMAXPROCS when CPU usage exceeds quotas. This helps prevent agents from
// consuming excessive CPU resources in shared environments.
type CPUThrottler struct {
	mu              sync.Mutex
	quotaPercent    int
	lastThrottleTime time.Time
	throttleDuration time.Duration
}

// NewCPUThrottler creates a new CPU throttler with the specified quota percentage.
// The quota represents the maximum allowed CPU usage as a percentage (100 = 1 CPU core).
func NewCPUThrottler(quotaPercent int) *CPUThrottler {
	return &CPUThrottler{
		quotaPercent:     quotaPercent,
		throttleDuration: 10 * time.Millisecond,
	}
}

// Throttle applies CPU throttling proportional to the excess usage above the quota.
// It uses sleep delays and temporarily reduces GOMAXPROCS for severe overages.
// The throttling is applied immediately in the calling goroutine.
func (t *CPUThrottler) Throttle(currentUsagePercent float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	// Calculate how much to throttle
	excessPercent := currentUsagePercent - float64(t.quotaPercent)
	if excessPercent <= 0 {
		return
	}
	
	// Throttle proportionally to excess
	sleepRatio := excessPercent / currentUsagePercent
	sleepDuration := time.Duration(float64(t.throttleDuration) * sleepRatio)
	
	// Apply throttling
	runtime.Gosched() // Yield to other goroutines
	time.Sleep(sleepDuration)
	
	// Reduce GOMAXPROCS temporarily if significantly over limit
	if excessPercent > 50 {
		currentProcs := runtime.GOMAXPROCS(0)
		targetProcs := int(float64(currentProcs) * float64(t.quotaPercent) / currentUsagePercent)
		if targetProcs < 1 {
			targetProcs = 1
		}
		
		runtime.GOMAXPROCS(targetProcs)
		
		// Restore after a delay
		go func() {
			time.Sleep(100 * time.Millisecond)
			runtime.GOMAXPROCS(currentProcs)
		}()
	}
}

// FileDescriptorTracker monitors the number of open file descriptors for the current process.
// It can count file descriptors and categorize them by type (files, sockets, pipes, etc.)
// to help diagnose file descriptor leaks and resource usage patterns.
type FileDescriptorTracker struct {
	mu sync.RWMutex
}

// NewFileDescriptorTracker creates a new file descriptor tracker.
// The tracker works by examining the /proc/self/fd directory on Unix systems.
func NewFileDescriptorTracker() *FileDescriptorTracker {
	return &FileDescriptorTracker{}
}

// GetCurrentCount returns the current number of open file descriptors for the process.
// On Unix systems, this counts entries in /proc/self/fd. If that fails, it falls back
// to attempting to dup file descriptors to estimate the count.
func (t *FileDescriptorTracker) GetCurrentCount() int {
	// On Unix systems, count files in /proc/self/fd
	pid := os.Getpid()
	fdPath := fmt.Sprintf("/proc/%d/fd", pid)
	
	entries, err := os.ReadDir(fdPath)
	if err != nil {
		// Fallback: try to get from resource limits
		var rLimit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err == nil {
			// Estimate based on trying to dup stderr
			count := 0
			for i := 3; i < int(rLimit.Cur); i++ {
				if _, err := syscall.Dup(i); err == nil {
					syscall.Close(i)
					count++
				}
			}
			return count + 3 // stdin, stdout, stderr
		}
		return -1
	}
	
	return len(entries)
}

// LogOpenFiles logs detailed information about currently open file descriptors,
// categorized by type (files, sockets, pipes, etc.). This is useful for debugging
// file descriptor leaks and understanding resource usage patterns.
func (t *FileDescriptorTracker) LogOpenFiles(logger *zap.Logger) {
	pid := os.Getpid()
	fdPath := fmt.Sprintf("/proc/%d/fd", pid)
	
	entries, err := os.ReadDir(fdPath)
	if err != nil {
		logger.Error("Failed to read file descriptors", zap.Error(err))
		return
	}
	
	// Group by type
	fileTypes := make(map[string]int)
	
	for _, entry := range entries {
		link, err := os.Readlink(fdPath + "/" + entry.Name())
		if err != nil {
			continue
		}
		
		// Categorize by type
		switch {
		case link == "pipe:[0]" || link[:5] == "pipe:":
			fileTypes["pipe"]++
		case link[:7] == "socket:":
			fileTypes["socket"]++
		case link[:11] == "anon_inode:":
			fileTypes["anon_inode"]++
		case link[0] == '/':
			fileTypes["file"]++
		default:
			fileTypes["other"]++
		}
	}
	
	logger.Info("Open file descriptors by type",
		zap.Int("total", len(entries)),
		zap.Any("types", fileTypes),
	)
}

// GetSystemResourceLimits retrieves the current OS-level resource limits for the process.
// It returns limits for memory, file descriptors, and CPU time if available. This is
// useful for understanding the environment constraints and setting appropriate agent limits.
func GetSystemResourceLimits() (map[string]uint64, error) {
	limits := make(map[string]uint64)
	
	// Memory limit
	var memLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_AS, &memLimit); err == nil {
		limits["memory_bytes"] = memLimit.Cur
	}
	
	// File descriptor limit
	var fdLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &fdLimit); err == nil {
		limits["file_descriptors"] = fdLimit.Cur
	}
	
	// CPU limit (if available)
	var cpuLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CPU, &cpuLimit); err == nil {
		limits["cpu_seconds"] = cpuLimit.Cur
	}
	
	return limits, nil
}

// ResourceLimitGuard provides automatic resource limit checking before executing operations.
// It can be used to prevent resource-intensive operations from running when the system
// is already near its limits, helping maintain stability and prevent resource exhaustion.
type ResourceLimitGuard struct {
	limiter *ResourceLimiter
	name    string
}

// NewGuard creates a new ResourceLimitGuard for protecting a named operation.
// The guard will check resource usage before allowing operations to proceed.
// The name is used for error messages and logging.
func (rl *ResourceLimiter) NewGuard(name string) *ResourceLimitGuard {
	return &ResourceLimitGuard{
		limiter: rl,
		name:    name,
	}
}

// Execute runs the provided function only if resource usage is below safety thresholds.
// It checks memory and file descriptor usage before execution and returns an error
// if either resource is above 90% of its limit. This prevents operations from
// running when the system is already under resource pressure.
func (g *ResourceLimitGuard) Execute(ctx context.Context, fn func() error) error {
	// Check resources before execution
	metrics := g.limiter.GetMetrics()
	
	memUsage := metrics["memory_usage_mb"].(uint64)
	memLimit := metrics["memory_limit_mb"].(uint64)
	
	if memUsage > memLimit*9/10 { // 90% threshold
		return fmt.Errorf("operation %s aborted: memory usage too high (%d/%d MB)", 
			g.name, memUsage, memLimit)
	}
	
	fdCount := metrics["file_descriptors"].(int)
	fdLimit := metrics["fd_limit"].(int)
	
	if fdCount > fdLimit*9/10 { // 90% threshold
		return fmt.Errorf("operation %s aborted: too many file descriptors (%d/%d)", 
			g.name, fdCount, fdLimit)
	}
	
	// Execute function
	return fn()
}