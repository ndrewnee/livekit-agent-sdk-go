package agent

import (
	"runtime"
	"sync"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// ParticipantTracker manages participant state for a room
type ParticipantTracker struct {
	mu           sync.RWMutex
	room         *lksdk.Room
	participants map[string]*ParticipantInfo
}

// NewParticipantTracker creates a new participant tracker
func NewParticipantTracker(room *lksdk.Room) *ParticipantTracker {
	return &ParticipantTracker{
		room:         room,
		participants: make(map[string]*ParticipantInfo),
	}
}

// GetParticipant returns a participant by identity
func (pt *ParticipantTracker) GetParticipant(identity string) (*lksdk.RemoteParticipant, error) {
	for _, p := range pt.room.GetRemoteParticipants() {
		if p.Identity() == identity {
			return p, nil
		}
	}
	return nil, ErrParticipantNotFound
}

// GetAllParticipants returns all participants in the room
func (pt *ParticipantTracker) GetAllParticipants() []*lksdk.RemoteParticipant {
	return pt.room.GetRemoteParticipants()
}

// UpdateParticipantInfo updates tracked info for a participant
func (pt *ParticipantTracker) UpdateParticipantInfo(identity string, info *ParticipantInfo) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.participants[identity] = info
}

// GetParticipantInfo returns tracked info for a participant
func (pt *ParticipantTracker) GetParticipantInfo(identity string) (*ParticipantInfo, bool) {
	pt.mu.RLock()
	defer pt.mu.RUnlock()
	info, exists := pt.participants[identity]
	return info, exists
}

// RemoveParticipant removes a participant from tracking
func (pt *ParticipantTracker) RemoveParticipant(identity string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	delete(pt.participants, identity)
}

// UniversalEventProcessor handles event processing for the universal worker
type UniversalEventProcessor struct {
	mu         sync.RWMutex
	handlers   map[UniversalEventType][]UniversalEventHandler
	eventQueue chan UniversalEvent
	stopCh     chan struct{}
}

// UniversalEvent represents a generic event in the universal worker system
type UniversalEvent struct {
	Type      UniversalEventType
	Timestamp time.Time
	Data      interface{}
}

// UniversalEventType represents different types of events for universal worker
type UniversalEventType string

const (
	UniversalEventTypeParticipantJoined        UniversalEventType = "participant_joined"
	UniversalEventTypeParticipantLeft          UniversalEventType = "participant_left"
	UniversalEventTypeTrackPublished           UniversalEventType = "track_published"
	UniversalEventTypeTrackUnpublished         UniversalEventType = "track_unpublished"
	UniversalEventTypeMetadataChanged          UniversalEventType = "metadata_changed"
	UniversalEventTypeConnectionQualityChanged UniversalEventType = "connection_quality_changed"
)

// UniversalEventHandler is a function that handles universal events
type UniversalEventHandler func(event UniversalEvent) error

// NewUniversalEventProcessor creates a new event processor
func NewUniversalEventProcessor() *UniversalEventProcessor {
	ep := &UniversalEventProcessor{
		handlers:   make(map[UniversalEventType][]UniversalEventHandler),
		eventQueue: make(chan UniversalEvent, 1000),
		stopCh:     make(chan struct{}),
	}
	go ep.processEvents()
	return ep
}

// RegisterHandler registers a handler for an event type
func (ep *UniversalEventProcessor) RegisterHandler(eventType UniversalEventType, handler UniversalEventHandler) {
	ep.mu.Lock()
	defer ep.mu.Unlock()
	ep.handlers[eventType] = append(ep.handlers[eventType], handler)
}

// QueueEvent queues an event for processing
func (ep *UniversalEventProcessor) QueueEvent(event UniversalEvent) {
	select {
	case ep.eventQueue <- event:
	default:
		// Queue full, drop event
		// In production, you might want to log this or handle it differently
	}
}

// Stop stops the event processor
func (ep *UniversalEventProcessor) Stop() {
	close(ep.stopCh)
}

// processEvents processes events from the queue
func (ep *UniversalEventProcessor) processEvents() {
	for {
		select {
		case <-ep.stopCh:
			return
		case event := <-ep.eventQueue:
			ep.handleEvent(event)
		}
	}
}

// handleEvent handles a single event
func (ep *UniversalEventProcessor) handleEvent(event UniversalEvent) {
	ep.mu.RLock()
	handlers := ep.handlers[event.Type]
	ep.mu.RUnlock()

	for _, handler := range handlers {
		if err := handler(event); err != nil {
			// Log error but continue processing
			// In production, you would log this error
			_ = err
		}
	}
}

// Additional helper methods for UniversalWorker

