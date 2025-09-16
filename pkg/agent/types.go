// Package agent provides a framework for building LiveKit agents that can process
// audio, video, and data streams in real-time. Agents are server-side applications
// that join LiveKit rooms as participants and can interact with other participants.
//
// The package supports different types of agents:
//   - Room agents: Join when a room is created
//   - Participant agents: Join when participants connect
//   - Publisher agents: Join when participants start publishing media
//
// Key features:
//   - Automatic job assignment and load balancing
//   - Graceful shutdown and job recovery
//   - Resource monitoring and limits
//   - Connection resilience with automatic reconnection
//   - Extensible plugin system for custom functionality
package agent

import (
	"context"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// JobHandler is the interface that agents must implement to handle jobs.
// A job represents a task assigned to an agent, typically involving joining
// a LiveKit room and performing some processing on the media streams or
// interacting with participants.
//
// Implementations should be thread-safe as methods may be called concurrently.
type JobHandler interface {
	// OnJobRequest is called when a job is offered to the agent.
	// The agent should inspect the job details and decide whether to accept it.
	// If accepted, the agent should return metadata specifying how it will join the room.
	//
	// This method should return quickly as the server waits for the response.
	// Heavy initialization should be deferred to OnJobAssigned.
	//
	// Parameters:
	//   - ctx: Context for the request, may have a deadline
	//   - job: The job being offered, contains room and participant information
	//
	// Returns:
	//   - accept: true to accept the job, false to decline
	//   - metadata: Agent participant metadata if accepting (can be nil)
	OnJobRequest(ctx context.Context, job *livekit.Job) (accept bool, metadata *JobMetadata)

	// OnJobAssigned is called when a job has been assigned to this agent.
	// The agent is already connected to the room as a participant when this is called.
	// This is where the main agent logic should be implemented.
	//
	// The method should block until the agent's work is complete or the context is cancelled.
	// Returning an error will mark the job as failed.
	//
	// Parameters:
	//   - ctx: Context for the job, cancelled when the job should terminate
	//   - job: The assigned job with full details
	//   - room: Connected room client for interacting with the room
	//
	// Returns:
	//   - error: nil on success, error to mark job as failed
	OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error

	// OnJobTerminated is called when a job is terminated.
	// This can happen due to:
	//   - Agent disconnection
	//   - Job dispatch removal by the server
	//   - Server shutdown
	//   - Explicit job termination request
	//
	// Use this method to clean up any resources associated with the job.
	// The room connection is already closed when this is called.
	//
	// Parameters:
	//   - ctx: Context for cleanup operations
	//   - jobID: ID of the terminated job
	OnJobTerminated(ctx context.Context, jobID string)
}

// JobMetadata contains agent-specific metadata for a job.
// When an agent accepts a job, it provides this metadata to specify
// how it will appear as a participant in the room.
type JobMetadata struct {
	// ParticipantIdentity is the unique identity the agent will use when joining the room.
	// If empty, a default identity will be generated.
	ParticipantIdentity string

	// ParticipantName is the display name for the agent participant.
	// This is what other participants will see as the agent's name.
	ParticipantName string

	// ParticipantMetadata is optional metadata attached to the participant.
	// This can be any string data (often JSON) that other participants can read.
	ParticipantMetadata string

	// ParticipantAttributes are key-value pairs attached to the participant.
	// These are synchronized to all participants and can be updated during the session.
	ParticipantAttributes map[string]string

	// SupportsResume indicates if the agent can resume a previously started job.
	// If true, the agent should be able to recover its state if reconnected to the same job.
	SupportsResume bool
}

// RoomCallbackProvider is an optional interface that handlers can implement
// to provide custom room callbacks that will be merged with the Worker's callbacks
type RoomCallbackProvider interface {
	// GetRoomCallbacks returns callbacks to be used when connecting to the room
	// These will be merged with the Worker's default callbacks
	GetRoomCallbacks() *lksdk.RoomCallback
}

// WorkerOptions configures the agent worker behavior and capabilities.
// These options control how the worker connects to LiveKit, handles jobs,
// manages resources, and reports its status.
type WorkerOptions struct {
	// AgentName identifies this agent type.
	// Jobs can be dispatched to specific agent names.
	// If empty, the agent will receive jobs for any agent name.
	AgentName string
	// Version is the agent version string.
	// This is reported to the server for debugging and compatibility checks.
	Version string
	// Namespace provides multi-tenant isolation.
	// Agents in different namespaces cannot see each other's jobs.
	// If empty, uses the default namespace.
	Namespace string
	// JobType specifies which type of jobs this agent handles.
	// Common types include:
	//   - JT_ROOM: Agent joins when a room is created
	//   - JT_PARTICIPANT: Agent joins when participants connect
	//   - JT_PUBLISHER: Agent joins when participants publish media
	JobType livekit.JobType
	// Permissions the agent will have when joining rooms.
	// These permissions determine what the agent can do:
	//   - CanPublish: Publish audio/video tracks
	//   - CanSubscribe: Subscribe to other participants' tracks
	//   - CanPublishData: Send data messages
	//   - Hidden: Hide from other participants
	// If nil, default permissions are used.
	Permissions *livekit.ParticipantPermission
	// MaxJobs is the maximum number of concurrent jobs this worker will handle.
	// Once this limit is reached, the worker reports as "full" and won't receive new jobs.
	// Set to 0 for unlimited jobs (limited only by system resources).
	MaxJobs int
	// Logger for debug output.
	// If nil, a default logger is used.
	// Implement the Logger interface for custom logging integration.
	Logger Logger
	// PingInterval for keepalive messages to the server.
	// Regular pings ensure the connection stays active and detect disconnections quickly.
	// Default: 10s
	PingInterval time.Duration

	// PingTimeout for keepalive responses.
	// If a ping response isn't received within this duration, the connection is considered lost.
	// Default: 2s
	PingTimeout time.Duration
	// LoadCalculator for custom load calculation.
	// Implement this to define custom metrics for job assignment decisions.
	// If nil and EnableCPUMemoryLoad is true, a default calculator is used.
	LoadCalculator LoadCalculator

	// EnableCPUMemoryLoad enables CPU/memory-based load calculation.
	// When true, the worker reports system resource usage to help with job distribution.
	EnableCPUMemoryLoad bool

	// EnableLoadPrediction enables predictive load calculation.
	// Uses historical data to predict future load and make better job assignment decisions.
	EnableLoadPrediction bool
	// StatusUpdateBatchInterval batches status updates to reduce server load.
	// Multiple status updates within this interval are combined into a single message.
	// Set to 0 to disable batching (send updates immediately).
	StatusUpdateBatchInterval time.Duration

	// StatusRefreshInterval sets how often to refresh worker status with the server.
	// This helps prevent the server from "forgetting" about the worker after extended periods.
	// Set to 0 to disable periodic refresh (not recommended for production).
	// Default: 5 minutes
	StatusRefreshInterval time.Duration
	// JobRecoveryHandler for custom job recovery logic.
	// Called when reconnecting to determine which jobs should be resumed.
	// If nil, all jobs are recovered by default when EnableJobRecovery is true.
	JobRecoveryHandler JobRecoveryHandler

	// EnableJobRecovery enables job recovery after reconnection.
	// When true, the worker attempts to resume jobs after temporary disconnections.
	EnableJobRecovery bool
	// PartialMessageBufferSize max size for partial WebSocket message buffering.
	// Prevents memory exhaustion from malformed or malicious messages.
	// Default: 1MB
	PartialMessageBufferSize int
	// EnableJobQueue enables job queueing with priority handling.
	// Queued jobs are processed in priority order when capacity becomes available.
	EnableJobQueue bool

	// JobQueueSize maximum number of jobs in queue.
	// When the queue is full, new jobs are rejected.
	// Set to 0 for unlimited queue size.
	JobQueueSize int

	// JobPriorityCalculator custom priority calculator.
	// Implement this to define custom job prioritization logic.
	// If nil, jobs are processed in FIFO order.
	JobPriorityCalculator JobPriorityCalculator
	// EnableResourcePool enables resource pooling for efficient resource reuse.
	// Useful for expensive resources like ML models or media processors.
	EnableResourcePool bool

	// ResourcePoolMinSize minimum number of resources to maintain in the pool.
	// Resources are pre-allocated to this level for quick job startup.
	ResourcePoolMinSize int

	// ResourcePoolMaxSize maximum number of resources in pool.
	// Limits memory usage from idle resources.
	ResourcePoolMaxSize int

	// ResourceFactory custom resource factory.
	// Implement this to define how resources are created and destroyed.
	// If nil, resource pooling is disabled even if EnableResourcePool is true.
	ResourceFactory ResourceFactory
	// StrictProtocolMode rejects unknown message types if true.
	// Enable for better security and protocol compliance.
	// Disable for forward compatibility with newer server versions.
	StrictProtocolMode bool

	// CustomMessageHandlers for handling custom message types.
	// Allows extending the protocol with application-specific messages.
	CustomMessageHandlers []MessageTypeHandler
	// EnableResourceMonitoring enables out-of-memory and leak detection.
	// The worker monitors resource usage and can take corrective action.
	EnableResourceMonitoring bool

	// MemoryLimitMB sets memory limit for OOM detection.
	// When exceeded, the worker may reject new jobs or trigger garbage collection.
	// Set to 0 to use 80% of system memory.
	MemoryLimitMB int

	// GoroutineLimit sets max goroutines before leak detection.
	// Helps detect goroutine leaks that could exhaust system resources.
	// Set to 0 to use default limit of 10000.
	GoroutineLimit int
	// EnableResourceLimits enables hard resource limit enforcement.
	// Uses OS-level limits to prevent resource exhaustion.
	// May require elevated privileges on some systems.
	EnableResourceLimits bool

	// HardMemoryLimitMB sets hard memory limit in MB.
	// Process is killed if this limit is exceeded.
	// Set to 0 to use default of 1024MB.
	HardMemoryLimitMB int

	// CPUQuotaPercent sets CPU quota as percentage.
	// 100 = 1 CPU core, 200 = 2 CPU cores, etc.
	// Set to 0 to use default of 200 (2 cores).
	CPUQuotaPercent int

	// MaxFileDescriptors sets max file descriptors.
	// Prevents "too many open files" errors.
	// Set to 0 to use default of 1024.
	MaxFileDescriptors int
}

// WorkerStatus represents the current state of the worker.
// Used by the server to determine job assignment.
type WorkerStatus int

const (
	// WorkerStatusAvailable indicates the worker can accept new jobs.
	WorkerStatusAvailable WorkerStatus = iota

	// WorkerStatusFull indicates the worker is at capacity.
	// No new jobs will be assigned until capacity is available.
	WorkerStatusFull
)

// String returns the string representation of WorkerStatus.
// Returns "available", "full", or "unknown".
func (ws WorkerStatus) String() string {
	switch ws {
	case WorkerStatusAvailable:
		return "available"
	case WorkerStatusFull:
		return "full"
	default:
		return "unknown"
	}
}

// Logger interface for pluggable logging.
// Implement this interface to integrate with your application's logging system.
// The fields parameter accepts key-value pairs for structured logging.
type Logger interface {
	// Debug logs a debug-level message with optional fields.
	Debug(msg string, fields ...interface{})

	// Info logs an info-level message with optional fields.
	Info(msg string, fields ...interface{})

	// Warn logs a warning-level message with optional fields.
	Warn(msg string, fields ...interface{})

	// Error logs an error-level message with optional fields.
	Error(msg string, fields ...interface{})
}

// Error represents a typed error with a code and message.
// Error codes are stable and can be used for programmatic error handling.
type Error struct {
	// Code is a stable identifier for the error type.
	Code string

	// Message provides human-readable error details.
	Message string
}

// Error implements the error interface.
// Returns a string in the format "CODE: message".
func (e *Error) Error() string {
	return e.Code + ": " + e.Message
}

// Common errors returned by the agent framework.
// Use errors.Is() to check for specific error types.
var (
	// ErrConnectionFailed indicates a failure to establish connection to LiveKit server.
	ErrConnectionFailed = &Error{Code: "CONNECTION_FAILED", Message: "failed to connect to LiveKit server"}

	// ErrAuthenticationError indicates invalid or missing authentication credentials.
	ErrAuthenticationError = &Error{Code: "AUTHENTICATION_ERROR", Message: "authentication failed"}

	// ErrRegistrationTimeout indicates the worker registration process timed out.
	ErrRegistrationTimeout = &Error{Code: "REGISTRATION_TIMEOUT", Message: "worker registration timed out"}

	// ErrProtocolError indicates a protocol-level error in communication.
	ErrProtocolError = &Error{Code: "PROTOCOL_ERROR", Message: "protocol error"}

	// ErrJobNotFound indicates the requested job does not exist.
	ErrJobNotFound = &Error{Code: "JOB_NOT_FOUND", Message: "job not found"}

	// ErrInvalidCredentials indicates the API key or secret is invalid.
	ErrInvalidCredentials = &Error{Code: "INVALID_CREDENTIALS", Message: "invalid API key or secret"}

	// ErrProtocolMismatch indicates incompatible protocol versions between agent and server.
	ErrProtocolMismatch = &Error{Code: "PROTOCOL_MISMATCH", Message: "protocol version not supported by server"}

	// ErrRegistrationRejected indicates the server rejected the worker registration.
	ErrRegistrationRejected = &Error{Code: "REGISTRATION_REJECTED", Message: "worker registration rejected by server"}

	// ErrTokenExpired indicates the authentication token has expired and needs renewal.
	ErrTokenExpired = &Error{Code: "TOKEN_EXPIRED", Message: "authentication token has expired"}

	// ErrRoomNotFound indicates the specified room does not exist.
	ErrRoomNotFound = &Error{Code: "ROOM_NOT_FOUND", Message: "room does not exist"}

	// ErrParticipantNotFound indicates the specified participant does not exist.
	ErrParticipantNotFound = &Error{Code: "PARTICIPANT_NOT_FOUND", Message: "participant does not exist"}

	// ErrTrackNotFound indicates the specified track does not exist.
	ErrTrackNotFound = &Error{Code: "TRACK_NOT_FOUND", Message: "track does not exist"}
)

// WorkerState holds persistent state for reconnection recovery.
// This state is preserved across disconnections to enable seamless recovery.
type WorkerState struct {
	// WorkerID is the unique identifier assigned by the server.
	WorkerID string

	// ActiveJobs maps job IDs to their current state.
	ActiveJobs map[string]*JobState

	// LastStatus is the last reported worker status.
	LastStatus WorkerStatus

	// LastLoad is the last reported load value (0.0 to 1.0).
	LastLoad float32
}

// JobState holds persistent job state for recovery.
// This information allows jobs to be resumed after temporary disconnections.
type JobState struct {
	// JobID is the unique job identifier.
	JobID string

	// Status is the current job status.
	Status livekit.JobStatus

	// StartedAt is when the job was started.
	StartedAt time.Time

	// RoomName is the name of the room associated with the job.
	RoomName string
}

// ParticipantInfo stores information about a participant
type ParticipantInfo struct {
	Identity          string
	Name              string
	Metadata          string
	JoinedAt          time.Time
	LastActivity      time.Time
	Permissions       *livekit.ParticipantPermission
	Attributes        map[string]string
	IsSpeaking        bool
	AudioLevel        float32
	ConnectionQuality livekit.ConnectionQuality
	Participant       *lksdk.RemoteParticipant
}

// activeJob represents an active job with its associated room
type activeJob struct {
	job       *livekit.Job
	room      *lksdk.Room
	startTime time.Time
	startedAt time.Time
	status    livekit.JobStatus
	token     string
}

// VideoDimensions represents video dimensions
type VideoDimensions struct {
	Width  uint32
	Height uint32
}

// PublisherTrackSubscription represents a subscription to a publisher's track
type PublisherTrackSubscription struct {
	TrackID           string
	ParticipantID     string
	TrackInfo         *livekit.TrackInfo
	Publication       *lksdk.RemoteTrackPublication
	Track             interface{} // Generic track interface
	SubscribedAt      time.Time
	Quality           livekit.VideoQuality
	PreferredQuality  livekit.VideoQuality
	CurrentQuality    livekit.VideoQuality
	LastQualityChange time.Time
	Dimensions        *VideoDimensions
	FrameRate         float32
	Enabled           bool
}
