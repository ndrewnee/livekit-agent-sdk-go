#!/bin/bash

# Simple demo script that runs agent and creates a room with dispatch

set -e

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${BLUE}=== LiveKit Simple Room Agent Demo ===${NC}"
echo ""

# Configuration
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
ROOM_NAME="demo-room-$(date +%s)"

# Build the agent
echo -e "${YELLOW}Building agent...${NC}"
go build -o simple-room-agent .
echo -e "${GREEN}✓ Build complete${NC}"
echo ""

# Start the agent in background
echo -e "${YELLOW}Starting agent...${NC}"
./simple-room-agent &
AGENT_PID=$!

# Cleanup function
cleanup() {
    echo ""
    echo -e "${YELLOW}Stopping agent...${NC}"
    kill $AGENT_PID 2>/dev/null || true
    rm -f create_room_temp.go
    echo -e "${GREEN}✓ Demo stopped${NC}"
}
trap cleanup EXIT

# Wait for agent to start
sleep 3
echo -e "${GREEN}✓ Agent started (PID: $AGENT_PID)${NC}"
echo ""

# Create room with agent dispatch
echo -e "${YELLOW}Creating room with agent dispatch...${NC}"
ROOM_NAME=$ROOM_NAME go run test_with_dispatch.go > /tmp/room_output.txt 2>&1
if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Room created: $ROOM_NAME${NC}"
    cat /tmp/room_output.txt
else
    echo "Failed to create room"
    cat /tmp/room_output.txt
    exit 1
fi

echo ""
echo -e "${BLUE}=== Demo is running! ===${NC}"
echo "The agent is now monitoring the room."
echo "Check the agent output above to see it processing the room."
echo ""
echo "Press Ctrl+C to stop the demo"
echo ""

# Keep the script running
wait $AGENT_PID