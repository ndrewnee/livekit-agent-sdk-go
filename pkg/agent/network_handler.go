package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

)

// WebSocketConn interface for WebSocket operations
type WebSocketConn interface {
	WriteMessage(messageType int, data []byte) error
	SetWriteDeadline(t time.Time) error
}

// NetworkHandler provides enhanced network error handling
type NetworkHandler struct {
	mu                    sync.RWMutex
	partialWriteBuffer    []byte
	partialWriteOffset    int
	partialWriteMessageType int
	lastNetworkActivity   time.Time
	networkPartitionDetected bool
	dnsFailureCount       int32
	maxDNSRetries         int
	partitionTimeout      time.Duration
}

// NewNetworkHandler creates a new network handler
func NewNetworkHandler() *NetworkHandler {
	return &NetworkHandler{
		maxDNSRetries:       3,
		partitionTimeout:    30 * time.Second,
		lastNetworkActivity: time.Now(),
	}
}

// WriteMessageWithRetry handles partial writes and retries
func (n *NetworkHandler) WriteMessageWithRetry(conn WebSocketConn, messageType int, data []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Check if we have a partial write in progress
	if n.partialWriteBuffer != nil && len(n.partialWriteBuffer) > 0 {
		// Resume from partial write
		if n.partialWriteMessageType != messageType {
			// Different message type, clear buffer
			n.clearPartialWrite()
		} else {
			// Same message type, try to continue
			data = n.partialWriteBuffer
			messageType = n.partialWriteMessageType
		}
	}

	// Set write deadline to detect network issues
	deadline := time.Now().Add(10 * time.Second)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	// Attempt to write
	err := conn.WriteMessage(messageType, data)
	if err != nil {
		// Check if it's a temporary error
		if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
			// Save for retry
			n.partialWriteBuffer = data
			n.partialWriteMessageType = messageType
			n.partialWriteOffset = 0 // WebSocket doesn't expose partial write offset
			return fmt.Errorf("partial write, will retry: %w", err)
		}

		// Check for specific network errors
		if isNetworkError(err) {
			n.networkPartitionDetected = true
			return fmt.Errorf("network error detected: %w", err)
		}

		return err
	}

	// Success, clear any partial write state
	n.clearPartialWrite()
	n.lastNetworkActivity = time.Now()
	n.networkPartitionDetected = false
	
	return nil
}

// clearPartialWrite clears the partial write buffer
func (n *NetworkHandler) clearPartialWrite() {
	n.partialWriteBuffer = nil
	n.partialWriteOffset = 0
	n.partialWriteMessageType = 0
}

// HasPartialWrite returns true if there's a partial write pending
func (n *NetworkHandler) HasPartialWrite() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.partialWriteBuffer != nil && len(n.partialWriteBuffer) > 0
}

// GetPartialWriteData returns the partial write data if any
func (n *NetworkHandler) GetPartialWriteData() (int, []byte) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.partialWriteBuffer == nil {
		return 0, nil
	}
	return n.partialWriteMessageType, n.partialWriteBuffer
}

// DetectNetworkPartition checks for network partition based on activity
func (n *NetworkHandler) DetectNetworkPartition() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.networkPartitionDetected {
		return true
	}

	// Check if we haven't had network activity for too long
	if time.Since(n.lastNetworkActivity) > n.partitionTimeout {
		return true
	}

	return false
}

// UpdateNetworkActivity updates the last network activity time
func (n *NetworkHandler) UpdateNetworkActivity() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastNetworkActivity = time.Now()
	n.networkPartitionDetected = false
}

// ResolveDNSWithRetry performs DNS resolution with retry logic
func (n *NetworkHandler) ResolveDNSWithRetry(ctx context.Context, host string) ([]string, error) {
	var lastErr error
	retries := 0

	for retries < n.maxDNSRetries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Use custom resolver with timeout
		resolver := &net.Resolver{
			PreferGo: true, // Use Go's DNS resolver
		}

		// Create a context with timeout for DNS resolution
		dnsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := resolver.LookupHost(dnsCtx, host)
		cancel()

		if err == nil {
			// Success
			atomic.StoreInt32(&n.dnsFailureCount, 0)
			return addrs, nil
		}

		lastErr = err
		retries++
		atomic.AddInt32(&n.dnsFailureCount, 1)

		// Check error type
		if dnsErr, ok := err.(*net.DNSError); ok {
			if dnsErr.IsTemporary {
				// Temporary failure, wait before retry
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(retries) * time.Second):
					continue
				}
			} else if dnsErr.IsNotFound {
				// Host not found, no point retrying
				return nil, fmt.Errorf("DNS: host not found: %s", host)
			}
		}

		// For other errors, wait before retry
		if retries < n.maxDNSRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(retries) * time.Second):
			}
		}
	}

	return nil, fmt.Errorf("DNS resolution failed after %d attempts: %w", retries, lastErr)
}

// GetDNSFailureCount returns the current DNS failure count
func (n *NetworkHandler) GetDNSFailureCount() int32 {
	return atomic.LoadInt32(&n.dnsFailureCount)
}

// isNetworkError checks if an error is a network-related error
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Check for common network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Check for specific error messages
	errStr := err.Error()
	networkErrors := []string{
		"connection reset",
		"broken pipe",
		"connection refused",
		"no route to host",
		"network is unreachable",
		"connection timed out",
	}

	for _, netErrStr := range networkErrors {
		if containsNetError(errStr, netErrStr) {
			return true
		}
	}

	return false
}

// containsNetError checks if a string contains a substring (case-insensitive)
func containsNetError(s, substr string) bool {
	return len(s) >= len(substr) && 
		(s == substr || 
		 len(s) > len(substr) && 
		 (containsNetErrorHelper(s, substr)))
}

func containsNetErrorHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// NetworkMonitor monitors network health
type NetworkMonitor struct {
	handler           *NetworkHandler
	checkInterval     time.Duration
	partitionCallback func()
	stopChan          chan struct{}
	wg                sync.WaitGroup
}

// NewNetworkMonitor creates a new network monitor
func NewNetworkMonitor(handler *NetworkHandler, checkInterval time.Duration) *NetworkMonitor {
	if checkInterval == 0 {
		checkInterval = 5 * time.Second
	}
	return &NetworkMonitor{
		handler:       handler,
		checkInterval: checkInterval,
		stopChan:      make(chan struct{}),
	}
}

// Start begins monitoring network health
func (m *NetworkMonitor) Start(partitionCallback func()) {
	m.partitionCallback = partitionCallback
	m.wg.Add(1)
	go m.monitor()
}

// Stop stops the network monitor
func (m *NetworkMonitor) Stop() {
	close(m.stopChan)
	m.wg.Wait()
}

// monitor runs the monitoring loop
func (m *NetworkMonitor) monitor() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			if m.handler.DetectNetworkPartition() && m.partitionCallback != nil {
				m.partitionCallback()
			}
		}
	}
}

// RetryableWriteMessage wraps a WebSocket message for retry
type RetryableWriteMessage struct {
	MessageType int
	Data        []byte
	Retries     int
	MaxRetries  int
}

// CanRetry returns true if the message can be retried
func (r *RetryableWriteMessage) CanRetry() bool {
	return r.Retries < r.MaxRetries
}

// IncrementRetries increments the retry count
func (r *RetryableWriteMessage) IncrementRetries() {
	r.Retries++
}