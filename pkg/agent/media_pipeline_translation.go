package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/livekit/protocol/logger"
	"golang.org/x/time/rate"
)

// Constants for configuration
const (
	// HTTP Client Configuration
	DefaultHTTPTimeout = 30 * time.Second

	// OpenAI Streaming API Configuration
	OpenAIStreamingEndpoint = "https://api.openai.com/v1/chat/completions"
	DefaultModel            = "gpt-4o-mini"
	DefaultTemperature      = 0.3

	// Streaming Configuration
	StreamReadTimeout = 30 * time.Second
	StreamBufferSize  = 8192
	MaxStreamChunks   = 1000

	// Input Validation
	MaxInputTextSize   = 4000 // characters
	MaxTargetLanguages = 20

	// Rate Limiting (requests per second)
	DefaultRateLimit = rate.Limit(10) // 10 requests per second
	DefaultBurstSize = 20

	// Caching Configuration
	MaxCacheSize = 1000
	CacheTTL     = 1 * time.Hour

	// Circuit Breaker Configuration
	MaxConsecutiveFailures = 5
	CircuitBreakerTimeout  = 30 * time.Second

	// Metrics
	LatencyBuckets = 10 // For moving average calculation

	// Streaming Metrics
	TimeToFirstTokenBuckets = 10
)

// TranslationStage translates transcriptions to multiple participant languages using OpenAI Streaming API.
//
// This stage runs AFTER BroadcastStage and translates already-broadcast transcriptions
// to all unique participant translation languages. It uses OpenAI's Streaming API
// with gpt-4o-mini model for real-time translation responses via Server-Sent Events.
//
// Key features:
//   - Dynamic participant language tracking
//   - Multi-language single-call streaming translation
//   - Smart filtering to avoid unnecessary translations
//   - Streaming API integration with gpt-4o-mini for faster responses
//   - Real-time single-attempt translation for immediate responses
//   - No retry logic to maintain real-time performance
//   - Automatic translation broadcasting
//
// Pipeline position:
//  1. RealtimeTranscriptionStage (priority 10) - Generates transcriptions
//  2. BroadcastStage (priority 20) - Broadcasts original transcriptions
//  3. TranslationStage (priority 30) - Translates and broadcasts translations
type TranslationStage struct {
	name     string
	priority int

	// OpenAI streaming configuration
	apiKey        string
	model         string // "gpt-4o-mini"
	endpoint      string // OpenAI API endpoint
	client        *http.Client
	temperature   float64
	streamTimeout time.Duration

	// Connection state
	mu sync.RWMutex

	// Translation handling
	translationCallbacks       []TranslationCallback
	beforeTranslationCallbacks []BeforeTranslationCallback

	// Rate limiting
	rateLimiter *rate.Limiter

	// Caching
	cache   map[string]*translationCacheEntry
	cacheMu sync.RWMutex

	// Circuit breaker
	breaker *circuitBreaker

	// Metrics
	metrics *apiMetrics

	// Statistics
	stats *TranslationStats
}

// TranslationCallback is called when translation events occur.
type TranslationCallback func(event TranslationEvent)

// BeforeTranslationCallback is called before translation processing starts.
// It can modify the MediaData, particularly metadata, to inject target languages or other data.
type BeforeTranslationCallback func(data *MediaData)

// TranslationEvent represents a translation event.
type TranslationEvent struct {
	Type         string            // "translation", "error"
	Translations map[string]string // targetLang -> translatedText
	SourceText   string            // Original transcription
	SourceLang   string            // Speaking language
	Timestamp    time.Time
	IsFinal      bool // Matches transcription finality
	Error        error
}

// Translation cache entry
type translationCacheEntry struct {
	translations map[string]string
	timestamp    time.Time
}

// Circuit breaker states
type circuitState int

const (
	circuitClosed circuitState = iota
	circuitOpen
	circuitHalfOpen
)

// Circuit breaker for API calls
type circuitBreaker struct {
	mu              sync.RWMutex
	state           circuitState
	failureCount    int
	lastFailureTime time.Time
	nextRetryTime   time.Time
}

