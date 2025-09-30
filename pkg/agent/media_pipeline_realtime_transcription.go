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
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// RealtimeTranscriptionStage connects to OpenAI Realtime API via WebRTC for audio transcription.
//
// This stage implements the OpenAI Realtime API WebRTC integration pattern:
//   - Establishes WebRTC connection with OpenAI's servers
//   - Routes incoming audio packets to the Realtime API
//   - Receives transcribed text as a stream via data channel
//   - Supports real-time, low-latency transcription
//
// The implementation follows OpenAI's Realtime API specifications:
// https://platform.openai.com/docs/guides/realtime-transcription
//
// Key features:
//   - WebRTC peer connection with OpenAI servers
//   - Opus audio codec support (required by Realtime API)
//   - Real-time streaming transcription results
//   - Automatic reconnection on connection failure
//   - Transcription event callbacks
//   - Support for multiple languages and models
type RealtimeTranscriptionStage struct {
	name     string
	priority int

	// Configuration
	config                   *RealtimeTranscriptionConfig
	audioTranscriptionConfig *AudioTranscriptionConfig
	turnDetection            *TurnDetection

	// Connection state
	mu             sync.RWMutex
	peerConnection *webrtc.PeerConnection
	dataChannel    *webrtc.DataChannel
	audioTrack     *webrtc.TrackLocalStaticRTP

	// Ephemeral key for WebRTC
	ephemeralKey string

	// Transcription handling
	transcriptionCallbacks       []TranscriptionCallback
	beforeTranscriptionCallbacks []BeforeTranscriptionCallback
	eventChan                    chan RealtimeEvent
	closeChan                    chan struct{}
	closeOnce                    sync.Once
	wg                           sync.WaitGroup

	// Connection state
	connected     bool
	connecting    bool
	lastConnected time.Time

	// Statistics
	stats *RealtimeTranscriptionStats

	// Latest transcription for pipeline output
	latestTranscription *TranscriptionEvent

	// RTP packet tracking
	sequenceNumber uint16
	timestampBase  uint32
	ssrc           uint32
}

type NoiseReduction struct {
	Type string `json:"type,omitempty"`
}

