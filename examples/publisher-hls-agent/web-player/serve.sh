#!/bin/bash
# Complete E2EE testing environment
# Starts: MinIO, LiveKit server, publisher-hls-agent (with E2EE), and HTTP server

set -e

# Configuration
LIVEKIT_URL="${LIVEKIT_URL:-ws://localhost:7880}"
LIVEKIT_API_KEY="${LIVEKIT_API_KEY:-devkey}"
LIVEKIT_API_SECRET="${LIVEKIT_API_SECRET:-secret}"
E2EE_PASSPHRASE="${E2EE_PASSPHRASE:-test-e2ee-secret-123}"
MINIO_PORT="${MINIO_PORT:-9000}"
MINIO_CONSOLE_PORT="${MINIO_CONSOLE_PORT:-9001}"
MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY:-minioadmin}"
MINIO_SECRET_KEY="${MINIO_SECRET_KEY:-minioadmin}"
MINIO_BUCKET="${MINIO_BUCKET:-publisher-hls}"
WEB_PORT="${WEB_PORT:-8080}"
AGENT_NAME="${AGENT_NAME:-e2ee-web-agent}"
ROOM_NAME="${ROOM_NAME:-e2ee-web-room}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Cleanup function
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    # Kill background processes
    [ -n "$MINIO_PID" ] && kill $MINIO_PID 2>/dev/null && echo "Stopped MinIO"
    [ -n "$LIVEKIT_PID" ] && kill $LIVEKIT_PID 2>/dev/null && echo "Stopped LiveKit"
    [ -n "$AGENT_PID" ] && kill $AGENT_PID 2>/dev/null && echo "Stopped Agent"
    [ -n "$HTTP_PID" ] && kill $HTTP_PID 2>/dev/null && echo "Stopped HTTP server"

    # Stop Docker containers if started
    docker stop minio-e2ee-test 2>/dev/null || true
    docker stop livekit-e2ee-test 2>/dev/null || true

    echo -e "${GREEN}Cleanup complete${NC}"
}

trap cleanup EXIT

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGENT_DIR="$(dirname "$SCRIPT_DIR")"

echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║        LiveKit E2EE Publisher Test Environment            ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""

# Check prerequisites
echo -e "${YELLOW}Checking prerequisites...${NC}"

if ! command -v docker &> /dev/null; then
    echo -e "${RED}Docker is required but not installed.${NC}"
    exit 1
fi

if ! command -v python3 &> /dev/null; then
    echo -e "${RED}Python3 is required but not installed.${NC}"
    exit 1
fi

# Check if agent binary exists
AGENT_BINARY="$AGENT_DIR/publisher-hls-agent"
if [ ! -f "$AGENT_BINARY" ]; then
    echo -e "${YELLOW}Agent binary not found, building...${NC}"
    (cd "$AGENT_DIR" && go build .)
fi

echo -e "${GREEN}Prerequisites OK${NC}"
echo ""

# ============================================================================
# Start MinIO
# ============================================================================
echo -e "${BLUE}[1/4] Starting MinIO...${NC}"

# Check if MinIO is already running
if curl -s http://localhost:$MINIO_PORT/minio/health/live > /dev/null 2>&1; then
    echo -e "${YELLOW}MinIO already running on port $MINIO_PORT${NC}"
else
    # Start MinIO in Docker
    docker rm -f minio-e2ee-test 2>/dev/null || true
    docker run -d --name minio-e2ee-test \
        -p $MINIO_PORT:9000 \
        -p $MINIO_CONSOLE_PORT:9001 \
        -e "MINIO_ROOT_USER=$MINIO_ACCESS_KEY" \
        -e "MINIO_ROOT_PASSWORD=$MINIO_SECRET_KEY" \
        minio/minio server /data --console-address ":9001"

    # Wait for MinIO to start
    echo -n "Waiting for MinIO..."
    for i in {1..30}; do
        if curl -s http://localhost:$MINIO_PORT/minio/health/live > /dev/null 2>&1; then
            echo -e " ${GREEN}Ready${NC}"
            break
        fi
        echo -n "."
        sleep 1
    done
fi

# Create bucket using mc (MinIO Client) via Docker
echo "Creating bucket '$MINIO_BUCKET'..."
docker run --rm --network host \
    -e MC_HOST_local="http://$MINIO_ACCESS_KEY:$MINIO_SECRET_KEY@localhost:$MINIO_PORT" \
    minio/mc mb --ignore-existing local/$MINIO_BUCKET 2>/dev/null || true

docker run --rm --network host \
    -e MC_HOST_local="http://$MINIO_ACCESS_KEY:$MINIO_SECRET_KEY@localhost:$MINIO_PORT" \
    minio/mc anonymous set download local/$MINIO_BUCKET 2>/dev/null || true

echo -e "${GREEN}MinIO ready at http://localhost:$MINIO_PORT${NC}"
echo ""

# ============================================================================
# Start LiveKit Server
# ============================================================================
echo -e "${BLUE}[2/4] Starting LiveKit Server...${NC}"

# Check if LiveKit is already running
if curl -s http://localhost:7880 > /dev/null 2>&1; then
    echo -e "${YELLOW}LiveKit already running on port 7880${NC}"
