package egress

/*
#include <mach/mach.h>
#include <mach/task_info.h>
#include <mach/mach_init.h>
#include <sys/types.h>
#include <sys/sysctl.h>
#include <unistd.h>
#include <stdlib.h>
#include <time.h>

// Get CPU usage for current process
double get_cpu_usage() {
    struct task_basic_info info;
    mach_msg_type_number_t info_count = TASK_BASIC_INFO_COUNT;

    if (task_info(mach_task_self(), TASK_BASIC_INFO,
                  (task_info_t)&info, &info_count) != KERN_SUCCESS) {
        return -1.0;
    }

    // Get CPU ticks
    natural_t user_time = info.user_time.seconds * 1000000 + info.user_time.microseconds;
    natural_t system_time = info.system_time.seconds * 1000000 + info.system_time.microseconds;
    natural_t total_time = user_time + system_time;

    // Get number of CPUs
    int ncpu;
    size_t len = sizeof(ncpu);
    sysctlbyname("hw.ncpu", &ncpu, &len, NULL, 0);

    // Calculate REAL CPU percentage by tracking delta between measurements
    static natural_t last_total = 0;
    static double last_timestamp = 0;

    double current_timestamp = (double)clock() / CLOCKS_PER_SEC;
    double time_delta = current_timestamp - last_timestamp;

    if (time_delta > 0.01 && last_total > 0) { // Need at least 10ms delta
        natural_t cpu_delta = total_time - last_total;
        double cpu_percent = ((double)cpu_delta / (time_delta * 1000000.0)) * 100.0 / ncpu;

        last_total = total_time;
        last_timestamp = current_timestamp;

        // Return CPU percentage
        return cpu_percent;
    }

    last_total = total_time;
    last_timestamp = current_timestamp;

    // On first call, return 0 (no previous data to calculate from)
    return 0.0;
}

// Get memory usage in MB
uint64_t get_memory_usage_mb() {
    struct task_basic_info info;
    mach_msg_type_number_t info_count = TASK_BASIC_INFO_COUNT;

    if (task_info(mach_task_self(), TASK_BASIC_INFO,
                  (task_info_t)&info, &info_count) != KERN_SUCCESS) {
        return 0;
    }

    return info.resident_size / (1024 * 1024); // Convert to MB
}
*/
import "C"
import (
	"runtime"
	"sync"
	"time"
)

// SystemMonitor provides real system resource monitoring
type SystemMonitor struct {
	mu             sync.RWMutex
	lastCPUCheck   time.Time
	lastCPUPercent float64
	cpuSamples     []float64
	maxSamples     int
}

// NewSystemMonitor creates a real system monitor
func NewSystemMonitor() *SystemMonitor {
	return &SystemMonitor{
		maxSamples: 10, // Keep last 10 samples for averaging
		cpuSamples: make([]float64, 0, 10),
	}
}

// GetCPUUsage returns real CPU usage percentage
func (m *SystemMonitor) GetCPUUsage() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Get real CPU usage from C function
	cpuPercent := float64(C.get_cpu_usage())

	// If we got a valid reading, store it
	if cpuPercent >= 0 {
		m.cpuSamples = append(m.cpuSamples, cpuPercent)
		if len(m.cpuSamples) > m.maxSamples {
			m.cpuSamples = m.cpuSamples[1:]
		}
		m.lastCPUPercent = cpuPercent
		m.lastCPUCheck = time.Now()
	}

	// Return average of recent samples for stability
	if len(m.cpuSamples) > 0 {
		sum := 0.0
		for _, v := range m.cpuSamples {
			sum += v
		}
		return sum / float64(len(m.cpuSamples))
	}

	return m.lastCPUPercent
}

// GetMemoryUsageMB returns real memory usage in megabytes
func (m *SystemMonitor) GetMemoryUsageMB() uint64 {
	// Get real memory usage from C function
	memMB := uint64(C.get_memory_usage_mb())

	// Fallback to Go runtime if C function fails
	if memMB == 0 {
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		memMB = memStats.Alloc / (1024 * 1024)
	}

	return memMB
}

// GetGoroutineCount returns the current number of goroutines
func (m *SystemMonitor) GetGoroutineCount() int {
	return runtime.NumGoroutine()
}

// GetSystemStats returns comprehensive system statistics
func (m *SystemMonitor) GetSystemStats() SystemStats {
	return SystemStats{
		CPUPercent:     m.GetCPUUsage(),
		MemoryMB:       m.GetMemoryUsageMB(),
		GoroutineCount: m.GetGoroutineCount(),
		Timestamp:      time.Now(),
	}
}

// SystemStats holds system resource statistics
type SystemStats struct {
	CPUPercent     float64
	MemoryMB       uint64
	GoroutineCount int
	Timestamp      time.Time
}

// StartMonitoring starts continuous monitoring in background
func (m *SystemMonitor) StartMonitoring(interval time.Duration) chan SystemStats {
	statsChan := make(chan SystemStats, 10)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(statsChan)

		for {
			select {
			case <-ticker.C:
				stats := m.GetSystemStats()
				select {
				case statsChan <- stats:
				default:
					// Channel full, skip this sample
				}
			}
		}
	}()

	return statsChan
}