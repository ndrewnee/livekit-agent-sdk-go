package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockRealtimeServer simulates OpenAI Realtime API WebSocket server
type MockRealtimeServer struct {
	server           *httptest.Server
	upgrader         websocket.Upgrader
	connectedClients []*websocket.Conn
	mu               sync.Mutex
	receivedMessages []interface{}
}

// NewMockRealtimeServer creates a mock Realtime API server
func NewMockRealtimeServer() *MockRealtimeServer {
	mock := &MockRealtimeServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for testing
			},
		},
		connectedClients: make([]*websocket.Conn, 0),
		receivedMessages: make([]interface{}, 0),
	}

	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check authorization header
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check OpenAI-Beta header
		beta := r.Header.Get("OpenAI-Beta")
		if beta != "realtime=v1" {
			http.Error(w, "Missing beta header", http.StatusBadRequest)
			return
		}

		// Upgrade to WebSocket
		conn, err := mock.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		mock.mu.Lock()
		mock.connectedClients = append(mock.connectedClients, conn)
		mock.mu.Unlock()

		// Handle WebSocket messages
		go mock.handleClient(conn)
	}))

	return mock
}

// handleClient handles messages from a connected client
func (m *MockRealtimeServer) handleClient(conn *websocket.Conn) {
	defer conn.Close()

	for {
		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)
		if err != nil {
			break
		}

		m.mu.Lock()
		m.receivedMessages = append(m.receivedMessages, msg)
		m.mu.Unlock()

		// Handle different message types
		msgType, _ := msg["type"].(string)
		switch msgType {
		case "session.update":
			// Respond with session.created
			response := map[string]interface{}{
				"type": "session.created",
				"session": map[string]interface{}{
					"id":         "test-session-id",
					"object":     "realtime.session",
					"model":      "gpt-4o-realtime-preview-2024-12-17",
					"modalities": []string{"text", "audio"},
				},
			}
			conn.WriteJSON(response)

		case "webrtc.offer":
			// Respond with WebRTC answer
			response := map[string]interface{}{
				"type": "webrtc.answer",
				"sdp":  "mock-sdp-answer",
			}
			conn.WriteJSON(response)

		case "conversation.item.create":
			// Simulate transcription response
			go func() {
				time.Sleep(100 * time.Millisecond)

				// Send partial transcription
				partialResponse := map[string]interface{}{
					"type": "response.audio_transcript.delta",
					"delta": map[string]interface{}{
						"transcript": "Hello, this is a ",
					},
				}
				conn.WriteJSON(partialResponse)

				time.Sleep(100 * time.Millisecond)

				// Send final transcription
				finalResponse := map[string]interface{}{
					"type":       "response.audio_transcript.done",
					"transcript": "Hello, this is a test transcription.",
				}
				conn.WriteJSON(finalResponse)
			}()
		}
	}
}

// SendTranscription sends a transcription event to all connected clients
func (m *MockRealtimeServer) SendTranscription(text string, isFinal bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.connectedClients {
		var msg map[string]interface{}
		if isFinal {
			msg = map[string]interface{}{
				"type":       "response.audio_transcript.done",
				"transcript": text,
			}
		} else {
			msg = map[string]interface{}{
				"type": "response.audio_transcript.delta",
				"delta": map[string]interface{}{
					"transcript": text,
				},
			}
		}
		conn.WriteJSON(msg)
	}
}

// Close shuts down the mock server
func (m *MockRealtimeServer) Close() {
	m.mu.Lock()
	for _, conn := range m.connectedClients {
		conn.Close()
	}
	m.mu.Unlock()
	m.server.Close()
}

