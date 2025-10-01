package storage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/logger"
)

// UploadManager manages concurrent segment uploads with rate limiting
// Implements upload management requirements from PLAN.md Milestone 3
type UploadManager struct {
	logger   logger.Logger
	storage  Storage
	config   *UploadConfig
	breaker  *CircuitBreaker

	// Concurrency control
	semaphore    chan struct{}
	uploadQueue  chan *UploadJob
	batchQueue   chan *UploadBatch
	workerGroup  sync.WaitGroup

	// State
	mu       sync.RWMutex
	running  bool
	stopChan chan struct{}

	// Metrics
	metrics UploadMetrics
}

// UploadJob represents a single upload task
type UploadJob struct {
	ID          string
	SessionID   string
	SegmentName string
	Data        []byte
	ContentType string
	Priority    int
	Retries     int
	CreatedAt   time.Time
	Callback    func(error)
}

// UploadBatch represents a batch of uploads
type UploadBatch struct {
	Jobs      []*UploadJob
	CreatedAt time.Time
}

// UploadMetrics tracks upload performance
type UploadMetrics struct {
	TotalJobs         int64
	PendingJobs       int64
	ActiveUploads     int64
	CompletedUploads  int64
	FailedUploads     int64
	RetryCount        int64
	AverageUploadTime time.Duration
	TotalBytesQueued  int64
	TotalBytesUploaded int64
	LastUploadTime    time.Time
	LastError         error
	LastErrorTime     time.Time
}

// NewUploadManager creates a new upload manager
func NewUploadManager(storage Storage, config *UploadConfig, logger logger.Logger) *UploadManager {
	// Set defaults
	maxConcurrent := config.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}

	// Create circuit breaker
	breaker := NewCircuitBreaker(
		config.CircuitBreaker.FailureThreshold,
		config.CircuitBreaker.SuccessThreshold,
		config.CircuitBreaker.OpenTimeout,
		config.CircuitBreaker.HalfOpenRequests,
	)

	um := &UploadManager{
		logger:      logger,
		storage:     storage,
		config:      config,
		breaker:     breaker,
		semaphore:   make(chan struct{}, maxConcurrent),
		uploadQueue: make(chan *UploadJob, 1000),
		batchQueue:  make(chan *UploadBatch, 100),
		stopChan:    make(chan struct{}),
	}

	// Initialize semaphore
	for i := 0; i < maxConcurrent; i++ {
		um.semaphore <- struct{}{}
	}

	return um
}

// Start starts the upload manager
func (um *UploadManager) Start() error {
	um.mu.Lock()
	defer um.mu.Unlock()

	if um.running {
		return fmt.Errorf("upload manager already running")
	}

	um.running = true

	// Start upload workers
	workerCount := um.config.MaxConcurrent
	if workerCount <= 0 {
		workerCount = 5
	}

	for i := 0; i < workerCount; i++ {
		um.workerGroup.Add(1)
		go um.uploadWorker(i)
	}

	// Start batch processor if enabled
	if um.config.Batch.Enabled {
		um.workerGroup.Add(1)
		go um.batchProcessor()
	}

	// Start metrics collector
	um.workerGroup.Add(1)
	go um.metricsCollector()

	um.logger.Infow("upload manager started",
		"workers", workerCount,
		"batch_enabled", um.config.Batch.Enabled)

	return nil
}

// Stop stops the upload manager
func (um *UploadManager) Stop() error {
	um.mu.Lock()
	if !um.running {
		um.mu.Unlock()
		return nil
	}

	um.running = false
	close(um.stopChan)
	um.mu.Unlock()

	// Wait for workers to finish
	done := make(chan struct{})
	go func() {
		um.workerGroup.Wait()
		close(done)
	}()

	select {
	case <-done:
		um.logger.Infow("upload manager stopped gracefully")
	case <-time.After(30 * time.Second):
		um.logger.Warnw("upload manager stop timeout", nil)
	}

	return nil
}

// QueueUpload queues a segment for upload
func (um *UploadManager) QueueUpload(job *UploadJob) error {
	if !um.isRunning() {
		return fmt.Errorf("upload manager not running")
	}

	// Update metrics
	atomic.AddInt64(&um.metrics.TotalJobs, 1)
	atomic.AddInt64(&um.metrics.PendingJobs, 1)
	atomic.AddInt64(&um.metrics.TotalBytesQueued, int64(len(job.Data)))

	// Queue the job
	select {
	case um.uploadQueue <- job:
		return nil
	case <-time.After(5 * time.Second):
		atomic.AddInt64(&um.metrics.PendingJobs, -1)
		return fmt.Errorf("upload queue full, timeout queueing job")
	}
}

