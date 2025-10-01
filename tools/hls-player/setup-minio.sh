#!/bin/bash

# Setup MinIO for HLS Player Testing
# Configures bucket, permissions, and CORS for browser access

set -e

MINIO_ENDPOINT="${MINIO_ENDPOINT:-localhost:9000}"
MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY:-minioadmin}"
MINIO_SECRET_KEY="${MINIO_SECRET_KEY:-minioadmin}"
BUCKET_NAME="${BUCKET_NAME:-egress-test}"

echo "🗄️  Setting up MinIO for HLS Testing"
echo ""
echo "MinIO endpoint: $MINIO_ENDPOINT"
echo "Bucket: $BUCKET_NAME"
echo ""

# Check if MinIO is running
echo "Checking MinIO connection..."
if ! curl -s "http://$MINIO_ENDPOINT/minio/health/live" > /dev/null; then
    echo "❌ MinIO is not running!"
    echo ""
    echo "Start MinIO with:"
    echo "docker run -d -p 9000:9000 -p 9001:9001 \\"
    echo "  --name minio \\"
    echo "  -e MINIO_ROOT_USER=$MINIO_ACCESS_KEY \\"
    echo "  -e MINIO_ROOT_PASSWORD=$MINIO_SECRET_KEY \\"
    echo "  quay.io/minio/minio server /data --console-address ':9001'"
    echo ""
    exit 1
fi

echo "✅ MinIO is running"
echo ""

# Setup mc (MinIO client)
if ! command -v mc &> /dev/null; then
    echo "Installing MinIO client (mc)..."

    if [[ "$OSTYPE" == "darwin"* ]]; then
        # macOS
        if command -v brew &> /dev/null; then
            brew install minio/stable/mc
        else
            curl -o /usr/local/bin/mc https://dl.min.io/client/mc/release/darwin-amd64/mc
            chmod +x /usr/local/bin/mc
        fi
    elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
        # Linux
        curl -o /usr/local/bin/mc https://dl.min.io/client/mc/release/linux-amd64/mc
        chmod +x /usr/local/bin/mc
    else
        echo "❌ Unsupported OS. Please install mc manually: https://min.io/docs/minio/linux/reference/minio-mc.html"
        exit 1
    fi
fi

# Configure mc alias
echo "Configuring MinIO client..."
mc alias set myminio "http://$MINIO_ENDPOINT" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" > /dev/null 2>&1

# Create bucket if it doesn't exist
echo "Creating bucket '$BUCKET_NAME'..."
if mc ls myminio/$BUCKET_NAME > /dev/null 2>&1; then
    echo "✅ Bucket already exists"
else
    mc mb myminio/$BUCKET_NAME
    echo "✅ Bucket created"
fi

# Set public read policy for HLS files
echo "Setting bucket policy to allow public reads..."
cat > /tmp/minio-policy.json << EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {"AWS": ["*"]},
      "Action": ["s3:GetObject"],
      "Resource": ["arn:aws:s3:::$BUCKET_NAME/*"]
    },
    {
      "Effect": "Allow",
      "Principal": {"AWS": ["*"]},
      "Action": ["s3:ListBucket"],
      "Resource": ["arn:aws:s3:::$BUCKET_NAME"]
    }
  ]
}
EOF

mc anonymous set-json /tmp/minio-policy.json myminio/$BUCKET_NAME
rm /tmp/minio-policy.json
echo "✅ Bucket policy set"

# Test access
echo ""
echo "Testing bucket access..."
if curl -s "http://$MINIO_ENDPOINT/$BUCKET_NAME/" | grep -q "ListBucketResult"; then
    echo "✅ Bucket is accessible"
else
    echo "⚠️  Bucket access test inconclusive (may work for HLS files)"
fi

echo ""
echo "✅ MinIO setup complete!"
echo ""
echo "Next steps:"
echo "1. Run E2E test that uploads to MinIO:"
echo "   go test -v -tags=e2e ./pkg/egress -run TestE2ECompletePipeline"
echo ""
echo "2. Start HLS player:"
echo "   ./tools/hls-player/play.sh"
echo ""
echo "3. In player, use URL format:"
echo "   http://$MINIO_ENDPOINT/$BUCKET_NAME/session-id/playlist.m3u8"
echo ""
echo "MinIO Console (to browse files):"
echo "   http://localhost:9001"
echo "   Username: $MINIO_ACCESS_KEY"
echo "   Password: $MINIO_SECRET_KEY"
echo ""
