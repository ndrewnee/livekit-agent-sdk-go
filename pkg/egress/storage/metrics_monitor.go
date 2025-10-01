package storage

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/livekit/protocol/logger"
)

// MetricsMonitor collects and monitors storage metrics
// Implements metrics and monitoring requirements from PLAN.md Milestone 3
type MetricsMonitor struct {
	logger logger.Logger

	// Collectors
	storageCollector   *StorageMetricsCollector
	uploadCollector    *UploadMetricsCollector
	playbackCollector  *PlaybackMetricsCollector
	performanceCollector *PerformanceMetricsCollector

	// Aggregated metrics
	mu              sync.RWMutex
	currentMetrics  *AggregatedMetrics
	historicalData  []HistoricalMetric

	// Alerting
	alertThresholds AlertThresholds
	alertHandlers   []AlertHandler

	// State
	running  bool
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// StorageMetricsCollector collects storage-specific metrics
type StorageMetricsCollector struct {
	TotalSegments      int64
	TotalPlaylists     int64
	TotalScreenshots   int64
	TotalBytes         int64
	LocalBytes         int64
	CloudBytes         int64
	UploadedBytes      int64
	DownloadedBytes    int64
	DeletedBytes       int64
	AverageSegmentSize int64
	LargestSegment     int64
	SmallestSegment    int64
}

// UploadMetricsCollector collects upload-specific metrics
type UploadMetricsCollector struct {
	TotalUploads       int64
	SuccessfulUploads  int64
	FailedUploads      int64
	RetryCount         int64
	AverageUploadTime  time.Duration
	MaxUploadTime      time.Duration
	MinUploadTime      time.Duration
	CurrentQueueDepth  int64
	MaxQueueDepth      int64
	BytesPerSecond     float64
	CircuitBreakerTrips int64
}

// PlaybackMetricsCollector collects playback-related metrics
type PlaybackMetricsCollector struct {
	TotalSessions       int64
	ActiveSessions      int64
	CompletedSessions   int64
	AverageSessionTime  time.Duration
	TotalPlaybackTime   time.Duration
	SegmentServedCount  int64
	PlaylistServedCount int64
	ValidationFailures  int64
	PlaybackErrors      int64
}

// PerformanceMetricsCollector collects performance metrics
type PerformanceMetricsCollector struct {
	CPUUsage           float64
	MemoryUsage        int64
	DiskIOReadBytes    int64
	DiskIOWriteBytes   int64
	NetworkInBytes     int64
	NetworkOutBytes    int64
	GoroutineCount     int
	OpenFileDescriptors int
	APILatencyP50      time.Duration
	APILatencyP95      time.Duration
	APILatencyP99      time.Duration
}

// AggregatedMetrics holds all aggregated metrics
type AggregatedMetrics struct {
	Timestamp    time.Time                    `json:"timestamp"`
	Storage      StorageMetricsCollector      `json:"storage"`
	Upload       UploadMetricsCollector       `json:"upload"`
	Playback     PlaybackMetricsCollector     `json:"playback"`
	Performance  PerformanceMetricsCollector `json:"performance"`
	Health       HealthMetrics                `json:"health"`
	Alerts       []Alert                      `json:"alerts,omitempty"`
}

// HealthMetrics represents overall system health
type HealthMetrics struct {
	Status           HealthStatus `json:"status"`
	Score            int          `json:"score"` // 0-100
	Issues           []string     `json:"issues,omitempty"`
	LastHealthCheck  time.Time    `json:"last_health_check"`
	UptimeSeconds    int64        `json:"uptime_seconds"`
	ErrorRate        float64      `json:"error_rate"`
	SuccessRate      float64      `json:"success_rate"`
}

// HealthStatus represents health status levels
type HealthStatus string

const (
	HealthStatusHealthy  HealthStatus = "healthy"
	HealthStatusDegraded HealthStatus = "degraded"
	HealthStatusCritical HealthStatus = "critical"
)

// HistoricalMetric represents a historical metric point
type HistoricalMetric struct {
	Timestamp time.Time
	Metrics   *AggregatedMetrics
}

// Alert represents a metric alert
type Alert struct {
	ID          string       `json:"id"`
	Level       AlertLevel   `json:"level"`
	Type        string       `json:"type"`
	Message     string       `json:"message"`
	Value       interface{}  `json:"value"`
	Threshold   interface{}  `json:"threshold"`
	Timestamp   time.Time    `json:"timestamp"`
	Resolved    bool         `json:"resolved"`
	ResolvedAt  *time.Time   `json:"resolved_at,omitempty"`
}

// AlertLevel represents alert severity levels
type AlertLevel string

const (
	AlertLevelInfo     AlertLevel = "info"
	AlertLevelWarning  AlertLevel = "warning"
	AlertLevelError    AlertLevel = "error"
	AlertLevelCritical AlertLevel = "critical"
)

// AlertThresholds defines thresholds for alerts
type AlertThresholds struct {
	MaxUploadQueueDepth    int64
	MaxFailureRate         float64
	MinSuccessRate         float64
	MaxUploadTime          time.Duration
	MaxMemoryUsage         int64
	MaxCPUUsage            float64
	MinHealthScore         int
	MaxCircuitBreakerTrips int64
}

// AlertHandler handles metric alerts
type AlertHandler func(alert Alert)

// NewMetricsMonitor creates a new metrics monitor
func NewMetricsMonitor(logger logger.Logger) *MetricsMonitor {
	return &MetricsMonitor{
		logger:              logger,
		storageCollector:    &StorageMetricsCollector{},
		uploadCollector:     &UploadMetricsCollector{},
		playbackCollector:   &PlaybackMetricsCollector{},
		performanceCollector: &PerformanceMetricsCollector{},
		currentMetrics:      &AggregatedMetrics{},
		historicalData:      make([]HistoricalMetric, 0, 1440), // 24 hours of minute data
		alertThresholds:     GetDefaultAlertThresholds(),
		alertHandlers:       []AlertHandler{},
		stopChan:            make(chan struct{}),
	}
}

// Start starts the metrics monitor
func (mm *MetricsMonitor) Start() error {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	if mm.running {
		return fmt.Errorf("metrics monitor already running")
	}

	mm.running = true

	// Start collectors
	mm.wg.Add(1)
	go mm.collectorWorker()

	// Start aggregator
	mm.wg.Add(1)
	go mm.aggregatorWorker()

	// Start health checker
	mm.wg.Add(1)
	go mm.healthCheckWorker()

	mm.logger.Infow("metrics monitor started")

	return nil
}

// Stop stops the metrics monitor
func (mm *MetricsMonitor) Stop() error {
	mm.mu.Lock()
	if !mm.running {
		mm.mu.Unlock()
		return nil
	}

	mm.running = false
	close(mm.stopChan)
	mm.mu.Unlock()

	// Wait for workers
	done := make(chan struct{})
	go func() {
		mm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		mm.logger.Infow("metrics monitor stopped gracefully")
	case <-time.After(10 * time.Second):
		mm.logger.Warnw("metrics monitor stop timeout", nil)
	}

	return nil
}

// collectorWorker collects metrics periodically
func (mm *MetricsMonitor) collectorWorker() {
	defer mm.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-mm.stopChan:
			return

		case <-ticker.C:
			mm.collectMetrics()
		}
	}
}

