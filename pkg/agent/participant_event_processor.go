package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// EventType represents the type of participant event
type EventType string

const (
	EventTypeParticipantJoined  EventType = "participant_joined"
	EventTypeParticipantLeft    EventType = "participant_left"
	EventTypeTrackPublished     EventType = "track_published"
	EventTypeTrackUnpublished   EventType = "track_unpublished"
	EventTypeMetadataChanged    EventType = "metadata_changed"
	EventTypeNameChanged        EventType = "name_changed"
	EventTypePermissionsChanged EventType = "permissions_changed"
	EventTypeSpeakingChanged    EventType = "speaking_changed"
	EventTypeDataReceived       EventType = "data_received"
	EventTypeConnectionQuality  EventType = "connection_quality"
)

// ParticipantEvent represents an event related to a participant
type ParticipantEvent struct {
	ID          string
	Type        EventType
	Participant *lksdk.RemoteParticipant
	Track       *lksdk.RemoteTrackPublication
	OldValue    string
	NewValue    string
	Data        []byte
	DataKind    livekit.DataPacket_Kind
	Timestamp   time.Time
	Metadata    map[string]interface{}
}

// ParticipantEventProcessor processes participant events
type ParticipantEventProcessor struct {
	mu                sync.RWMutex
	eventQueue        chan ParticipantEvent
	handlers          map[EventType][]EventHandler
	filters           []EventFilter
	batchProcessors   []BatchEventProcessor
	eventHistory      []ParticipantEvent
	historySize       int
	processingMetrics *EventProcessingMetrics
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
}

// EventHandler handles individual events
type EventHandler func(event ParticipantEvent) error

// EventFilter filters events before processing
type EventFilter func(event ParticipantEvent) bool

// BatchEventProcessor processes events in batches
type BatchEventProcessor interface {
	// ShouldBatch determines if an event should be batched
	ShouldBatch(event ParticipantEvent) bool

	// ProcessBatch processes a batch of events
	ProcessBatch(events []ParticipantEvent) error

	// GetBatchSize returns the preferred batch size
	GetBatchSize() int

	// GetBatchTimeout returns the batch timeout
	GetBatchTimeout() time.Duration
}

// EventProcessingMetrics tracks event processing metrics
type EventProcessingMetrics struct {
	mu              sync.RWMutex
	totalEvents     int64
	processedEvents int64
	failedEvents    int64
	filteredEvents  int64
	batchedEvents   int64
	processingTimes map[EventType][]time.Duration
	eventCounts     map[EventType]int64
	lastProcessedAt time.Time
}

// NewParticipantEventProcessor creates a new event processor
func NewParticipantEventProcessor() *ParticipantEventProcessor {
	ctx, cancel := context.WithCancel(context.Background())

	processor := &ParticipantEventProcessor{
		eventQueue:      make(chan ParticipantEvent, 1000),
		handlers:        make(map[EventType][]EventHandler),
		filters:         make([]EventFilter, 0),
		batchProcessors: make([]BatchEventProcessor, 0),
		eventHistory:    make([]ParticipantEvent, 0),
		historySize:     100,
		processingMetrics: &EventProcessingMetrics{
			processingTimes: make(map[EventType][]time.Duration),
			eventCounts:     make(map[EventType]int64),
		},
		ctx:    ctx,
		cancel: cancel,
	}

	// Start event processing
	processor.wg.Add(1)
	go processor.processEvents()

	// Start batch processing
	processor.wg.Add(1)
	go processor.processBatches()

	return processor
}

// QueueEvent queues an event for processing
func (p *ParticipantEventProcessor) QueueEvent(event ParticipantEvent) {
	// Generate event ID if not set
	if event.ID == "" {
		event.ID = generateEventID()
	}

	// Update metrics
	p.processingMetrics.mu.Lock()
	p.processingMetrics.totalEvents++
	p.processingMetrics.eventCounts[event.Type]++
	p.processingMetrics.mu.Unlock()

	// Apply filters
	for _, filter := range p.filters {
		if !filter(event) {
			p.processingMetrics.mu.Lock()
			p.processingMetrics.filteredEvents++
			p.processingMetrics.mu.Unlock()
			return
		}
	}

	// Queue event
	select {
	case p.eventQueue <- event:
		// Event queued successfully
	case <-time.After(1 * time.Second):
		getLogger := logger.GetLogger()
		getLogger.Warnw("event queue full, dropping event", nil,
			"eventType", event.Type,
			"participant", event.Participant.Identity())
	}
}

