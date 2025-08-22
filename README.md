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
go get github.com/livekit/agent-sdk-go
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
    // Create a simple job handler
    handler := &agent.SimpleJobHandler{
        OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
            log.Printf("Agent joined room: %s", room.Name())
            
            // Your agent logic here
            <-ctx.Done()
            
            return nil
        },
    }
    
    // Create worker options
    opts := agent.WorkerOptions{
        AgentName: "my-agent",
        Version:   "1.0.0",
        JobType:   livekit.JobType_JT_ROOM,
    }
    
    // Create and start worker
    worker := agent.NewWorker("http://localhost:7880", "devkey", "secret", handler, opts)
    
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
Launched when a room is created. Has access to all room events and participants.

```go
opts := agent.WorkerOptions{
    JobType: livekit.JobType_JT_ROOM,
}
```

### Publisher Agent (`JT_PUBLISHER`)
Launched when a participant starts publishing media. Ideal for processing published tracks.

```go
opts := agent.WorkerOptions{
    JobType: livekit.JobType_JT_PUBLISHER,
}
```

### Participant Agent (`JT_PARTICIPANT`)
Launched when any participant joins. Can interact with specific participants.

```go
opts := agent.WorkerOptions{
    JobType: livekit.JobType_JT_PARTICIPANT,
}
```

## Advanced Usage

### Custom Job Handler

Implement the `JobHandler` interface for full control:

```go
type MyAgent struct{}

func (a *MyAgent) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    // Decide whether to accept the job
    return true, &agent.JobMetadata{
        ParticipantIdentity: "my-agent-" + job.Id,
        ParticipantName:     "My Agent",
        ParticipantMetadata: `{"version": "1.0"}`,
    }
}

func (a *MyAgent) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Handle the job
    return nil
}

func (a *MyAgent) OnJobTerminated(ctx context.Context, jobID string) {
    // Cleanup
}
```

### Worker Options

```go
opts := agent.WorkerOptions{
    AgentName:    "my-agent",           // Agent identifier
    Version:      "1.0.0",              // Agent version
    Namespace:    "production",         // For multi-tenant isolation
    JobType:      livekit.JobType_JT_ROOM,
    Permissions:  &livekit.ParticipantPermission{
        CanPublish:   true,
        CanSubscribe: true,
    },
    MaxJobs:      10,                   // Maximum concurrent jobs
    Logger:       customLogger,         // Custom logger
    PingInterval: 10 * time.Second,     // Keepalive interval
}
```

### Using Job Context

The SDK provides `JobContext` for easier job management:

```go
func handleJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    jobCtx := agent.NewJobContext(ctx, job, room)
    
    // Wait for a specific participant
    participant, err := jobCtx.WaitForParticipant("user123", 30*time.Second)
    if err != nil {
        return err
    }
    
    // Publish data to room
    err = jobCtx.PublishData([]byte("Hello from agent"), true, nil)
    
    // Sleep with cancellation support
    jobCtx.Sleep(5 * time.Second)
    
    return nil
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
   livekit-server --dev
   ```

2. Run your agent:
   ```bash
   go run main.go
   ```

3. Create a room with agent dispatch:
   ```bash
   go run examples/create-room-with-agent/main.go
   ```

4. The agent will now receive and process the job.

## Examples

### Simple Agent

See the [simple agent example](examples/simple-agent/main.go) for a basic implementation that:
- Joins rooms as an agent participant
- Sends periodic messages to the room
- Demonstrates basic agent lifecycle

```bash
cd examples/simple-agent
go run main.go --url http://localhost:7880 --api-key devkey --api-secret secret
```

### Transcription Agent

See the [transcription agent example](examples/transcription-agent/main.go) for a more complex implementation that:
- Connects to audio publishers
- Demonstrates publisher job handling
- Shows how to process media streams

## Architecture

The SDK implements the complete LiveKit Agent Protocol:

1. **WebSocket Connection**: Establishes secure connection to LiveKit server
2. **Worker Registration**: Registers capabilities and receives unique worker ID
3. **Job Discovery**: Receives job availability requests based on agent type
4. **Job Assignment**: Accepts jobs and receives authentication tokens
5. **Room Participation**: Joins rooms as a special participant
6. **Status Updates**: Reports job progress and worker load
7. **Graceful Termination**: Handles job cleanup and disconnection

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

## Contributing

Contributions are welcome! Please ensure:
- Code follows Go conventions
- Tests pass
- Documentation is updated

## License

This SDK follows the same license as the LiveKit server project.