// updateLoad updates the worker's load based on active jobs
func (w *UniversalWorker) updateLoad() {
	w.mu.RLock()
	activeJobCount := len(w.activeJobs)
	maxJobs := w.opts.MaxJobs
	w.mu.RUnlock()

	var load float32
	if maxJobs > 0 {
		load = float32(activeJobCount) / float32(maxJobs)
	} else {
		// Use load calculator if no max jobs set
		metrics := w.buildLoadMetrics()
		load = w.loadCalculator.Calculate(metrics)
	}

	// Update status based on load
	status := WorkerStatusAvailable
	if (maxJobs > 0 && activeJobCount >= maxJobs) || load >= 1.0 {
		status = WorkerStatusFull
	}

	// Use batcher if available, otherwise update directly
	if w.loadBatcher != nil {
		w.loadBatcher.Update(status, load)
	} else {
		if err := w.UpdateStatus(status, load); err != nil {
			w.logger.Error("Failed to update worker status", "error", err)
		}
	}
}

// buildLoadMetrics builds load metrics for the load calculator
func (w *UniversalWorker) buildLoadMetrics() LoadMetrics {
	w.mu.RLock()
	jobCount := len(w.activeJobs)
	maxJobs := w.opts.MaxJobs

	// Calculate job durations
	durations := make(map[string]time.Duration)
	now := time.Now()
	for jobID, startTime := range w.jobStartTimes {
		durations[jobID] = now.Sub(startTime)
	}
	w.mu.RUnlock()

	// Get system metrics if collector is available
	var cpuPercent, memoryPercent float64
	var memoryMB uint64
	if w.metricsCollector != nil {
		// Collect metrics first
		w.metricsCollector.collect()
		// Then get the values
		cpu, mem, memUsed, _ := w.metricsCollector.GetMetrics()
		cpuPercent = cpu
		memoryPercent = mem
		memoryMB = memUsed
		_ = runtime.NumGoroutine() // Can be used for debugging
	}

	return LoadMetrics{
		ActiveJobs:    jobCount,
		MaxJobs:       maxJobs,
		JobDuration:   durations,
		CPUPercent:    cpuPercent,
		MemoryPercent: memoryPercent,
		MemoryUsedMB:  memoryMB,
	}
}

// updateJobStatus updates a job's status
func (w *UniversalWorker) updateJobStatus(jobID string, status livekit.JobStatus, error string) {
	// Check if we should queue this update during reconnection
	if w.raceProtector != nil && w.raceProtector.QueueStatusUpdate(jobID, status, error) {
		return
	}

	msg := &livekit.UpdateJobStatus{
		JobId:  jobID,
		Status: status,
		Error:  error,
	}

	if err := w.sendMessage(&livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateJob{
			UpdateJob: msg,
		},
	}); err != nil {
		w.logger.Error("Failed to update job status", "error", err, "jobID", jobID, "status", status)
		// Queue for retry
		w.queueStatusUpdate(statusUpdate{
			jobID:      jobID,
			status:     status,
			error:      error,
			timestamp:  time.Now(),
			retryCount: 0,
		})
	}
}

// queueStatusUpdate queues a status update for retry
func (w *UniversalWorker) queueStatusUpdate(update statusUpdate) {
	w.mu.Lock()
	w.statusQueue = append(w.statusQueue, update)
	w.mu.Unlock()

	// Signal that there's a new update to process
	select {
	case w.statusQueueChan <- struct{}{}:
	default:
		// Channel full, already signaled
	}
}

// WebSocketState represents the current state of the WebSocket connection
type WebSocketState int

const (
	WebSocketStateDisconnected WebSocketState = iota
	WebSocketStateConnecting
	WebSocketStateConnected
	WebSocketStateReconnecting
)

// statusUpdate represents a queued job status update
type statusUpdate struct {
	jobID      string
	status     livekit.JobStatus
	error      string
	timestamp  time.Time
	retryCount int
}

// JobTimingManager manages job execution timing and deadlines
type JobTimingManager struct {
	mu        sync.RWMutex
	deadlines map[string]time.Time
}

// NewJobTimingManager creates a new job timing manager
func NewJobTimingManager() *JobTimingManager {
	return &JobTimingManager{
		deadlines: make(map[string]time.Time),
	}
}

// StatusUpdateRaceProtector prevents race conditions during status updates
type StatusUpdateRaceProtector struct {
	mu             sync.Mutex
	queuedUpdates  []statusUpdate
	isReconnecting bool
}

// NewStatusUpdateRaceProtector creates a new race protector
func NewStatusUpdateRaceProtector() *StatusUpdateRaceProtector {
	return &StatusUpdateRaceProtector{
		queuedUpdates: make([]statusUpdate, 0),
	}
}

// QueueStatusUpdate queues a status update if reconnecting
func (p *StatusUpdateRaceProtector) QueueStatusUpdate(jobID string, status livekit.JobStatus, error string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isReconnecting {
		p.queuedUpdates = append(p.queuedUpdates, statusUpdate{
			jobID:  jobID,
			status: status,
			error:  error,
		})
		return true
	}
	return false
}

// ProcessQueuedUpdates processes all queued updates
func (p *StatusUpdateRaceProtector) ProcessQueuedUpdates(handler func(jobID string, status livekit.JobStatus, error string)) {
	p.mu.Lock()
	updates := p.queuedUpdates
	p.queuedUpdates = make([]statusUpdate, 0)
	p.isReconnecting = false
	p.mu.Unlock()

	for _, update := range updates {
		handler(update.jobID, update.status, update.error)
	}
}