// uploadWorker processes upload jobs
func (um *UploadManager) uploadWorker(id int) {
	defer um.workerGroup.Done()

	um.logger.Debugw("upload worker started", "worker_id", id)

	for {
		select {
		case <-um.stopChan:
			um.logger.Debugw("upload worker stopping", "worker_id", id)
			return

		case job := <-um.uploadQueue:
			if job == nil {
				continue
			}

			// Acquire semaphore
			<-um.semaphore

			// Process upload
			um.processUpload(job)

			// Release semaphore
			um.semaphore <- struct{}{}
		}
	}
}

// processUpload handles a single upload job
func (um *UploadManager) processUpload(job *UploadJob) {
	// Update metrics
	atomic.AddInt64(&um.metrics.PendingJobs, -1)
	atomic.AddInt64(&um.metrics.ActiveUploads, 1)
	defer atomic.AddInt64(&um.metrics.ActiveUploads, -1)

	startTime := time.Now()

	// Execute with circuit breaker
	err := um.breaker.Execute(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), um.config.UploadTimeout)
		defer cancel()

		return um.storage.StoreSegment(ctx, job.SessionID, job.SegmentName, job.Data)
	})

	uploadDuration := time.Since(startTime)

	if err != nil {
		um.handleUploadError(job, err)
	} else {
		um.handleUploadSuccess(job, uploadDuration)
	}
}

// handleUploadError handles upload failures
func (um *UploadManager) handleUploadError(job *UploadJob, err error) {
	atomic.AddInt64(&um.metrics.FailedUploads, 1)

	um.mu.Lock()
	um.metrics.LastError = err
	um.metrics.LastErrorTime = time.Now()
	um.mu.Unlock()

	// Check if we should retry
	if um.shouldRetry(job) {
		job.Retries++
		atomic.AddInt64(&um.metrics.RetryCount, 1)

		// Calculate backoff
		backoff := um.calculateBackoff(job.Retries)

		um.logger.Warnw("upload failed, retrying", err,
			"session_id", job.SessionID,
			"segment", job.SegmentName,
			"attempt", job.Retries,
			"backoff", backoff)

		// Re-queue after backoff
		time.AfterFunc(backoff, func() {
			um.QueueUpload(job)
		})
	} else {
		um.logger.Errorw("upload failed after max retries", err,
			"session_id", job.SessionID,
			"segment", job.SegmentName,
			"attempts", job.Retries)

		// Call failure callback if provided
		if job.Callback != nil {
			job.Callback(err)
		}
	}
}

// handleUploadSuccess handles successful uploads
func (um *UploadManager) handleUploadSuccess(job *UploadJob, duration time.Duration) {
	atomic.AddInt64(&um.metrics.CompletedUploads, 1)
	atomic.AddInt64(&um.metrics.TotalBytesUploaded, int64(len(job.Data)))

	um.mu.Lock()
	um.metrics.LastUploadTime = time.Now()

	// Update average upload time
	if um.metrics.AverageUploadTime == 0 {
		um.metrics.AverageUploadTime = duration
	} else {
		// Simple moving average
		um.metrics.AverageUploadTime = (um.metrics.AverageUploadTime + duration) / 2
	}
	um.mu.Unlock()

	um.logger.Debugw("upload completed",
		"session_id", job.SessionID,
		"segment", job.SegmentName,
		"duration", duration,
		"size", len(job.Data))

	// Call success callback if provided
	if job.Callback != nil {
		job.Callback(nil)
	}
}

// shouldRetry determines if a job should be retried
func (um *UploadManager) shouldRetry(job *UploadJob) bool {
	if !um.config.Retry.Enabled {
		return false
	}

	return job.Retries < um.config.Retry.MaxAttempts
}

// calculateBackoff calculates exponential backoff duration
func (um *UploadManager) calculateBackoff(attempt int) time.Duration {
	backoff := um.config.Retry.InitialBackoff

	for i := 1; i < attempt; i++ {
		backoff = time.Duration(float64(backoff) * um.config.Retry.Multiplier)
		if backoff > um.config.Retry.MaxBackoff {
			backoff = um.config.Retry.MaxBackoff
			break
		}
	}

	// Add jitter if configured
	if um.config.Retry.Jitter {
		jitter := time.Duration(float64(backoff) * 0.2) // 20% jitter
		backoff = backoff + jitter
	}

	return backoff
}

