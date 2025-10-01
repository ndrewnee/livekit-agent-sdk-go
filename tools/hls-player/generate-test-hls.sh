#!/bin/bash

# Generate HLS test output for manual verification
# Creates HLS files in /tmp/hls-test-output that persist after test

set -e

OUTPUT_DIR="/tmp/hls-test-output"
TIMESTAMP=$(date +%s)
SESSION_DIR="$OUTPUT_DIR/test-session-$TIMESTAMP"

echo "🎬 Generating HLS Test Output"
echo ""
echo "Output directory: $SESSION_DIR"
echo ""

# Create output directory
mkdir -p "$SESSION_DIR"

# Run test with custom output directory
cd "$(dirname "$0")/../.."

echo "Running E2E test..."
go test -v -tags=e2e ./pkg/egress -run TestE2EPipelineHLSGeneration -timeout 30s

# Find the most recent test output
LATEST_TEST=$(find /var/folders -type d -name "e2e-session-*" -mmin -1 2>/dev/null | head -1)

if [ -z "$LATEST_TEST" ]; then
    echo "❌ Could not find test output"
    echo "The test may have failed or files were cleaned up too quickly"
    exit 1
fi

echo ""
echo "Copying files to persistent location..."
cp -r "$LATEST_TEST"/* "$SESSION_DIR/"

echo "✅ HLS files generated successfully!"
echo ""
echo "Files created:"
ls -lh "$SESSION_DIR/"
echo ""
echo "Playlist location:"
echo "$SESSION_DIR/playlist.m3u8"
echo ""
echo "To play:"
echo "1. Start server: go run tools/hls-player/server.go"
echo "2. Open: http://localhost:8080/tools/hls-player/player.html"
echo "3. Load: $SESSION_DIR/playlist.m3u8"
echo ""
