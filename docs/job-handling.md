# Job Handling

This guide covers the complete job lifecycle in the LiveKit Agent SDK, from assignment to completion. You'll learn how to implement robust job handlers, manage job state, and handle various scenarios.

## Job Lifecycle

Understanding the job lifecycle is crucial for building reliable agents:

```
┌──────────┐      ┌──────────┐      ┌─────────┐      ┌───────────┐      ┌───────────┐
│ Created  │ ───► │ Assigned │ ───► │ Running │ ───► │ Completed │      │  Failed   │
└──────────┘      └──────────┘      └─────────┘      └───────────┘      └───────────┘
                        │                  │                                     ▲
                        │                  └─────────────────────────────────────┘
                        │                                                        │
                        └────────────────────────────────────────────────────────┘
```

### Job States

| State | Description | Next States |
|-------|-------------|-------------|
| JS_CREATED | Job created on server | JS_ASSIGNED, JS_FAILED |
| JS_ASSIGNED | Job assigned to worker | JS_RUNNING, JS_FAILED |
| JS_RUNNING | Job being processed | JS_SUCCESS, JS_FAILED |
| JS_SUCCESS | Job completed successfully | None (terminal) |
| JS_FAILED | Job failed | None (terminal) |

## Implementing Job Handlers

### Basic Handler Structure

```go
type JobHandler interface {
    OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
}
```

The handler receives:
- **Context**: For cancellation and deadline management
- **Job**: Contains job details and metadata
- **Room**: Pre-connected room instance

### Function-Based Handler

For simple use cases, use `JobHandlerFunc`:

```go
handler := &agent.JobHandlerFunc{
    OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
        log.Printf("Processing job %s for room %s", job.Id, room.Name())
        
        // Your job logic here
        
        return nil // Success
    },
}
```

### Struct-Based Handler

For complex agents with state:

```go
type MyAgentHandler struct {
    config     *Config
    database   *Database
    metrics    *MetricsCollector
    jobStates  sync.Map
}

func (h *MyAgentHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Initialize job state
    state := &JobState{
        ID:        job.Id,
        StartTime: time.Now(),
        Status:    "initializing",
    }
    h.jobStates.Store(job.Id, state)
    defer h.jobStates.Delete(job.Id)
    
    // Record metrics
    h.metrics.JobStarted(job.Id)
    defer func() {
        h.metrics.JobCompleted(job.Id, time.Since(state.StartTime))
    }()
    
    // Process based on job type
    switch job.Type {
    case livekit.JobType_JT_ROOM:
        return h.handleRoomJob(ctx, job, room, state)
    case livekit.JobType_JT_PARTICIPANT:
        return h.handleParticipantJob(ctx, job, room, state)
    case livekit.JobType_JT_PUBLISHER:
        return h.handlePublisherJob(ctx, job, room, state)
    default:
        return fmt.Errorf("unsupported job type: %v", job.Type)
    }
}
```

## Job Metadata

Jobs can carry metadata for configuration and context:

### Parsing Metadata

```go
type JobMetadata struct {
    Action     string                 `json:"action"`
    Parameters map[string]interface{} `json:"parameters"`
    Priority   string                 `json:"priority"`
    RequestID  string                 `json:"request_id"`
}

func parseJobMetadata(job *livekit.Job) (*JobMetadata, error) {
    if job.Metadata == "" {
        return &JobMetadata{}, nil
    }
    
    var metadata JobMetadata
    if err := json.Unmarshal([]byte(job.Metadata), &metadata); err != nil {
        return nil, fmt.Errorf("invalid job metadata: %w", err)
    }
    
    return &metadata, nil
}

func (h *MyAgentHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    metadata, err := parseJobMetadata(job)
    if err != nil {
        return err
    }
    
    log.Printf("Job action: %s, priority: %s", metadata.Action, metadata.Priority)
    
    switch metadata.Action {
    case "record":
        return h.startRecording(ctx, room, metadata.Parameters)
    case "transcribe":
        return h.startTranscription(ctx, room, metadata.Parameters)
    case "analyze":
        return h.startAnalysis(ctx, room, metadata.Parameters)
    default:
        return fmt.Errorf("unknown action: %s", metadata.Action)
    }
}
```

