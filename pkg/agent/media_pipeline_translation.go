package agent

import (
	"bufio"
	"bytes"
	"context"
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
	// OpenAI Streaming API Configuration
	openAIStreamingEndpoint = "https://api.openai.com/v1/chat/completions"
	defaultModel            = "gpt-4.1-nano" // Fastest model for translation (35% faster than gpt-4o-mini)
	defaultTemperature      = 0.0

	// Streaming Configuration
	streamReadTimeout = 15 * time.Second
	streamBufferSize  = 8192
	maxStreamChunks   = 1000

	// Input Validation
	maxInputTextSize   = 4000 // characters
	maxTargetLanguages = 20

	// Rate Limiting (requests per second)
	defaultRateLimit = rate.Limit(10) // 10 requests per second
	defaultBurstSize = 20

	// Circuit Breaker Configuration
	maxConsecutiveFailures = 5
	circuitBreakerTimeout  = 30 * time.Second

	// Metrics
	latencyBuckets = 10 // For moving average calculation

	// Streaming Metrics
	timeToFirstTokenBuckets = 10
)

// TranslationConfig configures the TranslationStage.
type TranslationConfig struct {
	Name         string        // Unique identifier for this stage
	Priority     int           // Execution order (should be 30, after BroadcastStage)
	APIKey       string        // OpenAI API key for authentication
	Model        string        // Model to use (empty for default)
	Temperature  float64       // Temperature setting (empty for default)
	Endpoint     string        // API endpoint (empty for default)
	Timeout      time.Duration // Stream timeout (empty for default)
	EnableWarmup bool          // If true, performs async warm-up call on initialization
}

// TranslationStage translates transcriptions to multiple participant languages using OpenAI Streaming API.
//
// This stage runs AFTER BroadcastStage and translates already-broadcast transcriptions
// to all unique participant translation languages. It uses OpenAI's Streaming API
// with gpt-4.1-nano model for real-time translation responses via Server-Sent Events.
//
// Key features:
//   - Dynamic participant language tracking
//   - Multi-language single-call streaming translation
//   - Smart filtering to avoid unnecessary translations
//   - Streaming API integration with gpt-4.1-nano for fastest responses
//   - Real-time single-attempt translation for immediate responses
//   - No retry logic to maintain real-time performance
//   - Automatic translation broadcasting
//
// Pipeline position:
//  1. RealtimeTranscriptionStage (priority 10) - Generates transcriptions
//  2. BroadcastStage (priority 20) - Broadcasts original transcriptions
//  3. TranslationStage (priority 30) - Translates and broadcasts translations
type TranslationStage struct {
	config *TranslationConfig

	// HTTP client
	client *http.Client

	// Connection state
	mu sync.RWMutex

	// Translation handling
	translationCallbacks       []TranslationCallback
	beforeTranslationCallbacks []BeforeTranslationCallback

	// Rate limiting
	rateLimiter *rate.Limiter

	// Circuit breaker
	breaker *circuitBreaker

	// Metrics
	metrics *apiMetrics

	// Statistics
	stats *TranslationStats
}

// TranslationCallback is called when translation events occur.
type TranslationCallback func(event TranscriptionEvent)

// BeforeTranslationCallback is called before translation processing starts.
// It can modify the MediaData, particularly metadata, to inject target languages or other data.
type BeforeTranslationCallback func(data *MediaData)

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

// NewTranslationStage creates a new translation stage with OpenAI configuration.
//
// Parameters:
//   - name: Unique identifier for this stage
//   - priority: Execution order (should be 30, after BroadcastStage)
//   - apiKey: OpenAI API key for authentication
//   - model: OpenAI model to use (empty for default "gpt-4.1-nano")
func NewTranslationStage(config *TranslationConfig) *TranslationStage {
	// Apply defaults
	if config.Model == "" {
		config.Model = defaultModel
	}
	if config.Temperature == 0 {
		config.Temperature = defaultTemperature
	}
	if config.Endpoint == "" {
		config.Endpoint = openAIStreamingEndpoint
	}
	if config.Timeout == 0 {
		config.Timeout = streamReadTimeout
	}

	stage := &TranslationStage{
		config:                     config,
		client:                     getSharedHTTPClient(),
		translationCallbacks:       make([]TranslationCallback, 0),
		beforeTranslationCallbacks: make([]BeforeTranslationCallback, 0),
		rateLimiter:                rate.NewLimiter(defaultRateLimit, defaultBurstSize),
		breaker: &circuitBreaker{
			state: circuitClosed,
		},
		metrics: &apiMetrics{
			latencyBuckets:          make([]float64, latencyBuckets),
			timeToFirstTokenBuckets: make([]float64, timeToFirstTokenBuckets),
		},
		stats: &TranslationStats{},
	}

	// Perform async warm-up to establish connection and eliminate cold start
	if config.EnableWarmup {
		go stage.warmUp()
	}

	return stage
}

