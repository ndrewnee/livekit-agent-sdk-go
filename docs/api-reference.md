# LiveKit Agent SDK API Reference

Complete reference documentation for all public APIs in the LiveKit Agent SDK.

## Table of Contents

- [Core Types](#core-types)
- [Worker API](#worker-api)
- [Job Handling](#job-handling)
- [Agent Types](#agent-types)
- [Media Processing](#media-processing)
- [Load Balancing](#load-balancing)
- [Recovery & Resilience](#recovery--resilience)
- [Monitoring & Metrics](#monitoring--metrics)
- [Participant Coordination](#participant-coordination)
- [Resource Management](#resource-management)
- [Configuration](#configuration)

## Core Types

### UniversalHandler Interface

```go
type UniversalHandler interface {
    // Called when a job is available - decide whether to accept it
    OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata)
    
    // Called when the job is assigned - perform the actual work
    OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error
    
    // Called when the job is terminated - clean up resources
    OnJobTerminated(ctx context.Context, jobID string)
    
    // Optional: participant event handlers
    OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant)
    OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant)
    OnTrackPublished(ctx context.Context, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant)
    OnTrackUnpublished(ctx context.Context, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant)
}
```

The primary interface that must be implemented by all agent handlers.

**Methods:**

- `OnJobRequest(ctx, job)`: Called when a job is available
  - `ctx`: Context for the request
  - `job`: Job information including type, metadata, and ID
  - Returns: (accept bool, metadata *JobMetadata)

- `OnJobAssigned(ctx, jobCtx)`: Called when the job is assigned
  - `ctx`: Context for job execution (cancelled when job should stop)
  - `jobCtx`: Job context containing job, room, and metadata
  - Returns: Error if job execution fails

- `OnJobTerminated(ctx, jobID)`: Called when job ends
  - `ctx`: Context for cleanup
  - `jobID`: ID of the terminated job

- Participant event handlers: Optional methods for tracking room events

### JobType Enumeration

```go
const (
    JobType_JT_ROOM        = livekit.JobType_JT_ROOM
    JobType_JT_PARTICIPANT = livekit.JobType_JT_PARTICIPANT
    JobType_JT_PUBLISHER   = livekit.JobType_JT_PUBLISHER
)
```

**Job Types:**

- `JT_ROOM`: Room-level agents that monitor entire rooms
- `JT_PARTICIPANT`: Participant-specific agents assigned to individual users
- `JT_PUBLISHER`: Publishing agents that generate media content

## Worker API

### UniversalWorker Struct

```go
type UniversalWorker struct {
    // Internal fields (not exported)
}
```

Main worker instance that connects to LiveKit and processes jobs. Replaces the deprecated Worker, ParticipantAgent, and PublisherAgent.

### NewUniversalWorker Function

```go
func NewUniversalWorker(
    url string,
    apiKey string,
    apiSecret string,
    handler UniversalHandler,
    opts WorkerOptions,
) *UniversalWorker
```

Creates a new universal worker instance.

**Parameters:**

- `url`: LiveKit server WebSocket URL (e.g., "ws://localhost:7880")
- `apiKey`: LiveKit API key for authentication
- `apiSecret`: LiveKit API secret for authentication  
- `handler`: UniversalHandler implementation
- `opts`: Worker configuration options

**Returns:** UniversalWorker instance ready to start

### UniversalWorker Methods

#### Start

```go
func (w *UniversalWorker) Start(ctx context.Context) error
```

Starts the worker and begins processing jobs. Blocks until context is cancelled or fatal error occurs.

#### Stop

```go
func (w *UniversalWorker) Stop() error
```

Gracefully stops the worker, completing active jobs within timeout.

#### GetStatus

```go
func (w *UniversalWorker) GetStatus() WorkerStatus
```

Returns current worker status (available, busy, full, etc.).

#### GetWorkerID

```go
func (w *UniversalWorker) GetWorkerID() string
```

Returns the unique worker ID assigned by the server.

### WorkerOptions Struct

```go
type WorkerOptions struct {
    AgentName         string
    JobType           livekit.JobType
    MaxJobs           int
    Namespace         string
    Permissions       *livekit.ParticipantPermission
    LoadCalculator    LoadCalculator
    ResourceLimiter   ResourceLimiter
    EnableJobRecovery bool
    RecoveryHandler   RecoveryHandler
    ShutdownTimeout   time.Duration
    HealthCheckInterval time.Duration
    WebSocketDebug    bool
    Logger            logger.Logger
}
```

**Fields:**

- `AgentName`: Unique identifier for this agent type (must match dispatch config)
- `JobType`: Single job type this worker handles
- `MaxJobs`: Maximum concurrent jobs (defaults to 1)
- `Namespace`: Agent namespace for multi-tenant deployments
- `Permissions`: Agent participant permissions
- `LoadCalculator`: Custom load calculation strategy
- `ResourceLimiter`: Resource monitoring and limits
- `EnableJobRecovery`: Enable automatic job recovery
- `RecoveryHandler`: Custom recovery logic
- `ShutdownTimeout`: Maximum time to wait for graceful shutdown
- `HealthCheckInterval`: How often to perform health checks
- `WebSocketDebug`: Enable WebSocket message logging
- `Logger`: Custom logger instance

## Job Handling

### Job Information

Jobs contain the following information accessible via the `livekit.Job` parameter:

```go
type Job struct {
    Id         string                 // Unique job identifier
    Type       JobType               // Job type (room/participant/publisher)
    Room       *RoomInfo            // Target room information
    Participant *ParticipantInfo     // Target participant (for participant jobs)
    Namespace   string               // Agent namespace
    Metadata    string               // JSON-encoded job metadata
    CreatedAt   int64                // Job creation timestamp
    UpdatedAt   int64                // Job last update timestamp
}
```

### JobMetadata

Metadata returned when accepting a job:

```go
type JobMetadata struct {
    ParticipantIdentity string // Agent's participant identity
    ParticipantName     string // Agent's display name
    ParticipantMetadata string // Agent's metadata (JSON)
}
```

### JobContext

Context provided to job handlers:

```go
type JobContext struct {
    Job      *livekit.Job        // Job information
    Room     *lksdk.Room         // Connected room instance
    Metadata *JobMetadata        // Agent metadata
}
```

### Room Information

Room context provided to job handlers:

```go
// lksdk.Room provides:
func (r *Room) Name() string                           // Room name
func (r *Room) SID() string                           // Room SID
func (r *Room) GetRemoteParticipants() []*RemoteParticipant // All participants
func (r *Room) LocalParticipant() *LocalParticipant   // Agent's participant
// Note: Callbacks cannot be modified after connection
```

### Context Management

Job contexts are automatically managed:

- Context is cancelled when job should terminate
- Use `ctx.Done()` to check for cancellation
- Always respect context cancellation for graceful shutdown
- Context deadline reflects job timeout if configured

## Agent Types

### SimpleUniversalHandler

```go
type SimpleUniversalHandler struct {
    // Job handling callbacks
    JobRequestFunc    func(context.Context, *livekit.Job) (bool, *JobMetadata)
    JobAssignedFunc   func(context.Context, *JobContext) error
    JobTerminatedFunc func(context.Context, string)
    
    // Participant event callbacks
    ParticipantJoinedFunc     func(context.Context, *lksdk.RemoteParticipant)
    ParticipantLeftFunc       func(context.Context, *lksdk.RemoteParticipant)
    TrackPublishedFunc        func(context.Context, *lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
    TrackUnpublishedFunc      func(context.Context, *lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
    TrackSubscribedFunc       func(context.Context, *lksdk.RemoteTrack, *lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
    TrackUnsubscribedFunc     func(context.Context, *lksdk.RemoteTrack, *lksdk.RemoteTrackPublication, *lksdk.RemoteParticipant)
    DataReceivedFunc          func(context.Context, []byte, *lksdk.RemoteParticipant)
    SpeakingChangedFunc       func(context.Context, *lksdk.RemoteParticipant, bool)
    ConnectionQualityChangedFunc func(context.Context, *lksdk.RemoteParticipant, livekit.ConnectionQuality)
}
```

Convenient handler implementation using function callbacks.

### BaseHandler

```go
type BaseHandler struct{}
```

Base implementation of UniversalHandler with no-op methods. Embed this in your handler to only implement the methods you need:

```go
type MyHandler struct {
    agent.BaseHandler
}

func (h *MyHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
    // Your implementation
}

func (h *MyHandler) OnJobAssigned(ctx context.Context, jobCtx *JobContext) error {
    // Your implementation
}
```

## Media Processing

### MediaPipeline

```go
type MediaPipeline struct {
    // Pipeline configuration
    InputTracks  []TrackInput
    OutputTracks []TrackOutput
    Processors   []MediaProcessor
}
```

Framework for processing media streams.

#### NewMediaPipeline

```go
func NewMediaPipeline(config PipelineConfig) *MediaPipeline
```

**PipelineConfig:**

- `BufferSize`: Audio/video buffer size
- `SampleRate`: Audio sample rate
- `Channels`: Audio channel count
- `FrameRate`: Video frame rate
- `Resolution`: Video resolution

### QualityController

```go
type QualityController struct {
    // Quality adaptation
    TargetBitrate    int
    MaxBitrate       int
    AdaptationSpeed  float64
    QualityLevels    []QualityLevel
}
```

Manages track quality based on network conditions.

#### NewQualityController

```go
func NewQualityController(opts QualityOptions) *QualityController
```

## Load Balancing

### LoadCalculator Interface

```go
type LoadCalculator interface {
    CalculateLoad(stats WorkerStats) float64
    ShouldAcceptJob(jobType JobType, load float64) bool
}
```

Custom load calculation for job distribution.

### DefaultLoadCalculator

```go
type DefaultLoadCalculator struct {
    CPUWeight    float64  // CPU usage weight (0-1)
    MemoryWeight float64  // Memory usage weight (0-1)
    JobWeight    float64  // Active jobs weight (0-1)
}
```

Default load calculation based on system resources.

#### NewDefaultLoadCalculator

```go
func NewDefaultLoadCalculator() *DefaultLoadCalculator
```

### CustomLoadCalculator

```go
type CustomLoadCalculator struct {
    Calculator func(WorkerStats) float64
    Acceptor   func(JobType, float64) bool
}
```

Custom load calculation with user-defined functions.

## Recovery & Resilience

### RecoveryHandler Interface

```go
type RecoveryHandler interface {
    OnJobFailed(job *Job, err error) RecoveryAction
    OnConnectionLost() RecoveryAction
    OnResourceExhausted(resource string) RecoveryAction
}
```

Custom recovery logic for various failure scenarios.

### RecoveryAction

```go
type RecoveryAction int

const (
    RecoveryActionRetry      RecoveryAction = iota // Retry the operation
    RecoveryActionSkip                            // Skip and continue
    RecoveryActionTerminate                       // Terminate worker
    RecoveryActionBackoff                         // Exponential backoff
)
```

### DefaultRecoveryHandler

```go
type DefaultRecoveryHandler struct {
    MaxRetries      int
    BackoffBase     time.Duration
    BackoffMultiplier float64
    MaxBackoff      time.Duration
}
```

Default recovery handler with exponential backoff.

## Monitoring & Metrics

### WorkerStats

```go
type WorkerStats struct {
    // Connection status
    Connected         bool
    ConnectionQuality string
    LastHeartbeat     time.Time
    
    // Job statistics
    ActiveJobs        int
    CompletedJobs     int64
    FailedJobs        int64
    TotalJobs         int64
    
    // Resource usage
    CPUPercent        float64
    MemoryMB          float64
    NetworkBytesIn    int64
    NetworkBytesOut   int64
    
    // Load metrics
    CurrentLoad       float64
    LoadHistory       []float64
    
    // Timing
    StartTime         time.Time
    Uptime            time.Duration
    LastJobTime       time.Time
}
```

### HealthMonitor

```go
type HealthMonitor struct {
    // Health check configuration
    CheckInterval     time.Duration
    HealthyThreshold  int
    UnhealthyThreshold int
    
    // Callbacks
    OnHealthy         func()
    OnUnhealthy       func(reason string)
}
```

Monitors worker health and triggers callbacks.

#### NewHealthMonitor

```go
func NewHealthMonitor(opts HealthOptions) *HealthMonitor
```

### ResourceMonitor

```go
type ResourceMonitor struct {
    // Internal fields
}

func NewResourceMonitor(logger *zap.Logger, opts ResourceMonitorOptions) *ResourceMonitor
```

Monitors system resources and detects issues like OOM conditions, goroutine leaks, and circular dependencies.

**Methods:**

- `Start()`: Begin monitoring resources
- `Stop()`: Stop monitoring
- `GetResourceStatus() ResourceStatus`: Get current resource status
- `SetOOMCallback(func())`: Set callback for OOM detection
- `SetLeakCallback(func(count int))`: Set callback for goroutine leak detection

**ResourceStatus:**

```go
type ResourceStatus struct {
    MemoryUsageMB       uint64
    MemoryLimitMB       uint64
    MemoryPercent       float64
    GoroutineCount      int
    GoroutineLimit      int
    HealthLevel         ResourceHealthLevel
    OOMDetected         bool
    LeakDetected        bool
    Timestamp           time.Time
}

const (
    ResourceHealthGood     ResourceHealthLevel = iota // < 80% memory, < 5000 goroutines
    ResourceHealthWarning                              // 80-90% memory, 5000-8000 goroutines
    ResourceHealthCritical                             // > 90% memory, > 8000 goroutines
)
```

## Participant Coordination

### MultiParticipantCoordinator

```go
type MultiParticipantCoordinator struct {
    // Internal fields
}

func NewMultiParticipantCoordinator() *MultiParticipantCoordinator
```

Coordinates activities across multiple participants in a room.

**Methods:**

- `RegisterParticipant(identity string, participant *lksdk.RemoteParticipant)`: Register a participant
- `UnregisterParticipant(identity string)`: Remove a participant
- `CreateGroup(id, name string, metadata map[string]interface{}) (*ParticipantGroup, error)`: Create a participant group
- `AddParticipantToGroup(identity, groupID string) error`: Add participant to group
- `RemoveParticipantFromGroup(identity, groupID string) error`: Remove from group
- `UpdateParticipantActivity(identity string, activityType ActivityType)`: Update participant activity
- `RecordInteraction(from, to, interactionType string, data interface{})`: Record interaction between participants
- `GetActiveParticipants() []*CoordinatedParticipant`: Get active participants
- `GetParticipantGroups(identity string) []*ParticipantGroup`: Get groups for a participant
- `GetGroupMembers(groupID string) []string`: Get members of a group
- `GetInteractionGraph() map[string]map[string]int`: Get interaction graph
- `GetActivityMetrics() ActivityMetrics`: Get activity metrics
- `AddCoordinationRule(rule CoordinationRule)`: Add coordination rule
- `RegisterEventHandler(eventType string, handler CoordinationEventHandler)`: Register event handler
- `Stop()`: Stop the coordinator

**Activity Types:**

```go
const (
    ActivityTypeJoined           ActivityType = "joined"
    ActivityTypeLeft             ActivityType = "left"
    ActivityTypeTrackPublished   ActivityType = "track_published"
    ActivityTypeTrackUnpublished ActivityType = "track_unpublished"
    ActivityTypeDataReceived     ActivityType = "data_received"
    ActivityTypeSpeaking         ActivityType = "speaking"
    ActivityTypeMetadataChanged  ActivityType = "metadata_changed"
)
```

### ParticipantEventProcessor

```go
type ParticipantEventProcessor struct {
    // Internal fields
}

func NewParticipantEventProcessor() *ParticipantEventProcessor
```

Processes participant events with filtering, batching, and async handling.

**Methods:**

- `QueueEvent(event ParticipantEvent)`: Queue an event for processing
- `RegisterHandler(eventType EventType, handler EventHandler)`: Register event handler
- `AddFilter(filter EventFilter)`: Add event filter
- `AddBatchProcessor(processor BatchEventProcessor)`: Add batch processor
- `ProcessPendingEvents()`: Process pending events synchronously
- `GetEventHistory(limit int) []ParticipantEvent`: Get recent event history
- `GetMetrics() map[string]interface{}`: Get processing metrics
- `Stop()`: Stop the processor

**Event Types:**

```go
const (
    EventTypeParticipantJoined  EventType = "participant_joined"
    EventTypeParticipantLeft    EventType = "participant_left"
    EventTypeTrackPublished     EventType = "track_published"
    EventTypeTrackUnpublished   EventType = "track_unpublished"
    EventTypeMetadataChanged    EventType = "metadata_changed"
    EventTypeSpeakingChanged    EventType = "speaking_changed"
    EventTypeDataReceived       EventType = "data_received"
    EventTypeConnectionQuality  EventType = "connection_quality"
)
```

## Resource Management

### ResourcePool

```go
type ResourcePool struct {
    // Internal fields
}

func NewResourcePool(factory ResourceFactory, opts ResourcePoolOptions) (*ResourcePool, error)
```

Manages a pool of reusable resources for efficient resource allocation.

**Methods:**

- `Acquire(ctx context.Context) (Resource, error)`: Get a resource from the pool
- `Release(resource Resource)`: Return a resource to the pool
- `Size() int`: Get current pool size
- `Available() int`: Get number of available resources
- `InUse() int`: Get number of resources in use
- `Stats() map[string]int64`: Get pool statistics
- `Close() error`: Close the pool and release all resources

**ResourcePoolOptions:**

```go
type ResourcePoolOptions struct {
    MinSize     int           // Minimum pool size
    MaxSize     int           // Maximum pool size
    MaxIdleTime time.Duration // Max idle time before resource cleanup
}
```

## Configuration

### Environment Variables

The SDK supports the following environment variables:

- `LIVEKIT_URL`: LiveKit server URL
- `LIVEKIT_API_KEY`: API key for authentication
- `LIVEKIT_API_SECRET`: API secret for authentication
- `LIVEKIT_AGENT_NAME`: Default agent name
- `LIVEKIT_NAMESPACE`: Agent namespace
- `LIVEKIT_MAX_CONCURRENT_JOBS`: Maximum concurrent jobs
- `LIVEKIT_LOG_LEVEL`: Logging level (debug, info, warn, error)
- `LIVEKIT_RECOVERY_ENABLED`: Enable job recovery (true/false)
- `LIVEKIT_WEBSOCKET_DEBUG`: Enable WebSocket debugging (true/false)

### Configuration Helpers

```go
// Load configuration from environment
func LoadConfigFromEnv() (*Config, error)

// Validate configuration
func ValidateConfig(config *Config) error

// Get environment variable with default
func GetEnvWithDefault(key, defaultValue string) string

// Parse environment variable as integer
func GetEnvAsInt(key string, defaultValue int) int

// Parse environment variable as duration
func GetEnvAsDuration(key string, defaultValue time.Duration) time.Duration

// Parse environment variable as boolean
func GetEnvAsBool(key string, defaultValue bool) bool
```

### Resource Limits

```go
type ResourceLimits struct {
    MaxMemoryMB   int           // Maximum memory usage in MB
    MaxCPUPercent float64       // Maximum CPU usage percentage
    MaxJobs       int           // Maximum concurrent jobs
    MaxUptime     time.Duration // Maximum worker uptime
    
    // Callbacks
    OnMemoryLimit func()        // Called when memory limit reached
    OnCPULimit    func()        // Called when CPU limit reached
    OnJobLimit    func()        // Called when job limit reached
}
```

### ResourceLimiter

```go
type ResourceLimiter interface {
    CheckLimits() error
    GetCurrentUsage() ResourceUsage
    SetLimits(limits ResourceLimits)
}
```

## Error Handling

### Common Error Types

```go
var (
    ErrWorkerNotConnected    = errors.New("worker not connected")
    ErrJobTimeout            = errors.New("job execution timeout")
    ErrResourceExhausted     = errors.New("resource limits exceeded") 
    ErrInvalidJobType        = errors.New("invalid job type")
    ErrConnectionLost        = errors.New("connection to LiveKit lost")
    ErrRecoveryFailed        = errors.New("job recovery failed")
    ErrShutdownTimeout       = errors.New("shutdown timeout exceeded")
)
```

### Error Recovery

All APIs return structured errors that can be handled appropriately:

```go
if err := worker.Start(ctx); err != nil {
    switch {
    case errors.Is(err, ErrConnectionLost):
        // Handle connection issues
    case errors.Is(err, ErrResourceExhausted):
        // Handle resource problems
    default:
        // Handle other errors
    }
}
```

## Examples

See the [examples directory](examples/) for complete working examples of all APIs.

## Versioning

The Agent SDK follows semantic versioning. This documentation corresponds to version 0.1.x. 

Breaking changes will increment the major version number, and new features will increment the minor version.

## Support

- [Documentation](../README.md)
- [Examples](examples/)
- [Troubleshooting](troubleshooting.md)
- [Community Slack](https://livekit.io/slack)