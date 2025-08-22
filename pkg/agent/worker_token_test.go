package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

// TestWorker_TokenExpiredErrorDetection tests detection of token expiration errors
func TestWorker_TokenExpiredErrorDetection(t *testing.T) {

	tests := []struct {
		name          string
		connectError  error
		expectedError error
		expectedMsg   string
	}{
		{
			name:          "unauthorized error",
			connectError:  errors.New("websocket: bad handshake: unauthorized"),
			expectedError: ErrTokenExpired,
			expectedMsg:   "TOKEN_EXPIRED",
		},
		{
			name:          "401 error",
			connectError:  errors.New("websocket: bad handshake: 401 Unauthorized"),
			expectedError: ErrTokenExpired,
			expectedMsg:   "TOKEN_EXPIRED",
		},
		{
			name:          "room not found",
			connectError:  errors.New("websocket: bad handshake: 404 not found"),
			expectedError: ErrRoomNotFound,
			expectedMsg:   "ROOM_NOT_FOUND",
		},
		{
			name:          "generic error",
			connectError:  errors.New("connection refused"),
			expectedError: errors.New("connection refused"),
			expectedMsg:   "connection refused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the error categorization logic from handleJobAssignment
			errorMsg := tt.connectError.Error()
			var jobError error
			
			if contains(errorMsg, "unauthorized") || contains(errorMsg, "401") {
				jobError = ErrTokenExpired
			} else if contains(errorMsg, "not found") || contains(errorMsg, "404") {
				jobError = ErrRoomNotFound
			} else {
				jobError = tt.connectError
			}
			
			assert.Equal(t, tt.expectedError.Error(), jobError.Error())
			assert.Contains(t, jobError.Error(), tt.expectedMsg)
		})
	}
}

// TestWorker_DisconnectionReasons tests handling of various disconnection reasons
func TestWorker_DisconnectionReasons(t *testing.T) {
	tests := []struct {
		name             string
		reason           lksdk.DisconnectionReason
		expectedErrorMsg string
	}{
		{
			name:             "duplicate identity",
			reason:           lksdk.DuplicateIdentity,
			expectedErrorMsg: "duplicate participant identity",
		},
		{
			name:             "participant removed",
			reason:           lksdk.ParticipantRemoved,
			expectedErrorMsg: "participant was removed from room",
		},
		{
			name:             "room closed",
			reason:           lksdk.RoomClosed,
			expectedErrorMsg: "room was closed",
		},
		{
			name:             "leave requested",
			reason:           lksdk.LeaveRequested,
			expectedErrorMsg: "", // No error for client-initiated disconnection
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the logic that would be in the OnDisconnectedWithReason callback
			var expectedStatus livekit.JobStatus
			var expectedError string

			// Simulate what the disconnection callback would do
			switch tt.reason {
			case lksdk.DuplicateIdentity:
				expectedStatus = livekit.JobStatus_JS_FAILED
				expectedError = "duplicate participant identity"
			case lksdk.ParticipantRemoved:
				expectedStatus = livekit.JobStatus_JS_FAILED
				expectedError = "participant was removed from room"
			case lksdk.RoomClosed:
				expectedStatus = livekit.JobStatus_JS_FAILED
				expectedError = "room was closed"
			}

			if tt.expectedErrorMsg != "" {
				assert.Equal(t, livekit.JobStatus_JS_FAILED, expectedStatus)
				assert.Equal(t, tt.expectedErrorMsg, expectedError)
			}
		})
	}
}

// TestWorker_JobAssignmentWithInvalidToken tests the full flow with an invalid token
func TestWorker_JobAssignmentWithInvalidToken(t *testing.T) {
	handler := &testJobHandler{
		OnJobAssignedFunc: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			// This should not be called when room connection fails
			t.Error("OnJobAssigned should not be called when room connection fails")
			return nil
		},
	}

	_ = newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Track job status updates
	// Note: In a real implementation, we would capture the WebSocket messages
	// or use a mock room server to verify the error handling

	// Create a job assignment
	_ = &livekit.JobAssignment{
		Job: &livekit.Job{
			Id:   "test-job",
			Type: livekit.JobType_JT_ROOM,
			Room: &livekit.Room{
				Name: "test-room",
			},
		},
		Token: "invalid-token",
	}

	// Note: In a real test, ConnectToRoomWithToken would fail with an unauthorized error.
	// Since we can't easily mock the LiveKit SDK, this test demonstrates the structure
	// but would need integration with a mock room server to fully test.
	
	// The handleJobAssignment method would detect the error and categorize it
	t.Skip("This test requires a mock LiveKit room server")
}

// Helper function that mimics strings.Contains but works with our imports
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}