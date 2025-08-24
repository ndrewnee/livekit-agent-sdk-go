#!/bin/bash

# Test script to dispatch a job to the agent

echo "Dispatching test job to simple-room-agent..."

# Use lk CLI to dispatch a job
if command -v lk &> /dev/null; then
    LK_CMD="lk"
else
    LK_CMD="livekit-cli"
fi

# Create a room with agent dispatch
ROOM_NAME="agent-test-$(date +%s)"

echo "Creating room: $ROOM_NAME"

# Create room with agent configuration
$LK_CMD room create \
    --api-key "devkey" \
    --api-secret "secret" \
    --url "ws://localhost:7880" \
    --name "$ROOM_NAME" \
    --agents-file - <<EOF
{
  "agents": [
    {
      "type": "JT_ROOM",
      "namespace": "default",
      "metadata": "{\"test\": true}"
    }
  ]
}
EOF

echo "Room created with agent job dispatch"
echo "Check the agent logs to see if it received the job"