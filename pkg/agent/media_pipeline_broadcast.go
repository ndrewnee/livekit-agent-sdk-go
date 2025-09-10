package agent

import (
	"context"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
)

// BroadcastCallback defines the function signature for handling broadcasts.
// It receives the complete MediaData which contains all metadata including transcriptions, translations, TTS audio, etc.
type BroadcastCallback func(ctx context.Context, data MediaData) error

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
// Checks for data that needs broadcasting (transcriptions, translations, TTS audio) and calls registered callbacks.
// The data should be added by a previous stage (like RealtimeTranscriptionStage, TranslationStage, or TextToSpeechStage).
func (bs *BroadcastStage) Process(ctx context.Context, input MediaData) (MediaData, error) {
	// Check if we have data in metadata
	if input.Metadata == nil {
		return input, nil
	}

	// Check if there's anything to broadcast
	hasTranscription := false
	hasTTS := false

	if transcriptionEvent, ok := input.Metadata["transcription_event"].(TranscriptionEvent); ok && transcriptionEvent.Text != "" {
		hasTranscription = true
	}

	if ttsAudioMap, ok := input.Metadata["tts_audio_map"].(map[string][]byte); ok && len(ttsAudioMap) > 0 {
		hasTTS = true
	}

	// Nothing to broadcast
	if !hasTranscription && !hasTTS {
		return input, nil
	}

	// Get callbacks to call
	bs.mu.RLock()
	callbacks := make([]BroadcastCallback, len(bs.broadcastCallbacks))
	copy(callbacks, bs.broadcastCallbacks)
	bs.mu.RUnlock()

	// Call each registered callback with the complete MediaData
	for _, callback := range callbacks {
		startTime := time.Now()
		err := callback(ctx, input)
		broadcastDuration := time.Since(startTime)

		// Update statistics
		dataSize := 0
		isFinal := true

		// Calculate data size for statistics
		if hasTranscription {
			if event, ok := input.Metadata["transcription_event"].(TranscriptionEvent); ok {
				dataSize += len(event.Text)
				isFinal = event.IsFinal
			}
		}
		if hasTTS {
			if ttsAudioMap, ok := input.Metadata["tts_audio_map"].(map[string][]byte); ok {
				for _, audio := range ttsAudioMap {
					dataSize += len(audio)
				}
			}
		}

		bs.updateStats(isFinal, err != nil, broadcastDuration, dataSize)

		if err != nil {
			getLogger := logger.GetLogger()
			getLogger.Errorw("broadcast callback failed", err,
				"trackID", input.TrackID,
				"hasTranscription", hasTranscription,
				"hasTTS", hasTTS)
			// Continue with other callbacks even if one fails
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
// The callback will be called with the complete MediaData containing all metadata
// (transcriptions, translations, TTS audio, etc.) that needs to be broadcast.
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
