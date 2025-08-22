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
go get github.com/livekit/agent-sdk-go
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
    // Create a simple job handler
    handler := &agent.JobHandlerFunc{
        OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
            log.Printf("Agent joined room: %s (Job ID: %s)", room.Name(), job.Id)
            
            // Set up room event handlers
            room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
                log.Printf("Participant connected: %s", p.Identity())
            }
            
            room.Callback.OnParticipantDisconnected = func(p *lksdk.RemoteParticipant) {
                log.Printf("Participant disconnected: %s", p.Identity())
            }
            
            // Keep the agent in the room
            <-ctx.Done()
            log.Printf("Agent leaving room: %s", room.Name())
            return nil
        },
    }
    
    // Create worker with configuration
    worker := agent.NewWorker(
        "ws://localhost:7880",  // Your LiveKit server URL
        "your-api-key",         // Your API key
        "your-api-secret",      // Your API secret
        handler,
        agent.WorkerOptions{
            AgentName: "my-first-agent",
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
worker := agent.NewWorker(serverURL, apiKey, apiSecret, handler, agent.WorkerOptions{
    AgentName:       "my-agent",           // Unique name for your agent
    MaxConcurrentJobs: 5,                  // Limit concurrent jobs
    JobTypes:        []livekit.JobType{    // Specify which job types to accept
        livekit.JobType_JT_ROOM,
        livekit.JobType_JT_PUBLISHER,
    },
    Namespace:       "production",         // Namespace for multi-tenant setups
    Version:         "1.0.0",              // Agent version
})
```

## Creating Different Agent Types

### Room Agent Example

A room agent that monitors room activity:

```go
handler := &agent.JobHandlerFunc{
    OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
        log.Printf("Monitoring room: %s", room.Name())
        
        // Track participants
        participants := make(map[string]*lksdk.RemoteParticipant)
        
        room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
            participants[p.SID()] = p
            log.Printf("Participant joined: %s (total: %d)", p.Identity(), len(participants))
        }
        
        room.Callback.OnParticipantDisconnected = func(p *lksdk.RemoteParticipant) {
            delete(participants, p.SID())
            log.Printf("Participant left: %s (remaining: %d)", p.Identity(), len(participants))
        }
        
        room.Callback.OnTrackPublished = func(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
            log.Printf("Track published: %s by %s", publication.SID(), rp.Identity())
        }
        
        // Stay in room until context is cancelled
        <-ctx.Done()
        return nil
    },
}
```

### Publisher Agent Example

An agent that publishes audio to a room:

```go
handler := &agent.JobHandlerFunc{
    OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
        // Only handle publisher jobs
        if job.Type != livekit.JobType_JT_PUBLISHER {
            return nil
        }
        
        log.Printf("Publishing to room: %s", room.Name())
        
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
        publication, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
            Name: "Agent Audio",
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
                }
            }
        }()
        
        <-ctx.Done()
        return nil
    },
}
```

## Connecting to LiveKit Server

### Local Development

For local development with a LiveKit server running on your machine:

```go
worker := agent.NewWorker(
    "ws://localhost:7880",
    "devkey",  // Default development key
    "secret",  // Default development secret
    handler,
    agent.WorkerOptions{AgentName: "dev-agent"},
)
```

### Production Setup

For production, use secure WebSocket and proper credentials:

```go
worker := agent.NewWorker(
    "wss://your-livekit-server.com",
    os.Getenv("LIVEKIT_API_KEY"),
    os.Getenv("LIVEKIT_API_SECRET"),
    handler,
    agent.WorkerOptions{
        AgentName: "production-agent",
        MaxConcurrentJobs: 10,
    },
)
```

## Error Handling

Always implement proper error handling in your job handler:

```go
handler := &agent.JobHandlerFunc{
    OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
        // Wrap operations that might fail
        if err := riskyOperation(); err != nil {
            // Log the error
            log.Printf("Error in job %s: %v", job.Id, err)
            
            // Decide whether to retry or fail the job
            if isRetryable(err) {
                return fmt.Errorf("retryable error: %w", err)
            }
            
            // Non-retryable errors should still be returned
            return fmt.Errorf("fatal error: %w", err)
        }
        
        return nil
    },
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
    
    worker := agent.NewWorker(/* ... */)
    
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
        
        worker.Stop(shutdownCtx)
        
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
    
    worker := agent.NewWorker(
        getEnvOrDefault("LIVEKIT_URL", "ws://localhost:7880"),
        os.Getenv("LIVEKIT_API_KEY"),
        os.Getenv("LIVEKIT_API_SECRET"),
        handler,
        agent.WorkerOptions{
            AgentName: getEnvOrDefault("AGENT_NAME", "agent"),
            MaxConcurrentJobs: getIntEnv("MAX_CONCURRENT_JOBS", 5),
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

handler := &agent.JobHandlerFunc{
    OnJob: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
        log := logger.GetLogger().WithValues(
            "jobID", job.Id,
            "roomName", room.Name(),
            "jobType", job.Type.String(),
        )
        
        log.Infow("Processing job")
        
        // Your agent logic here
        
        log.Infow("Job completed successfully")
        return nil
    },
}
```

### Health Checks

Implement health checks for monitoring:

```go
import "net/http"

func startHealthServer(worker *agent.Worker) {
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
- Open an issue on [GitHub](https://github.com/livekit/agent-sdk-go)