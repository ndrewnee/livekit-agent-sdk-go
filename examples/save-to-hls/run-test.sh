#!/bin/bash

set -e  # Exit on error

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

# Clean up previous test output
echo
echo "Cleaning up previous test output..."
rm -rf hls-output hls-output_final
echo -e "${GREEN}✓${NC} Cleaned up"

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

    # Verify HLS output
    echo
    echo "Verifying HLS output..."

    if [ ! -d "hls-output_final" ]; then
        echo -e "${RED}✗ hls-output_final directory not found${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓${NC} Output directory exists"

    if [ ! -f "hls-output_final/index.m3u8" ]; then
        echo -e "${RED}✗ index.m3u8 not found${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓${NC} Playlist exists"

    # Count segments
    SEGMENT_COUNT=$(ls hls-output_final/*.ts 2>/dev/null | wc -l | tr -d ' ')
    echo -e "${GREEN}✓${NC} Found $SEGMENT_COUNT HLS segments"

    if [ "$SEGMENT_COUNT" -eq 0 ]; then
        echo -e "${RED}✗ No .ts segments found${NC}"
        exit 1
    fi

    # Validate first segment
    FIRST_SEGMENT=$(ls hls-output_final/*.ts | head -1)
    echo
    echo "Validating first segment: $FIRST_SEGMENT"
    ffprobe -v error -show_streams -select_streams v:0 "$FIRST_SEGMENT" 2>&1 | grep -E "(codec_name|width|height)"

    echo
    echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${GREEN}✅ All validations passed!${NC}"
    echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"
    echo
    echo "To play the HLS stream:"
    echo -e "  ${YELLOW}ffplay hls-output_final/index.m3u8${NC}"
    echo -e "  ${YELLOW}Or open hls-output_final/index.m3u8 in VLC${NC}"
else
    echo -e "${RED}❌ Test FAILED${NC}"
fi

echo -e "${GREEN}═══════════════════════════════════════════════════════════${NC}"

exit $TEST_RESULT
