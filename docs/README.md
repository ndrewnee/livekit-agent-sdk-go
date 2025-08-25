# LiveKit Agent SDK Documentation

Welcome to the comprehensive documentation for the LiveKit Agent SDK for Go. This SDK enables you to build server-side agents that can interact with LiveKit rooms, process media, and handle various real-time communication scenarios.

## 📚 Documentation Structure

### Getting Started
- **[Quick Start Guide](getting-started.md)** - Installation, setup, and your first agent
- **[Core Concepts](concepts.md)** - Understanding agents, workers, and jobs
- **[Agent Types](agent-types.md)** - Room, Participant, and Publisher agents explained

### Development Guide
- **[Job Handling](job-handling.md)** - Implementing job handlers and managing job lifecycle
- **[Advanced Features](advanced-features.md)** - Load balancing, job recovery, resource management
- **[Media Processing](media-processing.md)** - Working with media pipelines and quality control

### Reference
- **[API Reference](api-reference.md)** - Complete API documentation
- **[Troubleshooting](troubleshooting.md)** - Common issues and solutions
- **[Migration Guide](migration-guide.md)** - Upgrading from previous versions

## 🚀 Quick Links

- [LiveKit Server](https://github.com/livekit/livekit) - The LiveKit server implementation
- [LiveKit Protocol](https://github.com/livekit/protocol) - Protocol definitions
- [LiveKit Docs](https://docs.livekit.io) - Official LiveKit documentation
- [Community Slack](https://livekit.io/slack) - Get help and connect with other developers

## 📖 How to Use This Documentation

1. **New to LiveKit Agents?** Start with the [Getting Started Guide](getting-started.md) and [Core Concepts](concepts.md)
2. **Building your first agent?** Check out the [Examples](examples/README.md) for complete working code
3. **Need specific features?** Browse the development guides for detailed explanations
4. **Looking for API details?** See the [API Reference](api-reference.md)

## 🔍 What You'll Learn

- How to create and deploy LiveKit agents
- Different agent types and when to use each
- Handling jobs and managing agent lifecycle
- Processing media streams and controlling quality
- Building scalable, production-ready agent deployments
- Advanced features like load balancing and job recovery

## 📝 Code Examples

Throughout this documentation, you'll find practical code examples that you can copy and adapt:

```go
// Example: Creating a simple room agent with UniversalWorker
package main

import (
    "context"
    "log"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
)

func main() {
    handler := &agent.SimpleUniversalHandler{
        JobAssignedFunc: func(ctx context.Context, jobCtx *agent.JobContext) error {
            log.Printf("Handling job %s for room %s", jobCtx.Job.Id, jobCtx.Room.Name())
            // Your agent logic here
            return nil
        },
    }
    
    worker := agent.NewUniversalWorker("ws://localhost:7880", "api-key", "api-secret", handler, agent.WorkerOptions{
        AgentName: "my-agent",
        JobType:   livekit.JobType_JT_ROOM,
    })
    
    if err := worker.Start(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

## 🤝 Contributing

Found an issue or want to contribute? Please check our [GitHub repository](https://github.com/am-sokolov/livekit-agent-sdk-go) for:
- Filing issues
- Submitting pull requests
- Viewing the source code

## 📄 License

The LiveKit Agent SDK is licensed under the Apache License 2.0. See the [LICENSE](https://github.com/am-sokolov/livekit-agent-sdk-go/blob/main/LICENSE) file for details.