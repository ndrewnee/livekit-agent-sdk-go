# Getting Started with LiveKit Agent SDK

This guide will help you get started with the LiveKit Agent SDK for Go. You'll learn how to install the SDK, create your first agent, and understand the basic concepts.

## Prerequisites

Before you begin, ensure you have:

- Go 1.21 or later installed
- A LiveKit server instance (local or cloud)
- API key and secret for your LiveKit server
- Basic familiarity with Go programming

## Installation

Install the LiveKit Agent SDK using Go modules:

```bash
go get github.com/am-sokolov/livekit-agent-sdk-go
```

This will also install the required dependencies:
- `github.com/livekit/protocol` - LiveKit protocol definitions
- `github.com/livekit/server-sdk-go/v2` - LiveKit Go SDK
- `github.com/pion/webrtc/v4` - WebRTC implementation

## Quick Start

### 1. Create Your First Agent

Here's a minimal example of a room agent that joins rooms and logs events:

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
    // Create a handler using SimpleUniversalHandler for convenience
    handler := &agent.SimpleUniversalHandler{
        JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
            // Accept all jobs and provide agent metadata
            return true, &agent.JobMetadata{
                ParticipantIdentity: "my-first-agent",
                ParticipantName:     "My First Agent",
                ParticipantMetadata: `{"version": "1.0"}`,
            }
        },
        
        JobAssignedFunc: func(ctx context.Context, jobCtx *agent.JobContext) error {
            log.Printf("Agent joined room: %s (Job ID: %s)", jobCtx.Room.Name(), jobCtx.Job.Id)
            
            // Keep the agent in the room
            <-ctx.Done()
            log.Printf("Agent leaving room: %s", jobCtx.Room.Name())
            return nil
        },
        
        JobTerminatedFunc: func(ctx context.Context, jobID string) {
            log.Printf("Job terminated: %s", jobID)
        },
        
        // Handle participant events directly
        ParticipantJoinedFunc: func(ctx context.Context, participant *lksdk.RemoteParticipant) {
            log.Printf("Participant joined: %s", participant.Identity())
        },
        
        ParticipantLeftFunc: func(ctx context.Context, participant *lksdk.RemoteParticipant) {
            log.Printf("Participant left: %s", participant.Identity())
        },
    }
    
    // Create UniversalWorker with configuration
    worker := agent.NewUniversalWorker(
        "ws://localhost:7880",  // Your LiveKit server URL
        "your-api-key",         // Your API key
        "your-api-secret",      // Your API secret
        handler,
        agent.WorkerOptions{
            AgentName: "my-first-agent",
            JobType:   livekit.JobType_JT_ROOM,  // Accept room jobs
            MaxJobs:   5,                         // Handle up to 5 concurrent jobs
        },
    )
    
    // Start the worker
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    // Handle shutdown gracefully
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    go func() {
        <-sigChan
        log.Println("Shutting down...")
        cancel()
    }()
    
    // Start the worker (blocks until context is cancelled)
    if err := worker.Start(ctx); err != nil {
        log.Fatal("Failed to start worker:", err)
    }
}
```

### 2. Run Your Agent

Save the code above to `main.go` and run:

```bash
go run main.go
```

Your agent is now connected to the LiveKit server and ready to receive job assignments!

## Understanding the Basics

### Workers and Jobs

The Agent SDK uses a worker-job model:

- **Worker**: A long-running process that connects to LiveKit and waits for job assignments
- **Job**: A task assigned by the server, typically to join a room and perform some action

### Job Types

LiveKit supports three types of agent jobs:

1. **Room Jobs (JT_ROOM)**: Join a room and process room-level events
2. **Participant Jobs (JT_PARTICIPANT)**: Handle specific participant interactions
3. **Publisher Jobs (JT_PUBLISHER)**: Publish media (audio/video) to a room

### Basic Worker Configuration

```go
worker := agent.NewUniversalWorker(serverURL, apiKey, apiSecret, handler, agent.WorkerOptions{
    AgentName:       "my-agent",           // Unique name for your agent
    JobType:         livekit.JobType_JT_ROOM,  // Single job type per worker
    MaxJobs:         5,                    // Maximum concurrent jobs
    Namespace:       "production",         // Namespace for multi-tenant setups
    Version:         "1.0.0",              // Agent version
})
```

## Creating Different Agent Types

### Room Agent Example

A room agent that monitors room activity:

```go
type RoomMonitorHandler struct {
    participants map[string]*lksdk.RemoteParticipant
    mu           sync.RWMutex
}

func (h *RoomMonitorHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    // Accept room jobs only
    if job.Type != livekit.JobType_JT_ROOM {
        return false, nil
    }
    return true, &agent.JobMetadata{
        ParticipantIdentity: "room-monitor",
        ParticipantName:     "Room Monitor Agent",
    }
}

