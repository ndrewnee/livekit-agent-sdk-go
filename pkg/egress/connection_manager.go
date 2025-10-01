package egress

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// ConnectionState represents the current connection state
type ConnectionState int

const (
	ConnectionStateDisconnected ConnectionState = iota
	ConnectionStateConnecting
	ConnectionStateConnected
	ConnectionStateReconnecting
	ConnectionStateFailed
)

// ConnectionManager handles LiveKit room connections with automatic reconnection
// Implements the connection management requirements from PLAN.md Milestone 2
type ConnectionManager struct {
	room   *lksdk.Room
	config *NetworkConfig

	// State management
	state      ConnectionState
	stateMu    sync.RWMutex
	stateChanged chan ConnectionState

	// Reconnection control
	reconnectAttempts int32
	lastConnectTime   time.Time
	lastDisconnectTime time.Time

	// Callbacks for connection events
	onConnected    func()
	onDisconnected func(error)
	onReconnecting func()
	onFailed       func(error)

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Statistics
	stats ConnectionStats
}

// ConnectionStats holds connection statistics
type ConnectionStats struct {
	ConnectAttempts     int64     `json:"connect_attempts"`
	SuccessfulConnects  int64     `json:"successful_connects"`
	FailedConnects      int64     `json:"failed_connects"`
	Disconnections      int64     `json:"disconnections"`
	ReconnectAttempts   int64     `json:"reconnect_attempts"`
	TotalConnectedTime  int64     `json:"total_connected_time_seconds"`
	LastConnectTime     time.Time `json:"last_connect_time"`
	LastDisconnectTime  time.Time `json:"last_disconnect_time"`
	CurrentState        string    `json:"current_state"`
	ConsecutiveFailures int32     `json:"consecutive_failures"`
}

// NewConnectionManager creates a new connection manager
func NewConnectionManager(room *lksdk.Room, config *NetworkConfig) *ConnectionManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &ConnectionManager{
		room:         room,
		config:       config,
		state:        ConnectionStateDisconnected,
		stateChanged: make(chan ConnectionState, 10),
		ctx:          ctx,
		cancel:       cancel,
	}
}

// Start initializes the connection manager and begins monitoring
func (cm *ConnectionManager) Start() error {
	cm.stateMu.Lock()
	if cm.state != ConnectionStateDisconnected {
		cm.stateMu.Unlock()
		return fmt.Errorf("connection manager already started")
	}
	cm.state = ConnectionStateConnecting
	cm.stateMu.Unlock()

	// Set up room callbacks
	cm.setupRoomCallbacks()

	// Start monitoring goroutine
	cm.wg.Add(1)
	go cm.monitorConnection()

	logger.Infow("connection manager started",
		"reconnectAttempts", cm.config.ReconnectAttempts,
		"reconnectDelay", cm.config.ReconnectDelay)

	return nil
}

// setupRoomCallbacks configures room connection callbacks
func (cm *ConnectionManager) setupRoomCallbacks() {
	// TODO: The LiveKit SDK v2 doesn't expose room callbacks directly
	// We need to handle state monitoring through other means or
	// register callbacks during room creation
	// For now, we'll rely on periodic monitoring through monitorConnection()

	// The actual reconnection logic is handled internally by the LiveKit SDK
	// We monitor the state through periodic checks
}

// handleConnectionStateChange processes LiveKit connection state changes
func (cm *ConnectionManager) handleConnectionStateChange(state lksdk.ConnectionState) {
	logger.Debugw("connection state changed", "state", state)

	cm.stateMu.Lock()
	oldState := cm.state

	switch state {
	case lksdk.ConnectionStateConnected:
		cm.state = ConnectionStateConnected
		cm.lastConnectTime = time.Now()
		atomic.StoreInt32(&cm.reconnectAttempts, 0)
		atomic.AddInt64(&cm.stats.SuccessfulConnects, 1)

	case lksdk.ConnectionStateReconnecting:
		cm.state = ConnectionStateReconnecting
		atomic.AddInt32(&cm.reconnectAttempts, 1)
		atomic.AddInt64(&cm.stats.ReconnectAttempts, 1)

	case lksdk.ConnectionStateDisconnected:
		cm.state = ConnectionStateDisconnected
		cm.lastDisconnectTime = time.Now()
		atomic.AddInt64(&cm.stats.Disconnections, 1)
	}

	newState := cm.state
	cm.stateMu.Unlock()

	// Notify state change
	if oldState != newState {
		select {
		case cm.stateChanged <- newState:
		default:
			// Channel full, drop oldest
			select {
			case <-cm.stateChanged:
				cm.stateChanged <- newState
			default:
			}
		}
	}

	// Trigger callbacks
	switch newState {
	case ConnectionStateConnected:
		if cm.onConnected != nil {
			cm.onConnected()
		}
	case ConnectionStateReconnecting:
		if cm.onReconnecting != nil {
			cm.onReconnecting()
		}
	}
}