// batchProcessor processes batched uploads
func (um *UploadManager) batchProcessor() {
	defer um.workerGroup.Done()

	ticker := time.NewTicker(um.config.Batch.MaxWait)
	defer ticker.Stop()

	currentBatch := &UploadBatch{
		Jobs:      make([]*UploadJob, 0, um.config.Batch.MaxSize),
		CreatedAt: time.Now(),
	}

	for {
		select {
		case <-um.stopChan:
			// Process remaining batch
			if len(currentBatch.Jobs) > 0 {
				um.processBatch(currentBatch)
			}
			return

		case job := <-um.uploadQueue:
			if job == nil {
				continue
			}

			currentBatch.Jobs = append(currentBatch.Jobs, job)

			// Check if batch is full
			if len(currentBatch.Jobs) >= um.config.Batch.MaxSize {
				um.processBatch(currentBatch)
				currentBatch = &UploadBatch{
					Jobs:      make([]*UploadJob, 0, um.config.Batch.MaxSize),
					CreatedAt: time.Now(),
				}
			}

		case <-ticker.C:
			// Process batch on timeout
			if len(currentBatch.Jobs) > 0 {
				um.processBatch(currentBatch)
				currentBatch = &UploadBatch{
					Jobs:      make([]*UploadJob, 0, um.config.Batch.MaxSize),
					CreatedAt: time.Now(),
				}
			}
		}
	}
}

// processBatch processes a batch of uploads
func (um *UploadManager) processBatch(batch *UploadBatch) {
	if len(batch.Jobs) == 0 {
		return
	}

	um.logger.Debugw("processing upload batch",
		"size", len(batch.Jobs),
		"age", time.Since(batch.CreatedAt))

	// Process each job in the batch concurrently
	var wg sync.WaitGroup
	for _, job := range batch.Jobs {
		wg.Add(1)
		go func(j *UploadJob) {
			defer wg.Done()

			// Acquire semaphore
			<-um.semaphore
			defer func() {
				um.semaphore <- struct{}{}
			}()

			um.processUpload(j)
		}(job)
	}

	// Wait for batch to complete
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		um.logger.Debugw("batch upload completed", "size", len(batch.Jobs))
	case <-time.After(um.config.UploadTimeout * time.Duration(len(batch.Jobs))):
		um.logger.Warnw("batch upload timeout", nil, "size", len(batch.Jobs))
	}
}

// metricsCollector periodically collects metrics
func (um *UploadManager) metricsCollector() {
	defer um.workerGroup.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-um.stopChan:
			return

		case <-ticker.C:
			metrics := um.GetMetrics()
			um.logger.Infow("upload manager metrics",
				"total_jobs", metrics.TotalJobs,
				"pending", metrics.PendingJobs,
				"active", metrics.ActiveUploads,
				"completed", metrics.CompletedUploads,
				"failed", metrics.FailedUploads,
				"retries", metrics.RetryCount,
				"avg_upload_time", metrics.AverageUploadTime,
				"bytes_uploaded", metrics.TotalBytesUploaded,
				"circuit_breaker", um.breaker.GetStateString())
		}
	}
}

// GetMetrics returns upload manager metrics
func (um *UploadManager) GetMetrics() UploadMetrics {
	um.mu.RLock()
	defer um.mu.RUnlock()

	// Create a copy of metrics
	metrics := um.metrics

	// Add circuit breaker state
	if um.breaker.IsOpen() {
		metrics.LastError = fmt.Errorf("circuit breaker open")
	}

	return metrics
}

// IsHealthy checks if the upload manager is healthy
func (um *UploadManager) IsHealthy() bool {
	if !um.isRunning() {
		return false
	}

	// Check circuit breaker
	if um.breaker.IsOpen() {
		return false
	}

	// Check queue depth
	pendingJobs := atomic.LoadInt64(&um.metrics.PendingJobs)
	if pendingJobs > 500 { // High queue depth threshold
		return false
	}

	// Check failure rate
	failed := atomic.LoadInt64(&um.metrics.FailedUploads)
	completed := atomic.LoadInt64(&um.metrics.CompletedUploads)
	total := failed + completed

	if total > 100 && float64(failed)/float64(total) > 0.1 { // > 10% failure rate
		return false
	}

	return true
}

// isRunning checks if the upload manager is running
func (um *UploadManager) isRunning() bool {
	um.mu.RLock()
	defer um.mu.RUnlock()
	return um.running
}

// Flush waits for all pending uploads to complete
func (um *UploadManager) Flush(timeout time.Duration) error {
	if !um.isRunning() {
		return fmt.Errorf("upload manager not running")
	}

	deadline := time.Now().Add(timeout)

	for {
		pending := atomic.LoadInt64(&um.metrics.PendingJobs)
		active := atomic.LoadInt64(&um.metrics.ActiveUploads)

		if pending == 0 && active == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("flush timeout, pending: %d, active: %d", pending, active)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// CancelAll cancels all pending uploads
func (um *UploadManager) CancelAll() {
	um.mu.Lock()
	defer um.mu.Unlock()

	// Drain the queue
	for {
		select {
		case <-um.uploadQueue:
			atomic.AddInt64(&um.metrics.PendingJobs, -1)
		default:
			return
		}
	}
}