func (h *RoomMonitorHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    log.Printf("Monitoring room: %s", jobCtx.Room.Name())
    
    // Initialize participants map
    h.participants = make(map[string]*lksdk.RemoteParticipant)
    
    // Monitor room using polling pattern
    go h.monitorRoom(ctx, jobCtx.Room)
    
    // Stay in room until context is cancelled
    <-ctx.Done()
    return nil
}

func (h *RoomMonitorHandler) OnJobTerminated(ctx context.Context, jobID string) {
    log.Printf("Room monitoring job terminated: %s", jobID)
}

func (h *RoomMonitorHandler) monitorRoom(ctx context.Context, room *lksdk.Room) {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            h.mu.Lock()
            // Check for new participants and track changes
            currentParticipants := room.GetRemoteParticipants()
            
            // Check for new participants
            for _, p := range currentParticipants {
                if _, exists := h.participants[p.SID()]; !exists {
                    h.participants[p.SID()] = p
                    log.Printf("Participant joined: %s (total: %d)", p.Identity(), len(h.participants))
                    
                    // Check their tracks
                    for _, pub := range p.TrackPublications() {
                        if remoteTrack := pub.TrackPublication().Track(); remoteTrack != nil {
                            log.Printf("Track published: %s by %s", pub.TrackPublication().SID(), p.Identity())
                        }
                    }
                }
            }
            
            // Check for disconnected participants
            for sid, p := range h.participants {
                found := false
                for _, current := range currentParticipants {
                    if current.SID() == sid {
                        found = true
                        break
                    }
                }
                if !found {
                    delete(h.participants, sid)
                    log.Printf("Participant left: %s (remaining: %d)", p.Identity(), len(h.participants))
                }
            }
            h.mu.Unlock()
        }
    }
}
```

### Publisher Agent Example

An agent that publishes audio to a room:

```go
type PublisherHandler struct{}

func (h *PublisherHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    // Only accept publisher jobs
    if job.Type != livekit.JobType_JT_PUBLISHER {
        return false, nil
    }
    return true, &agent.JobMetadata{
        ParticipantIdentity: "audio-publisher",
        ParticipantName:     "Audio Publisher Agent",
    }
}

func (h *PublisherHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    log.Printf("Publishing to room: %s", jobCtx.Room.Name())
    
    // Create an audio track (example: sine wave generator)
    track, err := webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
        "audio",
        "agent-audio",
    )
    if err != nil {
        return err
    }
    
    // Publish the track
    publication, err := jobCtx.Room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
        Name: "Agent Audio",
        Source: livekit.TrackSource_MICROPHONE,
    })
    if err != nil {
        return err
    }
    
    log.Printf("Published audio track: %s", publication.SID())
    
    // Generate and send audio samples
    go func() {
        ticker := time.NewTicker(20 * time.Millisecond)
        defer ticker.Stop()
        
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                // Generate audio samples here
                // This is where you'd integrate with TTS, audio files, etc.
                // Example: track.WriteSample(media.Sample{Data: audioData, Duration: 20 * time.Millisecond})
            }
        }
    }()
    
    <-ctx.Done()
    return nil
}

func (h *PublisherHandler) OnJobTerminated(ctx context.Context, jobID string) {
    log.Printf("Publisher job terminated: %s", jobID)
}
```

## Connecting to LiveKit Server

### Local Development

For local development with a LiveKit server running on your machine:

```go
worker := agent.NewUniversalWorker(
    "ws://localhost:7880",
    "devkey",  // Default development key
    "secret",  // Default development secret
    handler,
    agent.WorkerOptions{
        AgentName: "dev-agent",
        JobType:   livekit.JobType_JT_ROOM,
        MaxJobs:   5,
    },
)
```

### Production Setup

For production, use secure WebSocket and proper credentials:

```go
worker := agent.NewUniversalWorker(
    "wss://your-livekit-server.com",
    os.Getenv("LIVEKIT_API_KEY"),
    os.Getenv("LIVEKIT_API_SECRET"),
    handler,
    agent.WorkerOptions{
        AgentName: "production-agent",
        JobType:   livekit.JobType_JT_ROOM,
        MaxJobs:   10,
    },
)
```

## Error Handling

Always implement proper error handling in your job handler:

```go
type ErrorHandlingHandler struct{}

func (h *ErrorHandlingHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    // Validate job before accepting
    if !isValidJob(job) {
        log.Printf("Rejecting invalid job: %s", job.Id)
        return false, nil
    }
    
    return true, &agent.JobMetadata{
        ParticipantIdentity: "error-handling-agent",
        ParticipantName:     "Error Handling Agent",
    }
}

func (h *ErrorHandlingHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    // Wrap operations that might fail
    if err := riskyOperation(); err != nil {
        // Log the error
        log.Printf("Error in job %s: %v", jobCtx.Job.Id, err)
        
        // Decide whether to retry or fail the job
        if isRetryable(err) {
            return fmt.Errorf("retryable error: %w", err)
        }
        
        // Non-retryable errors should still be returned
        return fmt.Errorf("fatal error: %w", err)
    }
    
    return nil
}

