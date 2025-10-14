# Publisher HLS Agent

A LiveKit agent that records publisher tracks to HLS (HTTP Live Streaming) format with optional S3 upload.

## Overview

The Publisher HLS Agent is a specialized LiveKit agent that:
- Records audio and video from specified participants in real-time
- Generates HLS playlists and segments for adaptive streaming
- Uploads recordings to S3-compatible storage (optional)
- Supports auto-activation or manual recording control
- Uses GStreamer for efficient media processing

## Features

- **Real-time HLS Generation**: Creates live HLS playlists and segments as the stream progresses
- **H.264 + AAC Encoding**: Transcodes video to H.264 and audio to AAC for broad compatibility
- **S3 Upload**: Automatically uploads completed recordings to S3/MinIO/compatible storage
- **Auto-Activation**: Optionally start recording automatically when tracks are ready
- **Manual Control**: Programmatic API to control recording activation
- **High Quality**: Requests HIGH video quality from LiveKit publishers
- **Delayed Pipeline Start**: Ensures HLS segments start with valid keyframes for immediate playability

## Architecture

```
┌─────────────┐
│  Publisher  │ (Target Participant)
└──────┬──────┘
       │ RTP Video (H.264) + Audio (Opus)
       │
       ▼
┌──────────────────────────────────────┐
│     Publisher HLS Agent              │
│  ┌────────────────────────────────┐  │
│  │  LiveKit Room Connection       │  │
│  │  - Subscribes to target tracks │  │
│  │  - Requests HIGH video quality │  │
│  └────────────┬───────────────────┘  │
│               │                      │
│  ┌────────────▼───────────────────┐  │
│  │  GStreamer Pipeline            │  │
│  │  ┌──────────────────────────┐  │  │
│  │  │ Video: rtpjitterbuffer → │  │  │
│  │  │ rtph264depay → h264parse │  │  │
│  │  └──────────┬───────────────┘  │  │
│  │  ┌──────────▼───────────────┐  │  │
│  │  │ Audio: rtpjitterbuffer → │  │  │
│  │  │ rtpopusdepay → opusdec → │  │  │
│  │  │ audioconvert → avenc_aac │  │  │
│  │  └──────────┬───────────────┘  │  │
│  │  ┌──────────▼───────────────┐  │  │
│  │  │ mpegtsmux → tee          │  │  │
│  │  │   ├─→ filesink (output)  │  │  │
│  │  │   └─→ hlssink (segments) │  │  │
│  │  └──────────────────────────┘  │  │
│  └────────────┬───────────────────┘  │
│               │                      │
│  ┌────────────▼───────────────────┐  │
│  │  Output                        │  │
│  │  - output.ts (full recording)  │  │
│  │  - playlist.m3u8               │  │
│  │  - segment*.ts (HLS chunks)    │  │
│  └────────────┬───────────────────┘  │
│               │                      │
│  ┌────────────▼───────────────────┐  │
│  │  S3 Uploader (optional)        │  │
│  │  - Uploads all files to bucket │  │
│  └────────────────────────────────┘  │
└──────────────────────────────────────┘
```

## Requirements

### System Dependencies

- **Go 1.21+**
- **GStreamer 1.26+** with the following plugins:
  - gst-plugins-base (rtpjitterbuffer, audioconvert)
  - gst-plugins-good (rtph264depay, rtpopusdepay)
  - gst-libav (avenc_aac, h264parse)
  - gst-plugins-bad (mpegtsmux, hlssink)

### Installing GStreamer

**macOS (Homebrew)**:
```bash
brew install gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad gst-libav
```

**Ubuntu/Debian**:
```bash
sudo apt-get install \
  libgstreamer1.0-dev \
  libgstreamer-plugins-base1.0-dev \
  libgstreamer-plugins-good1.0-dev \
  libgstreamer-plugins-bad1.0-dev \
  gstreamer1.0-libav \
  gstreamer1.0-plugins-ugly
```

**Fedora/RHEL**:
```bash
sudo dnf install \
  gstreamer1-devel \
  gstreamer1-plugins-base-devel \
  gstreamer1-plugins-good \
  gstreamer1-plugins-bad-free \
  gstreamer1-libav
```

## Installation

```bash
git clone https://github.com/am-sokolov/livekit-agent-sdk-go.git
cd livekit-agent-sdk-go/examples/publisher-hls-agent
go build -o publisher-hls-agent
```

## Configuration

All configuration is done via environment variables:

### Required Variables

| Variable | Description | Example |
|----------|-------------|---------|
| `LIVEKIT_API_KEY` | LiveKit API key | `APIxxxxxxxxxxxx` |
| `LIVEKIT_API_SECRET` | LiveKit API secret | `secretxxxxxxxxx` |

