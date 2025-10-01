package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/logger"
)

// BufferManager manages local buffering during cloud storage outages
// Implements local buffering requirements from PLAN.md Milestone 3
type BufferManager struct {
	logger       logger.Logger
	config       *LocalConfig
	localStorage Storage
	cloudStorage Storage

	// Buffer tracking
	mu            sync.RWMutex
	bufferedItems map[string]*BufferedItem
	bufferQueue   chan *BufferedItem

	// State
	running    bool
	stopChan   chan struct{}
	wg         sync.WaitGroup

	// Metrics
	metrics BufferMetrics
}

// BufferedItem represents an item buffered locally
type BufferedItem struct {
	ID           string
	SessionID    string
	ItemType     BufferItemType
	Name         string
	Data         []byte
	Timestamp    time.Time
	RetryCount   int
	Priority     int
	LastAttempt  time.Time
	Error        error
}

// BufferItemType represents the type of buffered item
type BufferItemType int

const (
	BufferItemSegment BufferItemType = iota
	BufferItemPlaylist
	BufferItemManifest
	BufferItemScreenshot
)

// BufferMetrics tracks buffer manager performance
type BufferMetrics struct {
	TotalBuffered      int64
	CurrentBuffered    int64
	TotalFlushed       int64
	TotalFailed        int64
	BufferSize         int64
	MaxBufferSize      int64
	OldestBufferedTime time.Time
	LastFlushTime      time.Time
	LastError          error
	LastErrorTime      time.Time
}

// BufferState represents the state of a buffered session
type BufferState struct {
	SessionID      string    `json:"session_id"`
	ItemCount      int       `json:"item_count"`
	TotalSize      int64     `json:"total_size"`
	OldestItem     time.Time `json:"oldest_item"`
	LastUpdate     time.Time `json:"last_update"`
	BufferedItems  []string  `json:"buffered_items"`
}

// NewBufferManager creates a new buffer manager
func NewBufferManager(localStorage, cloudStorage Storage, config *LocalConfig, logger logger.Logger) *BufferManager {
	return &BufferManager{
		logger:        logger,
		config:        config,
		localStorage:  localStorage,
		cloudStorage:  cloudStorage,
		bufferedItems: make(map[string]*BufferedItem),
		bufferQueue:   make(chan *BufferedItem, 1000),
		stopChan:      make(chan struct{}),
		metrics: BufferMetrics{
			MaxBufferSize: config.MaxBufferSize,
		},
	}
}

// Start starts the buffer manager
func (bm *BufferManager) Start() error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.running {
		return fmt.Errorf("buffer manager already running")
	}

	if !bm.config.BufferOnFailure {
		return fmt.Errorf("buffering not enabled")
	}

	bm.running = true

	// Create buffer directory if needed
	if err := bm.ensureBufferDirectory(); err != nil {
		bm.running = false
		return fmt.Errorf("failed to create buffer directory: %w", err)
	}

	// Load existing buffer state
	if err := bm.loadBufferState(); err != nil {
		bm.logger.Warnw("failed to load buffer state", err)
	}

	// Start flush worker
	bm.wg.Add(1)
	go bm.flushWorker()

	// Start metrics collector
	bm.wg.Add(1)
	go bm.metricsCollector()

	bm.logger.Infow("buffer manager started",
		"buffer_path", bm.config.BufferPath,
		"max_size", bm.config.MaxBufferSize)

	return nil
}

// Stop stops the buffer manager
func (bm *BufferManager) Stop() error {
	bm.mu.Lock()
	if !bm.running {
		bm.mu.Unlock()
		return nil
	}

	bm.running = false
	close(bm.stopChan)
	bm.mu.Unlock()

	// Save buffer state before stopping
	if err := bm.saveBufferState(); err != nil {
		bm.logger.Errorw("failed to save buffer state", err)
	}

	// Wait for workers
	done := make(chan struct{})
	go func() {
		bm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		bm.logger.Infow("buffer manager stopped gracefully", nil)
	case <-time.After(30 * time.Second):
		bm.logger.Warnw("buffer manager stop timeout", nil)
	}

	return nil
}

// BufferSegment buffers a segment locally during cloud storage failure
func (bm *BufferManager) BufferSegment(sessionID, segmentName string, data []byte) error {
	return bm.bufferItem(&BufferedItem{
		ID:        fmt.Sprintf("%s-%s", sessionID, segmentName),
		SessionID: sessionID,
		ItemType:  BufferItemSegment,
		Name:      segmentName,
		Data:      data,
		Timestamp: time.Now(),
		Priority:  1,
	})
}

