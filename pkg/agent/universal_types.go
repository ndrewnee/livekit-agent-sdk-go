package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
)

const (
	// CurrentProtocol is the current protocol version
	CurrentProtocol = 1

	// defaultPingInterval is the default ping interval
	defaultPingInterval = 30 * time.Second

	// defaultPingTimeout is the default ping timeout
	defaultPingTimeout = 10 * time.Second

	// defaultStatusRefreshInterval is the default status refresh interval
	defaultStatusRefreshInterval = 5 * time.Minute
)

// WorkerMessage represents a message sent from worker to server
type WorkerMessage struct {
	Message interface{} // Actual protobuf message
}

// Specific message types for WorkerMessage
type WorkerMessage_Register struct {
	Register *livekit.RegisterWorkerRequest
}

type WorkerMessage_Availability struct {
	Availability *livekit.AvailabilityResponse
}

type WorkerMessage_UpdateWorker struct {
	UpdateWorker *livekit.UpdateWorkerStatus
}

type WorkerMessage_UpdateJob struct {
	UpdateJob *livekit.UpdateJobStatus
}

type WorkerMessage_Ping struct {
	Ping *livekit.Ping
}

type WorkerMessage_Pong struct {
	Pong *livekit.Pong
}

type WorkerMessage_JobAccept struct {
	JobAccept *JobAcceptMessage
}

// JobAcceptMessage wraps job acceptance
type JobAcceptMessage struct {
	JobId  string
	Accept *JobAcceptInfo
}

// ServerMessage represents a message received from server
type ServerMessage struct {
	Message interface{} // Actual protobuf message
}

// Specific message types for ServerMessage
type ServerMessage_Register struct {
	Register *livekit.RegisterWorkerResponse
}

type ServerMessage_Availability struct {
	Availability *livekit.AvailabilityRequest
}

type ServerMessage_Assignment struct {
	Assignment *livekit.JobAssignment
}

type ServerMessage_Termination struct {
	Termination *livekit.JobTermination
}

type ServerMessage_Ping struct {
	Ping *livekit.Ping
}

// GetRegister returns the register response if this is a register message
func (m *ServerMessage) GetRegister() *livekit.RegisterWorkerResponse {
	if reg, ok := m.Message.(*ServerMessage_Register); ok {
		return reg.Register
	}
	return nil
}

// GetAvailability returns the availability request if this is an availability message
func (m *ServerMessage) GetAvailability() *livekit.AvailabilityRequest {
	if avail, ok := m.Message.(*ServerMessage_Availability); ok {
		return avail.Availability
	}
	return nil
}

// GetAssignment returns the job assignment if this is an assignment message
func (m *ServerMessage) GetAssignment() *livekit.JobAssignment {
	if assign, ok := m.Message.(*ServerMessage_Assignment); ok {
		return assign.Assignment
	}
	return nil
}

// GetTermination returns the job termination if this is a termination message
func (m *ServerMessage) GetTermination() *livekit.JobTermination {
	if term, ok := m.Message.(*ServerMessage_Termination); ok {
		return term.Termination
	}
	return nil
}

// GetPing returns the ping if this is a ping message
func (m *ServerMessage) GetPing() *livekit.Ping {
	if ping, ok := m.Message.(*ServerMessage_Ping); ok {
		return ping.Ping
	}
	return nil
}

// ShutdownHandler manages graceful shutdown
type ShutdownHandler struct {
	logger Logger
	hooks  map[ShutdownPhase][]ShutdownHook
	mu     sync.RWMutex
}

// NewShutdownHandler creates a new shutdown handler
func NewShutdownHandler(logger Logger) *ShutdownHandler {
	return &ShutdownHandler{
		logger: logger,
		hooks:  make(map[ShutdownPhase][]ShutdownHook),
	}
}

// ExecutePhase executes shutdown hooks for a specific phase
func (h *ShutdownHandler) ExecutePhase(ctx context.Context, phase ShutdownPhase) error {
	h.mu.RLock()
	hooks := h.hooks[phase]
	h.mu.RUnlock()

	for _, hook := range hooks {
		if err := hook.Handler(ctx); err != nil {
			if h.logger != nil {
				h.logger.Error("Shutdown hook failed", "phase", phase, "name", hook.Name, "error", err)
			}
		}
	}
	return nil
}

// AddHook adds a shutdown hook
func (h *ShutdownHandler) AddHook(phase ShutdownPhase, hook ShutdownHook) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hooks[phase] = append(h.hooks[phase], hook)
	return nil
}

