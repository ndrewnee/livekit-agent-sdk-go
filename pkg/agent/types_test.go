package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
)

func TestError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *Error
		expected string
	}{
		{
			name: "connection failed error",
			err:  ErrConnectionFailed,
			expected: "CONNECTION_FAILED: failed to connect to LiveKit server",
		},
		{
			name: "authentication error",
			err:  ErrAuthenticationError,
			expected: "AUTHENTICATION_ERROR: authentication failed",
		},
		{
			name: "custom error",
			err: &Error{
				Code:    "CUSTOM_ERROR",
				Message: "custom error message",
			},
			expected: "CUSTOM_ERROR: custom error message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Errorf("Error.Error() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestJobMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata *JobMetadata
	}{
		{
			name: "basic metadata",
			metadata: &JobMetadata{
				ParticipantIdentity: "agent-123",
				ParticipantName:     "Test Agent",
			},
		},
		{
			name: "full metadata",
			metadata: &JobMetadata{
				ParticipantIdentity:   "agent-456",
				ParticipantName:       "Advanced Agent",
				ParticipantMetadata:   `{"version": "1.0"}`,
				ParticipantAttributes: map[string]string{
					"role": "transcriber",
					"lang": "en",
				},
				SupportsResume: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that fields are accessible
			if tt.metadata.ParticipantIdentity == "" {
				t.Error("ParticipantIdentity should not be empty")
			}
			if tt.metadata.ParticipantName == "" {
				t.Error("ParticipantName should not be empty")
			}
		})
	}
}

func TestWorkerOptions(t *testing.T) {
	tests := []struct {
		name string
		opts WorkerOptions
	}{
		{
			name: "minimal options",
			opts: WorkerOptions{
				JobType: livekit.JobType_JT_ROOM,
			},
		},
		{
			name: "full options",
			opts: WorkerOptions{
				AgentName: "test-agent",
				Version:   "1.0.0",
				Namespace: "production",
				JobType:   livekit.JobType_JT_PUBLISHER,
				Permissions: &livekit.ParticipantPermission{
					CanPublish:   true,
					CanSubscribe: true,
					CanPublishData: true,
				},
				MaxJobs: 10,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that options are properly set
			// Since JobType is an enum starting at 0 (JT_ROOM), we just check it's a valid value
			if int(tt.opts.JobType) < 0 || int(tt.opts.JobType) > 2 {
				t.Error("JobType should be a valid enum value")
			}
		})
	}
}

func TestWorkerStatus(t *testing.T) {
	tests := []struct {
		name   string
		status WorkerStatus
		value  int
	}{
		{
			name:   "available",
			status: WorkerStatusAvailable,
			value:  0,
		},
		{
			name:   "full",
			status: WorkerStatusFull,
			value:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if int(tt.status) != tt.value {
				t.Errorf("WorkerStatus value = %v, want %v", int(tt.status), tt.value)
			}
		})
	}
}

func TestLoggerInterface(t *testing.T) {
	// Test that our mock logger implements the Logger interface
	var _ Logger = (*mockTestLogger)(nil)
	
	logger := &mockTestLogger{}
	
	// Test all methods
	logger.Debug("debug message", "key", "value")
	logger.Info("info message", "count", 42)
	logger.Warn("warn message", "error", "something")
	logger.Error("error message", "fatal", true)
	
	// Verify calls were made
	if len(logger.calls) != 4 {
		t.Errorf("Expected 4 log calls, got %d", len(logger.calls))
	}
	
	expectedLevels := []string{"Debug", "Info", "Warn", "Error"}
	for i, call := range logger.calls {
		if call.level != expectedLevels[i] {
			t.Errorf("Call %d: expected level %s, got %s", i, expectedLevels[i], call.level)
		}
	}
}

// mockTestLogger is a simple logger for testing the interface
type mockTestLogger struct {
	calls []logCall
}

type logCall struct {
	level  string
	msg    string
	fields []interface{}
}

func (m *mockTestLogger) Debug(msg string, fields ...interface{}) {
	m.calls = append(m.calls, logCall{level: "Debug", msg: msg, fields: fields})
}

func (m *mockTestLogger) Info(msg string, fields ...interface{}) {
	m.calls = append(m.calls, logCall{level: "Info", msg: msg, fields: fields})
}

func (m *mockTestLogger) Warn(msg string, fields ...interface{}) {
	m.calls = append(m.calls, logCall{level: "Warn", msg: msg, fields: fields})
}

func (m *mockTestLogger) Error(msg string, fields ...interface{}) {
	m.calls = append(m.calls, logCall{level: "Error", msg: msg, fields: fields})
}