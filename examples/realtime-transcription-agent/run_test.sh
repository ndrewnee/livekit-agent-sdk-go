#!/bin/bash

# run_test.sh - Script to run the OpenAI Realtime transcription integration test

set -e

echo "=== OpenAI Realtime API Integration Test ==="
echo

# Check for .env file
if [ ! -f "../../.env" ]; then
    echo "ERROR: .env file not found in project root"
    echo "Please create a .env file with your OpenAI API key:"
    echo "  OPENAI_API_KEY=your-key-here"
    exit 1
fi

# Check if LiveKit server is running
if ! curl -s http://localhost:7880 > /dev/null 2>&1; then
    echo "WARNING: LiveKit server doesn't appear to be running on localhost:7880"
    echo "Please start LiveKit server first:"
    echo "  docker run -d --rm -p 7880:7880 -e LIVEKIT_KEYS=devkey:secret livekit/livekit-server --dev"
    echo
    read -p "Press Enter to continue anyway, or Ctrl+C to exit..."
fi

# Check for audiobook.wav
if [ ! -f "../../audiobook.wav" ]; then
    echo "ERROR: audiobook.wav not found in project root"
    echo "Please place an audio file named 'audiobook.wav' in the project root"
    exit 1
fi

# Get dependencies
echo "Installing dependencies..."
go mod tidy

# Build the test
echo "Building integration test..."
go build -o realtime-test .

# Run the test
echo "Running integration test..."
echo "This will:"
echo "  1. Create a test room in LiveKit"
echo "  2. Start the transcription agent"
echo "  3. Publish audio from audiobook.wav"
echo "  4. Display transcriptions in real-time"
echo "  5. Run for 30 seconds (or press Ctrl+C to stop)"
echo

./realtime-test -mode=test

echo
echo "Test completed!"