func (h *ErrorHandlingHandler) OnJobTerminated(ctx context.Context, jobID string) {
    // Clean up any resources
    log.Printf("Cleaning up after job: %s", jobID)
}
```

## Graceful Shutdown

Implement graceful shutdown to ensure jobs complete properly:

```go
func main() {
    ctx, cancel := context.WithCancel(context.Background())
    
    // Set up signal handling
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    worker := agent.NewUniversalWorker(/* ... */)
    
    // Start worker in a goroutine
    errChan := make(chan error, 1)
    go func() {
        errChan <- worker.Start(ctx)
    }()
    
    // Wait for shutdown signal or error
    select {
    case sig := <-sigChan:
        log.Printf("Received signal: %v", sig)
        cancel()
        
        // Give worker time to clean up
        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer shutdownCancel()
        
        worker.Stop()
        
    case err := <-errChan:
        if err != nil {
            log.Fatal("Worker error:", err)
        }
    }
}
```

## Environment Variables

Common environment variables for agent configuration:

```bash
# LiveKit connection
export LIVEKIT_URL="wss://your-server.com"
export LIVEKIT_API_KEY="your-key"
export LIVEKIT_API_SECRET="your-secret"

# Agent configuration
export AGENT_NAME="my-agent"
export AGENT_NAMESPACE="default"
export MAX_CONCURRENT_JOBS="10"
export LOG_LEVEL="info"
```

Load them in your agent:

```go
import "github.com/joho/godotenv"

func main() {
    // Load .env file if it exists
    godotenv.Load()
    
    worker := agent.NewUniversalWorker(
        getEnvOrDefault("LIVEKIT_URL", "ws://localhost:7880"),
        os.Getenv("LIVEKIT_API_KEY"),
        os.Getenv("LIVEKIT_API_SECRET"),
        handler,
        agent.WorkerOptions{
            AgentName: getEnvOrDefault("AGENT_NAME", "agent"),
            JobType:   livekit.JobType_JT_ROOM,
            MaxJobs:   getIntEnv("MAX_JOBS", 5),
        },
    )
}
```

## Monitoring and Logging

### Structured Logging

Use structured logging for better observability:

```go
import "github.com/livekit/protocol/logger"

// Set up logger
logger.InitializeLogger("info", "production")

type LoggingHandler struct{}

func (h *LoggingHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    return true, &agent.JobMetadata{
        ParticipantIdentity: "logging-agent",
        ParticipantName:     "Logging Agent",
    }
}

func (h *LoggingHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    log := logger.GetLogger().WithValues(
        "jobID", jobCtx.Job.Id,
        "roomName", jobCtx.Room.Name(),
        "jobType", jobCtx.Job.Type.String(),
    )
    
    log.Infow("Processing job")
    
    // Your agent logic here
    
    log.Infow("Job completed successfully")
    return nil
}

func (h *LoggingHandler) OnJobTerminated(ctx context.Context, jobID string) {
    logger.GetLogger().Infow("Job terminated", "jobID", jobID)
}
```

### Health Checks

Implement health checks for monitoring:

```go
import "net/http"

func startHealthServer(worker *agent.UniversalWorker) {
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        status := worker.GetStatus()
        if status == agent.WorkerStatus_WS_AVAILABLE {
            w.WriteHeader(http.StatusOK)
            w.Write([]byte("OK"))
        } else {
            w.WriteHeader(http.StatusServiceUnavailable)
            w.Write([]byte("Unavailable"))
        }
    })
    
    http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
        // Export worker metrics
        metrics := worker.GetMetrics()
        json.NewEncoder(w).Encode(metrics)
    })
    
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

## Next Steps

Now that you have a basic agent running:

1. Learn about [Core Concepts](concepts.md) to understand the architecture
2. Explore different [Agent Types](agent-types.md) for your use case
3. Understand [Job Handling](job-handling.md) for complex workflows
4. Check out [Examples](examples/README.md) for complete implementations

## Troubleshooting

### Common Issues

1. **Connection refused**: Ensure LiveKit server is running and accessible
2. **Authentication failed**: Check API key and secret are correct
3. **No jobs received**: Verify agent namespace matches job dispatch configuration
4. **High memory usage**: Implement resource limits and job throttling

### Debug Mode

Enable debug logging for troubleshooting:

```go
logger.InitializeLogger("debug", "development")
```

### Getting Help

- Check the [Troubleshooting Guide](troubleshooting.md)
- Visit [LiveKit Docs](https://docs.livekit.io)
- Join the [LiveKit Community Slack](https://livekit.io/slack)
- Open an issue on [GitHub](https://github.com/am-sokolov/livekit-agent-sdk-go)