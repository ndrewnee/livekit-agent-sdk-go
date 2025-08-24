package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

// mockWebSocketServer simulates a LiveKit server for testing
type mockWebSocketServer struct {
	*httptest.Server
	upgrader             websocket.Upgrader
	mu                   sync.Mutex
	connections          []*websocket.Conn
	receivedMsgs         [][]byte
	responses            chan *livekit.ServerMessage
	workerID             string
	simulateErrors       bool
	closeOnConnect       bool
	delayResponse        time.Duration
	suppressRegistration bool
}

func newMockWebSocketServer() *mockWebSocketServer {
	m := &mockWebSocketServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		connections:  make([]*websocket.Conn, 0),
		receivedMsgs: make([][]byte, 0),
		responses:    make(chan *livekit.ServerMessage, 10),
		workerID:     "mock-worker-123",
	}

	m.Server = httptest.NewServer(http.HandlerFunc(m.handleWebSocket))
	return m
}

func (m *mockWebSocketServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Validate auth token
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		http.Error(w, "Missing auth token", http.StatusUnauthorized)
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	_, err := auth.ParseAPIToken(token)
	if err != nil {
		http.Error(w, "Invalid auth token", http.StatusUnauthorized)
		return
	}

	// Check protocol version
	protocol := r.URL.Query().Get("protocol")
	if protocol != "1" {
		http.Error(w, "Unsupported protocol version", http.StatusBadRequest)
		return
	}

	if m.closeOnConnect {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	conn, err := m.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	m.mu.Lock()
	m.connections = append(m.connections, conn)
	m.mu.Unlock()

	// Handle messages
	go m.handleConnection(conn)
}

func (m *mockWebSocketServer) handleConnection(conn *websocket.Conn) {
	defer func() {
		m.mu.Lock()
		for i, c := range m.connections {
			if c == conn {
				m.connections = append(m.connections[:i], m.connections[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		m.mu.Lock()
		m.receivedMsgs = append(m.receivedMsgs, data)
		m.mu.Unlock()

		// Parse worker message
		var msg livekit.WorkerMessage
		if err := proto.Unmarshal(data, &msg); err != nil {
			continue
		}

		// Handle different message types
		switch msg.Message.(type) {
		case *livekit.WorkerMessage_Register:
			if m.delayResponse > 0 {
				time.Sleep(m.delayResponse)
			}
			// Only send registration response if not suppressed
			if !m.suppressRegistration {
				resp := &livekit.ServerMessage{
					Message: &livekit.ServerMessage_Register{
						Register: &livekit.RegisterWorkerResponse{
							WorkerId: m.workerID,
							ServerInfo: &livekit.ServerInfo{
								Version: "1.0.0",
							},
						},
					},
				}
				_ = m.sendMessage(conn, resp)
			} else {
				// When registration is suppressed, immediately send any queued responses
				select {
				case resp := <-m.responses:
					_ = m.sendMessage(conn, resp)
				default:
				}
			}

		case *livekit.WorkerMessage_UpdateWorker:
			// Status update received

		case *livekit.WorkerMessage_Ping:
			// Send pong
			ping := msg.GetPing()
			resp := &livekit.ServerMessage{
				Message: &livekit.ServerMessage_Pong{
					Pong: &livekit.WorkerPong{
						LastTimestamp: ping.Timestamp,
					},
				},
			}
			if !m.simulateErrors {
				_ = m.sendMessage(conn, resp)
			}

		case *livekit.WorkerMessage_Availability:
			// Job availability response

		case *livekit.WorkerMessage_UpdateJob:
			// Job status update
		}

		// Send any queued responses
		select {
		case resp := <-m.responses:
			_ = m.sendMessage(conn, resp)
		default:
		}
	}
}

func (m *mockWebSocketServer) sendMessage(conn *websocket.Conn, msg *livekit.ServerMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.BinaryMessage, data)
}

func (m *mockWebSocketServer) SendAvailabilityRequest(job *livekit.Job) {
	msg := &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Availability{
			Availability: &livekit.AvailabilityRequest{
				Job: job,
			},
		},
	}
	m.responses <- msg
}

func (m *mockWebSocketServer) SendJobAssignment(job *livekit.Job, token string) {
	msg := &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Assignment{
			Assignment: &livekit.JobAssignment{
				Job:   job,
				Token: token,
			},
		},
	}
	m.responses <- msg
}

func (m *mockWebSocketServer) SendJobTermination(jobID string) {
	msg := &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Termination{
			Termination: &livekit.JobTermination{
				JobId: jobID,
			},
		},
	}
	m.responses <- msg
}

func (m *mockWebSocketServer) GetReceivedMessages() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte{}, m.receivedMsgs...)
}

func (m *mockWebSocketServer) GetLastWorkerMessage() (*livekit.WorkerMessage, error) {
	msgs := m.GetReceivedMessages()
	if len(msgs) == 0 {
		return nil, fmt.Errorf("no messages received")
	}

	var msg livekit.WorkerMessage
	err := proto.Unmarshal(msgs[len(msgs)-1], &msg)
	return &msg, err
}

func (m *mockWebSocketServer) WaitForMessage(msgType string, timeout time.Duration) (*livekit.WorkerMessage, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		msgs := m.GetReceivedMessages()
		for i := len(msgs) - 1; i >= 0; i-- {
			var msg livekit.WorkerMessage
			if err := proto.Unmarshal(msgs[i], &msg); err != nil {
				continue
			}

			switch msgType {
			case "register":
				if msg.GetRegister() != nil {
					return &msg, nil
				}
			case "update":
				if msg.GetUpdateWorker() != nil {
					return &msg, nil
				}
			case "ping":
				if msg.GetPing() != nil {
					return &msg, nil
				}
			case "availability":
				if msg.GetAvailability() != nil {
					return &msg, nil
				}
			case "updateJob":
				if msg.GetUpdateJob() != nil {
					return &msg, nil
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	return nil, fmt.Errorf("timeout waiting for %s message", msgType)
}

func (m *mockWebSocketServer) Close() {
	m.mu.Lock()
	for _, conn := range m.connections {
		_ = conn.Close()
	}
	m.mu.Unlock()
	m.Server.Close()
}

func (m *mockWebSocketServer) URL() string {
	return strings.Replace(m.Server.URL, "http://", "ws://", 1)
}

// mockRoomServer simulates a LiveKit room connection
type mockRoomServer struct {
	*httptest.Server
}

func newMockRoomServer() *mockRoomServer {
	m := &mockRoomServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate room API responses
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
		})
	}))
	return m
}
