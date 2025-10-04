package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/livekit/protocol/logger"
)

// S3Storage implements S3 storage with retry logic and circuit breaker
// Implements requirements from PLAN.md Milestone 3
type S3Storage struct {
	client  *s3.Client
	config  *S3Config
	upload  *UploadConfig

	// Circuit breaker
	circuitBreaker *CircuitBreaker

	// Statistics
	stats S3Stats
	mu    sync.RWMutex

	// Concurrent upload management
	uploadSemaphore chan struct{}
	activeUploads   atomic.Int32

	// Local fallback
	localFallback *LocalStorage
}

// S3Stats tracks S3 operation statistics
type S3Stats struct {
	TotalAttempts      int64     `json:"total_attempts"`
	UploadsAttempted   int64     `json:"uploads_attempted"`
	UploadsSucceeded   int64     `json:"uploads_succeeded"`
	UploadsFailed      int64     `json:"uploads_failed"`
	UploadsRetried     int64     `json:"uploads_retried"`
	RetryAttempts      int64     `json:"retry_attempts"`
	BytesUploaded      int64     `json:"bytes_uploaded"`
	BytesDownloaded    int64     `json:"bytes_downloaded"`
	TotalUploadTime    int64     `json:"total_upload_time_ms"`
	AverageUploadTime  int64     `json:"average_upload_time_ms"`
	LastUploadTime     time.Time `json:"last_upload_time"`
	LastFailureTime    time.Time `json:"last_failure_time"`
	CircuitBreakerOpen bool      `json:"circuit_breaker_open"`
}

// NewS3Storage creates a new S3 storage with enhanced features
func NewS3Storage(s3Config *S3Config, uploadConfig *UploadConfig, localFallback *LocalStorage) (*S3Storage, error) {
	if s3Config.Bucket == "" {
		return nil, fmt.Errorf("S3 bucket not specified")
	}

	// Create AWS configuration
	ctx := context.Background()
	awsConfig, err := createAWSConfig(ctx, s3Config)
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS config: %w", err)
	}

	// Create S3 client
	client := s3.NewFromConfig(awsConfig)

	// Create upload semaphore for concurrent uploads
	semaphore := make(chan struct{}, uploadConfig.MaxConcurrent)
	for i := 0; i < uploadConfig.MaxConcurrent; i++ {
		semaphore <- struct{}{}
	}

	s3s := &S3Storage{
		client:          client,
		config:          s3Config,
		upload:          uploadConfig,
		uploadSemaphore: semaphore,
		localFallback:   localFallback,
	}

	// Initialize circuit breaker if enabled
	if uploadConfig.CircuitBreaker.Enabled {
		s3s.circuitBreaker = NewCircuitBreaker(
			uploadConfig.CircuitBreaker.FailureThreshold,
			uploadConfig.CircuitBreaker.SuccessThreshold,
			uploadConfig.CircuitBreaker.OpenTimeout,
			uploadConfig.CircuitBreaker.HalfOpenRequests,
		)
	}

	logger.Infow("S3 storage initialized",
		"bucket", s3Config.Bucket,
		"region", s3Config.Region,
		"prefix", s3Config.Prefix,
		"retryEnabled", uploadConfig.Retry.Enabled,
		"circuitBreakerEnabled", uploadConfig.CircuitBreaker.Enabled)

	return s3s, nil
}

// createAWSConfig creates AWS configuration with credentials
func createAWSConfig(ctx context.Context, s3Config *S3Config) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	// Set region
	if s3Config.Region != "" {
		opts = append(opts, config.WithRegion(s3Config.Region))
	}

	// Set credentials if provided
	if s3Config.AccessKeyID != "" && s3Config.SecretAccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				s3Config.AccessKeyID,
				s3Config.SecretAccessKey,
				s3Config.SessionToken,
			),
		))
	}

	// Custom endpoint for MinIO or other S3-compatible storage
	if s3Config.Endpoint != "" && s3Config.Endpoint != "s3.amazonaws.com" {
		// Determine protocol based on UseSSL setting
		protocol := "https"
		if !s3Config.UseSSL {
			protocol = "http"
		}

		opts = append(opts, config.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(
				func(service, region string, options ...interface{}) (aws.Endpoint, error) {
					if service == s3.ServiceID {
						return aws.Endpoint{
							URL:               fmt.Sprintf("%s://%s", protocol, s3Config.Endpoint),
							SigningRegion:     s3Config.Region,
							HostnameImmutable: true,
						}, nil
					}
					return aws.Endpoint{}, fmt.Errorf("unknown service: %s", service)
				},
			),
		))
	}

	return config.LoadDefaultConfig(ctx, opts...)
}

