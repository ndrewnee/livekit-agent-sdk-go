package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/livekit/protocol/logger"
)

// LocalStorage implements local file storage with retention policies
// Implements requirements from PLAN.md Milestone 3
type LocalStorage struct {
	config *LocalConfig

	// Storage paths
	basePath   string
	bufferPath string

	// State management
	mu         sync.RWMutex
	closed     atomic.Bool
	closeOnce  sync.Once

	// Retention management
	retentionConfig *RetentionConfig
	cleanupTicker   *time.Ticker
	cleanupStop     chan struct{}

	// Statistics
	stats       LocalStorageStats
	statsTime   time.Time

	// Space management
	currentSize atomic.Int64

	// File tracking for retention
	fileRegistry map[string]*FileMetadata
	registryMu   sync.RWMutex
}

// FileMetadata tracks file information for retention
type FileMetadata struct {
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	CreatedAt    time.Time `json:"created_at"`
	LastAccessed time.Time `json:"last_accessed"`
	SessionID    string    `json:"session_id"`
	FileType     string    `json:"file_type"` // "segment", "playlist", "screenshot"
	Uploaded     bool      `json:"uploaded"`
	UploadedAt   time.Time `json:"uploaded_at,omitempty"`
}

// LocalStorageStats tracks storage statistics
type LocalStorageStats struct {
	FilesStored      int64     `json:"files_stored"`
	BytesStored      int64     `json:"bytes_stored"`
	FilesDeleted     int64     `json:"files_deleted"`
	BytesDeleted     int64     `json:"bytes_deleted"`
	FilesBuffered    int64     `json:"files_buffered"`
	WriteErrors      int64     `json:"write_errors"`
	DeleteErrors     int64     `json:"delete_errors"`
	LastCleanup      time.Time `json:"last_cleanup"`
	OldestFile       time.Time `json:"oldest_file"`
	SpaceAvailable   int64     `json:"space_available"`
}