// BufferPlaylist buffers a playlist locally
func (bm *BufferManager) BufferPlaylist(sessionID string, playlistType string, data []byte) error {
	return bm.bufferItem(&BufferedItem{
		ID:        fmt.Sprintf("%s-%s", sessionID, playlistType),
		SessionID: sessionID,
		ItemType:  BufferItemPlaylist,
		Name:      playlistType,
		Data:      data,
		Timestamp: time.Now(),
		Priority:  2, // Higher priority for playlists
	})
}

// BufferManifest buffers a manifest locally
func (bm *BufferManager) BufferManifest(sessionID string, data []byte) error {
	return bm.bufferItem(&BufferedItem{
		ID:        fmt.Sprintf("%s-manifest", sessionID),
		SessionID: sessionID,
		ItemType:  BufferItemManifest,
		Name:      "manifest",
		Data:      data,
		Timestamp: time.Now(),
		Priority:  3, // Highest priority for manifests
	})
}

// BufferScreenshot buffers a screenshot locally
func (bm *BufferManager) BufferScreenshot(sessionID string, timestamp int64, data []byte) error {
	return bm.bufferItem(&BufferedItem{
		ID:        fmt.Sprintf("%s-screenshot-%d", sessionID, timestamp),
		SessionID: sessionID,
		ItemType:  BufferItemScreenshot,
		Name:      fmt.Sprintf("screenshot_%d.jpg", timestamp),
		Data:      data,
		Timestamp: time.Now(),
		Priority:  0, // Lowest priority for screenshots
	})
}

// bufferItem adds an item to the buffer
func (bm *BufferManager) bufferItem(item *BufferedItem) error {
	if !bm.isRunning() {
		return fmt.Errorf("buffer manager not running")
	}

	// Check buffer size limit
	currentSize := atomic.LoadInt64(&bm.metrics.BufferSize)
	itemSize := int64(len(item.Data))

	if currentSize+itemSize > bm.config.MaxBufferSize {
		return fmt.Errorf("buffer full: current %d + new %d > max %d",
			currentSize, itemSize, bm.config.MaxBufferSize)
	}

	// Store to local buffer
	bufferPath := filepath.Join(bm.config.BufferPath, item.SessionID, item.Name)
	if err := bm.writeBufferFile(bufferPath, item.Data); err != nil {
		return fmt.Errorf("failed to write buffer file: %w", err)
	}

	// Track buffered item
	bm.mu.Lock()
	bm.bufferedItems[item.ID] = item
	bm.mu.Unlock()

	// Update metrics
	atomic.AddInt64(&bm.metrics.TotalBuffered, 1)
	atomic.AddInt64(&bm.metrics.CurrentBuffered, 1)
	atomic.AddInt64(&bm.metrics.BufferSize, itemSize)

	// Queue for flush
	select {
	case bm.bufferQueue <- item:
		bm.logger.Debugw("item buffered",
			"session_id", item.SessionID,
			"type", item.ItemType,
			"name", item.Name,
			"size", itemSize)
	default:
		bm.logger.Warnw("buffer queue full, item will be flushed later", nil)
	}

	return nil
}

// flushWorker attempts to flush buffered items to cloud storage
func (bm *BufferManager) flushWorker() {
	defer bm.wg.Done()

	ticker := time.NewTicker(30 * time.Second) // Retry interval
	defer ticker.Stop()

	for {
		select {
		case <-bm.stopChan:
			// Flush remaining items before stopping
			bm.flushAll()
			return

		case item := <-bm.bufferQueue:
			if item != nil {
				bm.attemptFlush(item)
			}

		case <-ticker.C:
			// Periodic retry of failed items
			bm.retryFailedItems()
		}
	}
}

