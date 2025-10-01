package storage

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
)

// HLSPlaylistManager manages HLS playlists and segments
// Implements HLS playlist requirements from PLAN.md Milestone 3
type HLSPlaylistManager struct {
	logger  logger.Logger
	storage Storage
	config  HLSConfig

	mu             sync.RWMutex
	segments       map[string][]*HLSSegment
	masterPlaylist map[string]*MasterPlaylist
	encryptionKeys map[string][]byte

	// Encryption
	cipher cipher.Block
	iv     []byte
}

// HLSSegment represents an HLS segment
type HLSSegment struct {
	Index        int
	Name         string
	Duration     float64
	URI          string
	DiscontinuityBefore bool
	ByteRange    *ByteRange
	Key          *EncryptionKey
	Timestamp    time.Time
	Size         int64
}

// ByteRange represents byte range for a segment
type ByteRange struct {
	Length int64
	Offset int64
}

// EncryptionKey represents encryption key info
type EncryptionKey struct {
	Method            string
	URI               string
	IV                string
	KeyFormatVersions string
}

// MasterPlaylist represents a master playlist for multi-variant streams
type MasterPlaylist struct {
	Variants []*Variant
	Audio    []*AudioTrack
	Subtitles []*SubtitleTrack
}

// Variant represents a variant stream in master playlist
type Variant struct {
	Bandwidth  int
	Resolution string
	Codecs     string
	FrameRate  float64
	URI        string
	Audio      string
	Subtitles  string
}

// AudioTrack represents an audio track
type AudioTrack struct {
	GroupID  string
	Name     string
	Language string
	Default  bool
	URI      string
	Channels int
}

// SubtitleTrack represents a subtitle track
type SubtitleTrack struct {
	GroupID  string
	Name     string
	Language string
	Default  bool
	URI      string
	Forced   bool
}

// NewHLSPlaylistManager creates a new HLS playlist manager
func NewHLSPlaylistManager(storage Storage, config HLSConfig, logger logger.Logger) (*HLSPlaylistManager, error) {
	m := &HLSPlaylistManager{
		logger:         logger,
		storage:        storage,
		config:         config,
		segments:       make(map[string][]*HLSSegment),
		masterPlaylist: make(map[string]*MasterPlaylist),
		encryptionKeys: make(map[string][]byte),
	}

	// Initialize encryption if enabled
	if config.Encryption.Enabled {
		if err := m.initializeEncryption(); err != nil {
			return nil, fmt.Errorf("failed to initialize encryption: %w", err)
		}
	}

	return m, nil
}

// initializeEncryption sets up AES encryption
func (m *HLSPlaylistManager) initializeEncryption() error {
	// Generate random key
	key := make([]byte, 16) // AES-128
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("failed to generate encryption key: %w", err)
	}

	// Create cipher
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed to create AES cipher: %w", err)
	}
	m.cipher = block

	// Generate IV
	m.iv = make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, m.iv); err != nil {
		return fmt.Errorf("failed to generate IV: %w", err)
	}

	m.logger.Infow("HLS encryption initialized",
		"method", m.config.Encryption.Method,
		"key_rotation", m.config.Encryption.KeyRotationInterval)

	return nil
}

// AddSegment adds a new segment to the playlist
func (m *HLSPlaylistManager) AddSegment(sessionID string, segment *HLSSegment) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Apply encryption if enabled
	if m.config.Encryption.Enabled {
		segment.Key = m.getCurrentEncryptionKey(sessionID, segment.Index)
	}

	// Add to segments list
	m.segments[sessionID] = append(m.segments[sessionID], segment)

	// Apply max segments limit if configured
	if m.config.MaxSegments > 0 && len(m.segments[sessionID]) > m.config.MaxSegments {
		// Remove oldest segments
		removeCount := len(m.segments[sessionID]) - m.config.MaxSegments
		m.segments[sessionID] = m.segments[sessionID][removeCount:]

		// Mark discontinuity
		if len(m.segments[sessionID]) > 0 {
			m.segments[sessionID][0].DiscontinuityBefore = true
		}
	}

	m.logger.Debugw("segment added to playlist",
		"session_id", sessionID,
		"segment", segment.Name,
		"duration", segment.Duration,
		"total_segments", len(m.segments[sessionID]))

	return nil
}

