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

	// OpenAI configuration
	apiKey                   string
	model                    string //
	voiceMode                string // e.g., "text" for transcription only
	inputAudioNoiseReduction *NoiseReduction
	audioTranscriptionConfig *AudioTranscriptionConfig
	instruction              string  // Custom instruction for transcription
	temperature              float64 // Custom temperature for transcription
	turnDetection            *TurnDetection
	voice                    string

	// Connection state
	mu             sync.RWMutex
	peerConnection *webrtc.PeerConnection
	dataChannel    *webrtc.DataChannel
	audioTrack     *webrtc.TrackLocalStaticRTP

	// Ephemeral key for WebRTC
	ephemeralKey string

	// Transcription handling
	transcriptionCallbacks []TranscriptionCallback
	eventChan              chan RealtimeEvent
	closeChan              chan struct{}
	wg                     sync.WaitGroup

	// Connection state
	connected     bool
	connecting    bool
	lastConnected time.Time

	// Statistics
	stats *RealtimeTranscriptionStats
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
	SilenceDuration   float64 `json:"silence_duration_ms,omitempty"`
	PreffixPadding    float64 `json:"prefix_padding_ms,omitempty"`
	InterruptResponse bool    `json:"interrupt_response,omitempty"`
	Eagerness         string  `json:"eagerness,omitempty"`
	CreateResponse    bool    `json:"create_response,omitempty"`
}

// TranscriptionCallback is called when transcription events occur.
type TranscriptionCallback func(event TranscriptionEvent)

// TranscriptionEvent represents a transcription event from the Realtime API.
type TranscriptionEvent struct {
	Type      string    // "partial", "final", "error"
	Text      string    // Transcribed text
	Timestamp time.Time // When the transcription was received
	Language  string    // Detected language (if available)
	IsFinal   bool      // Whether this is a final transcription
	Error     error     // Error if Type is "error"
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
}

// NewRealtimeTranscriptionStage creates a new OpenAI Realtime transcription stage.
//
// Parameters:
//   - name: Unique identifier for this stage
//   - priority: Execution order (lower runs first)
//   - apiKey: OpenAI API key for authentication
//   - model: Model to use
//
// The stage will establish a WebRTC connection to OpenAI's Realtime API
// and stream audio for transcription.
func NewRealtimeTranscriptionStage(name string, priority int, apiKey string, model string) *RealtimeTranscriptionStage {
	if model == "" {
		// Use the transcription model for WebRTC connection
		model = "gpt-4o-transcribe"
	}

	return &RealtimeTranscriptionStage{
		name:      name,
		priority:  priority,
		apiKey:    apiKey,
		model:     model,
		voiceMode: "alloy", // Default voice (required even for transcription)
		audioTranscriptionConfig: &AudioTranscriptionConfig{
			Model: model,
		},
		transcriptionCallbacks: make([]TranscriptionCallback, 0),
		eventChan:              make(chan RealtimeEvent, 100),
		closeChan:              make(chan struct{}),
		stats:                  &RealtimeTranscriptionStats{},
		turnDetection: &TurnDetection{
			Type:              "server_vad",
			Threshold:         0,
			SilenceDuration:   0,
			PreffixPadding:    0,
			InterruptResponse: false,
			Eagerness:         "",
			CreateResponse:    true,
		},
	}
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
// The transcribed text is delivered asynchronously via callbacks.
func (rts *RealtimeTranscriptionStage) Process(ctx context.Context, input MediaData) (MediaData, error) {
	// Ensure we're connected
	if !rts.IsConnected() {
		if err := rts.Connect(ctx); err != nil {
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

		// Create RTP packet from Opus audio
		rtpPacket := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111, // Opus payload type
				SequenceNumber: uint16(time.Now().UnixNano() & 0xFFFF),
				Timestamp:      uint32(time.Now().UnixNano() / 1000),
				SSRC:           12345,
			},
			Payload: input.Data,
		}

		// Write RTP packet to WebRTC audio track
		if err := rts.audioTrack.WriteRTP(rtpPacket); err != nil {
			getLogger := logger.GetLogger()
			getLogger.Debugw("failed to write audio to WebRTC track", "error", err)
		} else {
			rts.stats.mu.Lock()
			rts.stats.AudioPacketsSent++
			rts.stats.BytesTranscribed += uint64(len(input.Data))
			//packetsSent := rts.stats.AudioPacketsSent
			rts.stats.mu.Unlock()

			// Periodically commit audio buffer to trigger transcription
			// With whisper-1 model, we need to explicitly commit
			//if packetsSent%100 == 0 {
			//	rts.commitAudioBufferViaDataChannel()
			//}
		}
	}

	// Mark as processed by transcription
	if input.Metadata == nil {
		input.Metadata = make(map[string]interface{})
	}
	input.Metadata["realtime_transcribed"] = true
	input.Metadata["transcribed_at"] = time.Now()

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
		// Send session configuration when data channel opens
		rts.sendDataChannelConfig()
	})

	// Handle connection state changes
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		getLogger.Infow("WebRTC connection state changed", "state", state.String())

		if state == webrtc.PeerConnectionStateConnected {
			rts.mu.Lock()
			rts.connected = true
			rts.lastConnected = time.Now()
			rts.mu.Unlock()

			rts.stats.mu.Lock()
			rts.stats.ConnectionSuccesses++
			rts.stats.CurrentlyConnected = true
			rts.stats.mu.Unlock()

			fmt.Println("✅ WebRTC connected to OpenAI Realtime API")
		} else if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
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
		"model", rts.model,
		"voiceMode", rts.voiceMode)

	return nil
}

