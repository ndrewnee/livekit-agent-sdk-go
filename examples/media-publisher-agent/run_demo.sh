#!/bin/bash

# Demo script for media-publisher-agent

set -e

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}=== LiveKit Media Publisher Agent Demo ===${NC}"
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
echo "1. Start the media publisher agent in Terminal 1:"
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
echo -e "${YELLOW}Creating test room with media publisher...${NC}"
go run . dispatch

echo ""
echo -e "${BLUE}=== What the Agent Does ===${NC}"
echo "The media publisher agent will:"
echo "- Publish audio (sine wave tone) and/or video (test patterns)"
echo "- Support multiple publishing modes:"
echo "  • audio_tone: Generates a pure tone at specified frequency"
echo "  • video_pattern: Generates video test patterns (color bars, etc)"
echo "  • both: Publishes both audio and video simultaneously"
echo "  • interactive: Responds to data messages for dynamic control"
echo "- Send welcome messages to new participants"
echo "- Track publishing statistics (frame counts, duration)"
echo ""
echo -e "${YELLOW}Note:${NC} This agent uses JobType_JT_PUBLISHER and demonstrates"
echo "how to generate and publish media streams programmatically."