// GetName implements MediaPipelineStage.
func (ts *TranslationStage) GetName() string { return ts.config.Name }

// GetPriority implements MediaPipelineStage.
func (ts *TranslationStage) GetPriority() int { return ts.config.Priority }

// SetEndpoint sets the translation API endpoint for testing purposes.
func (ts *TranslationStage) SetEndpoint(endpoint string) {
	ts.config.Endpoint = endpoint
}

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

	// For partial transcriptions, only process if text is substantial (>30 characters)
	// This enables faster streaming while avoiding processing very short fragments
	if !transcriptionEvent.IsFinal {
		textLength := len(strings.TrimSpace(transcriptionEvent.Text))
		if textLength < 30 {
			return input, nil
		}
		// Mark as partial for downstream stages
		if input.Metadata == nil {
			input.Metadata = make(map[string]interface{})
		}
		input.Metadata["from_partial_transcription"] = true
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
	ts.notifyTranslation(transcriptionEvent)

	return input, nil
}

// translateViaStreaming translates text to multiple languages using OpenAI Streaming API.
func (ts *TranslationStage) translateViaStreaming(ctx context.Context, text, sourceLang string, targetLangs []string) (map[string]string, error) {
	if len(targetLangs) == 0 {
		return make(map[string]string), nil
	}

	// Input validation
	if len(text) > maxInputTextSize {
		return nil, fmt.Errorf("input text too large: %d characters (max %d)", len(text), maxInputTextSize)
	}
	if len(targetLangs) > maxTargetLanguages {
		return nil, fmt.Errorf("too many target languages: %d (max %d)", len(targetLangs), maxTargetLanguages)
	}
	if strings.TrimSpace(text) == "" {
		return make(map[string]string), nil
	}

	// Create optimized multi-language translation prompt
	targetLangList := strings.Join(targetLangs, ", ")
	prompt := fmt.Sprintf(`Translate to %s: %s
JSON format with language codes.`, targetLangList, text)

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

	return filteredTranslations, nil
}

// notifyTranslation notifies all registered callbacks of a translation event.
func (ts *TranslationStage) notifyTranslation(event TranscriptionEvent) {
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
		latencyMs := float64(duration.Milliseconds())
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

// warmUp performs a small translation to establish HTTP connection and warm up the API.
// This eliminates the cold start delay (typically 500-1000ms) on the first real translation.
// The warm-up translation is not counted in statistics.
func (ts *TranslationStage) warmUp() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	getLogger := logger.GetLogger()
	getLogger.Infow("Starting translation stage warm-up")

	// Perform a small test translation to establish connection
	_, err := ts.translateViaStreaming(ctx, "Hello", "en", []string{"es"})
	if err != nil {
		getLogger.Debugw("Translation stage warm-up failed (non-critical)", "error", err)
	} else {
		getLogger.Infow("Translation stage warm-up completed successfully")
	}

	// Reset statistics to exclude warm-up call from metrics
	ts.stats.mu.Lock()
	ts.stats.TotalTranslations = 0
	ts.stats.SuccessfulTranslations = 0
	ts.stats.FailedTranslations = 0
	ts.stats.AverageLatencyMs = 0
	ts.stats.AverageTimeToFirstToken = 0
	ts.stats.BytesTranslated = 0
	ts.stats.mu.Unlock()
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
		Model:       ts.config.Model,
		Temperature: ts.config.Temperature,
		Stream:      true, // Enable streaming
		MaxTokens:   500,  // Limit response length for faster generation
		Messages: []ChatMessage{
			{
				Role:    "system",
				Content: "You are a translation engine. Output only JSON with language codes as keys. Be concise.",
			},
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.config.Endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ts.config.APIKey)
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
	scanner.Buffer(make([]byte, streamBufferSize), streamBufferSize)

	var result strings.Builder
	result.Grow(1024) // Pre-allocate capacity for typical JSON response
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
			if chunksReceived > maxStreamChunks {
				return "", fmt.Errorf("stream exceeded maximum chunks limit (%d)", maxStreamChunks)
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

	// Fast path: if response starts with {, it's already JSON
	if strings.HasPrefix(responseText, "{") {
		return responseText
	}

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

	if ts.breaker.failureCount >= maxConsecutiveFailures {
		ts.breaker.state = circuitOpen
		ts.breaker.nextRetryTime = time.Now().Add(circuitBreakerTimeout)

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

	latencyMs := float64(duration.Milliseconds())

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

	ttftMs := float64(duration.Milliseconds())

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
