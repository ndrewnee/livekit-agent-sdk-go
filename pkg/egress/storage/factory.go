package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
)

// Storage defines the interface for storage backends
// Implements storage requirements from PLAN.md Milestone 3
type Storage interface {
	// Core segment operations
	StoreSegment(ctx context.Context, sessionID string, segmentName string, data []byte) error
	GetSegment(ctx context.Context, sessionID string, segmentName string) ([]byte, error)
	DeleteSegment(ctx context.Context, sessionID string, segmentName string) error
	ListSegments(ctx context.Context, sessionID string) ([]string, error)

	// Playlist operations
	StorePlaylists(ctx context.Context, sessionID string, master, media []byte) error
	GetPlaylist(ctx context.Context, sessionID string, playlistType string) ([]byte, error)

	// Manifest operations
	StoreManifest(ctx context.Context, sessionID string, manifest []byte) error
	GetManifest(ctx context.Context, sessionID string) ([]byte, error)

	// Screenshot operations
	StoreScreenshot(ctx context.Context, sessionID string, timestamp int64, data []byte) error
	ListScreenshots(ctx context.Context, sessionID string) ([]ScreenshotInfo, error)

	// Management operations
	GetStorageInfo() StorageInfo
	Close() error
	IsHealthy() bool
	GetMetrics() StorageMetrics
}

// ScreenshotInfo holds screenshot metadata
type ScreenshotInfo struct {
	Timestamp int64
	Size      int64
	Format    string
	URL       string
}

// StorageInfo holds storage backend information
type StorageInfo struct {
	Type        StorageType
	Available   bool
	TotalSpace  int64
	UsedSpace   int64
	FreeSpace   int64
	Location    string
	CloudBucket string
}

// StorageMetrics holds storage performance metrics
type StorageMetrics struct {
	TotalUploads       int64
	SuccessfulUploads  int64
	FailedUploads      int64
	RetryCount         int64
	TotalDownloads     int64
	BytesUploaded      int64
	BytesDownloaded    int64
	AverageUploadTime  time.Duration
	CircuitBreakerOpen bool
	BufferedSegments   int
	LastError          string
	LastErrorTime      time.Time
}

// Factory creates storage instances based on configuration
type Factory struct {
	config  *Config
	logger  logger.Logger
	mu      sync.RWMutex
	storage Storage
	metrics StorageMetrics
}

// NewFactory creates a new storage factory
func NewFactory(config *Config, logger logger.Logger) (*Factory, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid storage config: %w", err)
	}

	f := &Factory{
		config: config,
		logger: logger,
	}

	// Create storage based on type
	storage, err := f.createStorage()
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}

	f.storage = storage
	return f, nil
}

// createStorage creates the appropriate storage backend
func (f *Factory) createStorage() (Storage, error) {
	switch f.config.Type {
	case StorageTypeLocal:
		return f.createLocalStorage()

	case StorageTypeS3:
		return f.createS3Storage()

	case StorageTypeGCS:
		return f.createGCSStorage()

	case StorageTypeHybrid:
		return f.createHybridStorage()

	default:
		return nil, fmt.Errorf("unsupported storage type: %s", f.config.Type)
	}
}

// createLocalStorage creates a local filesystem storage
func (f *Factory) createLocalStorage() (Storage, error) {
	ls, err := NewLocalStorage(&f.config.Local, &f.config.Retention)
	if err != nil {
		return nil, fmt.Errorf("failed to create local storage: %w", err)
	}

	return ls, nil
}

// createS3Storage creates an S3 storage backend
func (f *Factory) createS3Storage() (Storage, error) {
	// Create local fallback if needed
	var localFallback *LocalStorage
	if f.config.Local.BufferOnFailure {
		ls, err := NewLocalStorage(&f.config.Local, &f.config.Retention)
		if err != nil {
			f.logger.Warnw("failed to create local buffer for S3", err)
		} else {
			localFallback = ls
		}
	}

	// Create S3 storage with config
	s3Storage, err := NewS3Storage(&f.config.S3, &f.config.Upload, localFallback)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 storage: %w", err)
	}

	return s3Storage, nil
}

// createGCSStorage creates a Google Cloud Storage backend
func (f *Factory) createGCSStorage() (Storage, error) {
	// GCS implementation will be added in the next task
	return nil, fmt.Errorf("GCS storage not yet implemented")
}