// TestRealtimeTranscriptionStageCreation tests stage creation
func TestRealtimeTranscriptionStageCreation(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	assert.NotNil(t, stage)
	assert.Equal(t, "transcription", stage.GetName())
	assert.Equal(t, 20, stage.GetPriority())
	assert.True(t, stage.CanProcess(MediaTypeAudio))
	assert.False(t, stage.CanProcess(MediaTypeVideo))
	assert.Equal(t, "gpt-4o-transcribe", stage.model)
	assert.Equal(t, "alloy", stage.voiceMode)
	assert.NotNil(t, stage.stats)
	assert.False(t, stage.IsConnected())
}

// TestRealtimeTranscriptionProcess tests the Process method
func TestRealtimeTranscriptionProcess(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping RealtimeTranscriptionProcess test: OPENAI_API_KEY not set")
	}

	stage := NewRealtimeTranscriptionStage("transcription", 20, apiKey, "", "en")
	ctx := context.Background()

	// Process audio data
	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "audio-track-1",
		Timestamp: time.Now(),
		Data:      []byte("audio-data"),
		Metadata:  make(map[string]interface{}),
	}

	// Add RTP header
	rtpHeader := &rtp.Header{
		Version:        2,
		PayloadType:    111,
		SequenceNumber: 1000,
		Timestamp:      48000,
		SSRC:           99999999,
	}
	input.Metadata["rtp_header"] = rtpHeader

	output, err := stage.Process(ctx, input)

	assert.NoError(t, err)
	assert.Equal(t, input.TrackID, output.TrackID)
	assert.NotNil(t, output.Metadata["transcribed_at"])
	if assert.NotNil(t, output.Metadata["realtime_transcribed"]) {
		assert.True(t, output.Metadata["realtime_transcribed"].(bool))
	}
}

// TestRealtimeTranscriptionCallbacks tests transcription callbacks
func TestRealtimeTranscriptionCallbacks(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	receivedEvents := make([]TranscriptionEvent, 0)
	var mu sync.Mutex

	// Add callback
	stage.AddTranscriptionCallback(func(event TranscriptionEvent) {
		mu.Lock()
		receivedEvents = append(receivedEvents, event)
		mu.Unlock()
	})

	// Notify transcription events
	stage.notifyTranscription(TranscriptionEvent{
		Type:      "partial",
		Text:      "Hello",
		Timestamp: time.Now(),
		IsFinal:   false,
	})

	stage.notifyTranscription(TranscriptionEvent{
		Type:      "final",
		Text:      "Hello, world!",
		Timestamp: time.Now(),
		IsFinal:   true,
	})

	// Wait for callbacks to be processed
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	assert.Len(t, receivedEvents, 2)

	// Sort events since goroutines can deliver them out of order
	var partialEvent, finalEvent *TranscriptionEvent
	for i := range receivedEvents {
		if receivedEvents[i].Type == "partial" {
			partialEvent = &receivedEvents[i]
		} else if receivedEvents[i].Type == "final" {
			finalEvent = &receivedEvents[i]
		}
	}

	assert.NotNil(t, partialEvent)
	assert.NotNil(t, finalEvent)
	assert.Equal(t, "Hello", partialEvent.Text)
	assert.False(t, partialEvent.IsFinal)
	assert.Equal(t, "Hello, world!", finalEvent.Text)
	assert.True(t, finalEvent.IsFinal)

	// Test removing callbacks
	stage.RemoveAllCallbacks()
	stage.notifyTranscription(TranscriptionEvent{
		Type: "partial",
		Text: "Should not be received",
	})

	time.Sleep(50 * time.Millisecond)
	assert.Len(t, receivedEvents, 2) // Should still be 2
}

