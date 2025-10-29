package agent

import (
	"errors"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

// errBatchProcessor is a test batch processor that returns an error
type errBatchProcessor struct{}

func (e *errBatchProcessor) ShouldBatch(event ParticipantEvent) bool { return true }
func (e *errBatchProcessor) ProcessBatch(events []ParticipantEvent) error {
	return errors.New("batch error")
}
func (e *errBatchProcessor) GetBatchSize() int              { return 10 }
func (e *errBatchProcessor) GetBatchTimeout() time.Duration { return 10 * time.Millisecond }

func TestParticipantEventProcessor_ProcessBatch_ErrorPath(t *testing.T) {
	p := NewParticipantEventProcessor()
	defer p.Stop()

	// Directly invoke processBatch with a processor that returns an error
	proc := &errBatchProcessor{}
	p.processBatch(proc, []ParticipantEvent{{ID: "e1"}})
}

func TestParticipantEventProcessor_FilterAndHandlerError(t *testing.T) {
	p := NewParticipantEventProcessor()
	defer p.Stop()

	// Add a filter that drops events
	p.AddFilter(func(ev ParticipantEvent) bool { return false })

	// This event should be filtered and not processed
	p.QueueEvent(ParticipantEvent{
		Type:        EventTypeMetadataChanged,
		Participant: &lksdk.RemoteParticipant{},
		Timestamp:   time.Now(),
	})

	// Remove filters and add a handler that returns error
	p.mu.Lock()
	p.filters = nil
	p.mu.Unlock()

	p.RegisterHandler(EventTypeDataReceived, func(ParticipantEvent) error { return errors.New("handler error") })

	p.QueueEvent(ParticipantEvent{
		Type:        EventTypeDataReceived,
		Participant: &lksdk.RemoteParticipant{},
		Timestamp:   time.Now(),
	})

	// Allow background goroutines to process
	time.Sleep(20 * time.Millisecond)
}
