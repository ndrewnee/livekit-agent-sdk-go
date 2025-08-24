# Simple Room Agent Example

A basic LiveKit agent that monitors room activity, tracks participants, and collects analytics.

## Overview

This example demonstrates:
- Room-level job handling
- Participant tracking and event monitoring
- Basic analytics collection
- Graceful shutdown handling

## Prerequisites

- Go 1.21 or later
- LiveKit server running locally or accessible
- API key and secret for authentication

## Configuration

Set the following environment variables:

```bash
export LIVEKIT_URL="ws://localhost:7880"      # Your LiveKit server URL
export LIVEKIT_API_KEY="your-api-key"          # Your API key
export LIVEKIT_API_SECRET="your-api-secret"    # Your API secret
```

## Running the Example

### Quick Start

Use the provided demo scripts for the easiest setup:

```bash
# Run the complete demo (recommended)
./run_full_demo.sh

# Or run a simpler demo
./run_demo.sh
```

### Manual Setup

1. Start a local LiveKit server in development mode:
```bash
docker run --rm -p 7880:7880 \
  livekit/livekit-server \
  --dev \
  --bind 0.0.0.0
```

2. Run the agent:
```bash
go run .
```

3. Create a room with agent dispatch (important!):
```bash
# Use the dispatch script
./dispatch_job.sh

# Or create manually with Go
go run test_with_dispatch.go
```

The room MUST be created with agent dispatch configuration for the agent to receive jobs. Regular rooms created without dispatch will not trigger the agent.

## Features

### Room Event Monitoring
- Participant connected/disconnected
- Track published/unpublished
- Data messages received
- Room metadata updates

### Analytics Collection
- Total participants count
- Peak concurrent participants
- Track statistics (audio/video)
- Session durations
- Periodic status reports

### Example Output

```
2024/01/15 10:30:00 Starting Simple Room Agent
2024/01/15 10:30:00 Configuration loaded - URL: ws://localhost:7880
2024/01/15 10:30:00 Worker registered with LiveKit server
2024/01/15 10:30:05 Job request received for room RM_ABC123
2024/01/15 10:30:05 Accepting job - Identity: room-analytics-agent
2024/01/15 10:30:05 Room session started - Room: RM_ABC123, Name: test-room
2024/01/15 10:30:10 Participant connected: user1 (PA_xyz789)
2024/01/15 10:30:12 Track published: TR_audio123 (audio) by user1
2024/01/15 10:30:35 Room Status - SID: RM_ABC123, Participants: 1, Audio: 1, Video: 0, Duration: 30s
2024/01/15 10:31:35 Analytics Report - Total: 1, Peak: 1, Active: 1, Disconnected: 0
```

## Code Structure

- `main.go` - Entry point and configuration
- `handler.go` - Room job handler and event processing
- `analytics.go` - Analytics collection and reporting

## Extending the Example

You can extend this example by:

1. **Adding persistence**: Store analytics in a database
2. **Webhooks**: Send events to external services
3. **Room management**: Implement participant limits or moderation
4. **Media recording**: Trigger recording based on events
5. **Custom analytics**: Track specific application metrics

## Troubleshooting

### No jobs received
- Ensure the LiveKit server is running
- Verify API credentials are correct
- **Make sure rooms are created with agent dispatch configuration**
- Use the provided dispatch scripts or test_with_dispatch.go
- Regular rooms without agent dispatch will not trigger jobs

### Connection errors
- Verify the LIVEKIT_URL is accessible
- Check firewall settings
- Ensure the server is configured to accept agent connections

## Next Steps

- Try the [Participant Monitoring](../participant-monitoring-agent) example for participant-level analytics
- Explore the [Media Publisher](../media-publisher-agent) example for media streaming
- Read the main [LiveKit Agent SDK documentation](../../docs/README.md)