#!/bin/bash

# Color codes for output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}           Save to HLS - Automated Test Runner            ${NC}"
echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
echo

# Check prerequisites
echo "Checking prerequisites..."

# Check Go
if ! command -v go &> /dev/null; then
    echo -e "${RED}✗ Go not found${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} Go found: $(go version)"

# Check FFmpeg
if ! command -v ffmpeg &> /dev/null; then
    echo -e "${RED}✗ FFmpeg not found${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} FFmpeg found: $(ffmpeg -version | head -1)"

# Check FFprobe
if ! command -v ffprobe &> /dev/null; then
    echo -e "${RED}✗ FFprobe not found${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} FFprobe found"

# Check test video
TEST_VIDEO="../../examples/egress-agent/test-data/test.mp4"
if [ ! -f "$TEST_VIDEO" ]; then
    echo -e "${RED}✗ Test video not found: $TEST_VIDEO${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} Test video found"

# Check LiveKit server
echo
echo "Checking LiveKit server..."
if ! curl -s http://localhost:7880 > /dev/null 2>&1; then
    echo -e "${RED}✗ LiveKit server not responding on localhost:7880${NC}"
    echo -e "${YELLOW}  Please start the LiveKit server:${NC}"
    echo -e "  ${YELLOW}cd /path/to/livekit && ./livekit-server --dev${NC}"
    exit 1
fi
echo -e "${GREEN}✓${NC} LiveKit server is running"

echo
echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
echo "Running test..."
echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
echo

# Run the test
go test -v -tags e2e -run TestSaveToHLS -timeout 60s

TEST_RESULT=$?

echo
echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
if [ $TEST_RESULT -eq 0 ]; then
    echo -e "${GREEN}✅ Test PASSED${NC}"
else
    echo -e "${RED}❌ Test FAILED${NC}"
fi
echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"

exit $TEST_RESULT
