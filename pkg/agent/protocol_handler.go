package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/livekit"
	"go.uber.org/zap"
)

// ProtocolVersion represents the current protocol version
const (
	CurrentProtocolVersion = "1.0.0"
	MinSupportedVersion    = "0.9.0"
	MaxSupportedVersion    = "2.0.0"
)

// ProtocolHandler manages protocol-level operations and compatibility
type ProtocolHandler struct {
	mu                     sync.RWMutex
	logger                 *zap.Logger
	serverVersion          string
	negotiatedVersion      string
	unknownMessageCount    int64
	unsupportedMessageTypes map[string]int64
	versionMismatchDetected bool
	strictMode             bool // If true, reject unknown message types
}

// NewProtocolHandler creates a new protocol handler
func NewProtocolHandler(logger *zap.Logger) *ProtocolHandler {
	return &ProtocolHandler{
		logger:                  logger,
		unsupportedMessageTypes: make(map[string]int64),
		strictMode:             false, // By default, log but don't fail on unknown messages
	}
}

// SetServerVersion sets the server version from registration response
func (p *ProtocolHandler) SetServerVersion(version string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.serverVersion = version
	p.negotiatedVersion = p.negotiateVersion(version)
}

// negotiateVersion determines the protocol version to use
func (p *ProtocolHandler) negotiateVersion(serverVersion string) string {
	// Simple version negotiation - in real implementation would parse and compare
	if serverVersion == "" {
		return CurrentProtocolVersion
	}
	
	// Check if server version is compatible
	if !p.isVersionCompatible(serverVersion) {
		p.versionMismatchDetected = true
		p.logger.Warn("Protocol version mismatch detected",
			zap.String("serverVersion", serverVersion),
			zap.String("clientVersion", CurrentProtocolVersion),
			zap.String("minSupported", MinSupportedVersion),
			zap.String("maxSupported", MaxSupportedVersion),
		)
	}
	
	return serverVersion
}

// isVersionCompatible checks if the server version is compatible
func (p *ProtocolHandler) isVersionCompatible(version string) bool {
	// Simplified compatibility check - in production would use semver
	if version < MinSupportedVersion || version > MaxSupportedVersion {
		return false
	}
	return true
}

// HandleUnknownMessage processes unknown message types
func (p *ProtocolHandler) HandleUnknownMessage(msgType string, data []byte) error {
	atomic.AddInt64(&p.unknownMessageCount, 1)
	
	p.mu.Lock()
	p.unsupportedMessageTypes[msgType]++
	count := p.unsupportedMessageTypes[msgType]
	strictMode := p.strictMode
	p.mu.Unlock()
	
	p.logger.Warn("Received unknown message type",
		zap.String("type", msgType),
		zap.Int("dataSize", len(data)),
		zap.Int64("occurrences", count),
		zap.Int64("totalUnknown", atomic.LoadInt64(&p.unknownMessageCount)),
	)
	
	if strictMode {
		return fmt.Errorf("unknown message type: %s", msgType)
	}
	
	// In non-strict mode, just log and continue
	return nil
}

// GetUnknownMessageCount returns the count of unknown messages received
func (p *ProtocolHandler) GetUnknownMessageCount() int64 {
	return atomic.LoadInt64(&p.unknownMessageCount)
}

// GetUnsupportedMessageTypes returns a copy of unsupported message types and their counts
func (p *ProtocolHandler) GetUnsupportedMessageTypes() map[string]int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	result := make(map[string]int64)
	for k, v := range p.unsupportedMessageTypes {
		result[k] = v
	}
	return result
}

// IsVersionMismatchDetected returns true if version mismatch was detected
func (p *ProtocolHandler) IsVersionMismatchDetected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.versionMismatchDetected
}

// SetStrictMode enables or disables strict protocol mode
func (p *ProtocolHandler) SetStrictMode(strict bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strictMode = strict
}

// ValidateProtocolMessage performs protocol-level validation
func (p *ProtocolHandler) ValidateProtocolMessage(msg *livekit.ServerMessage) error {
	if msg == nil {
		return fmt.Errorf("nil message")
	}
	
	// Check for version compatibility in messages that contain version info
	if reg := msg.GetRegister(); reg != nil && reg.ServerInfo != nil {
		p.SetServerVersion(reg.ServerInfo.Version)
		
		if p.versionMismatchDetected && p.strictMode {
			return fmt.Errorf("protocol version incompatible: server=%s, client=%s",
				reg.ServerInfo.Version, CurrentProtocolVersion)
		}
	}
	
	return nil
}