// attemptFlush attempts to flush a buffered item to cloud storage
func (bm *BufferManager) attemptFlush(item *BufferedItem) {
	// Check if cloud storage is available
	if bm.cloudStorage == nil || !bm.cloudStorage.IsHealthy() {
		// Re-queue for later
		time.AfterFunc(1*time.Minute, func() {
			select {
			case bm.bufferQueue <- item:
			default:
				bm.logger.Warnw("failed to re-queue item", nil)
			}
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error

	// Upload based on item type
	switch item.ItemType {
	case BufferItemSegment:
		err = bm.cloudStorage.StoreSegment(ctx, item.SessionID, item.Name, item.Data)

	case BufferItemPlaylist:
		if item.Name == "master" {
			err = bm.cloudStorage.StorePlaylists(ctx, item.SessionID, item.Data, nil)
		} else {
			err = bm.cloudStorage.StorePlaylists(ctx, item.SessionID, nil, item.Data)
		}

	case BufferItemManifest:
		err = bm.cloudStorage.StoreManifest(ctx, item.SessionID, item.Data)

	case BufferItemScreenshot:
		var timestamp int64
		fmt.Sscanf(item.Name, "screenshot_%d", &timestamp)
		err = bm.cloudStorage.StoreScreenshot(ctx, item.SessionID, timestamp, item.Data)
	}

	item.LastAttempt = time.Now()

	if err != nil {
		item.Error = err
		item.RetryCount++

		atomic.AddInt64(&bm.metrics.TotalFailed, 1)

		bm.mu.Lock()
		bm.metrics.LastError = err
		bm.metrics.LastErrorTime = time.Now()
		bm.mu.Unlock()

		// Re-queue if under retry limit
		if item.RetryCount < 5 {
			backoff := time.Duration(item.RetryCount) * 30 * time.Second
			time.AfterFunc(backoff, func() {
				select {
				case bm.bufferQueue <- item:
				default:
					bm.logger.Warnw("failed to re-queue item after error", err)
				}
			})
		} else {
			bm.logger.Errorw("item flush failed after max retries", err)
			bm.removeBufferedItem(item)
		}
	} else {
		// Success - remove from buffer
		bm.removeBufferedItem(item)
		atomic.AddInt64(&bm.metrics.TotalFlushed, 1)

		bm.mu.Lock()
		bm.metrics.LastFlushTime = time.Now()
		bm.mu.Unlock()

		bm.logger.Debugw("buffered item flushed",
			"id", item.ID,
			"retries", item.RetryCount)

		// Delete local buffer file
		bufferPath := filepath.Join(bm.config.BufferPath, item.SessionID, item.Name)
		if err := os.Remove(bufferPath); err != nil {
			bm.logger.Warnw("failed to delete buffer file", err)
		}
	}
}

// retryFailedItems retries all failed buffered items
func (bm *BufferManager) retryFailedItems() {
	bm.mu.RLock()
	items := make([]*BufferedItem, 0, len(bm.bufferedItems))
	for _, item := range bm.bufferedItems {
		if item.Error != nil && time.Since(item.LastAttempt) > 1*time.Minute {
			items = append(items, item)
		}
	}
	bm.mu.RUnlock()

	for _, item := range items {
		select {
		case bm.bufferQueue <- item:
		default:
			// Queue full, will retry next time
		}
	}
}

// flushAll attempts to flush all buffered items
func (bm *BufferManager) flushAll() {
	bm.mu.RLock()
	items := make([]*BufferedItem, 0, len(bm.bufferedItems))
	for _, item := range bm.bufferedItems {
		items = append(items, item)
	}
	bm.mu.RUnlock()

	bm.logger.Infow("flushing all buffered items", "count", len(items))

	for _, item := range items {
		bm.attemptFlush(item)
	}
}

// removeBufferedItem removes an item from the buffer
func (bm *BufferManager) removeBufferedItem(item *BufferedItem) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if _, exists := bm.bufferedItems[item.ID]; exists {
		delete(bm.bufferedItems, item.ID)
		atomic.AddInt64(&bm.metrics.CurrentBuffered, -1)
		atomic.AddInt64(&bm.metrics.BufferSize, -int64(len(item.Data)))
	}
}

// ensureBufferDirectory creates the buffer directory if it doesn't exist
func (bm *BufferManager) ensureBufferDirectory() error {
	return os.MkdirAll(bm.config.BufferPath, 0755)
}

// writeBufferFile writes data to a buffer file
func (bm *BufferManager) writeBufferFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// loadBufferState loads the buffer state from disk
func (bm *BufferManager) loadBufferState() error {
	statePath := filepath.Join(bm.config.BufferPath, "buffer_state.json")

	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file, fresh start
		}
		return err
	}

	var states []BufferState
	if err := json.Unmarshal(data, &states); err != nil {
		return err
	}

	// Reload buffered items
	for _, state := range states {
		for _, itemName := range state.BufferedItems {
			bufferPath := filepath.Join(bm.config.BufferPath, state.SessionID, itemName)
			data, err := os.ReadFile(bufferPath)
			if err != nil {
				bm.logger.Warnw("failed to reload buffered item", err)
				continue
			}

			// Determine item type from name
			itemType := BufferItemSegment
			if strings.HasSuffix(itemName, ".m3u8") {
				itemType = BufferItemPlaylist
			} else if itemName == "manifest.json" {
				itemType = BufferItemManifest
			} else if strings.HasPrefix(itemName, "screenshot_") {
				itemType = BufferItemScreenshot
			}

			item := &BufferedItem{
				ID:        fmt.Sprintf("%s-%s", state.SessionID, itemName),
				SessionID: state.SessionID,
				ItemType:  itemType,
				Name:      itemName,
				Data:      data,
				Timestamp: state.LastUpdate,
			}

			bm.bufferedItems[item.ID] = item
			bm.bufferQueue <- item
		}
	}

	bm.logger.Infow("buffer state loaded",
		"sessions", len(states),
		"items", len(bm.bufferedItems))

	return nil
}

