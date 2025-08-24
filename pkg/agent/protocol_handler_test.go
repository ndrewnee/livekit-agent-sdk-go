package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestProtocolHandlerUnknownMessage tests handling of unknown message types
func TestProtocolHandlerUnknownMessage(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewProtocolHandler(logger)

	// Test non-strict mode (default)
	err := handler.HandleUnknownMessage("UnknownType", []byte("test data"))
	assert.NoError(t, err)
	assert.Equal(t, int64(1), handler.GetUnknownMessageCount())

	// Test multiple unknown messages
	err = handler.HandleUnknownMessage("UnknownType", []byte("test data 2"))
	assert.NoError(t, err)
	err = handler.HandleUnknownMessage("AnotherUnknown", []byte("test data 3"))
	assert.NoError(t, err)
	assert.Equal(t, int64(3), handler.GetUnknownMessageCount())

	// Check unsupported types tracking
	unsupported := handler.GetUnsupportedMessageTypes()
	assert.Equal(t, int64(2), unsupported["UnknownType"])
	assert.Equal(t, int64(1), unsupported["AnotherUnknown"])

	// Test strict mode
	handler.SetStrictMode(true)
	err = handler.HandleUnknownMessage("StrictUnknown", []byte("test"))
	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "unknown message type")
	}
}

// TestProtocolVersionHandling tests version compatibility checks
func TestProtocolVersionHandling(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewProtocolHandler(logger)

	// Test compatible version
	handler.SetServerVersion("1.0.0")
	assert.False(t, handler.IsVersionMismatchDetected())

	// Test incompatible version (too old)
	handler = NewProtocolHandler(logger)
	handler.SetServerVersion("0.8.0")
	assert.True(t, handler.IsVersionMismatchDetected())

	// Test incompatible version (too new)
	handler = NewProtocolHandler(logger)
	handler.SetServerVersion("3.0.0")
	assert.True(t, handler.IsVersionMismatchDetected())

	// Test empty version (should use default)
	handler = NewProtocolHandler(logger)
	handler.SetServerVersion("")
	assert.False(t, handler.IsVersionMismatchDetected())
}

// TestProtocolValidation tests protocol message validation
func TestProtocolValidation(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewProtocolHandler(logger)

	// Test nil message
	err := handler.ValidateProtocolMessage(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil message")

	// Test registration message with version
	msg := &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Register{
			Register: &livekit.RegisterWorkerResponse{
				WorkerId: "test-worker",
				ServerInfo: &livekit.ServerInfo{
					Version: "1.0.0",
				},
			},
		},
	}

	err = handler.ValidateProtocolMessage(msg)
	assert.NoError(t, err)
	assert.False(t, handler.IsVersionMismatchDetected())

	// Test registration with incompatible version in strict mode
	handler.SetStrictMode(true)
	msg.GetRegister().ServerInfo.Version = "0.5.0"
	err = handler.ValidateProtocolMessage(msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "protocol version incompatible")
}

// TestMessageTypeRegistry tests custom message type handling
func TestMessageTypeRegistry(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	registry := NewMessageTypeRegistry(logger)

	// Test handler registration
	handler := &mockMessageHandler{msgType: "CustomType"}
	registry.RegisterHandler(handler)

	assert.True(t, registry.HasHandler("CustomType"))
	assert.False(t, registry.HasHandler("UnknownType"))

	// Test message handling
	ctx := context.Background()
	err := registry.HandleMessage(ctx, "CustomType", []byte("test data"))
	assert.NoError(t, err)
	assert.Equal(t, 1, handler.handleCallCount)

	// Test unknown handler
	err = registry.HandleMessage(ctx, "UnknownType", []byte("test"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no handler registered")
}

// TestProtocolNegotiator tests version negotiation
func TestProtocolNegotiator(t *testing.T) {
	negotiator := NewProtocolNegotiator()

	// Test successful negotiation
	version, err := negotiator.NegotiateVersion([]string{"2.0.0", "1.0.0", "0.8.0"})
	assert.NoError(t, err)
	assert.Equal(t, "1.0.0", version)

	// Test with only compatible version
	version, err = negotiator.NegotiateVersion([]string{"0.9.0"})
	assert.NoError(t, err)
	assert.Equal(t, "0.9.0", version)

	// Test no compatible versions
	_, err = negotiator.NegotiateVersion([]string{"3.0.0", "2.5.0"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no compatible protocol version")

	// Test feature retrieval
	features := negotiator.GetFeatures("1.0.0")
	assert.Contains(t, features, "job_recovery")
	assert.Contains(t, features, "partial_messages")
	assert.Contains(t, features, "load_balancing")

	features = negotiator.GetFeatures("0.9.0")
	assert.Contains(t, features, "basic_jobs")
	assert.Contains(t, features, "status_updates")
}

// TestProtocolMetrics tests protocol metrics collection
func TestProtocolMetrics(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	handler := NewProtocolHandler(logger)

	// Set server version
	handler.SetServerVersion("1.0.0")

	// Generate some unknown messages
	_ = handler.HandleUnknownMessage("Type1", []byte("data"))
	_ = handler.HandleUnknownMessage("Type1", []byte("data"))
	_ = handler.HandleUnknownMessage("Type2", []byte("data"))

	// Get metrics
	metrics := handler.GetProtocolMetrics()

	assert.Equal(t, "1.0.0", metrics["server_version"])
	assert.Equal(t, "1.0.0", metrics["negotiated_version"])
	assert.Equal(t, CurrentProtocolVersion, metrics["current_version"])
	assert.Equal(t, int64(3), metrics["unknown_message_count"])
	assert.Equal(t, false, metrics["version_mismatch"])
	assert.Equal(t, false, metrics["strict_mode"])

	unsupported := metrics["unsupported_types"].(map[string]int64)
	assert.Equal(t, int64(2), unsupported["Type1"])
	assert.Equal(t, int64(1), unsupported["Type2"])
}

// TestProtocolUpgradeHandler tests protocol upgrade functionality
func TestProtocolUpgradeHandler(t *testing.T) {
	upgraded := false
	rolledBack := false

	// Test successful upgrade
	upgrader := &ProtocolUpgradeHandler{
		currentVersion: "1.0.0",
		targetVersion:  "1.1.0",
		upgradeFunc: func() error {
			upgraded = true
			return nil
		},
		rollbackFunc: func() error {
			rolledBack = true
			return nil
		},
	}

	err := upgrader.PerformUpgrade(1 * time.Second)
	assert.NoError(t, err)
	assert.True(t, upgraded)
	assert.False(t, rolledBack)

	// Test failed upgrade with rollback
	upgraded = false
	rolledBack = false
	upgrader.upgradeFunc = func() error {
		upgraded = true
		return assert.AnError
	}

	err = upgrader.PerformUpgrade(1 * time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "upgrade failed and rolled back")
	assert.True(t, upgraded)
	assert.True(t, rolledBack)

	// Test timeout with rollback
	upgraded = false
	rolledBack = false
	upgrader.upgradeFunc = func() error {
		time.Sleep(2 * time.Second)
		return nil
	}

	err = upgrader.PerformUpgrade(100 * time.Millisecond)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "protocol upgrade timed out")
	assert.True(t, rolledBack)
}

// mockMessageHandler is a test implementation of MessageTypeHandler
type mockMessageHandler struct {
	msgType         string
	handleCallCount int
}

func (m *mockMessageHandler) HandleMessage(ctx context.Context, data []byte) error {
	m.handleCallCount++
	return nil
}

func (m *mockMessageHandler) GetMessageType() string {
	return m.msgType
}
