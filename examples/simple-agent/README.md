# Simple Agent Example

This example demonstrates a basic LiveKit agent that:
- Connects to a LiveKit server
- Registers as a room agent
- Sends periodic messages when assigned to a room

## Running the Agent

```bash
go run main.go --api-key devkey --api-secret secret
```

## Testing the Agent

The agent will connect and wait for job assignments. To trigger agent jobs, the LiveKit server must be configured with agent dispatch rules.

### Server Configuration

For the agent to receive jobs, the LiveKit server needs to be configured with agent dispatch. Add this to your `livekit.yaml`:

```yaml
agent_dispatch:
  enabled: true
  default_agent_name: simple-agent
  agent_namespace: default
```

### Triggering Jobs

Once the server is configured, agents will be triggered when:
- A new room is created (for JT_ROOM agents)
- A participant starts publishing (for JT_PUBLISHER agents)
- A participant joins (for JT_PARTICIPANT agents)

## Output

When running, you'll see:
1. Worker registration confirmation
2. Job request notifications when rooms are created
3. Periodic messages sent to the room while the job is active

## Notes

- The agent uses the SimpleJobHandler for easy implementation
- It demonstrates basic lifecycle: registration, job handling, and cleanup
- The agent will automatically reconnect if the connection is lost