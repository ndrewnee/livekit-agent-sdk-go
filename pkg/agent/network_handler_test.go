package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

// MockWebSocketConn implements a mock WebSocket connection for testing
type MockWebSocketConn struct {
	mu               sync.Mutex
	writeErr         error
	writeDeadlineErr error
	writtenMessages  [][]byte
	writeCallCount   int
	partialWriteAt   int // Simulate partial write at this call number
}

func (m *MockWebSocketConn) WriteMessage(messageType int, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.writeCallCount++

	// Simulate partial write
	if m.partialWriteAt > 0 && m.writeCallCount == m.partialWriteAt {
		return &net.OpError{
			Op:  "write",
			Err: &tempError{message: "temporary write error"},
		}
	}

	if m.writeErr != nil {
		return m.writeErr
	}

	m.writtenMessages = append(m.writtenMessages, data)
	return nil
}

func (m *MockWebSocketConn) SetWriteDeadline(t time.Time) error {
	return m.writeDeadlineErr
}

func (m *MockWebSocketConn) Close() error {
	return nil
}

func (m *MockWebSocketConn) ReadMessage() (messageType int, p []byte, err error) {
	return 0, nil, fmt.Errorf("not implemented")
}

// tempError implements net.Error interface for temporary errors
type tempError struct {
	message string
}

func (t *tempError) Error() string   { return t.message }
func (t *tempError) Temporary() bool { return true }
func (t *tempError) Timeout() bool   { return false }

// TestNetworkHandlerPartialWrite tests partial write handling
func TestNetworkHandlerPartialWrite(t *testing.T) {
	handler := NewNetworkHandler()
	mockConn := &MockWebSocketConn{
		partialWriteAt: 1, // Fail on first write
	}

	// Create a type assertion wrapper
	conn := &wsConnWrapper{mock: mockConn}

	// First write should fail with temporary error
	data := []byte("test message")
	err := handler.WriteMessageWithRetry(conn, websocket.TextMessage, data)
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "partial write")
	}

	// Verify partial write was saved
	assert.True(t, handler.HasPartialWrite())
	msgType, savedData := handler.GetPartialWriteData()
	assert.Equal(t, websocket.TextMessage, msgType)
	assert.Equal(t, data, savedData)

	// Reset mock to succeed
	mockConn.partialWriteAt = 0

	// Retry should succeed
	err = handler.WriteMessageWithRetry(conn, websocket.TextMessage, data)
	assert.NoError(t, err)

	// Verify partial write was cleared
	assert.False(t, handler.HasPartialWrite())
}