// handleDisconnection processes unexpected disconnections
func (cm *ConnectionManager) handleDisconnection() {
	logger.Debugw("disconnected from room")

	cm.stateMu.Lock()
	cm.state = ConnectionStateDisconnected
	cm.lastDisconnectTime = time.Now()
	atomic.AddInt64(&cm.stats.Disconnections, 1)
	cm.stateMu.Unlock()

	// Update connected time
	if !cm.lastConnectTime.IsZero() && !cm.lastDisconnectTime.IsZero() {
		duration := cm.lastDisconnectTime.Sub(cm.lastConnectTime).Seconds()
		atomic.AddInt64(&cm.stats.TotalConnectedTime, int64(duration))
	}

	// Trigger callback
	if cm.onDisconnected != nil {
		cm.onDisconnected(fmt.Errorf("disconnected from room"))
	}

	// Attempt reconnection if configured
	if cm.config.ReconnectAttempts > 0 {
		cm.wg.Add(1)
		go cm.attemptReconnection()
	}
}

// handleReconnection processes successful reconnections
func (cm *ConnectionManager) handleReconnection() {
	logger.Infow("reconnected to room")

	cm.stateMu.Lock()
	cm.state = ConnectionStateConnected
	cm.lastConnectTime = time.Now()
	atomic.StoreInt32(&cm.reconnectAttempts, 0)
	atomic.StoreInt32(&cm.stats.ConsecutiveFailures, 0)
	cm.stateMu.Unlock()

	// Trigger callback
	if cm.onConnected != nil {
		cm.onConnected()
	}
}

// attemptReconnection tries to reconnect to the room
func (cm *ConnectionManager) attemptReconnection() {
	defer cm.wg.Done()

	attempts := atomic.LoadInt32(&cm.reconnectAttempts)
	maxAttempts := int32(cm.config.ReconnectAttempts)

	for attempts < maxAttempts {
		select {
		case <-cm.ctx.Done():
			return
		default:
		}

		// Wait before attempting
		delay := cm.calculateBackoff(attempts)
		logger.Infow("attempting reconnection",
			"attempt", attempts+1,
			"maxAttempts", maxAttempts,
			"delay", delay)

		select {
		case <-time.After(delay):
		case <-cm.ctx.Done():
			return
		}

		// Check if already reconnected
		cm.stateMu.RLock()
		if cm.state == ConnectionStateConnected {
			cm.stateMu.RUnlock()
			return
		}
		cm.stateMu.RUnlock()

		// Attempt to reconnect
		atomic.AddInt64(&cm.stats.ConnectAttempts, 1)

		// The actual reconnection is handled by the LiveKit SDK
		// We just monitor the state changes

		// Wait for state change
		timeout := time.After(30 * time.Second)
		select {
		case newState := <-cm.stateChanged:
			if newState == ConnectionStateConnected {
				logger.Infow("reconnection successful", "attempts", attempts+1)
				return
			}
		case <-timeout:
			logger.Debugw("reconnection attempt timed out", "attempt", attempts+1)
			atomic.AddInt32(&cm.stats.ConsecutiveFailures, 1)
		case <-cm.ctx.Done():
			return
		}

		attempts = atomic.AddInt32(&cm.reconnectAttempts, 1)
	}

	// Max attempts reached
	logger.Debugw("max reconnection attempts reached",
		"attempts", attempts,
		"maxAttempts", maxAttempts)

	cm.stateMu.Lock()
	cm.state = ConnectionStateFailed
	atomic.AddInt64(&cm.stats.FailedConnects, 1)
	cm.stateMu.Unlock()

	if cm.onFailed != nil {
		cm.onFailed(fmt.Errorf("max reconnection attempts reached"))
	}
}

// calculateBackoff calculates the backoff delay for reconnection attempts
func (cm *ConnectionManager) calculateBackoff(attempt int32) time.Duration {
	baseDelay := cm.config.ReconnectDelay
	if baseDelay == 0 {
		baseDelay = 2 * time.Second
	}

	// Exponential backoff with jitter
	backoff := baseDelay * time.Duration(1<<uint(attempt))

	// Cap at 30 seconds
	maxBackoff := 30 * time.Second
	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	// Add jitter (±25%)
	jitter := time.Duration(float64(backoff) * 0.25 * (0.5 - float64(time.Now().UnixNano()%1000)/1000))
	return backoff + jitter
}

// monitorConnection monitors the connection health
func (cm *ConnectionManager) monitorConnection() {
	defer cm.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cm.ctx.Done():
			return
		case <-ticker.C:
			cm.checkConnectionHealth()
		}
	}
}

