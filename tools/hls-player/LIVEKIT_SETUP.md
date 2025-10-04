# LiveKit Server Setup for Manual Testing

Quick guide to verify LiveKit server and run the real E2E manual test.

## Quick Start

### If You Already Have LiveKit Running

```bash
# Auto-detect server and get connection details
./tools/hls-player/detect-livekit.sh

# This will show:
#   ✓ Server status and PID
#   ✓ WebSocket and HTTP ports
#   ✓ Credentials (if running in --dev mode)
#   ✓ Environment variables to export

# Run manual test (auto-detects LiveKit)
./tools/hls-player/manual-test.sh
```

### If You Need To Start LiveKit

**Option 1: Docker**
```bash
docker run -d \
  -p 7880:7880 \
  -p 7881:7881 \
  -p 7882:7882/udp \
  --name livekit \
  livekit/livekit-server \
  --dev
```

**Option 2: Local Binary (if already installed)**
```bash
livekit-server --dev
```

## What You Get

The manual test now:
- ✅ Creates real LiveKit room
- ✅ Publishes from test.mp4 (10 seconds, 720p)
- ✅ Runs actual egress agent
- ✅ Generates production-quality HLS
- ✅ Saves to /tmp/hls-manual-test/

## Stop/Restart

**If using Docker:**
```bash
# Stop server
docker stop livekit

# Start again
docker start livekit

# Remove
docker rm livekit
```

**If using local binary:**
```bash
# Stop with Ctrl+C or:
pkill livekit-server
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
Another service is using the ports.

**If your local LiveKit uses different ports:**
```bash
# Set environment variables for the test
export LIVEKIT_URL="ws://localhost:YOUR_PORT"
export LIVEKIT_API_KEY="your_api_key"
export LIVEKIT_API_SECRET="your_api_secret"

# Run test
./tools/hls-player/manual-test.sh
```

### Test still fails

**Check server logs:**

If using Docker:
```bash
docker logs livekit
```

If using local binary:
```bash
# Logs usually go to stdout or check:
tail -f /var/log/livekit/livekit.log
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
# 1. Verify LiveKit is running
curl http://localhost:7881/validate

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
