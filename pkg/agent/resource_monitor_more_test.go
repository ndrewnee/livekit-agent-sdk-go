package agent

import (
	"go.uber.org/zap"
	"testing"
)

func TestResourceMonitor_InternalPaths(t *testing.T) {
	m := NewResourceMonitor(zap.NewNop(), ResourceMonitorOptions{GoroutineLimit: 1, GoroutineLeakThreshold: 2})

	// Leak callback coverage
	leakCalled := 0
	m.SetLeakCallback(func(count int) { leakCalled++ })
	// Simulate increasing goroutines
	m.checkGoroutineLeaks(1)
	m.checkGoroutineLeaks(2)
	m.checkGoroutineLeaks(3)
	if leakCalled == 0 {
		t.Fatalf("expected leak callback")
	}
	// Resolve leak
	m.checkGoroutineLeaks(1)

	// Circular dependency detection
	cycleCalled := 0
	m.SetCircularDependencyCallback(func(deps []string) { cycleCalled++ })
	m.AddDependency("A", "B")
	m.AddDependency("B", "C")
	m.AddDependency("C", "A") // creates a cycle
	if cycleCalled == 0 {
		t.Fatalf("expected circular callback")
	}
}
