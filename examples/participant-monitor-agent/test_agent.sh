#!/bin/bash

# Test script for Participant Monitor Agent
# This script demonstrates how to test the participant agent with a LiveKit server

echo "🧪 Participant Monitor Agent Test Script"
echo "======================================="

# Configuration
LIVEKIT_URL="${LIVEKIT_URL:-http://localhost:7880}"
LIVEKIT_WS_URL="${LIVEKIT_WS_URL:-ws://localhost:7880}"
API_KEY="${LIVEKIT_API_KEY:-devkey}"
API_SECRET="${LIVEKIT_API_SECRET:-secret}"
ROOM_NAME="test-participant-monitor-$(date +%s)"

echo "📋 Configuration:"
echo "  - Server: $LIVEKIT_URL"
echo "  - Room: $ROOM_NAME"
echo ""

# Check if livekit-cli is installed
if ! command -v livekit-cli &> /dev/null; then
    echo "❌ livekit-cli is required but not installed."
    echo "   Install with: go install github.com/livekit/livekit-cli@latest"
    exit 1
fi

# Create room with agent dispatch
echo "1️⃣ Creating room with participant monitor agent..."
livekit-cli create-room \
    --url "$LIVEKIT_URL" \
    --api-key "$API_KEY" \
    --api-secret "$API_SECRET" \
    --name "$ROOM_NAME" \
    --agent "participant-monitor"

if [ $? -ne 0 ]; then
    echo "❌ Failed to create room"
    exit 1
fi

echo "✅ Room created: $ROOM_NAME"
echo ""

# Generate tokens for test participants
echo "2️⃣ Generating participant tokens..."

# Presenter token
PRESENTER_TOKEN=$(livekit-cli create-token \
    --url "$LIVEKIT_URL" \
    --api-key "$API_KEY" \
    --api-secret "$API_SECRET" \
    --identity "presenter-1" \
    --name "Main Presenter" \
    --room "$ROOM_NAME" \
    --metadata "presenter" \
    --grant '{"canPublish":true,"canSubscribe":true,"canPublishData":true}')

# Viewer token
VIEWER_TOKEN=$(livekit-cli create-token \
    --url "$LIVEKIT_URL" \
    --api-key "$API_KEY" \
    --api-secret "$API_SECRET" \
    --identity "viewer-1" \
    --name "Viewer One" \
    --room "$ROOM_NAME" \
    --metadata "viewer" \
    --grant '{"canPublish":false,"canSubscribe":true,"canPublishData":true}')

echo "✅ Tokens generated"
echo ""

# Start the agent
echo "3️⃣ Starting participant monitor agent..."
echo "   Run this in a separate terminal:"
echo ""
echo "   go run main.go --url $LIVEKIT_URL --api-key $API_KEY --api-secret $API_SECRET"
echo ""
echo "   Press Enter when agent is running..."
read -r

# Join as participants
echo "4️⃣ You can now test the agent by:"
echo ""
echo "Option A: Use LiveKit example app"
echo "  1. Open: https://meet.livekit.io"
echo "  2. Enter URL: $LIVEKIT_WS_URL"
echo "  3. Enter Token (Presenter): $PRESENTER_TOKEN"
echo "  4. Or Token (Viewer): $VIEWER_TOKEN"
echo ""

echo "Option B: Use livekit-cli to simulate participants"
echo "  Terminal 1 (Presenter):"
echo "  livekit-cli join-room --url $LIVEKIT_URL --token $PRESENTER_TOKEN"
echo ""
echo "  Terminal 2 (Viewer):"
echo "  livekit-cli join-room --url $LIVEKIT_URL --token $VIEWER_TOKEN"
echo ""

echo "Option C: Use the Go SDK test client"
cat > test_client.go << 'EOF'
package main

import (
    "fmt"
    "log"
    "os"
    "time"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
    token := os.Args[1]
    identity := os.Args[2]
    
    room, err := lksdk.ConnectToRoomWithToken("ws://localhost:7880", token, &lksdk.RoomCallback{
        OnDataPacketReceived: func(data lksdk.DataPacket, params lksdk.DataReceiveParams) {
            fmt.Printf("📨 Received: %s\n", string(data.Payload))
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    
    fmt.Printf("✅ Connected as %s\n", identity)
    
    // Send test message
    room.LocalParticipant.PublishData([]byte("Hello from " + identity), lksdk.WithDataPublishReliable(true))
    
    // Stay connected
    time.Sleep(30 * time.Second)
    room.Disconnect()
}
EOF

echo "  go run test_client.go '$PRESENTER_TOKEN' presenter-1"
echo ""

echo "5️⃣ Monitor the agent output to see:"
echo "  - Participants joining/leaving"
echo "  - Metadata and permission changes"
echo "  - Track publishing events"
echo "  - Data messages"
echo "  - Status reports every 30 seconds"
echo ""

echo "6️⃣ Cleanup (when done):"
echo "  livekit-cli delete-room --url $LIVEKIT_URL --api-key $API_KEY --api-secret $API_SECRET --room $ROOM_NAME"
echo ""

echo "🎯 Test scenarios to try:"
echo "  1. Join/leave with different participants"
echo "  2. Update participant metadata"
echo "  3. Change permissions (presenter to viewer)"
echo "  4. Publish/unpublish video and audio"
echo "  5. Send data messages between participants"
echo "  6. Start screen sharing to trigger presenter group"