#!/bin/bash

# Create a room to trigger the agent
echo "Creating test room..."

# Generate token using lk
TOKEN=$(lk token create --api-key devkey --api-secret secret --create --list)

curl -X POST http://localhost:7880/twirp/livekit.RoomService/CreateRoom \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "test-room",
    "emptyTimeout": 60,
    "metadata": "{\"require_agent\": true}"
  }'

echo ""
echo "Room created. Check if agent joined..."