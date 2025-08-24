#!/bin/bash

# Script to start LiveKit server for local development

set -e

echo "Starting LiveKit Server for Agent Development"
echo "==========================================="
echo ""
echo "Configuration:"
echo "  URL: ws://localhost:7880"
echo "  API Key: devkey"
echo "  API Secret: secret"
echo ""
echo "Press Ctrl+C to stop the server"
echo ""

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    echo "Error: Docker is not installed"
    echo "Please install Docker from https://docker.com"
    exit 1
fi

# Run LiveKit server with development configuration
docker run --rm \
  -p 7880:7880 \
  -p 7881:7881 \
  -p 50000-60000:50000-60000/udp \
  -v "$(pwd)/livekit-server-dev.yaml:/config.yaml" \
  livekit/livekit-server \
  --config /config.yaml \
  --node-ip 127.0.0.1

# Alternative: Run in dev mode without config file
# docker run --rm \
#   -p 7880:7880 \
#   -p 7881:7881 \
#   livekit/livekit-server \
#   --dev \
#   --bind 0.0.0.0