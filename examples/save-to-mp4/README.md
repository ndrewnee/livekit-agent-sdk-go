# Save to HLS Example

A simple application that shows how to receive video and audio using Pion WebRTC and save to HLS container. **Based on save-to-webm.go but outputs HLS instead of WebM.**

## Overview

This example:
- Uses **pure Pion WebRTC** (no LiveKit required)
- Receives H.264 video and Opus audio via WebRTC
- Saves to HLS segments using MPEG-TS format
- Uses **manual signaling** (paste offer/answer in console)

## Prerequisites

- **FFmpeg** installed (for testing/playback verification)
- **Go 1.22+**
- A WebRTC source (browser, ffmpeg, or test publisher)

**NO LiveKit server needed!**

## Quick Start

### Step 1: Start the HLS receiver

```bash
cd examples/save-to-hls
go run main.go
```

The application will wait for you to paste a WebRTC offer.

### Step 2: Publish video via WebRTC

You have several options:

#### Option A: Using test.mp4 with publisher tool

```bash
# In another terminal, create a simple publisher using the test video
# (You'll need to create a companion tool or use ffmpeg - see below)
```

#### Option B: Using a browser

1. Open https://webrtc.github.io/samples/src/content/peerconnection/pc1/
2. Click "Start" to create local video
3. In the receiver terminal, you'll see it waiting for an offer
4. The browser will generate an offer
5. Paste the offer into the receiver's console
6. Copy the answer from the receiver
7. Paste the answer back into the browser

#### Option C: Using FFmpeg as WebRTC publisher

```bash
# Install webrtc-streamer or use gstreamer with webrtc plugin
# This is more complex - see WEBRTC_PUBLISHING.md for details
```

### Step 3: Record

Once connected, the application will:
- Receive RTP packets
- Save to HLS segments every 2 seconds
- Output to `/tmp/hls-output/hls-session-{timestamp}_final/`

Press **Ctrl+C** to stop and finalize the HLS output.

### Step 4: Play the HLS stream

```bash
# Create a playlist manually (see Creating Playlist below)

# Start HTTP server
cd /tmp/hls-output
python3 -m http.server 8080

# Open in browser or VLC
http://localhost:8080/hls-session-{timestamp}_final/playlist.m3u8
```

## How It Works

```
WebRTC Offer/Answer (manual signaling)
          ↓
   Pion WebRTC Connection
          ↓
   RTP Packets (H.264 + Opus)
          ↓
      HLS Saver
          ↓
   MPEG-TS Segments
```

### Comparison with save-to-webm.go

| Feature | save-to-webm.go | save-to-hls (this) |
|---------|-----------------|---------------------|
| Transport | Pion WebRTC | Pion WebRTC |
| Signaling | stdin/stdout | stdin/stdout |
| Video codec | VP8 or H.264 | H.264 only |
| Audio codec | Opus | Opus |
| Output format | WebM | HLS (MPEG-TS) |
| Output structure | Single file | Multiple segments + playlist |
| Streaming | No | Yes (segments) |

## Output Structure

```
/tmp/hls-output/
└── hls-session-{timestamp}_final/
    ├── {hash}_main_seg0.ts    # Segment 0 (~2 seconds)
    ├── {hash}_main_seg1.ts    # Segment 1 (~2 seconds)
    ├── {hash}_main_seg2.ts    # Segment 2 (~2 seconds)
    └── ...
```

## Creating the Playlist

After recording, you must create `playlist.m3u8` manually:

```bash
cd /tmp/hls-output/hls-session-{timestamp}_final/

# Create playlist
cat > playlist.m3u8 << 'EOF'
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-TARGETDURATION:2

#EXTINF:2.0,
{hash}_main_seg0.ts
#EXTINF:2.0,
{hash}_main_seg1.ts
#EXTINF:2.0,
{hash}_main_seg2.ts
#EXT-X-ENDLIST
EOF
```

Replace `{hash}` with the actual filename prefix from your directory.

## Testing with test.mp4

To use `examples/egress-agent/test-data/test.mp4` as source, you need to:

1. **Extract and publish via WebRTC** - You'll need a companion publisher tool
2. **Use the automated test** - Run `go test` which does this automatically

See `main_test.go` for the automated test that publishes test.mp4 via WebRTC.

## Configuration

Edit `main.go` constants:

```go
const (
    outputDir       = "/tmp/hls-output"     // Output directory
    segmentDuration = 2 * time.Second       // Segment duration
)
```

## Known Limitations

- **Audio disabled**: Audio track initialization is currently disabled in HLSSaver
- **Short recordings**: Due to timestamp issues, recordings may be shorter than expected (~11-12 seconds instead of 15+)
- **Manual playlist**: You must create playlist.m3u8 manually after recording
- **H.264 only**: Only H.264 video codec is supported (not VP8)

## Troubleshooting

### No segments generated

**Check**:
1. Was WebRTC connection established? (Look for "Connection State has changed connected")
2. Was video track received? (Look for "Track has started, of type ... video/H264")
3. Check output directory: `ls -lh /tmp/hls-output/hls-session-*/`

### Playback issues

**Create CORS-enabled server**:
```bash
# Create cors_server.py
cat > /tmp/cors_server.py << 'EOF'
#!/usr/bin/env python3
from http.server import HTTPServer, SimpleHTTPRequestHandler

class CORSRequestHandler(SimpleHTTPRequestHandler):
    def end_headers(self):
        self.send_header('Access-Control-Allow-Origin', '*')
        self.send_header('Access-Control-Allow-Methods', 'GET')
        super().end_headers()

HTTPServer(('0.0.0.0', 8080), CORSRequestHandler).serve_forever()
EOF

python3 /tmp/cors_server.py
```

### WebRTC connection fails

**Common issues**:
1. NAT/Firewall blocking UDP
2. Incorrect offer/answer format
3. STUN server unreachable

**Try local network test**:
- Use same machine for publisher and receiver
- Check firewall settings

## See Also

- [save-to-webm.go](../../save-to-webm.go) - The reference example this is based on
- [HLS saver implementation](../../pkg/egress/hls_saver.go) - Core HLS recording logic
- [Test file](main_test.go) - Automated test with WebRTC publisher
- [Pion WebRTC documentation](https://github.com/pion/webrtc)