// RegisterHandler registers an event handler
func (p *ParticipantEventProcessor) RegisterHandler(eventType EventType, handler EventHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.handlers[eventType]; !exists {
		p.handlers[eventType] = make([]EventHandler, 0)
	}
	p.handlers[eventType] = append(p.handlers[eventType], handler)
}

// AddFilter adds an event filter
func (p *ParticipantEventProcessor) AddFilter(filter EventFilter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.filters = append(p.filters, filter)
}

// AddBatchProcessor adds a batch processor
func (p *ParticipantEventProcessor) AddBatchProcessor(processor BatchEventProcessor) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batchProcessors = append(p.batchProcessors, processor)
}

// ProcessPendingEvents processes any pending events synchronously
func (p *ParticipantEventProcessor) ProcessPendingEvents() {
	// Process up to 100 pending events
	processed := 0
	for processed < 100 {
		select {
		case event := <-p.eventQueue:
			p.processEvent(event)
			processed++
		default:
			return
		}
	}
}

// GetEventHistory returns recent event history
func (p *ParticipantEventProcessor) GetEventHistory(limit int) []ParticipantEvent {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if limit <= 0 || limit > len(p.eventHistory) {
		limit = len(p.eventHistory)
	}

	// Return most recent events
	result := make([]ParticipantEvent, limit)
	copy(result, p.eventHistory[len(p.eventHistory)-limit:])
	return result
}

// GetMetrics returns processing metrics
func (p *ParticipantEventProcessor) GetMetrics() map[string]interface{} {
	p.processingMetrics.mu.RLock()
	defer p.processingMetrics.mu.RUnlock()

	metrics := map[string]interface{}{
		"total_events":     p.processingMetrics.totalEvents,
		"processed_events": p.processingMetrics.processedEvents,
		"failed_events":    p.processingMetrics.failedEvents,
		"filtered_events":  p.processingMetrics.filteredEvents,
		"batched_events":   p.processingMetrics.batchedEvents,
		"queue_size":       len(p.eventQueue),
		"event_counts":     p.processingMetrics.eventCounts,
	}

	// Calculate average processing times
	avgTimes := make(map[EventType]float64)
	for eventType, times := range p.processingMetrics.processingTimes {
		if len(times) > 0 {
			var total time.Duration
			for _, t := range times {
				total += t
			}
			avgTimes[eventType] = float64(total.Milliseconds()) / float64(len(times))
		}
	}
	metrics["avg_processing_times_ms"] = avgTimes

	return metrics
}

// Stop stops the event processor
func (p *ParticipantEventProcessor) Stop() {
	p.cancel()
	close(p.eventQueue)
	p.wg.Wait()
}

// Private methods

// processEvents processes events from the queue
func (p *ParticipantEventProcessor) processEvents() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		case event, ok := <-p.eventQueue:
			if !ok {
				return
			}
			p.processEvent(event)
		}
	}
}

// processEvent processes a single event
func (p *ParticipantEventProcessor) processEvent(event ParticipantEvent) {
	start := time.Now()

	// Add to history
	p.mu.Lock()
	p.eventHistory = append(p.eventHistory, event)
	if len(p.eventHistory) > p.historySize {
		p.eventHistory = p.eventHistory[len(p.eventHistory)-p.historySize:]
	}
	p.mu.Unlock()

	// Get handlers
	p.mu.RLock()
	handlers := p.handlers[event.Type]
	p.mu.RUnlock()

	// Process event
	var err error
	for _, handler := range handlers {
		if handlerErr := handler(event); handlerErr != nil {
			err = handlerErr
			getLogger := logger.GetLogger()
			getLogger.Errorw("event handler failed", handlerErr,
				"eventType", event.Type,
				"eventID", event.ID)
		}
	}

	// Update metrics
	duration := time.Since(start)
	p.processingMetrics.mu.Lock()
	if err != nil {
		p.processingMetrics.failedEvents++
	} else {
		p.processingMetrics.processedEvents++
	}
	p.processingMetrics.lastProcessedAt = time.Now()

	// Track processing time
	if _, exists := p.processingMetrics.processingTimes[event.Type]; !exists {
		p.processingMetrics.processingTimes[event.Type] = make([]time.Duration, 0)
	}
	p.processingMetrics.processingTimes[event.Type] = append(
		p.processingMetrics.processingTimes[event.Type], duration)

	// Keep only last 100 measurements per type
	if len(p.processingMetrics.processingTimes[event.Type]) > 100 {
		p.processingMetrics.processingTimes[event.Type] =
			p.processingMetrics.processingTimes[event.Type][1:]
	}
	p.processingMetrics.mu.Unlock()
}

