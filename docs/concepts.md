# Core Concepts and Architecture

This guide explains the fundamental concepts and architecture of the LiveKit Agent SDK. Understanding these concepts will help you build robust and scalable agent applications.

## Architecture Overview

The LiveKit Agent SDK follows a distributed architecture designed for scalability and reliability:

```
┌─────────────────┐     WebSocket      ┌─────────────────┐
│                 │ ◄────────────────► │                 │
│  LiveKit Server │                    │  Agent Worker   │
│                 │ ────────────────►  │                 │
└─────────────────┘    Job Assignment  └─────────────────┘
         │                                      │
         │                                      │
         ▼                                      ▼
┌─────────────────┐                    ┌─────────────────┐
│   Rooms/Media   │                    │  Job Handlers   │
└─────────────────┘                    └─────────────────┘
```

## Key Components

### 1. Worker

The Worker is the fundamental unit of the Agent SDK. It represents a long-running process that:

- Maintains a persistent WebSocket connection to the LiveKit server
- Registers its capabilities and availability
- Receives and processes job assignments
- Reports status and load metrics
- Handles graceful shutdown and recovery

```go
// Worker lifecycle
worker := agent.NewWorker(...)
worker.Start(ctx)  // Connects and begins accepting jobs
worker.Stop(ctx)   // Gracefully shuts down
```

### 2. Jobs

Jobs are tasks assigned by the LiveKit server to workers. Each job represents a specific action to perform:

```go
type Job struct {
    ID       string
    Type     JobType      // JT_ROOM, JT_PARTICIPANT, or JT_PUBLISHER
    Room     *RoomInfo    // Target room information
    Metadata string       // Application-specific data
}
```

#### Job Lifecycle

```
Created ──► Assigned ──► Running ──► Completed
                │            │
                └──► Failed  └──► Failed
```

### 3. Job Handlers

Job handlers contain your agent's business logic. They process assigned jobs and interact with LiveKit rooms:

```go
type JobHandler interface {
    OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
}
```

The handler receives:
- **Context**: For cancellation and deadline management
- **Job**: The job assignment with metadata
- **Room**: Connected LiveKit room instance

## Connection Management

### WebSocket Connection

The worker maintains a persistent WebSocket connection for:
- Job assignments
- Status updates
- Load reporting
- Keepalive messages

### Automatic Reconnection

The SDK handles connection failures automatically:

1. **Connection Lost**: Worker detects disconnection
2. **Backoff Retry**: Exponential backoff with jitter
3. **State Preservation**: Active jobs are tracked
4. **Recovery**: Jobs can be resumed after reconnection

```go
// Configure reconnection behavior
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    ReconnectInterval: 5 * time.Second,
    MaxReconnectAttempts: 10,
})
```

## Load Management

### Load Calculation

Workers report their load to enable intelligent job distribution:

```go
// Default: Based on job count
load = activeJobs / maxJobs

// Custom: Include system resources
type CustomLoadCalculator struct{}
func (c *CustomLoadCalculator) Calculate(metrics LoadMetrics) float32 {
    return (cpuUsage * 0.4) + (memoryUsage * 0.3) + (jobRatio * 0.3)
}
```

### Load-Based Assignment

The server uses worker load to:
- Distribute jobs evenly
- Prevent overloading
- Optimize resource usage
- Enable auto-scaling

## Job Types Deep Dive

### Room Jobs (JT_ROOM)

Room jobs operate at the room level, processing events for all participants:

```go
handler := &agent.JobHandlerFunc{
    OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
        // Access all participants
        for _, participant := range room.GetRemoteParticipants() {
            // Process participant
        }
        
        // Handle room events
        room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
            // New participant joined
        }
        
        return nil
    },
}
```

**Use Cases:**
- Recording/transcription services
- Moderation bots
- Analytics collectors
- Room monitors

### Participant Jobs (JT_PARTICIPANT)

Participant jobs focus on individual participant interactions:

