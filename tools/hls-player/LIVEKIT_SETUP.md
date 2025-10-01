# LiveKit Server Setup for Manual Testing

Quick guide to set up LiveKit server for running the real E2E manual test.

## Quick Start

```bash
# Start LiveKit server
docker run -d \
  -p 7880:7880 \
  -p 7881:7881 \
  -p 7882:7882/udp \
  --name livekit \
  livekit/livekit-server \
  --dev

# Verify it's running
curl http://localhost:7881/validate

# Run manual test
./tools/hls-player/manual-test.sh
```

## What You Get

The manual test now:
- ✅ Creates real LiveKit room
- ✅ Publishes from test.mp4 (10 seconds, 720p)
- ✅ Runs actual egress agent
- ✅ Generates production-quality HLS
- ✅ Saves to /tmp/hls-manual-test/

## Stop/Restart

```bash
# Stop server
docker stop livekit

# Start again
docker start livekit

# Remove
docker rm livekit
```

## Default Credentials

When running with `--dev` flag:
- **URL**: `ws://localhost:7880`
- **API Key**: `devkey`
- **API Secret**: `secret`

These are automatically used by the manual test.

## Checking Server Status

```bash
# HTTP health check
curl http://localhost:7881/validate

# View logs
docker logs livekit

# Follow logs
docker logs -f livekit
```

## Troubleshooting

### "Connection refused"
Server isn't running. Start with the docker command above.

### "Port already in use"
Another service is using the ports. Stop it or use different ports:
```bash
docker run -d \
  -p 8880:7880 \
  -p 8881:7881 \
  --name livekit \
  livekit/livekit-server --dev

# Update manual test
export LIVEKIT_URL="ws://localhost:8880"
./tools/hls-player/manual-test.sh
```

### Test still fails
Check logs:
```bash
docker logs livekit
```

Look for:
- Room creation
- Participant connection
- Track publishing

## Production Setup

For production, don't use `--dev`. Create a config file:

```yaml
# config.yaml
port: 7880
rtc:
  port_range_start: 50000
  port_range_end: 60000
keys:
  YOUR_API_KEY: YOUR_API_SECRET
```

Then run:
```bash
docker run -d \
  -p 7880:7880 \
  -p 50000-60000:50000-60000/udp \
  -v $(pwd)/config.yaml:/config.yaml \
  --name livekit \
  livekit/livekit-server \
  --config /config.yaml
```

## Alternative: Local Binary

Instead of Docker:

```bash
# macOS
brew install livekit

# Linux
wget https://github.com/livekit/livekit/releases/latest/download/livekit_linux_amd64
chmod +x livekit_linux_amd64
./livekit_linux_amd64 --dev
```

## Complete Test Workflow

```bash
# 1. Start LiveKit (if not running)
docker run -d -p 7880:7880 -p 7881:7881 --name livekit livekit/livekit-server --dev

# 2. Run manual test
./tools/hls-player/manual-test.sh

# Output:
# Running REAL E2E test with LiveKit room...
# This test:
#   • Creates real LiveKit room
#   • Publishes from test.mp4 (10 seconds, 720p)
#   • Captures with egress agent
#   • Generates HLS output
#
# ✓ Room created
# ✓ Participant connected
# ✓ Video track published
# ✓ Audio track published
# ✓ Egress agent started
# Capturing for 15s...
# ✓ Capture complete
# ✓ HLS output verified
#
# Output location: /tmp/hls-manual-test/manual-test-1759338954
#
# Start HLS player now? [y/N]
```

## What Gets Tested

With LiveKit + real media:

- **Real participant** publishing tracks
- **Actual network transport** (WebRTC)
- **Track subscription** by egress agent
- **Full LiveKit SDK** integration
- **Production egress pipeline**
- **Real Opus → AAC transcoding**
- **Proper HLS segmentation**

Much better than synthetic RTP injection! 🎯

## Resources

- LiveKit Docs: https://docs.livekit.io
- Docker Hub: https://hub.docker.com/r/livekit/livekit-server
- GitHub: https://github.com/livekit/livekit