// UploadSegment uploads an HLS segment with retry logic
func (s *S3Storage) UploadSegment(ctx context.Context, sessionID string, filename string, data []byte) error {
	key := s.buildKey(sessionID, "segments", filename)
	return s.uploadWithRetry(ctx, key, data, "video/MP2T")
}

// UploadPlaylist uploads an HLS playlist with retry logic
func (s *S3Storage) UploadPlaylist(ctx context.Context, sessionID string, filename string, data []byte) error {
	key := s.buildKey(sessionID, "", filename)
	return s.uploadWithRetry(ctx, key, data, "application/x-mpegURL")
}

// UploadScreenshot uploads a screenshot with retry logic
func (s *S3Storage) UploadScreenshot(ctx context.Context, sessionID string, filename string, data []byte) error {
	key := s.buildKey(sessionID, "screenshots", filename)

	// Determine content type
	contentType := "image/jpeg"
	if len(filename) > 4 {
		switch filename[len(filename)-4:] {
		case ".png":
			contentType = "image/png"
		case "webp":
			contentType = "image/webp"
		}
	}

	return s.uploadWithRetry(ctx, key, data, contentType)
}

// uploadWithRetry performs upload with exponential backoff retry
func (s *S3Storage) uploadWithRetry(ctx context.Context, key string, data []byte, contentType string) error {
	// Check circuit breaker
	if s.circuitBreaker != nil && !s.circuitBreaker.CanExecute() {
		s.handleCircuitBreakerOpen(key, data)
		return fmt.Errorf("circuit breaker open")
	}

	// Acquire semaphore for concurrent upload limit
	select {
	case <-s.uploadSemaphore:
		defer func() { s.uploadSemaphore <- struct{}{} }()
	case <-ctx.Done():
		return ctx.Err()
	}

	s.activeUploads.Add(1)
	defer s.activeUploads.Add(-1)

	atomic.AddInt64(&s.stats.UploadsAttempted, 1)

	// Retry logic
	var lastErr error
	maxAttempts := 1
	if s.upload.Retry.Enabled {
		maxAttempts = s.upload.Retry.MaxAttempts
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Calculate backoff
			backoff := s.calculateBackoff(attempt)

			logger.Debugw("retrying S3 upload",
				"attempt", attempt+1,
				"maxAttempts", maxAttempts,
				"backoff", backoff,
				"key", key)

			atomic.AddInt64(&s.stats.UploadsRetried, 1)

			// Wait with backoff
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		// Attempt upload
		uploadStart := time.Now()
		err := s.performUpload(ctx, key, data, contentType)
		uploadDuration := time.Since(uploadStart)

		if err == nil {
			// Success
			atomic.AddInt64(&s.stats.UploadsSucceeded, 1)
			atomic.AddInt64(&s.stats.BytesUploaded, int64(len(data)))
			atomic.AddInt64(&s.stats.TotalUploadTime, uploadDuration.Milliseconds())

			s.mu.Lock()
			s.stats.LastUploadTime = time.Now()
			if s.stats.UploadsSucceeded > 0 {
				s.stats.AverageUploadTime = s.stats.TotalUploadTime / s.stats.UploadsSucceeded
			}
			s.mu.Unlock()

			// Record success with circuit breaker
			if s.circuitBreaker != nil {
				s.circuitBreaker.RecordSuccess()
			}

			// Mark as uploaded in local storage
			if s.localFallback != nil {
				s.localFallback.MarkUploaded(key)
			}

			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err) {
			break
		}
	}

	// All attempts failed
	atomic.AddInt64(&s.stats.UploadsFailed, 1)

	s.mu.Lock()
	s.stats.LastFailureTime = time.Now()
	s.mu.Unlock()

	// Record failure with circuit breaker
	if s.circuitBreaker != nil {
		s.circuitBreaker.RecordFailure()
	}

	// Fallback to local storage if available
	if s.localFallback != nil && s.localFallback.config.BufferOnFailure {
		if err := s.localFallback.BufferForUpload(ctx, "", key, data); err != nil {
			logger.Errorw("failed to buffer to local storage", err,
				"key", key)
		} else {
			logger.Infow("buffered to local storage after S3 failure",
				"key", key,
				"size", len(data))
		}
	}

	return fmt.Errorf("upload failed after %d attempts: %w", maxAttempts, lastErr)
}