// createHybridStorage creates a hybrid storage backend
func (f *Factory) createHybridStorage() (Storage, error) {
	// Create local storage
	localStorage, err := f.createLocalStorage()
	if err != nil {
		return nil, fmt.Errorf("failed to create local storage for hybrid: %w", err)
	}

	// Determine cloud backend
	var cloudStorage Storage
	if f.config.S3.Bucket != "" {
		cloudStorage, err = f.createS3Storage()
		if err != nil {
			f.logger.Warnw("failed to create S3 storage for hybrid, using local only", err)
			return localStorage, nil
		}
	} else if f.config.GCS.Bucket != "" {
		cloudStorage, err = f.createGCSStorage()
		if err != nil {
			f.logger.Warnw("failed to create GCS storage for hybrid, using local only", err)
			return localStorage, nil
		}
	} else {
		return nil, fmt.Errorf("hybrid storage requires S3 or GCS configuration")
	}

	// Create hybrid storage wrapper
	return NewHybridStorage(localStorage, cloudStorage, f.config, f.logger)
}

// GetStorage returns the configured storage backend
func (f *Factory) GetStorage() Storage {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.storage
}

// UpdateConfig updates the storage configuration and recreates storage if needed
func (f *Factory) UpdateConfig(config *Config) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid storage config: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Close existing storage
	if f.storage != nil {
		if err := f.storage.Close(); err != nil {
			f.logger.Warnw("error closing existing storage", err)
		}
	}

	// Update config
	f.config = config

	// Create new storage
	storage, err := f.createStorage()
	if err != nil {
		return fmt.Errorf("failed to create storage with new config: %w", err)
	}

	f.storage = storage
	return nil
}

// GetMetrics returns storage metrics
func (f *Factory) GetMetrics() StorageMetrics {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if f.storage != nil {
		return f.storage.GetMetrics()
	}
	return f.metrics
}

// Close closes the storage factory and underlying storage
func (f *Factory) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.storage != nil {
		return f.storage.Close()
	}
	return nil
}

// HybridStorage implements hybrid local+cloud storage
type HybridStorage struct {
	local       Storage
	cloud       Storage
	config      *Config
	logger      logger.Logger
	mu          sync.RWMutex
	uploadQueue chan *uploadTask
	wg          sync.WaitGroup
	closed      bool
	metrics     StorageMetrics
}

// uploadTask represents a segment to upload
type uploadTask struct {
	sessionID   string
	segmentName string
	data        []byte
	retries     int
	timestamp   time.Time
}

// NewHybridStorage creates a hybrid storage backend
func NewHybridStorage(local, cloud Storage, config *Config, logger logger.Logger) (*HybridStorage, error) {
	h := &HybridStorage{
		local:       local,
		cloud:       cloud,
		config:      config,
		logger:      logger,
		uploadQueue: make(chan *uploadTask, 1000),
	}

	// Start upload workers
	workerCount := config.Upload.MaxConcurrent
	if workerCount <= 0 {
		workerCount = 5
	}

	for i := 0; i < workerCount; i++ {
		h.wg.Add(1)
		go h.uploadWorker()
	}

	return h, nil
}

// StoreSegment stores a segment in both local and cloud storage
func (h *HybridStorage) StoreSegment(ctx context.Context, sessionID string, segmentName string, data []byte) error {
	// Always store locally first
	if err := h.local.StoreSegment(ctx, sessionID, segmentName, data); err != nil {
		return fmt.Errorf("failed to store segment locally: %w", err)
	}

	// Queue for cloud upload if enabled
	if h.config.Upload.UploadOnCreate && h.cloud != nil {
		select {
		case h.uploadQueue <- &uploadTask{
			sessionID:   sessionID,
			segmentName: segmentName,
			data:        data,
			timestamp:   time.Now(),
		}:
			h.metrics.BufferedSegments++
		default:
			h.logger.Warnw("upload queue full, segment will be uploaded later", nil,
				"sessionID", sessionID,
				"segmentName", segmentName)
		}
	}

	return nil
}

// uploadWorker processes upload tasks
func (h *HybridStorage) uploadWorker() {
	defer h.wg.Done()

	for task := range h.uploadQueue {
		h.processUpload(task)
	}
}