// NewLocalStorage creates a new local storage instance
func NewLocalStorage(config *LocalConfig, retentionConfig *RetentionConfig) (*LocalStorage, error) {
	if config.Path == "" {
		return nil, fmt.Errorf("local storage path not specified")
	}

	// Ensure base path exists
	if err := os.MkdirAll(config.Path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	// Ensure buffer path exists if buffering is enabled
	if config.BufferOnFailure && config.BufferPath != "" {
		if err := os.MkdirAll(config.BufferPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create buffer directory: %w", err)
		}
	}

	ls := &LocalStorage{
		config:          config,
		basePath:        config.Path,
		bufferPath:      config.BufferPath,
		retentionConfig: retentionConfig,
		fileRegistry:    make(map[string]*FileMetadata),
		cleanupStop:     make(chan struct{}),
		statsTime:       time.Now(),
	}

	// Calculate initial storage size
	if err := ls.calculateStorageSize(); err != nil {
		logger.Warnw("failed to calculate initial storage size", err)
	}

	// Start retention cleanup if enabled
	if retentionConfig != nil && retentionConfig.Enabled {
		ls.startRetentionCleanup()
	}

	return ls, nil
}

// StoreSegment stores an HLS segment locally
func (ls *LocalStorage) StoreSegment(ctx context.Context, sessionID string, segmentName string, data []byte) error {
	if ls.closed.Load() {
		return fmt.Errorf("storage is closed")
	}

	// Check space constraints
	if ls.config.MaxSize > 0 {
		currentSize := ls.currentSize.Load()
		if currentSize+int64(len(data)) > ls.config.MaxSize {
			return fmt.Errorf("storage size limit exceeded")
		}
	}

	// Create session directory
	sessionPath := filepath.Join(ls.basePath, sessionID)
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		atomic.AddInt64(&ls.stats.WriteErrors, 1)
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	// Write segment file
	segmentPath := filepath.Join(sessionPath, segmentName)
	if err := ls.writeFile(segmentPath, data); err != nil {
		atomic.AddInt64(&ls.stats.WriteErrors, 1)
		return fmt.Errorf("failed to write segment: %w", err)
	}

	// Register file for retention tracking
	ls.registerFile(segmentPath, int64(len(data)), sessionID, "segment")

	// Update statistics
	atomic.AddInt64(&ls.stats.FilesStored, 1)
	atomic.AddInt64(&ls.stats.BytesStored, int64(len(data)))
	ls.currentSize.Add(int64(len(data)))

	logger.Debugw("stored segment",
		"sessionID", sessionID,
		"segment", segmentName,
		"size", len(data))

	return nil
}

// StorePlaylist stores an HLS playlist locally
func (ls *LocalStorage) StorePlaylist(ctx context.Context, sessionID string, playlistName string, data []byte) error {
	if ls.closed.Load() {
		return fmt.Errorf("storage is closed")
	}

	// Create session directory
	sessionPath := filepath.Join(ls.basePath, sessionID)
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		atomic.AddInt64(&ls.stats.WriteErrors, 1)
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	// Write playlist file
	playlistPath := filepath.Join(sessionPath, playlistName)
	if err := ls.writeFile(playlistPath, data); err != nil {
		atomic.AddInt64(&ls.stats.WriteErrors, 1)
		return fmt.Errorf("failed to write playlist: %w", err)
	}

	// Register file for retention tracking
	ls.registerFile(playlistPath, int64(len(data)), sessionID, "playlist")

	logger.Debugw("stored playlist",
		"sessionID", sessionID,
		"playlist", playlistName,
		"size", len(data))

	return nil
}

// StoreScreenshot stores a screenshot locally
func (ls *LocalStorage) StoreScreenshot(ctx context.Context, sessionID string, timestamp int64, data []byte) error {
	if ls.closed.Load() {
		return fmt.Errorf("storage is closed")
	}

	// Create screenshots directory
	screenshotsPath := filepath.Join(ls.basePath, "sessions", sessionID, "screenshots")
	if err := os.MkdirAll(screenshotsPath, 0755); err != nil {
		atomic.AddInt64(&ls.stats.WriteErrors, 1)
		return fmt.Errorf("failed to create screenshots directory: %w", err)
	}

	// Generate filename from timestamp
	filename := fmt.Sprintf("screenshot_%d.jpg", timestamp)

	// Write screenshot file
	screenshotPath := filepath.Join(screenshotsPath, filename)
	if err := ls.writeFile(screenshotPath, data); err != nil {
		atomic.AddInt64(&ls.stats.WriteErrors, 1)
		return fmt.Errorf("failed to write screenshot: %w", err)
	}

	// Register file for retention tracking
	ls.registerFile(screenshotPath, int64(len(data)), sessionID, "screenshot")

	logger.Debugw("stored screenshot",
		"sessionID", sessionID,
		"filename", filename,
		"size", len(data))

	return nil
}

// BufferForUpload buffers a file for later upload (during cloud outages)
func (ls *LocalStorage) BufferForUpload(ctx context.Context, sessionID string, filename string, data []byte) error {
	if !ls.config.BufferOnFailure {
		return fmt.Errorf("buffering not enabled")
	}

	if ls.bufferPath == "" {
		return fmt.Errorf("buffer path not configured")
	}

	// Check buffer size constraints
	if ls.config.MaxBufferSize > 0 {
		bufferSize := ls.getBufferSize()
		if bufferSize+int64(len(data)) > ls.config.MaxBufferSize {
			return fmt.Errorf("buffer size limit exceeded")
		}
	}

	// Create buffer directory for session
	bufferSessionPath := filepath.Join(ls.bufferPath, sessionID)
	if err := os.MkdirAll(bufferSessionPath, 0755); err != nil {
		return fmt.Errorf("failed to create buffer directory: %w", err)
	}

	// Write buffered file
	bufferFilePath := filepath.Join(bufferSessionPath, filename)
	if err := ls.writeFile(bufferFilePath, data); err != nil {
		return fmt.Errorf("failed to buffer file: %w", err)
	}

	logger.Infow("buffered file for upload",
		"sessionID", sessionID,
		"filename", filename,
		"size", len(data))

	return nil
}

// GetBufferedFiles returns list of files waiting to be uploaded
func (ls *LocalStorage) GetBufferedFiles() ([]*FileMetadata, error) {
	if ls.bufferPath == "" {
		return nil, nil
	}

	var bufferedFiles []*FileMetadata

	err := filepath.Walk(ls.bufferPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			relPath, _ := filepath.Rel(ls.bufferPath, path)
			sessionID := filepath.Dir(relPath)

			bufferedFiles = append(bufferedFiles, &FileMetadata{
				Path:      path,
				Size:      info.Size(),
				CreatedAt: info.ModTime(),
				SessionID: sessionID,
				FileType:  "buffered",
			})
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list buffered files: %w", err)
	}

	return bufferedFiles, nil
}

// RetrieveFile retrieves a file from local storage
func (ls *LocalStorage) RetrieveFile(ctx context.Context, path string) ([]byte, error) {
	if ls.closed.Load() {
		return nil, fmt.Errorf("storage is closed")
	}

	fullPath := filepath.Join(ls.basePath, path)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Update last accessed time
	ls.registryMu.Lock()
	if meta, exists := ls.fileRegistry[fullPath]; exists {
		meta.LastAccessed = time.Now()
	}
	ls.registryMu.Unlock()

	return data, nil
}

// MarkUploaded marks a file as successfully uploaded
func (ls *LocalStorage) MarkUploaded(path string) {
	ls.registryMu.Lock()
	defer ls.registryMu.Unlock()

	if meta, exists := ls.fileRegistry[path]; exists {
		meta.Uploaded = true
		meta.UploadedAt = time.Now()

		// Delete after upload if configured
		if ls.retentionConfig != nil && ls.retentionConfig.DeleteAfterUpload {
			go ls.deleteFile(path)
		}
	}
}

// writeFile writes data to a file atomically
func (ls *LocalStorage) writeFile(path string, data []byte) error {
	// Write to temporary file first
	tmpPath := path + ".tmp"

	file, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}

	_, err = file.Write(data)
	if err != nil {
		file.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write data: %w", err)
	}

	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename file: %w", err)
	}

	return nil
}

