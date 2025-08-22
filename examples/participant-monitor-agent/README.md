# Participant Monitor Agent Example

This example demonstrates a comprehensive participant monitoring agent using the LiveKit Agent SDK's Participant Agent features.

## Features

The agent monitors and manages participants in a LiveKit room with:

- **Real-time Participant Tracking**: Monitors join/leave events, metadata changes, permission updates
- **Permission Management**: Role-based permissions (presenter vs viewer)
- **Activity Coordination**: Groups participants, tracks speakers, coordinates interactions
- **Event Processing**: Batches and processes events with custom handlers
- **Data Messaging**: Responds to participant data messages
- **Status Reporting**: Periodic reports on room state and metrics

## Prerequisites

- Go 1.21 or higher
- LiveKit server running (local or cloud)
- LiveKit CLI (optional, for testing)

## Quick Start

### 1. Start LiveKit Server (Dev Mode)

```bash
docker run -d \
  -p 7880:7880 \
  -p 7881:7881 \
  -p 7882:7882/udp \
  livekit/livekit-server \
  --dev
```

### 2. Run the Agent

```bash
go run main.go
```

Or with custom settings:

```bash
go run main.go \
  --url http://localhost:7880 \
  --api-key devkey \
  --api-secret secret \
  --agent-name participant-monitor
```

### 3. Test the Agent

Use the provided test script:

```bash
./test_agent.sh
```

Or manually:

1. Create a room with agent dispatch
2. Join as participants
3. Observe agent logs

## Agent Behavior

### On Participant Join
- Logs participant details
- Checks metadata for role assignment
- Adds to appropriate groups (presenters, viewers)
- Updates permissions based on role

### On Track Published
- Logs track details (kind, source, SID)
- Detects screen sharing and promotes to presenter
- Tracks active media streams

### On Data Received
- Logs message content
- Sends echo response to confirm receipt
- Can trigger custom actions based on message

### Permission Management
- **Presenters**: Can publish, subscribe, send data, update metadata
- **Viewers**: Can only subscribe and send data
- Automatically assigns roles based on metadata

### Event Processing
- Batches events for analytics (10 events or 5 seconds)
- Custom handlers for specific event types
- Filters and processes events asynchronously

### Status Reports (Every 30 seconds)
- Total participant count
- Individual participant details
- Group memberships
- Event processing metrics
- Activity statistics

## Example Output

```
🚀 Starting Participant Monitor Agent
   Server: http://localhost:7880
   Agent: participant-monitor
✅ Agent started and waiting for jobs...

📋 Job assigned - Room: test-room, Job ID: job_123
👥 Monitoring all participants in room

✅ Participant joined: presenter-1 (Main Presenter) - Metadata: presenter
✅ Participant joined: viewer-1 (Viewer One) - Metadata: viewer
📡 presenter-1 published video track (SID: TR_ABCD, Source: CAMERA)
🎤 presenter-1 started speaking
💬 Data from viewer-1 (RELIABLE): Hello from viewer

📊 === STATUS REPORT ===
Total participants: 2
  👤 presenter-1:
     - Joined: 2m30s ago
     - Tracks: 2
     - Speaking: true
     - Can Publish: true
  👤 viewer-1:
     - Joined: 1m45s ago
     - Tracks: 0
     - Speaking: false
     - Can Publish: false

👥 Groups:
  - presenters: 1 members
    • presenter-1
  - active_speakers: 1 members
    • presenter-1

📈 Event Metrics:
  - Total Events: 45
  - Processed: 45
  - Dropped: 0

🎯 Activity Metrics:
  - Total Activities: 12
  - Active Participants: 2
======================
```

## Customization

### Add Custom Event Handlers

```go
processor.RegisterHandler(agent.EventTypeTrackPublished, func(event agent.ParticipantEvent) error {
    // Your custom logic here
    return nil
})
```

### Add Permission Policies

```go
// Time-based policy
timePolicy := agent.NewTimeBasedPolicy(9, 17) // 9 AM to 5 PM
permManager.AddPolicy(timePolicy)
```

### Add Coordination Rules

```go
coordinator.AddCoordinationRule(agent.CoordinationRule{
    ID: "auto-mute",
    Condition: func(activity agent.ParticipantActivity) bool {
        return activity.Type == agent.ActivityTypeTrackPublished &&
               activity.Participant.Metadata() == "guest"
    },
    Action: func(activity agent.ParticipantActivity) {
        // Request mute for guest publishers
    },
})
```

## Testing Scenarios

1. **Basic Monitoring**
   - Join with multiple participants
   - Watch join/leave events

2. **Permission Testing**
   - Join as presenter (metadata: "presenter")
   - Join as viewer (metadata: "viewer")
   - Try publishing tracks with each role

3. **Data Messaging**
   - Send data messages from participants
   - Observe echo responses

4. **Screen Share Detection**
   - Start screen sharing
   - Watch automatic promotion to presenter group

5. **High Activity**
   - Rapidly join/leave participants
   - Send many data messages
   - Observe batched event processing

## Architecture

```
ParticipantMonitorHandler
├── Permission Manager
│   ├── Role-based Policy
│   └── Custom Policies
├── Coordination Manager
│   ├── Groups (presenters, speakers)
│   └── Coordination Rules
├── Event Processor
│   ├── Event Handlers
│   └── Batch Processors
└── Status Reporter
    └── Periodic Metrics
```

## Notes

- The agent automatically reconnects on disconnection
- All participant data is stored in-memory (lost on restart)
- Suitable for rooms with up to 100 participants
- Can be extended with persistence for larger deployments