// collectMetrics collects all metrics
func (mm *MetricsMonitor) collectMetrics() {
	// This would collect actual metrics from the system
	// For now, we'll simulate collection

	mm.mu.Lock()
	defer mm.mu.Unlock()

	// Update performance metrics
	mm.performanceCollector.CPUUsage = mm.getCPUUsage()
	mm.performanceCollector.MemoryUsage = mm.getMemoryUsage()
	mm.performanceCollector.GoroutineCount = mm.getGoroutineCount()

	// Calculate derived metrics
	mm.calculateDerivedMetrics()

	// Check for alerts
	mm.checkAlerts()
}

// aggregatorWorker aggregates metrics periodically
func (mm *MetricsMonitor) aggregatorWorker() {
	defer mm.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-mm.stopChan:
			return

		case <-ticker.C:
			mm.aggregateMetrics()
		}
	}
}

// aggregateMetrics aggregates current metrics
func (mm *MetricsMonitor) aggregateMetrics() {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	// Create snapshot
	snapshot := &AggregatedMetrics{
		Timestamp:   time.Now(),
		Storage:     *mm.storageCollector,
		Upload:      *mm.uploadCollector,
		Playback:    *mm.playbackCollector,
		Performance: *mm.performanceCollector,
		Health:      mm.calculateHealth(),
	}

	// Store current metrics
	mm.currentMetrics = snapshot

	// Add to historical data
	mm.historicalData = append(mm.historicalData, HistoricalMetric{
		Timestamp: snapshot.Timestamp,
		Metrics:   snapshot,
	})

	// Trim old historical data (keep 24 hours)
	cutoff := time.Now().Add(-24 * time.Hour)
	for len(mm.historicalData) > 0 && mm.historicalData[0].Timestamp.Before(cutoff) {
		mm.historicalData = mm.historicalData[1:]
	}

	mm.logger.Debugw("metrics aggregated",
		"health_score", snapshot.Health.Score,
		"active_sessions", snapshot.Playback.ActiveSessions)
}