// registerFile registers a file for retention tracking
func (ls *LocalStorage) registerFile(path string, size int64, sessionID string, fileType string) {
	ls.registryMu.Lock()
	defer ls.registryMu.Unlock()

	now := time.Now()
	ls.fileRegistry[path] = &FileMetadata{
		Path:         path,
		Size:         size,
		CreatedAt:    now,
		LastAccessed: now,
		SessionID:    sessionID,
		FileType:     fileType,
	}
}

// startRetentionCleanup starts the retention cleanup routine
func (ls *LocalStorage) startRetentionCleanup() {
	if ls.retentionConfig.CleanupInterval <= 0 {
		ls.retentionConfig.CleanupInterval = 1 * time.Hour
	}

	ls.cleanupTicker = time.NewTicker(ls.retentionConfig.CleanupInterval)

	go func() {
		for {
			select {
			case <-ls.cleanupTicker.C:
				ls.performCleanup()
			case <-ls.cleanupStop:
				return
			}
		}
	}()

	logger.Infow("started retention cleanup",
		"interval", ls.retentionConfig.CleanupInterval,
		"localHours", ls.retentionConfig.LocalHours)
}

// performCleanup performs retention cleanup
func (ls *LocalStorage) performCleanup() {
	if ls.closed.Load() {
		return
	}

	logger.Debugw("performing retention cleanup")

	ls.registryMu.RLock()
	files := make([]*FileMetadata, 0, len(ls.fileRegistry))
	for _, meta := range ls.fileRegistry {
		files = append(files, meta)
	}
	ls.registryMu.RUnlock()

	// Sort by creation time (oldest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].CreatedAt.Before(files[j].CreatedAt)
	})

	now := time.Now()
	retentionDuration := time.Duration(ls.retentionConfig.LocalHours) * time.Hour

	var (
		deletedCount int64
		deletedBytes int64
		segmentCount = make(map[string]int)
	)

	// Count segments per session for minimum retention
	for _, file := range files {
		if file.FileType == "segment" {
			segmentCount[file.SessionID]++
		}
	}

	for _, file := range files {
		// Skip if within retention period
		if now.Sub(file.CreatedAt) < retentionDuration {
			continue
		}

		// Skip if uploaded and DeleteAfterUpload is false
		if file.Uploaded && !ls.retentionConfig.DeleteAfterUpload {
			continue
		}

		// Check minimum segments constraint
		if file.FileType == "segment" {
			if count := segmentCount[file.SessionID]; count <= ls.retentionConfig.MinSegments {
				continue
			}
		}

		// Delete the file
		if err := ls.deleteFile(file.Path); err != nil {
			logger.Errorw("failed to delete file during cleanup", err,
				"path", file.Path)
			atomic.AddInt64(&ls.stats.DeleteErrors, 1)
		} else {
			deletedCount++
			deletedBytes += file.Size

			// Update segment count
			if file.FileType == "segment" {
				segmentCount[file.SessionID]--
			}
		}
	}

	if deletedCount > 0 {
		atomic.AddInt64(&ls.stats.FilesDeleted, deletedCount)
		atomic.AddInt64(&ls.stats.BytesDeleted, deletedBytes)
		ls.currentSize.Add(-deletedBytes)

		logger.Infow("retention cleanup completed",
			"filesDeleted", deletedCount,
			"bytesDeleted", deletedBytes)
	}

	ls.stats.LastCleanup = now
}

