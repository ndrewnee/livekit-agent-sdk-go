#!/bin/bash

# Generate Test Media Files Script

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
TEST_DATA_DIR="$SCRIPT_DIR/test-data"

echo "================================"
echo "Generating Test Media Files"
echo "================================"

# Create test data directory
mkdir -p "$TEST_DATA_DIR"

echo "Creating test video files..."

# Generate a 10-second H.264 test video with audio
echo "1. Generating test-video-h264.mp4 (10s, 1280x720, H.264 + AAC)..."
gst-launch-1.0 -e \
    videotestsrc pattern=smpte num-buffers=300 ! \
    video/x-raw,width=1280,height=720,framerate=30/1 ! \
    x264enc ! mp4mux name=mux ! \
    filesink location="$TEST_DATA_DIR/test-video-h264.mp4" \
    audiotestsrc wave=sine freq=440 num-buffers=470 ! \
    audio/x-raw,rate=48000,channels=2 ! \
    avenc_aac ! mux. \
    2>/dev/null

# Generate a 5-second H.264 test pattern
echo "2. Generating test-pattern.mp4 (5s, 1920x1080, H.264)..."
gst-launch-1.0 -e \
    videotestsrc pattern=ball num-buffers=150 ! \
    video/x-raw,width=1920,height=1080,framerate=30/1 ! \
    x264enc ! mp4mux ! \
    filesink location="$TEST_DATA_DIR/test-pattern.mp4" \
    2>/dev/null

echo ""
echo "Creating test audio files..."

# Generate Opus audio file
echo "3. Generating test-audio-opus.ogg (10s, Opus)..."
gst-launch-1.0 -e \
    audiotestsrc wave=sine freq=440 num-buffers=470 ! \
    audio/x-raw,rate=48000,channels=2 ! \
    opusenc ! oggmux ! \
    filesink location="$TEST_DATA_DIR/test-audio-opus.ogg" \
    2>/dev/null

# Generate MP3 audio file
echo "4. Generating test-audio-mp3.mp3 (10s, MP3)..."
gst-launch-1.0 -e \
    audiotestsrc wave=sine freq=440 num-buffers=470 ! \
    audio/x-raw,rate=48000,channels=2 ! \
    lamemp3enc ! \
    filesink location="$TEST_DATA_DIR/test-audio-mp3.mp3" \
    2>/dev/null

# Generate AAC audio file
echo "5. Generating test-audio-aac.m4a (10s, AAC)..."
gst-launch-1.0 -e \
    audiotestsrc wave=sine freq=440 num-buffers=470 ! \
    audio/x-raw,rate=48000,channels=2 ! \
    avenc_aac ! mp4mux ! \
    filesink location="$TEST_DATA_DIR/test-audio-aac.m4a" \
    2>/dev/null

echo ""
echo "Creating sample HLS stream..."

# Generate sample HLS stream
HLS_DIR="$TEST_DATA_DIR/sample-hls"
mkdir -p "$HLS_DIR"

echo "6. Generating sample HLS stream..."
gst-launch-1.0 -e \
    videotestsrc pattern=snow num-buffers=300 ! \
    video/x-raw,width=1280,height=720,framerate=30/1 ! \
    x264enc ! h264parse config-interval=-1 ! \
    mpegtsmux name=mux ! \
    hlssink2 \
        location="$HLS_DIR/segment%05d.ts" \
        playlist-location="$HLS_DIR/playlist.m3u8" \
        target-duration=4 \
        max-files=0 \
    audiotestsrc wave=pink-noise num-buffers=470 ! \
    audio/x-raw,rate=48000,channels=2 ! \
    opusenc ! opusparse ! mux. \
    2>/dev/null

echo ""
echo "================================"
echo "Test Media Files Generated!"
echo "================================"
echo ""
echo "Generated files:"
echo "- $TEST_DATA_DIR/test-video-h264.mp4"
echo "- $TEST_DATA_DIR/test-pattern.mp4"
echo "- $TEST_DATA_DIR/test-audio-opus.ogg"
echo "- $TEST_DATA_DIR/test-audio-mp3.mp3"
echo "- $TEST_DATA_DIR/test-audio-aac.m4a"
echo "- $HLS_DIR/playlist.m3u8 (HLS stream)"
echo ""

# Display file information
echo "File details:"
ls -lh "$TEST_DATA_DIR"/*.{mp4,ogg,mp3,m4a} 2>/dev/null || true
echo ""
echo "HLS segments:"
ls -lh "$HLS_DIR"/*.ts 2>/dev/null | head -5 || true
echo "..."