// healthCheckWorker performs health checks
func (mm *MetricsMonitor) healthCheckWorker() {
	defer mm.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-mm.stopChan:
			return

		case <-ticker.C:
			mm.performHealthCheck()
		}
	}
}

// performHealthCheck performs a health check
func (mm *MetricsMonitor) performHealthCheck() {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	health := mm.calculateHealth()
	mm.currentMetrics.Health = health

	if health.Status == HealthStatusCritical {
		mm.logger.Errorw("system health critical", nil,
			"score", health.Score,
			"issues", health.Issues)
		mm.createAlert(AlertLevelCritical, "health", "System health critical", health.Score, 50)
	} else if health.Status == HealthStatusDegraded {
		mm.logger.Warnw("system health degraded", nil,
			"score", health.Score,
			"issues", health.Issues)
		mm.createAlert(AlertLevelWarning, "health", "System health degraded", health.Score, 70)
	}
}

// calculateHealth calculates overall health metrics
func (mm *MetricsMonitor) calculateHealth() HealthMetrics {
	health := HealthMetrics{
		LastHealthCheck: time.Now(),
		UptimeSeconds:   int64(time.Since(mm.currentMetrics.Timestamp).Seconds()),
		Issues:          []string{},
		Score:           100,
	}

	// Calculate success rate
	totalUploads := mm.uploadCollector.SuccessfulUploads + mm.uploadCollector.FailedUploads
	if totalUploads > 0 {
		health.SuccessRate = float64(mm.uploadCollector.SuccessfulUploads) / float64(totalUploads)
		health.ErrorRate = 1.0 - health.SuccessRate
	} else {
		health.SuccessRate = 1.0
		health.ErrorRate = 0.0
	}

	// Deduct points for various issues
	if health.ErrorRate > 0.1 {
		health.Score -= 20
		health.Issues = append(health.Issues, fmt.Sprintf("High error rate: %.1f%%", health.ErrorRate*100))
	}

	if mm.uploadCollector.CurrentQueueDepth > mm.alertThresholds.MaxUploadQueueDepth {
		health.Score -= 15
		health.Issues = append(health.Issues, fmt.Sprintf("Upload queue depth high: %d", mm.uploadCollector.CurrentQueueDepth))
	}

	if mm.performanceCollector.CPUUsage > mm.alertThresholds.MaxCPUUsage {
		health.Score -= 10
		health.Issues = append(health.Issues, fmt.Sprintf("High CPU usage: %.1f%%", mm.performanceCollector.CPUUsage*100))
	}

	if mm.performanceCollector.MemoryUsage > mm.alertThresholds.MaxMemoryUsage {
		health.Score -= 10
		health.Issues = append(health.Issues, fmt.Sprintf("High memory usage: %d MB", mm.performanceCollector.MemoryUsage/1024/1024))
	}

	if mm.uploadCollector.CircuitBreakerTrips > mm.alertThresholds.MaxCircuitBreakerTrips {
		health.Score -= 25
		health.Issues = append(health.Issues, fmt.Sprintf("Circuit breaker trips: %d", mm.uploadCollector.CircuitBreakerTrips))
	}

	// Determine status based on score
	if health.Score >= 80 {
		health.Status = HealthStatusHealthy
	} else if health.Score >= 50 {
		health.Status = HealthStatusDegraded
	} else {
		health.Status = HealthStatusCritical
	}

	return health
}

// calculateDerivedMetrics calculates derived metrics
func (mm *MetricsMonitor) calculateDerivedMetrics() {
	// Calculate average segment size
	if mm.storageCollector.TotalSegments > 0 {
		mm.storageCollector.AverageSegmentSize = mm.storageCollector.TotalBytes / mm.storageCollector.TotalSegments
	}

	// Calculate upload bytes per second
	if mm.uploadCollector.AverageUploadTime > 0 && mm.storageCollector.UploadedBytes > 0 {
		seconds := mm.uploadCollector.AverageUploadTime.Seconds()
		mm.uploadCollector.BytesPerSecond = float64(mm.storageCollector.UploadedBytes) / seconds
	}

	// Calculate average session time
	if mm.playbackCollector.CompletedSessions > 0 {
		mm.playbackCollector.AverageSessionTime = mm.playbackCollector.TotalPlaybackTime / time.Duration(mm.playbackCollector.CompletedSessions)
	}
}

