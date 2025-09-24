package agent

import (
	"context"
	"testing"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
	"sync/atomic"
)

type shortBatchProc struct{ seen int32 }

func (s *shortBatchProc) ShouldBatch(event ParticipantEvent) bool { return true }
func (s *shortBatchProc) ProcessBatch(events []ParticipantEvent) error {
	atomic.AddInt32(&s.seen, int32(len(events)))
	return nil
}
func (s *shortBatchProc) GetBatchSize() int              { return 2 }
func (s *shortBatchProc) GetBatchTimeout() time.Duration { return 5 * time.Millisecond }

func TestParticipantEventProcessor_ProcessBatches_TimerAndFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &ParticipantEventProcessor{
		eventQueue:        make(chan ParticipantEvent, 10),
		handlers:          make(map[EventType][]EventHandler),
		filters:           nil,
		batchProcessors:   make([]BatchEventProcessor, 0),
		eventHistory:      make([]ParticipantEvent, 0),
		historySize:       100,
		processingMetrics: &EventProcessingMetrics{processingTimes: make(map[EventType][]time.Duration), eventCounts: make(map[EventType]int64)},
		ctx:               ctx,
		cancel:            cancel,
	}
	sb := &shortBatchProc{}
	p.AddBatchProcessor(sb)

	done := make(chan struct{})
	p.wg.Add(1)
	go func() { p.processBatches(); close(done) }()

	rp := &lksdk.RemoteParticipant{}
	// Enqueue events to trigger both timer and batch-size criteria
	p.eventQueue <- ParticipantEvent{Type: EventTypeDataReceived, Participant: rp, Timestamp: time.Now()}
	time.Sleep(7 * time.Millisecond) // let timer fire for first event
	p.eventQueue <- ParticipantEvent{Type: EventTypeDataReceived, Participant: rp, Timestamp: time.Now()}
	p.eventQueue <- ParticipantEvent{Type: EventTypeDataReceived, Participant: rp, Timestamp: time.Now()}

	// Cancel processing and wait
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("processBatches did not exit")
	}
}