### Strongly-Typed Metadata

For better type safety:

```go
// Define specific metadata types
type RecordingJobMetadata struct {
    Format      string   `json:"format"`      // mp4, webm
    Quality     string   `json:"quality"`     // high, medium, low
    AudioOnly   bool     `json:"audio_only"`
    Tracks      []string `json:"tracks"`      // specific track IDs
    MaxDuration int      `json:"max_duration_seconds"`
}

type TranscriptionJobMetadata struct {
    Language       string   `json:"language"`
    Model          string   `json:"model"`
    Participants   []string `json:"participants"`
    RealtimeOutput bool     `json:"realtime_output"`
}

// Type-safe parser
func parseTypedMetadata[T any](job *livekit.Job) (*T, error) {
    if job.Metadata == "" {
        return new(T), nil
    }
    
    var metadata T
    if err := json.Unmarshal([]byte(job.Metadata), &metadata); err != nil {
        return nil, fmt.Errorf("failed to parse metadata as %T: %w", metadata, err)
    }
    
    return &metadata, nil
}

// Usage
func (h *MyAgentHandler) handleRecordingJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    metadata, err := parseTypedMetadata[RecordingJobMetadata](job)
    if err != nil {
        return err
    }
    
    recorder := NewRecorder(RecorderConfig{
        Format:    metadata.Format,
        Quality:   metadata.Quality,
        AudioOnly: metadata.AudioOnly,
    })
    
    return recorder.Record(ctx, room, metadata.Tracks)
}
```

## Error Handling

Proper error handling ensures reliable job processing:

### Error Categories

```go
// Retryable errors - temporary failures
type RetryableError struct {
    Err error
    RetryAfter time.Duration
}

func (e *RetryableError) Error() string {
    return fmt.Sprintf("retryable error: %v", e.Err)
}

// Fatal errors - cannot recover
type FatalError struct {
    Err error
    Reason string
}

func (e *FatalError) Error() string {
    return fmt.Sprintf("fatal error (%s): %v", e.Reason, e.Err)
}

// Validation errors - bad input
type ValidationError struct {
    Field string
    Value interface{}
    Message string
}

func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation error for %s: %s", e.Field, e.Message)
}
```

### Error Handler Implementation

```go
func (h *MyAgentHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    err := h.processJob(ctx, job, room)
    if err == nil {
        return nil
    }
    
    // Categorize and handle errors
    switch e := err.(type) {
    case *RetryableError:
        log.Printf("Retryable error for job %s: %v", job.Id, e)
        // Worker will retry based on configuration
        return e
        
    case *FatalError:
        log.Printf("Fatal error for job %s: %v", job.Id, e)
        // Report to monitoring
        h.metrics.RecordFatalError(job.Id, e)
        return e
        
    case *ValidationError:
        log.Printf("Validation error for job %s: %v", job.Id, e)
        // Don't retry validation errors
        return &FatalError{
            Err: e,
            Reason: "invalid_input",
        }
        
    default:
        // Unknown errors - analyze for retry decision
        if isNetworkError(err) {
            return &RetryableError{
                Err: err,
                RetryAfter: 5 * time.Second,
            }
        }
        return &FatalError{
            Err: err,
            Reason: "unknown_error",
        }
    }
}
```

## Context and Cancellation

Proper context handling ensures clean shutdowns:

### Timeout Management

```go
func (h *MyAgentHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Add job-specific timeout
    metadata, _ := parseJobMetadata(job)
    timeout := 5 * time.Minute // default
    
    if metadata.Timeout > 0 {
        timeout = time.Duration(metadata.Timeout) * time.Second
    }
    
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    
    // Process with timeout
    done := make(chan error, 1)
    go func() {
        done <- h.doWork(ctx, job, room)
    }()
    
    select {
    case err := <-done:
        return err
    case <-ctx.Done():
        if ctx.Err() == context.DeadlineExceeded {
            return &FatalError{
                Err: ctx.Err(),
                Reason: "job_timeout",
            }
        }
        return ctx.Err()
    }
}
```