// performUpload performs the actual S3 upload
func (s *S3Storage) performUpload(ctx context.Context, key string, data []byte, contentType string) error {
	// Create upload context with timeout
	uploadCtx, cancel := context.WithTimeout(ctx, s.upload.UploadTimeout)
	defer cancel()

	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.config.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	}

	// Add storage class if specified
	if s.config.StorageClass != "" {
		input.StorageClass = types.StorageClass(s.config.StorageClass)
	}

	// Add ACL if specified
	if s.config.ACL != "" {
		input.ACL = types.ObjectCannedACL(s.config.ACL)
	}

	// Add server-side encryption if enabled
	if s.config.SSEEnabled {
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
		if s.config.KMSKeyID != "" {
			input.ServerSideEncryption = types.ServerSideEncryptionAwsKms
			input.SSEKMSKeyId = aws.String(s.config.KMSKeyID)
		}
	}

	_, err := s.client.PutObject(uploadCtx, input)
	return err
}

// calculateBackoff calculates exponential backoff with jitter
func (s *S3Storage) calculateBackoff(attempt int) time.Duration {
	if !s.upload.Retry.Enabled {
		return 0
	}

	// Calculate base backoff
	backoff := float64(s.upload.Retry.InitialBackoff)
	backoff *= math.Pow(s.upload.Retry.Multiplier, float64(attempt-1))

	// Cap at max backoff
	if backoff > float64(s.upload.Retry.MaxBackoff) {
		backoff = float64(s.upload.Retry.MaxBackoff)
	}

	// Add jitter if enabled
	if s.upload.Retry.Jitter {
		jitter := rand.Float64() * 0.3 * backoff // Up to 30% jitter
		if rand.Intn(2) == 0 {
			backoff += jitter
		} else {
			backoff -= jitter
		}
	}

	return time.Duration(backoff)
}

// handleCircuitBreakerOpen handles uploads when circuit breaker is open
func (s *S3Storage) handleCircuitBreakerOpen(key string, data []byte) {
	logger.Warnw("circuit breaker open, buffering to local storage", nil,
		"key", key,
		"size", len(data))

	// Update stats
	s.mu.Lock()
	s.stats.CircuitBreakerOpen = true
	s.mu.Unlock()

	// Buffer to local storage if available
	if s.localFallback != nil && s.localFallback.config.BufferOnFailure {
		ctx := context.Background()
		if err := s.localFallback.BufferForUpload(ctx, "", key, data); err != nil {
			logger.Errorw("failed to buffer during circuit breaker open", err,
				"key", key)
		}
	}
}

// RetryBufferedUploads retries uploads that were buffered during outages
func (s *S3Storage) RetryBufferedUploads(ctx context.Context) error {
	if s.localFallback == nil {
		return nil
	}

	bufferedFiles, err := s.localFallback.GetBufferedFiles()
	if err != nil {
		return fmt.Errorf("failed to get buffered files: %w", err)
	}

	if len(bufferedFiles) == 0 {
		return nil
	}

	logger.Infow("retrying buffered uploads", "count", len(bufferedFiles))

	var (
		succeeded int
		failed    int
	)

	for _, file := range bufferedFiles {
		// Read file data
		data, err := s.localFallback.RetrieveFile(ctx, file.Path)
		if err != nil {
			logger.Errorw("failed to read buffered file", err,
				"path", file.Path)
			failed++
			continue
		}

		// Determine content type based on file extension
		contentType := "application/octet-stream"
		if len(file.Path) > 3 {
			ext := file.Path[len(file.Path)-3:]
			switch ext {
			case ".ts":
				contentType = "video/MP2T"
			case "m3u8":
				contentType = "application/x-mpegURL"
			case "jpg", "jpeg":
				contentType = "image/jpeg"
			case "png":
				contentType = "image/png"
			}
		}

		// Retry upload
		if err := s.uploadWithRetry(ctx, file.Path, data, contentType); err != nil {
			logger.Errorw("failed to upload buffered file", err,
				"path", file.Path)
			failed++
		} else {
			succeeded++
			// Delete buffered file after successful upload
			if err := s.localFallback.deleteFile(file.Path); err != nil {
				logger.Warnw("failed to delete buffered file after upload", err,
					"path", file.Path)
			}
		}
	}

	logger.Infow("buffered uploads completed",
		"succeeded", succeeded,
		"failed", failed)

	return nil
}

// buildKey constructs the S3 key path
func (s *S3Storage) buildKey(sessionID, subdir, filename string) string {
	parts := []string{}

	if s.config.Prefix != "" {
		parts = append(parts, s.config.Prefix)
	}

	if sessionID != "" {
		parts = append(parts, sessionID)
	}

	if subdir != "" {
		parts = append(parts, subdir)
	}

	parts = append(parts, filename)

	key := ""
	for i, part := range parts {
		if i > 0 {
			key += "/"
		}
		key += part
	}

	return key
}

