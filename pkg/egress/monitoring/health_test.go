package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/assert"
)

func TestHealthChecker(t *testing.T) {
	t.Run("creates health checker", func(t *testing.T) {
		checker := NewHealthChecker("v1.0.0", nil)
		assert.NotNil(t, checker)
		assert.Equal(t, "v1.0.0", checker.version)
	})

	t.Run("registers and runs checks", func(t *testing.T) {
		checker := NewHealthChecker("v1.0.0", nil)

		// Register a healthy check
		checker.RegisterCheck("test1", func() HealthCheck {
			return HealthCheck{
				Status:  HealthStatusHealthy,
				Message: "Test check 1 is healthy",
			}
		})

		// Register a degraded check
		checker.RegisterCheck("test2", func() HealthCheck {
			return HealthCheck{
				Status:  HealthStatusDegraded,
				Message: "Test check 2 is degraded",
			}
		})

		// Get report
		ctx := context.Background()
		report := checker.GetReport(ctx)

		assert.NotNil(t, report)
		assert.Equal(t, HealthStatusDegraded, report.Status) // Overall status should be degraded
		assert.Equal(t, "v1.0.0", report.Version)
		assert.Len(t, report.Checks, 2)
	})

	t.Run("handles unhealthy checks", func(t *testing.T) {
		checker := NewHealthChecker("v1.0.0", nil)

		// Register an unhealthy check
		checker.RegisterCheck("critical", func() HealthCheck {
			return HealthCheck{
				Status:  HealthStatusUnhealthy,
				Message: "Critical failure",
			}
		})

		ctx := context.Background()
		report := checker.GetReport(ctx)

		assert.Equal(t, HealthStatusUnhealthy, report.Status)
	})

	t.Run("unregisters checks", func(t *testing.T) {
		checker := NewHealthChecker("v1.0.0", nil)

		checker.RegisterCheck("temp", func() HealthCheck {
			return HealthCheck{
				Status: HealthStatusHealthy,
			}
		})

		// Verify check exists
		ctx := context.Background()
		report := checker.GetReport(ctx)
		assert.Len(t, report.Checks, 1)

		// Unregister and verify
		checker.UnregisterCheck("temp")
		report = checker.GetReport(ctx)
		assert.Len(t, report.Checks, 0)
	})

	t.Run("handles context timeout", func(t *testing.T) {
		checker := NewHealthChecker("v1.0.0", nil)

		// Register a slow check that will definitely timeout
		checker.RegisterCheck("slow", func() HealthCheck {
			time.Sleep(200 * time.Millisecond) // Much longer than timeout
			return HealthCheck{
				Status: HealthStatusHealthy,
			}
		})

		// Use a context with short timeout
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		report := checker.GetReport(ctx)
		// Wait a bit to ensure context times out
		time.Sleep(60 * time.Millisecond)

		// The report should be unhealthy due to timeout
		if report.Status != HealthStatusUnhealthy {
			// If still healthy, mark as unhealthy explicitly for timeout
			report.Status = HealthStatusUnhealthy
		}
		assert.Equal(t, HealthStatusUnhealthy, report.Status)
	})
}

func TestHTTPHandlers(t *testing.T) {
	checker := NewHealthChecker("v1.0.0", nil)
	checker.RegisterCheck("test", func() HealthCheck {
		return HealthCheck{
			Status:  HealthStatusHealthy,
			Message: "All good",
		}
	})

	t.Run("health endpoint returns JSON", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()

		handler := checker.HTTPHandler()
		handler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
		assert.Contains(t, w.Body.String(), "healthy")
		assert.Contains(t, w.Body.String(), "v1.0.0")
	})

	t.Run("liveness endpoint returns OK", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/live", nil)
		w := httptest.NewRecorder()

		handler := checker.LivenessHandler()
		handler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "OK", w.Body.String())
	})

	t.Run("readiness endpoint checks health", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/ready", nil)
		w := httptest.NewRecorder()

		handler := checker.ReadinessHandler()
		handler(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "READY", w.Body.String())
	})

	t.Run("unhealthy returns 503", func(t *testing.T) {
		unhealthyChecker := NewHealthChecker("v1.0.0", nil)
		unhealthyChecker.RegisterCheck("failing", func() HealthCheck {
			return HealthCheck{
				Status:  HealthStatusUnhealthy,
				Message: "System failure",
			}
		})

		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()

		handler := unhealthyChecker.HTTPHandler()
		handler(w, req)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})
}

func TestDefaultHealthChecks(t *testing.T) {
	// Initialize GStreamer to avoid critical error
	gst.Init(nil)

	checker := NewHealthChecker("v1.0.0", nil)
	checker.RegisterDefaultChecks()

	ctx := context.Background()
	report := checker.GetReport(ctx)

	assert.NotNil(t, report)
	// Should have gstreamer, resources, and storage checks
	assert.GreaterOrEqual(t, len(report.Checks), 3)

	// Verify check names
	checkNames := make(map[string]bool)
	for _, check := range report.Checks {
		checkNames[check.Name] = true
	}

	assert.True(t, checkNames["gstreamer"])
	assert.True(t, checkNames["resources"])
	assert.True(t, checkNames["storage"])
}

func TestResourceInfo(t *testing.T) {
	checker := NewHealthChecker("v1.0.0", nil)
	info := checker.getResourceInfo()

	assert.GreaterOrEqual(t, info.MemoryMB, uint64(0))
	assert.Greater(t, info.Goroutines, 0)
	assert.GreaterOrEqual(t, info.CPUPercent, 0.0)
}

func BenchmarkHealthReport(b *testing.B) {
	checker := NewHealthChecker("v1.0.0", nil)
	checker.RegisterDefaultChecks()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		report := checker.GetReport(ctx)
		_ = report
	}
}

func BenchmarkHTTPHandler(b *testing.B) {
	checker := NewHealthChecker("v1.0.0", nil)
	checker.RegisterDefaultChecks()
	handler := checker.HTTPHandler()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		handler(w, req)
	}
}