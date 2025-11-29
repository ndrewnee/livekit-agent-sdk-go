#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

VIDEO_URL="https://sync-one2.harkwood.co.uk/media/Sync-One2_Test_720p_59.94_H.264_PCM_Stereo.mov.zip"
ZIP_FILE="source_video.zip"
OUTPUT_FILE="test.mp4"

echo "Downloading test video from $VIDEO_URL..."
curl -L -o "$ZIP_FILE" "$VIDEO_URL"

echo "Extracting archive..."
unzip -o "$ZIP_FILE"

# Find the extracted .mov file
MOV_FILE=$(find . -maxdepth 1 -name "*.mov" -type f | head -1)

if [ -z "$MOV_FILE" ]; then
    echo "Error: No .mov file found in archive"
    exit 1
fi

echo "Converting $MOV_FILE to H.264 + Opus..."
ffmpeg -i "$MOV_FILE" \
    -c:v copy \
    -c:a libopus -b:a 128k \
    -y "$OUTPUT_FILE"

echo "Cleaning up temporary files..."
rm -f "$ZIP_FILE" "$MOV_FILE"
rm -rf __MACOSX

echo "Done! Test video created: $OUTPUT_FILE"
ls -lh "$OUTPUT_FILE"
