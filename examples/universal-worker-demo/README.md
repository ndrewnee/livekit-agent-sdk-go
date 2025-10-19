# Universal Worker Demo

This example demonstrates the UniversalWorker, a unified agent that combines all capabilities of Worker, ParticipantAgent, and PublisherAgent into a single, flexible interface.

## Features Demonstrated

### Core Capabilities
- ✅ WebSocket connection management
- ✅ Job assignment handling (ROOM, PARTICIPANT, PUBLISHER)
- ✅ Load balancing and reporting
- ✅ Graceful shutdown with hooks
- ✅ Status updates and health monitoring
- ✅ Authentication and reconnection

### Advanced Features
- ✅ Job queueing with priority
- ✅ Resource pooling (configurable)
- ✅ Job recovery after disconnection
- ✅ CPU/Memory load tracking
- ✅ Resource monitoring and limits
- ✅ Custom message handlers

### Participant Management
- ✅ Track all participants
- ✅ Handle join/leave events
- ✅ Metadata change tracking
- ✅ Target participant tracking (for PARTICIPANT jobs)
- ✅ Data messaging

### Media Handling
- ✅ Track publication events
- ✅ Track subscription management
- ✅ Quality control settings
- ✅ Connection quality monitoring

## Running the Demo

### Prerequisites
1. LiveKit server running locally or accessible
2. Go 1.21 or later

### Quick Start

```bash
# Set environment variables
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"

# Run the demo
go run main.go

# Or with custom options
go run main.go -agent-name="my-universal-agent" -max-jobs=10
```

### Command Line Options

- `-url`: LiveKit server URL (default: $LIVEKIT_URL)
- `-api-key`: LiveKit API key (default: $LIVEKIT_API_KEY)
- `-api-secret`: LiveKit API secret (default: $LIVEKIT_API_SECRET)
- `-agent-name`: Agent identifier (default: "universal-demo")
- `-namespace`: Agent namespace for multi-tenancy (default: "")
- `-max-jobs`: Maximum concurrent jobs (default: 5)

## Testing the Agent

### Test with Room Job
```bash
# Create a room and the agent will automatically join
livekit-cli create-room --name test-room --empty
```

### Test with Participant Job
```bash
# Dispatch a participant job
livekit-cli dispatch-agent \
  --agent-name universal-demo \
  --room test-room \
  --participant test-user
```

### Test with Publisher Job
```bash
# The agent will handle publisher events when participants publish media
livekit-cli join-room --room test-room --identity publisher --publish
```

## Output Example

```
🚀 Starting Universal Worker Demo
   Server: ws://localhost:7880
   Agent: universal-demo
   Max jobs: 5
📊 Worker capabilities:
   ✓ Job queueing enabled: 10 queue size
   ✓ CPU/Memory load tracking: true
   ✓ Job recovery: true
   ✓ Resource monitoring: true
   Health: map[status:available active_jobs:0 max_jobs:5]
   Metrics: map[jobs_accepted:0 jobs_completed:0 jobs_failed:0]
✅ Worker started, waiting for jobs...
📥 Job request received: ID=job-123 Type=JT_ROOM Room=test-room
🚀 Job assigned: ID=job-123 Room=test-room
   Type: ROOM job - Agent joins when room is created
   Room connected: test-room
   Local participant: universal-agent
   Remote participants: 0
   📤 Sent greeting message to room
👤 Participant joined: user-1 (Test User)
🎥 Track published by user-1: TR_video (kind: video)
📺 Subscribed to track from user-1: TR_video
📨 Data received from user-1: Hello agent!
👤 Participant left: user-1
✅ Job completed: ID=job-123
🛑 Job terminated: ID=job-123
```

## Architecture Benefits

The UniversalWorker provides:

1. **Unified Interface**: Single worker type handles all job types
2. **Simplified Development**: No need to choose between Worker, ParticipantAgent, or PublisherAgent
3. **Feature Complete**: All capabilities from deprecated workers in one place
4. **Backward Compatible**: Works with existing LiveKit infrastructure
5. **Future Proof**: Easy to extend with new capabilities

## Comparison with Deprecated Workers

| Feature | Worker | ParticipantAgent | PublisherAgent | UniversalWorker |
|---------|--------|-----------------|----------------|-----------------|
| Job handling | ✅ | ✅ | ✅ | ✅ |
| Load balancing | ✅ | ❌ | ❌ | ✅ |
| Participant tracking | ❌ | ✅ | ❌ | ✅ |
| Media control | ❌ | ❌ | ✅ | ✅ |
| Job queueing | ✅ | ❌ | ❌ | ✅ |
| Resource pooling | ✅ | ❌ | ❌ | ✅ |
| All job types | ❌ | ❌ | ❌ | ✅ |

## Next Steps

- Implement actual media processing in publisher jobs
- Add ML model integration for AI agents
- Implement custom resource pooling for expensive operations
- Add metrics exporters for monitoring systems