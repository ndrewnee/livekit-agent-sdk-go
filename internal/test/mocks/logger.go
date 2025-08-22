package mocks

import (
	"fmt"
	"sync"
)

// MockLogger implements a mock Logger for testing
type MockLogger struct {
	mu       sync.Mutex
	Messages []LogMessage
}

type LogMessage struct {
	Level   string
	Message string
	Fields  []interface{}
}

func NewMockLogger() *MockLogger {
	return &MockLogger{
		Messages: make([]LogMessage, 0),
	}
}

func (m *MockLogger) Debug(msg string, fields ...interface{}) {
	m.log("DEBUG", msg, fields...)
}

func (m *MockLogger) Info(msg string, fields ...interface{}) {
	m.log("INFO", msg, fields...)
}

func (m *MockLogger) Warn(msg string, fields ...interface{}) {
	m.log("WARN", msg, fields...)
}

func (m *MockLogger) Error(msg string, fields ...interface{}) {
	m.log("ERROR", msg, fields...)
}

func (m *MockLogger) log(level, msg string, fields ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.Messages = append(m.Messages, LogMessage{
		Level:   level,
		Message: msg,
		Fields:  fields,
	})
}

// GetMessages returns all logged messages
func (m *MockLogger) GetMessages() []LogMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]LogMessage{}, m.Messages...)
}

// GetMessagesByLevel returns messages for a specific log level
func (m *MockLogger) GetMessagesByLevel(level string) []LogMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	var filtered []LogMessage
	for _, msg := range m.Messages {
		if msg.Level == level {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

// HasMessage checks if a specific message was logged
func (m *MockLogger) HasMessage(level, message string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	for _, msg := range m.Messages {
		if msg.Level == level && msg.Message == message {
			return true
		}
	}
	return false
}

// Reset clears all logged messages
func (m *MockLogger) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Messages = make([]LogMessage, 0)
}

// String returns a string representation of all messages
func (m *MockLogger) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	var result string
	for _, msg := range m.Messages {
		result += fmt.Sprintf("[%s] %s", msg.Level, msg.Message)
		if len(msg.Fields) > 0 {
			result += fmt.Sprintf(" %v", msg.Fields)
		}
		result += "\n"
	}
	return result
}