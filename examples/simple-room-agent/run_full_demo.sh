#!/bin/bash

# Full demo script for simple-room-agent
# This script:
# 1. Builds and starts the agent
# 2. Creates a room with agent dispatch
# 3. Connects a test participant
# 4. Shows the agent logs

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
LIVEKIT_URL="ws://localhost:7880"
LIVEKIT_HTTP_URL="http://localhost:7880"
API_KEY="devkey"
API_SECRET="secret"
ROOM_NAME="demo-room-$(date +%s)"

echo -e "${BLUE}=== LiveKit Simple Room Agent Demo ===${NC}"
echo ""

# Check if LiveKit server is running
echo -e "${YELLOW}Checking LiveKit server...${NC}"
if ! curl -s http://localhost:7880/ > /dev/null; then
    echo -e "${RED}Error: LiveKit server is not running on port 7880${NC}"
    echo "Please start it with: docker run --rm -p 7880:7880 livekit/livekit-server --dev"
    exit 1
fi
echo -e "${GREEN}✓ LiveKit server is running${NC}"
echo ""

# Build the agent
echo -e "${YELLOW}Building simple-room-agent...${NC}"
if ! go build -o simple-room-agent .; then
    echo -e "${RED}Failed to build agent${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Agent built successfully${NC}"
echo ""

# Start the agent in background
echo -e "${YELLOW}Starting simple-room-agent...${NC}"
export LIVEKIT_URL="$LIVEKIT_URL"
export LIVEKIT_API_KEY="$API_KEY"
export LIVEKIT_API_SECRET="$API_SECRET"

# Create log file
AGENT_LOG="agent-$(date +%Y%m%d-%H%M%S).log"
./simple-room-agent > "$AGENT_LOG" 2>&1 &
AGENT_PID=$!

# Function to cleanup on exit
cleanup() {
    echo ""
    echo -e "${YELLOW}Cleaning up...${NC}"
    if [ ! -z "$AGENT_PID" ]; then
        kill $AGENT_PID 2>/dev/null || true
    fi
    echo -e "${GREEN}✓ Cleanup complete${NC}"
}
trap cleanup EXIT

# Wait for agent to start
echo "Waiting for agent to register..."
sleep 3

# Check if agent is running
if ! kill -0 $AGENT_PID 2>/dev/null; then
    echo -e "${RED}Agent failed to start. Check $AGENT_LOG for errors${NC}"
    cat "$AGENT_LOG"
    exit 1
fi

# Check agent registration in log
if grep -q "Worker registered" "$AGENT_LOG"; then
    echo -e "${GREEN}✓ Agent registered successfully${NC}"
    WORKER_ID=$(grep "Worker registered" "$AGENT_LOG" | grep -o '"workerID":"[^"]*"' | cut -d'"' -f4)
    echo -e "  Worker ID: ${BLUE}$WORKER_ID${NC}"
else
    echo -e "${YELLOW}⚠ Agent may not be fully registered yet${NC}"
fi
echo ""

# Create room with agent dispatch
echo -e "${YELLOW}Creating room with agent dispatch...${NC}"
cat > create_room_temp.go << 'EOF'
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
    host := os.Args[1]
    apiKey := os.Args[2]
    apiSecret := os.Args[3]
    roomName := os.Args[4]

    roomClient := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)
    
    room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
        Name:     roomName,
        Metadata: "Demo room with analytics agent",
        Agents: []*livekit.RoomAgentDispatch{
            {
                AgentName: "simple-room-agent",
                Metadata:  `{"demo": true}`,
            },
        },
    })
    
    if err != nil {
        log.Fatal("Failed to create room:", err)
    }
    
    fmt.Printf("ROOM_SID=%s\n", room.Sid)
}
EOF

ROOM_OUTPUT=$(go run create_room_temp.go "$LIVEKIT_HTTP_URL" "$API_KEY" "$API_SECRET" "$ROOM_NAME")
ROOM_SID=$(echo "$ROOM_OUTPUT" | grep ROOM_SID | cut -d'=' -f2)
rm create_room_temp.go

if [ -z "$ROOM_SID" ]; then
    echo -e "${RED}Failed to create room${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Room created successfully${NC}"
echo -e "  Room Name: ${BLUE}$ROOM_NAME${NC}"
echo -e "  Room SID: ${BLUE}$ROOM_SID${NC}"
echo ""

# Wait for agent to receive job
echo -e "${YELLOW}Waiting for agent to receive job...${NC}"
sleep 2

# Check if agent received the job
if grep -q "Job request received.*$ROOM_SID" "$AGENT_LOG"; then
    echo -e "${GREEN}✓ Agent received job request${NC}"
fi

if grep -q "Room session started.*$ROOM_SID" "$AGENT_LOG"; then
    echo -e "${GREEN}✓ Agent connected to room${NC}"
fi
echo ""

# Generate participant token
echo -e "${YELLOW}Generating participant token...${NC}"
if command -v lk &> /dev/null; then
    LK_CMD="lk"
else
    LK_CMD="livekit-cli"
fi

TOKEN=$($LK_CMD create-token \
    --api-key "$API_KEY" \
    --api-secret "$API_SECRET" \
    --room "$ROOM_NAME" \
    --identity "demo-user" \
    --grant '{"video": true, "audio": true, "room": "'"$ROOM_NAME"'", "roomJoin": true}' 2>/dev/null || \
    $LK_CMD create-token \
    --api-key "$API_KEY" \
    --api-secret "$API_SECRET" \
    --room "$ROOM_NAME" \
    --identity "demo-user")

echo -e "${GREEN}✓ Token generated${NC}"
echo ""

# Show status
echo -e "${BLUE}=== Demo Status ===${NC}"
echo -e "Agent PID: $AGENT_PID"
echo -e "Agent Log: $AGENT_LOG"
echo -e "Room: $ROOM_NAME"
echo ""

# Show how to connect
echo -e "${BLUE}=== How to Test ===${NC}"
echo "1. Connect to the room using LiveKit Meet:"
echo -e "   ${GREEN}https://meet.livekit.io/custom?liveKitUrl=$LIVEKIT_URL&token=$TOKEN${NC}"
echo ""
echo "2. Or use the token directly:"
echo -e "   ${YELLOW}$TOKEN${NC}"
echo ""

# Monitor agent logs
echo -e "${BLUE}=== Agent Activity (last 20 lines) ===${NC}"
tail -n 20 "$AGENT_LOG"
echo ""

echo -e "${BLUE}=== Monitoring Agent Logs ===${NC}"
echo "Press Ctrl+C to stop the demo"
echo ""

# Follow the agent log
tail -f "$AGENT_LOG" | while read line; do
    # Highlight important log lines
    if echo "$line" | grep -q "Participant connected\|Track published\|Room Status"; then
        echo -e "${GREEN}$line${NC}"
    elif echo "$line" | grep -q "error\|Error\|ERROR"; then
        echo -e "${RED}$line${NC}"
    elif echo "$line" | grep -q "Analytics Report"; then
        echo -e "${BLUE}$line${NC}"
    else
        echo "$line"
    fi
done