// checkAlerts checks for metric alerts
func (mm *MetricsMonitor) checkAlerts() {
	// Check upload queue depth
	if mm.uploadCollector.CurrentQueueDepth > mm.alertThresholds.MaxUploadQueueDepth {
		mm.createAlert(AlertLevelWarning, "upload_queue", "Upload queue depth exceeded",
			mm.uploadCollector.CurrentQueueDepth, mm.alertThresholds.MaxUploadQueueDepth)
	}

	// Check failure rate
	failureRate := float64(mm.uploadCollector.FailedUploads) / float64(mm.uploadCollector.TotalUploads+1)
	if failureRate > mm.alertThresholds.MaxFailureRate {
		mm.createAlert(AlertLevelError, "failure_rate", "High failure rate",
			failureRate, mm.alertThresholds.MaxFailureRate)
	}

	// Check upload time
	if mm.uploadCollector.MaxUploadTime > mm.alertThresholds.MaxUploadTime {
		mm.createAlert(AlertLevelWarning, "upload_time", "Upload time exceeded",
			mm.uploadCollector.MaxUploadTime, mm.alertThresholds.MaxUploadTime)
	}

	// Check memory usage
	if mm.performanceCollector.MemoryUsage > mm.alertThresholds.MaxMemoryUsage {
		mm.createAlert(AlertLevelWarning, "memory", "High memory usage",
			mm.performanceCollector.MemoryUsage, mm.alertThresholds.MaxMemoryUsage)
	}

	// Check CPU usage
	if mm.performanceCollector.CPUUsage > mm.alertThresholds.MaxCPUUsage {
		mm.createAlert(AlertLevelWarning, "cpu", "High CPU usage",
			mm.performanceCollector.CPUUsage, mm.alertThresholds.MaxCPUUsage)
	}
}

// createAlert creates a new alert
func (mm *MetricsMonitor) createAlert(level AlertLevel, alertType, message string, value, threshold interface{}) {
	alert := Alert{
		ID:        fmt.Sprintf("%s-%d", alertType, time.Now().Unix()),
		Level:     level,
		Type:      alertType,
		Message:   message,
		Value:     value,
		Threshold: threshold,
		Timestamp: time.Now(),
		Resolved:  false,
	}

	// Add to current metrics
	if mm.currentMetrics.Alerts == nil {
		mm.currentMetrics.Alerts = []Alert{}
	}
	mm.currentMetrics.Alerts = append(mm.currentMetrics.Alerts, alert)

	// Notify handlers
	for _, handler := range mm.alertHandlers {
		go handler(alert)
	}

	mm.logger.Warnw("alert created", nil,
		"level", level,
		"type", alertType,
		"message", message,
		"value", value,
		"threshold", threshold)
}

// RegisterAlertHandler registers an alert handler
func (mm *MetricsMonitor) RegisterAlertHandler(handler AlertHandler) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.alertHandlers = append(mm.alertHandlers, handler)
}

// GetCurrentMetrics returns current metrics
func (mm *MetricsMonitor) GetCurrentMetrics() *AggregatedMetrics {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.currentMetrics
}

// GetHistoricalMetrics returns historical metrics
func (mm *MetricsMonitor) GetHistoricalMetrics(duration time.Duration) []HistoricalMetric {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	cutoff := time.Now().Add(-duration)
	var result []HistoricalMetric

	for _, metric := range mm.historicalData {
		if metric.Timestamp.After(cutoff) {
			result = append(result, metric)
		}
	}

	return result
}

// ExportMetrics exports metrics in JSON format
func (mm *MetricsMonitor) ExportMetrics() ([]byte, error) {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	return json.MarshalIndent(mm.currentMetrics, "", "  ")
}

// Metric update methods

// RecordSegmentStored records a segment storage event
func (mm *MetricsMonitor) RecordSegmentStored(size int64, local bool) {
	atomic.AddInt64(&mm.storageCollector.TotalSegments, 1)
	atomic.AddInt64(&mm.storageCollector.TotalBytes, size)

	if local {
		atomic.AddInt64(&mm.storageCollector.LocalBytes, size)
	} else {
		atomic.AddInt64(&mm.storageCollector.CloudBytes, size)
	}

	// Update largest/smallest
	for {
		current := atomic.LoadInt64(&mm.storageCollector.LargestSegment)
		if size <= current || atomic.CompareAndSwapInt64(&mm.storageCollector.LargestSegment, current, size) {
			break
		}
	}

	if mm.storageCollector.SmallestSegment == 0 || size < mm.storageCollector.SmallestSegment {
		atomic.StoreInt64(&mm.storageCollector.SmallestSegment, size)
	}
}