// processUpload handles a single upload task
func (h *HybridStorage) processUpload(task *uploadTask) {
	ctx, cancel := context.WithTimeout(context.Background(), h.config.Upload.UploadTimeout)
	defer cancel()

	h.metrics.TotalUploads++

	err := h.cloud.StoreSegment(ctx, task.sessionID, task.segmentName, task.data)
	if err != nil {
		h.metrics.FailedUploads++
		h.metrics.LastError = err.Error()
		h.metrics.LastErrorTime = time.Now()

		// Retry logic
		if task.retries < h.config.Upload.Retry.MaxAttempts {
			task.retries++
			h.metrics.RetryCount++

			// Re-queue with backoff
			time.Sleep(h.calculateBackoff(task.retries))
			select {
			case h.uploadQueue <- task:
				// Re-queued successfully
			default:
				h.logger.Errorw("failed to re-queue upload task", err,
					"sessionID", task.sessionID,
					"segmentName", task.segmentName)
			}
		} else {
			h.logger.Errorw("upload failed after max retries", err,
				"sessionID", task.sessionID,
				"segmentName", task.segmentName)
		}
	} else {
		h.metrics.SuccessfulUploads++
		h.metrics.BytesUploaded += int64(len(task.data))
		h.metrics.BufferedSegments--

		// Delete from local if configured
		if h.config.Retention.DeleteAfterUpload {
			if err := h.local.DeleteSegment(context.Background(), task.sessionID, task.segmentName); err != nil {
				h.logger.Warnw("failed to delete local segment after upload", err,
					"sessionID", task.sessionID,
					"segmentName", task.segmentName)
			}
		}
	}
}

// calculateBackoff calculates exponential backoff duration
func (h *HybridStorage) calculateBackoff(attempt int) time.Duration {
	backoff := h.config.Upload.Retry.InitialBackoff
	for i := 1; i < attempt; i++ {
		backoff = time.Duration(float64(backoff) * h.config.Upload.Retry.Multiplier)
		if backoff > h.config.Upload.Retry.MaxBackoff {
			backoff = h.config.Upload.Retry.MaxBackoff
			break
		}
	}

	// Add jitter if configured
	if h.config.Upload.Retry.Jitter {
		jitter := time.Duration(float64(backoff) * 0.1)
		backoff += jitter
	}

	return backoff
}

// GetSegment retrieves a segment from storage
func (h *HybridStorage) GetSegment(ctx context.Context, sessionID string, segmentName string) ([]byte, error) {
	// Try local first
	data, err := h.local.GetSegment(ctx, sessionID, segmentName)
	if err == nil {
		h.metrics.TotalDownloads++
		h.metrics.BytesDownloaded += int64(len(data))
		return data, nil
	}

	// Fallback to cloud
	if h.cloud != nil {
		data, err = h.cloud.GetSegment(ctx, sessionID, segmentName)
		if err == nil {
			h.metrics.TotalDownloads++
			h.metrics.BytesDownloaded += int64(len(data))

			// Cache locally for future access
			h.local.StoreSegment(ctx, sessionID, segmentName, data)
			return data, nil
		}
	}

	return nil, fmt.Errorf("segment not found in local or cloud storage")
}

// DeleteSegment deletes a segment from both storages
func (h *HybridStorage) DeleteSegment(ctx context.Context, sessionID string, segmentName string) error {
	var errs []error

	// Delete from local
	if err := h.local.DeleteSegment(ctx, sessionID, segmentName); err != nil {
		errs = append(errs, fmt.Errorf("local delete failed: %w", err))
	}

	// Delete from cloud
	if h.cloud != nil {
		if err := h.cloud.DeleteSegment(ctx, sessionID, segmentName); err != nil {
			errs = append(errs, fmt.Errorf("cloud delete failed: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("delete errors: %v", errs)
	}
	return nil
}

// ListSegments lists segments from both storages
func (h *HybridStorage) ListSegments(ctx context.Context, sessionID string) ([]string, error) {
	segmentMap := make(map[string]bool)

	// Get local segments
	localSegments, err := h.local.ListSegments(ctx, sessionID)
	if err == nil {
		for _, seg := range localSegments {
			segmentMap[seg] = true
		}
	}

	// Get cloud segments
	if h.cloud != nil {
		cloudSegments, err := h.cloud.ListSegments(ctx, sessionID)
		if err == nil {
			for _, seg := range cloudSegments {
				segmentMap[seg] = true
			}
		}
	}

	// Convert map to slice
	segments := make([]string, 0, len(segmentMap))
	for seg := range segmentMap {
		segments = append(segments, seg)
	}

	return segments, nil
}

// StorePlaylists stores playlists in both storages
func (h *HybridStorage) StorePlaylists(ctx context.Context, sessionID string, master, media []byte) error {
	// Store locally
	if err := h.local.StorePlaylists(ctx, sessionID, master, media); err != nil {
		return fmt.Errorf("failed to store playlists locally: %w", err)
	}

	// Queue for cloud upload if available
	if h.cloud != nil && h.config.Upload.UploadOnCreate {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), h.config.Upload.UploadTimeout)
			defer cancel()
			if err := h.cloud.StorePlaylists(ctx, sessionID, master, media); err != nil {
				h.logger.Warnw("failed to upload playlists to cloud", err)
			}
		}()
	}

	return nil
}

