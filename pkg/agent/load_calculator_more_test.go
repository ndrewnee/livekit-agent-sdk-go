package agent

import (
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestCPUMemoryLoadCalculator_CalculateBranches(t *testing.T) {
	calc := NewCPUMemoryLoadCalculator()
	// With MaxJobs
	m := LoadMetrics{ActiveJobs: 3, MaxJobs: 6, CPUPercent: 50, MemoryPercent: 40}
	v1 := calc.Calculate(m)
	assert.Greater(t, v1, float32(0))

	// Without MaxJobs (0) and different CPU/Memory
	m = LoadMetrics{ActiveJobs: 2, MaxJobs: 0, CPUPercent: 10, MemoryPercent: 10}
	v2 := calc.Calculate(m)
	assert.GreaterOrEqual(t, v2, float32(0))

	// Clamp > 1 case
	m = LoadMetrics{ActiveJobs: 10, MaxJobs: 10, CPUPercent: 100, MemoryPercent: 100}
	v3 := calc.Calculate(m)
	if v3 > 1 {
		t.Fatalf("expected clamped <=1, got %f", v3)
	}

	// Clamp < 0 case by using negative weight
	calcNeg := &CPUMemoryLoadCalculator{JobWeight: 0, CPUWeight: -1, MemoryWeight: 0}
	m = LoadMetrics{ActiveJobs: 0, MaxJobs: 0, CPUPercent: 10, MemoryPercent: 0}
	v4 := calcNeg.Calculate(m)
	if v4 < 0 {
		t.Fatalf("expected clamped >=0, got %f", v4)
	}
}

func TestPredictiveLoadCalculator_Trend(t *testing.T) {
	p := NewPredictiveLoadCalculator(&DefaultLoadCalculator{}, 5)
	// Increasing job count over multiple calls to generate positive trend
	for i := 1; i <= 5; i++ {
		_ = p.Calculate(LoadMetrics{ActiveJobs: i, MaxJobs: 10})
		time.Sleep(1 * time.Millisecond)
	}
	// Decreasing to exercise another slope
	for i := 5; i >= 1; i-- {
		_ = p.Calculate(LoadMetrics{ActiveJobs: i, MaxJobs: 10})
	}
}
