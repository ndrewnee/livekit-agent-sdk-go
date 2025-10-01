# End-to-End Testing Guide for LiveKit Egress

This guide covers comprehensive end-to-end testing of the egress system with real LiveKit server and MinIO storage.

## Prerequisites

### 1. LiveKit Server (Dev Mode)
The tests require a running LiveKit server in development mode.

```bash
# Start LiveKit server
livekit-server --dev --bind 0.0.0.0

# Default credentials (dev mode):
# URL: ws://localhost:7880
# API Key: devkey
# API Secret: secret
```

### 2. MinIO Storage
The tests require MinIO for HLS segment storage.

```bash
# Start MinIO using Docker
docker run -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  quay.io/minio/minio server /data --console-address ":9001"

# Or use existing MinIO installation
# Web Console: http://localhost:9001
# API Endpoint: http://localhost:9000
```

### 3. Create MinIO Bucket

```bash
# Using mc (MinIO Client)
mc alias set local http://localhost:9000 minioadmin minioadmin
mc mb local/egress-test

# Or use the web console at http://localhost:9001
```

## Environment Configuration

Set these environment variables (or use defaults):

```bash
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"

export MINIO_ENDPOINT="localhost:9000"
export MINIO_ACCESS_KEY="minioadmin"
export MINIO_SECRET_KEY="minioadmin"
export MINIO_BUCKET="egress-test"
```

## Running E2E Tests

### Run All E2E Tests

```bash
# Run with e2e build tag
go test -tags=e2e -v ./pkg/egress -run TestE2E

# Run specific test
go test -tags=e2e -v ./pkg/egress -run TestE2ECompleteEgressWorkflow

# Run with timeout (recommended for long tests)
go test -tags=e2e -v -timeout 5m ./pkg/egress -run TestE2E
```

### Available Test Scenarios

#### 1. Complete Egress Workflow
Tests the full pipeline: room creation → track publishing → HLS capture → MinIO upload → verification

```bash
go test -tags=e2e -v ./pkg/egress -run TestE2ECompleteEgressWorkflow
```

#### 2. Multiple Participants
Tests egress with multiple simultaneous participants

```bash
go test -tags=e2e -v ./pkg/egress -run TestE2EMultipleParticipants
```

#### 3. Long Running Session
Tests egress over an extended period (60 seconds)

```bash
go test -tags=e2e -v ./pkg/egress -run TestE2ELongRunningSession
```

## Verifying Results

### 1. Check MinIO Storage

Open MinIO web console: http://localhost:9001

Navigate to `egress-test` bucket to see uploaded HLS segments.

### 2. Use HLS Player

Open the HLS player in a web browser:

```bash
# Serve the player locally
cd tools/hls-player
python3 -m http.server 8080

# Open in browser
open http://localhost:8080/player.html
```

Enter the HLS playlist URL from MinIO:
```
http://localhost:9000/egress-test/session-{timestamp}/playlist.m3u8
```

### 3. Manual Verification

```bash
# List files in MinIO bucket
mc ls local/egress-test/

# Download and inspect playlist
mc cp local/egress-test/session-*/playlist.m3u8 ./test.m3u8
cat test.m3u8

# Verify with ffprobe
ffprobe -v quiet -print_format json -show_format -show_streams \
  http://localhost:9000/egress-test/session-*/playlist.m3u8
```

## Test Data Files

The tests use real media files from `examples/egress-agent/test-data/`:

- `test-video-h264.mp4` - H.264 video (1280x720, 10 seconds, 2.6MB)
- `test-audio-opus.ogg` - Opus audio (80KB)
- `test-audio-aac.m4a` - AAC audio (159KB)
- `test-pattern.mp4` - Test pattern video (39KB)

## Architecture

### Test Flow

```
┌─────────────────┐
│ LiveKit Server  │
│   (Dev Mode)    │
└────────┬────────┘
         │
         │ WebRTC
         ▼
┌─────────────────┐
│   Test Client   │
│ (Publishes A/V) │
└────────┬────────┘
         │
         │ Room Join + Tracks
         ▼
┌─────────────────┐
│ Egress Worker   │
│ (Our Code)      │
└────────┬────────┘
         │
         │ HLS Segments
         ▼
┌─────────────────┐
│     MinIO       │
│   (Storage)     │
└─────────────────┘
         │
         │ HTTP
         ▼
┌─────────────────┐
│   HLS Player    │
│  (Verification) │
└─────────────────┘
```

### Components Tested

1. **LiveKit Integration**
   - Room creation and management
   - Participant connection
   - Track publishing (audio/video)
   - Track subscription