### Graceful Cancellation

```go
func (h *MyAgentHandler) doWork(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Set up cleanup
    cleanupDone := make(chan struct{})
    defer func() {
        close(cleanupDone)
    }()
    
    // Monitor for cancellation
    go func() {
        <-ctx.Done()
        log.Printf("Job %s cancelled, starting cleanup", job.Id)
        
        // Perform graceful cleanup
        h.cleanup(job.Id)
        
        // Wait for cleanup with timeout
        cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        
        select {
        case <-cleanupDone:
            log.Printf("Cleanup completed for job %s", job.Id)
        case <-cleanupCtx.Done():
            log.Printf("Cleanup timeout for job %s", job.Id)
        }
    }()
    
    // Main work
    return h.processJobWork(ctx, job, room)
}
```

## Long-Running Jobs

For jobs that run indefinitely:

### Heartbeat Pattern

```go
type LongRunningHandler struct {
    heartbeatInterval time.Duration
}

func (h *LongRunningHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Start heartbeat
    heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
    defer cancelHeartbeat()
    
    go h.sendHeartbeats(heartbeatCtx, job.Id)
    
    // Main processing loop
    for {
        select {
        case <-ctx.Done():
            return nil // Graceful shutdown
            
        case event := <-room.EventChannel():
            if err := h.processEvent(event); err != nil {
                return err
            }
            
        case <-time.After(30 * time.Second):
            // Periodic tasks
            if err := h.performHealthCheck(room); err != nil {
                return &RetryableError{
                    Err: err,
                    RetryAfter: 10 * time.Second,
                }
            }
        }
    }
}

func (h *LongRunningHandler) sendHeartbeats(ctx context.Context, jobID string) {
    ticker := time.NewTicker(h.heartbeatInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Send heartbeat to indicate job is still active
            log.Printf("Heartbeat for job %s", jobID)
            // Could update job status or send metrics
        }
    }
}
```

### Checkpointing

For resumable jobs:

```go
type CheckpointingHandler struct {
    storage CheckpointStorage
}

func (h *CheckpointingHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Load checkpoint if exists
    checkpoint, err := h.storage.LoadCheckpoint(job.Id)
    if err != nil && !errors.Is(err, ErrCheckpointNotFound) {
        return err
    }
    
    state := &ProcessingState{
        JobID: job.Id,
        ProcessedCount: 0,
        LastProcessedID: "",
    }
    
    if checkpoint != nil {
        state = checkpoint
        log.Printf("Resuming job %s from checkpoint: %+v", job.Id, state)
    }
    
    // Periodic checkpoint saving
    checkpointTicker := time.NewTicker(30 * time.Second)
    defer checkpointTicker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            // Save final checkpoint
            h.storage.SaveCheckpoint(job.Id, state)
            return nil
            
        case <-checkpointTicker.C:
            // Save periodic checkpoint
            if err := h.storage.SaveCheckpoint(job.Id, state); err != nil {
                log.Printf("Failed to save checkpoint: %v", err)
            }
            
        default:
            // Process next item
            processed, err := h.processNext(state)
            if err != nil {
                return err
            }
            if !processed {
                time.Sleep(100 * time.Millisecond)
            }
        }
    }
}
```

## Job Coordination

When multiple agents need to coordinate:

### Leader Election