// deleteFile deletes a file and updates registry
func (ls *LocalStorage) deleteFile(path string) error {
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}

	// Remove from registry
	ls.registryMu.Lock()
	delete(ls.fileRegistry, path)
	ls.registryMu.Unlock()

	return nil
}

// calculateStorageSize calculates total storage size
func (ls *LocalStorage) calculateStorageSize() error {
	var totalSize int64

	err := filepath.Walk(ls.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	if err != nil {
		return err
	}

	ls.currentSize.Store(totalSize)
	return nil
}

// getBufferSize calculates buffer directory size
func (ls *LocalStorage) getBufferSize() int64 {
	if ls.bufferPath == "" {
		return 0
	}

	var totalSize int64

	filepath.Walk(ls.bufferPath, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})

	return totalSize
}

// GetStats returns storage statistics
func (ls *LocalStorage) GetStats() LocalStorageStats {
	ls.mu.RLock()
	defer ls.mu.RUnlock()

	stats := ls.stats
	stats.BytesStored = ls.currentSize.Load()

	// Find oldest file
	ls.registryMu.RLock()
	var oldestTime time.Time
	for _, meta := range ls.fileRegistry {
		if oldestTime.IsZero() || meta.CreatedAt.Before(oldestTime) {
			oldestTime = meta.CreatedAt
		}
	}
	ls.registryMu.RUnlock()

	stats.OldestFile = oldestTime
	stats.FilesStored = int64(len(ls.fileRegistry))

	// Get available space
	if stat, err := os.Stat(ls.basePath); err == nil {
		if statfs, ok := stat.Sys().(*os.FileInfo); ok {
			_ = statfs // Space calculation is OS-specific
		}
	}

	return stats
}

