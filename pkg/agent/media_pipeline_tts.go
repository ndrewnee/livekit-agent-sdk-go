package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
	"golang.org/x/time/rate"
)

// Constants for TTS configuration
const (
	// OpenAI TTS API Configuration
	openAITTSEndpoint = "https://api.openai.com/v1/audio/speech"
	defaultTTSModel   = "gpt-4o-mini-tts"
	defaultVoice      = "alloy"
	defaultSpeed      = 1.0

	// Audio format configuration
	ttsResponseFormat = "opus" // Opus format for direct streaming to LiveKit

	// Input validation
	maxTTSInputSize = 4096 // characters

	// Rate limiting (requests per second)
	ttsRateLimit = rate.Limit(10) // Increase to 10 requests per second for TTS
	ttsBurstSize = 20

	// Circuit breaker configuration
	ttsMaxFailures    = 5
	ttsCircuitTimeout = 30 * time.Second
	ttsLatencyBuckets = 10
)

// TTSCallback is called when TTS audio is generated.
type TTSCallback func(roomID string, ttsAudioMap map[string][]byte)

// TextToSpeechConfig configures the TextToSpeechStage.
type TextToSpeechConfig struct {
	Name         string        // Unique identifier for this stage
	Priority     int           // Execution order (should be 40, after TranslationStage)
	APIKey       string        // OpenAI API key for authentication
	Model        string        // TTS model to use (empty for default)
	Voice        string        // Voice to use (empty for default)
	Speed        float64       // Speaking speed (empty for default)
	Timeout      time.Duration // Timeout for API calls (empty for default 5s)
	Endpoint     string        // API endpoint (empty for default)
	EnableWarmup bool          // If true, performs async warm-up call on initialization
}

// TextToSpeechStage converts translated text chunks to speech using OpenAI TTS API.
//
// This stage processes MediaData containing TranscriptionEvent with translations,
// generates speech audio for each target language in parallel, and prepares the
// audio for broadcasting through room-specific, language-specific tracks.
//
// Key features:
//   - Parallel TTS generation for multiple languages
//   - OpenAI TTS API integration with Opus format for direct LiveKit streaming
//   - Rate limiting and circuit breaker for reliability
//   - Non-blocking error handling to maintain pipeline flow
//   - Room-aware processing for multi-room agent support
//   - Direct opus audio streaming to LiveKit rooms
//
// Pipeline position:
//  1. RealtimeTranscriptionStage (priority 10) - Generates transcriptions
//  2. BroadcastStage (priority 20) - Broadcasts original transcriptions
//  3. TranslationStage (priority 30) - Translates transcriptions
//  4. TextToSpeechStage (priority 50) - Converts translations to opus audio
//  5. BroadcastStage (priority 60) - Streams opus audio to LiveKit room
type TextToSpeechStage struct {
	config *TextToSpeechConfig

	// HTTP client
	client *http.Client

	// TTS callbacks
	ttsCallbacks []TTSCallback

	// Rate limiting
	rateLimiter *rate.Limiter

	// Circuit breaker
	breaker *ttsCircuitBreaker

	// Metrics
	metrics *ttsMetrics

	// Statistics
	stats *TTSStats
}

// OpenAI TTS API request structure
type OpenAITTSRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed,omitempty"`
}

// Circuit breaker states
type ttsCircuitState int

const (
	ttsCircuitClosed ttsCircuitState = iota
	ttsCircuitOpen
	ttsCircuitHalfOpen
)

// Circuit breaker for TTS API calls
type ttsCircuitBreaker struct {
	mu              sync.RWMutex
	state           ttsCircuitState
	failureCount    int
	lastFailureTime time.Time
	nextRetryTime   time.Time
}

// TTS API call metrics
type ttsMetrics struct {
	mu                  sync.RWMutex
	totalCalls          uint64
	successfulCalls     uint64
	failedCalls         uint64
	totalLatencyMs      float64
	latencyBuckets      []float64
	bucketIndex         int
	circuitBreakerTrips uint64
	rateLimitExceeded   uint64
}

// TTS result for parallel processing
type TTSResult struct {
	Language string
	Audio    []byte
	Error    error
}

// TTSStats tracks TTS performance metrics
type TTSStats struct {
	mu sync.RWMutex

	// Generation metrics
	TotalGenerations      uint64
	SuccessfulGenerations uint64
	FailedGenerations     uint64
	ParallelBatches       uint64

	// Performance metrics
	AverageLatencyMs   float64
	LastGenerationAt   time.Time
	BytesGenerated     uint64
	CharactersToSpeech uint64
}