```go
type CoordinatedHandler struct {
    coordinator JobCoordinator
}

func (h *CoordinatedHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Try to become leader for this room
    isLeader, err := h.coordinator.ElectLeader(ctx, room.Name(), job.Id)
    if err != nil {
        return err
    }
    
    if isLeader {
        log.Printf("Job %s is leader for room %s", job.Id, room.Name())
        return h.runAsLeader(ctx, job, room)
    } else {
        log.Printf("Job %s is follower for room %s", job.Id, room.Name())
        return h.runAsFollower(ctx, job, room)
    }
}

func (h *CoordinatedHandler) runAsLeader(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Leader responsibilities
    // - Coordinate work distribution
    // - Aggregate results
    // - Make decisions
    
    return h.coordinateWork(ctx, job, room)
}

func (h *CoordinatedHandler) runAsFollower(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Follower responsibilities
    // - Receive work assignments
    // - Process assigned tasks
    // - Report results to leader
    
    return h.processAssignments(ctx, job, room)
}
```

### Job Communication

```go
type CommunicatingHandler struct {
    pubsub PubSubService
}

func (h *CommunicatingHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Subscribe to job coordination channel
    channel := fmt.Sprintf("room:%s:jobs", room.Name())
    sub, err := h.pubsub.Subscribe(ctx, channel)
    if err != nil {
        return err
    }
    defer sub.Close()
    
    // Announce presence
    announcement := JobAnnouncement{
        JobID:      job.Id,
        WorkerID:   h.workerID,
        Capability: h.capability,
        Timestamp:  time.Now(),
    }
    
    if err := h.pubsub.Publish(ctx, channel, announcement); err != nil {
        return err
    }
    
    // Process messages from other jobs
    for {
        select {
        case <-ctx.Done():
            return nil
            
        case msg := <-sub.Messages():
            if err := h.handleJobMessage(msg); err != nil {
                log.Printf("Error handling job message: %v", err)
            }
        }
    }
}
```

## Advanced Patterns

### Pipeline Processing

```go
type PipelineHandler struct {
    stages []ProcessingStage
}

type ProcessingStage interface {
    Process(ctx context.Context, input interface{}) (output interface{}, err error)
    Name() string
}

func (h *PipelineHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Initialize pipeline
    pipeline := make(chan interface{}, 100)
    errors := make(chan error, len(h.stages))
    
    // Start stage processors
    var wg sync.WaitGroup
    for i, stage := range h.stages {
        wg.Add(1)
        go func(stageIndex int, stage ProcessingStage) {
            defer wg.Done()
            
            if err := h.runStage(ctx, stage, stageIndex, pipeline); err != nil {
                errors <- fmt.Errorf("stage %s failed: %w", stage.Name(), err)
            }
        }(i, stage)
    }
    
    // Feed initial data
    pipeline <- &InitialData{Job: job, Room: room}
    
    // Wait for completion
    done := make(chan struct{})
    go func() {
        wg.Wait()
        close(done)
    }()
    
    select {
    case <-done:
        // All stages completed
        return nil
    case err := <-errors:
        // A stage failed
        return err
    case <-ctx.Done():
        // Context cancelled
        return ctx.Err()
    }
}
```

### State Machine

```go
type StateMachineHandler struct {
    currentState JobState
    transitions  map[JobState]map[Event]Transition
}

type JobState string
type Event string

type Transition struct {
    NextState JobState
    Action    func(ctx context.Context) error
}

func (h *StateMachineHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    h.currentState = JobStateInitial
    
    for {
        select {
        case <-ctx.Done():
            return nil
            
        case event := <-h.getNextEvent(ctx, room):
            transition, ok := h.transitions[h.currentState][event]
            if !ok {
                log.Printf("No transition for state %s and event %s", h.currentState, event)
                continue
            }
            
            // Execute transition action
            if transition.Action != nil {
                if err := transition.Action(ctx); err != nil {
                    return err
                }
            }
            
            // Update state
            log.Printf("State transition: %s -> %s (event: %s)", 
                h.currentState, transition.NextState, event)
            h.currentState = transition.NextState
            
            // Check for terminal state
            if h.isTerminalState(h.currentState) {
                return nil
            }
        }
    }
}
```

## Performance Considerations

### Resource Pooling