else
    docker rm -f livekit-e2ee-test 2>/dev/null || true
    docker run -d --name livekit-e2ee-test \
        -p 7880:7880 \
        -p 7881:7881 \
        -p 7882:7882/udp \
        -e "LIVEKIT_KEYS=$LIVEKIT_API_KEY: $LIVEKIT_API_SECRET" \
        livekit/livekit-server --dev --bind 0.0.0.0

    # Wait for LiveKit to start
    echo -n "Waiting for LiveKit..."
    for i in {1..30}; do
        if curl -s http://localhost:7880 > /dev/null 2>&1; then
            echo -e " ${GREEN}Ready${NC}"
            break
        fi
        echo -n "."
        sleep 1
    done
fi

echo -e "${GREEN}LiveKit ready at ws://localhost:7880${NC}"
echo ""

# ============================================================================
# Start Publisher HLS Agent
# ============================================================================
echo -e "${BLUE}[3/4] Starting Publisher HLS Agent with E2EE...${NC}"

# Create output directory
OUTPUT_DIR="$SCRIPT_DIR/recordings"
mkdir -p "$OUTPUT_DIR"

# Start the agent
export LIVEKIT_URL="$LIVEKIT_URL"
export LIVEKIT_API_KEY="$LIVEKIT_API_KEY"
export LIVEKIT_API_SECRET="$LIVEKIT_API_SECRET"
export E2EE_PASSPHRASE="$E2EE_PASSPHRASE"
export S3_ENDPOINT="localhost:$MINIO_PORT"
export S3_BUCKET="$MINIO_BUCKET"
export S3_REGION="us-east-1"
export S3_ACCESS_KEY="$MINIO_ACCESS_KEY"
export S3_SECRET_KEY="$MINIO_SECRET_KEY"
export S3_FORCE_PATH_STYLE="true"
export S3_USE_SSL="false"
export S3_PREFIX="web-e2ee-recordings"
export S3_OBJECT_ACL="public-read"
export AUTO_ACTIVATE_RECORDING="true"
export AGENT_NAME="$AGENT_NAME"

echo "Agent configuration:"
echo "  - E2EE Passphrase: $E2EE_PASSPHRASE"
echo "  - S3 Endpoint: $S3_ENDPOINT"
echo "  - S3 Bucket: $MINIO_BUCKET"
echo "  - Agent Name: $AGENT_NAME"
echo ""

"$AGENT_BINARY" > "$SCRIPT_DIR/agent.log" 2>&1 &
AGENT_PID=$!

# Wait for agent to register
echo -n "Waiting for agent to register..."
sleep 3
if kill -0 $AGENT_PID 2>/dev/null; then
    echo -e " ${GREEN}Running (PID: $AGENT_PID)${NC}"
else
    echo -e " ${RED}Failed to start!${NC}"
    echo "Check $SCRIPT_DIR/agent.log for errors"
    exit 1
fi

echo -e "${GREEN}Agent ready and waiting for jobs${NC}"
echo ""

# ============================================================================
# Start HTTP Server for Web Client
# ============================================================================
echo -e "${BLUE}[4/4] Starting HTTP Server...${NC}"

cd "$SCRIPT_DIR"
python3 -m http.server $WEB_PORT > /dev/null 2>&1 &
HTTP_PID=$!

sleep 1
if kill -0 $HTTP_PID 2>/dev/null; then
    echo -e "${GREEN}HTTP server running on port $WEB_PORT${NC}"
else
    echo -e "${RED}Failed to start HTTP server${NC}"
    exit 1
fi

echo ""
echo -e "${BLUE}╔════════════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║                    Environment Ready!                      ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${GREEN}Services:${NC}"
echo -e "  LiveKit Server:  ${YELLOW}ws://localhost:7880${NC}"
echo -e "  MinIO:           ${YELLOW}http://localhost:$MINIO_PORT${NC}"
echo -e "  MinIO Console:   ${YELLOW}http://localhost:$MINIO_CONSOLE_PORT${NC}"
echo -e "  Web Client:      ${YELLOW}http://localhost:$WEB_PORT/e2ee-publisher-simple.html${NC}"
echo ""
echo -e "${GREEN}E2EE Configuration:${NC}"
echo -e "  Passphrase:      ${YELLOW}$E2EE_PASSPHRASE${NC}"
echo -e "  Agent Name:      ${YELLOW}$AGENT_NAME${NC}"
echo ""
echo -e "${GREEN}S3 Storage:${NC}"
echo -e "  Bucket:          ${YELLOW}$MINIO_BUCKET${NC}"
echo -e "  Prefix:          ${YELLOW}web-e2ee-recordings${NC}"
echo -e "  Access Key:      ${YELLOW}$MINIO_ACCESS_KEY${NC}"
echo ""
echo -e "${BLUE}To test:${NC}"
echo "  1. Open http://localhost:$WEB_PORT/e2ee-publisher-simple.html"
echo "  2. Set Room Name to: $ROOM_NAME"
echo "  3. Ensure E2EE Passphrase is: $E2EE_PASSPHRASE"
echo "  4. Click 'Connect & Publish'"
echo "  5. The agent will record and upload to MinIO"
echo ""
echo -e "${BLUE}To create room with agent dispatch:${NC}"
echo "  curl -X POST http://localhost:7880/twirp/livekit.RoomService/CreateRoom \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -H 'Authorization: Bearer <token>' \\"
echo "    -d '{\"name\":\"$ROOM_NAME\",\"agents\":[{\"agentName\":\"$AGENT_NAME\"}]}'"
echo ""
echo -e "${YELLOW}Agent log: $SCRIPT_DIR/agent.log${NC}"
echo -e "${YELLOW}Press Ctrl+C to stop all services${NC}"
echo ""

# Tail agent log
tail -f "$SCRIPT_DIR/agent.log"