// API call metrics with streaming support
type apiMetrics struct {
	mu                  sync.RWMutex
	totalCalls          uint64
	successfulCalls     uint64
	failedCalls         uint64
	totalLatencyMs      float64
	latencyBuckets      []float64
	bucketIndex         int
	circuitBreakerTrips uint64
	rateLimitExceeded   uint64
	// Streaming-specific metrics
	timeToFirstTokenBuckets []float64
	ttftBucketIndex         int
	streamingErrors         uint64
	connectionErrors        uint64
}

// OpenAI Streaming API structures
type OpenAIChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAI streaming response structures
type OpenAIChatResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Streaming response chunk
type OpenAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// TranslationStats tracks translation performance metrics with streaming support.
type TranslationStats struct {
	mu sync.RWMutex

	// Translation metrics
	TotalTranslations      uint64
	SuccessfulTranslations uint64
	FailedTranslations     uint64
	SkippedSameLanguage    uint64

	// Performance metrics
	AverageLatencyMs        float64
	AverageTimeToFirstToken float64
	LastTranslationAt       time.Time
	BytesTranslated         uint64
	// Streaming metrics
	StreamingErrors  uint64
	ConnectionErrors uint64
	ChunksReceived   uint64
}

// NewTranslationStage creates a new translation stage with default OpenAI endpoint.
//
// Parameters:
//   - name: Unique identifier for this stage
//   - priority: Execution order (should be 30, after BroadcastStage)
//   - apiKey: OpenAI API key for authentication
func NewTranslationStage(name string, priority int, apiKey string) *TranslationStage {
	return NewTranslationStageWithEndpoint(name, priority, apiKey, OpenAIStreamingEndpoint)
}

// NewTranslationStageWithEndpoint creates a new translation stage with custom endpoint.
//
// Parameters:
//   - name: Unique identifier for this stage
//   - priority: Execution order (should be 30, after BroadcastStage)
//   - apiKey: OpenAI API key for authentication
//   - endpoint: Custom API endpoint (useful for testing or alternative providers)
func NewTranslationStageWithEndpoint(name string, priority int, apiKey string, endpoint string) *TranslationStage {
	return &TranslationStage{
		name:          name,
		priority:      priority,
		apiKey:        apiKey,
		model:         DefaultModel,
		endpoint:      endpoint,
		temperature:   DefaultTemperature,
		streamTimeout: StreamReadTimeout,
		client: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
		translationCallbacks:       make([]TranslationCallback, 0),
		beforeTranslationCallbacks: make([]BeforeTranslationCallback, 0),
		rateLimiter:                rate.NewLimiter(DefaultRateLimit, DefaultBurstSize),
		cache:                      make(map[string]*translationCacheEntry),
		breaker: &circuitBreaker{
			state: circuitClosed,
		},
		metrics: &apiMetrics{
			latencyBuckets:          make([]float64, LatencyBuckets),
			timeToFirstTokenBuckets: make([]float64, TimeToFirstTokenBuckets),
		},
		stats: &TranslationStats{},
	}
}

// GetName implements MediaPipelineStage.
func (ts *TranslationStage) GetName() string { return ts.name }

// GetPriority implements MediaPipelineStage.
func (ts *TranslationStage) GetPriority() int { return ts.priority }

// CanProcess implements MediaPipelineStage. Only processes audio with transcriptions.
func (ts *TranslationStage) CanProcess(mediaType MediaType) bool {
	return mediaType == MediaTypeAudio
}