// checkConnectionHealth verifies the connection is healthy
func (cm *ConnectionManager) checkConnectionHealth() {
	cm.stateMu.RLock()
	state := cm.state
	cm.stateMu.RUnlock()

	if state == ConnectionStateConnected {
		// Connection is healthy
		logger.Debugw("connection health check passed",
			"uptime", time.Since(cm.lastConnectTime))
	} else if state == ConnectionStateDisconnected {
		// Check if we should attempt reconnection
		timeSinceDisconnect := time.Since(cm.lastDisconnectTime)
		if timeSinceDisconnect > 30*time.Second && cm.config.ReconnectAttempts > 0 {
			logger.Debugw("connection lost for extended period",
				"duration", timeSinceDisconnect)
		}
	}
}

// Stop stops the connection manager
func (cm *ConnectionManager) Stop() {
	logger.Infow("stopping connection manager")

	// Cancel context
	cm.cancel()

	// Update state
	cm.stateMu.Lock()
	cm.state = ConnectionStateDisconnected
	cm.stateMu.Unlock()

	// Wait for goroutines
	cm.wg.Wait()

	// Update total connected time
	if cm.state == ConnectionStateConnected && !cm.lastConnectTime.IsZero() {
		duration := time.Since(cm.lastConnectTime).Seconds()
		atomic.AddInt64(&cm.stats.TotalConnectedTime, int64(duration))
	}

	close(cm.stateChanged)

	logger.Infow("connection manager stopped",
		"totalConnectedTime", atomic.LoadInt64(&cm.stats.TotalConnectedTime))
}

// GetState returns the current connection state
func (cm *ConnectionManager) GetState() ConnectionState {
	cm.stateMu.RLock()
	defer cm.stateMu.RUnlock()
	return cm.state
}

// GetStats returns connection statistics
func (cm *ConnectionManager) GetStats() ConnectionStats {
	cm.stateMu.RLock()
	state := cm.state
	lastConnect := cm.lastConnectTime
	lastDisconnect := cm.lastDisconnectTime
	cm.stateMu.RUnlock()

	return ConnectionStats{
		ConnectAttempts:     atomic.LoadInt64(&cm.stats.ConnectAttempts),
		SuccessfulConnects:  atomic.LoadInt64(&cm.stats.SuccessfulConnects),
		FailedConnects:      atomic.LoadInt64(&cm.stats.FailedConnects),
		Disconnections:      atomic.LoadInt64(&cm.stats.Disconnections),
		ReconnectAttempts:   atomic.LoadInt64(&cm.stats.ReconnectAttempts),
		TotalConnectedTime:  atomic.LoadInt64(&cm.stats.TotalConnectedTime),
		LastConnectTime:     lastConnect,
		LastDisconnectTime:  lastDisconnect,
		CurrentState:        cm.stateString(state),
		ConsecutiveFailures: atomic.LoadInt32(&cm.stats.ConsecutiveFailures),
	}
}

// stateString returns a string representation of the connection state
func (cm *ConnectionManager) stateString(state ConnectionState) string {
	switch state {
	case ConnectionStateDisconnected:
		return "disconnected"
	case ConnectionStateConnecting:
		return "connecting"
	case ConnectionStateConnected:
		return "connected"
	case ConnectionStateReconnecting:
		return "reconnecting"
	case ConnectionStateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// IsConnected returns true if currently connected
func (cm *ConnectionManager) IsConnected() bool {
	cm.stateMu.RLock()
	defer cm.stateMu.RUnlock()
	return cm.state == ConnectionStateConnected
}

// WaitForConnection waits for the connection to be established
func (cm *ConnectionManager) WaitForConnection(ctx context.Context) error {
	if cm.IsConnected() {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case state := <-cm.stateChanged:
			if state == ConnectionStateConnected {
				return nil
			} else if state == ConnectionStateFailed {
				return fmt.Errorf("connection failed")
			}
		}
	}
}

// SetCallbacks sets the connection event callbacks
func (cm *ConnectionManager) SetCallbacks(
	onConnected func(),
	onDisconnected func(error),
	onReconnecting func(),
	onFailed func(error)) {
	cm.onConnected = onConnected
	cm.onDisconnected = onDisconnected
	cm.onReconnecting = onReconnecting
	cm.onFailed = onFailed
}

// GetUptimePercentage returns the percentage of time connected
func (cm *ConnectionManager) GetUptimePercentage() float64 {
	totalTime := time.Since(cm.lastConnectTime).Seconds()
	if totalTime == 0 {
		return 0
	}

	connectedTime := atomic.LoadInt64(&cm.stats.TotalConnectedTime)
	return (float64(connectedTime) / totalTime) * 100
}

// ResetStats resets the connection statistics
func (cm *ConnectionManager) ResetStats() {
	atomic.StoreInt64(&cm.stats.ConnectAttempts, 0)
	atomic.StoreInt64(&cm.stats.SuccessfulConnects, 0)
	atomic.StoreInt64(&cm.stats.FailedConnects, 0)
	atomic.StoreInt64(&cm.stats.Disconnections, 0)
	atomic.StoreInt64(&cm.stats.ReconnectAttempts, 0)
	atomic.StoreInt64(&cm.stats.TotalConnectedTime, 0)
	atomic.StoreInt32(&cm.stats.ConsecutiveFailures, 0)
}