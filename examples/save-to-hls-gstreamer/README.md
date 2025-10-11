# save-to-hls-gstreamer

This example demonstrates how to receive H.264 video and Opus audio via WebRTC and save to HLS (HTTP Live Streaming) format using GStreamer.

## Features

- **WebRTC RTP Reception**: Receives RTP packets from WebRTC tracks
- **GStreamer Pipeline**: Uses GStreamer for robust media processing
- **Jitter Buffer**: Built-in `rtpjitterbuffer` handles packet ordering and timing
- **Audio Transcoding**: Opus → AAC transcoding for broader HLS compatibility
- **MPEG-TS Output**: Outputs MPEG-TS segments (more compatible than fMP4)
- **Automatic Synchronization**: GStreamer handles audio/video sync

## Prerequisites

### Install GStreamer

**macOS:**
```bash
brew install gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad gst-plugins-ugly gst-libav
```

**Ubuntu/Debian:**
```bash
sudo apt-get install libgstreamer1.0-dev libgstreamer-plugins-base1.0-dev \
    libgstreamer-plugins-good1.0-dev libgstreamer-plugins-bad1.0-dev \
    gstreamer1.0-plugins-ugly gstreamer1.0-libav
```

### Verify GStreamer Installation

```bash
gst-inspect-1.0 --version
```

Test the pipeline components:
```bash
gst-inspect-1.0 rtpjitterbuffer
gst-inspect-1.0 rtph264depay
gst-inspect-1.0 rtpopusdepay
gst-inspect-1.0 avenc_aac
gst-inspect-1.0 hlssink2
```

## How It Works

### Pipeline Architecture

```
Video Path:
appsrc (RTP) → rtpjitterbuffer → rtph264depay → h264parse → mpegtsmux → hlssink2

Audio Path:
appsrc (RTP) → rtpjitterbuffer → rtpopusdepay → opusdec → avenc_aac → aacparse → mpegtsmux → hlssink2
```

### Key Components

1. **appsrc**: Receives RTP packets from WebRTC with proper PTS timestamps
2. **rtpjitterbuffer**: Handles packet reordering, jitter compensation, and packet loss
3. **rtph264depay/rtpopusdepay**: Depacketizes RTP to elementary streams
4. **opusdec → avenc_aac**: Transcodes Opus to AAC for MPEG-TS compatibility
5. **mpegtsmux**: Multiplexes H.264 and AAC into MPEG-TS
6. **hlssink2**: Creates HLS playlist and segments

### Why GStreamer?

Compared to the manual approach in `save-to-hls`:

- **Built-in jitter buffer**: Automatic packet ordering and timing recovery
- **Packet loss handling**: PLI requests and graceful degradation
- **Tested pipeline**: GStreamer elements are production-tested
- **Audio/video sync**: Automatic synchronization based on PTS
- **Transcoding**: Easy Opus → AAC conversion for compatibility

## Usage

### Build

```bash
cd examples/save-to-hls-gstreamer
go mod tidy
go build
```

### Run

```bash
./save-to-hls-gstreamer [output-directory]
```

The application will:
1. Initialize GStreamer pipeline
2. Create WebRTC peer connection
3. Wait for SDP offer on stdin
4. Output SDP answer
5. Start recording to HLS

### Testing

Run the end-to-end test with a sample video:

```bash
go test -tags=e2e -v -run TestHLSRecorderGStreamer -timeout=5m
```

This test:
1. Creates the GStreamer recorder
2. Sets up a WebRTC publisher
3. Publishes H.264 video and Opus audio from `test.mp4`
4. Records to HLS segments
5. Validates the output

## Output Format

The recorder creates:

- `playlist.m3u8`: HLS master playlist
- `segment_00000.ts`, `segment_00001.ts`, ...: MPEG-TS segments

### Playback

**Using ffplay:**
```bash
ffplay hls-gst-output/playlist.m3u8
```

**Using VLC:**
```bash
vlc hls-gst-output/playlist.m3u8
```

**Using web browser:**
```bash
python3 -m http.server 8080
# Open http://localhost:8080/hls-gst-output/playlist.m3u8 in browser with HLS support
```

## Configuration

### Pipeline Parameters

Edit the pipeline string in `initGStreamer()`:

- **Jitter Buffer Latency**: `rtpjitterbuffer latency=200` (in milliseconds)
- **AAC Bitrate**: `avenc_aac bitrate=128000`
- **Segment Duration**: `hlssink2 target-duration=2` (in seconds)
- **Max Segments**: `hlssink2 max-files=10`

### Example: Increase Jitter Buffer for Unstable Networks

```go
rtpjitterbuffer latency=500 !
```

### Example: Higher Audio Quality

```go
avenc_aac bitrate=256000 !
```

## Comparison with save-to-hls

| Feature | save-to-hls (manual) | save-to-hls-gstreamer |
|---------|---------------------|---------------------|
| Jitter handling | Manual samplebuilder | Built-in rtpjitterbuffer |
| Packet ordering | samplebuilder | rtpjitterbuffer |
| Audio transcoding | Not supported | Opus → AAC |
| Output format | fMP4 | MPEG-TS |
| A/V sync | Manual PTS calculation | Automatic |
| Packet loss recovery | Limited | PLI requests |
| Code complexity | Higher | Lower |

## Debugging

### Enable GStreamer Debug Logging

```bash
export GST_DEBUG=3
./save-to-hls-gstreamer
```

Debug levels:
- `1`: Error
- `2`: Warning
- `3`: Info
- `4`: Debug
- `5`: Trace

### Generate Pipeline Graph

```bash
export GST_DEBUG_DUMP_DOT_DIR=./
# Run the application
# Convert .dot to .png
dot -Tpng pipeline.dot -o pipeline.png
```

### Common Issues

**No audio in output:**
- Check that `gst-plugins-ugly` is installed (contains AAC encoder)
- Verify Opus decoder: `gst-inspect-1.0 opusdec`

**Segments not created:**
- Check `hlssink2` is available: `gst-inspect-1.0 hlssink2`
- Increase recording duration
- Check for GStreamer errors in logs

**Audio/video out of sync:**
- Verify PTS timestamps are set correctly on RTP packets
- Check jitter buffer latency settings
- Ensure both tracks are receiving data

## Performance

Typical resource usage (1080p30 H.264 + Opus):
- CPU: 10-20% (single core)
- Memory: ~50MB
- Disk I/O: ~2-5 MB/s

## License

Apache 2.0 (same as parent project)