// Process implements MediaPipelineStage.
//
// Extracts transcription from metadata, translates to all participant languages,
// and broadcasts the translations.
func (ts *TranslationStage) Process(ctx context.Context, input MediaData) (output MediaData, err error) {
	// Recover from any panics to prevent crashing the pipeline
	defer func() {
		if r := recover(); r != nil {
			log.Printf("recovered from panic in TranslationStage: %v", r)
			output = input
			err = nil // Don't fail the pipeline
		}
	}()

	// Call before translation callbacks to allow modification of input data
	ts.callBeforeTranslationCallbacks(&input)

	// Check if we have transcription data
	if input.Metadata == nil {
		return input, nil
	}

	// Extract transcription event
	transcriptionEvent, ok := input.Metadata["transcription_event"].(TranscriptionEvent)
	if !ok || transcriptionEvent.Text == "" {
		// No transcription to translate
		return input, nil
	}

	// Get source language from transcription
	sourceLang := transcriptionEvent.Language
	if sourceLang == "" {
		// Can't translate without knowing source language
		return input, nil
	}

	// Get target languages from metadata (provided by room language registry)
	var targetLangs []string
	if metaTargetLangs, ok := input.Metadata["target_languages"].([]string); ok && len(metaTargetLangs) > 0 {
		// Filter out source language from metadata languages
		targetLangs = make([]string, 0, len(metaTargetLangs))
		for _, lang := range metaTargetLangs {
			if lang != sourceLang && lang != "" {
				targetLangs = append(targetLangs, lang)
			}
		}
	}

	// If no translation needed, pass through
	if len(targetLangs) == 0 {
		ts.stats.mu.Lock()
		ts.stats.SkippedSameLanguage++
		ts.stats.mu.Unlock()
		return input, nil
	}

	// Translate to all target languages using Streaming API
	startTime := time.Now()
	translations, err := ts.translateViaStreaming(ctx, transcriptionEvent.Text, sourceLang, targetLangs)
	translationDuration := time.Since(startTime)

	// Update statistics
	ts.updateStats(len(translations), err != nil, translationDuration, len(transcriptionEvent.Text))

	if err != nil {
		getLogger := logger.GetLogger()
		getLogger.Errorw("translation failed", err,
			"sourceLang", sourceLang,
			"targetLangs", targetLangs)
		// Continue without translations
		return input, nil
	}

	// Update the transcription event with translation data
	transcriptionEvent.Translations = translations

	// Update the transcription event in metadata for BroadcastStage
	input.Metadata["transcription_event"] = transcriptionEvent

	// Notify callbacks with unified transcription event (now includes translations)
	ts.notifyTranslation(TranslationEvent{
		Type:         "translation",
		Translations: translations,
		SourceText:   transcriptionEvent.Text,
		SourceLang:   sourceLang,
		Timestamp:    time.Now(),
		IsFinal:      transcriptionEvent.IsFinal,
	})

	return input, nil
}