type CreateSessionRequest struct {
	Model                    string          `json:"model"`
	InputAudioNoiseReduction *NoiseReduction `json:"input_audio_noise_reduction,omitempty"`
	Instructions             string          `json:"instructions,omitempty"`
	Modalities               []string        `json:"modalities,omitempty"`
	Temperature              float64         `json:"temperature,omitempty"`
	TurnDetection            *TurnDetection  `json:"turn_detection,omitempty"`
	Voice                    string          `json:"voice,omitempty"`
}

// getEphemeralKey gets an ephemeral key for WebRTC connection from OpenAI API.
func (rts *RealtimeTranscriptionStage) getEphemeralKey(ctx context.Context) (string, error) {
	// The ephemeral key endpoint for WebRTC is different
	url := "https://api.openai.com/v1/realtime/sessions"

	// Only send the model for session creation
	// For ephemeral key, we use gpt-4o-transcribe
	reqBody := CreateSessionRequest{
		Model:         "gpt-4o-transcribe",
		Modalities:    []string{"text"},
		TurnDetection: rts.turnDetection,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+rts.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "realtime=v1")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

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
	url := fmt.Sprintf("https://api.openai.com/v1/realtime?model=%s", "gpt-4o-transcribe")

	// The SDP should be sent as plain text with proper headers
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer([]byte(offer.SDP)))
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
	defer resp.Body.Close()

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

// Removed connectWebSocketWithKey - using WebRTC only

// sendDataChannelConfig sends session configuration via data channel.
func (rts *RealtimeTranscriptionStage) sendDataChannelConfig() error {
	if rts.dataChannel == nil || rts.dataChannel.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("data channel not open")
	}

	// Send session configuration for transcription as per OpenAI guide
	// https://platform.openai.com/docs/guides/realtime-transcription
	sessionMsg := map[string]interface{}{
		"type": "session.update",
		"session": map[string]interface{}{
			// Set modalities to text only to prevent audio generation
			"modalities": []string{"text"},

			// Configure transcription with whisper-1 model (currently supported)
			"input_audio_transcription": rts.audioTranscriptionConfig,

			// Disable turn detection for pure transcription
			"turn_detection": rts.turnDetection,

			// Set temperature for transcription (0.8 recommended)
			"temperature": 0.8,
		},
	}

	data, err := json.Marshal(sessionMsg)
	if err != nil {
		return err
	}

	getLogger := logger.GetLogger()
	getLogger.Infow("Sending session configuration via data channel", "config", sessionMsg)
	fmt.Printf("📤 Sending session config: %v\n", sessionMsg)

	return rts.dataChannel.SendText(string(data))
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
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "partial",
				Text:      delta,
				Timestamp: time.Now(),
				IsFinal:   false,
			})
			rts.stats.mu.Lock()
			rts.stats.PartialTranscriptions++
			rts.stats.mu.Unlock()
		}

	case "conversation.item.input_audio_transcription.completed":
		// Handle final transcriptions from input audio
		if transcript, ok := msg["transcript"].(string); ok && transcript != "" {
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "final",
				Text:      transcript,
				Timestamp: time.Now(),
				IsFinal:   true,
			})
			rts.stats.mu.Lock()
			rts.stats.FinalTranscriptions++
			rts.stats.mu.Unlock()
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
			rts.notifyTranscription(TranscriptionEvent{
				Type:      "partial",
				Text:      transcript,
				Timestamp: time.Now(),
				IsFinal:   false,
			})

			rts.stats.mu.Lock()
			rts.stats.PartialTranscriptions++
			rts.stats.TranscriptionsReceived++
			rts.stats.LastTranscriptionAt = time.Now()
			rts.stats.mu.Unlock()
		}
	}
}

