package helpers

import (
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/proto"
)

// AssertJobEqual checks if two jobs are equal
func AssertJobEqual(t *testing.T, expected, actual *livekit.Job) {
	t.Helper()
	
	if expected.Id != actual.Id {
		t.Errorf("Job ID mismatch: expected %s, got %s", expected.Id, actual.Id)
	}
	
	if expected.Type != actual.Type {
		t.Errorf("Job type mismatch: expected %v, got %v", expected.Type, actual.Type)
	}
	
	if expected.Room != nil && actual.Room != nil {
		if expected.Room.Name != actual.Room.Name {
			t.Errorf("Room name mismatch: expected %s, got %s", expected.Room.Name, actual.Room.Name)
		}
	} else if expected.Room != actual.Room {
		t.Errorf("Room mismatch: expected %v, got %v", expected.Room, actual.Room)
	}
	
	if expected.AgentName != actual.AgentName {
		t.Errorf("Agent name mismatch: expected %s, got %s", expected.AgentName, actual.AgentName)
	}
}

// AssertMessageType checks the type of a protobuf message
func AssertMessageType[T proto.Message](t *testing.T, data []byte, msgType T) T {
	t.Helper()
	
	msg := msgType
	err := proto.Unmarshal(data, msg)
	if err != nil {
		t.Fatalf("Failed to unmarshal message: %v", err)
	}
	
	return msg
}

// AssertEventuallyTrue waits for a condition to become true
func AssertEventuallyTrue(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	t.Helper()
	
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	
	for {
		if condition() {
			return
		}
		
		select {
		case <-ticker.C:
			if time.Now().After(deadline) {
				t.Fatal(message)
			}
		}
	}
}

// AssertNoError checks that error is nil
func AssertNoError(t *testing.T, err error, message string) {
	t.Helper()
	
	if err != nil {
		t.Fatalf("%s: %v", message, err)
	}
}

// AssertError checks that error is not nil
func AssertError(t *testing.T, err error, message string) {
	t.Helper()
	
	if err == nil {
		t.Fatal(message)
	}
}

// AssertEqual checks if two values are equal
func AssertEqual[T comparable](t *testing.T, expected, actual T, message string) {
	t.Helper()
	
	if expected != actual {
		t.Errorf("%s: expected %v, got %v", message, expected, actual)
	}
}

// AssertNotEqual checks if two values are not equal
func AssertNotEqual[T comparable](t *testing.T, expected, actual T, message string) {
	t.Helper()
	
	if expected == actual {
		t.Errorf("%s: expected values to be different, but both are %v", message, expected)
	}
}

// AssertTrue checks if condition is true
func AssertTrue(t *testing.T, condition bool, message string) {
	t.Helper()
	
	if !condition {
		t.Error(message)
	}
}

// AssertFalse checks if condition is false
func AssertFalse(t *testing.T, condition bool, message string) {
	t.Helper()
	
	if condition {
		t.Error(message)
	}
}

// AssertNil checks if value is nil
func AssertNil(t *testing.T, value interface{}, message string) {
	t.Helper()
	
	if value != nil {
		t.Errorf("%s: expected nil, got %v", message, value)
	}
}

// AssertNotNil checks if value is not nil
func AssertNotNil(t *testing.T, value interface{}, message string) {
	t.Helper()
	
	if value == nil {
		t.Error(message)
	}
}

// AssertLen checks the length of a slice or map
func AssertLen[T any](t *testing.T, collection []T, expectedLen int, message string) {
	t.Helper()
	
	if len(collection) != expectedLen {
		t.Errorf("%s: expected length %d, got %d", message, expectedLen, len(collection))
	}
}

// AssertMapLen checks the length of a map
func AssertMapLen[K comparable, V any](t *testing.T, m map[K]V, expectedLen int, message string) {
	t.Helper()
	
	if len(m) != expectedLen {
		t.Errorf("%s: expected length %d, got %d", message, expectedLen, len(m))
	}
}

// AssertContains checks if a slice contains a value
func AssertContains[T comparable](t *testing.T, slice []T, value T, message string) {
	t.Helper()
	
	for _, v := range slice {
		if v == value {
			return
		}
	}
	
	t.Errorf("%s: value %v not found in slice", message, value)
}

// AssertNotContains checks if a slice does not contain a value
func AssertNotContains[T comparable](t *testing.T, slice []T, value T, message string) {
	t.Helper()
	
	for _, v := range slice {
		if v == value {
			t.Errorf("%s: value %v found in slice", message, value)
			return
		}
	}
}

// AssertMapContainsKey checks if a map contains a key
func AssertMapContainsKey[K comparable, V any](t *testing.T, m map[K]V, key K, message string) {
	t.Helper()
	
	if _, exists := m[key]; !exists {
		t.Errorf("%s: key %v not found in map", message, key)
	}
}

// AssertWorkerMessageType checks the specific type of a worker message
func AssertWorkerMessageType(t *testing.T, msg *livekit.WorkerMessage, expectedType string) {
	t.Helper()
	
	var actualType string
	switch msg.Message.(type) {
	case *livekit.WorkerMessage_Register:
		actualType = "register"
	case *livekit.WorkerMessage_Availability:
		actualType = "availability"
	case *livekit.WorkerMessage_UpdateJob:
		actualType = "update_job"
	case *livekit.WorkerMessage_Ping:
		actualType = "ping"
	case *livekit.WorkerMessage_UpdateWorker:
		actualType = "update_worker"
	default:
		actualType = "unknown"
	}
	
	if actualType != expectedType {
		t.Errorf("Expected worker message type %s, got %s", expectedType, actualType)
	}
}

// AssertServerMessageType checks the specific type of a server message
func AssertServerMessageType(t *testing.T, msg *livekit.ServerMessage, expectedType string) {
	t.Helper()
	
	var actualType string
	switch msg.Message.(type) {
	case *livekit.ServerMessage_Register:
		actualType = "register"
	case *livekit.ServerMessage_Availability:
		actualType = "availability"
	case *livekit.ServerMessage_Assignment:
		actualType = "assignment"
	case *livekit.ServerMessage_Termination:
		actualType = "termination"
	case *livekit.ServerMessage_Pong:
		actualType = "pong"
	default:
		actualType = "unknown"
	}
	
	if actualType != expectedType {
		t.Errorf("Expected server message type %s, got %s", expectedType, actualType)
	}
}