// NewTextToSpeechStage creates a new TTS stage with OpenAI configuration.
func NewTextToSpeechStage(config *TextToSpeechConfig) *TextToSpeechStage {
	// Apply defaults
	if config.Model == "" {
		config.Model = defaultTTSModel
	}
	if config.Voice == "" {
		config.Voice = defaultVoice
	}
	if config.Speed == 0 {
		config.Speed = defaultSpeed
	}
	if config.Timeout == 0 {
		config.Timeout = sharedHTTPTimeout
	}
	if config.Endpoint == "" {
		config.Endpoint = openAITTSEndpoint
	}

	stage := &TextToSpeechStage{
		config:       config,
		ttsCallbacks: make([]TTSCallback, 0),
		client:       getSharedHTTPClient(),
		rateLimiter:  rate.NewLimiter(ttsRateLimit, ttsBurstSize),
		breaker: &ttsCircuitBreaker{
			state: ttsCircuitClosed,
		},
		metrics: &ttsMetrics{
			latencyBuckets: make([]float64, ttsLatencyBuckets),
		},
		stats: &TTSStats{},
	}

	// Perform async warm-up to establish connection and eliminate cold start
	if config.EnableWarmup {
		go stage.warmUp()
	}

	return stage
}

// GetName implements MediaPipelineStage.
func (tts *TextToSpeechStage) GetName() string { return tts.config.Name }

// GetPriority implements MediaPipelineStage.
func (tts *TextToSpeechStage) GetPriority() int { return tts.config.Priority }

// SetEndpoint sets the TTS API endpoint for testing purposes.
func (tts *TextToSpeechStage) SetEndpoint(endpoint string) {
	tts.config.Endpoint = endpoint
}

// SetRateLimiter sets the rate limiter for testing purposes.
// Pass nil to disable rate limiting entirely.
func (tts *TextToSpeechStage) SetRateLimiter(limiter *rate.Limiter) {
	tts.rateLimiter = limiter
}

// CanProcess implements MediaPipelineStage. Only processes audio with translations.
func (tts *TextToSpeechStage) CanProcess(mediaType MediaType) bool {
	return mediaType == MediaTypeAudio
}

// Process implements MediaPipelineStage.
//
// Extracts translated text from metadata, generates speech for each language
// in parallel, and stores the audio data for broadcasting.
func (tts *TextToSpeechStage) Process(ctx context.Context, input MediaData) (MediaData, error) {
	// Check if we have transcription data with translations
	if input.Metadata == nil {
		return input, nil
	}

	transcriptionEvent, ok := input.Metadata["transcription_event"].(TranscriptionEvent)
	if !ok || len(transcriptionEvent.Translations) == 0 {
		return input, nil
	}

	// Only generate TTS for final transcriptions
	if !transcriptionEvent.IsFinal {
		return input, nil
	}

	// Generate TTS for all translations in parallel
	startTime := time.Now()
	ttsResults := tts.generateTTSParallel(ctx, transcriptionEvent.Translations)
	duration := time.Since(startTime)

	// Update statistics
	tts.updateStats(len(transcriptionEvent.Translations), len(ttsResults), duration)

	// Store TTS opus audio results in metadata for BroadcastStage
	if len(ttsResults) > 0 {
		input.Metadata["tts_audio_map"] = ttsResults
		input.Metadata["tts_generated"] = true
		input.Metadata["tts_generation_time"] = duration
		input.Metadata["tts_audio_format"] = "opus"

		getLogger := logger.GetLogger()
		getLogger.Infow("Generated TTS opus audio",
			"languages", len(ttsResults),
			"duration_ms", duration.Milliseconds(),
			"trackID", input.TrackID)
	}

	return input, nil
}

// generateTTSParallel generates TTS for multiple translations concurrently.
func (tts *TextToSpeechStage) generateTTSParallel(ctx context.Context, translations map[string]string) map[string][]byte {
	if len(translations) == 0 {
		return nil
	}

	// Create channel for results
	resultChan := make(chan TTSResult, len(translations))

	// Launch parallel TTS requests
	var wg sync.WaitGroup
	for lang, text := range translations {
		wg.Add(1)
		go func(language, translatedText string) {
			defer wg.Done()

			audio, err := tts.generateTTS(ctx, translatedText)

			resultChan <- TTSResult{
				Language: language,
				Audio:    audio,
				Error:    err,
			}
		}(lang, text)
	}

	// Wait for all requests to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect successful results
	ttsAudioMap := make(map[string][]byte)
	for result := range resultChan {
		if result.Error != nil {
			getLogger := logger.GetLogger()
			getLogger.Errorw("TTS generation failed", result.Error,
				"language", result.Language)
			continue
		}

		ttsAudioMap[result.Language] = result.Audio

		getLogger := logger.GetLogger()
		getLogger.Infow("TTS generation successful",
			"language", result.Language,
			"audioBytes", len(result.Audio))
	}

	return ttsAudioMap
}