// getCurrentEncryptionKey gets or generates encryption key for segment
func (m *HLSPlaylistManager) getCurrentEncryptionKey(sessionID string, segmentIndex int) *EncryptionKey {
	// Check if key rotation is needed
	keyIndex := 0
	if m.config.Encryption.KeyRotationInterval > 0 {
		keyIndex = segmentIndex / m.config.Encryption.KeyRotationInterval
	}

	keyID := fmt.Sprintf("%s-key-%d", sessionID, keyIndex)

	// Check if key exists
	if _, exists := m.encryptionKeys[keyID]; !exists {
		// Generate new key
		key := make([]byte, 16)
		io.ReadFull(rand.Reader, key)
		m.encryptionKeys[keyID] = key
	}

	return &EncryptionKey{
		Method:            m.config.Encryption.Method,
		URI:               fmt.Sprintf("%s%s.key", m.config.Encryption.KeyURI, keyID),
		IV:                hex.EncodeToString(m.iv),
		KeyFormatVersions: "1",
	}
}

// GenerateMediaPlaylist generates a media playlist for a session
func (m *HLSPlaylistManager) GenerateMediaPlaylist(sessionID string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	segments, exists := m.segments[sessionID]
	if !exists || len(segments) == 0 {
		return nil, fmt.Errorf("no segments found for session %s", sessionID)
	}

	var buf bytes.Buffer

	// Write header
	buf.WriteString("#EXTM3U\n")
	buf.WriteString(fmt.Sprintf("#EXT-X-VERSION:%d\n", 6))
	buf.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", m.config.TargetDuration))

	// Calculate media sequence
	mediaSequence := 0
	if len(segments) > 0 {
		mediaSequence = segments[0].Index
	}
	buf.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", mediaSequence))

	// Add playlist type
	if m.config.PlaylistType != "" {
		buf.WriteString(fmt.Sprintf("#EXT-X-PLAYLIST-TYPE:%s\n", strings.ToUpper(m.config.PlaylistType)))
	}

	// Add encryption info if applicable
	var currentKey *EncryptionKey

	// Write segments
	for _, segment := range segments {
		// Handle discontinuity
		if segment.DiscontinuityBefore {
			buf.WriteString("#EXT-X-DISCONTINUITY\n")
		}

		// Handle encryption key changes
		if segment.Key != nil && !equalKeys(currentKey, segment.Key) {
			buf.WriteString(fmt.Sprintf("#EXT-X-KEY:METHOD=%s,URI=\"%s\"",
				segment.Key.Method, segment.Key.URI))
			if segment.Key.IV != "" {
				buf.WriteString(fmt.Sprintf(",IV=0x%s", segment.Key.IV))
			}
			buf.WriteString("\n")
			currentKey = segment.Key
		}

		// Write segment duration and URI
		buf.WriteString(fmt.Sprintf("#EXTINF:%.6f,\n", segment.Duration))

		// Handle byte range if enabled
		if m.config.ByteRange && segment.ByteRange != nil {
			buf.WriteString(fmt.Sprintf("#EXT-X-BYTERANGE:%d@%d\n",
				segment.ByteRange.Length, segment.ByteRange.Offset))
		}

		buf.WriteString(fmt.Sprintf("%s\n", segment.URI))
	}

	// Add end tag for VOD playlists
	if strings.ToUpper(m.config.PlaylistType) == "VOD" {
		buf.WriteString("#EXT-X-ENDLIST\n")
	}

	return buf.Bytes(), nil
}

