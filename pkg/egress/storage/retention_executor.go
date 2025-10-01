package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/logger"
)

// RetentionExecutor manages retention policies across storage backends
// Implements retention policy requirements from PLAN.md Milestone 3
type RetentionExecutor struct {
	logger  logger.Logger
	config  *RetentionConfig
	storage Storage

	// State
	mu       sync.RWMutex
	running  bool
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Metrics
	metrics RetentionMetrics

	// Session tracking
	sessions map[string]*SessionRetention
}

// RetentionMetrics tracks retention executor performance
type RetentionMetrics struct {
	TotalScans         int64
	TotalDeleted       int64
	TotalRetained      int64
	BytesReclaimed     int64
	LastCleanupTime    time.Time
	LastCleanupDuration time.Duration
	ErrorCount         int64
	ActiveSessions     int64
}

// SessionRetention tracks retention state for a session
type SessionRetention struct {
	SessionID       string
	StartTime       time.Time
	LastUpdate      time.Time
	SegmentCount    int
	TotalSize       int64
	UploadedCount   int
	DeletedCount    int
	RetainUntil     time.Time
	Priority        int // Higher priority sessions retained longer
}

// NewRetentionExecutor creates a new retention executor
func NewRetentionExecutor(storage Storage, config *RetentionConfig, logger logger.Logger) *RetentionExecutor {
	return &RetentionExecutor{
		logger:   logger,
		config:   config,
		storage:  storage,
		stopChan: make(chan struct{}),
		sessions: make(map[string]*SessionRetention),
	}
}

// Start starts the retention executor
func (re *RetentionExecutor) Start() error {
	re.mu.Lock()
	defer re.mu.Unlock()

	if re.running {
		return fmt.Errorf("retention executor already running")
	}

	if !re.config.Enabled {
		return fmt.Errorf("retention policies not enabled")
	}

	re.running = true

	// Start cleanup worker
	re.wg.Add(1)
	go re.cleanupWorker()

	// Start metrics collector
	re.wg.Add(1)
	go re.metricsCollector()

	re.logger.Infow("retention executor started",
		"local_hours", re.config.LocalHours,
		"cloud_hours", re.config.CloudHours,
		"cleanup_interval", re.config.CleanupInterval,
		"delete_after_upload", re.config.DeleteAfterUpload)

	return nil
}

// Stop stops the retention executor
func (re *RetentionExecutor) Stop() error {
	re.mu.Lock()
	if !re.running {
		re.mu.Unlock()
		return nil
	}

	re.running = false
	close(re.stopChan)
	re.mu.Unlock()

	// Wait for workers to finish
	done := make(chan struct{})
	go func() {
		re.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		re.logger.Infow("retention executor stopped gracefully")
	case <-time.After(30 * time.Second):
		re.logger.Warnw("retention executor stop timeout", nil)
	}

	return nil
}

// cleanupWorker performs periodic cleanup based on retention policies
func (re *RetentionExecutor) cleanupWorker() {
	defer re.wg.Done()

	ticker := time.NewTicker(re.config.CleanupInterval)
	defer ticker.Stop()

	// Run initial cleanup after startup delay
	time.Sleep(1 * time.Minute)

	for {
		select {
		case <-re.stopChan:
			return

		case <-ticker.C:
			re.performCleanup()
		}
	}
}

