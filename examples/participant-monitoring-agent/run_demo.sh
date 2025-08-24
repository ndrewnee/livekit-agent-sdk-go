#!/bin/bash

# Demo script for participant-monitoring-agent

set -e

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}=== LiveKit Participant Monitoring Agent Demo ===${NC}"
echo ""

# Check if LiveKit server is running
echo -e "${YELLOW}Checking LiveKit server...${NC}"
if ! curl -s http://localhost:7880/ > /dev/null; then
    echo -e "${RED}Error: LiveKit server is not running${NC}"
    echo "Start it with: docker run --rm -p 7880:7880 livekit/livekit-server --dev"
    exit 1
fi
echo -e "${GREEN}✓ LiveKit server is running${NC}"
echo ""

# Instructions
echo -e "${YELLOW}Instructions:${NC}"
echo "1. Start the participant monitoring agent in Terminal 1:"
echo -e "   ${GREEN}export LIVEKIT_URL='ws://localhost:7880'${NC}"
echo -e "   ${GREEN}export LIVEKIT_API_KEY='devkey'${NC}"
echo -e "   ${GREEN}export LIVEKIT_API_SECRET='secret'${NC}"
echo -e "   ${GREEN}go run .${NC}"
echo ""
echo "2. In Terminal 2 (this terminal), run the test setup:"
echo ""
echo "Press ENTER when the agent is running..."
read

# Run the test setup
echo -e "${YELLOW}Creating test room and generating tokens...${NC}"
go run . dispatch

echo ""
echo -e "${BLUE}=== What the Agent Does ===${NC}"
echo "The participant monitoring agent will:"
echo "- Monitor the specific participant's connection status"
echo "- Track when they publish/unpublish audio/video"
echo "- Detect when they are speaking (simulated)"
echo "- Monitor video frame rates"
echo "- Send welcome messages when they connect"
echo "- Generate reports every 30 seconds"
echo ""
echo -e "${YELLOW}Note:${NC} The agent uses JobType_JT_PARTICIPANT and monitors individual participants,"
echo "not entire rooms. This is useful for personalized monitoring and assistance."