// translateViaStreaming translates text to multiple languages using OpenAI Streaming API.
func (ts *TranslationStage) translateViaStreaming(ctx context.Context, text, sourceLang string, targetLangs []string) (map[string]string, error) {
	if len(targetLangs) == 0 {
		return make(map[string]string), nil
	}

	// Input validation
	if len(text) > MaxInputTextSize {
		return nil, fmt.Errorf("input text too large: %d characters (max %d)", len(text), MaxInputTextSize)
	}
	if len(targetLangs) > MaxTargetLanguages {
		return nil, fmt.Errorf("too many target languages: %d (max %d)", len(targetLangs), MaxTargetLanguages)
	}
	if strings.TrimSpace(text) == "" {
		return make(map[string]string), nil
	}

	// Check cache first
	cacheKey := ts.generateCacheKey(text, sourceLang, targetLangs)
	if cached := ts.getCachedTranslation(cacheKey); cached != nil {
		return cached, nil
	}

	// Create multi-language translation prompt
	targetLangList := strings.Join(targetLangs, ", ")
	prompt := fmt.Sprintf(`You are a professional translator. Translate the given text accurately to multiple languages while preserving meaning and context.

IMPORTANT INSTRUCTIONS:
- Respond with ONLY a valid JSON object
- Use the exact format shown below with language codes as keys
- Do not include any explanation, markdown, or additional text
- Preserve the original meaning, tone, and context
- Use ISO 639-1 language codes as keys (e.g., "en", "es", "fr")

REQUIRED FORMAT:
{"en": "English translation", "es": "Spanish translation", "fr": "French translation"}

TRANSLATION TASK:
- Source language: %s
- Target languages: %s
- Original text: %s

JSON RESPONSE:`, sourceLang, targetLangList, text)

	// Check circuit breaker
	if !ts.canMakeAPICall() {
		return nil, fmt.Errorf("circuit breaker is open, API temporarily unavailable")
	}

	// Apply rate limiting
	if !ts.rateLimiter.Allow() {
		ts.recordRateLimitExceeded()
		return nil, fmt.Errorf("rate limit exceeded, please try again later")
	}

	// Call OpenAI Streaming API - single attempt for real-time performance
	responseText, err := ts.callOpenAIWithMetrics(ctx, prompt)
	if err != nil {
		ts.recordAPIFailure()
		// Record specific streaming errors
		if strings.Contains(err.Error(), "stream") || strings.Contains(err.Error(), "connection") {
			ts.recordStreamingError(err)
		}
		return nil, fmt.Errorf("OpenAI streaming API call failed: %w", err)
	}
	ts.recordAPISuccess()

	// Parse JSON response
	jsonText := ts.extractJSONFromResponse(responseText)
	translations := make(map[string]string)
	if err := json.Unmarshal([]byte(jsonText), &translations); err != nil {
		getLogger := logger.GetLogger()
		getLogger.Errorw("Failed to parse translation response", err, "response", responseText)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Filter to only include requested target languages
	filteredTranslations := make(map[string]string)
	for _, lang := range targetLangs {
		if translation, exists := translations[lang]; exists && strings.TrimSpace(translation) != "" {
			filteredTranslations[lang] = translation
		}
	}

	// Cache the result
	ts.cacheTranslation(cacheKey, filteredTranslations)

	return filteredTranslations, nil
}

// notifyTranslation notifies all registered callbacks of a translation event.
func (ts *TranslationStage) notifyTranslation(event TranslationEvent) {
	ts.mu.RLock()
	callbacks := make([]TranslationCallback, len(ts.translationCallbacks))
	copy(callbacks, ts.translationCallbacks)
	ts.mu.RUnlock()

	for _, callback := range callbacks {
		// Call in goroutine to prevent blocking
		go callback(event)
	}
}

// AddTranslationCallback adds a callback for translation events.
func (ts *TranslationStage) AddTranslationCallback(callback TranslationCallback) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.translationCallbacks = append(ts.translationCallbacks, callback)
}

// AddBeforeTranslationCallback adds a callback that runs before translation processing.
func (ts *TranslationStage) AddBeforeTranslationCallback(callback BeforeTranslationCallback) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.beforeTranslationCallbacks = append(ts.beforeTranslationCallbacks, callback)
}

// callBeforeTranslationCallbacks calls all registered before translation callbacks.
func (ts *TranslationStage) callBeforeTranslationCallbacks(data *MediaData) {
	ts.mu.RLock()
	callbacks := make([]BeforeTranslationCallback, len(ts.beforeTranslationCallbacks))
	copy(callbacks, ts.beforeTranslationCallbacks)
	ts.mu.RUnlock()

	for _, callback := range callbacks {
		// Call callback synchronously since it needs to modify data before processing
		callback(data)
	}
}

// updateStats updates translation statistics.
func (ts *TranslationStage) updateStats(translationCount int, failed bool, duration time.Duration, textSize int) {
	ts.stats.mu.Lock()
	defer ts.stats.mu.Unlock()

	ts.stats.TotalTranslations++

	if failed {
		ts.stats.FailedTranslations++
	} else {
		ts.stats.SuccessfulTranslations += uint64(translationCount)
		ts.stats.BytesTranslated += uint64(textSize * translationCount)
		ts.stats.LastTranslationAt = time.Now()

		// Update average latency (simple moving average)
		latencyMs := float64(duration.Microseconds()) / 1000.0
		if ts.stats.AverageLatencyMs == 0 {
			ts.stats.AverageLatencyMs = latencyMs
		} else {
			ts.stats.AverageLatencyMs = (ts.stats.AverageLatencyMs * 0.9) + (latencyMs * 0.1)
		}
	}
}

