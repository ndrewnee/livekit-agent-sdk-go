#!/bin/bash

# Simple script to connect a participant to trigger the agent

ROOM_NAME="${1:-test-room}"
IDENTITY="${2:-test-user}"

# Check if lk is installed
if command -v lk &> /dev/null; then
    LK_CMD="lk"
elif command -v livekit-cli &> /dev/null; then
    LK_CMD="livekit-cli"
else
    echo "Error: lk (or livekit-cli) is not installed"
    exit 1
fi

# Generate token
TOKEN=$($LK_CMD create-token \
    --api-key "devkey" \
    --api-secret "secret" \
    --room "$ROOM_NAME" \
    --identity "$IDENTITY" \
    --valid-for 1h \
    --grant '{"video": true, "audio": true, "room": "'"$ROOM_NAME"'", "roomJoin": true}')

echo "Token generated for $IDENTITY in room $ROOM_NAME:"
echo "$TOKEN"
echo ""
echo "You can connect using:"
echo "1. LiveKit Meet: https://meet.livekit.io"
echo "2. Custom URL: https://meet.livekit.io/custom?liveKitUrl=ws://localhost:7880&token=$TOKEN"