// TestRealtimeTranscriptionHandlers tests message handlers
func TestRealtimeTranscriptionHandlers(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	receivedTranscriptions := make([]string, 0)
	var mu sync.Mutex

	stage.AddTranscriptionCallback(func(event TranscriptionEvent) {
		if event.Type != "error" {
			mu.Lock()
			receivedTranscriptions = append(receivedTranscriptions, event.Text)
			mu.Unlock()
		}
	})

	// Test partial transcription handler
	stage.handleTranscriptionDelta(map[string]interface{}{
		"delta": map[string]interface{}{
			"transcript": "Testing partial",
		},
	})

	// Test complete transcription handler
	stage.handleTranscriptionComplete(map[string]interface{}{
		"transcript": "Testing complete",
	})

	// Test conversation item handler
	stage.handleConversationItem(map[string]interface{}{
		"item": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"transcript": "Testing conversation",
				},
			},
		},
	})

	// Wait for callbacks
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	assert.Len(t, receivedTranscriptions, 3)
	assert.Contains(t, receivedTranscriptions, "Testing partial")
	assert.Contains(t, receivedTranscriptions, "Testing complete")
	assert.Contains(t, receivedTranscriptions, "Testing conversation")

	// Check stats
	stats := stage.GetStats()
	assert.Equal(t, uint64(1), stats.PartialTranscriptions)
	assert.Equal(t, uint64(2), stats.FinalTranscriptions)
	assert.Equal(t, uint64(3), stats.TranscriptionsReceived)
}

// TestRealtimeErrorHandling tests error handling
func TestRealtimeErrorHandling(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	var receivedError error
	var mu sync.Mutex
	stage.AddTranscriptionCallback(func(event TranscriptionEvent) {
		if event.Type == "error" {
			mu.Lock()
			receivedError = event.Error
			mu.Unlock()
		}
	})

	// Test error handler
	stage.handleError(map[string]interface{}{
		"error": map[string]interface{}{
			"message": "Test error message",
			"code":    "test_error",
		},
	})

	// Wait for callback
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	assert.NotNil(t, receivedError)
	assert.Contains(t, receivedError.Error(), "Test error message")
	mu.Unlock()

	// Check stats
	stats := stage.GetStats()
	assert.Equal(t, uint64(1), stats.Errors)
}

// TestRealtimeDataChannelMessage tests data channel message handling
func TestRealtimeDataChannelMessage(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	var receivedText string
	var mu sync.Mutex
	stage.AddTranscriptionCallback(func(event TranscriptionEvent) {
		mu.Lock()
		receivedText = event.Text
		mu.Unlock()
	})

	// Create a data channel message
	event := RealtimeEvent{
		Type: "response.audio_transcript.delta",
		Delta: &RealtimeDelta{
			Transcript: "Data channel transcription",
		},
	}

	data, err := json.Marshal(event)
	require.NoError(t, err)

	// Handle data channel message
	stage.handleDataChannelMessage(data)

	// Wait for callback
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, "Data channel transcription", receivedText)
	mu.Unlock()

	// Check stats
	stats := stage.GetStats()
	assert.Equal(t, uint64(1), stats.PartialTranscriptions)
	assert.Equal(t, uint64(1), stats.TranscriptionsReceived)
}

// TestRealtimeEventProcessing tests event processing
func TestRealtimeEventProcessing(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	transcriptions := make([]TranscriptionEvent, 0)
	var mu sync.Mutex

	stage.AddTranscriptionCallback(func(event TranscriptionEvent) {
		mu.Lock()
		transcriptions = append(transcriptions, event)
		mu.Unlock()
	})

	// Process partial transcription event
	stage.processRealtimeEvent(RealtimeEvent{
		Type: "response.audio_transcript.delta",
		Delta: &RealtimeDelta{
			Transcript: "Partial text",
		},
	})

	// Process final transcription event
	stage.processRealtimeEvent(RealtimeEvent{
		Type: "response.audio_transcript.done",
		Item: &RealtimeItem{
			Content: []ContentItem{
				{Transcript: "Final text"},
			},
		},
	})

	// Process error event
	stage.processRealtimeEvent(RealtimeEvent{
		Type: "error",
		Error: &RealtimeError{
			Code:    "test_error",
			Message: "Test error",
		},
	})

	// Wait for callbacks
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	assert.Len(t, transcriptions, 3)

	// Sort events by type since goroutines can deliver them out of order
	typeMap := make(map[string]TranscriptionEvent)
	for _, t := range transcriptions {
		typeMap[t.Type] = t
	}

	assert.Contains(t, typeMap, "partial")
	assert.Contains(t, typeMap, "final")
	assert.Contains(t, typeMap, "error")

	assert.Equal(t, "Partial text", typeMap["partial"].Text)
	assert.Equal(t, "Final text", typeMap["final"].Text)
	assert.NotNil(t, typeMap["error"].Error)
}