// saveBufferState saves the buffer state to disk
func (bm *BufferManager) saveBufferState() error {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	// Group items by session
	sessions := make(map[string]*BufferState)

	for _, item := range bm.bufferedItems {
		state, exists := sessions[item.SessionID]
		if !exists {
			state = &BufferState{
				SessionID:     item.SessionID,
				BufferedItems: []string{},
				OldestItem:    item.Timestamp,
				LastUpdate:    item.Timestamp,
			}
			sessions[item.SessionID] = state
		}

		state.ItemCount++
		state.TotalSize += int64(len(item.Data))
		state.BufferedItems = append(state.BufferedItems, item.Name)

		if item.Timestamp.Before(state.OldestItem) {
			state.OldestItem = item.Timestamp
		}
		if item.Timestamp.After(state.LastUpdate) {
			state.LastUpdate = item.Timestamp
		}
	}

	// Convert to slice
	states := make([]BufferState, 0, len(sessions))
	for _, state := range sessions {
		states = append(states, *state)
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return err
	}

	// Write to file
	statePath := filepath.Join(bm.config.BufferPath, "buffer_state.json")
	return os.WriteFile(statePath, data, 0644)
}

// metricsCollector periodically collects metrics
func (bm *BufferManager) metricsCollector() {
	defer bm.wg.Done()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-bm.stopChan:
			return

		case <-ticker.C:
			metrics := bm.GetMetrics()
			bm.logger.Infow("buffer manager metrics",
				"total_buffered", metrics.TotalBuffered,
				"current_buffered", metrics.CurrentBuffered,
				"total_flushed", metrics.TotalFlushed,
				"total_failed", metrics.TotalFailed,
				"buffer_size", metrics.BufferSize,
				"max_buffer_size", metrics.MaxBufferSize)
		}
	}
}

// GetMetrics returns buffer manager metrics
func (bm *BufferManager) GetMetrics() BufferMetrics {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	metrics := bm.metrics

	// Find oldest buffered item
	for _, item := range bm.bufferedItems {
		if metrics.OldestBufferedTime.IsZero() || item.Timestamp.Before(metrics.OldestBufferedTime) {
			metrics.OldestBufferedTime = item.Timestamp
		}
	}

	return metrics
}

// isRunning checks if the buffer manager is running
func (bm *BufferManager) isRunning() bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	return bm.running
}

// GetBufferedSessions returns list of sessions with buffered data
func (bm *BufferManager) GetBufferedSessions() []string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	sessions := make(map[string]bool)
	for _, item := range bm.bufferedItems {
		sessions[item.SessionID] = true
	}

	result := make([]string, 0, len(sessions))
	for session := range sessions {
		result = append(result, session)
	}

	return result
}

// ClearSession clears all buffered data for a session
func (bm *BufferManager) ClearSession(sessionID string) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	var itemsRemoved int
	var sizeRemoved int64

	for id, item := range bm.bufferedItems {
		if item.SessionID == sessionID {
			delete(bm.bufferedItems, id)
			itemsRemoved++
			sizeRemoved += int64(len(item.Data))
		}
	}

	// Update metrics
	atomic.AddInt64(&bm.metrics.CurrentBuffered, -int64(itemsRemoved))
	atomic.AddInt64(&bm.metrics.BufferSize, -sizeRemoved)

	// Remove buffer files
	sessionPath := filepath.Join(bm.config.BufferPath, sessionID)
	if err := os.RemoveAll(sessionPath); err != nil {
		return fmt.Errorf("failed to remove buffer files: %w", err)
	}

	bm.logger.Infow("session buffer cleared",
		"session_id", sessionID,
		"items_removed", itemsRemoved,
		"size_removed", sizeRemoved)

	return nil
}