// GenerateMasterPlaylist generates a master playlist for multi-variant streams
func (m *HLSPlaylistManager) GenerateMasterPlaylist(sessionID string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	master, exists := m.masterPlaylist[sessionID]
	if !exists || master == nil {
		return nil, fmt.Errorf("no master playlist configured for session %s", sessionID)
	}

	var buf bytes.Buffer

	// Write header
	buf.WriteString("#EXTM3U\n")
	buf.WriteString("#EXT-X-VERSION:7\n")

	// Write audio tracks
	for _, audio := range master.Audio {
		buf.WriteString(fmt.Sprintf("#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"%s\",NAME=\"%s\"",
			audio.GroupID, audio.Name))
		if audio.Language != "" {
			buf.WriteString(fmt.Sprintf(",LANGUAGE=\"%s\"", audio.Language))
		}
		if audio.Default {
			buf.WriteString(",DEFAULT=YES,AUTOSELECT=YES")
		}
		if audio.URI != "" {
			buf.WriteString(fmt.Sprintf(",URI=\"%s\"", audio.URI))
		}
		if audio.Channels > 0 {
			buf.WriteString(fmt.Sprintf(",CHANNELS=\"%d\"", audio.Channels))
		}
		buf.WriteString("\n")
	}

	// Write subtitle tracks
	for _, subtitle := range master.Subtitles {
		buf.WriteString(fmt.Sprintf("#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"%s\",NAME=\"%s\"",
			subtitle.GroupID, subtitle.Name))
		if subtitle.Language != "" {
			buf.WriteString(fmt.Sprintf(",LANGUAGE=\"%s\"", subtitle.Language))
		}
		if subtitle.Default {
			buf.WriteString(",DEFAULT=YES,AUTOSELECT=YES")
		}
		if subtitle.Forced {
			buf.WriteString(",FORCED=YES")
		}
		buf.WriteString(fmt.Sprintf(",URI=\"%s\"", subtitle.URI))
		buf.WriteString("\n")
	}

	// Write variant streams
	for _, variant := range master.Variants {
		// Stream info
		buf.WriteString("#EXT-X-STREAM-INF:")
		buf.WriteString(fmt.Sprintf("BANDWIDTH=%d", variant.Bandwidth))

		if variant.Resolution != "" {
			buf.WriteString(fmt.Sprintf(",RESOLUTION=%s", variant.Resolution))
		}
		if variant.Codecs != "" {
			buf.WriteString(fmt.Sprintf(",CODECS=\"%s\"", variant.Codecs))
		}
		if variant.FrameRate > 0 {
			buf.WriteString(fmt.Sprintf(",FRAME-RATE=%.3f", variant.FrameRate))
		}
		if variant.Audio != "" {
			buf.WriteString(fmt.Sprintf(",AUDIO=\"%s\"", variant.Audio))
		}
		if variant.Subtitles != "" {
			buf.WriteString(fmt.Sprintf(",SUBTITLES=\"%s\"", variant.Subtitles))
		}
		buf.WriteString("\n")

		// Variant URI
		buf.WriteString(fmt.Sprintf("%s\n", variant.URI))
	}

	return buf.Bytes(), nil
}

// SetMasterPlaylist sets the master playlist configuration
func (m *HLSPlaylistManager) SetMasterPlaylist(sessionID string, master *MasterPlaylist) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.masterPlaylist[sessionID] = master
}

// SavePlaylist saves a playlist to storage
func (m *HLSPlaylistManager) SavePlaylist(ctx context.Context, sessionID string, playlistType string) error {
	var playlist []byte
	var err error

	switch playlistType {
	case "master":
		if !m.config.MasterPlaylist {
			return fmt.Errorf("master playlist not enabled")
		}
		playlist, err = m.GenerateMasterPlaylist(sessionID)
		if err != nil {
			return fmt.Errorf("failed to generate master playlist: %w", err)
		}

	case "media":
		playlist, err = m.GenerateMediaPlaylist(sessionID)
		if err != nil {
			return fmt.Errorf("failed to generate media playlist: %w", err)
		}

	default:
		return fmt.Errorf("unknown playlist type: %s", playlistType)
	}

	// Store playlist
	playlistName := fmt.Sprintf("%s.m3u8", playlistType)
	if playlistType == "media" {
		playlistName = "playlist.m3u8"
	} else if playlistType == "master" {
		playlistName = "master.m3u8"
	}

	err = m.storage.StoreSegment(ctx, sessionID, playlistName, playlist)
	if err != nil {
		return fmt.Errorf("failed to store playlist: %w", err)
	}

	m.logger.Debugw("playlist saved",
		"session_id", sessionID,
		"type", playlistType,
		"size", len(playlist))

	return nil
}

// GetSegmentDuration returns the total duration of all segments
func (m *HLSPlaylistManager) GetSegmentDuration(sessionID string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	segments, exists := m.segments[sessionID]
	if !exists {
		return 0
	}

	var totalDuration float64
	for _, segment := range segments {
		totalDuration += segment.Duration
	}

	return totalDuration
}

// GetSegmentCount returns the number of segments
func (m *HLSPlaylistManager) GetSegmentCount(sessionID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	segments, exists := m.segments[sessionID]
	if !exists {
		return 0
	}

	return len(segments)
}

// GetLatestSegment returns the most recent segment
func (m *HLSPlaylistManager) GetLatestSegment(sessionID string) *HLSSegment {
	m.mu.RLock()
	defer m.mu.RUnlock()

	segments, exists := m.segments[sessionID]
	if !exists || len(segments) == 0 {
		return nil
	}

	return segments[len(segments)-1]
}