// GetStats returns current translation statistics including streaming metrics.
func (ts *TranslationStage) GetStats() TranslationStats {
	ts.stats.mu.RLock()
	defer ts.stats.mu.RUnlock()

	return TranslationStats{
		TotalTranslations:       ts.stats.TotalTranslations,
		SuccessfulTranslations:  ts.stats.SuccessfulTranslations,
		FailedTranslations:      ts.stats.FailedTranslations,
		SkippedSameLanguage:     ts.stats.SkippedSameLanguage,
		AverageLatencyMs:        ts.stats.AverageLatencyMs,
		AverageTimeToFirstToken: ts.stats.AverageTimeToFirstToken,
		LastTranslationAt:       ts.stats.LastTranslationAt,
		BytesTranslated:         ts.stats.BytesTranslated,
		StreamingErrors:         ts.stats.StreamingErrors,
		ConnectionErrors:        ts.stats.ConnectionErrors,
		ChunksReceived:          ts.stats.ChunksReceived,
	}
}

// Disconnect is a no-op for streaming API implementation (no persistent connections).
func (ts *TranslationStage) Disconnect() {
	// No persistent connections to clean up with streaming API - each request creates its own connection
}

// callOpenAIWithMetrics makes a streaming call to the OpenAI API with metrics tracking.
func (ts *TranslationStage) callOpenAIWithMetrics(ctx context.Context, prompt string) (string, error) {
	start := time.Now()
	var firstTokenTime time.Time

	result, err := ts.callOpenAIStreaming(ctx, prompt, func(isFirstToken bool) {
		if isFirstToken {
			firstTokenTime = time.Now()
		}
	})

	// Record metrics
	totalLatency := time.Since(start)
	ts.recordAPILatency(totalLatency)

	if !firstTokenTime.IsZero() {
		timeToFirstToken := firstTokenTime.Sub(start)
		ts.recordTimeToFirstToken(timeToFirstToken)
	}

	return result, err
}

// callOpenAIStreaming makes a streaming call to the OpenAI API using Server-Sent Events.
func (ts *TranslationStage) callOpenAIStreaming(ctx context.Context, prompt string, onFirstToken func(bool)) (string, error) {
	request := OpenAIChatRequest{
		Model:       ts.model,
		Temperature: ts.temperature,
		Stream:      true, // Enable streaming
		Messages: []ChatMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ts.apiKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := ts.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	return ts.readStreamingResponse(ctx, resp, onFirstToken)
}

// readStreamingResponse reads and processes the streaming response from OpenAI.
func (ts *TranslationStage) readStreamingResponse(ctx context.Context, resp *http.Response, onFirstToken func(bool)) (string, error) {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, StreamBufferSize), StreamBufferSize)

	var result strings.Builder
	firstToken := true
	chunksReceived := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		line := scanner.Text()

		// Skip empty lines and non-data lines
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		// Extract the JSON data after "data: "
		data := strings.TrimPrefix(line, "data: ")

		// Check for stream termination
		if data == "[DONE]" {
			break
		}

		// Parse the streaming chunk
		var chunk OpenAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			getLogger := logger.GetLogger()
			getLogger.Debugw("Failed to parse stream chunk", "error", err, "data", data)
			continue
		}

		// Handle API errors in stream
		if chunk.Error != nil {
			return "", fmt.Errorf("OpenAI streaming error: %s", chunk.Error.Message)
		}

		// Extract content from the first choice
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			content := chunk.Choices[0].Delta.Content

			// Notify about first token
			if firstToken && onFirstToken != nil {
				onFirstToken(true)
				firstToken = false
			}

			result.WriteString(content)
			chunksReceived++

			// Safety check to prevent infinite streams
			if chunksReceived > MaxStreamChunks {
				return "", fmt.Errorf("stream exceeded maximum chunks limit (%d)", MaxStreamChunks)
			}
		}

		// Check for completion
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != nil {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading stream: %w", err)
	}

	// Update streaming metrics
	ts.updateStreamingStats(chunksReceived, nil)

	return result.String(), nil
}

