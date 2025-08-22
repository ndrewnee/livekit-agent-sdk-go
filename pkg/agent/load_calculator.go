package agent

import (
	"runtime"
	"sync"
	"time"
)

// LoadCalculator provides custom load calculation for workers.
//
// Implementations determine how "loaded" a worker is based on various
// metrics. The load value is used for:
//   - Job distribution decisions
//   - Auto-scaling triggers
//   - Health monitoring
//   - Status reporting to the server
//
// Load values must be normalized between 0.0 (no load) and 1.0 (full load).
type LoadCalculator interface {
	// Calculate returns a load value between 0.0 and 1.0.
	//
	// The calculation can consider any combination of metrics:
	//   - Number of active jobs vs capacity
	//   - CPU and memory usage
	//   - Job processing duration
	//   - Historical trends
	//
	// Return values:
	//   - 0.0: Worker is idle
	//   - 0.0-0.5: Light load
	//   - 0.5-0.8: Moderate load
	//   - 0.8-1.0: Heavy load
	//   - 1.0: Worker at full capacity
	Calculate(metrics LoadMetrics) float32
}

// LoadMetrics contains metrics for load calculation.
//
// This structure provides various measurements that LoadCalculator
// implementations can use to determine worker load. Not all fields
// need to be populated - calculators should handle missing data gracefully.
type LoadMetrics struct {
	// ActiveJobs is the current number of jobs being processed
	ActiveJobs  int
	
	// MaxJobs is the maximum number of concurrent jobs allowed (0 = unlimited)
	MaxJobs     int
	
	// JobDuration maps job IDs to their processing duration.
	// This helps identify long-running jobs that might indicate higher load.
	JobDuration map[string]time.Duration

	// CPUPercent is the current CPU usage percentage (0-100)
	CPUPercent    float64
	
	// MemoryPercent is the current memory usage percentage (0-100)
	MemoryPercent float64
	
	// MemoryUsedMB is the actual memory used in megabytes
	MemoryUsedMB  uint64
	
	// MemoryTotalMB is the total available memory in megabytes
	MemoryTotalMB uint64

	// RecentLoads contains recent load calculations for trend analysis.
	// Newer values should be appended to the end.
	RecentLoads []float32
}

// DefaultLoadCalculator implements the default load calculation strategy.
//
// This calculator uses a simple approach based solely on job count:
//   - With MaxJobs set: load = ActiveJobs / MaxJobs
//   - Without MaxJobs: load = ActiveJobs * 0.1 (capped at 1.0)
//
// This is suitable for workers where job count is the primary constraint
// and system resources are not a concern.
type DefaultLoadCalculator struct{}

// Calculate implements LoadCalculator using job count ratio.
//
// The calculation is straightforward:
//   - If MaxJobs > 0: Returns ActiveJobs/MaxJobs
//   - If MaxJobs = 0: Returns ActiveJobs * 0.1, capped at 1.0
//
// This assumes each job contributes equally to load and ignores
// system resource usage.
func (d *DefaultLoadCalculator) Calculate(metrics LoadMetrics) float32 {
	if metrics.MaxJobs > 0 {
		return float32(metrics.ActiveJobs) / float32(metrics.MaxJobs)
	}
	// For unlimited jobs, use a heuristic
	load := float32(metrics.ActiveJobs) * 0.1
	if load > 1.0 {
		load = 1.0
	}
	return load
}

// CPUMemoryLoadCalculator calculates load based on CPU and memory usage.
//
// This calculator combines multiple metrics using weighted averages:
//   - Job count relative to capacity
//   - CPU usage percentage
//   - Memory usage percentage
//
// The weights are configurable but should sum to 1.0 for normalized output.
// This calculator is ideal for workers that process resource-intensive jobs
// where system resources are the limiting factor.
type CPUMemoryLoadCalculator struct {
	// JobWeight is the weight given to job count (default: 0.4)
	JobWeight    float32
	
	// CPUWeight is the weight given to CPU usage (default: 0.3)
	CPUWeight    float32
	
	// MemoryWeight is the weight given to memory usage (default: 0.3)
	MemoryWeight float32
}

// NewCPUMemoryLoadCalculator creates a calculator with default weights.
//
// Default weights:
//   - Jobs: 40%
//   - CPU: 30%
//   - Memory: 30%
//
// These weights provide a balanced view of worker load, considering both
// job count and system resource utilization.
func NewCPUMemoryLoadCalculator() *CPUMemoryLoadCalculator {
	return &CPUMemoryLoadCalculator{
		JobWeight:    0.4,
		CPUWeight:    0.3,
		MemoryWeight: 0.3,
	}
}

