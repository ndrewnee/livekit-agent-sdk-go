#!/bin/bash
export AGENT_NAME="e2ee-web-agent"
export E2EE_PASSPHRASE="test-e2ee-secret-123"
export AUTO_ACTIVATE_RECORDING="true"
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
export S3_ENDPOINT="localhost:9000"
export S3_BUCKET="publisher-hls"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_PREFIX="web-e2ee-recordings"
export S3_USE_SSL="false"
export S3_FORCE_PATH_STYLE="true"
export S3_REALTIME_UPLOAD="true"

echo "Starting publisher-hls-agent with S3 config:"
echo "  S3_ENDPOINT=$S3_ENDPOINT"
echo "  S3_BUCKET=$S3_BUCKET"
echo "  S3_PREFIX=$S3_PREFIX"
echo "  S3_REALTIME_UPLOAD=$S3_REALTIME_UPLOAD"
echo ""

./publisher-hls-agent
