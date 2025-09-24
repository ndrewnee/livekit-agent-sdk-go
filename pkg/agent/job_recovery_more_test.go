package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/stretchr/testify/assert"
)

// Minimal worker mock for recovery tests
type recoveryTestWorker struct{}

func (w *recoveryTestWorker) UpdateStatus(status WorkerStatus, load float32) error               { return nil }
func (w *recoveryTestWorker) updateJobStatus(jobID string, status livekit.JobStatus, err string) {}
func (w *recoveryTestWorker) GetServerURL() string                                               { return "ws://localhost:7880" }
func (w *recoveryTestWorker) GetLogger() Logger                                                  { return NewDefaultLogger() }
func (w *recoveryTestWorker) GetActiveJobs() map[string]interface{}                              { return map[string]interface{}{} }
func (w *recoveryTestWorker) SetActiveJob(jobID string, job interface{})                         {}

func TestDefaultJobRecoveryHandler_Noops(t *testing.T) {
	h := &DefaultJobRecoveryHandler{}
	// Attempt decision
	ok := h.OnJobRecoveryAttempt(context.Background(), "job1", &JobState{Status: livekit.JobStatus_JS_RUNNING})
	assert.True(t, ok)
	ok = h.OnJobRecoveryAttempt(context.Background(), "job2", &JobState{Status: livekit.JobStatus_JS_SUCCESS})
	assert.False(t, ok)

	// No-op calls for coverage
	h.OnJobRecovered(context.Background(), &livekit.Job{}, &lksdk.Room{})
	h.OnJobRecoveryFailed(context.Background(), "job3", assert.AnError)
}

func TestPartialMessageBuffer_ClearAndGetComplete(t *testing.T) {
	b := NewPartialMessageBuffer(1024)
	// Append a complete JSON text message (messageType 1)
	assert.NoError(t, b.Append(1, []byte(`{"a":1}`)))
	mt, data, complete := b.GetComplete()
	assert.True(t, complete)
	assert.Equal(t, 1, mt)
	assert.Equal(t, `{"a":1}`, string(data))

	// Append then clear
	assert.NoError(t, b.Append(1, []byte(`{"b":2}`)))
	b.Clear()
	_, _, complete = b.GetComplete()
	assert.False(t, complete)
}

func TestPartialMessageBuffer_DefaultMaxSizeAndTypeChange(t *testing.T) {
	b := NewPartialMessageBuffer(0) // defaults to 1MB
	// Append first text
	_ = b.Append(1, []byte(`{"a":1`))
	// Change message type resets buffer
	_ = b.Append(2, []byte{0x01, 0x02})
	_, data, complete := b.GetComplete()
	if complete || data != nil {
		t.Fatalf("expected incomplete binary without framing")
	}
}

func TestJobRecoveryManager_RecoverJob_NoToken(t *testing.T) {
	w := &recoveryTestWorker{}
	m := NewJobRecoveryManager(w, nil)

	err := m.recoverJob(context.Background(), "job-x", &RecoverableJob{
		JobData:   &livekit.Job{Room: &livekit.Room{Name: "room1"}},
		RoomToken: "", // no token, forces fallback path
		JobState:  &JobState{JobID: "job-x", Status: livekit.JobStatus_JS_RUNNING, StartedAt: time.Now()},
	})
	assert.Error(t, err)
}

func TestJobRecoveryManager_IncrementAttempts_Removal(t *testing.T) {
	w := &recoveryTestWorker{}
	m := NewJobRecoveryManager(w, nil)
	m.pendingRecovery["r1"] = &RecoverableJob{}

	// 4 increments -> removed
	m.incrementRecoveryAttempts("r1")
	m.incrementRecoveryAttempts("r1")
	m.incrementRecoveryAttempts("r1")
	m.incrementRecoveryAttempts("r1")

	_, exists := m.pendingRecovery["r1"]
	assert.False(t, exists)
}

func TestJobRecoveryManager_RecoverJob_WithInvalidToken(t *testing.T) {
	w := &recoveryTestWorker{}
	m := NewJobRecoveryManager(w, nil)
	// Provide a fake token and job; connection should fail and return error (still covered path)
	err := m.recoverJob(context.Background(), "job-y", &RecoverableJob{
		JobData:   &livekit.Job{Room: &livekit.Room{Name: "room2"}},
		RoomToken: "invalidtoken",
		JobState:  &JobState{JobID: "job-y", Status: livekit.JobStatus_JS_RUNNING, StartedAt: time.Now()},
	})
	if err == nil {
		t.Fatalf("expected error for invalid token")
	}
}