// RecordUpload records an upload event
func (mm *MetricsMonitor) RecordUpload(success bool, duration time.Duration, size int64) {
	atomic.AddInt64(&mm.uploadCollector.TotalUploads, 1)

	if success {
		atomic.AddInt64(&mm.uploadCollector.SuccessfulUploads, 1)
		atomic.AddInt64(&mm.storageCollector.UploadedBytes, size)
	} else {
		atomic.AddInt64(&mm.uploadCollector.FailedUploads, 1)
	}

	// Update timing metrics
	mm.mu.Lock()
	if mm.uploadCollector.AverageUploadTime == 0 {
		mm.uploadCollector.AverageUploadTime = duration
	} else {
		// Simple moving average
		mm.uploadCollector.AverageUploadTime = (mm.uploadCollector.AverageUploadTime + duration) / 2
	}

	if duration > mm.uploadCollector.MaxUploadTime {
		mm.uploadCollector.MaxUploadTime = duration
	}

	if mm.uploadCollector.MinUploadTime == 0 || duration < mm.uploadCollector.MinUploadTime {
		mm.uploadCollector.MinUploadTime = duration
	}
	mm.mu.Unlock()
}

// RecordPlaybackSession records a playback session
func (mm *MetricsMonitor) RecordPlaybackSession(started bool, duration time.Duration) {
	if started {
		atomic.AddInt64(&mm.playbackCollector.TotalSessions, 1)
		atomic.AddInt64(&mm.playbackCollector.ActiveSessions, 1)
	} else {
		atomic.AddInt64(&mm.playbackCollector.ActiveSessions, -1)
		atomic.AddInt64(&mm.playbackCollector.CompletedSessions, 1)

		mm.mu.Lock()
		mm.playbackCollector.TotalPlaybackTime += duration
		mm.mu.Unlock()
	}
}

// Helper methods for real metrics

var (
	lastCPUUsage     syscall.Rusage
	lastCPUSampleTime time.Time
	cpuMutex         sync.Mutex
)

func (mm *MetricsMonitor) getCPUUsage() float64 {
	cpuMutex.Lock()
	defer cpuMutex.Unlock()

	var usage syscall.Rusage
	err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	if err != nil {
		mm.logger.Errorw("failed to get CPU usage", err)
		return 0.0
	}

	now := time.Now()
	if lastCPUSampleTime.IsZero() {
		lastCPUUsage = usage
		lastCPUSampleTime = now
		return 0.0
	}

	// Calculate CPU usage percentage
	timeDelta := now.Sub(lastCPUSampleTime).Seconds()
	if timeDelta == 0 {
		return 0.0
	}

	userTime := float64(usage.Utime.Sec - lastCPUUsage.Utime.Sec) + float64(usage.Utime.Usec - lastCPUUsage.Utime.Usec)/1e6
	sysTime := float64(usage.Stime.Sec - lastCPUUsage.Stime.Sec) + float64(usage.Stime.Usec - lastCPUUsage.Stime.Usec)/1e6
	totalCPUTime := userTime + sysTime

	// Get number of CPUs
	numCPU := runtime.NumCPU()

	// Calculate CPU percentage (0.0 to 1.0)
	cpuUsage := totalCPUTime / (timeDelta * float64(numCPU))

	lastCPUUsage = usage
	lastCPUSampleTime = now

	return cpuUsage
}

func (mm *MetricsMonitor) getMemoryUsage() int64 {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Return total allocated memory (heap + stack)
	return int64(memStats.Alloc)
}

func (mm *MetricsMonitor) getGoroutineCount() int {
	return runtime.NumGoroutine()
}

// GetDefaultAlertThresholds returns default alert thresholds
func GetDefaultAlertThresholds() AlertThresholds {
	return AlertThresholds{
		MaxUploadQueueDepth:    100,
		MaxFailureRate:         0.1,  // 10%
		MinSuccessRate:         0.9,  // 90%
		MaxUploadTime:          30 * time.Second,
		MaxMemoryUsage:         1024 * 1024 * 1024, // 1GB
		MaxCPUUsage:            0.8,                // 80%
		MinHealthScore:         70,
		MaxCircuitBreakerTrips: 5,
	}
}