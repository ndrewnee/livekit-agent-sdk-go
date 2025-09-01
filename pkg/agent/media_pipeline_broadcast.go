package agent

import (
	"context"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
)

// BroadcastCallback defines the function signature for handling broadcasts.
// It receives the TranscriptionEvent, ParticipantMetadata, and trackID for broadcasting logic.
type BroadcastCallback func(ctx context.Context, event TranscriptionEvent, metadata ParticipantMetadata, trackID string) error

// BroadcastStage broadcasts transcription data using a configurable callback.
//
// This stage receives transcription data from previous pipeline stages (like RealtimeTranscriptionStage)
// and calls a user-defined callback to handle the broadcasting. This allows for flexible broadcasting
// strategies that can work with different rooms, topics, and delivery mechanisms.
//
// Key features:
//   - Extracts transcription data from MediaData metadata
//   - Uses callback pattern for flexible broadcasting strategies
//   - Supports different rooms, topics, and delivery mechanisms per job
//   - Uses LiveKit TranscriptionSegment format for compatibility
//   - Handles both partial and final transcriptions
//   - Non-blocking broadcasts with error handling
//   - Comprehensive statistics tracking
//
// The stage follows the pipeline pattern where transcription data flows through:
//  1. Audio input → RealtimeTranscriptionStage (adds transcription to metadata)
//  2. MediaData with transcription → BroadcastStage (calls callback for broadcasting)
type BroadcastStage struct {
	name     string
	priority int

	// Broadcast callbacks for handling actual broadcasting
	mu                 sync.RWMutex
	broadcastCallbacks []BroadcastCallback

	// Statistics
	stats *BroadcastStats
}

// ParticipantMetadata contains metadata about the participant.
// Used in TranscriptionEvent for database operations.
type ParticipantMetadata struct {
	ClassID          string `json:"class_id"`          // Class ID (room name)
	ClassroomID      string `json:"classroom_id"`      // Classroom ID
	UserID           string `json:"user_id"`           // User ID (participant identity)
	AllowTranslation bool   `json:"allow_translation"` // Whether translation is allowed for this participant
}

// BroadcastStats tracks broadcasting metrics.
type BroadcastStats struct {
	mu sync.RWMutex

	// Broadcast metrics
	TotalBroadcasts   uint64
	PartialBroadcasts uint64
	FinalBroadcasts   uint64
	FailedBroadcasts  uint64

	// Data metrics
	BytesSent          uint64
	LastBroadcastAt    time.Time
	AverageBroadcastMs float64
}

// NewBroadcastStage creates a new broadcast stage for transcriptions.
//
// Parameters:
//   - name: Unique identifier for this stage
//   - priority: Execution order (should be higher than transcription stage)
//
// Use AddBroadcastCallback() to register callbacks for handling broadcasts.
func NewBroadcastStage(name string, priority int) *BroadcastStage {
	return &BroadcastStage{
		name:     name,
		priority: priority,
		stats:    &BroadcastStats{},
	}
}

// GetName implements MediaPipelineStage.
func (bs *BroadcastStage) GetName() string { return bs.name }

// GetPriority implements MediaPipelineStage.
func (bs *BroadcastStage) GetPriority() int { return bs.priority }

// CanProcess implements MediaPipelineStage. Processes audio data with transcriptions.
func (bs *BroadcastStage) CanProcess(mediaType MediaType) bool {
	// Only process audio tracks that might have transcriptions
	return mediaType == MediaTypeAudio
}

