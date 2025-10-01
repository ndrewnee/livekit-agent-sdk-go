#!/bin/bash

# Manual HLS Test Script
# Generates high-quality HLS output for manual audio/video verification
# Uses full test data (138 packets = ~3 seconds) for better quality assessment

set -e

OUTPUT_DIR="/tmp/hls-manual-test"
TIMESTAMP=$(date +%s)
SESSION_ID="manual-test-$TIMESTAMP"
SESSION_DIR="$OUTPUT_DIR/$SESSION_ID"

echo "🎬 Manual HLS Quality Test"
echo ""
echo "This script generates HLS output for manual verification of:"
echo "  ✓ Audio quality (Opus → AAC transcoding)"
echo "  ✓ Video quality (H.264 passthrough)"
echo "  ✓ Audio/video synchronization"
echo "  ✓ HLS segment structure"
echo ""
echo "Output: $SESSION_DIR"
echo ""

# Create output directory
mkdir -p "$SESSION_DIR"

# Go to project root
cd "$(dirname "$0")/../.."

echo "Running REAL E2E test with LiveKit room..."
echo "This test:"
echo "  • Creates real LiveKit room"
echo "  • Publishes from test.mp4 (10 seconds, 720p)"
echo "  • Captures with egress agent"
echo "  • Generates HLS output"
echo ""
echo "NOTE: This requires LiveKit server running on ws://localhost:7880"
echo "      Start with: docker run -p 7880:7880 -p 7881:7881 livekit/livekit-server"
echo ""

# Check if LiveKit server is running
if ! curl -s "http://localhost:7881/validate" > /dev/null 2>&1; then
    echo "⚠️  LiveKit server not detected on localhost:7880"
    echo ""
    read -p "Continue anyway? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborted. Start LiveKit server first."
        exit 1
    fi
fi

# Set output directory for test
export TEST_OUTPUT_DIR="$OUTPUT_DIR"

# Run real E2E test
TEST_OUTPUT=$(go test -v -tags=e2e ./pkg/egress -run TestE2EManualVerification -timeout 60s 2>&1)
TEST_EXIT_CODE=$?

# Show test result
echo "$TEST_OUTPUT" | tail -30
echo ""

if [ $TEST_EXIT_CODE -ne 0 ]; then
    echo "❌ Test failed!"
    echo ""
    echo "Full output:"
    echo "$TEST_OUTPUT"
    exit 1
fi

# The test creates output directly in $OUTPUT_DIR/$SESSION_ID
# Find the session directory
ACTUAL_SESSION=$(find "$OUTPUT_DIR" -maxdepth 1 -type d -name "manual-test-*" -mmin -2 2>/dev/null | sort | tail -1)

if [ -n "$ACTUAL_SESSION" ]; then
    SESSION_DIR="$ACTUAL_SESSION"
    echo "Found session output: $SESSION_DIR"
else
    echo "⚠️  Could not find session output, using: $SESSION_DIR"
fi

# Verify files were copied
if [ ! -f "$SESSION_DIR/playlist.m3u8" ]; then
    echo "❌ playlist.m3u8 not found in output directory"
    echo "Directory contents:"
    ls -la "$SESSION_DIR"
    exit 1
fi

echo ""
echo "✅ HLS files generated successfully!"
echo ""
echo "═══════════════════════════════════════════════════════════"
echo "📁 Output Directory:"
echo "   $SESSION_DIR"
echo ""
echo "📝 Files Created:"
ls -lh "$SESSION_DIR"
echo ""
echo "═══════════════════════════════════════════════════════════"
echo "🎥 To Play & Verify:"
echo ""
echo "1. Start HLS player server:"
echo "   ./tools/hls-player/play.sh"
echo ""
echo "2. Open player in browser:"
echo "   http://localhost:8080/tools/hls-player/player.html"
echo ""
echo "3. Enter playlist path:"
echo "   $SESSION_DIR/playlist.m3u8"
echo ""
echo "4. Click 'Load Stream'"
echo ""
echo "═══════════════════════════════════════════════════════════"
echo "✓ What to Check:"
echo ""
echo "  Audio Quality:"
echo "    • Click 🔊 to unmute"
echo "    • Should hear clear audio (not garbled)"
echo "    • Check for clicks, pops, distortion"
echo ""
echo "  Video Quality:"
echo "    • Should be smooth playback"
echo "    • Resolution shown in stats"
echo "    • No dropped frames"
echo ""
echo "  Duration:"
echo "    • ~2-3 seconds total"
echo "    • 138 video packets"
echo "    • 138 audio packets"
echo ""
echo "  Synchronization:"
echo "    • Audio and video in sync"
echo "    • No drift or delay"
echo ""
echo "═══════════════════════════════════════════════════════════"
echo ""
echo "Files will remain at: $OUTPUT_DIR"
echo "Delete when done: rm -rf $OUTPUT_DIR"
echo ""

# Offer to start player automatically
read -p "Start HLS player now? [y/N] " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo ""
    echo "Starting HLS player..."
    echo "Playlist URL: $SESSION_DIR/playlist.m3u8"
    echo ""

    # Start player in background
    ./tools/hls-player/play.sh &
    PLAYER_PID=$!

    sleep 3

    echo ""
    echo "Player started (PID: $PLAYER_PID)"
    echo "Press Ctrl+C to stop"
    echo ""

    # Keep script running
    wait $PLAYER_PID
fi