// handleTranscriptionComplete handles final transcription.
func (rts *RealtimeTranscriptionStage) handleTranscriptionComplete(msg map[string]interface{}) {
	if transcript, ok := msg["transcript"].(string); ok && transcript != "" {
		rts.notifyTranscription(TranscriptionEvent{
			Type:      "final",
			Text:      transcript,
			Timestamp: time.Now(),
			IsFinal:   true,
		})

		rts.stats.mu.Lock()
		rts.stats.FinalTranscriptions++
		rts.stats.TranscriptionsReceived++
		rts.stats.LastTranscriptionAt = time.Now()
		rts.stats.mu.Unlock()
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
						rts.notifyTranscription(TranscriptionEvent{
							Type:      "final",
							Text:      transcript,
							Timestamp: time.Now(),
							IsFinal:   true,
						})

						rts.stats.mu.Lock()
						rts.stats.FinalTranscriptions++
						rts.stats.TranscriptionsReceived++
						rts.stats.LastTranscriptionAt = time.Now()
						rts.stats.mu.Unlock()
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

	rts.notifyTranscription(TranscriptionEvent{
		Type:      "error",
		Timestamp: time.Now(),
		Error:     fmt.Errorf("Realtime API error: %s", errorMsg),
	})

	rts.stats.mu.Lock()
	rts.stats.Errors++
	rts.stats.mu.Unlock()

	getLogger := logger.GetLogger()
	getLogger.Errorw("Realtime API error", fmt.Errorf("%s", errorMsg))
}

// Removed trackConnectionState - not needed without WebRTC

// handleDisconnection handles cleanup when disconnected.
func (rts *RealtimeTranscriptionStage) handleDisconnection() {
	rts.mu.Lock()
	rts.connected = false
	rts.mu.Unlock()

	rts.stats.mu.Lock()
	rts.stats.CurrentlyConnected = false
	rts.stats.mu.Unlock()

	// Clean up WebSocket
	// Clean up WebRTC connection
	if rts.peerConnection != nil {
		rts.peerConnection.Close()
		rts.peerConnection = nil
	}

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
	rts.mu.RLock()
	callbacks := make([]TranscriptionCallback, len(rts.transcriptionCallbacks))
	copy(callbacks, rts.transcriptionCallbacks)
	rts.mu.RUnlock()

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

// RemoveAllCallbacks removes all transcription callbacks.
func (rts *RealtimeTranscriptionStage) RemoveAllCallbacks() {
	rts.mu.Lock()
	defer rts.mu.Unlock()
	rts.transcriptionCallbacks = make([]TranscriptionCallback, 0)
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
	close(rts.closeChan)

	// Wait for goroutines
	rts.wg.Wait()

	// Clean up connections
	rts.handleDisconnection()

	getLogger := logger.GetLogger()
	getLogger.Infow("disconnected from OpenAI Realtime API")
}

// commitAudioBufferViaDataChannel sends commit message via data channel
func (rts *RealtimeTranscriptionStage) commitAudioBufferViaDataChannel() {
	if rts.dataChannel == nil || rts.dataChannel.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}

	// Commit audio buffer to trigger transcription
	commitMsg := map[string]interface{}{
		"type": "input_audio_buffer.commit",
	}

	data, err := json.Marshal(commitMsg)
	if err != nil {
		return
	}

	if err := rts.dataChannel.SendText(string(data)); err != nil {
		getLogger := logger.GetLogger()
		getLogger.Debugw("failed to commit audio buffer", "error", err)
	} else {
		fmt.Println("📤 Committing audio buffer for transcription")
	}
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
	rts.model = model
}
