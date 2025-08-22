package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.uber.org/zap"
)

// TimingManager handles clock skew, deadline propagation, and backpressure
type TimingManager struct {
	mu               sync.RWMutex
	logger           *zap.Logger
	
	// Clock skew detection
	serverTimeOffset time.Duration
	skewSamples      []time.Duration
	maxSkewSamples   int
	skewThreshold    time.Duration
	
	// Deadline tracking
	deadlines        map[string]*DeadlineContext
	
	// Backpressure control
	backpressure     *BackpressureController
	
	// Metrics
	skewDetections   int64
	missedDeadlines  int64
	backpressureEvents int64
}

// DeadlineContext tracks deadline information for a job
type DeadlineContext struct {
	JobID            string
	OriginalDeadline time.Time
	AdjustedDeadline time.Time
	PropagatedFrom   string
	CreatedAt        time.Time
}

// TimingManagerOptions configures the timing manager
type TimingManagerOptions struct {
	MaxSkewSamples      int           // Max samples for skew calculation (default: 10)
	SkewThreshold       time.Duration // Threshold to trigger skew correction (default: 1s)
	BackpressureWindow  time.Duration // Window for rate calculation (default: 1s)
	BackpressureLimit   int           // Max events per window (default: 100)
}

// NewTimingManager creates a new timing manager
func NewTimingManager(logger *zap.Logger, opts TimingManagerOptions) *TimingManager {
	if opts.MaxSkewSamples == 0 {
		opts.MaxSkewSamples = 10
	}
	if opts.SkewThreshold == 0 {
		opts.SkewThreshold = 1 * time.Second
	}
	if opts.BackpressureWindow == 0 {
		opts.BackpressureWindow = 1 * time.Second
	}
	if opts.BackpressureLimit == 0 {
		opts.BackpressureLimit = 100
	}
	
	return &TimingManager{
		logger:         logger,
		maxSkewSamples: opts.MaxSkewSamples,
		skewThreshold:  opts.SkewThreshold,
		skewSamples:    make([]time.Duration, 0, opts.MaxSkewSamples),
		deadlines:      make(map[string]*DeadlineContext),
		backpressure:   NewBackpressureController(opts.BackpressureWindow, opts.BackpressureLimit),
	}
}

// UpdateServerTime updates the server time offset based on a timestamp from the server
func (tm *TimingManager) UpdateServerTime(serverTime time.Time, receivedAt time.Time) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	
	// Calculate the offset between server time and local time
	offset := serverTime.Sub(receivedAt)
	
	// Add to samples
	tm.skewSamples = append(tm.skewSamples, offset)
	if len(tm.skewSamples) > tm.maxSkewSamples {
		tm.skewSamples = tm.skewSamples[1:]
	}
	
	// Calculate average offset
	var totalOffset time.Duration
	for _, sample := range tm.skewSamples {
		totalOffset += sample
	}
	avgOffset := totalOffset / time.Duration(len(tm.skewSamples))
	
	// Check if skew is significant
	if abs(avgOffset) > tm.skewThreshold {
		oldOffset := tm.serverTimeOffset
		tm.serverTimeOffset = avgOffset
		
		atomic.AddInt64(&tm.skewDetections, 1)
		
		tm.logger.Warn("Clock skew detected",
			zap.Duration("offset", avgOffset),
			zap.Duration("threshold", tm.skewThreshold),
			zap.Int("samples", len(tm.skewSamples)),
			zap.Duration("previous_offset", oldOffset),
		)
		
		// Adjust all existing deadlines
		tm.adjustDeadlinesForSkew(avgOffset - oldOffset)
	}
}

// adjustDeadlinesForSkew adjusts all deadlines when clock skew is detected
func (tm *TimingManager) adjustDeadlinesForSkew(adjustment time.Duration) {
	for jobID, deadline := range tm.deadlines {
		deadline.AdjustedDeadline = deadline.AdjustedDeadline.Add(adjustment)
		tm.logger.Debug("Adjusted deadline for clock skew",
			zap.String("jobID", jobID),
			zap.Duration("adjustment", adjustment),
			zap.Time("new_deadline", deadline.AdjustedDeadline),
		)
	}
}

// ServerTimeNow returns the current time adjusted for server clock skew
func (tm *TimingManager) ServerTimeNow() time.Time {
	tm.mu.RLock()
	offset := tm.serverTimeOffset
	tm.mu.RUnlock()
	
	return time.Now().Add(offset)
}

// SetDeadline sets a deadline for a job with propagation support
func (tm *TimingManager) SetDeadline(jobID string, deadline time.Time, propagatedFrom string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	
	// Adjust deadline for clock skew
	adjustedDeadline := deadline.Add(-tm.serverTimeOffset)
	
	tm.deadlines[jobID] = &DeadlineContext{
		JobID:            jobID,
		OriginalDeadline: deadline,
		AdjustedDeadline: adjustedDeadline,
		PropagatedFrom:   propagatedFrom,
		CreatedAt:        time.Now(),
	}
	
	tm.logger.Debug("Set deadline",
		zap.String("jobID", jobID),
		zap.Time("original", deadline),
		zap.Time("adjusted", adjustedDeadline),
		zap.Duration("skew_offset", tm.serverTimeOffset),
		zap.String("propagated_from", propagatedFrom),
	)
}