// extractJSONFromResponse extracts JSON from response text, handling markdown code fences.
func (ts *TranslationStage) extractJSONFromResponse(responseText string) string {
	responseText = strings.TrimSpace(responseText)

	// Check if response is wrapped in markdown code fences
	if strings.HasPrefix(responseText, "```json") && strings.HasSuffix(responseText, "```") {
		// Extract JSON from markdown code fences
		lines := strings.Split(responseText, "\n")
		if len(lines) >= 3 {
			// Remove first line (```json) and last line (```)
			jsonLines := lines[1 : len(lines)-1]
			return strings.TrimSpace(strings.Join(jsonLines, "\n"))
		}
	} else if strings.HasPrefix(responseText, "```") && strings.HasSuffix(responseText, "```") {
		// Handle generic code fences without "json" specifier
		lines := strings.Split(responseText, "\n")
		if len(lines) >= 3 {
			jsonLines := lines[1 : len(lines)-1]
			return strings.TrimSpace(strings.Join(jsonLines, "\n"))
		}
	}

	// Return as-is if no code fences
	return responseText
}

// Cache management methods
func (ts *TranslationStage) generateCacheKey(text, sourceLang string, targetLangs []string) string {
	// Create a deterministic cache key
	key := fmt.Sprintf("%s|%s|%s", text, sourceLang, strings.Join(targetLangs, ","))
	hash := sha1.Sum([]byte(key))
	return fmt.Sprintf("%x", hash)
}

func (ts *TranslationStage) getCachedTranslation(cacheKey string) map[string]string {
	ts.cacheMu.RLock()
	defer ts.cacheMu.RUnlock()

	entry, exists := ts.cache[cacheKey]
	if !exists {
		return nil
	}

	// Check if cache entry is still valid
	if time.Since(entry.timestamp) > CacheTTL {
		// Entry is stale, will be cleaned up later
		return nil
	}

	return entry.translations
}

func (ts *TranslationStage) cacheTranslation(cacheKey string, translations map[string]string) {
	ts.cacheMu.Lock()
	defer ts.cacheMu.Unlock()

	// Clean up stale entries if cache is getting full
	if len(ts.cache) >= MaxCacheSize {
		ts.cleanupStaleEntriesLocked()
	}

	// Add new entry
	ts.cache[cacheKey] = &translationCacheEntry{
		translations: translations,
		timestamp:    time.Now(),
	}
}

func (ts *TranslationStage) cleanupStaleEntriesLocked() {
	now := time.Now()
	for key, entry := range ts.cache {
		if now.Sub(entry.timestamp) > CacheTTL {
			delete(ts.cache, key)
		}
	}
}

// Circuit breaker methods
func (ts *TranslationStage) canMakeAPICall() bool {
	ts.breaker.mu.RLock()
	defer ts.breaker.mu.RUnlock()

	switch ts.breaker.state {
	case circuitClosed:
		return true
	case circuitOpen:
		// Check if we should try to recover
		return time.Now().After(ts.breaker.nextRetryTime)
	case circuitHalfOpen:
		return true
	default:
		return false
	}
}

func (ts *TranslationStage) recordAPISuccess() {
	ts.breaker.mu.Lock()
	defer ts.breaker.mu.Unlock()

	ts.breaker.failureCount = 0
	if ts.breaker.state == circuitHalfOpen {
		ts.breaker.state = circuitClosed
	}

	// Record successful API call
	ts.metrics.mu.Lock()
	ts.metrics.successfulCalls++
	ts.metrics.mu.Unlock()
}

func (ts *TranslationStage) recordAPIFailure() {
	ts.breaker.mu.Lock()
	defer ts.breaker.mu.Unlock()

	ts.breaker.failureCount++
	ts.breaker.lastFailureTime = time.Now()

	// Record failed API call
	ts.metrics.mu.Lock()
	ts.metrics.failedCalls++
	ts.metrics.mu.Unlock()

	if ts.breaker.failureCount >= MaxConsecutiveFailures {
		ts.breaker.state = circuitOpen
		ts.breaker.nextRetryTime = time.Now().Add(CircuitBreakerTimeout)

		// Record circuit breaker trip
		ts.metrics.mu.Lock()
		ts.metrics.circuitBreakerTrips++
		ts.metrics.mu.Unlock()
	}
}

