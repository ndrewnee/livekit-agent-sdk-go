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
- [Configuration](#configuration)

## Core Types

### JobHandler Interface

```go
type JobHandler interface {
    OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
}
```

The primary interface that must be implemented by all agent handlers.

**Methods:**

- `OnJob(ctx, job, room)`: Called when a job is assigned to the agent
  - `ctx`: Context for job execution (cancelled when job should stop)
  - `job`: Job information including type, metadata, and ID
  - `room`: Connected LiveKit room instance
  - Returns: Error if job execution fails

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

### Worker Struct

```go
type Worker struct {
    // Internal fields (not exported)
}
```

Main worker instance that connects to LiveKit and processes jobs.

### NewWorker Function

```go
func NewWorker(
    url string,
    apiKey string,
    apiSecret string,
    handler JobHandler,
    opts WorkerOptions,
) *Worker
```

Creates a new worker instance.

**Parameters:**

- `url`: LiveKit server WebSocket URL (e.g., "ws://localhost:7880")
- `apiKey`: LiveKit API key for authentication
- `apiSecret`: LiveKit API secret for authentication  
- `handler`: JobHandler implementation
- `opts`: Worker configuration options

**Returns:** Worker instance ready to start

### Worker Methods

#### Start

```go
func (w *Worker) Start(ctx context.Context) error
```

Starts the worker and begins processing jobs. Blocks until context is cancelled or fatal error occurs.

#### Stop

```go
func (w *Worker) Stop(ctx context.Context) error
```

Gracefully stops the worker, completing active jobs within timeout.

#### IsHealthy

```go
func (w *Worker) IsHealthy() bool
```

Returns true if worker is connected and healthy.

#### GetStats

```go
func (w *Worker) GetStats() WorkerStats
```

Returns current worker statistics and metrics.

### WorkerOptions Struct

```go
type WorkerOptions struct {
    AgentName         string
    JobTypes          []livekit.JobType
    MaxConcurrentJobs int
    Namespace         string
    LoadCalculator    LoadCalculator
    ResourceLimiter   ResourceLimiter
    EnableJobRecovery bool
    RecoveryHandler   RecoveryHandler
    ShutdownTimeout   time.Duration
    HealthCheckInterval time.Duration
    WebSocketDebug    bool
}
```

**Fields:**

- `AgentName`: Unique identifier for this agent type
- `JobTypes`: Supported job types (defaults to all types)
- `MaxConcurrentJobs`: Maximum concurrent jobs (defaults to 1)
- `Namespace`: Agent namespace for multi-tenant deployments
- `LoadCalculator`: Custom load calculation strategy
- `ResourceLimiter`: Resource monitoring and limits
- `EnableJobRecovery`: Enable automatic job recovery
- `RecoveryHandler`: Custom recovery logic
- `ShutdownTimeout`: Maximum time to wait for graceful shutdown
- `HealthCheckInterval`: How often to perform health checks
- `WebSocketDebug`: Enable WebSocket message logging

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

### Room Information

Room context provided to job handlers:

```go
// lksdk.Room provides:
func (r *Room) Name() string                           // Room name
func (r *Room) SID() string                           // Room SID
func (r *Room) GetRemoteParticipants() []*RemoteParticipant // All participants
func (r *Room) LocalParticipant() *LocalParticipant   // Agent's participant
func (r *Room) Callback                               // Event callbacks
```

### Context Management

Job contexts are automatically managed:

- Context is cancelled when job should terminate
- Use `ctx.Done()` to check for cancellation
- Always respect context cancellation for graceful shutdown
- Context deadline reflects job timeout if configured

## Agent Types

### ParticipantAgent

```go
type ParticipantAgent struct {
    // Configuration
    Identity string
    Metadata string
    
    // Handlers
    OnTrackPublished   func(*RemoteTrackPublication, *RemoteParticipant)
    OnTrackUnpublished func(*RemoteTrackPublication, *RemoteParticipant)
    OnDataReceived     func([]byte, DataReceiveParams)
    OnDisconnected     func()
}
```

Agent that acts as a room participant.

#### NewParticipantAgent

```go
func NewParticipantAgent(opts ParticipantAgentOptions) *ParticipantAgent
```

**ParticipantAgentOptions:**

- `Identity`: Participant identity (must be unique in room)
- `Metadata`: JSON metadata attached to participant
- `AutoSubscribe`: Automatically subscribe to tracks
- `Permissions`: Participant permissions

### PublisherAgent

```go
type PublisherAgent struct {
    // Media publishing
    AudioTrack *LocalAudioTrack
    VideoTrack *LocalVideoTrack
    
    // Event handlers
    OnTrackMuted   func(bool, TrackKind)
    OnSubscribed   func(*RemoteParticipant, *TrackPublication)
}
```

Agent that publishes media to rooms.

#### NewPublisherAgent

```go
func NewPublisherAgent(opts PublisherAgentOptions) *PublisherAgent
```

**PublisherAgentOptions:**

- `Identity`: Publisher identity
- `AudioEnabled`: Enable audio publishing
- `VideoEnabled`: Enable video publishing
- `TrackOptions`: Audio/video track configuration

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