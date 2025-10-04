#!/bin/bash

# Full-Scale E2E Test Runner
# Tests complete egress workflow with:
# - LiveKit server (dev mode)
# - Egress worker
# - Multiple participants
# - MinIO storage
# - HLS output verification

set -e

echo "═══════════════════════════════════════════════════════════"
echo "Full-Scale E2E Test"
echo "═══════════════════════════════════════════════════════════"
echo ""

# Go to project root
cd "$(dirname "$0")/../.."

# Step 1: Detect LiveKit server
echo "Step 1: Detecting LiveKit server..."
echo ""

DETECT_OUTPUT=$(./tools/hls-player/detect-livekit.sh 2>&1)
DETECT_EXIT_CODE=$?

if [ $DETECT_EXIT_CODE -ne 0 ]; then
    echo "❌ LiveKit server not detected!"
    echo ""
    echo "$DETECT_OUTPUT"
    echo ""
    echo "Please start LiveKit server first:"
    echo "  livekit-server --dev"
    echo ""
    echo "Or with Docker:"
    echo "  docker run -d -p 7880:7880 -p 7881:7881 livekit/livekit-server --dev"
    exit 1
fi

# Parse environment variables from detection output
eval $(echo "$DETECT_OUTPUT" | grep "^export LIVEKIT_")

echo "✓ LiveKit server detected"
echo "  URL: $LIVEKIT_URL"
echo ""

# Step 2: Check MinIO
echo "Step 2: Checking MinIO..."
echo ""

MINIO_ENDPOINT="${MINIO_ENDPOINT:-localhost:9000}"
MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY:-minioadmin}"
MINIO_SECRET_KEY="${MINIO_SECRET_KEY:-minioadmin}"
MINIO_BUCKET="${MINIO_BUCKET:-egress-test}"

if ! curl -s "http://$MINIO_ENDPOINT/minio/health/live" > /dev/null 2>&1; then
    echo "❌ MinIO not detected on $MINIO_ENDPOINT"
    echo ""
    echo "Starting MinIO with Docker..."

    docker run -d \
        -p 9000:9000 \
        -p 9001:9001 \
        --name minio-egress-test \
        -e MINIO_ROOT_USER=$MINIO_ACCESS_KEY \
        -e MINIO_ROOT_PASSWORD=$MINIO_SECRET_KEY \
        quay.io/minio/minio server /data --console-address ":9001"

    echo "Waiting for MinIO to start..."
    sleep 5

    if ! curl -s "http://$MINIO_ENDPOINT/minio/health/live" > /dev/null 2>&1; then
        echo "❌ Failed to start MinIO"
        exit 1
    fi

    echo "✓ MinIO started"
else
    echo "✓ MinIO detected at $MINIO_ENDPOINT"
fi
echo ""

# Step 3: Set up MinIO bucket
echo "Step 3: Setting up MinIO bucket..."
echo ""

if [ -f "./tools/hls-player/setup-minio.sh" ]; then
    ./tools/hls-player/setup-minio.sh
    echo "✓ MinIO bucket configured"
else
    echo "⚠️  setup-minio.sh not found, skipping bucket setup"
    echo "   You may need to configure bucket permissions manually"
fi
echo ""

# Step 4: Export environment variables
export LIVEKIT_URL="$LIVEKIT_URL"
export LIVEKIT_API_KEY="$LIVEKIT_API_KEY"
export LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET"
export MINIO_ENDPOINT="$MINIO_ENDPOINT"
export MINIO_ACCESS_KEY="$MINIO_ACCESS_KEY"
export MINIO_SECRET_KEY="$MINIO_SECRET_KEY"
export MINIO_BUCKET="$MINIO_BUCKET"

echo "Step 4: Environment configured"
echo ""
echo "  LIVEKIT_URL:      $LIVEKIT_URL"
echo "  LIVEKIT_API_KEY:  $LIVEKIT_API_KEY"
echo "  MINIO_ENDPOINT:   $MINIO_ENDPOINT"
echo "  MINIO_BUCKET:     $MINIO_BUCKET"
echo ""

# Step 5: Run the test
echo "Step 5: Running full-scale E2E test..."
echo ""
echo "This test will:"
echo "  • Create a LiveKit room"
echo "  • Start egress worker"
echo "  • Launch 3 participants publishing from test.mp4"
echo "  • Record to MinIO for 15 seconds"
echo "  • Verify HLS output"
echo "  • Generate MinIO URLs for manual verification"
echo ""

read -p "Continue? [Y/n] " -n 1 -r
echo
if [[ $REPLY =~ ^[Nn]$ ]]; then
    echo "Aborted."
    exit 0
fi

echo ""
echo "Running test..."
echo ""

# Run the Go test
TEST_OUTPUT=$(go test -v -tags=e2e ./pkg/egress -run TestE2EFullScale -timeout 5m 2>&1)
TEST_EXIT_CODE=$?

# Show test output
echo "$TEST_OUTPUT"
echo ""

if [ $TEST_EXIT_CODE -ne 0 ]; then
    echo "❌ Test failed!"
    exit 1
fi

# Extract MinIO URLs from test output
PLAYLIST_URLS=$(echo "$TEST_OUTPUT" | grep -E "http://.*\.m3u8" || echo "")

if [ -n "$PLAYLIST_URLS" ]; then
    echo "═══════════════════════════════════════════════════════════"
    echo "HLS Playlists Available"
    echo "═══════════════════════════════════════════════════════════"
    echo ""
    echo "$PLAYLIST_URLS"
    echo ""
    echo "═══════════════════════════════════════════════════════════"
    echo "Manual Verification"
    echo "═══════════════════════════════════════════════════════════"
    echo ""
    echo "1. Start HLS player:"
    echo "   ./tools/hls-player/play.sh"
    echo ""
    echo "2. Open in browser:"
    echo "   http://localhost:8080/tools/hls-player/player.html"
    echo ""
    echo "3. Load each playlist URL above"
    echo ""

    # Offer to start player
    read -p "Start HLS player now? [y/N] " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        echo ""
        echo "Starting HLS player..."
        ./tools/hls-player/play.sh &
        PLAYER_PID=$!

        sleep 3

        echo ""
        echo "Player started (PID: $PLAYER_PID)"
        echo "Open: http://localhost:8080/tools/hls-player/player.html"
        echo ""
        echo "Press Ctrl+C to stop"
        echo ""

        wait $PLAYER_PID
    fi
else
    echo "⚠️  No playlist URLs found in test output"
    echo ""
    echo "Check MinIO manually:"
    echo "  MinIO Console: http://$MINIO_ENDPOINT"
    echo "  Login: $MINIO_ACCESS_KEY / $MINIO_SECRET_KEY"
    echo "  Bucket: $MINIO_BUCKET"
fi

echo ""
echo "═══════════════════════════════════════════════════════════"
echo "✅ Full-Scale E2E Test Complete!"
echo "═══════════════════════════════════════════════════════════"
echo ""

# Cleanup info
echo "Cleanup:"
echo "  Stop MinIO: docker stop minio-egress-test && docker rm minio-egress-test"
echo ""
