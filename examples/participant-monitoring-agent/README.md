# Participant Monitoring Agent Example

A specialized participant agent that provides personalized monitoring, connection quality tracking, and intelligent assistance for individual participants. This example demonstrates participant-focused job handling and stateful interactions.

## Overview

This agent monitors individual participants in LiveKit rooms, tracking:
- Audio levels and speaking detection
- Video frame rates and activity
- Connection quality changes
- Session statistics and duration
- Real-time notifications

## Features

- **Participant-specific job handling** (JT_PARTICIPANT)
- **Real-time audio analysis** with speaking detection
- **Video quality monitoring** with frame rate tracking
- **Connection quality tracking** and alerts
- **Personalized notifications** to participants
- **Stateful session management** per participant
- **Configurable thresholds** for detection

## Prerequisites

- Go 1.18 or later
- LiveKit server running (locally or cloud)
- Valid LiveKit API credentials

## Configuration

The agent uses environment variables for configuration:

```bash
# Required
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="your-api-key"
export LIVEKIT_API_SECRET="your-api-secret"

# Optional (with defaults)
export CONNECTION_QUALITY_THRESHOLD="0.5"    # Quality threshold (0-1)
export INACTIVITY_TIMEOUT="5m"              # Timeout for participant connection
export SPEAKING_THRESHOLD="0.01"            # Audio level threshold for speaking
export ENABLE_NOTIFICATIONS="true"          # Send notifications to participants
```

## Running the Agent

### Quick Start

Use the provided demo script:

```bash
./run_demo.sh
```

### Manual Setup

1. Install dependencies:
   ```bash
   go mod download
   ```

2. Run the agent:
   ```bash
   go run .
   ```

3. Create a room and dispatch a participant job:
   ```bash
   # Use the dispatch script
   ./dispatch_participant_job.sh <participant-identity>
   
   # Or use the test dispatch program
   go run test_participant_dispatch.go
   ```

## Job Metadata

When dispatching a participant job, include metadata specifying the target participant:

```json
{
  "participant_identity": "user123",
  "monitoring_type": "full",
  "end_on_disconnect": false,
  "notification_preferences": {
    "speaking_alerts": true,
    "quality_alerts": true
  },
  "custom_settings": {
    "priority": "high"
  }
}
```

### Metadata Fields

- `participant_identity` (required): The identity of the participant to monitor
- `monitoring_type`: Level of monitoring ("basic", "full", "custom")
- `end_on_disconnect`: Whether to end the job when participant disconnects
- `notification_preferences`: Settings for notifications
- `custom_settings`: Additional custom configuration

## Testing

Use the included test script to simulate a participant monitoring scenario:

```bash
./test_agent.sh
```

This script will:
1. Start the monitoring agent
2. Create a test room
3. Dispatch a participant job
4. Simulate participant activity

## Architecture

The agent consists of several components:

### Main Components

1. **ParticipantMonitoringHandler**: Handles participant job lifecycle
   - Validates job type and metadata
   - Manages participant connection/disconnection
   - Coordinates monitoring activities

2. **ParticipantMonitor**: Central monitoring service
   - Tracks all active sessions
   - Performs periodic health checks
   - Generates monitoring reports

3. **ParticipantSession**: Individual session state
   - Tracks participant metrics
   - Manages speaking detection
   - Records quality changes

### Monitoring Flow

1. Agent receives JT_PARTICIPANT job
2. Creates monitoring session for target participant
3. Waits for participant to connect (or finds if already connected)
4. Subscribes to participant's audio/video tracks
5. Monitors tracks for:
   - Audio levels and speaking patterns
   - Video frame rates
   - Connection quality
6. Sends notifications based on events
7. Generates periodic reports
8. Ends session on disconnect or job completion

## Data Messages

The agent sends various data messages to participants:

### Welcome Message
```json
{
  "type": "participant_monitoring",
  "action": "welcome",
  "message": "Hello user123! Your connection is being monitored for optimal experience.",
  "features": ["connection_quality", "speaking_detection", "activity_tracking"]
}
```

### Speaking Notification
```json
{
  "type": "speaking_detection",
  "participant": "user123",
  "speaking": true,
  "timestamp": 1705353600
}
```

## Monitoring Metrics

The agent tracks various metrics per participant:

- **Audio Metrics**
  - Current audio level (0-1)
  - Speaking state (true/false)
  - Total speaking duration

- **Video Metrics**  
  - Frame rate (FPS)
  - Last frame timestamp
  - Total frames received

- **Session Metrics**
  - Connection status
  - Session duration
  - Active track count
  - Connection quality changes

## Use Cases

- **Customer Support**: Monitor agent performance and connection quality
- **Education**: Track student engagement in online classes
- **Healthcare**: Ensure quality during telemedicine sessions
- **Broadcasting**: Monitor presenter connection stability
- **Gaming**: Track player connection quality

## Extending the Example

You can extend this example to add:

- Machine learning for emotion/sentiment detection
- Bandwidth optimization based on quality
- External notification services (email, SMS)
- Real-time dashboards
- Analytics and insights
- Automatic quality adjustments

## Troubleshooting

### Agent not receiving jobs
- Ensure job type is set to `JT_PARTICIPANT` 
- **Rooms must be created with agent dispatch configuration**
- Verify participant identity in metadata matches actual participant
- Check LiveKit server connectivity
- Use the provided dispatch scripts to ensure proper job creation

### No audio/video monitoring
- Confirm tracks are published by participant
- Check subscription permissions
- Verify track monitoring goroutines

### High CPU usage
- Adjust monitoring intervals
- Optimize audio level calculations
- Reduce logging verbosity

## Next Steps

- See the [Simple Room Agent](../simple-room-agent) for basic agent setup
- Explore [Media Publisher Agent](../media-publisher-agent) for media generation
- Review [Advanced Features](../../docs/advanced-features.md) for production deployment