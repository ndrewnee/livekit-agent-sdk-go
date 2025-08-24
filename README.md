# LiveKit Agent SDK for Go

A Go SDK for building LiveKit agents that are fully compliant with the LiveKit agent protocol and lifecycle. This SDK enables you to create autonomous agents that can join LiveKit rooms, process media streams, and interact with participants.

## Features

- ✅ Full implementation of LiveKit Agent Protocol v1
- ✅ Automatic reconnection and error handling
- ✅ Support for all job types (Room, Publisher, Participant)
- ✅ Load balancing and worker management
- ✅ WebSocket connection with JSON/Protobuf support
- ✅ Graceful shutdown and job cleanup
- ✅ Helper utilities for common agent tasks

## Installation

```bash
go get github.com/am-sokolov/livekit-agent-sdk-go
```

## Quick Start

```go
package main

import (
    "context"
    "log"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
    // Create a handler using the simple handler helper
    handler := &agent.SimpleUniversalHandler{
        JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
            // Accept all jobs
            return true, &agent.JobMetadata{
                ParticipantIdentity: "my-agent",
                ParticipantName:     "My Agent",
            }
        },
        JobAssignedFunc: func(ctx context.Context, jobCtx *agent.JobContext) error {
            log.Printf("Agent joined room: %s", jobCtx.Room.Name())
            
            // Your agent logic here
            <-ctx.Done()
            
            return nil
        },
        JobTerminatedFunc: func(ctx context.Context, jobID string) {
            log.Printf("Job terminated: %s", jobID)
        },
    }
    
    // Create worker options
    opts := agent.WorkerOptions{
        AgentName: "my-agent",
        JobType:   livekit.JobType_JT_ROOM,
        MaxJobs:   5,
    }
    
    // Create and start the universal worker
    worker := agent.NewUniversalWorker("ws://localhost:7880", "devkey", "secret", handler, opts)
    
    ctx := context.Background()
    if err := worker.Start(ctx); err != nil {
        log.Fatal(err)
    }
    
    // Run until interrupted
    select {}
}
```

## Agent Types

The SDK supports three types of agents:

### Room Agent (`JT_ROOM`)
Launched when a room is created with agent dispatch configuration. Has access to all room events and participants.

```go
opts := agent.WorkerOptions{
    JobType: livekit.JobType_JT_ROOM,
    MaxJobs: 5,  // Handle up to 5 concurrent rooms
}
```

### Publisher Agent (`JT_PUBLISHER`)
Launched for media publishing jobs. Can generate and publish audio/video content to rooms.

```go
opts := agent.WorkerOptions{
    JobType: livekit.JobType_JT_PUBLISHER,
    MaxJobs: 10,  // Handle multiple concurrent publishing jobs
}
```

### Participant Agent (`JT_PARTICIPANT`)
Launched for individual participant monitoring and interaction. Provides personalized services per participant.

```go
opts := agent.WorkerOptions{
    JobType: livekit.JobType_JT_PARTICIPANT,
    MaxJobs: 20,  // Handle many participants concurrently
}
```

## Advanced Usage

### Custom Job Handler

Implement the `UniversalHandler` interface for full control:

```go
type MyAgent struct {
    agent.BaseHandler
}

func (a *MyAgent) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    // Decide whether to accept the job
    return true, &agent.JobMetadata{
        ParticipantIdentity: "my-agent-" + job.Id,
        ParticipantName:     "My Agent",
        ParticipantMetadata: `{"version": "1.0"}`,
    }
}

func (a *MyAgent) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    // Access the room and job details
    log.Printf("Handling job %s in room %s", jobCtx.Job.Id, jobCtx.Room.Name())
    
    // Handle the job
    return nil
}

func (a *MyAgent) OnJobTerminated(ctx context.Context, jobID string) {
    // Cleanup
}

// Optional: Handle participant events
func (a *MyAgent) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
    log.Printf("Participant joined: %s", participant.Identity())
}
```

### Worker Options

```go
opts := agent.WorkerOptions{
    AgentName:    "my-agent",           // Agent identifier (must match dispatch config)
    Namespace:    "production",         // For multi-tenant isolation
    JobType:      livekit.JobType_JT_ROOM,  // Single job type per worker
    Permissions:  &livekit.ParticipantPermission{
        CanPublish:   true,
        CanSubscribe: true,
    },
    MaxJobs:      10,                   // Maximum concurrent jobs
    Logger:       customLogger,         // Custom logger
}
```

### Room Monitoring Pattern

Since room callbacks cannot be modified after connection, use polling patterns for monitoring:

```go
func monitorRoom(ctx context.Context, room *lksdk.Room) {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    
    knownParticipants := make(map[string]bool)
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Check for new participants
            for _, p := range room.GetRemoteParticipants() {
                if !knownParticipants[p.Identity()] {
                    knownParticipants[p.Identity()] = true
                    // Handle new participant
                    log.Printf("New participant: %s", p.Identity())
                }
            }
        }
    }
}
```

### Load Balancing

For multi-worker deployments:

