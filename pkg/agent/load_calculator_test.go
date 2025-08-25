package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDefaultLoadCalculator tests the default load calculation
func TestDefaultLoadCalculator(t *testing.T) {
	calc := &DefaultLoadCalculator{}

	tests := []struct {
		name     string
		metrics  LoadMetrics
		expected float32
	}{
		{
			name: "with max jobs",
			metrics: LoadMetrics{
				ActiveJobs: 5,
				MaxJobs:    10,
			},
			expected: 0.5,
		},
		{
			name: "unlimited jobs low count",
			metrics: LoadMetrics{
				ActiveJobs: 5,
				MaxJobs:    0,
			},
			expected: 0.5,
		},
		{
			name: "unlimited jobs high count",
			metrics: LoadMetrics{
				ActiveJobs: 15,
				MaxJobs:    0,
			},
			expected: 1.0,
		},
		{
			name: "no active jobs",
			metrics: LoadMetrics{
				ActiveJobs: 0,
				MaxJobs:    10,
			},
			expected: 0.0,
		},
		{
			name: "at capacity",
			metrics: LoadMetrics{
				ActiveJobs: 10,
				MaxJobs:    10,
			},
			expected: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			load := calc.Calculate(tt.metrics)
			assert.Equal(t, tt.expected, load)
		})
	}
}

// TestCPUMemoryLoadCalculator tests CPU and memory based load calculation
func TestCPUMemoryLoadCalculator(t *testing.T) {
	calc := NewCPUMemoryLoadCalculator()

	tests := []struct {
		name     string
		metrics  LoadMetrics
		expected float32
		delta    float32
	}{
		{
			name: "balanced load",
			metrics: LoadMetrics{
				ActiveJobs:    5,
				MaxJobs:       10,
				CPUPercent:    50.0,
				MemoryPercent: 60.0,
			},
			expected: 0.53, // 0.5*0.4 + 0.5*0.3 + 0.6*0.3
			delta:    0.01,
		},
		{
			name: "high CPU load",
			metrics: LoadMetrics{
				ActiveJobs:    2,
				MaxJobs:       10,
				CPUPercent:    90.0,
				MemoryPercent: 30.0,
			},
			expected: 0.44, // 0.2*0.4 + 0.9*0.3 + 0.3*0.3
			delta:    0.01,
		},
		{
			name: "high memory load",
			metrics: LoadMetrics{
				ActiveJobs:    3,
				MaxJobs:       10,
				CPUPercent:    20.0,
				MemoryPercent: 95.0,
			},
			expected: 0.465, // 0.3*0.4 + 0.2*0.3 + 0.95*0.3
			delta:    0.01,
		},
		{
			name: "unlimited jobs",
			metrics: LoadMetrics{
				ActiveJobs:    8,
				MaxJobs:       0,
				CPUPercent:    40.0,
				MemoryPercent: 50.0,
			},
			expected: 0.59, // 0.8*0.4 + 0.4*0.3 + 0.5*0.3
			delta:    0.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			load := calc.Calculate(tt.metrics)
			assert.InDelta(t, tt.expected, load, float64(tt.delta))
		})
	}
}

// TestPredictiveLoadCalculator tests the predictive load calculation
func TestPredictiveLoadCalculator(t *testing.T) {
	baseCalc := &DefaultLoadCalculator{}
	calc := NewPredictiveLoadCalculator(baseCalc, 5)

	// Test with no history
	metrics := LoadMetrics{
		ActiveJobs: 5,
		MaxJobs:    10,
	}
	load := calc.Calculate(metrics)
	assert.Equal(t, float32(0.5), load)

	// Add some history with increasing load
	for i := 1; i <= 3; i++ {
		metrics.ActiveJobs = 5 + i
		calc.Calculate(metrics)
		time.Sleep(10 * time.Millisecond)
	}

	// Next calculation should predict higher load
	metrics.ActiveJobs = 8
	predictedLoad := calc.Calculate(metrics)
	assert.Greater(t, predictedLoad, float32(0.8))
	assert.LessOrEqual(t, predictedLoad, float32(1.0))

	// Test with decreasing trend
	calc = NewPredictiveLoadCalculator(baseCalc, 5)
	for i := 10; i >= 5; i-- {
		metrics.ActiveJobs = i
		calc.Calculate(metrics)
		time.Sleep(10 * time.Millisecond)
	}

	metrics.ActiveJobs = 5
	predictedLoad = calc.Calculate(metrics)
	assert.Less(t, predictedLoad, float32(0.5))
	assert.GreaterOrEqual(t, predictedLoad, float32(0.0))
}

// TestLoadBatcher tests the load update batching
func TestLoadBatcher(t *testing.T) {
	// Test the batching behavior without relying on actual network send
	worker := newMockWorker(nil)

	batcher := NewLoadBatcher(worker, 100*time.Millisecond)
	batcher.Start()

	// Send multiple updates rapidly
	batcher.Update(WorkerStatusAvailable, 0.3)
	batcher.Update(WorkerStatusAvailable, 0.5)
	batcher.Update(WorkerStatusAvailable, 0.7)

	// Check that updates are queued but not sent immediately
	batcher.mu.Lock()
	pendingLoad := batcher.pendingLoad
	pendingStatus := batcher.pendingStatus
	batcher.mu.Unlock()

	// Should have pending updates
	assert.NotNil(t, pendingLoad)
	assert.NotNil(t, pendingStatus)
	assert.Equal(t, float32(0.7), *pendingLoad)
	assert.Equal(t, WorkerStatusAvailable, *pendingStatus)

	// Wait for batch interval
	time.Sleep(150 * time.Millisecond)

	// Check that pending updates were cleared (indicating flush was called)
	batcher.mu.Lock()
	pendingLoadAfter := batcher.pendingLoad
	pendingStatusAfter := batcher.pendingStatus
	batcher.mu.Unlock()

	// Should have been flushed
	assert.Nil(t, pendingLoadAfter)
	assert.Nil(t, pendingStatusAfter)

	// Stop the batcher
	batcher.Stop()
}

// TestSystemMetricsCollector tests system metrics collection
func TestSystemMetricsCollector(t *testing.T) {
	collector := NewSystemMetricsCollector()
	defer collector.Stop() // Ensure cleanup

	// Give it time to collect initial metrics
	time.Sleep(1100 * time.Millisecond)

	cpu, mem, usedMB, totalMB := collector.GetMetrics()

	// Basic sanity checks
	assert.GreaterOrEqual(t, cpu, float64(0))
	assert.LessOrEqual(t, cpu, float64(100))
	assert.GreaterOrEqual(t, mem, float64(0))
	assert.LessOrEqual(t, mem, float64(100))
	assert.Greater(t, usedMB, uint64(0))
	assert.Greater(t, totalMB, uint64(0))
	assert.LessOrEqual(t, usedMB, totalMB)
}

// Worker-specific tests removed - use UniversalWorker tests instead

// mockLoadCalculator for testing
type mockLoadCalculator struct {
	callCount  int
	returnLoad float32
}

func (m *mockLoadCalculator) Calculate(metrics LoadMetrics) float32 {
	m.callCount++
	return m.returnLoad
}
