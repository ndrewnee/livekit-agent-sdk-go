# LiveKit Agent SDK Examples

This directory contains complete, runnable examples demonstrating various features and use cases of the LiveKit Agent SDK.

## Available Examples

### Basic Examples

1. **[Simple Room Agent](simple-room-agent.md)**
   - Basic room monitoring and event handling
   - Participant tracking
   - Simple analytics collection
   - Perfect starting point for new developers

2. **[Participant Monitoring](participant-monitoring.md)**
   - Individual participant tracking
   - Connection quality monitoring
   - Speaking detection
   - Activity logging

3. **[Media Publisher](media-publisher.md)**
   - Publishing audio/video to rooms
   - Text-to-speech integration
   - Audio file playback
   - Dynamic media generation

### Advanced Examples

4. **[Load-Balanced Workers](load-balanced-workers.md)**
   - Multi-worker deployment
   - Custom load calculation
   - Auto-scaling implementation
   - High availability setup

## Running the Examples

### Prerequisites

1. Go 1.21 or later installed
2. LiveKit server running (local or cloud)
3. API key and secret configured

### Basic Setup

1. Clone the repository:
   ```bash
   git clone https://github.com/livekit/agent-sdk-go
   cd agent-sdk-go/docs/examples
   ```

2. Install dependencies:
   ```bash
   go mod download
   ```

3. Set environment variables:
   ```bash
   export LIVEKIT_URL="ws://localhost:7880"  # Your LiveKit server
   export LIVEKIT_API_KEY="your-api-key"
   export LIVEKIT_API_SECRET="your-api-secret"
   ```

4. Run an example:
   ```bash
   go run simple-room-agent/main.go
   ```

## Example Structure

Each example follows a consistent structure:

```
example-name/
├── main.go           # Main entry point
├── handler.go        # Job handler implementation
├── config.go         # Configuration structures
├── README.md         # Detailed documentation
└── go.mod           # Dependencies
```

## Common Patterns

### Error Handling

All examples demonstrate proper error handling:

```go
if err := worker.Start(ctx); err != nil {
    log.Fatal("Failed to start worker:", err)
}
```

### Graceful Shutdown

Examples include signal handling for clean shutdown:

```go
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

select {
case <-sigChan:
    log.Println("Shutting down...")
    cancel()
case err := <-errChan:
    log.Fatal("Worker error:", err)
}
```

### Configuration

Examples use environment variables for configuration:

```go
config := &Config{
    URL:       getEnv("LIVEKIT_URL", "ws://localhost:7880"),
    APIKey:    mustGetEnv("LIVEKIT_API_KEY"),
    APISecret: mustGetEnv("LIVEKIT_API_SECRET"),
}
```

## Building Production Agents

When adapting these examples for production use:

### 1. Add Monitoring

```go
// Prometheus metrics
metrics := NewMetricsCollector()
handler := &MetricsHandler{
    base: handler,
    metrics: metrics,
}
```

### 2. Implement Health Checks

```go
http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
    if worker.IsHealthy() {
        w.WriteHeader(http.StatusOK)
    } else {
        w.WriteHeader(http.StatusServiceUnavailable)
    }
})
```

### 3. Add Structured Logging

```go
logger := logger.GetLogger().WithValues(
    "service", "my-agent",
    "version", version,
)
```

### 4. Configure Resource Limits

```go
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    MaxConcurrentJobs: 10,
    ResourceLimiter: agent.NewResourceLimiter(agent.ResourceLimits{
        MaxMemoryMB: 2048,
        MaxCPUPercent: 80,
    }),
})
```

### 5. Enable Recovery

```go
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    EnableJobRecovery: true,
    RecoveryHandler: &CustomRecoveryHandler{},
})
```

## Testing Examples

Each example includes test scenarios:

### Unit Tests

```bash
go test ./simple-room-agent/...
```

### Integration Tests

```bash
# Start test LiveKit server
docker run --rm -p 7880:7880 livekit/livekit-server --dev

# Run integration tests
go test ./... -tags=integration
```

### Load Testing

```go
// Simulate multiple concurrent jobs
for i := 0; i < 100; i++ {
    go simulateJob(i)
}
```

## Debugging Tips

### Enable Debug Logging

```go
logger.InitializeLogger("debug", "development")
```

### Use Delve Debugger

```bash
dlv debug simple-room-agent/main.go
```

### Monitor WebSocket Traffic

```go
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    WebSocketDebug: true,
})
```

## Contributing Examples

To add a new example:

1. Create a new directory with descriptive name
2. Implement the example following the structure above
3. Include comprehensive README with:
   - What the example demonstrates
   - Key concepts illustrated
   - Configuration options
   - Expected output
4. Add tests for the example
5. Update this README to include your example

## Example Categories

### By Complexity

- **Beginner**: Simple room agent, basic publisher
- **Intermediate**: Participant monitoring, media processing
- **Advanced**: Load balancing, distributed agents

### By Use Case

- **Analytics**: Room statistics, participant metrics
- **Communication**: Chat bots, notification agents
- **Media**: Recording, transcription, translation
- **Moderation**: Content filtering, participant management

### By Features

- **WebRTC**: Track subscription, quality control
- **Scaling**: Load balancing, auto-scaling
- **Recovery**: Job recovery, state persistence
- **Integration**: External services, databases

## Troubleshooting Examples

### Connection Issues

```
Error: failed to connect to LiveKit server
```
- Verify LIVEKIT_URL is correct
- Check API credentials
- Ensure server is running

### No Jobs Received

```
Worker connected but not receiving jobs
```
- Verify agent namespace matches
- Check job type configuration
- Ensure rooms are being created

### Performance Problems

```
High CPU/memory usage
```
- Implement resource limits
- Add job throttling
- Optimize media processing

## Next Steps

1. Start with the [Simple Room Agent](simple-room-agent.md)
2. Progress to more complex examples
3. Combine patterns from multiple examples
4. Build your custom agent

## Resources

- [LiveKit Documentation](https://docs.livekit.io)
- [Agent SDK API Reference](../api-reference.md)
- [Troubleshooting Guide](../troubleshooting.md)
- [Community Slack](https://livekit.io/slack)