// performCleanup executes retention policy cleanup
func (re *RetentionExecutor) performCleanup() {
	startTime := time.Now()
	atomic.AddInt64(&re.metrics.TotalScans, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	re.logger.Debugw("starting retention cleanup")

	// Get all sessions
	sessions := re.getActiveSessions()

	var totalDeleted int64
	var totalRetained int64
	var bytesReclaimed int64

	for _, session := range sessions {
		deleted, retained, bytes := re.cleanupSession(ctx, session)
		totalDeleted += deleted
		totalRetained += retained
		bytesReclaimed += bytes
	}

	// Update metrics
	atomic.AddInt64(&re.metrics.TotalDeleted, totalDeleted)
	atomic.AddInt64(&re.metrics.TotalRetained, totalRetained)
	atomic.AddInt64(&re.metrics.BytesReclaimed, bytesReclaimed)

	re.mu.Lock()
	re.metrics.LastCleanupTime = time.Now()
	re.metrics.LastCleanupDuration = time.Since(startTime)
	re.mu.Unlock()

	re.logger.Infow("retention cleanup completed",
		"duration", time.Since(startTime),
		"deleted", totalDeleted,
		"retained", totalRetained,
		"bytes_reclaimed", bytesReclaimed,
		"sessions_processed", len(sessions))
}

// cleanupSession cleans up a single session based on retention policy
func (re *RetentionExecutor) cleanupSession(ctx context.Context, session *SessionRetention) (deleted, retained, bytesReclaimed int64) {
	// Check if session should be retained
	if time.Now().Before(session.RetainUntil) {
		re.logger.Debugw("session within retention period",
			"session_id", session.SessionID,
			"retain_until", session.RetainUntil)
		return 0, int64(session.SegmentCount), 0
	}

	// Get segments for session
	segments, err := re.storage.ListSegments(ctx, session.SessionID)
	if err != nil {
		re.logger.Errorw("failed to list segments for cleanup", err,
			"session_id", session.SessionID)
		atomic.AddInt64(&re.metrics.ErrorCount, 1)
		return 0, 0, 0
	}

	// Apply retention rules
	for _, segment := range segments {
		shouldDelete := re.shouldDeleteSegment(session, segment)

		if shouldDelete {
			if err := re.deleteSegment(ctx, session.SessionID, segment); err != nil {
				re.logger.Errorw("failed to delete segment", err,
					"session_id", session.SessionID,
					"segment", segment)
				atomic.AddInt64(&re.metrics.ErrorCount, 1)
			} else {
				deleted++
				// Estimate size (would need actual size tracking in production)
				bytesReclaimed += estimateSegmentSize(segment)
			}
		} else {
			retained++
		}
	}

	// Update session state
	session.DeletedCount += int(deleted)
	session.SegmentCount = int(retained)

	// Remove session if all segments deleted
	if retained == 0 {
		re.removeSession(session.SessionID)
	}

	return deleted, retained, bytesReclaimed
}

// shouldDeleteSegment determines if a segment should be deleted
func (re *RetentionExecutor) shouldDeleteSegment(session *SessionRetention, segment string) bool {
	// Keep minimum segments
	if session.SegmentCount <= re.config.MinSegments {
		return false
	}

	// Keep if session duration is below minimum
	sessionDuration := time.Since(session.StartTime)
	if sessionDuration < time.Duration(re.config.MinDuration)*time.Second {
		return false
	}

	// Check file type specific rules
	if isPlaylistFile(segment) {
		// Keep playlists longer
		return false
	}

	if isManifestFile(segment) {
		// Always keep manifests
		return false
	}

	// Check age-based retention
	segmentAge := time.Since(session.LastUpdate) // Approximation
	localRetention := time.Duration(re.config.LocalHours) * time.Hour

	if localRetention > 0 && segmentAge > localRetention {
		return true
	}

	// Check if uploaded and delete_after_upload is enabled
	if re.config.DeleteAfterUpload && session.UploadedCount > 0 {
		// Check if this segment was uploaded (would need tracking)
		return true
	}

	return false
}

// deleteSegment deletes a segment from storage
func (re *RetentionExecutor) deleteSegment(ctx context.Context, sessionID, segment string) error {
	return re.storage.DeleteSegment(ctx, sessionID, segment)
}

// RegisterSession registers a new session for retention tracking
func (re *RetentionExecutor) RegisterSession(sessionID string, priority int) {
	re.mu.Lock()
	defer re.mu.Unlock()

	retentionHours := re.config.LocalHours
	if priority > 1 {
		// Higher priority sessions get longer retention
		retentionHours = retentionHours * priority
	}

	re.sessions[sessionID] = &SessionRetention{
		SessionID:   sessionID,
		StartTime:   time.Now(),
		LastUpdate:  time.Now(),
		Priority:    priority,
		RetainUntil: time.Now().Add(time.Duration(retentionHours) * time.Hour),
	}

	atomic.AddInt64(&re.metrics.ActiveSessions, 1)

	re.logger.Debugw("session registered for retention",
		"session_id", sessionID,
		"priority", priority,
		"retain_until", re.sessions[sessionID].RetainUntil)
}

// UpdateSession updates session retention metadata
func (re *RetentionExecutor) UpdateSession(sessionID string, segmentCount int, totalSize int64, uploadedCount int) {
	re.mu.Lock()
	defer re.mu.Unlock()

	session, exists := re.sessions[sessionID]
	if !exists {
		// Auto-register if not exists
		re.sessions[sessionID] = &SessionRetention{
			SessionID:    sessionID,
			StartTime:    time.Now(),
			LastUpdate:   time.Now(),
			SegmentCount: segmentCount,
			TotalSize:    totalSize,
			UploadedCount: uploadedCount,
			RetainUntil:  time.Now().Add(time.Duration(re.config.LocalHours) * time.Hour),
		}
	} else {
		session.LastUpdate = time.Now()
		session.SegmentCount = segmentCount
		session.TotalSize = totalSize
		session.UploadedCount = uploadedCount
	}
}

// UnregisterSession removes a session from retention tracking
func (re *RetentionExecutor) UnregisterSession(sessionID string) {
	re.removeSession(sessionID)
}

// removeSession removes a session from tracking
func (re *RetentionExecutor) removeSession(sessionID string) {
	re.mu.Lock()
	defer re.mu.Unlock()

	if _, exists := re.sessions[sessionID]; exists {
		delete(re.sessions, sessionID)
		atomic.AddInt64(&re.metrics.ActiveSessions, -1)

		re.logger.Debugw("session removed from retention tracking",
			"session_id", sessionID)
	}
}

// getActiveSessions returns all active sessions
func (re *RetentionExecutor) getActiveSessions() []*SessionRetention {
	re.mu.RLock()
	defer re.mu.RUnlock()

	sessions := make([]*SessionRetention, 0, len(re.sessions))
	for _, session := range re.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// ExtendRetention extends the retention period for a session
func (re *RetentionExecutor) ExtendRetention(sessionID string, additionalHours int) error {
	re.mu.Lock()
	defer re.mu.Unlock()

	session, exists := re.sessions[sessionID]
	if !exists {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.RetainUntil = session.RetainUntil.Add(time.Duration(additionalHours) * time.Hour)

	re.logger.Infow("retention extended",
		"session_id", sessionID,
		"additional_hours", additionalHours,
		"new_retain_until", session.RetainUntil)

	return nil
}

// ForceCleanup forces immediate cleanup for a session
func (re *RetentionExecutor) ForceCleanup(sessionID string) error {
	re.mu.Lock()
	session, exists := re.sessions[sessionID]
	if !exists {
		re.mu.Unlock()
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Mark for immediate deletion
	session.RetainUntil = time.Now().Add(-1 * time.Hour)
	re.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	deleted, retained, bytes := re.cleanupSession(ctx, session)

	re.logger.Infow("forced cleanup completed",
		"session_id", sessionID,
		"deleted", deleted,
		"retained", retained,
		"bytes_reclaimed", bytes)

	return nil
}

// metricsCollector periodically collects and logs metrics
func (re *RetentionExecutor) metricsCollector() {
	defer re.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-re.stopChan:
			return

		case <-ticker.C:
			metrics := re.GetMetrics()
			re.logger.Infow("retention executor metrics",
				"total_scans", metrics.TotalScans,
				"total_deleted", metrics.TotalDeleted,
				"total_retained", metrics.TotalRetained,
				"bytes_reclaimed", metrics.BytesReclaimed,
				"active_sessions", metrics.ActiveSessions,
				"error_count", metrics.ErrorCount,
				"last_cleanup", metrics.LastCleanupTime,
				"last_duration", metrics.LastCleanupDuration)
		}
	}
}

// GetMetrics returns retention executor metrics
func (re *RetentionExecutor) GetMetrics() RetentionMetrics {
	re.mu.RLock()
	defer re.mu.RUnlock()

	return re.metrics
}

// GetSessionInfo returns information about a specific session
func (re *RetentionExecutor) GetSessionInfo(sessionID string) (*SessionRetention, error) {
	re.mu.RLock()
	defer re.mu.RUnlock()

	session, exists := re.sessions[sessionID]
	if !exists {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	// Return a copy
	sessionCopy := *session
	return &sessionCopy, nil
}

// SetPolicy updates retention policy configuration
func (re *RetentionExecutor) SetPolicy(config *RetentionConfig) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	re.mu.Lock()
	defer re.mu.Unlock()

	re.config = config

	re.logger.Infow("retention policy updated",
		"local_hours", config.LocalHours,
		"cloud_hours", config.CloudHours,
		"delete_after_upload", config.DeleteAfterUpload)

	return nil
}

// Helper functions

// isPlaylistFile checks if a file is an HLS playlist
func isPlaylistFile(filename string) bool {
	return strings.HasSuffix(filename, ".m3u8")
}

// isManifestFile checks if a file is a manifest
func isManifestFile(filename string) bool {
	return strings.HasSuffix(filename, "manifest.json")
}

// estimateSegmentSize estimates segment size based on filename
func estimateSegmentSize(filename string) int64 {
	// In production, this would track actual sizes
	ext := filepath.Ext(filename)
	switch ext {
	case ".ts":
		return 2 * 1024 * 1024 // Estimate 2MB for TS segments
	case ".m3u8":
		return 4 * 1024 // Estimate 4KB for playlists
	case ".jpg", ".jpeg":
		return 100 * 1024 // Estimate 100KB for screenshots
	default:
		return 1024 * 1024 // Default 1MB
	}
}