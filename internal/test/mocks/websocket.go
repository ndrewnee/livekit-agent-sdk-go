package mocks

import (
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// MockWebSocketConn implements a mock WebSocket connection for testing
type MockWebSocketConn struct {
	mu            sync.Mutex
	ReadMessages  []MockMessage
	WriteMessages []MockMessage
	readIndex     int
	closed        bool
	closeErr      error
	readErr       error
	writeErr      error
}

type MockMessage struct {
	MessageType int
	Data        []byte
	Error       error
}

func NewMockWebSocketConn() *MockWebSocketConn {
	return &MockWebSocketConn{
		ReadMessages:  make([]MockMessage, 0),
		WriteMessages: make([]MockMessage, 0),
	}
}

func (m *MockWebSocketConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.closeErr
}

func (m *MockWebSocketConn) LocalAddr() net.Addr {
	return nil
}

func (m *MockWebSocketConn) RemoteAddr() net.Addr {
	return nil
}

func (m *MockWebSocketConn) WriteMessage(messageType int, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.closed {
		return websocket.ErrCloseSent
	}
	
	if m.writeErr != nil {
		return m.writeErr
	}
	
	m.WriteMessages = append(m.WriteMessages, MockMessage{
		MessageType: messageType,
		Data:        data,
	})
	return nil
}

func (m *MockWebSocketConn) ReadMessage() (messageType int, p []byte, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.closed {
		return 0, nil, websocket.ErrCloseSent
	}
	
	if m.readErr != nil {
		return 0, nil, m.readErr
	}
	
	if m.readIndex >= len(m.ReadMessages) {
		// Block until new message or timeout
		m.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		m.mu.Lock()
		
		if m.readIndex >= len(m.ReadMessages) {
			return 0, nil, errors.New("read timeout")
		}
	}
	
	msg := m.ReadMessages[m.readIndex]
	m.readIndex++
	
	if msg.Error != nil {
		return 0, nil, msg.Error
	}
	
	return msg.MessageType, msg.Data, nil
}

func (m *MockWebSocketConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	return m.WriteMessage(messageType, data)
}

func (m *MockWebSocketConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *MockWebSocketConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (m *MockWebSocketConn) SetCloseHandler(h func(code int, text string) error) {}

func (m *MockWebSocketConn) SetPongHandler(h func(appData string) error) {}

func (m *MockWebSocketConn) SetPingHandler(h func(appData string) error) {}

func (m *MockWebSocketConn) UnderlyingConn() net.Conn {
	return nil
}

func (m *MockWebSocketConn) SetCompressionLevel(level int) error {
	return nil
}

func (m *MockWebSocketConn) EnableWriteCompression(enable bool) {}

// AddReadMessage adds a message to be read
func (m *MockWebSocketConn) AddReadMessage(messageType int, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ReadMessages = append(m.ReadMessages, MockMessage{
		MessageType: messageType,
		Data:        data,
	})
}

// GetWrittenMessages returns all messages written to the connection
func (m *MockWebSocketConn) GetWrittenMessages() []MockMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MockMessage{}, m.WriteMessages...)
}

// SetReadError sets an error to be returned on next read
func (m *MockWebSocketConn) SetReadError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readErr = err
}

// SetWriteError sets an error to be returned on next write
func (m *MockWebSocketConn) SetWriteError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeErr = err
}

// MockDialer implements a mock WebSocket dialer
type MockDialer struct {
	Conn      *MockWebSocketConn
	DialError error
	URL       string
	Headers   http.Header
}

func (d *MockDialer) Dial(urlStr string, requestHeader http.Header) (*websocket.Conn, *http.Response, error) {
	d.URL = urlStr
	d.Headers = requestHeader
	
	if d.DialError != nil {
		return nil, nil, d.DialError
	}
	
	// We can't return our mock directly, so we return nil for testing purposes
	// In real tests, we'll need to inject the mock connection differently
	return nil, &http.Response{StatusCode: 101}, nil
}