// Process implements MediaPipelineStage.
//
// Extracts transcription or translation data from MediaData metadata and calls registered callbacks.
// The data should be added by a previous stage (like RealtimeTranscriptionStage or TranslationStage).
func (bs *BroadcastStage) Process(ctx context.Context, input MediaData) (MediaData, error) {
	// Check if we have data in metadata
	if input.Metadata == nil {
		return input, nil
	}

	var events []TranscriptionEvent
	var textSizes []int
	var isFinals []bool

	// Check for transcription event
	if transcriptionEvent, ok := input.Metadata["transcription_event"].(TranscriptionEvent); ok && transcriptionEvent.Text != "" {
		// Always broadcast the transcription event (for transcription and translation)
		events = append(events, transcriptionEvent)
		textSizes = append(textSizes, len(transcriptionEvent.Text))
		isFinals = append(isFinals, transcriptionEvent.IsFinal)

	}

	// No broadcasts to send
	if len(events) == 0 {
		return input, nil
	}

	// Get callbacks to call
	bs.mu.RLock()
	callbacks := make([]BroadcastCallback, len(bs.broadcastCallbacks))
	copy(callbacks, bs.broadcastCallbacks)
	bs.mu.RUnlock()

	// Extract participant metadata from input metadata (injected by transcription stage)
	var participantMetadata ParticipantMetadata
	if participantMeta, exists := input.Metadata["participant_metadata"]; exists {
		if meta, ok := participantMeta.(ParticipantMetadata); ok {
			participantMetadata = meta
		}
	}

	// Call each registered callback for each broadcast
	for i, event := range events {
		for _, callback := range callbacks {
			startTime := time.Now()
			err := callback(ctx, event, participantMetadata, input.TrackID)
			broadcastDuration := time.Since(startTime)

			// Update statistics
			bs.updateStats(isFinals[i], err != nil, broadcastDuration, textSizes[i])

			if err != nil {
				getLogger := logger.GetLogger()
				getLogger.Errorw("broadcast callback failed", err,
					"trackID", input.TrackID,
					"final", isFinals[i])
				// Continue with other callbacks even if one fails
			}
		}
	}

	// Mark as broadcast in metadata
	if input.Metadata == nil {
		input.Metadata = make(map[string]interface{})
	}
	input.Metadata["broadcast_completed"] = true
	input.Metadata["broadcast_at"] = time.Now()

	// Don't fail the pipeline even if callbacks fail
	return input, nil
}

// AddBroadcastCallback adds a callback for broadcast events.
//
// The callback will be called for each transcription that needs to be broadcast.
// Multiple callbacks can be registered and will be called in the order they were added.
func (bs *BroadcastStage) AddBroadcastCallback(callback BroadcastCallback) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	bs.broadcastCallbacks = append(bs.broadcastCallbacks, callback)
}

// updateStats updates broadcast statistics.
func (bs *BroadcastStage) updateStats(isFinal bool, failed bool, duration time.Duration, textSize int) {
	bs.stats.mu.Lock()
	defer bs.stats.mu.Unlock()

	bs.stats.TotalBroadcasts++

	if failed {
		bs.stats.FailedBroadcasts++
	} else {
		if isFinal {
			bs.stats.FinalBroadcasts++
		} else {
			bs.stats.PartialBroadcasts++
		}

		bs.stats.BytesSent += uint64(textSize)
		bs.stats.LastBroadcastAt = time.Now()

		// Update average broadcast time (simple moving average)
		broadcastMs := float64(duration.Microseconds()) / 1000.0
		if bs.stats.AverageBroadcastMs == 0 {
			bs.stats.AverageBroadcastMs = broadcastMs
		} else {
			bs.stats.AverageBroadcastMs = (bs.stats.AverageBroadcastMs * 0.9) + (broadcastMs * 0.1)
		}
	}
}

// GetStats returns current broadcast statistics.
func (bs *BroadcastStage) GetStats() BroadcastStats {
	bs.stats.mu.RLock()
	defer bs.stats.mu.RUnlock()

	return BroadcastStats{
		TotalBroadcasts:    bs.stats.TotalBroadcasts,
		PartialBroadcasts:  bs.stats.PartialBroadcasts,
		FinalBroadcasts:    bs.stats.FinalBroadcasts,
		FailedBroadcasts:   bs.stats.FailedBroadcasts,
		BytesSent:          bs.stats.BytesSent,
		LastBroadcastAt:    bs.stats.LastBroadcastAt,
		AverageBroadcastMs: bs.stats.AverageBroadcastMs,
	}
}