// GetPlaylist retrieves a playlist
func (h *HybridStorage) GetPlaylist(ctx context.Context, sessionID string, playlistType string) ([]byte, error) {
	// Try local first
	data, err := h.local.GetPlaylist(ctx, sessionID, playlistType)
	if err == nil {
		return data, nil
	}

	// Fallback to cloud
	if h.cloud != nil {
		return h.cloud.GetPlaylist(ctx, sessionID, playlistType)
	}

	return nil, err
}

// StoreManifest stores manifest in both storages
func (h *HybridStorage) StoreManifest(ctx context.Context, sessionID string, manifest []byte) error {
	// Store locally
	if err := h.local.StoreManifest(ctx, sessionID, manifest); err != nil {
		return fmt.Errorf("failed to store manifest locally: %w", err)
	}

	// Upload to cloud if available
	if h.cloud != nil && h.config.Upload.UploadOnCreate {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), h.config.Upload.UploadTimeout)
			defer cancel()
			if err := h.cloud.StoreManifest(ctx, sessionID, manifest); err != nil {
				h.logger.Warnw("failed to upload manifest to cloud", err)
			}
		}()
	}

	return nil
}

// GetManifest retrieves manifest
func (h *HybridStorage) GetManifest(ctx context.Context, sessionID string) ([]byte, error) {
	// Try local first
	data, err := h.local.GetManifest(ctx, sessionID)
	if err == nil {
		return data, nil
	}

	// Fallback to cloud
	if h.cloud != nil {
		return h.cloud.GetManifest(ctx, sessionID)
	}

	return nil, err
}

// StoreScreenshot stores screenshot in both storages
func (h *HybridStorage) StoreScreenshot(ctx context.Context, sessionID string, timestamp int64, data []byte) error {
	// Store locally
	if err := h.local.StoreScreenshot(ctx, sessionID, timestamp, data); err != nil {
		return fmt.Errorf("failed to store screenshot locally: %w", err)
	}

	// Upload to cloud if configured
	if h.cloud != nil && h.config.Screenshot.Upload {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), h.config.Upload.UploadTimeout)
			defer cancel()
			if err := h.cloud.StoreScreenshot(ctx, sessionID, timestamp, data); err != nil {
				h.logger.Warnw("failed to upload screenshot to cloud", err)
			}
		}()
	}

	return nil
}

// ListScreenshots lists screenshots
func (h *HybridStorage) ListScreenshots(ctx context.Context, sessionID string) ([]ScreenshotInfo, error) {
	// Get from local (primary source)
	return h.local.ListScreenshots(ctx, sessionID)
}

// GetStorageInfo returns storage information
func (h *HybridStorage) GetStorageInfo() StorageInfo {
	localInfo := h.local.GetStorageInfo()
	if h.cloud != nil {
		cloudInfo := h.cloud.GetStorageInfo()
		localInfo.Type = StorageTypeHybrid
		localInfo.CloudBucket = cloudInfo.CloudBucket
	}
	return localInfo
}

// IsHealthy checks if storage is healthy
func (h *HybridStorage) IsHealthy() bool {
	localHealthy := h.local.IsHealthy()

	// Hybrid is healthy if local is healthy (cloud is optional)
	return localHealthy
}

// GetMetrics returns storage metrics
func (h *HybridStorage) GetMetrics() StorageMetrics {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.metrics
}

// Close closes the hybrid storage
func (h *HybridStorage) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	close(h.uploadQueue)
	h.mu.Unlock()

	// Wait for upload workers to finish
	h.wg.Wait()

	var errs []error

	// Close local storage
	if err := h.local.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close local storage: %w", err))
	}

	// Close cloud storage
	if h.cloud != nil {
		if err := h.cloud.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close cloud storage: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}