### Optional Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LIVEKIT_URL` | `ws://localhost:7880` | LiveKit server WebSocket URL |
| `AGENT_NAME` | `publisher-hls-recorder` | Agent name for job matching |
| `OUTPUT_DIR` | `publisher-hls-output` | Local directory for recordings |
| `AUTO_ACTIVATE_RECORDING` | `false` | Auto-start recording when tracks ready |
| `HLS_SEGMENT_DURATION` | `2` | HLS segment duration in seconds |
| `HLS_MAX_SEGMENTS` | `0` | Max segments in playlist (0 = unlimited) |

### S3 Upload Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | - | S3 endpoint URL (e.g., `s3.amazonaws.com`) |
| `S3_BUCKET` | - | S3 bucket name |
| `S3_REGION` | `us-east-1` | S3 region |
| `S3_ACCESS_KEY` | - | S3 access key |
| `S3_SECRET_KEY` | - | S3 secret key |
| `S3_SESSION_TOKEN` | - | S3 session token (optional) |
| `S3_PREFIX` | - | S3 object key prefix |
| `S3_USE_SSL` | `false` | Use HTTPS for S3 |
| `S3_FORCE_PATH_STYLE` | `true` | Use path-style S3 URLs |
| `S3_OBJECT_ACL` | - | S3 object ACL (e.g., `public-read`) |

## Usage

### Basic Usage

1. **Create a configuration file** (optional):

```bash
# config.env
export LIVEKIT_URL="wss://your-livekit-server.com"
export LIVEKIT_API_KEY="your-api-key"
export LIVEKIT_API_SECRET="your-api-secret"
export OUTPUT_DIR="./recordings"
export AUTO_ACTIVATE_RECORDING="true"
```

2. **Run the agent**:

```bash
source config.env
./publisher-hls-agent
```

3. **Dispatch a recording job**:

Using the LiveKit CLI:
```bash
livekit-cli agent dispatch \
  --room "your-room-name" \
  --participant-identity "publisher-to-record" \
  --agent-name "publisher-hls-recorder" \
  --job-type JT_PUBLISHER
```

Or using the LiveKit API:
```go
import (
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

client := lksdk.NewRoomServiceClient(serverURL, apiKey, apiSecret)
_, err := client.StartAgentDispatch(ctx, &livekit.StartAgentDispatchRequest{
    Room: "your-room-name",
    AgentName: "publisher-hls-recorder",
    Metadata: `{"participant":"publisher-to-record"}`,
})
```

### With S3 Upload

```bash
export LIVEKIT_URL="wss://your-livekit-server.com"
export LIVEKIT_API_KEY="your-api-key"
export LIVEKIT_API_SECRET="your-api-secret"

# S3 Configuration
export S3_ENDPOINT="s3.amazonaws.com"
export S3_BUCKET="my-recordings"
export S3_REGION="us-west-2"
export S3_ACCESS_KEY="AKIAIOSFODNN7EXAMPLE"
export S3_SECRET_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
export S3_PREFIX="hls-recordings"

./publisher-hls-agent
```

### With MinIO (Local S3-Compatible Storage)

```bash
# Start MinIO
docker run -p 9000:9000 -p 9001:9001 \
  -e "MINIO_ROOT_USER=minioadmin" \
  -e "MINIO_ROOT_PASSWORD=minioadmin" \
  minio/minio server /data --console-address ":9001"

# Configure agent for MinIO
export S3_ENDPOINT="localhost:9000"
export S3_BUCKET="recordings"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_USE_SSL="false"
export S3_FORCE_PATH_STYLE="true"

./publisher-hls-agent
```

## Output Files

For each recording, the agent creates:

```
<OUTPUT_DIR>/
  <room-name>/
    <participant-identity>/
      output.ts           # Full recording in MPEG-TS format
      playlist.m3u8       # HLS master playlist
      segment00000.ts     # HLS segment 0
      segment00001.ts     # HLS segment 1
      ...
```

### HLS Playlist Format

The generated `playlist.m3u8` follows the HLS specification:

```m3u8
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:2.000,
segment00000.ts
#EXTINF:2.000,
segment00001.ts
...
```

## Programmatic Control

### Manual Recording Activation

If `AUTO_ACTIVATE_RECORDING=false`, you can control recording via the handler:

```go
handler := NewPublisherHLSHandler(cfg)

// Wait for handler to be ready (tracks subscribed)
if err := handler.WaitReady(ctx); err != nil {
    log.Fatal(err)
}

// Activate recording for specific participant
if err := handler.ActivateRecording("participant-identity"); err != nil {
    log.Fatal(err)
}
```

## Testing

### Unit Tests

```bash
go test -v -run TestPublisherHLSHandler
```

### E2E Integration Tests

The repository includes end-to-end tests that:
- Start a local LiveKit server
- Create a synthetic publisher with H.264 video + Opus audio
- Verify HLS recording and S3 upload

```bash
# Run E2E test
go test -v -run TestPublisherHLSAgentUploadsToS3

# Keep MinIO running after test for inspection
PUBLISHER_HLS_KEEP_MINIO=1 go test -v -run TestPublisherHLSAgentUploadsToS3
```