// ListSessions lists all recording sessions in storage
func (ls *LocalStorage) ListSessions() ([]string, error) {
	entries, err := os.ReadDir(ls.basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	var sessions []string
	for _, entry := range entries {
		if entry.IsDir() {
			sessions = append(sessions, entry.Name())
		}
	}

	return sessions, nil
}

// GetSessionFiles returns all files for a session
func (ls *LocalStorage) GetSessionFiles(sessionID string) ([]*FileMetadata, error) {
	ls.registryMu.RLock()
	defer ls.registryMu.RUnlock()

	var files []*FileMetadata
	for _, meta := range ls.fileRegistry {
		if meta.SessionID == sessionID {
			files = append(files, meta)
		}
	}

	// Sort by creation time
	sort.Slice(files, func(i, j int) bool {
		return files[i].CreatedAt.Before(files[j].CreatedAt)
	})

	return files, nil
}

// Close closes the local storage
func (ls *LocalStorage) Close() error {
	ls.closeOnce.Do(func() {
		ls.closed.Store(true)

		// Stop cleanup routine
		if ls.cleanupTicker != nil {
			ls.cleanupTicker.Stop()
			close(ls.cleanupStop)
		}

		// Final cleanup
		if ls.retentionConfig != nil && ls.retentionConfig.Enabled {
			ls.performCleanup()
		}

		logger.Infow("local storage closed",
			"filesStored", atomic.LoadInt64(&ls.stats.FilesStored),
			"bytesStored", atomic.LoadInt64(&ls.stats.BytesStored))
	})

	return nil
}

// StreamFile streams a file from storage
func (ls *LocalStorage) StreamFile(path string, w io.Writer) error {
	fullPath := filepath.Join(ls.basePath, path)

	file, err := os.Open(fullPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	if _, err := io.Copy(w, file); err != nil {
		return fmt.Errorf("failed to stream file: %w", err)
	}

	return nil
}

// GetSegment retrieves a segment from storage
func (ls *LocalStorage) GetSegment(ctx context.Context, sessionID string, segmentName string) ([]byte, error) {
	segmentPath := filepath.Join(ls.basePath, "sessions", sessionID, "segments", segmentName)

	data, err := os.ReadFile(segmentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("segment not found: %s", segmentName)
		}
		return nil, fmt.Errorf("failed to read segment: %w", err)
	}

	// Track bytes retrieved (could add BytesRead field if needed)
	return data, nil
}

// DeleteSegment deletes a segment from storage
func (ls *LocalStorage) DeleteSegment(ctx context.Context, sessionID string, segmentName string) error {
	segmentPath := filepath.Join(ls.basePath, "sessions", sessionID, "segments", segmentName)

	if err := os.Remove(segmentPath); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete segment: %w", err)
	}

	atomic.AddInt64(&ls.stats.FilesDeleted, 1)
	return nil
}

// ListSegments lists all segments for a session
func (ls *LocalStorage) ListSegments(ctx context.Context, sessionID string) ([]string, error) {
	segmentsPath := filepath.Join(ls.basePath, "sessions", sessionID, "segments")

	entries, err := os.ReadDir(segmentsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to list segments: %w", err)
	}

	var segments []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ts") {
			segments = append(segments, entry.Name())
		}
	}

	return segments, nil
}

// StorePlaylists stores both master and media playlists
func (ls *LocalStorage) StorePlaylists(ctx context.Context, sessionID string, master, media []byte) error {
	playlistsPath := filepath.Join(ls.basePath, "sessions", sessionID, "playlists")

	// Create playlists directory
	if err := os.MkdirAll(playlistsPath, 0755); err != nil {
		return fmt.Errorf("failed to create playlists directory: %w", err)
	}

	// Store master playlist
	if len(master) > 0 {
		masterPath := filepath.Join(playlistsPath, "master.m3u8")
		if err := ls.writeFile(masterPath, master); err != nil {
			return fmt.Errorf("failed to write master playlist: %w", err)
		}
		ls.registerFile(masterPath, int64(len(master)), sessionID, "playlist")
	}

	// Store media playlist
	if len(media) > 0 {
		mediaPath := filepath.Join(playlistsPath, "media.m3u8")
		if err := ls.writeFile(mediaPath, media); err != nil {
			return fmt.Errorf("failed to write media playlist: %w", err)
		}
		ls.registerFile(mediaPath, int64(len(media)), sessionID, "playlist")
	}

	return nil
}

// GetPlaylist retrieves a playlist from storage
func (ls *LocalStorage) GetPlaylist(ctx context.Context, sessionID string, playlistType string) ([]byte, error) {
	var filename string
	switch playlistType {
	case "master":
		filename = "master.m3u8"
	case "media":
		filename = "media.m3u8"
	default:
		return nil, fmt.Errorf("invalid playlist type: %s", playlistType)
	}

	playlistPath := filepath.Join(ls.basePath, "sessions", sessionID, "playlists", filename)

	data, err := os.ReadFile(playlistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("playlist not found: %s", filename)
		}
		return nil, fmt.Errorf("failed to read playlist: %w", err)
	}

	// Track bytes retrieved (could add BytesRead field if needed)
	return data, nil
}