// Additional helper methods for UniversalWorker

// GetCurrentLoad returns the current load of the worker
func (w *UniversalWorker) GetCurrentLoad() float32 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	activeJobs := len(w.activeJobs)
	maxJobs := w.opts.MaxJobs
	if maxJobs <= 0 {
		maxJobs = 1
	}

	return float32(activeJobs) / float32(maxJobs)
}

// SetDebugMode enables or disables debug mode
func (w *UniversalWorker) SetDebugMode(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.debugMode = enabled
}

// GetWorkerType returns the type of jobs this worker handles
func (w *UniversalWorker) GetWorkerType() livekit.JobType {
	return w.opts.JobType
}

// GetOptions returns the worker options
func (w *UniversalWorker) GetOptions() WorkerOptions {
	return w.opts
}

// GetTrackPublication gets a track publication by track ID
func (w *UniversalWorker) GetTrackPublication(trackID string) (*lksdk.RemoteTrackPublication, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if sub, exists := w.subscribedTracks[trackID]; exists {
		return sub.Publication, nil
	}

	return nil, ErrTrackNotFound
}

// UnsubscribeTrack unsubscribes from a track by track ID
func (w *UniversalWorker) UnsubscribeTrack(trackID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if sub, exists := w.subscribedTracks[trackID]; exists {
		if sub.Publication != nil {
			if err := sub.Publication.SetSubscribed(false); err != nil {
				return err
			}
		}
		delete(w.subscribedTracks, trackID)
		return nil
	}

	return ErrTrackNotFound
}

// GetTrackStats gets statistics for a track
func (w *UniversalWorker) GetTrackStats(trackID string) (map[string]interface{}, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if sub, exists := w.subscribedTracks[trackID]; exists {
		stats := map[string]interface{}{
			"subscribed_at":   sub.SubscribedAt,
			"current_quality": sub.CurrentQuality,
			"enabled":         sub.Enabled,
		}
		if sub.Dimensions != nil {
			stats["width"] = sub.Dimensions.Width
			stats["height"] = sub.Dimensions.Height
		}
		stats["frame_rate"] = sub.FrameRate
		return stats, nil
	}

	return nil, ErrTrackNotFound
}

// Event handling methods
func (w *UniversalWorker) handleParticipantJoined(participant *livekit.ParticipantInfo) {
	w.mu.Lock()
	w.participants[participant.Identity] = &ParticipantInfo{
		Identity:     participant.Identity,
		Name:         participant.Name,
		Metadata:     participant.Metadata,
		JoinedAt:     time.Now(),
		LastActivity: time.Now(),
		Permissions:  participant.Permission,
		Attributes:   participant.Attributes,
	}
	w.mu.Unlock()
}

func (w *UniversalWorker) handleParticipantLeft(participant *livekit.ParticipantInfo) {
	w.mu.Lock()
	delete(w.participants, participant.Identity)
	w.mu.Unlock()
}

func (w *UniversalWorker) handleTrackPublished(track *livekit.TrackInfo, participant *livekit.ParticipantInfo) {
	// Track publication event
	w.eventProcessor.QueueEvent(UniversalEvent{
		Type:      UniversalEventTypeTrackPublished,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"track":       track,
			"participant": participant,
		},
	})
}

func (w *UniversalWorker) handleTrackUnpublished(track *livekit.TrackInfo, participant *livekit.ParticipantInfo) {
	// Track unpublished event
	w.eventProcessor.QueueEvent(UniversalEvent{
		Type:      UniversalEventTypeTrackUnpublished,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"track":       track,
			"participant": participant,
		},
	})
}

func (w *UniversalWorker) handleMetadataChanged(oldMetadata, newMetadata string) {
	w.eventProcessor.QueueEvent(UniversalEvent{
		Type:      UniversalEventTypeMetadataChanged,
		Timestamp: time.Now(),
		Data: map[string]string{
			"old": oldMetadata,
			"new": newMetadata,
		},
	})
}

func (w *UniversalWorker) handleConnectionQualityChanged(participant *livekit.ParticipantInfo, quality livekit.ConnectionQuality) {
	w.mu.Lock()
	if info, exists := w.participants[participant.Identity]; exists {
		info.ConnectionQuality = quality
		info.LastActivity = time.Now()
	}
	w.mu.Unlock()

	w.eventProcessor.QueueEvent(UniversalEvent{
		Type:      UniversalEventTypeConnectionQualityChanged,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"participant": participant,
			"quality":     quality,
		},
	})
}

func (w *UniversalWorker) handleActiveSpeakersChanged(speakers []*livekit.ParticipantInfo) {
	w.mu.Lock()
	// Update speaking status
	for _, p := range speakers {
		if info, exists := w.participants[p.Identity]; exists {
			info.IsSpeaking = true
			info.LastActivity = time.Now()
		}
	}
	w.mu.Unlock()
}

func (w *UniversalWorker) handleDataReceived(data []byte, participant *livekit.ParticipantInfo) {
	w.mu.Lock()
	if info, exists := w.participants[participant.Identity]; exists {
		info.LastActivity = time.Now()
	}
	w.mu.Unlock()
}