// GetStats returns S3 storage statistics
func (s *S3Storage) GetStats() S3Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := s.stats
	stats.CircuitBreakerOpen = s.circuitBreaker != nil && !s.circuitBreaker.CanExecute()

	return stats
}

// GetActiveUploads returns the number of active uploads
func (s *S3Storage) GetActiveUploads() int32 {
	return s.activeUploads.Load()
}

// Close closes the S3 storage
func (s *S3Storage) Close() error {
	// Wait for active uploads to complete
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			logger.Warnw("timeout waiting for uploads to complete", nil,
				"activeUploads", s.activeUploads.Load())
			return fmt.Errorf("timeout waiting for uploads")
		case <-ticker.C:
			if s.activeUploads.Load() == 0 {
				logger.Infow("S3 storage closed",
					"uploadsSucceeded", atomic.LoadInt64(&s.stats.UploadsSucceeded),
					"uploadsFailed", atomic.LoadInt64(&s.stats.UploadsFailed))
				return nil
			}
		}
	}
}

// isRetryableError determines if an error should trigger a retry
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Add specific error checks for retryable S3 errors
	errStr := err.Error()

	// Network errors
	if contains(errStr, "connection refused", "connection reset", "broken pipe") {
		return true
	}

	// Timeout errors
	if contains(errStr, "timeout", "deadline exceeded") {
		return true
	}

	// Throttling errors
	if contains(errStr, "throttled", "rate exceeded", "slow down") {
		return true
	}

	// Service errors
	if contains(errStr, "service unavailable", "internal server error") {
		return true
	}

	return false
}

// contains checks if string contains any of the substrings
func contains(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if len(s) >= len(substr) {
			// Case-insensitive contains
			for i := 0; i <= len(s)-len(substr); i++ {
				match := true
				for j := 0; j < len(substr); j++ {
					if s[i+j] != substr[j] && s[i+j] != substr[j]-32 && s[i+j] != substr[j]+32 {
						match = false
						break
					}
				}
				if match {
					return true
				}
			}
		}
	}
	return false
}

// StoreSegment stores a segment in S3
func (s *S3Storage) StoreSegment(ctx context.Context, sessionID string, segmentName string, data []byte) error {
	key := s.buildKey(sessionID, "segments", segmentName)
	return s.uploadWithRetry(ctx, key, data, "video/MP2T")
}

// GetSegment retrieves a segment from S3
func (s *S3Storage) GetSegment(ctx context.Context, sessionID string, segmentName string) ([]byte, error) {
	key := s.buildKey(sessionID, "segments", segmentName)

	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get segment from S3: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read segment data: %w", err)
	}

	atomic.AddInt64(&s.stats.BytesDownloaded, int64(len(data)))
	return data, nil
}

// DeleteSegment deletes a segment from S3
func (s *S3Storage) DeleteSegment(ctx context.Context, sessionID string, segmentName string) error {
	key := s.buildKey(sessionID, "segments", segmentName)

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete segment from S3: %w", err)
	}

	return nil
}

// ListSegments lists all segments for a session in S3
func (s *S3Storage) ListSegments(ctx context.Context, sessionID string) ([]string, error) {
	prefix := s.buildKey(sessionID, "segments", "")

	result, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.config.Bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list segments from S3: %w", err)
	}

	var segments []string
	for _, obj := range result.Contents {
		if obj.Key != nil {
			// Extract segment name from full key
			segmentName := filepath.Base(*obj.Key)
			if strings.HasSuffix(segmentName, ".ts") {
				segments = append(segments, segmentName)
			}
		}
	}

	return segments, nil
}

// StorePlaylists stores both master and media playlists in S3
func (s *S3Storage) StorePlaylists(ctx context.Context, sessionID string, master, media []byte) error {
	// Store master playlist
	if len(master) > 0 {
		masterKey := s.buildKey(sessionID, "playlists", "master.m3u8")
		if err := s.uploadWithRetry(ctx, masterKey, master, "application/vnd.apple.mpegurl"); err != nil {
			return fmt.Errorf("failed to upload master playlist: %w", err)
		}
	}

	// Store media playlist
	if len(media) > 0 {
		mediaKey := s.buildKey(sessionID, "playlists", "media.m3u8")
		if err := s.uploadWithRetry(ctx, mediaKey, media, "application/vnd.apple.mpegurl"); err != nil {
			return fmt.Errorf("failed to upload media playlist: %w", err)
		}
	}

	return nil
}