// Metrics methods
func (ts *TranslationStage) recordAPILatency(duration time.Duration) {
	ts.metrics.mu.Lock()
	defer ts.metrics.mu.Unlock()

	latencyMs := float64(duration.Nanoseconds()) / 1e6

	// Update total calls
	ts.metrics.totalCalls++
	ts.metrics.totalLatencyMs += latencyMs

	// Update rolling average using circular buffer
	ts.metrics.latencyBuckets[ts.metrics.bucketIndex] = latencyMs
	ts.metrics.bucketIndex = (ts.metrics.bucketIndex + 1) % len(ts.metrics.latencyBuckets)
}

func (ts *TranslationStage) recordRateLimitExceeded() {
	ts.metrics.mu.Lock()
	defer ts.metrics.mu.Unlock()
	ts.metrics.rateLimitExceeded++
}

// GetAPIMetrics returns current API call metrics including streaming metrics.
func (ts *TranslationStage) GetAPIMetrics() map[string]any {
	ts.metrics.mu.RLock()
	defer ts.metrics.mu.RUnlock()

	// Calculate average latency from rolling buckets
	var avgLatency float64
	validBuckets := 0
	for _, latency := range ts.metrics.latencyBuckets {
		if latency > 0 {
			avgLatency += latency
			validBuckets++
		}
	}
	if validBuckets > 0 {
		avgLatency /= float64(validBuckets)
	}

	// Calculate average time-to-first-token from rolling buckets
	var avgTTFT float64
	validTTFTBuckets := 0
	for _, ttft := range ts.metrics.timeToFirstTokenBuckets {
		if ttft > 0 {
			avgTTFT += ttft
			validTTFTBuckets++
		}
	}
	if validTTFTBuckets > 0 {
		avgTTFT /= float64(validTTFTBuckets)
	}

	return map[string]any{
		"total_calls":                    ts.metrics.totalCalls,
		"successful_calls":               ts.metrics.successfulCalls,
		"failed_calls":                   ts.metrics.failedCalls,
		"average_latency_ms":             avgLatency,
		"average_time_to_first_token_ms": avgTTFT,
		"streaming_errors":               ts.metrics.streamingErrors,
		"connection_errors":              ts.metrics.connectionErrors,
		"circuit_breaker_trips":          ts.metrics.circuitBreakerTrips,
		"rate_limit_exceeded":            ts.metrics.rateLimitExceeded,
		"cache_entries":                  len(ts.cache),
	}
}

// updateStreamingStats updates streaming-specific statistics.
func (ts *TranslationStage) updateStreamingStats(chunksReceived int, streamErr error) {
	ts.stats.mu.Lock()
	defer ts.stats.mu.Unlock()

	ts.stats.ChunksReceived += uint64(chunksReceived)

	if streamErr != nil {
		ts.stats.StreamingErrors++
	}
}

// recordTimeToFirstToken records the time-to-first-token metric.
func (ts *TranslationStage) recordTimeToFirstToken(duration time.Duration) {
	ts.metrics.mu.Lock()
	defer ts.metrics.mu.Unlock()

	ttftMs := float64(duration.Nanoseconds()) / 1e6

	// Update rolling average using circular buffer
	ts.metrics.timeToFirstTokenBuckets[ts.metrics.ttftBucketIndex] = ttftMs
	ts.metrics.ttftBucketIndex = (ts.metrics.ttftBucketIndex + 1) % len(ts.metrics.timeToFirstTokenBuckets)

	// Update stats average
	ts.stats.mu.Lock()
	if ts.stats.AverageTimeToFirstToken == 0 {
		ts.stats.AverageTimeToFirstToken = ttftMs
	} else {
		ts.stats.AverageTimeToFirstToken = (ts.stats.AverageTimeToFirstToken * 0.9) + (ttftMs * 0.1)
	}
	ts.stats.mu.Unlock()
}

// recordStreamingError records streaming-specific errors.
func (ts *TranslationStage) recordStreamingError(err error) {
	ts.metrics.mu.Lock()
	defer ts.metrics.mu.Unlock()

	ts.metrics.streamingErrors++

	if strings.Contains(err.Error(), "connection") {
		ts.metrics.connectionErrors++
	}
}