type AudioTranscriptionConfig struct {
	Model    string `json:"model,omitempty"`
	Language string `json:"language,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

type TurnDetection struct {
	Type              string  `json:"type,omitempty"`
	Threshold         float64 `json:"threshold,omitempty"`
	SilenceDurationMs float64 `json:"silence_duration_ms,omitempty"`
	PrefixPaddingMs   float64 `json:"prefix_padding_ms,omitempty"`
	InterruptResponse bool    `json:"interrupt_response,omitempty"`
	Eagerness         string  `json:"eagerness,omitempty"`
	CreateResponse    bool    `json:"create_response,omitempty"`
}

// RealtimeTranscriptionConfig contains configuration for creating a RealtimeTranscriptionStage.
type RealtimeTranscriptionConfig struct {
	Name      string         // Unique identifier for this stage
	Priority  int            // Execution order (lower runs first)
	APIKey    string         // OpenAI API key for authentication
	Model     string         // Model to use (defaults to "gpt-4o-mini-transcribe" - fastest and most cost-effective)
	Language  string         // Language code (e.g., "en", "ru", "zh", "ar")
	Prompt    string         // Context prompt for better transcription accuracy
	Voice     string         // Voice to use (e.g., "alloy", "echo", "fable", "onyx", "nova", "shimmer")
	VADConfig *TurnDetection // Optional VAD configuration (auto-configured if nil)
}

// TranscriptionCallback is called when transcription events occur.
type TranscriptionCallback func(event TranscriptionEvent)

// BeforeTranscriptionCallback is called before transcription processing starts.
// It can modify the MediaData, particularly metadata, to inject participant data or other information.
type BeforeTranscriptionCallback func(data *MediaData)

// TranscriptionEvent represents a transcription event from the Realtime API.
// This unified event structure is used by both transcription and translation stages.
type TranscriptionEvent struct {
	Type      string    `json:"type,omitempty"`      // "partial", "final", "error"
	Text      string    `json:"text,omitempty"`      // Transcribed text
	Timestamp time.Time `json:"timestamp,omitempty"` // When the transcription was received
	Language  string    `json:"language,omitempty"`  // Detected language (if available)
	IsFinal   bool      `json:"isFinal"`             // Whether this is a final transcription
	Error     error     `json:"error,omitempty"`     // Error if Type is "error"

	// Translation data (populated by TranslationStage if enabled)
	Translations map[string]string `json:"translations,omitempty"` // targetLang -> translatedText
}

// RealtimeEvent represents an event from OpenAI Realtime API.
type RealtimeEvent struct {
	Type      string                 `json:"type"`
	EventID   string                 `json:"event_id,omitempty"`
	Response  *RealtimeResponse      `json:"response,omitempty"`
	Item      *RealtimeItem          `json:"item,omitempty"`
	Delta     *RealtimeDelta         `json:"delta,omitempty"`
	Error     *RealtimeError         `json:"error,omitempty"`
	Timestamp string                 `json:"timestamp,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"` // Generic data field for raw events
}

// RealtimeResponse represents a response from the Realtime API.
type RealtimeResponse struct {
	ID        string   `json:"id"`
	Object    string   `json:"object"`
	Status    string   `json:"status"`
	Output    []Output `json:"output,omitempty"`
	CreatedAt int64    `json:"created_at"`
}

// Output represents transcription output.
type Output struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Type    string        `json:"type"`
	Content []ContentItem `json:"content,omitempty"`
}

// ContentItem represents a content item in the output.
type ContentItem struct {
	Type       string     `json:"type"`
	Text       string     `json:"text,omitempty"`
	Transcript string     `json:"transcript,omitempty"`
	Audio      *AudioData `json:"audio,omitempty"`
}

// AudioData represents audio data in the content.
type AudioData struct {
	Data       string `json:"data,omitempty"`       // Base64 encoded audio
	Transcript string `json:"transcript,omitempty"` // Transcribed text
}

// RealtimeItem represents an item in the conversation.
type RealtimeItem struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Type    string        `json:"type"`
	Status  string        `json:"status,omitempty"`
	Role    string        `json:"role,omitempty"`
	Content []ContentItem `json:"content,omitempty"`
}

// RealtimeDelta represents incremental updates.
type RealtimeDelta struct {
	Audio      string `json:"audio,omitempty"`
	Text       string `json:"text,omitempty"`
	Transcript string `json:"transcript,omitempty"`
}

// RealtimeError represents an error from the API.
type RealtimeError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RealtimeTranscriptionStats tracks transcription performance metrics.
type RealtimeTranscriptionStats struct {
	mu sync.RWMutex

	// Connection metrics
	ConnectionAttempts  uint64
	ConnectionSuccesses uint64
	ConnectionFailures  uint64
	CurrentlyConnected  bool

	// Transcription metrics
	AudioPacketsSent       uint64
	TranscriptionsReceived uint64
	PartialTranscriptions  uint64
	FinalTranscriptions    uint64
	Errors                 uint64

	// Performance metrics
	AverageLatencyMs    float64
	LastTranscriptionAt time.Time
	BytesTranscribed    uint64

	// Latency tracking
	packetSendTimes map[uint16]time.Time // sequence number -> send time
}

// NewRealtimeTranscriptionStage creates a new OpenAI Realtime transcription stage.
//
// The stage will establish a WebRTC connection to OpenAI's Realtime API
// and stream audio for transcription with optimized settings based on language.
func NewRealtimeTranscriptionStage(config *RealtimeTranscriptionConfig) *RealtimeTranscriptionStage {
	if config == nil {
		panic("config cannot be nil")
	}

	// Apply defaults to config
	if config.Model == "" {
		config.Model = "gpt-4o-mini-transcribe" // Fastest and most cost-effective model
	}
	if config.Voice == "" {
		config.Voice = "alloy" // Default voice
	}

	// Create audio transcription config
	audioConfig := &AudioTranscriptionConfig{
		Model:    config.Model,
		Language: config.Language,
		Prompt:   config.Prompt,
	}

	// Use provided VAD config or create default optimized for the language
	vadConfig := config.VADConfig
	if vadConfig == nil {
		vadConfig = getDefaultVADConfig(config.Language)
	}

	// Generate unique SSRC for this session
	ssrc := uint32(time.Now().UnixNano() & 0xFFFFFFFF)

	return &RealtimeTranscriptionStage{
		name:                         config.Name,
		priority:                     config.Priority,
		config:                       config,
		audioTranscriptionConfig:     audioConfig,
		turnDetection:                vadConfig,
		transcriptionCallbacks:       make([]TranscriptionCallback, 0),
		beforeTranscriptionCallbacks: make([]BeforeTranscriptionCallback, 0),
		eventChan:                    make(chan RealtimeEvent, 100),
		closeChan:                    make(chan struct{}),
		stats: &RealtimeTranscriptionStats{
			packetSendTimes: make(map[uint16]time.Time),
		},
		// RTP packet tracking
		sequenceNumber: 0,
		timestampBase:  uint32(time.Now().UnixNano() / 1000000), // Start with current time in ms
		ssrc:           ssrc,
	}
}

// getDefaultVADConfig returns language-optimized voice activity detection settings
func getDefaultVADConfig(language string) *TurnDetection {
	config := &TurnDetection{
		Type:              "server_vad",
		Threshold:         0.5,
		SilenceDurationMs: 500, // Default 500ms
		PrefixPaddingMs:   300, // Default 300ms padding
		InterruptResponse: false,
		Eagerness:         "",
		CreateResponse:    false,
	}

	// Language-specific optimizations based on speech patterns
	switch language {
	case "ru": // Russian
		// Russian speakers tend to have longer pauses between words
		config.SilenceDurationMs = 800
		config.PrefixPaddingMs = 400
	case "ar": // Arabic
		// Arabic has complex consonant clusters and longer utterances
		config.SilenceDurationMs = 700
		config.PrefixPaddingMs = 350
	case "ja": // Japanese
		// Japanese has different pause patterns and mora timing
		config.SilenceDurationMs = 700
		config.PrefixPaddingMs = 350
	case "ko": // Korean
		// Korean has syllable timing and longer processing pauses
		config.SilenceDurationMs = 650
		config.PrefixPaddingMs = 350
	case "zh": // Chinese (Mandarin)
		// Chinese tonal languages benefit from longer context
		config.SilenceDurationMs = 600
		config.PrefixPaddingMs = 350
	case "hi": // Hindi
		// Hindi has complex consonant clusters
		config.SilenceDurationMs = 600
		config.PrefixPaddingMs = 300
	case "bn": // Bengali
		// Bengali has similar patterns to Hindi
		config.SilenceDurationMs = 600
		config.PrefixPaddingMs = 300
	case "ur": // Urdu
		// Urdu similar to Hindi with Arabic influence
		config.SilenceDurationMs = 650
		config.PrefixPaddingMs = 320
	case "tr": // Turkish
		// Turkish has agglutinative structure with longer words
		config.SilenceDurationMs = 550
		config.PrefixPaddingMs = 280
	case "de": // German
		// German has compound words and different rhythm
		config.SilenceDurationMs = 450
		config.PrefixPaddingMs = 280
	case "es", "pt", "fr", "it": // Romance languages
		// Romance languages often have faster speech with shorter pauses
		config.SilenceDurationMs = 400
		config.PrefixPaddingMs = 250
	case "id": // Indonesian
		// Indonesian has syllable timing
		config.SilenceDurationMs = 450
		config.PrefixPaddingMs = 270
	case "ha": // Hausa
		// Hausa has tonal elements
		config.SilenceDurationMs = 550
		config.PrefixPaddingMs = 300
	case "en": // English
		// English baseline - stress-timed language
		config.SilenceDurationMs = 500
		config.PrefixPaddingMs = 300
	}

	return config
}

// GetName implements MediaPipelineStage.
func (rts *RealtimeTranscriptionStage) GetName() string { return rts.name }

// GetPriority implements MediaPipelineStage.
func (rts *RealtimeTranscriptionStage) GetPriority() int { return rts.priority }

// CanProcess implements MediaPipelineStage. Only processes audio.
func (rts *RealtimeTranscriptionStage) CanProcess(mediaType MediaType) bool {
	return mediaType == MediaTypeAudio
}

// Process implements MediaPipelineStage.
//
// Routes incoming audio data to OpenAI Realtime API for transcription.
// The transcribed text is delivered asynchronously via callbacks and included in output metadata.
func (rts *RealtimeTranscriptionStage) Process(ctx context.Context, input MediaData) (MediaData, error) {
	// Call before transcription callbacks to allow modification of input data
	rts.callBeforeTranscriptionCallbacks(&input)

	// Ensure we're connected (atomic check-and-connect to prevent race conditions)
	rts.mu.Lock()
	shouldConnect := !rts.connected && !rts.connecting
	if shouldConnect {
		rts.connecting = true
	}
	rts.mu.Unlock()

	if shouldConnect {
		if err := rts.connectWithoutLock(ctx); err != nil {
			// Reset connecting flag on failure
			rts.mu.Lock()
			rts.connecting = false
			rts.mu.Unlock()

			getLogger := logger.GetLogger()
			getLogger.Errorw("failed to connect to Realtime API", err)
			// Don't fail the pipeline, just skip transcription
			return input, nil
		}
	}

	// Send audio to OpenAI Realtime API via WebRTC
	if input.Type == MediaTypeAudio && rts.audioTrack != nil {
		// The audio data from LiveKit is already in Opus format
		// WebRTC accepts Opus directly

		// Create RTP packet from Opus audio with proper timestamps
		rts.mu.Lock()
		rts.sequenceNumber++
		seqNum := rts.sequenceNumber
		ssrc := rts.ssrc
		// Calculate timestamp: Opus typically uses 48kHz sample rate
		// For 20ms frames (typical), increment by 960 samples (48000 * 0.02)
		timestamp := rts.timestampBase + uint32(seqNum)*960
		rts.mu.Unlock()

		rtpPacket := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111, // Opus payload type
				SequenceNumber: seqNum,
				Timestamp:      timestamp,
				SSRC:           ssrc,
			},
			Payload: input.Data,
		}

		// Write RTP packet to WebRTC audio track
		if err := rts.audioTrack.WriteRTP(rtpPacket); err != nil {
			getLogger := logger.GetLogger()
			getLogger.Debugw("failed to write audio to WebRTC track", "error", err)
		} else {
			// Record packet send time for latency calculation and update stats
			sendTime := time.Now()
			rts.stats.mu.Lock()
			rts.stats.packetSendTimes[seqNum] = sendTime
			// Keep only recent packets (last 1000) to prevent memory leak
			if len(rts.stats.packetSendTimes) > 1000 {
				// Remove oldest entries
				for seq := range rts.stats.packetSendTimes {
					if seq < seqNum-1000 {
						delete(rts.stats.packetSendTimes, seq)
					}
				}
			}
			rts.stats.AudioPacketsSent++
			rts.stats.BytesTranscribed += uint64(len(input.Data))
			rts.stats.mu.Unlock()
		}
	}

	// Mark as processed by transcription
	if input.Metadata == nil {
		input.Metadata = make(map[string]interface{})
	}
	input.Metadata["realtime_transcribed"] = true
	input.Metadata["transcribed_at"] = time.Now()

	// Include latest transcription in metadata if available (both final and interim)
	rts.mu.Lock()
	if rts.latestTranscription != nil {
		input.Metadata["transcription_event"] = *rts.latestTranscription

		// Clear after including in output to avoid duplicate sends
		rts.latestTranscription = nil
	}
	rts.mu.Unlock()

	return input, nil
}

// Connect establishes WebRTC connection to OpenAI Realtime API.
func (rts *RealtimeTranscriptionStage) Connect(ctx context.Context) error {
	rts.mu.Lock()
	if rts.connected || rts.connecting {
		rts.mu.Unlock()
		return nil
	}
	rts.connecting = true
	rts.mu.Unlock()

	defer func() {
		rts.mu.Lock()
		rts.connecting = false
		rts.mu.Unlock()
	}()

	return rts.connectWithoutLock(ctx)
}

// connectWithoutLock performs the actual connection work without acquiring the mutex.
func (rts *RealtimeTranscriptionStage) connectWithoutLock(ctx context.Context) error {
	// Update stats
	rts.stats.mu.Lock()
	rts.stats.ConnectionAttempts++
	rts.stats.mu.Unlock()

	getLogger := logger.GetLogger()

	// Step 1: Get ephemeral key for WebRTC
	ephemeralKey, err := rts.getEphemeralKey(ctx)
	if err != nil {
		rts.stats.mu.Lock()
		rts.stats.ConnectionFailures++
		rts.stats.mu.Unlock()
		return fmt.Errorf("failed to get ephemeral key: %w", err)
	}
	rts.ephemeralKey = ephemeralKey
	getLogger.Infow("Got ephemeral key for WebRTC connection")

	// Step 2: Create WebRTC peer connection
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		rts.stats.mu.Lock()
		rts.stats.ConnectionFailures++
		rts.stats.mu.Unlock()
		return fmt.Errorf("failed to create peer connection: %w", err)
	}
	rts.peerConnection = pc

	// Step 3: Add audio track for sending Opus audio
	// OpenAI expects specific Opus configuration
	audioCodec := webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeOpus,
		ClockRate:   48000,
		Channels:    2, // OpenAI expects stereo Opus
		SDPFmtpLine: "minptime=10;useinbandfec=1",
	}

	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		audioCodec,
		"audio",
		"audio-stream",
	)
	if err != nil {
		pc.Close()
		return fmt.Errorf("failed to create audio track: %w", err)
	}

	if _, err := pc.AddTrack(audioTrack); err != nil {
		pc.Close()
		return fmt.Errorf("failed to add audio track: %w", err)
	}
	rts.audioTrack = audioTrack

	// Step 4: Create data channel for receiving transcription events
	dataChannel, err := pc.CreateDataChannel("oai-events", &webrtc.DataChannelInit{
		Ordered: &[]bool{true}[0],
	})
	if err != nil {
		pc.Close()
		return fmt.Errorf("failed to create data channel: %w", err)
	}
	rts.dataChannel = dataChannel

	// Handle data channel events
	dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
		rts.handleDataChannelMessage(msg.Data)
	})

	dataChannel.OnOpen(func() {
		getLogger.Infow("OpenAI Realtime data channel opened")
	})

	// Handle connection state changes
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		getLogger.Infow("WebRTC connection state changed", "state", state.String())

		switch state {
		case webrtc.PeerConnectionStateConnected:
			rts.mu.Lock()
			rts.connected = true
			rts.lastConnected = time.Now()
			rts.mu.Unlock()

			rts.stats.mu.Lock()
			rts.stats.ConnectionSuccesses++
			rts.stats.CurrentlyConnected = true
			rts.stats.mu.Unlock()

			fmt.Println("✅ WebRTC connected to OpenAI Realtime API")
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			rts.handleDisconnection()
		}
	})

	// Step 5: Create offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return fmt.Errorf("failed to create offer: %w", err)
	}

	if err := pc.SetLocalDescription(offer); err != nil {
		pc.Close()
		return fmt.Errorf("failed to set local description: %w", err)
	}

	getLogger.Debugw("SDP Offer", "offer", offer.SDP)
	fmt.Printf("📤 SDP Offer created (length: %d)\n", len(offer.SDP))

	// Step 6: Exchange SDP with OpenAI
	answer, err := rts.exchangeSDPWithOpenAI(ctx, offer)
	if err != nil {
		pc.Close()
		return fmt.Errorf("failed to exchange SDP: %w", err)
	}

	getLogger.Debugw("SDP Answer", "answer", answer.SDP)
	fmt.Printf("📥 SDP Answer received (length: %d)\n", len(answer.SDP))

	if err := pc.SetRemoteDescription(*answer); err != nil {
		pc.Close()
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	// Start event processing
	rts.wg.Add(1)
	go rts.processEvents()

	getLogger.Infow("WebRTC connection established with OpenAI Realtime API",
		"model", rts.config.Model,
		"voice", rts.config.Voice)

	return nil
}

type CreateSessionRequest struct {
	InputAudioTranscription *AudioTranscriptionConfig `json:"input_audio_transcription,omitempty"`
	TurnDetection           *TurnDetection            `json:"turn_detection,omitempty"`
}

// getEphemeralKey gets an ephemeral key for WebRTC connection from OpenAI API.
func (rts *RealtimeTranscriptionStage) getEphemeralKey(ctx context.Context) (string, error) {
	// The ephemeral key endpoint for WebRTC is different
	url := "https://api.openai.com/v1/realtime/transcription_sessions"

	// Create session configuration with parameters accepted by OpenAI API
	reqBody := CreateSessionRequest{
		InputAudioTranscription: rts.audioTranscriptionConfig,
		TurnDetection:           rts.turnDetection,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+rts.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "realtime=v1")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			getLogger := logger.GetLogger()
			getLogger.Debugw("failed to close response body", "error", err)
		}
	}()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get ephemeral key (status %d): %s", resp.StatusCode, body)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	getLogger := logger.GetLogger()
	getLogger.Debugw("Ephemeral key response", "result", result)

	// Try different response formats
	if clientSecret, ok := result["client_secret"].(map[string]interface{}); ok {
		if value, ok := clientSecret["value"].(string); ok {
			return value, nil
		}
	}

	// Alternative format
	if clientSecret, ok := result["client_secret"].(string); ok {
		return clientSecret, nil
	}

	// Check if it's directly in the response
	if ephemeralKey, ok := result["ephemeral_key"].(string); ok {
		return ephemeralKey, nil
	}

	return "", fmt.Errorf("no client_secret or ephemeral_key in response: %v", result)
}

// exchangeSDPWithOpenAI exchanges SDP offer/answer with OpenAI.
func (rts *RealtimeTranscriptionStage) exchangeSDPWithOpenAI(ctx context.Context, offer webrtc.SessionDescription) (*webrtc.SessionDescription, error) {
	// According to OpenAI docs, we need to use the ephemeral key endpoint
	// and send the SDP offer in the body
	// For transcription, we always use gpt-4o-transcribe in the URL
	url := "https://api.openai.com/v1/realtime"

	// The SDP should be sent as plain text with proper headers
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(offer.SDP))
	if err != nil {
		return nil, err
	}

	// Use the ephemeral key as authorization
	req.Header.Set("Authorization", "Bearer "+rts.ephemeralKey)
	req.Header.Set("Content-Type", "application/sdp")
	req.Header.Set("OpenAI-Beta", "realtime=v1")

	getLogger := logger.GetLogger()
	getLogger.Debugw("Sending SDP offer", "url", url, "auth", "Bearer "+rts.ephemeralKey[:10]+"...")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			getLogger := logger.GetLogger()
			getLogger.Debugw("failed to close response body", "error", err)
		}
	}()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("failed to exchange SDP (status %d): %s", resp.StatusCode, body)
	}

	// The response should be the SDP answer
	answerSDP := string(body)

	// Check if it's a valid SDP
	if !strings.Contains(answerSDP, "v=0") {
		return nil, fmt.Errorf("invalid SDP answer received: %s", answerSDP)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}

	getLogger.Infow("Received SDP answer", "answer_length", len(answer.SDP))

	// Print codec information from answer
	if strings.Contains(answerSDP, "opus") {
		fmt.Println("✅ OpenAI accepted Opus codec")
	} else {
		fmt.Printf("⚠️ OpenAI SDP doesn't mention Opus codec\n")
	}

	return &answer, nil
}

// Removed WebSocket message handling - using WebRTC data channel only

// handleDataChannelMessage processes messages from the WebRTC data channel.
func (rts *RealtimeTranscriptionStage) handleDataChannelMessage(data []byte) {
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		getLogger := logger.GetLogger()
		getLogger.Debugw("failed to unmarshal data channel message", "error", err)
		return
	}

	msgType, ok := msg["type"].(string)
	if !ok {
		return
	}

	// Log all messages from OpenAI to console
	fmt.Printf("📨 OpenAI Message [%s]: %v\n", msgType, msg)

	getLogger := logger.GetLogger()
	getLogger.Debugw("Received data channel message", "type", msgType, "msg", msg)

	switch msgType {
	case "response.audio_transcript.delta":
		// Partial transcription
		rts.handleTranscriptionDelta(msg)

	case "response.audio_transcript.done":
		// Final transcription
		rts.handleTranscriptionComplete(msg)

	case "conversation.item.created":
		// New conversation item (could contain transcription)
		rts.handleConversationItem(msg)

	case "error":
		// Handle error
		rts.handleError(msg)

	case "session.created", "session.updated":
		// Session configuration confirmed
		fmt.Printf("✅ Session configured: modalities=%v, voice=%v\n",
			msg["session"].(map[string]interface{})["modalities"],
			msg["session"].(map[string]interface{})["voice"])

	case "conversation.item.input_audio_transcription.delta":
		// Handle partial transcriptions from input audio
		if delta, ok := msg["delta"].(string); ok && delta != "" {
			transcriptionTime := time.Now()
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "partial",
				Text:      delta,
				Timestamp: transcriptionTime,
				Language:  rts.audioTranscriptionConfig.Language,
				IsFinal:   false,
			})

			// Update stats using unified method
			rts.updateStats("partial", transcriptionTime, false)
		}

	case "conversation.item.input_audio_transcription.completed":
		// Handle final transcriptions from input audio
		if transcript, ok := msg["transcript"].(string); ok && transcript != "" {
			transcriptionTime := time.Now()
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "final",
				Text:      transcript,
				Timestamp: transcriptionTime,
				Language:  rts.audioTranscriptionConfig.Language,
				IsFinal:   true,
			})

			// Update stats using unified method
			rts.updateStats("final", transcriptionTime, false)
		}

	default:
		// Convert to RealtimeEvent for processing
		event := RealtimeEvent{
			Type: msgType,
			Data: msg,
		}
		select {
		case rts.eventChan <- event:
		default:
			// Event channel full, drop event
			getLogger.Debugw("event channel full, dropping event", "type", event.Type)
		}
	}
}

// handleTranscriptionDelta handles partial transcription updates.
func (rts *RealtimeTranscriptionStage) handleTranscriptionDelta(msg map[string]interface{}) {
	if delta, ok := msg["delta"].(map[string]interface{}); ok {
		if transcript, ok := delta["transcript"].(string); ok && transcript != "" {
			transcriptionTime := time.Now()
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "partial",
				Text:      transcript,
				Timestamp: transcriptionTime,
				Language:  rts.audioTranscriptionConfig.Language,
				IsFinal:   false,
			})

			// Update stats using unified method
			rts.updateStats("partial", transcriptionTime, false)
		}
	}
}

// handleTranscriptionComplete handles final transcription.
func (rts *RealtimeTranscriptionStage) handleTranscriptionComplete(msg map[string]interface{}) {
	if transcript, ok := msg["transcript"].(string); ok && transcript != "" {
		transcriptionTime := time.Now()
		rts.notifyTranscription(TranscriptionEvent{
			Type:      "final",
			Text:      transcript,
			Timestamp: transcriptionTime,
			Language:  rts.audioTranscriptionConfig.Language,
			IsFinal:   true,
		})

		// Update stats using unified method
		rts.updateStats("final", transcriptionTime, false)
	}
}

// handleConversationItem handles new conversation items that may contain transcriptions.
func (rts *RealtimeTranscriptionStage) handleConversationItem(msg map[string]interface{}) {
	if item, ok := msg["item"].(map[string]interface{}); ok {
		if content, ok := item["content"].([]interface{}); ok {
			for _, c := range content {
				if contentItem, ok := c.(map[string]interface{}); ok {
					// Check for transcript in the content
					if transcript, ok := contentItem["transcript"].(string); ok && transcript != "" {
						transcriptionTime := time.Now()
						rts.notifyTranscription(TranscriptionEvent{
							Type:      "final",
							Text:      transcript,
							Timestamp: transcriptionTime,
							Language:  rts.audioTranscriptionConfig.Language,
							IsFinal:   true,
						})

						// Update stats using unified method
						rts.updateStats("final", transcriptionTime, false)
					}
				}
			}
		}
	}
}

// handleError handles error messages from the API.
func (rts *RealtimeTranscriptionStage) handleError(msg map[string]interface{}) {
	errorMsg := "Unknown error"
	if err, ok := msg["error"].(map[string]interface{}); ok {
		if message, ok := err["message"].(string); ok {
			errorMsg = message
		}
	}

	errorTime := time.Now()
	rts.notifyTranscription(TranscriptionEvent{
		Type:      "error",
		Timestamp: errorTime,
		Error:     fmt.Errorf("realtime API error: %s", errorMsg),
	})

	// Update stats using unified method
	rts.updateStats("error", errorTime, true)

	getLogger := logger.GetLogger()
	getLogger.Errorw("Realtime API error", fmt.Errorf("%s", errorMsg))
}

// Removed trackConnectionState - not needed without WebRTC

// handleDisconnection handles cleanup when disconnected.
func (rts *RealtimeTranscriptionStage) handleDisconnection() {
	rts.mu.Lock()
	defer rts.mu.Unlock()

	// Check if already disconnected
	if rts.peerConnection == nil {
		return
	}

	rts.connected = false
	rts.connecting = false

	// Update stats with separate lock
	rts.stats.mu.Lock()
	rts.stats.CurrentlyConnected = false
	rts.stats.mu.Unlock()

	// Clean up WebRTC connection
	if err := rts.peerConnection.Close(); err != nil {
		getLogger := logger.GetLogger()
		getLogger.Debugw("failed to close peer connection", "error", err)
	}
	rts.peerConnection = nil
	rts.audioTrack = nil
	rts.dataChannel = nil
}

// processEvents processes events from the event channel.
func (rts *RealtimeTranscriptionStage) processEvents() {
	defer rts.wg.Done()

	for {
		select {
		case <-rts.closeChan:
			return

		case event := <-rts.eventChan:
			rts.processRealtimeEvent(event)
		}
	}
}

// processRealtimeEvent processes a single Realtime API event.
func (rts *RealtimeTranscriptionStage) processRealtimeEvent(event RealtimeEvent) {
	switch event.Type {
	case "conversation.item.input_audio_transcription.delta":
		//// Handle partial transcriptions from input audio
		//if delta, ok := event.Data["delta"].(string); ok && delta != "" {
		//	rts.notifyTranscription(TranscriptionEvent{
		//		Type:      "partial",
		//		Text:      delta,
		//		Timestamp: time.Now(),
		//		IsFinal:   false,
		//	})
		//	rts.stats.PartialTranscriptions++
		//}

	case "conversation.item.input_audio_transcription.completed":
		// Handle final transcriptions from input audio
		if transcript, ok := event.Data["transcript"].(string); ok && transcript != "" {
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "final",
				Text:      transcript,
				Timestamp: time.Now(),
				Language:  rts.audioTranscriptionConfig.Language,
				IsFinal:   true,
			})
			rts.stats.FinalTranscriptions++
		}

	case "response.audio_transcript.delta":
		if event.Delta != nil && event.Delta.Transcript != "" {
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "partial",
				Text:      event.Delta.Transcript,
				Timestamp: time.Now(),
				Language:  rts.audioTranscriptionConfig.Language,
				IsFinal:   false,
			})
		}

	case "response.audio_transcript.done":
		if event.Item != nil {
			for _, content := range event.Item.Content {
				if content.Transcript != "" {
					rts.notifyTranscription(TranscriptionEvent{
						Type:      "final",
						Text:      content.Transcript,
						Timestamp: time.Now(),
						Language:  rts.audioTranscriptionConfig.Language,
						IsFinal:   true,
					})
				}
			}
		}

	case "error":
		if event.Error != nil {
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "error",
				Timestamp: time.Now(),
				Error:     fmt.Errorf("%s: %s", event.Error.Code, event.Error.Message),
			})
		}
	}
}

// notifyTranscription notifies all registered callbacks of a transcription event.
func (rts *RealtimeTranscriptionStage) notifyTranscription(event TranscriptionEvent) {
	// Store latest transcription for pipeline output
	rts.mu.Lock()
	rts.latestTranscription = &event
	callbacks := make([]TranscriptionCallback, len(rts.transcriptionCallbacks))
	copy(callbacks, rts.transcriptionCallbacks)
	rts.mu.Unlock()

	for _, callback := range callbacks {
		// Call in goroutine to prevent blocking
		go callback(event)
	}
}

// AddTranscriptionCallback adds a callback for transcription events.
func (rts *RealtimeTranscriptionStage) AddTranscriptionCallback(callback TranscriptionCallback) {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	rts.transcriptionCallbacks = append(rts.transcriptionCallbacks, callback)
}

// AddBeforeTranscriptionCallback adds a callback that runs before transcription processing.
func (rts *RealtimeTranscriptionStage) AddBeforeTranscriptionCallback(callback BeforeTranscriptionCallback) {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	rts.beforeTranscriptionCallbacks = append(rts.beforeTranscriptionCallbacks, callback)
}

// callBeforeTranscriptionCallbacks calls all registered before transcription callbacks.
func (rts *RealtimeTranscriptionStage) callBeforeTranscriptionCallbacks(data *MediaData) {
	rts.mu.RLock()
	callbacks := make([]BeforeTranscriptionCallback, len(rts.beforeTranscriptionCallbacks))
	copy(callbacks, rts.beforeTranscriptionCallbacks)
	rts.mu.RUnlock()

	for _, callback := range callbacks {
		// Call callback synchronously since it needs to modify data before processing
		callback(data)
	}
}

// RemoveAllCallbacks removes all transcription callbacks.
func (rts *RealtimeTranscriptionStage) RemoveAllCallbacks() {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	rts.transcriptionCallbacks = make([]TranscriptionCallback, 0)
	rts.beforeTranscriptionCallbacks = make([]BeforeTranscriptionCallback, 0)
}

// IsConnected returns whether the stage is connected to the Realtime API.
func (rts *RealtimeTranscriptionStage) IsConnected() bool {
	rts.mu.RLock()
	defer rts.mu.RUnlock()
	return rts.connected
}

// Disconnect closes the connection to the Realtime API.
func (rts *RealtimeTranscriptionStage) Disconnect() {
	rts.mu.Lock()
	if !rts.connected {
		rts.mu.Unlock()
		return
	}
	rts.mu.Unlock()

	// Signal shutdown
	rts.closeOnce.Do(func() {
		close(rts.closeChan)
	})

	// Wait for goroutines
	rts.wg.Wait()

	// Clean up connections
	rts.handleDisconnection()

	getLogger := logger.GetLogger()
	getLogger.Infow("disconnected from OpenAI Realtime API")
}

// GetConfig returns the current configuration.
func (rts *RealtimeTranscriptionStage) GetConfig() *RealtimeTranscriptionConfig {
	rts.mu.RLock()
	defer rts.mu.RUnlock()
	return rts.config
}

// GetStats returns current transcription statistics.
func (rts *RealtimeTranscriptionStage) GetStats() RealtimeTranscriptionStats {
	rts.stats.mu.RLock()
	defer rts.stats.mu.RUnlock()

	return RealtimeTranscriptionStats{
		ConnectionAttempts:     rts.stats.ConnectionAttempts,
		ConnectionSuccesses:    rts.stats.ConnectionSuccesses,
		ConnectionFailures:     rts.stats.ConnectionFailures,
		CurrentlyConnected:     rts.stats.CurrentlyConnected,
		AudioPacketsSent:       rts.stats.AudioPacketsSent,
		TranscriptionsReceived: rts.stats.TranscriptionsReceived,
		PartialTranscriptions:  rts.stats.PartialTranscriptions,
		FinalTranscriptions:    rts.stats.FinalTranscriptions,
		Errors:                 rts.stats.Errors,
		AverageLatencyMs:       rts.stats.AverageLatencyMs,
		LastTranscriptionAt:    rts.stats.LastTranscriptionAt,
		BytesTranscribed:       rts.stats.BytesTranscribed,
	}
}

// SetModel sets the OpenAI model to use for transcription.
func (rts *RealtimeTranscriptionStage) SetModel(model string) {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	rts.config.Model = model
}

// SetLanguage sets the language for transcription.
func (rts *RealtimeTranscriptionStage) SetLanguage(language string) {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	if rts.audioTranscriptionConfig != nil {
		rts.audioTranscriptionConfig.Language = language
	}
}

// SetVoice sets the voice for audio generation.
// Available voices: "alloy", "echo", "fable", "onyx", "nova", "shimmer"
func (rts *RealtimeTranscriptionStage) SetVoice(voice string) {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	if voice != "" {
		rts.config.Voice = voice
	}
}

// SetPrompt sets the context prompt for better transcription accuracy.
func (rts *RealtimeTranscriptionStage) SetPrompt(prompt string) {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	if rts.audioTranscriptionConfig != nil {
		rts.audioTranscriptionConfig.Prompt = prompt
	}
}

// updateStats updates transcription statistics following the pattern of other pipeline stages.
func (rts *RealtimeTranscriptionStage) updateStats(transcriptionType string, transcriptionTime time.Time, isError bool) {
	rts.stats.mu.Lock()
	defer rts.stats.mu.Unlock()

	// Update counters based on transcription type
	if isError {
		rts.stats.Errors++
	} else {
		rts.stats.TranscriptionsReceived++
		rts.stats.LastTranscriptionAt = transcriptionTime

		switch transcriptionType {
		case "partial":
			rts.stats.PartialTranscriptions++
		case "final":
			rts.stats.FinalTranscriptions++
		}

		// Calculate and update average latency
		rts.updateAverageLatencyLocked(transcriptionTime)
	}
}

// updateAverageLatencyLocked calculates and updates the average latency based on packet send times.
// This method assumes stats.mu is already locked.
func (rts *RealtimeTranscriptionStage) updateAverageLatencyLocked(transcriptionTime time.Time) {
	// Find the most recent packet that could correspond to this transcription
	// We'll use a simple heuristic: find the packet sent closest to but before the transcription
	var latestPacketTime time.Time
	for _, sendTime := range rts.stats.packetSendTimes {
		if sendTime.Before(transcriptionTime) && sendTime.After(latestPacketTime) {
			latestPacketTime = sendTime
		}
	}

	// If we found a packet time, calculate latency
	if !latestPacketTime.IsZero() {
		latency := transcriptionTime.Sub(latestPacketTime)
		latencyMs := float64(latency.Milliseconds())

		// Update average latency using exponentially weighted moving average
		if rts.stats.AverageLatencyMs == 0 {
			rts.stats.AverageLatencyMs = latencyMs
		} else {
			rts.stats.AverageLatencyMs = (rts.stats.AverageLatencyMs * 0.9) + (latencyMs * 0.1)
		}
	}
}