// RemoveOldSegments removes segments older than the specified duration
func (m *HLSPlaylistManager) RemoveOldSegments(sessionID string, maxAge time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	segments, exists := m.segments[sessionID]
	if !exists || len(segments) == 0 {
		return 0
	}

	cutoff := time.Now().Add(-maxAge)
	removeIndex := -1

	for i, segment := range segments {
		if segment.Timestamp.After(cutoff) {
			removeIndex = i
			break
		}
	}

	if removeIndex > 0 {
		removed := removeIndex
		m.segments[sessionID] = segments[removeIndex:]

		// Mark discontinuity
		if len(m.segments[sessionID]) > 0 {
			m.segments[sessionID][0].DiscontinuityBefore = true
		}

		m.logger.Debugw("removed old segments",
			"session_id", sessionID,
			"removed_count", removed,
			"remaining", len(m.segments[sessionID]))

		return removed
	}

	return 0
}

// GenerateThumbnailPlaylist generates a thumbnail playlist for seeking preview
func (m *HLSPlaylistManager) GenerateThumbnailPlaylist(sessionID string, screenshots []ScreenshotInfo) ([]byte, error) {
	// Check if we have screenshots to generate playlist from
	if len(screenshots) == 0 {
		return nil, fmt.Errorf("no screenshots available for thumbnail playlist")
	}

	var buf bytes.Buffer

	// Sort screenshots by timestamp
	sort.Slice(screenshots, func(i, j int) bool {
		return screenshots[i].Timestamp < screenshots[j].Timestamp
	})

	// Write WebVTT header
	buf.WriteString("WEBVTT\n\n")

	// Write thumbnail entries
	for i, screenshot := range screenshots {
		startTime := formatTimestamp(screenshot.Timestamp)
		endTime := startTime
		if i+1 < len(screenshots) {
			endTime = formatTimestamp(screenshots[i+1].Timestamp)
		} else {
			// Last screenshot, extend by default 5 seconds
			endTime = formatTimestamp(screenshot.Timestamp + 5000)
		}

		buf.WriteString(fmt.Sprintf("%s --> %s\n", startTime, endTime))
		buf.WriteString(fmt.Sprintf("%s\n\n", screenshot.URL))
	}

	return buf.Bytes(), nil
}

// formatTimestamp formats milliseconds to WebVTT timestamp (HH:MM:SS.mmm)
func formatTimestamp(ms int64) string {
	hours := ms / 3600000
	minutes := (ms % 3600000) / 60000
	seconds := (ms % 60000) / 1000
	millis := ms % 1000

	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

// equalKeys compares two encryption keys
func equalKeys(k1, k2 *EncryptionKey) bool {
	if k1 == nil && k2 == nil {
		return true
	}
	if k1 == nil || k2 == nil {
		return false
	}
	return k1.Method == k2.Method && k1.URI == k2.URI && k1.IV == k2.IV
}

// ValidatePlaylist validates HLS playlist for compliance
func (m *HLSPlaylistManager) ValidatePlaylist(sessionID string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	segments, exists := m.segments[sessionID]
	if !exists || len(segments) == 0 {
		return fmt.Errorf("no segments found for validation")
	}

	// Check segment duration compliance
	for _, segment := range segments {
		if segment.Duration > float64(m.config.TargetDuration)*1.1 {
			return fmt.Errorf("segment %s duration %.2f exceeds target duration %d",
				segment.Name, segment.Duration, m.config.TargetDuration)
		}
	}

	// Check minimum segments for VOD
	if strings.ToUpper(m.config.PlaylistType) == "VOD" && len(segments) < 3 {
		return fmt.Errorf("VOD playlist requires at least 3 segments, found %d", len(segments))
	}

	// Validate encryption if enabled
	if m.config.Encryption.Enabled {
		for _, segment := range segments {
			if segment.Key == nil {
				return fmt.Errorf("segment %s missing encryption key", segment.Name)
			}
		}
	}

	return nil
}

// ClearSession clears all data for a session
func (m *HLSPlaylistManager) ClearSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.segments, sessionID)
	delete(m.masterPlaylist, sessionID)

	// Clear session-specific encryption keys
	for key := range m.encryptionKeys {
		if strings.HasPrefix(key, sessionID) {
			delete(m.encryptionKeys, key)
		}
	}

	m.logger.Debugw("session cleared from playlist manager", "session_id", sessionID)
}

// GetStats returns playlist statistics
func (m *HLSPlaylistManager) GetStats() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]interface{})
	stats["total_sessions"] = len(m.segments)
	stats["encryption_enabled"] = m.config.Encryption.Enabled
	stats["playlist_type"] = m.config.PlaylistType
	stats["max_segments"] = m.config.MaxSegments
	stats["target_duration"] = m.config.TargetDuration

	totalSegments := 0
	for _, segments := range m.segments {
		totalSegments += len(segments)
	}
	stats["total_segments"] = totalSegments

	return stats
}