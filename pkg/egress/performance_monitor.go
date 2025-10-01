package egress

import (
	"fmt"
	"sync"
	"time"
)

// PerformanceMetrics tracks CPU and memory usage for the egress pipeline
type PerformanceMetrics struct {
	CPUPercent      float64   `json:"cpu_percent"`
	MemoryMB        uint64    `json:"memory_mb"`
	GoroutineCount  int       `json:"goroutine_count"`
	LastUpdated     time.Time `json:"last_updated"`

	// Performance targets from REQUIREMENTS.md
	CPUThreshold    float64   `json:"cpu_threshold"`    // < 3% per stream
	MemoryThreshold uint64    `json:"memory_threshold"`  // < 100MB per stream
}

// PerformanceMonitor monitors resource usage
type PerformanceMonitor struct {
	mu             sync.RWMutex
	metrics        PerformanceMetrics
	startTime      time.Time
	streamCount    int
	stopChan       chan struct{}
	sysMonitor     *SystemMonitor // Real system monitor
}

// NewPerformanceMonitor creates a new performance monitor
func NewPerformanceMonitor(streamCount int) *PerformanceMonitor {
	if streamCount <= 0 {
		streamCount = 1
	}

	return &PerformanceMonitor{
		startTime:   time.Now(),
		streamCount: streamCount,
		stopChan:    make(chan struct{}),
		sysMonitor:  NewSystemMonitor(), // Create real system monitor
		metrics: PerformanceMetrics{
			CPUThreshold:    3.0 * float64(streamCount),  // 3% per stream
			MemoryThreshold: 100 * uint64(streamCount),   // 100MB per stream
		},
	}
}

// Start begins monitoring performance
func (pm *PerformanceMonitor) Start() {
	go pm.monitorLoop()
}

// Stop stops the performance monitor
func (pm *PerformanceMonitor) Stop() {
	close(pm.stopChan)
}

// monitorLoop continuously monitors performance metrics
func (pm *PerformanceMonitor) monitorLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pm.stopChan:
			return
		case <-ticker.C:
			pm.updateMetrics()
		}
	}
}

// updateMetrics updates current performance metrics
func (pm *PerformanceMonitor) updateMetrics() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Get real system stats from the system monitor
	sysStats := pm.sysMonitor.GetSystemStats()

	// Update metrics with real data
	pm.metrics = PerformanceMetrics{
		CPUPercent:      sysStats.CPUPercent,
		MemoryMB:        sysStats.MemoryMB,
		GoroutineCount:  sysStats.GoroutineCount,
		LastUpdated:     sysStats.Timestamp,
		CPUThreshold:    3.0 * float64(pm.streamCount),
		MemoryThreshold: 100 * uint64(pm.streamCount),
	}
}


// GetMetrics returns current performance metrics
func (pm *PerformanceMonitor) GetMetrics() PerformanceMetrics {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.metrics
}

// IsWithinThresholds checks if performance is within acceptable limits
func (pm *PerformanceMonitor) IsWithinThresholds() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	return pm.metrics.CPUPercent <= pm.metrics.CPUThreshold &&
		pm.metrics.MemoryMB <= pm.metrics.MemoryThreshold
}

// GetViolations returns any threshold violations
func (pm *PerformanceMonitor) GetViolations() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var violations []string

	if pm.metrics.CPUPercent > pm.metrics.CPUThreshold {
		violations = append(violations,
			fmt.Sprintf("CPU usage %.2f%% exceeds threshold %.2f%%",
				pm.metrics.CPUPercent, pm.metrics.CPUThreshold))
	}

	if pm.metrics.MemoryMB > pm.metrics.MemoryThreshold {
		violations = append(violations,
			fmt.Sprintf("Memory usage %dMB exceeds threshold %dMB",
				pm.metrics.MemoryMB, pm.metrics.MemoryThreshold))
	}

	return violations
}