// RemoveHook removes a shutdown hook
func (h *ShutdownHandler) RemoveHook(phase ShutdownPhase, name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	hooks := h.hooks[phase]
	for i, hook := range hooks {
		if hook.Name == name {
			h.hooks[phase] = append(hooks[:i], hooks[i+1:]...)
			return true
		}
	}
	return false
}

// GetHooks returns hooks for a phase
func (h *ShutdownHandler) GetHooks(phase ShutdownPhase) []ShutdownHook {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hooks[phase]
}

// MessageHandler manages custom message handlers
type MessageHandler struct {
	logger   Logger
	handlers map[string]func(*ServerMessage) error
}

// NewMessageHandler creates a new message handler
func NewMessageHandler(logger Logger) *MessageHandler {
	return &MessageHandler{
		logger:   logger,
		handlers: make(map[string]func(*ServerMessage) error),
	}
}

// RegisterHandler registers a handler for a message type
func (h *MessageHandler) RegisterHandler(messageType string, handler func(*ServerMessage) error) {
	h.handlers[messageType] = handler
}

// HandleMessage handles a server message
func (h *MessageHandler) HandleMessage(msg *ServerMessage) error {
	// Implementation would check message type and dispatch to appropriate handler
	return nil
}

// ProtocolValidator validates protocol messages
type ProtocolValidator struct {
	strictMode bool
}

// NewProtocolValidator creates a new protocol validator
func NewProtocolValidator(strictMode bool) *ProtocolValidator {
	return &ProtocolValidator{strictMode: strictMode}
}

// ValidateServerMessage validates a server message
func (v *ProtocolValidator) ValidateServerMessage(msg *ServerMessage) error {
	// Implementation would validate message structure
	return nil
}

// JobAcceptInfo contains info for accepting a job
type JobAcceptInfo struct {
	Identity       string
	Name           string
	Metadata       string
	Attributes     map[string]string
	SupportsResume bool
}

// DefaultLogger is a simple logger implementation
type DefaultLogger struct{}

// NewDefaultLogger creates a new default logger
func NewDefaultLogger() Logger {
	return &DefaultLogger{}
}

// Debug logs a debug message
func (l *DefaultLogger) Debug(msg string, fields ...interface{}) {
	log.Printf("[DEBUG] %s %v", msg, fields)
}

// Info logs an info message
func (l *DefaultLogger) Info(msg string, fields ...interface{}) {
	log.Printf("[INFO] %s %v", msg, fields)
}

// Warn logs a warning message
func (l *DefaultLogger) Warn(msg string, fields ...interface{}) {
	log.Printf("[WARN] %s %v", msg, fields)
}

// Error logs an error message
func (l *DefaultLogger) Error(msg string, fields ...interface{}) {
	log.Printf("[ERROR] %s %v", msg, fields)
}

// ResourceLimits defines resource limits for the worker
type ResourceLimits struct {
	MaxMemoryMB        int
	MaxCPUPercent      float64
	MaxFileDescriptors int
}

// ErrNotConnected is returned when trying to send a message while disconnected
var ErrNotConnected = errors.New("not connected to server")

// workerStatusToProto converts WorkerStatus to protobuf enum
func workerStatusToProto(status WorkerStatus) livekit.WorkerStatus {
	switch status {
	case WorkerStatusAvailable:
		return livekit.WorkerStatus_WS_AVAILABLE
	case WorkerStatusFull:
		return livekit.WorkerStatus_WS_FULL
	default:
		return livekit.WorkerStatus_WS_AVAILABLE
	}
}

// buildWebSocketURL builds the WebSocket URL for agent connection
func buildWebSocketURL(serverURL string) string {
	// Convert http/https to ws/wss
	wsURL := serverURL
	if len(wsURL) > 4 && wsURL[:4] == "http" {
		if wsURL[:5] == "https" {
			wsURL = "wss" + wsURL[5:]
		} else {
			wsURL = "ws" + wsURL[4:]
		}
	}
	// Add agent path and protocol version
	if wsURL[len(wsURL)-1] != '/' {
		wsURL += "/"
	}
	return fmt.Sprintf("%sagent?protocol=%d", wsURL, CurrentProtocol)
}

// workerTypeToProto converts JobType to protobuf job type (no conversion needed)
func workerTypeToProto(jobType livekit.JobType) livekit.JobType {
	return jobType
}
