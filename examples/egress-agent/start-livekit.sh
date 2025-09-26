#!/bin/bash

# Start LiveKit Server Script for Egress Agent Testing

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
CONFIG_FILE="$SCRIPT_DIR/livekit-config.yaml"

echo "================================"
echo "Starting LiveKit Server"
echo "================================"

# Check if livekit-server is installed
if ! command -v livekit-server &> /dev/null; then
    echo "LiveKit server is not installed. Please install it first:"
    echo "brew install livekit"
    exit 1
fi

# Check if config file exists
if [ ! -f "$CONFIG_FILE" ]; then
    echo "Config file not found: $CONFIG_FILE"
    exit 1
fi

echo "Starting LiveKit server with config: $CONFIG_FILE"
echo "Server URL: ws://localhost:7880"
echo "API Key: APIhZLy9N9dS7k"
echo ""
echo "Press Ctrl+C to stop the server"
echo "================================"

# Start the server
livekit-server --config "$CONFIG_FILE"