// GetPlaylist retrieves a playlist from S3
func (s *S3Storage) GetPlaylist(ctx context.Context, sessionID string, playlistType string) ([]byte, error) {
	var filename string
	switch playlistType {
	case "master":
		filename = "master.m3u8"
	case "media":
		filename = "media.m3u8"
	default:
		return nil, fmt.Errorf("invalid playlist type: %s", playlistType)
	}

	key := s.buildKey(sessionID, "playlists", filename)

	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get playlist from S3: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read playlist data: %w", err)
	}

	atomic.AddInt64(&s.stats.BytesDownloaded, int64(len(data)))
	return data, nil
}

// StoreManifest stores a manifest file in S3
func (s *S3Storage) StoreManifest(ctx context.Context, sessionID string, manifest []byte) error {
	key := s.buildKey(sessionID, "", "manifest.json")
	return s.uploadWithRetry(ctx, key, manifest, "application/json")
}

// GetManifest retrieves a manifest from S3
func (s *S3Storage) GetManifest(ctx context.Context, sessionID string) ([]byte, error) {
	key := s.buildKey(sessionID, "", "manifest.json")

	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.config.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get manifest from S3: %w", err)
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest data: %w", err)
	}

	atomic.AddInt64(&s.stats.BytesDownloaded, int64(len(data)))
	return data, nil
}

// StoreScreenshot stores a screenshot in S3
func (s *S3Storage) StoreScreenshot(ctx context.Context, sessionID string, timestamp int64, data []byte) error {
	// Generate filename from timestamp
	filename := fmt.Sprintf("screenshot_%d.jpg", timestamp)
	key := s.buildKey(sessionID, "screenshots", filename)
	return s.uploadWithRetry(ctx, key, data, "image/jpeg")
}

// GetMetrics returns storage metrics
func (s *S3Storage) GetMetrics() StorageMetrics {
	return StorageMetrics{
		TotalUploads:       atomic.LoadInt64(&s.stats.TotalAttempts),
		SuccessfulUploads:  atomic.LoadInt64(&s.stats.UploadsSucceeded),
		FailedUploads:      atomic.LoadInt64(&s.stats.UploadsFailed),
		RetryCount:         atomic.LoadInt64(&s.stats.RetryAttempts),
		BytesUploaded:      atomic.LoadInt64(&s.stats.BytesUploaded),
		BytesDownloaded:    atomic.LoadInt64(&s.stats.BytesDownloaded),
		CircuitBreakerOpen: s.circuitBreaker != nil && !s.circuitBreaker.IsAvailable(),
	}
}

// GetStorageInfo returns storage information
func (s *S3Storage) GetStorageInfo() StorageInfo {
	return StorageInfo{
		Type:        StorageTypeS3,
		Available:   s.circuitBreaker == nil || s.circuitBreaker.IsAvailable(),
		CloudBucket: s.config.Bucket,
		Location:    fmt.Sprintf("s3://%s/%s", s.config.Bucket, s.config.Prefix),
		// S3 doesn't provide space information directly
		TotalSpace: -1,
		UsedSpace:  -1,
		FreeSpace:  -1,
	}
}

// IsHealthy checks if S3 storage is healthy
func (s *S3Storage) IsHealthy() bool {
	// Check circuit breaker status
	if s.circuitBreaker != nil && !s.circuitBreaker.IsAvailable() {
		return false
	}

	// Could perform a HEAD request to check S3 availability
	// For now, just check circuit breaker
	return true
}

// ListScreenshots lists all screenshots for a session in S3
func (s *S3Storage) ListScreenshots(ctx context.Context, sessionID string) ([]ScreenshotInfo, error) {
	prefix := s.buildKey(sessionID, "screenshots", "")

	result, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.config.Bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list screenshots from S3: %w", err)
	}

	var screenshots []ScreenshotInfo
	for _, obj := range result.Contents {
		if obj.Key != nil {
			// Extract filename from full key
			filename := filepath.Base(*obj.Key)

			// Parse timestamp from filename (format: screenshot_<timestamp>.jpg)
			parts := strings.Split(filename, "_")
			if len(parts) < 2 {
				continue
			}
			timestampStr := strings.TrimSuffix(parts[1], filepath.Ext(filename))
			var timestamp int64
			if _, err := fmt.Sscanf(timestampStr, "%d", &timestamp); err != nil {
				continue
			}

			screenshots = append(screenshots, ScreenshotInfo{
				Timestamp: timestamp,
				URL:       fmt.Sprintf("https://%s.s3.amazonaws.com/%s", s.config.Bucket, *obj.Key),
				Size:      *obj.Size,
			})
		}
	}

	return screenshots, nil
}