// generateTTS generates speech for a single text using OpenAI TTS API.
func (tts *TextToSpeechStage) generateTTS(ctx context.Context, text string) ([]byte, error) {
	// Input validation
	if len(text) > maxTTSInputSize {
		return nil, fmt.Errorf("input text too large: %d characters (max %d)", len(text), maxTTSInputSize)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("empty text input")
	}

	// Check circuit breaker
	if !tts.canMakeAPICall() {
		return nil, fmt.Errorf("circuit breaker is open, TTS API temporarily unavailable")
	}

	// Apply rate limiting (skip if rate limiter is disabled)
	if tts.rateLimiter != nil && !tts.rateLimiter.Allow() {
		tts.recordRateLimitExceeded()
		return nil, fmt.Errorf("rate limit exceeded for TTS API")
	}

	// Make API call
	audioData, err := tts.callOpenAITTS(ctx, text)
	if err != nil {
		tts.recordAPIFailure()
		return nil, fmt.Errorf("OpenAI TTS API call failed: %w", err)
	}

	tts.recordAPISuccess()

	return audioData, nil
}

// callOpenAITTS makes the actual API call to OpenAI TTS service.
func (tts *TextToSpeechStage) callOpenAITTS(ctx context.Context, text string) ([]byte, error) {
	request := OpenAITTSRequest{
		Model:          tts.config.Model,
		Input:          text,
		Voice:          tts.config.Voice,
		ResponseFormat: ttsResponseFormat,
		Speed:          tts.config.Speed,
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tts.config.Endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+tts.config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	// Apply timeout to API call
	timeoutCtx, cancel := context.WithTimeout(req.Context(), tts.config.Timeout)
	defer cancel()
	req = req.WithContext(timeoutCtx)

	startTime := time.Now()
	resp, err := tts.client.Do(req)
	latency := time.Since(startTime)

	tts.recordAPILatency(latency)

	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Read Opus audio data
	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(audioData) == 0 {
		return nil, fmt.Errorf("received empty audio response")
	}

	return audioData, nil
}

// Circuit breaker methods
func (tts *TextToSpeechStage) canMakeAPICall() bool {
	tts.breaker.mu.RLock()
	defer tts.breaker.mu.RUnlock()

	switch tts.breaker.state {
	case ttsCircuitClosed:
		return true
	case ttsCircuitOpen:
		return time.Now().After(tts.breaker.nextRetryTime)
	case ttsCircuitHalfOpen:
		return true
	default:
		return false
	}
}

func (tts *TextToSpeechStage) recordAPISuccess() {
	tts.breaker.mu.Lock()
	defer tts.breaker.mu.Unlock()

	tts.breaker.failureCount = 0
	if tts.breaker.state == ttsCircuitHalfOpen {
		tts.breaker.state = ttsCircuitClosed
	}

	tts.metrics.mu.Lock()
	tts.metrics.successfulCalls++
	tts.metrics.mu.Unlock()
}

func (tts *TextToSpeechStage) recordAPIFailure() {
	tts.breaker.mu.Lock()
	defer tts.breaker.mu.Unlock()

	tts.breaker.failureCount++
	tts.breaker.lastFailureTime = time.Now()

	tts.metrics.mu.Lock()
	tts.metrics.failedCalls++
	tts.metrics.mu.Unlock()

	if tts.breaker.failureCount >= ttsMaxFailures {
		tts.breaker.state = ttsCircuitOpen
		tts.breaker.nextRetryTime = time.Now().Add(ttsCircuitTimeout)

		tts.metrics.mu.Lock()
		tts.metrics.circuitBreakerTrips++
		tts.metrics.mu.Unlock()
	}
}

// Metrics methods
func (tts *TextToSpeechStage) recordAPILatency(duration time.Duration) {
	tts.metrics.mu.Lock()
	defer tts.metrics.mu.Unlock()

	latencyMs := float64(duration.Nanoseconds()) / 1e6
	tts.metrics.totalCalls++
	tts.metrics.totalLatencyMs += latencyMs

	// Update rolling average using circular buffer
	tts.metrics.latencyBuckets[tts.metrics.bucketIndex] = latencyMs
	tts.metrics.bucketIndex = (tts.metrics.bucketIndex + 1) % len(tts.metrics.latencyBuckets)
}

func (tts *TextToSpeechStage) recordRateLimitExceeded() {
	tts.metrics.mu.Lock()
	defer tts.metrics.mu.Unlock()
	tts.metrics.rateLimitExceeded++
}

// updateStats updates TTS statistics.
func (tts *TextToSpeechStage) updateStats(requestedGenerations, successfulGenerations int, duration time.Duration) {
	tts.stats.mu.Lock()
	defer tts.stats.mu.Unlock()

	tts.stats.ParallelBatches++
	tts.stats.TotalGenerations += uint64(requestedGenerations)
	tts.stats.SuccessfulGenerations += uint64(successfulGenerations)
	tts.stats.FailedGenerations += uint64(requestedGenerations - successfulGenerations)

	if successfulGenerations > 0 {
		tts.stats.LastGenerationAt = time.Now()

		// Update average latency
		latencyMs := float64(duration.Microseconds()) / 1000.0
		if tts.stats.AverageLatencyMs == 0 {
			tts.stats.AverageLatencyMs = latencyMs
		} else {
			tts.stats.AverageLatencyMs = (tts.stats.AverageLatencyMs * 0.9) + (latencyMs * 0.1)
		}
	}
}

// GetStats returns current TTS statistics.
func (tts *TextToSpeechStage) GetStats() TTSStats {
	tts.stats.mu.RLock()
	defer tts.stats.mu.RUnlock()

	return TTSStats{
		TotalGenerations:      tts.stats.TotalGenerations,
		SuccessfulGenerations: tts.stats.SuccessfulGenerations,
		FailedGenerations:     tts.stats.FailedGenerations,
		ParallelBatches:       tts.stats.ParallelBatches,
		AverageLatencyMs:      tts.stats.AverageLatencyMs,
		LastGenerationAt:      tts.stats.LastGenerationAt,
		BytesGenerated:        tts.stats.BytesGenerated,
		CharactersToSpeech:    tts.stats.CharactersToSpeech,
	}
}

// GetAPIMetrics returns current TTS API metrics.
func (tts *TextToSpeechStage) GetAPIMetrics() map[string]interface{} {
	tts.metrics.mu.RLock()
	defer tts.metrics.mu.RUnlock()

	// Calculate average latency from rolling buckets
	var avgLatency float64
	validBuckets := 0
	for _, latency := range tts.metrics.latencyBuckets {
		if latency > 0 {
			avgLatency += latency
			validBuckets++
		}
	}
	if validBuckets > 0 {
		avgLatency /= float64(validBuckets)
	}

	return map[string]interface{}{
		"total_calls":           tts.metrics.totalCalls,
		"successful_calls":      tts.metrics.successfulCalls,
		"failed_calls":          tts.metrics.failedCalls,
		"average_latency_ms":    avgLatency,
		"circuit_breaker_trips": tts.metrics.circuitBreakerTrips,
		"rate_limit_exceeded":   tts.metrics.rateLimitExceeded,
	}
}

// SetVoice updates the TTS voice setting.
func (tts *TextToSpeechStage) SetVoice(voice string) {
	if voice != "" {
		tts.config.Voice = voice
	}
}

// SetSpeed updates the TTS speed setting.
func (tts *TextToSpeechStage) SetSpeed(speed float64) {
	if speed > 0 && speed <= 4.0 {
		tts.config.Speed = speed
	}
}

// warmUp performs an async warm-up call to establish API connection and eliminate cold start.
func (tts *TextToSpeechStage) warmUp() {
	ctx, cancel := context.WithTimeout(context.Background(), tts.config.Timeout)
	defer cancel()

	getLogger := logger.GetLogger()
	getLogger.Infow("Starting TTS stage warm-up")

	// Perform a small test TTS generation to establish connection
	_, err := tts.generateTTS(ctx, "Hello")
	if err != nil {
		getLogger.Debugw("TTS stage warm-up failed (non-critical)", "error", err)
	} else {
		getLogger.Infow("TTS stage warm-up completed successfully")
	}

	// Reset statistics to exclude warm-up call from metrics
	tts.stats.mu.Lock()
	tts.stats.TotalGenerations = 0
	tts.stats.SuccessfulGenerations = 0
	tts.stats.FailedGenerations = 0
	tts.stats.ParallelBatches = 0
	tts.stats.AverageLatencyMs = 0
	tts.stats.BytesGenerated = 0
	tts.stats.CharactersToSpeech = 0
	tts.stats.LastGenerationAt = time.Time{}
	tts.stats.mu.Unlock()
}
