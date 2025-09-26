#!/bin/bash

# Start MinIO Server Script for S3 Testing

set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
DATA_DIR="$SCRIPT_DIR/minio-data"

echo "================================"
echo "Starting MinIO Server"
echo "================================"

# Check if minio is installed
if ! command -v minio &> /dev/null; then
    echo "MinIO is not installed. Please install it first:"
    echo "brew install minio"
    exit 1
fi

# Create data directory
mkdir -p "$DATA_DIR"

echo "MinIO Server Configuration:"
echo "Endpoint: http://localhost:9000"
echo "Console: http://localhost:9001"
echo "Access Key: minioadmin"
echo "Secret Key: minioadmin"
echo ""
echo "Data Directory: $DATA_DIR"
echo ""
echo "Press Ctrl+C to stop the server"
echo "================================"

# Start MinIO server
export MINIO_ROOT_USER=minioadmin
export MINIO_ROOT_PASSWORD=minioadmin
minio server "$DATA_DIR" --console-address ":9001"