## Deployment

### Docker

Create a `Dockerfile`:

```dockerfile
FROM golang:1.21 AS builder

# Install GStreamer
RUN apt-get update && apt-get install -y \
    libgstreamer1.0-dev \
    libgstreamer-plugins-base1.0-dev \
    gstreamer1.0-plugins-good \
    gstreamer1.0-plugins-bad \
    gstreamer1.0-libav

WORKDIR /app
COPY . .
RUN go build -o publisher-hls-agent

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    gstreamer1.0-plugins-base \
    gstreamer1.0-plugins-good \
    gstreamer1.0-plugins-bad \
    gstreamer1.0-libav \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/publisher-hls-agent /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/publisher-hls-agent"]
```

Build and run:
```bash
docker build -t publisher-hls-agent .
docker run --rm \
  -e LIVEKIT_URL="wss://your-server.com" \
  -e LIVEKIT_API_KEY="your-key" \
  -e LIVEKIT_API_SECRET="your-secret" \
  publisher-hls-agent
```

### Systemd Service

Create `/etc/systemd/system/publisher-hls-agent.service`:

```ini
[Unit]
Description=LiveKit Publisher HLS Recording Agent
After=network.target

[Service]
Type=simple
User=livekit
Group=livekit
WorkingDirectory=/opt/livekit-agent
EnvironmentFile=/etc/livekit-agent/config.env
ExecStart=/opt/livekit-agent/publisher-hls-agent
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

Enable and start:
```bash
sudo systemctl daemon-reload
sudo systemctl enable publisher-hls-agent
sudo systemctl start publisher-hls-agent
sudo journalctl -u publisher-hls-agent -f
```

## Troubleshooting

### No HLS Output

**Problem**: Agent runs but no HLS files are created.

**Solutions**:
1. Check that recording was activated:
   ```bash
   # Set AUTO_ACTIVATE_RECORDING=true or manually activate
   ```
2. Verify GStreamer pipeline logs:
   ```bash
   export GST_DEBUG=3
   ./publisher-hls-agent
   ```
3. Ensure target participant is publishing video+audio

### Invalid HLS Segments

**Problem**: HLS segments cannot be played or have errors.

**Solutions**:
1. Verify H.264 codec is used by publisher (not VP8/VP9)
2. Check ffprobe output for errors:
   ```bash
   ffprobe segment00000.ts
   ```
3. Ensure proper SPS/PPS headers (the agent handles this automatically)

### S3 Upload Fails

**Problem**: Recording succeeds but S3 upload fails.

**Solutions**:
1. Verify S3 credentials:
   ```bash
   aws s3 ls s3://$S3_BUCKET --endpoint-url http://$S3_ENDPOINT
   ```
2. Check bucket permissions (agent needs `s3:PutObject`)
3. Enable debug logging for S3 errors

### High CPU Usage

**Problem**: Agent consumes excessive CPU.

**Solutions**:
1. Reduce HLS segment duration (currently 2s default)
2. Limit concurrent jobs via `MaxJobs` in worker options
3. Use hardware-accelerated GStreamer elements if available

## Performance Considerations

- **CPU**: ~50-100% of one core per active recording (H.264 encoding)
- **Memory**: ~100-200 MB per recording session
- **Disk I/O**: Writes HLS segments every 2 seconds (configurable)
- **Network**: Minimal (only receives RTP, uploads to S3 at end)

## Architecture Details

### Recording Flow

1. **Job Assignment**: LiveKit dispatches `JT_PUBLISHER` job to agent
2. **Room Connection**: Agent connects to room as "HLS Recorder" participant
3. **Track Subscription**: Subscribes to target participant's video+audio
4. **Pipeline Initialization**: Creates GStreamer pipeline (but doesn't start it)
5. **Handshake**: Waits for first keyframe to establish SPS/PPS parameters
6. **Recording Activation**: Auto or manual activation
7. **Delayed Pipeline Start**: On first recording keyframe, starts GStreamer
8. **HLS Generation**: hlssink creates segments and updates playlist
9. **Upload**: On completion, uploads all files to S3 if configured

### Why Delayed Pipeline Start?

The agent implements a **delayed pipeline start** mechanism to ensure HLS segments are immediately playable:

- **Problem**: Starting the pipeline before recording begins creates invalid initial segments without SPS/PPS headers
- **Solution**: Pipeline starts only when the first valid recording keyframe arrives
- **Benefit**: All HLS segments start with proper H.264 metadata, ensuring compatibility with all players (VLC, hls.js, Safari, etc.)

## Contributing

Contributions welcome! Please:
1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Submit a pull request

## License

See LICENSE file in repository root.

## Support

- **LiveKit Documentation**: https://docs.livekit.io
- **LiveKit Community**: https://livekit.io/community
- **Issues**: https://github.com/am-sokolov/livekit-agent-sdk-go/issues