// GetProtocolMetrics returns protocol-related metrics
func (p *ProtocolHandler) GetProtocolMetrics() map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	
	metrics := map[string]interface{}{
		"server_version":        p.serverVersion,
		"negotiated_version":    p.negotiatedVersion,
		"current_version":       CurrentProtocolVersion,
		"unknown_message_count": atomic.LoadInt64(&p.unknownMessageCount),
		"version_mismatch":      p.versionMismatchDetected,
		"strict_mode":          p.strictMode,
		"unsupported_types":    p.GetUnsupportedMessageTypes(),
	}
	
	return metrics
}

// MessageTypeHandler handles specific message type processing
type MessageTypeHandler interface {
	HandleMessage(ctx context.Context, data []byte) error
	GetMessageType() string
}

// MessageTypeRegistry manages custom message type handlers
type MessageTypeRegistry struct {
	mu       sync.RWMutex
	handlers map[string]MessageTypeHandler
	logger   *zap.Logger
}

// NewMessageTypeRegistry creates a new message type registry
func NewMessageTypeRegistry(logger *zap.Logger) *MessageTypeRegistry {
	return &MessageTypeRegistry{
		handlers: make(map[string]MessageTypeHandler),
		logger:   logger,
	}
}

// RegisterHandler registers a handler for a specific message type
func (r *MessageTypeRegistry) RegisterHandler(handler MessageTypeHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	msgType := handler.GetMessageType()
	r.handlers[msgType] = handler
	r.logger.Debug("Registered message type handler", zap.String("type", msgType))
}

// HandleMessage processes a message with the appropriate handler
func (r *MessageTypeRegistry) HandleMessage(ctx context.Context, msgType string, data []byte) error {
	r.mu.RLock()
	handler, exists := r.handlers[msgType]
	r.mu.RUnlock()
	
	if !exists {
		return fmt.Errorf("no handler registered for message type: %s", msgType)
	}
	
	return handler.HandleMessage(ctx, data)
}

// HasHandler checks if a handler exists for the message type
func (r *MessageTypeRegistry) HasHandler(msgType string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.handlers[msgType]
	return exists
}

// ProtocolNegotiator handles protocol version negotiation
type ProtocolNegotiator struct {
	supportedVersions []string
	features          map[string][]string // Features supported by each version
}

// NewProtocolNegotiator creates a new protocol negotiator
func NewProtocolNegotiator() *ProtocolNegotiator {
	return &ProtocolNegotiator{
		supportedVersions: []string{"1.0.0", "0.9.0"},
		features: map[string][]string{
			"1.0.0": {"job_recovery", "partial_messages", "load_balancing"},
			"0.9.0": {"basic_jobs", "status_updates"},
		},
	}
}

// NegotiateVersion selects the best compatible version
func (n *ProtocolNegotiator) NegotiateVersion(serverVersions []string) (string, error) {
	// Find the highest version that both support
	for _, supported := range n.supportedVersions {
		for _, server := range serverVersions {
			if supported == server {
				return supported, nil
			}
		}
	}
	
	return "", fmt.Errorf("no compatible protocol version found")
}

// GetFeatures returns features available for a specific version
func (n *ProtocolNegotiator) GetFeatures(version string) []string {
	return n.features[version]
}

// ProtocolUpgradeHandler manages protocol upgrades
type ProtocolUpgradeHandler struct {
	currentVersion string
	targetVersion  string
	upgradeFunc    func() error
	rollbackFunc   func() error
}

// PerformUpgrade executes a protocol upgrade with rollback capability
func (h *ProtocolUpgradeHandler) PerformUpgrade(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	done := make(chan error, 1)
	go func() {
		done <- h.upgradeFunc()
	}()
	
	select {
	case err := <-done:
		if err != nil && h.rollbackFunc != nil {
			// Attempt rollback
			if rollbackErr := h.rollbackFunc(); rollbackErr != nil {
				return fmt.Errorf("upgrade failed: %v, rollback failed: %v", err, rollbackErr)
			}
			return fmt.Errorf("upgrade failed and rolled back: %v", err)
		}
		return err
	case <-ctx.Done():
		// Timeout - attempt rollback
		if h.rollbackFunc != nil {
			h.rollbackFunc()
		}
		return fmt.Errorf("protocol upgrade timed out")
	}
}