// Calculate implements LoadCalculator using weighted system metrics.
//
// The calculation combines three components:
//  1. Job load: ActiveJobs/MaxJobs (or heuristic if unlimited)
//  2. CPU load: CPUPercent/100
//  3. Memory load: MemoryPercent/100
//
// Final load = (JobLoad * JobWeight) + (CPULoad * CPUWeight) + (MemoryLoad * MemoryWeight)
//
// The result is clamped between 0.0 and 1.0.
func (c *CPUMemoryLoadCalculator) Calculate(metrics LoadMetrics) float32 {
	var jobLoad float32
	if metrics.MaxJobs > 0 {
		jobLoad = float32(metrics.ActiveJobs) / float32(metrics.MaxJobs)
	} else {
		jobLoad = float32(metrics.ActiveJobs) * 0.1
		if jobLoad > 1.0 {
			jobLoad = 1.0
		}
	}

	cpuLoad := float32(metrics.CPUPercent / 100.0)
	memLoad := float32(metrics.MemoryPercent / 100.0)

	// Weighted average
	totalLoad := jobLoad*c.JobWeight + cpuLoad*c.CPUWeight + memLoad*c.MemoryWeight

	// Ensure load is between 0 and 1
	if totalLoad < 0 {
		totalLoad = 0
	} else if totalLoad > 1 {
		totalLoad = 1
	}

	return totalLoad
}

// PredictiveLoadCalculator uses historical data to predict future load.
//
// This calculator wraps another LoadCalculator and adds trend analysis
// to predict future load based on recent history. It's useful for:
//   - Anticipating load increases before they fully materialize
//   - Smoothing out temporary spikes or dips
//   - Making proactive scaling decisions
//
// The prediction is conservative (20% of trend) to avoid overreaction.
type PredictiveLoadCalculator struct {
	baseCalculator LoadCalculator
	windowSize     int
	mu             sync.Mutex
	history        []loadSnapshot
}

type loadSnapshot struct {
	timestamp time.Time
	load      float32
	jobCount  int
}

// NewPredictiveLoadCalculator creates a calculator with trend prediction.
//
// Parameters:
//   - base: The underlying calculator for current load calculation
//   - windowSize: Number of historical snapshots to keep (default: 10)
//
// The calculator maintains a sliding window of load history and uses
// linear regression to detect trends.
func NewPredictiveLoadCalculator(base LoadCalculator, windowSize int) *PredictiveLoadCalculator {
	if windowSize <= 0 {
		windowSize = 10
	}
	return &PredictiveLoadCalculator{
		baseCalculator: base,
		windowSize:     windowSize,
		history:        make([]loadSnapshot, 0, windowSize),
	}
}

// Calculate implements LoadCalculator with trend prediction.
//
// The calculation process:
//  1. Calculate current load using the base calculator
//  2. Add the snapshot to history
//  3. If enough history exists (3+ points), calculate trend
//  4. Apply conservative prediction: current + (trend * 0.2)
//  5. Clamp result between 0.0 and 1.0
//
// Returns the base calculation if insufficient history exists.
func (p *PredictiveLoadCalculator) Calculate(metrics LoadMetrics) float32 {
	currentLoad := p.baseCalculator.Calculate(metrics)
	
	p.mu.Lock()
	defer p.mu.Unlock()

	// Add to history
	p.history = append(p.history, loadSnapshot{
		timestamp: time.Now(),
		load:      currentLoad,
		jobCount:  metrics.ActiveJobs,
	})

	// Keep only recent history
	if len(p.history) > p.windowSize {
		p.history = p.history[len(p.history)-p.windowSize:]
	}

	// Not enough history for prediction
	if len(p.history) < 3 {
		return currentLoad
	}

	// Calculate trend
	trend := p.calculateTrend()
	
	// Predict future load based on trend
	predictedLoad := currentLoad + trend*0.2 // Conservative prediction

	// Ensure predicted load is between 0 and 1
	if predictedLoad < 0 {
		predictedLoad = 0
	} else if predictedLoad > 1 {
		predictedLoad = 1
	}

	return predictedLoad
}

func (p *PredictiveLoadCalculator) calculateTrend() float32 {
	if len(p.history) < 2 {
		return 0
	}

	// Simple linear regression to find trend
	n := float32(len(p.history))
	var sumX, sumY, sumXY, sumX2 float32

	for i, snapshot := range p.history {
		x := float32(i)
		y := snapshot.load
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}

	// Calculate slope (trend)
	denominator := n*sumX2 - sumX*sumX
	if denominator == 0 {
		return 0
	}

	slope := (n*sumXY - sumX*sumY) / denominator
	return slope
}

// LoadBatcher manages batched status updates to reduce network overhead.
//
// Instead of sending every load update immediately, the batcher collects
// updates and sends them at regular intervals. This reduces:
//   - Network traffic to the server
//   - Server processing overhead
//   - WebSocket message frequency
//
// Only the most recent update within each interval is sent.
type LoadBatcher struct {
	worker       *Worker
	interval     time.Duration
	mu           sync.Mutex
	pendingLoad  *float32
	pendingStatus *WorkerStatus
	timer        *time.Timer
	closed       bool
}