// GetDeadline returns the deadline context for a job
func (tm *TimingManager) GetDeadline(jobID string) (*DeadlineContext, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	
	deadline, exists := tm.deadlines[jobID]
	if !exists {
		return nil, false
	}
	
	// Return a copy to prevent external modification
	deadlineCopy := *deadline
	return &deadlineCopy, true
}

// CheckDeadline checks if a job deadline has been exceeded
func (tm *TimingManager) CheckDeadline(jobID string) (bool, time.Duration) {
	tm.mu.RLock()
	deadline, exists := tm.deadlines[jobID]
	tm.mu.RUnlock()
	
	if !exists {
		return false, 0
	}
	
	now := time.Now()
	if now.After(deadline.AdjustedDeadline) {
		atomic.AddInt64(&tm.missedDeadlines, 1)
		return true, now.Sub(deadline.AdjustedDeadline)
	}
	
	return false, deadline.AdjustedDeadline.Sub(now)
}

// RemoveDeadline removes a deadline for a completed job
func (tm *TimingManager) RemoveDeadline(jobID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	
	delete(tm.deadlines, jobID)
}

// PropagateDeadline creates a context with deadline from a job
func (tm *TimingManager) PropagateDeadline(ctx context.Context, jobID string) (context.Context, context.CancelFunc) {
	deadline, exists := tm.GetDeadline(jobID)
	if !exists {
		// No deadline set, return context as-is
		return ctx, func() {}
	}
	
	// Create context with the adjusted deadline
	return context.WithDeadline(ctx, deadline.AdjustedDeadline)
}

// CheckBackpressure checks if backpressure should be applied
func (tm *TimingManager) CheckBackpressure() bool {
	shouldApply := tm.backpressure.ShouldApplyBackpressure()
	if shouldApply {
		atomic.AddInt64(&tm.backpressureEvents, 1)
	}
	return shouldApply
}

// RecordEvent records an event for backpressure calculation
func (tm *TimingManager) RecordEvent() {
	tm.backpressure.RecordEvent()
}

// GetBackpressureDelay returns the recommended delay for backpressure
func (tm *TimingManager) GetBackpressureDelay() time.Duration {
	return tm.backpressure.GetDelay()
}

// GetMetrics returns timing-related metrics
func (tm *TimingManager) GetMetrics() map[string]interface{} {
	tm.mu.RLock()
	deadlineCount := len(tm.deadlines)
	offset := tm.serverTimeOffset
	tm.mu.RUnlock()
	
	return map[string]interface{}{
		"clock_skew_offset_ms":   offset.Milliseconds(),
		"skew_detections":        atomic.LoadInt64(&tm.skewDetections),
		"active_deadlines":       deadlineCount,
		"missed_deadlines":       atomic.LoadInt64(&tm.missedDeadlines),
		"backpressure_events":    atomic.LoadInt64(&tm.backpressureEvents),
		"backpressure_active":    tm.backpressure.IsActive(),
		"current_rate":          tm.backpressure.GetCurrentRate(),
	}
}

// BackpressureController manages backpressure for high-frequency operations
type BackpressureController struct {
	mu              sync.RWMutex
	window          time.Duration
	limit           int
	events          []time.Time
	lastCleanup     time.Time
	backoffFactor   float64
	maxBackoff      time.Duration
}

// NewBackpressureController creates a new backpressure controller
func NewBackpressureController(window time.Duration, limit int) *BackpressureController {
	return &BackpressureController{
		window:        window,
		limit:         limit,
		events:        make([]time.Time, 0, limit*2),
		lastCleanup:   time.Now(),
		backoffFactor: 1.5,
		maxBackoff:    5 * time.Second,
	}
}

// RecordEvent records a new event
func (b *BackpressureController) RecordEvent() {
	b.mu.Lock()
	defer b.mu.Unlock()
	
	now := time.Now()
	b.events = append(b.events, now)
	
	// Cleanup old events periodically
	if now.Sub(b.lastCleanup) > b.window {
		b.cleanup(now)
		b.lastCleanup = now
	}
}

// cleanup removes events outside the window
func (b *BackpressureController) cleanup(now time.Time) {
	cutoff := now.Add(-b.window)
	
	// Find first event within window
	i := 0
	for i < len(b.events) && b.events[i].Before(cutoff) {
		i++
	}
	
	// Remove old events
	if i > 0 {
		b.events = b.events[i:]
	}
}