```go
type PooledResourceHandler struct {
    resourcePool *ResourcePool
}

func (h *PooledResourceHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Acquire resources from pool
    resources, err := h.resourcePool.Acquire(ctx)
    if err != nil {
        return &RetryableError{
            Err: err,
            RetryAfter: 5 * time.Second,
        }
    }
    defer h.resourcePool.Release(resources)
    
    // Use pooled resources
    return h.processWithResources(ctx, job, room, resources)
}
```

### Batch Processing

```go
type BatchingHandler struct {
    batchSize     int
    batchTimeout  time.Duration
    processBatch  func([]interface{}) error
}

func (h *BatchingHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    batch := make([]interface{}, 0, h.batchSize)
    timer := time.NewTimer(h.batchTimeout)
    defer timer.Stop()
    
    for {
        select {
        case <-ctx.Done():
            // Process remaining batch
            if len(batch) > 0 {
                return h.processBatch(batch)
            }
            return nil
            
        case item := <-h.getNextItem(room):
            batch = append(batch, item)
            
            if len(batch) >= h.batchSize {
                if err := h.processBatch(batch); err != nil {
                    return err
                }
                batch = batch[:0]
                timer.Reset(h.batchTimeout)
            }
            
        case <-timer.C:
            if len(batch) > 0 {
                if err := h.processBatch(batch); err != nil {
                    return err
                }
                batch = batch[:0]
            }
            timer.Reset(h.batchTimeout)
        }
    }
}
```

## Testing Job Handlers

### Unit Testing

```go
func TestJobHandler(t *testing.T) {
    handler := &MyAgentHandler{
        config: testConfig,
    }
    
    // Create test job
    job := &livekit.Job{
        Id:   "test-job-1",
        Type: livekit.JobType_JT_ROOM,
        Room: &livekit.Room{
            Name: "test-room",
        },
        Metadata: `{"action":"test"}`,
    }
    
    // Create mock room
    room := &MockRoom{
        name: "test-room",
        participants: []*MockParticipant{
            {identity: "user1"},
            {identity: "user2"},
        },
    }
    
    // Test with timeout
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    err := handler.OnJob(ctx, job, room)
    assert.NoError(t, err)
    
    // Verify expected behavior
    assert.Equal(t, 2, handler.processedCount)
}
```

### Integration Testing

```go
func TestJobHandlerIntegration(t *testing.T) {
    // Start test LiveKit server
    server := startTestServer(t)
    defer server.Stop()
    
    // Create worker with handler
    handler := &MyAgentHandler{}
    worker := agent.NewWorker(
        server.URL,
        "test-key",
        "test-secret",
        handler,
        agent.WorkerOptions{
            AgentName: "test-agent",
        },
    )
    
    // Start worker
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    go worker.Start(ctx)
    
    // Dispatch test job
    job := server.DispatchJob(&livekit.Job{
        Type: livekit.JobType_JT_ROOM,
        Room: &livekit.Room{Name: "test-room"},
    })
    
    // Wait for job completion
    assert.Eventually(t, func() bool {
        return job.Status == livekit.JobStatus_JS_SUCCESS
    }, 10*time.Second, 100*time.Millisecond)
}
```

## Best Practices

1. **Always handle context cancellation** - Check ctx.Done() regularly
2. **Use structured logging** - Include job ID in all log messages
3. **Implement proper cleanup** - Use defer for resource cleanup
4. **Handle errors appropriately** - Distinguish between retryable and fatal errors
5. **Monitor job health** - Use metrics and health checks
6. **Test thoroughly** - Unit test handlers and integration test with LiveKit
7. **Document metadata format** - Clearly specify expected job metadata structure
8. **Set reasonable timeouts** - Prevent jobs from running indefinitely
9. **Use checkpointing for long jobs** - Enable job recovery after failures
10. **Coordinate when necessary** - Use leader election for room-wide operations

## Next Steps

- Explore [Advanced Features](advanced-features.md) for complex scenarios
- Learn about [Media Processing](media-processing.md) in agents
- See complete [Examples](examples/README.md) of job handlers
- Review the [API Reference](api-reference.md) for detailed documentation