```go
type ParticipantAgentHandler interface {
    OnParticipantConnected(participant *lksdk.RemoteParticipant)
    OnParticipantDisconnected(participant *lksdk.RemoteParticipant)
    OnTrackPublished(participant *lksdk.RemoteParticipant, track *lksdk.RemoteTrackPublication)
    // ... more participant-specific events
}
```

**Use Cases:**
- Personal assistants
- Language translators
- Individual processors
- Participant-specific features

### Publisher Jobs (JT_PUBLISHER)

Publisher jobs enable agents to publish media streams:

```go
type PublisherAgentHandler interface {
    OnConnected(localParticipant *lksdk.LocalParticipant)
    PrepareAudioTrack(ctx context.Context) (*webrtc.TrackLocalStaticSample, error)
    PrepareVideoTrack(ctx context.Context) (*webrtc.TrackLocalStaticSample, error)
}
```

**Use Cases:**
- AI avatars
- Text-to-speech bots
- Media injection
- Synthetic participants

## State Management

### Worker State

Workers maintain state across multiple levels:

```go
type WorkerStatus string

const (
    WorkerStatus_WS_INITIALIZING  // Starting up
    WorkerStatus_WS_IDLE          // Connected, no jobs
    WorkerStatus_WS_BUSY          // Processing jobs
    WorkerStatus_WS_FULL          // At capacity
    WorkerStatus_WS_DISCONNECTED  // Connection lost
)
```

### Job State

Each job has its own state tracking:

```go
type JobStatus string

const (
    JobStatus_JS_CREATED   // Job created
    JobStatus_JS_ASSIGNED  // Assigned to worker
    JobStatus_JS_RUNNING   // Being processed
    JobStatus_JS_SUCCESS   // Completed successfully
    JobStatus_JS_FAILED    // Failed with error
)
```

### Persistence and Recovery

For stateful agents, implement checkpointing:

```go
checkpoint := agent.NewJobCheckpoint(job.Id)
checkpoint.Save("processed_count", 1000)
checkpoint.Save("last_timestamp", time.Now())

// After recovery
if count, ok := checkpoint.Load("processed_count"); ok {
    processedCount = count.(int)
    // Resume from checkpoint
}
```

## Concurrency Model

### Job Processing

Each job runs in its own goroutine, allowing concurrent processing:

```go
// Worker handles multiple jobs concurrently
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    MaxConcurrentJobs: 10,  // Process up to 10 jobs simultaneously
})
```

### Thread Safety

The SDK ensures thread safety for:
- Worker state management
- Job queue operations
- Connection handling
- Metric collection

Your handlers should also be thread-safe when accessing shared state.

## Resource Management

### Memory Management

Control memory usage with buffering strategies:

```go
// Configure media buffers
bufferFactory := agent.NewMediaBufferFactory(
    100,   // Initial size
    1000,  // Max size
)

// Implement resource limits
limiter := agent.NewResourceLimiter(agent.ResourceLimits{
    MaxMemoryMB: 1024,
    MaxCPUPercent: 80,
})
```

### Connection Pooling

For multi-room agents, manage connections efficiently:

```go
type RoomPool struct {
    maxRooms int
    rooms    map[string]*lksdk.Room
    mu       sync.RWMutex
}
```

## Error Handling and Resilience

### Error Types

Understand different error categories:

```go
// Retryable errors - temporary failures
type RetryableError struct {
    Err error
}

// Fatal errors - cannot recover
type FatalError struct {
    Err error
}

// Handle appropriately
if err != nil {
    switch e := err.(type) {
    case *RetryableError:
        return fmt.Errorf("retryable: %w", e.Err)
    case *FatalError:
        // Log and fail the job
        return e.Err
    default:
        // Analyze error for retry decision
    }
}
```

### Circuit Breaker Pattern

Protect against cascading failures:

```go
breaker := agent.NewCircuitBreaker(agent.CircuitBreakerConfig{
    FailureThreshold: 5,
    ResetTimeout: 30 * time.Second,
})

err := breaker.Execute(func() error {
    return riskyOperation()
})
```