// processBatches handles batch processing
func (p *ParticipantEventProcessor) processBatches() {
	defer p.wg.Done()

	batchMap := make(map[BatchEventProcessor][]ParticipantEvent)
	timers := make(map[BatchEventProcessor]*time.Timer)

	for {
		select {
		case <-p.ctx.Done():
			// Process remaining batches
			for processor, events := range batchMap {
				if len(events) > 0 {
					_ = processor.ProcessBatch(events)
				}
			}
			return

		case event := <-p.eventQueue:
			// Check which batch processors want this event
			for _, processor := range p.batchProcessors {
				if processor.ShouldBatch(event) {
					// Add to batch
					if _, exists := batchMap[processor]; !exists {
						batchMap[processor] = make([]ParticipantEvent, 0)

						// Start timer
						timer := time.AfterFunc(processor.GetBatchTimeout(), func() {
							p.processBatch(processor, batchMap[processor])
							delete(batchMap, processor)
							delete(timers, processor)
						})
						timers[processor] = timer
					}

					batchMap[processor] = append(batchMap[processor], event)

					// Check if batch is full
					if len(batchMap[processor]) >= processor.GetBatchSize() {
						timers[processor].Stop()
						p.processBatch(processor, batchMap[processor])
						delete(batchMap, processor)
						delete(timers, processor)
					}

					// Update metrics
					p.processingMetrics.mu.Lock()
					p.processingMetrics.batchedEvents++
					p.processingMetrics.mu.Unlock()
				}
			}
		}
	}
}

// processBatch processes a batch of events
func (p *ParticipantEventProcessor) processBatch(processor BatchEventProcessor, events []ParticipantEvent) {
	if err := processor.ProcessBatch(events); err != nil {
		getLogger := logger.GetLogger()
		getLogger.Errorw("batch processing failed", err,
			"batchSize", len(events))
	}
}

// Built-in Event Handlers and Processors

// LoggingEventHandler logs all events
func LoggingEventHandler(event ParticipantEvent) error {
	getLogger := logger.GetLogger()
	getLogger.Infow("participant event",
		"type", event.Type,
		"participant", event.Participant.Identity(),
		"timestamp", event.Timestamp)
	return nil
}

// ThrottleFilter throttles events from specific participants
type ThrottleFilter struct {
	mu           sync.RWMutex
	maxPerMinute int
	counts       map[string][]time.Time
}

// NewThrottleFilter creates a new throttle filter
func NewThrottleFilter(maxPerMinute int) *ThrottleFilter {
	return &ThrottleFilter{
		maxPerMinute: maxPerMinute,
		counts:       make(map[string][]time.Time),
	}
}

// Filter implements EventFilter
func (f *ThrottleFilter) Filter(event ParticipantEvent) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	identity := event.Participant.Identity()
	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)

	// Clean old entries
	if times, exists := f.counts[identity]; exists {
		newTimes := make([]time.Time, 0)
		for _, t := range times {
			if t.After(cutoff) {
				newTimes = append(newTimes, t)
			}
		}
		f.counts[identity] = newTimes
	} else {
		f.counts[identity] = make([]time.Time, 0)
	}

	// Check count
	if len(f.counts[identity]) >= f.maxPerMinute {
		return false
	}

	// Add new entry
	f.counts[identity] = append(f.counts[identity], now)
	return true
}

// MetricsBatchProcessor batches events for metrics processing
type MetricsBatchProcessor struct {
	batchSize    int
	batchTimeout time.Duration
	metricsSink  func(events []ParticipantEvent)
}

// NewMetricsBatchProcessor creates a metrics batch processor
func NewMetricsBatchProcessor(sink func(events []ParticipantEvent)) *MetricsBatchProcessor {
	return &MetricsBatchProcessor{
		batchSize:    50,
		batchTimeout: 5 * time.Second,
		metricsSink:  sink,
	}
}

func (p *MetricsBatchProcessor) ShouldBatch(event ParticipantEvent) bool {
	// Batch all events for metrics
	return true
}

func (p *MetricsBatchProcessor) ProcessBatch(events []ParticipantEvent) error {
	p.metricsSink(events)
	return nil
}

func (p *MetricsBatchProcessor) GetBatchSize() int {
	return p.batchSize
}

func (p *MetricsBatchProcessor) GetBatchTimeout() time.Duration {
	return p.batchTimeout
}

// generateEventID generates a unique event ID
func generateEventID() string {
	return fmt.Sprintf("evt_%d_%s", time.Now().UnixNano(), generateRandomString(8))
}

// generateRandomString generates a random string of given length
func generateRandomString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
	}
	return string(b)
}