// NewLoadBatcher creates a new load update batcher.
//
// Parameters:
//   - worker: The worker whose status to update
//   - interval: Batching interval (default: 5 seconds if <= 0)
//
// The batcher will hold updates for the specified interval before
// sending them to the server.
func NewLoadBatcher(worker *Worker, interval time.Duration) *LoadBatcher {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &LoadBatcher{
		worker:   worker,
		interval: interval,
	}
}

// Start begins the batching process.
//
// Currently a no-op as batching is triggered by Update calls.
// Included for API consistency with other components.
func (b *LoadBatcher) Start() {
	// Nothing to do on start, updates are triggered by Update calls
}

// Stop stops the batcher and sends any pending update.
//
// This ensures the final status is sent to the server before shutdown.
// After Stop is called, further Update calls will be ignored.
func (b *LoadBatcher) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	if b.timer != nil {
		b.timer.Stop()
	}

	// Send final update if pending
	if b.pendingLoad != nil && b.pendingStatus != nil {
		b.worker.UpdateStatus(*b.pendingStatus, *b.pendingLoad)
	}
}

// Update queues a status update for batched sending.
//
// Multiple updates within the batching interval will be coalesced,
// with only the most recent values being sent. This method is
// non-blocking and thread-safe.
//
// Updates after Stop() are ignored.
func (b *LoadBatcher) Update(status WorkerStatus, load float32) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}

	// Store pending update
	b.pendingStatus = &status
	b.pendingLoad = &load

	// Cancel existing timer
	if b.timer != nil {
		b.timer.Stop()
	}

	// Set new timer
	b.timer = time.AfterFunc(b.interval, func() {
		b.flush()
	})
}

func (b *LoadBatcher) flush() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed || b.pendingLoad == nil || b.pendingStatus == nil {
		return
	}

	// Send the update
	b.worker.UpdateStatus(*b.pendingStatus, *b.pendingLoad)

	// Clear pending
	b.pendingLoad = nil
	b.pendingStatus = nil
	b.timer = nil
}

// SystemMetricsCollector collects system metrics for load calculation.
//
// This collector gathers CPU and memory usage information at regular
// intervals. The metrics can be used by load calculators to make
// resource-aware decisions.
//
// Note: CPU calculation uses a heuristic based on goroutine count
// rather than actual CPU usage, as accurate CPU measurement requires
// platform-specific code.
type SystemMetricsCollector struct {
	mu            sync.RWMutex
	cpuPercent    float64
	memoryPercent float64
	memoryUsedMB  uint64
	memoryTotalMB uint64
}

// NewSystemMetricsCollector creates a new system metrics collector.
//
// The collector starts a background goroutine that updates metrics
// every second. Metrics include:
//   - Memory usage (allocated and system memory)
//   - CPU usage estimate (based on goroutine count)
//
// The collector runs until the process exits.
func NewSystemMetricsCollector() *SystemMetricsCollector {
	collector := &SystemMetricsCollector{}
	go collector.collectLoop()
	return collector
}

func (s *SystemMetricsCollector) collectLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		s.collect()
	}
}

func (s *SystemMetricsCollector) collect() {
	// Get memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Calculate memory usage
	s.memoryUsedMB = m.Alloc / 1024 / 1024
	s.memoryTotalMB = m.Sys / 1024 / 1024
	if s.memoryTotalMB > 0 {
		s.memoryPercent = float64(s.memoryUsedMB) / float64(s.memoryTotalMB) * 100
	}

	// CPU calculation would require platform-specific code or external library
	// For now, we'll use goroutine count as a proxy
	numGoroutines := runtime.NumGoroutine()
	numCPU := runtime.NumCPU()
	// Simple heuristic: assume high goroutine count indicates high CPU usage
	s.cpuPercent = float64(numGoroutines) / float64(numCPU*100) * 100
	if s.cpuPercent > 100 {
		s.cpuPercent = 100
	}
}

// GetMetrics returns the current system metrics.
//
// Returns:
//   - cpuPercent: Estimated CPU usage (0-100)
//   - memoryPercent: Memory usage percentage (0-100)
//   - memoryUsedMB: Allocated memory in megabytes
//   - memoryTotalMB: Total system memory in megabytes
//
// The values are snapshots from the most recent collection cycle.
// The method is thread-safe.
func (s *SystemMetricsCollector) GetMetrics() (cpuPercent, memoryPercent float64, memoryUsedMB, memoryTotalMB uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cpuPercent, s.memoryPercent, s.memoryUsedMB, s.memoryTotalMB
}