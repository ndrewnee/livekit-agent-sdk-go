#!/bin/bash

# Detect Running LiveKit Server
# Outputs environment variables for LiveKit connection

set -e

echo "🔍 Detecting LiveKit Server..."
echo ""

# Check if LiveKit server is running
LIVEKIT_PID=$(pgrep -f "livekit-server" 2>/dev/null || echo "")

if [ -z "$LIVEKIT_PID" ]; then
    echo "❌ LiveKit server not detected"
    echo ""
    echo "Start LiveKit server with:"
    echo "  docker run -d -p 7880:7880 -p 7881:7881 -p 7882:7882/udp livekit/livekit-server --dev"
    echo "  OR"
    echo "  livekit-server --dev"
    exit 1
fi

echo "✓ LiveKit server running (PID: $LIVEKIT_PID)"
echo ""

# Detect port from command line arguments first
DETECTED_PORT=$(ps -p $LIVEKIT_PID -o command= | grep -oE '\-\-port[= ]([0-9]+)' | grep -oE '[0-9]+')

# If --port flag found, use it
if [ -n "$DETECTED_PORT" ]; then
    WS_PORT=$DETECTED_PORT
    HTTP_PORT=$((DETECTED_PORT + 1))
else
    # Fallback: use lsof to detect listening ports
    if command -v lsof &> /dev/null; then
        # Get all listening ports for this process
        PORTS=$(lsof -Pan -p $LIVEKIT_PID -i TCP -sTCP:LISTEN 2>/dev/null | awk '{print $9}' | cut -d: -f2 | sort -n | uniq)

        # First port is typically the main WebSocket port
        WS_PORT=$(echo "$PORTS" | head -1)
        # Second port is typically HTTP (if exists)
        HTTP_PORT=$(echo "$PORTS" | sed -n '2p')
    fi

    # Fallback: check common ports
    if [ -z "$WS_PORT" ]; then
        if nc -z localhost 7880 2>/dev/null; then
            WS_PORT=7880
        elif nc -z localhost 8080 2>/dev/null; then
            WS_PORT=8080
        fi
    fi

    if [ -z "$HTTP_PORT" ]; then
        HTTP_PORT=$((WS_PORT + 1))
    fi
fi

# Use defaults if all detection fails
WS_PORT=${WS_PORT:-7880}
HTTP_PORT=${HTTP_PORT:-7881}

echo "📡 Detected Ports:"
echo "   WebSocket: $WS_PORT"
echo "   HTTP:      $HTTP_PORT"
echo ""

# Verify HTTP endpoint
LIVEKIT_URL="ws://localhost:$WS_PORT"
HTTP_ENDPOINT="http://localhost:$HTTP_PORT"

if curl -s "$HTTP_ENDPOINT/validate" > /dev/null 2>&1; then
    echo "✓ HTTP endpoint responding: $HTTP_ENDPOINT"
else
    echo "⚠️  HTTP endpoint not responding: $HTTP_ENDPOINT"
    echo "   This may be normal if server is busy"
fi
echo ""

# Try to detect if server is running in --dev mode
# Dev mode uses: devkey/secret
echo "🔑 Credentials:"
echo ""

# Check process command line for --dev flag
if ps -p $LIVEKIT_PID -o command= | grep -q "\-\-dev"; then
    echo "✓ Server running in --dev mode"
    echo ""
    echo "Default credentials:"
    echo "   API Key:    devkey"
    echo "   API Secret: secret"
    API_KEY="devkey"
    API_SECRET="secret"
    DEV_MODE=true
else
    echo "Server running in production mode"
    echo ""
    echo "Check config file for credentials:"
    # Try to find config file
    CONFIG_FILE=$(ps -p $LIVEKIT_PID -o command= | grep -oE '\-\-config[= ][^ ]+' | cut -d' ' -f2 || echo "")
    if [ -n "$CONFIG_FILE" ] && [ -f "$CONFIG_FILE" ]; then
        echo "   Config: $CONFIG_FILE"
        # Try to extract keys from YAML
        if grep -q "keys:" "$CONFIG_FILE" 2>/dev/null; then
            echo "   Keys found in config (check file for details)"
        fi
    else
        echo "   Config file not found"
    fi
    API_KEY=""
    API_SECRET=""
    DEV_MODE=false
fi
echo ""

# Output environment variables
echo "═══════════════════════════════════════════════════════════"
echo "Environment Variables"
echo "═══════════════════════════════════════════════════════════"
echo ""

cat << EOF
export LIVEKIT_URL="$LIVEKIT_URL"
export LIVEKIT_API_KEY="${API_KEY:-YOUR_API_KEY}"
export LIVEKIT_API_SECRET="${API_SECRET:-YOUR_API_SECRET}"
EOF

echo ""
echo "═══════════════════════════════════════════════════════════"
echo ""

if [ "$DEV_MODE" = true ]; then
    echo "✅ Ready for testing!"
    echo ""
    echo "Run manual test:"
    echo "   ./tools/hls-player/manual-test.sh"
else
    echo "⚠️  Production mode detected"
    echo ""
    echo "Set credentials manually:"
    echo "   export LIVEKIT_API_KEY=\"your_key\""
    echo "   export LIVEKIT_API_SECRET=\"your_secret\""
    echo ""
    echo "Then run manual test:"
    echo "   ./tools/hls-player/manual-test.sh"
fi
echo ""

# Offer to export variables
if [ "$DEV_MODE" = true ]; then
    echo "Export these variables now? [y/N] "
    read -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        export LIVEKIT_URL="$LIVEKIT_URL"
        export LIVEKIT_API_KEY="$API_KEY"
        export LIVEKIT_API_SECRET="$API_SECRET"
        echo "✓ Variables exported to current shell"
        echo ""
        echo "Note: Variables only persist in this terminal session"
        echo "      Other terminals need to run this script again"
    fi
fi