2. **Egress Pipeline**
   - GStreamer pipeline initialization
   - RTP packet reception
   - H.264/Opus decoding
   - HLS muxing and segmentation

3. **Storage Integration**
   - MinIO client setup
   - File upload (segments + playlist)
   - Access verification

4. **Quality Assurance**
   - HLS structure validation
   - Segment integrity
   - Playback verification

## Troubleshooting

### LiveKit Connection Issues

```bash
# Check LiveKit is running
curl http://localhost:7880/healthz

# Check WebSocket connection
wscat -c ws://localhost:7880
```

### MinIO Connection Issues

```bash
# Check MinIO is running
curl http://localhost:9000/minio/health/live

# Test bucket access
mc ls local/egress-test/
```

### CORS Issues with HLS Player

Add CORS policy to MinIO bucket:

```bash
mc admin policy set local/ public
```

Or configure CORS properly:

```json
{
  "CORSRules": [
    {
      "AllowedOrigins": ["*"],
      "AllowedMethods": ["GET", "HEAD"],
      "AllowedHeaders": ["*"]
    }
  ]
}
```

### GStreamer Issues

```bash
# Check GStreamer installation
gst-inspect-1.0 --version

# List available plugins
gst-inspect-1.0 | grep -E "(h264|opus|hls)"

# Test pipeline manually
gst-launch-1.0 videotestsrc ! x264enc ! hlssink
```

### Common Test Failures

1. **"Failed to create room"**
   - Verify LiveKit server is running
   - Check API credentials
   - Ensure network connectivity

2. **"Failed to upload to MinIO"**
   - Verify MinIO is running
   - Check bucket exists
   - Verify credentials

3. **"No HLS segments generated"**
   - Check GStreamer plugins installed
   - Verify pipeline creation
   - Check output directory permissions

4. **"HLS player shows CORS error"**
   - Configure MinIO CORS
   - Use MinIO proxy
   - Serve player from same origin

## Performance Benchmarks

Expected performance for E2E tests:

- **Setup time**: < 2 seconds
- **First segment**: < 3 seconds
- **Segment generation**: ~2 seconds per segment
- **Upload latency**: < 100ms per segment
- **Total test duration**: 15-90 seconds depending on scenario

## CI/CD Integration

### GitHub Actions Example

```yaml
name: E2E Tests

on: [push, pull_request]

jobs:
  e2e-test:
    runs-on: ubuntu-latest

    services:
      livekit:
        image: livekit/livekit-server:latest
        ports:
          - 7880:7880
        options: --dev

      minio:
        image: quay.io/minio/minio
        ports:
          - 9000:9000
        env:
          MINIO_ROOT_USER: minioadmin
          MINIO_ROOT_PASSWORD: minioadmin

    steps:
      - uses: actions/checkout@v3

      - name: Setup Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.21'

      - name: Install GStreamer
        run: |
          sudo apt-get update
          sudo apt-get install -y gstreamer1.0-tools \
            gstreamer1.0-plugins-base \
            gstreamer1.0-plugins-good \
            gstreamer1.0-plugins-bad \
            libgstreamer1.0-dev

      - name: Create MinIO bucket
        run: |
          mc alias set myminio http://localhost:9000 minioadmin minioadmin
          mc mb myminio/egress-test

      - name: Run E2E Tests
        run: go test -tags=e2e -v -timeout 10m ./pkg/egress
        env:
          LIVEKIT_URL: ws://localhost:7880
          LIVEKIT_API_KEY: devkey
          LIVEKIT_API_SECRET: secret
          MINIO_ENDPOINT: localhost:9000
          MINIO_ACCESS_KEY: minioadmin
          MINIO_SECRET_KEY: minioadmin
          MINIO_BUCKET: egress-test
```

## Best Practices

1. **Cleanup**: Tests automatically clean up rooms and files
2. **Isolation**: Each test uses unique room names and session IDs
3. **Timeouts**: All tests have appropriate timeouts
4. **Logging**: Verbose logging helps diagnose issues
5. **Idempotency**: Tests can be run multiple times safely

## Future Enhancements

- [ ] Test different video codecs (VP8, VP9, AV1)
- [ ] Test adaptive bitrate (ABR)
- [ ] Test network failure scenarios
- [ ] Test concurrent egress sessions
- [ ] Test different segment durations
- [ ] Performance profiling and benchmarks
- [ ] Automated quality assessment (VMAF, SSIM)

## Support

For issues or questions:
- Check logs in `/tmp/egress-test-*`
- Review GStreamer pipeline dumps
- Enable debug logging: `GST_DEBUG=3`
- Consult LiveKit documentation: https://docs.livekit.io