# LiveKit Agent SDK Go Examples

This directory contains complete, runnable examples demonstrating various features and use cases of the LiveKit Agent SDK for Go.

## Quick Start

All examples are designed to work with a local LiveKit server running in development mode.

### Prerequisites

1. **Go 1.21+** installed
2. **Docker** (for running LiveKit server)
3. **LiveKit CLI** (optional, for testing)
   ```bash
   go install github.com/livekit/livekit-cli@latest
   ```

### Running LiveKit Server Locally

Start a local LiveKit server in development mode:

```bash
docker run --rm -p 7880:7880 \
  livekit/livekit-server \
  --dev \
  --bind 0.0.0.0
```

This provides:
- WebSocket URL: `ws://localhost:7880`
- API Key: `devkey`
- API Secret: `secret`

## Available Examples

### 1. [Simple Room Agent](simple-room-agent/)
**Perfect for beginners** - A basic agent that monitors room activity and collects analytics.

```bash
cd simple-room-agent
./run_full_demo.sh  # Easiest way to run the complete demo

# Or manually:
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
go run .
```

**Features:**
- Room event monitoring
- Participant tracking
- Basic analytics collection
- Graceful shutdown

### 2. [Participant Monitoring Agent](participant-monitoring-agent/)
An agent that provides personalized monitoring for individual participants.

```bash
cd participant-monitoring-agent
./run_demo.sh  # Run the demo with participant dispatch

# Or manually:
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
go run .
```

**Features:**
- Individual participant tracking
- Audio level monitoring & speaking detection
- Connection quality tracking
- Personalized notifications

### 3. [Media Publisher Agent](media-publisher-agent/)
Demonstrates publishing audio and video content to LiveKit rooms.

```bash
cd media-publisher-agent
./run_demo.sh  # Run the demo with publisher dispatch

# Or manually:
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
go run .
```

**Features:**
- Audio tone generation
- Video pattern generation
- Interactive controls via data messages
- Multiple publishing modes


## Example Structure

Each example follows a consistent structure:

```
example-name/
├── main.go           # Entry point
├── handler.go        # Job handler implementation
├── *.go             # Additional source files
├── go.mod           # Module dependencies
├── README.md        # Detailed documentation
└── test_agent.sh    # Test script
```

## Important: Agent Dispatch

**Agents only receive jobs when rooms are created with agent dispatch configuration.** Each example includes dispatch scripts that properly configure rooms:

- `dispatch_job.sh` - For room agents
- `dispatch_participant_job.sh` - For participant agents  
- `dispatch_publisher_job.sh` - For publisher agents

The demo scripts (`run_demo.sh`, `run_full_demo.sh`) handle this automatically.

## Common Patterns

### Environment Configuration

All examples use environment variables for configuration:

```bash
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
```

### Job Types

Different agents handle different job types:

- **Room Agent**: `JobType_JT_ROOM` - One job per room
- **Participant Agent**: `JobType_JT_PARTICIPANT` - One job per participant
- **Publisher Agent**: `JobType_JT_PUBLISHER` - Publishing jobs

### Graceful Shutdown

All examples implement proper signal handling:

```go
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
```

## Development Tips

### 1. Enable Debug Logging

```bash
export LOG_LEVEL=debug
```

### 2. Use LiveKit CLI for Testing

Create rooms and tokens:
```bash
# Create a room
livekit-cli create-room --api-key devkey --api-secret secret --url ws://localhost:7880 test-room

# Generate token
livekit-cli create-token --api-key devkey --api-secret secret --room test-room --identity user1
```

### 3. Monitor Agent Logs

Agents log important events and metrics. Watch the console output to understand behavior.

### 4. Test with LiveKit Meet

Connect to your test room using [LiveKit Meet](https://meet.livekit.io) with generated tokens.

## Building Your Own Agent

1. **Start Simple**: Begin with the simple-room-agent example
2. **Choose Job Type**: Decide if you need room-level or participant-level processing
3. **Implement Handler**: Create your job handler with needed callbacks
4. **Add Features**: Incrementally add functionality
5. **Test Locally**: Use the local development setup
6. **Deploy**: Use the load-balanced example for production deployment

## Troubleshooting

### Agent Not Receiving Jobs

1. **Most Common**: Ensure rooms are created with agent dispatch configuration
   - Use the provided dispatch scripts
   - Regular rooms without agent dispatch won't trigger jobs
2. Check LiveKit server is running
3. Verify API credentials match
4. Ensure agent name in dispatch matches agent configuration
5. Check agent namespace if using namespaces

### Connection Errors

1. Verify LIVEKIT_URL is accessible
2. Check firewall/network settings
3. Ensure WebSocket connections are allowed

### Performance Issues

1. Monitor CPU/memory usage
2. Implement resource limits
3. Use load balancing for scaling

## Resources

- [LiveKit Documentation](https://docs.livekit.io)
- [Agent SDK Documentation](../docs/README.md)
- [LiveKit Community Slack](https://livekit.io/slack)

## Contributing

Feel free to submit issues, fork the repository, and create pull requests for any improvements.