## Observability

### Metrics Collection

Track key performance indicators:

```go
metrics := agent.NewMetricsCollector()
metrics.RecordJobDuration(jobID, duration)
metrics.RecordJobStatus(jobID, status)
metrics.RecordWorkerLoad(load)
```

### Distributed Tracing

Integrate with tracing systems:

```go
import "go.opentelemetry.io/otel"

tracer := otel.Tracer("agent")
ctx, span := tracer.Start(ctx, "process_job")
defer span.End()

span.SetAttributes(
    attribute.String("job.id", job.Id),
    attribute.String("room.name", room.Name()),
)
```

### Logging Best Practices

Use structured logging with context:

```go
logger := logger.GetLogger().WithValues(
    "worker", worker.ID,
    "job", job.Id,
    "room", room.Name(),
)

logger.Infow("Processing job",
    "type", job.Type,
    "metadata", job.Metadata,
)
```

## Deployment Patterns

### Single Worker

Simple deployment for development or low load:

```
┌─────────────┐
│   Worker    │
│  (1 instance)│
└─────────────┘
```

### Worker Pool

Scale horizontally for high load:

```
┌─────────────┐ ┌─────────────┐ ┌─────────────┐
│  Worker 1   │ │  Worker 2   │ │  Worker 3   │
└─────────────┘ └─────────────┘ └─────────────┘
         │              │              │
         └──────────────┴──────────────┘
                        │
                 Load Balancer
```

### Specialized Workers

Different workers for different job types:

```go
// Audio processing worker
audioWorker := agent.NewWorker(url, key, secret, audioHandler, agent.WorkerOptions{
    AgentName: "audio-processor",
    JobTypes: []livekit.JobType{livekit.JobType_JT_ROOM},
})

// Video publishing worker
videoWorker := agent.NewWorker(url, key, secret, videoHandler, agent.WorkerOptions{
    AgentName: "video-publisher",
    JobTypes: []livekit.JobType{livekit.JobType_JT_PUBLISHER},
})
```

## Security Considerations

### Authentication

Workers authenticate using API key/secret:
- Keys are used to generate JWT tokens
- Tokens have expiration times
- Automatic token refresh on reconnection

### Authorization

Control agent permissions:
```go
// Agent permissions in token
grant := &auth.VideoGrant{
    Room:       roomName,
    RoomJoin:   true,
    CanPublish: true,
    CanSubscribe: true,
}
```

### Data Privacy

- Implement encryption for sensitive data
- Use secure WebSocket connections (wss://)
- Avoid logging sensitive information
- Implement data retention policies

## Performance Optimization

### Efficient Event Processing

Batch operations when possible:

```go
events := make([]Event, 0, 100)
ticker := time.NewTicker(100 * time.Millisecond)

for {
    select {
    case event := <-eventChan:
        events = append(events, event)
        if len(events) >= 100 {
            processBatch(events)
            events = events[:0]
        }
    case <-ticker.C:
        if len(events) > 0 {
            processBatch(events)
            events = events[:0]
        }
    }
}
```

### Memory Optimization

- Use object pools for frequent allocations
- Clear references to allow garbage collection
- Monitor memory usage and set limits
- Use streaming for large data processing

## Next Steps

Now that you understand the core concepts:

1. Explore [Agent Types](agent-types.md) in detail
2. Learn about [Job Handling](job-handling.md) patterns
3. Discover [Advanced Features](advanced-features.md)
4. See practical [Examples](examples/README.md)

## Summary

The LiveKit Agent SDK provides:
- **Scalable Architecture**: Distributed workers with load balancing
- **Flexible Job System**: Multiple job types for different use cases
- **Resilient Design**: Automatic recovery and error handling
- **Observable Operations**: Built-in metrics and logging
- **Production Ready**: Security, performance, and deployment patterns

Understanding these concepts enables you to build sophisticated real-time communication agents that scale with your needs.