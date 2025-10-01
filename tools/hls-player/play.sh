#!/bin/bash

# HLS Player Launch Script
# Starts a local HTTP server and opens the HLS player in the browser

set -e

PORT="${PORT:-8080}"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "🎥 Starting HLS Player Server"
echo ""
echo "Directory: $DIR"
echo "Port: $PORT"
echo ""

# Check if Go is available
if command -v go &> /dev/null; then
    echo "Using Go HTTP server (recommended)"
    echo "Player URL: http://localhost:$PORT/tools/hls-player/player.html"
    echo ""
    echo "Press Ctrl+C to stop"
    echo ""

    # Open browser after a delay
    (sleep 2 && open "http://localhost:$PORT/tools/hls-player/player.html") &

    cd "$DIR"
    go run tools/hls-player/server.go -port "$PORT"

elif command -v python3 &> /dev/null; then
    echo "Using Python HTTP server"
    echo "Player URL: http://localhost:$PORT/tools/hls-player/player.html"
    echo ""
    echo "Press Ctrl+C to stop"
    echo ""

    # Open browser after a delay
    (sleep 2 && open "http://localhost:$PORT/tools/hls-player/player.html") &

    cd "$DIR"
    python3 -m http.server "$PORT"

elif command -v node &> /dev/null; then
    echo "Using Node.js HTTP server"
    echo "Installing http-server if needed..."
    npx http-server -p "$PORT" --cors -o /tools/hls-player/player.html "$DIR"

else
    echo "❌ Error: No suitable HTTP server found"
    echo ""
    echo "Please install one of the following:"
    echo "  - Go (recommended): https://golang.org/dl/"
    echo "  - Python 3: https://www.python.org/downloads/"
    echo "  - Node.js: https://nodejs.org/"
    exit 1
fi