// TestNetworkHandlerNetworkErrors tests network error detection
func TestNetworkHandlerNetworkErrors(t *testing.T) {
	handler := NewNetworkHandler()

	testCases := []struct {
		name     string
		err      error
		expected bool
	}{
		{"connection reset", errors.New("connection reset by peer"), true},
		{"broken pipe", errors.New("broken pipe"), true},
		{"connection refused", errors.New("connection refused"), true},
		{"network unreachable", errors.New("network is unreachable"), true},
		{"timeout", errors.New("connection timed out"), true},
		{"normal error", errors.New("some other error"), false},
		{"nil error", nil, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockConn := &MockWebSocketConn{writeErr: tc.err}
			conn := &wsConnWrapper{mock: mockConn}

			err := handler.WriteMessageWithRetry(conn, websocket.TextMessage, []byte("test"))

			if tc.expected {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "network error detected")
				assert.True(t, handler.networkPartitionDetected)
			} else if tc.err != nil {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestNetworkPartitionDetection tests network partition detection
func TestNetworkPartitionDetection(t *testing.T) {
	handler := NewNetworkHandler()

	// Initially no partition
	assert.False(t, handler.DetectNetworkPartition())

	// Update activity
	handler.UpdateNetworkActivity()
	assert.False(t, handler.DetectNetworkPartition())

	// Manually set partition detected
	handler.networkPartitionDetected = true
	assert.True(t, handler.DetectNetworkPartition())

	// Update activity should clear partition flag
	handler.UpdateNetworkActivity()
	assert.False(t, handler.DetectNetworkPartition())

	// Test timeout-based detection
	handler.partitionTimeout = 100 * time.Millisecond
	handler.lastNetworkActivity = time.Now().Add(-200 * time.Millisecond)
	assert.True(t, handler.DetectNetworkPartition())
}

// TestDNSResolutionWithRetry tests DNS resolution with retry
func TestDNSResolutionWithRetry(t *testing.T) {
	handler := NewNetworkHandler()
	handler.maxDNSRetries = 2

	ctx := context.Background()

	// Test successful resolution
	addrs, err := handler.ResolveDNSWithRetry(ctx, "localhost")
	assert.NoError(t, err)
	assert.NotEmpty(t, addrs)
	assert.Equal(t, int32(0), handler.GetDNSFailureCount())

	// Test non-existent host (should fail after retries)
	addrs, err = handler.ResolveDNSWithRetry(ctx, "non-existent-host-12345.invalid")
	assert.Error(t, err)
	assert.Empty(t, addrs)
	assert.Greater(t, handler.GetDNSFailureCount(), int32(0))

	// Test context cancellation
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = handler.ResolveDNSWithRetry(cancelCtx, "example.com")
	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)
}

// TestNetworkMonitor tests the network monitor
func TestNetworkMonitor(t *testing.T) {
	handler := NewNetworkHandler()
	monitor := NewNetworkMonitor(handler, 50*time.Millisecond)

	partitionDetected := false
	var mu sync.Mutex

	monitor.Start(func() {
		mu.Lock()
		partitionDetected = true
		mu.Unlock()
	})

	// Simulate network partition
	handler.SetNetworkPartition(true)

	// Wait for monitor to detect
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	detected := partitionDetected
	mu.Unlock()

	assert.True(t, detected)

	// Stop monitor
	monitor.Stop()
}

// TestRetryableWriteMessage tests retryable write message
func TestRetryableWriteMessage(t *testing.T) {
	msg := &RetryableWriteMessage{
		MessageType: websocket.TextMessage,
		Data:        []byte("test"),
		Retries:     0,
		MaxRetries:  3,
	}

	// Should be able to retry
	assert.True(t, msg.CanRetry())

	// Increment retries
	msg.IncrementRetries()
	assert.Equal(t, 1, msg.Retries)
	assert.True(t, msg.CanRetry())

	// Max out retries
	msg.Retries = 3
	assert.False(t, msg.CanRetry())
}

// TestPartialWriteBufferClear tests clearing partial write buffer
func TestPartialWriteBufferClear(t *testing.T) {
	handler := NewNetworkHandler()

	// Set partial write data
	handler.partialWriteBuffer = []byte("partial data")
	handler.partialWriteMessageType = websocket.TextMessage
	handler.partialWriteOffset = 5

	assert.True(t, handler.HasPartialWrite())

	// Clear
	handler.clearPartialWrite()

	assert.False(t, handler.HasPartialWrite())
	msgType, data := handler.GetPartialWriteData()
	assert.Equal(t, 0, msgType)
	assert.Nil(t, data)
}

// TestWriteDeadlineError tests handling of write deadline errors
func TestWriteDeadlineError(t *testing.T) {
	handler := NewNetworkHandler()
	mockConn := &MockWebSocketConn{
		writeDeadlineErr: errors.New("deadline error"),
	}
	conn := &wsConnWrapper{mock: mockConn}

	err := handler.WriteMessageWithRetry(conn, websocket.TextMessage, []byte("test"))
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "failed to set write deadline")
	}
}

// wsConnWrapper wraps mock connection to implement minimal websocket.Conn interface
type wsConnWrapper struct {
	mock *MockWebSocketConn
}

func (w *wsConnWrapper) WriteMessage(messageType int, data []byte) error {
	return w.mock.WriteMessage(messageType, data)
}

func (w *wsConnWrapper) SetWriteDeadline(t time.Time) error {
	return w.mock.SetWriteDeadline(t)
}

func (w *wsConnWrapper) Close() error {
	return w.mock.Close()
}

func (w *wsConnWrapper) ReadMessage() (messageType int, p []byte, err error) {
	return w.mock.ReadMessage()
}

func (w *wsConnWrapper) WriteControl(messageType int, data []byte, deadline time.Time) error {
	return nil
}

func (w *wsConnWrapper) SetReadDeadline(t time.Time) error {
	return nil
}

func (w *wsConnWrapper) SetReadLimit(limit int64) {}

func (w *wsConnWrapper) SetPingHandler(h func(appData string) error) {}

func (w *wsConnWrapper) SetPongHandler(h func(appData string) error) {}

func (w *wsConnWrapper) SetCloseHandler(h func(code int, text string) error) {}

func (w *wsConnWrapper) CloseHandler() func(code int, text string) error {
	return nil
}

func (w *wsConnWrapper) PingHandler() func(appData string) error {
	return nil
}

func (w *wsConnWrapper) PongHandler() func(appData string) error {
	return nil
}

func (w *wsConnWrapper) UnderlyingConn() net.Conn {
	return nil
}

func (w *wsConnWrapper) EnableWriteCompression(enable bool) {}

func (w *wsConnWrapper) SetCompressionLevel(level int) error {
	return nil
}

func (w *wsConnWrapper) Subprotocol() string {
	return ""
}

func (w *wsConnWrapper) LocalAddr() net.Addr {
	return nil
}

func (w *wsConnWrapper) RemoteAddr() net.Addr {
	return nil
}

func (w *wsConnWrapper) NextWriter(messageType int) (io.WriteCloser, error) {
	return nil, nil
}

func (w *wsConnWrapper) NextReader() (messageType int, r io.Reader, err error) {
	return 0, nil, nil
}