// TestRealtimeStatistics tests statistics tracking
func TestRealtimeStatistics(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	// Don't try to connect in Process - the test will only test the manual stats update
	// Process some audio packets without connection attempts would just skip them

	// Simulate transcription events
	stage.handleTranscriptionDelta(map[string]interface{}{
		"delta": map[string]interface{}{
			"transcript": "partial",
		},
	})

	stage.handleTranscriptionComplete(map[string]interface{}{
		"transcript": "complete",
	})

	stage.handleError(map[string]interface{}{
		"error": map[string]interface{}{
			"message": "error",
		},
	})

	// Get stats
	stats := stage.GetStats()

	assert.Equal(t, uint64(1), stats.PartialTranscriptions)
	assert.Equal(t, uint64(1), stats.FinalTranscriptions)
	assert.Equal(t, uint64(2), stats.TranscriptionsReceived)
	assert.Equal(t, uint64(1), stats.Errors)
	assert.False(t, stats.CurrentlyConnected)
}

// TestRealtimeDisconnection tests disconnection handling
func TestRealtimeDisconnection(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	// Simulate connection with a mock peer connection
	stage.connected = true
	stage.connecting = false
	stage.stats.CurrentlyConnected = true

	// Create a mock peer connection to simulate an active connection
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	stage.peerConnection = pc

	// Handle disconnection
	stage.handleDisconnection()

	assert.False(t, stage.IsConnected())
	assert.False(t, stage.GetStats().CurrentlyConnected)
	assert.Nil(t, stage.peerConnection)
	assert.Nil(t, stage.audioTrack)
	assert.Nil(t, stage.dataChannel)
}

// TestRealtimeIntegrationWithPipeline tests integration with MediaPipeline
func TestRealtimeIntegrationWithPipeline(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Add Realtime transcription stage
	transcriptionStage := NewRealtimeTranscriptionStage(
		"realtime-transcription",
		15,
		"test-api-key",
		"gpt-4o-realtime-preview-2024-12-17",
		"en",
	)
	pipeline.AddStage(transcriptionStage)

	// Add callback to capture transcriptions
	transcriptions := make([]string, 0)
	var mu sync.Mutex

	transcriptionStage.AddTranscriptionCallback(func(event TranscriptionEvent) {
		if event.Type != "error" {
			mu.Lock()
			transcriptions = append(transcriptions, event.Text)
			mu.Unlock()
		}
	})

	// Test that only audio is processed
	assert.True(t, transcriptionStage.CanProcess(MediaTypeAudio))
	assert.False(t, transcriptionStage.CanProcess(MediaTypeVideo))
}

// TestRealtimeConcurrentOperations tests thread safety
func TestRealtimeConcurrentOperations(t *testing.T) {
	stage := NewRealtimeTranscriptionStage("transcription", 20, "test-api-key", "", "en")

	// Add multiple callbacks concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stage.AddTranscriptionCallback(func(event TranscriptionEvent) {
				// Callback logic
			})
		}(i)
	}
	wg.Wait()

	// Send notifications concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stage.notifyTranscription(TranscriptionEvent{
				Type: "partial",
				Text: "Concurrent transcription",
			})
		}(i)
	}
	wg.Wait()

	// Get stats concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = stage.GetStats()
		}()
	}
	wg.Wait()

	// Test passed if no panics or deadlocks
	assert.True(t, true)
}