// ShouldApplyBackpressure returns true if backpressure should be applied
func (b *BackpressureController) ShouldApplyBackpressure() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	
	now := time.Now()
	cutoff := now.Add(-b.window)
	
	// Count events in window
	count := 0
	for i := len(b.events) - 1; i >= 0 && b.events[i].After(cutoff); i-- {
		count++
	}
	
	return count >= b.limit
}

// GetDelay returns the recommended backpressure delay
func (b *BackpressureController) GetDelay() time.Duration {
	b.mu.RLock()
	defer b.mu.RUnlock()
	
	rate := b.GetCurrentRate()
	if rate <= float64(b.limit) {
		return 0
	}
	
	// Calculate delay based on how much over the limit we are
	overageRatio := rate / float64(b.limit)
	delay := time.Duration(float64(b.window) * (overageRatio - 1) * b.backoffFactor)
	
	if delay > b.maxBackoff {
		delay = b.maxBackoff
	}
	
	return delay
}

// GetCurrentRate returns the current event rate per window
func (b *BackpressureController) GetCurrentRate() float64 {
	now := time.Now()
	cutoff := now.Add(-b.window)
	
	count := 0
	for i := len(b.events) - 1; i >= 0 && b.events[i].After(cutoff); i-- {
		count++
	}
	
	return float64(count)
}

// IsActive returns true if backpressure is currently active
func (b *BackpressureController) IsActive() bool {
	return b.ShouldApplyBackpressure()
}

// TimingGuard provides timing protection for operations
type TimingGuard struct {
	manager   *TimingManager
	jobID     string
	operation string
}

// NewTimingGuard creates a guard for timing-sensitive operations
func (tm *TimingManager) NewGuard(jobID string, operation string) *TimingGuard {
	return &TimingGuard{
		manager:   tm,
		jobID:     jobID,
		operation: operation,
	}
}

// Execute runs a function with timing protection
func (g *TimingGuard) Execute(ctx context.Context, fn func(context.Context) error) error {
	// Check deadline
	if exceeded, overBy := g.manager.CheckDeadline(g.jobID); exceeded {
		return fmt.Errorf("deadline exceeded for %s by %v", g.operation, overBy)
	}
	
	// Check backpressure
	if g.manager.CheckBackpressure() {
		delay := g.manager.GetBackpressureDelay()
		g.manager.logger.Debug("Applying backpressure delay",
			zap.String("operation", g.operation),
			zap.Duration("delay", delay),
		)
		time.Sleep(delay)
	}
	
	// Record event for rate limiting
	g.manager.RecordEvent()
	
	// Propagate deadline to context
	ctxWithDeadline, cancel := g.manager.PropagateDeadline(ctx, g.jobID)
	defer cancel()
	
	// Execute function
	return fn(ctxWithDeadline)
}

// ClockSkewDetector provides standalone clock skew detection
type ClockSkewDetector struct {
	samples    []time.Duration
	maxSamples int
}

// NewClockSkewDetector creates a new clock skew detector
func NewClockSkewDetector(maxSamples int) *ClockSkewDetector {
	return &ClockSkewDetector{
		samples:    make([]time.Duration, 0, maxSamples),
		maxSamples: maxSamples,
	}
}

// AddSample adds a clock difference sample
func (d *ClockSkewDetector) AddSample(localTime, remoteTime time.Time) time.Duration {
	skew := remoteTime.Sub(localTime)
	
	d.samples = append(d.samples, skew)
	if len(d.samples) > d.maxSamples {
		d.samples = d.samples[1:]
	}
	
	return d.GetAverageSkew()
}

// GetAverageSkew returns the average clock skew
func (d *ClockSkewDetector) GetAverageSkew() time.Duration {
	if len(d.samples) == 0 {
		return 0
	}
	
	var total time.Duration
	for _, s := range d.samples {
		total += s
	}
	
	return total / time.Duration(len(d.samples))
}

// Helper function for absolute value of duration
func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// DeadlineManager provides high-level deadline management
type DeadlineManager struct {
	timingManager *TimingManager
	logger        *zap.Logger
}

// NewDeadlineManager creates a new deadline manager
func NewDeadlineManager(tm *TimingManager, logger *zap.Logger) *DeadlineManager {
	return &DeadlineManager{
		timingManager: tm,
		logger:        logger,
	}
}

// SetJobDeadline sets a deadline for a job from a Job message
func (dm *DeadlineManager) SetJobDeadline(job *livekit.Job) {
	if job == nil {
		return
	}
	
	// Extract deadline from job metadata if available
	// This is where you'd parse any deadline information from the job
	// For now, we'll use a default deadline
	deadline := time.Now().Add(5 * time.Minute)
	
	dm.timingManager.SetDeadline(job.Id, deadline, "job_assignment")
}

// CreateContextWithDeadline creates a context with the job's deadline
func (dm *DeadlineManager) CreateContextWithDeadline(ctx context.Context, jobID string) (context.Context, context.CancelFunc) {
	return dm.timingManager.PropagateDeadline(ctx, jobID)
}