// StoreManifest stores a manifest file
func (ls *LocalStorage) StoreManifest(ctx context.Context, sessionID string, manifest []byte) error {
	manifestPath := filepath.Join(ls.basePath, "sessions", sessionID, "manifest.json")

	// Create session directory if needed
	sessionPath := filepath.Join(ls.basePath, "sessions", sessionID)
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	if err := ls.writeFile(manifestPath, manifest); err != nil {
		return fmt.Errorf("failed to write manifest: %w", err)
	}

	ls.registerFile(manifestPath, int64(len(manifest)), sessionID, "manifest")
	return nil
}

// GetManifest retrieves a manifest from storage
func (ls *LocalStorage) GetManifest(ctx context.Context, sessionID string) ([]byte, error) {
	manifestPath := filepath.Join(ls.basePath, "sessions", sessionID, "manifest.json")

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("manifest not found for session: %s", sessionID)
		}
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	atomic.AddInt64(&ls.stats.BytesStored, int64(len(data)))
	return data, nil
}

// GetMetrics returns storage metrics
func (ls *LocalStorage) GetMetrics() StorageMetrics {
	return StorageMetrics{
		TotalUploads:      atomic.LoadInt64(&ls.stats.FilesStored),
		SuccessfulUploads: atomic.LoadInt64(&ls.stats.FilesStored) - atomic.LoadInt64(&ls.stats.WriteErrors),
		FailedUploads:     atomic.LoadInt64(&ls.stats.WriteErrors),
		BytesUploaded:     atomic.LoadInt64(&ls.stats.BytesStored),
		BufferedSegments:  int(atomic.LoadInt64(&ls.stats.FilesBuffered)),
	}
}

// GetStorageInfo returns storage information
func (ls *LocalStorage) GetStorageInfo() StorageInfo {
	var stat syscall.Statfs_t
	err := syscall.Statfs(ls.basePath, &stat)

	info := StorageInfo{
		Type:      StorageTypeLocal,
		Available: err == nil,
		Location:  ls.basePath,
	}

	if err == nil {
		// Calculate space in bytes
		info.TotalSpace = int64(stat.Blocks) * int64(stat.Bsize)
		info.FreeSpace = int64(stat.Bavail) * int64(stat.Bsize)
		info.UsedSpace = info.TotalSpace - info.FreeSpace
	}

	return info
}

// IsHealthy checks if storage is healthy
func (ls *LocalStorage) IsHealthy() bool {
	// Check if base path exists and is writable
	testFile := filepath.Join(ls.basePath, ".health_check")

	// Try to write a test file
	if err := os.WriteFile(testFile, []byte("health_check"), 0644); err != nil {
		return false
	}

	// Clean up test file
	os.Remove(testFile)

	// Check if we have sufficient space (at least 100MB free)
	info := ls.GetStorageInfo()
	if info.FreeSpace < 100*1024*1024 {
		return false
	}

	return true
}

// ListScreenshots lists all screenshots for a session
func (ls *LocalStorage) ListScreenshots(ctx context.Context, sessionID string) ([]ScreenshotInfo, error) {
	screenshotsPath := filepath.Join(ls.basePath, "sessions", sessionID, "screenshots")

	entries, err := os.ReadDir(screenshotsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []ScreenshotInfo{}, nil
		}
		return nil, fmt.Errorf("failed to list screenshots: %w", err)
	}

	var screenshots []ScreenshotInfo
	for _, entry := range entries {
		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".jpg") || strings.HasSuffix(entry.Name(), ".png")) {
			info, err := entry.Info()
			if err != nil {
				continue
			}

			// Parse timestamp from filename (format: screenshot_<timestamp>.jpg)
			parts := strings.Split(entry.Name(), "_")
			if len(parts) < 2 {
				continue
			}
			timestampStr := strings.TrimSuffix(parts[1], filepath.Ext(entry.Name()))
			var timestamp int64
			if _, err := fmt.Sscanf(timestampStr, "%d", &timestamp); err != nil {
				continue
			}

			screenshots = append(screenshots, ScreenshotInfo{
				Timestamp: timestamp,
				URL:       filepath.Join(screenshotsPath, entry.Name()),
				Size:      info.Size(),
			})
		}
	}

	return screenshots, nil
}