```go
loadBalancer := agent.NewLoadBalancer()

// Update worker status
loadBalancer.UpdateWorker("worker1", 0.5, 5, 10)

// Get least loaded worker
worker := loadBalancer.GetLeastLoadedWorker()
```

## Important: Agent Dispatch Configuration

Agents only receive jobs when rooms are created with agent dispatch configuration. Simply starting an agent and joining a room won't trigger job dispatch.

**Note:** LiveKit server in dev mode fully supports agent dispatch. Make sure to include the `Agents` field when creating rooms.

### Creating Rooms with Agent Dispatch

```go
// Create a room with agent dispatch enabled
room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
    Name: "my-room",
    Agents: []*livekit.RoomAgentDispatch{
        {
            AgentName: "my-agent", // Must match worker's AgentName
            Metadata:  "optional metadata",
        },
    },
})
```

### Testing Your Agent

1. Start the LiveKit server in dev mode:
   ```bash
   docker run --rm -p 7880:7880 livekit/livekit-server --dev
   ```

2. Run your agent:
   ```bash
   export LIVEKIT_URL="ws://localhost:7880"
   export LIVEKIT_API_KEY="devkey"
   export LIVEKIT_API_SECRET="secret"
   go run main.go
   ```

3. Create a room with agent dispatch:
   ```go
   room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
       Name: "test-room",
       Agents: []*livekit.RoomAgentDispatch{{
           AgentName: "my-agent",  // Must match your agent's name
       }},
   })
   ```

4. The agent will automatically receive and process the job.

## Examples

The repository includes three complete, working examples that demonstrate different agent types and use cases:

### 1. Simple Room Agent

A basic agent that monitors room activity and collects analytics.

```bash
cd examples/simple-room-agent
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
go run .
```

**Features:**
- Room event monitoring with polling pattern
- Participant tracking and statistics
- Data message publishing
- Graceful shutdown handling

### 2. Participant Monitoring Agent

Provides personalized monitoring for individual participants.

```bash
cd examples/participant-monitoring-agent
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
go run .
```

**Features:**
- Individual participant tracking
- Audio level monitoring & speaking detection
- Connection quality tracking
- Personalized welcome messages

### 3. Media Publisher Agent

Demonstrates publishing audio and video content to LiveKit rooms.

```bash
cd examples/media-publisher-agent
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
go run .
```

**Features:**
- Audio tone generation (sine waves)
- Video pattern generation (color bars, etc.)
- Multiple publishing modes (audio/video/both)
- Interactive control via data messages

### Running Examples

Each example includes dispatch scripts to create rooms with proper agent configuration:

```bash
# In one terminal, run the agent
go run .

# In another terminal, dispatch a job
go run . dispatch
```

### Testing All Examples

A comprehensive test script is provided to verify all examples:

```bash
cd examples
./test-all-examples.sh
```

This will build and briefly run each example to ensure they're working correctly.

## Architecture

The SDK implements the complete LiveKit Agent Protocol:

1. **WebSocket Connection**: Establishes secure connection to LiveKit server
2. **Worker Registration**: Registers capabilities and receives unique worker ID
3. **Job Discovery**: Receives job availability requests based on agent type
4. **Job Assignment**: Accepts jobs and receives authentication tokens
5. **Room Participation**: Joins rooms as a special participant
6. **Status Updates**: Reports job progress and worker load
7. **Graceful Termination**: Handles job cleanup and disconnection

## Key Implementation Notes

### API Changes from Documentation

This SDK implements the latest LiveKit Agent Protocol with some important differences from older documentation:

1. **JobHandler Interface**: Uses three separate methods instead of a single callback:
   - `OnJobRequest()` - Decide whether to accept a job
   - `OnJobAssigned()` - Handle the assigned job
   - `OnJobTerminated()` - Clean up when job ends

2. **Worker Options**: 
   - `JobType` (singular) instead of `JobTypes` array
   - `MaxJobs` instead of `MaxConcurrentJobs`

3. **Room Monitoring**: Callbacks cannot be set after room connection. Use polling patterns instead.

4. **Track Access**: Use `participant.TrackPublications()` instead of `participant.Tracks()`

## Protocol Compliance

This SDK is fully compliant with:
- LiveKit Agent Protocol v1
- WebSocket communication (JSON/Protobuf)
- Worker lifecycle management
- Job state machine
- Load-based routing
- Graceful shutdown

## Error Handling

The SDK provides robust error handling:
- Automatic reconnection on connection loss
- Job failure reporting
- Timeout management
- Graceful degradation

## Documentation

For detailed developer documentation, see the [docs](docs/) directory:
- [Getting Started Guide](docs/getting-started.md)
- [Concepts and Architecture](docs/concepts.md)
- [Job Handling](docs/job-handling.md)
- [API Reference](docs/api-reference.md)
- [Advanced Features](docs/advanced-features.md)
- [Media Processing](docs/media-processing.md)
- [Migration Guide](docs/migration-guide.md)
- [Troubleshooting](docs/troubleshooting.md)

## Contributing

Contributions are welcome! Please ensure:
- Code follows Go conventions
- Tests pass
- Documentation is updated

## License
Author: Alexey Sokolov
Apache License 2.0. See [LICENSE](LICENSE)

