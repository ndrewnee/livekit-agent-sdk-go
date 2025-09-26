# LiveKit Egress Agent

A zero-transcode HLS egress agent for LiveKit rooms, built using the LiveKit Agent SDK Go and GStreamer with go-gst bindings.

## Overview

This egress agent provides efficient room recording and HLS streaming capabilities by:
- Receiving WebRTC tracks from LiveKit rooms
- Forwarding RTP packets directly to GStreamer (zero-transcode for H.264/Opus)
- Outputting HLS streams with configurable segments
- Optional S3 upload support for cloud storage
- Automatic gap filling for interrupted streams

## Features

- **Zero-transcode operation** for H.264 video and Opus/MP3 audio
- **Native GStreamer integration** using go-gst bindings (no subprocess)
- **HLS output** with configurable segment duration
- **Multiple audio modes**:
  - Pass-through (Opus/MP3 as-is)
  - Transcode to AAC (maximum compatibility)
  - Transcode to MP3 (lower CPU usage)
- **Automatic gap filling** via videorate/audiorate elements
- **Screenshot extraction** at configurable intervals
- **S3 upload** support (MinIO/AWS S3 compatible)
- **Crash recovery** with exponential backoff (3 attempts)
- **Concurrent session handling**
- **Performance monitoring** with real-time statistics

## Prerequisites

- Go 1.20 or higher
- GStreamer 1.20 or higher with required plugins
- LiveKit server (for testing)
- MinIO (optional, for S3 testing)

## Installation

### 1. Install GStreamer

Run the provided installation script:

```bash
./install-gstreamer.sh
```

Or install manually:

**macOS:**
```bash
brew install gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad gst-plugins-ugly gst-libav
```

**Ubuntu/Debian:**
```bash
sudo apt-get install gstreamer1.0-tools gstreamer1.0-plugins-base gstreamer1.0-plugins-good \
  gstreamer1.0-plugins-bad gstreamer1.0-plugins-ugly gstreamer1.0-libav
```

### 2. Install LiveKit Server (for testing)

```bash
brew install livekit
```

### 3. Install MinIO (optional, for S3 testing)

```bash
brew install minio
```

### 4. Build the Agent

```bash
make build
```

## Configuration

Edit `config.yaml` to configure the agent:

```yaml
# Output configuration
output:
  dir: /tmp/recordings
  segment_duration: 4  # seconds

# GStreamer pipeline configuration
pipeline:
  video_port: 5004
  audio_port: 5006
  jitter_buffer_ms: 200

# Audio processing mode
audio:
  mode: passthrough  # Options: passthrough, transcode_aac, transcode_mp3
  aac_bitrate: 192   # kbps
  mp3_bitrate: 192   # kbps

# Screenshot extraction
screenshots:
  enabled: false
  interval: 5  # seconds

# S3 upload configuration
s3:
  enabled: false
  endpoint: http://localhost:9000
  bucket: recordings
  region: us-east-1
  access_key: minioadmin
  secret_key: minioadmin
```

## Usage

### 1. Start LiveKit Server

```bash
make start-livekit
# Or manually:
./start-livekit.sh
```

### 2. Start MinIO (optional)

```bash
make start-minio
# Or manually:
./start-minio.sh
```

### 3. Run the Egress Agent

```bash
make run
# Or manually:
./egress-agent -config config.yaml
```

### 4. Create a Room and Start Recording

The agent will automatically accept room recording jobs and start egress when participants join.

## Development

### Running Tests

```bash
make test
```

### Generate Test Media

```bash
make generate-test
```

### Verify Dependencies

```bash
make verify
```

### Development Setup

```bash
make dev-setup
```

## Architecture

```
WebRTC Track → RTP Router → UDP (localhost) → GStreamer Pipeline (go-gst) → HLS Output
                                                      ↓
                                               Gap Filling & Sync
                                                      ↓
                                              Optional S3 Upload
```

### Components

1. **RTP Router** (`pkg/egress/router`): Routes RTP packets from WebRTC to GStreamer via UDP
2. **GStreamer Pipeline** (`pkg/egress/pipeline`): Native GStreamer integration using go-gst bindings
   - Zero-transcode H.264/Opus pipeline
   - Automatic gap filling (videorate/audiorate)
   - Bus message handling for state management
   - Crash recovery with exponential backoff
3. **Configuration** (`pkg/egress/config`): Handles YAML configuration loading
4. **Agent Handler** (`examples/egress-agent/handler.go`): Implements LiveKit agent interface

## Implementation Status

### Milestone 1: GStreamer Pipeline Core ✅
- ✅ Pipeline implementation with go-gst bindings
- ✅ UDP RTP reception (ports 5004/5006)
- ✅ Zero-transcode H.264/Opus pipeline
- ✅ Automatic gap filling via videorate/audiorate
- ✅ Process crash recovery (3 attempts, exponential backoff)
- ✅ Performance monitoring and statistics
- ✅ HLS output with configurable segments
- ✅ Audio mode support (passthrough/AAC/MP3)

### Performance Targets (Milestone 1)
- CPU Usage: < 3% per stream (zero-transcode mode)
- Memory: < 100MB including buffers
- Gap Filling: Handles 100-500ms gaps
- A/V Sync: Maintained within 40ms
- Auto-restart: Max 3 attempts with backoff

### GStreamer Pipeline

The agent uses go-gst bindings to construct a native GStreamer pipeline that:
1. Receives RTP packets via UDP (udpsrc elements)
2. Manages jitter buffering and synchronization (rtpbin)
3. Handles gap filling for interrupted streams (videorate/audiorate)
4. Muxes audio/video into MPEG-TS (mpegtsmux)
5. Outputs HLS segments and playlist (hlssink2)

Key pipeline elements:
- **rtpbin**: Jitter buffer with configurable latency (default 200ms)
- **videorate**: Duplicates frames on gaps, maintains framerate
- **audiorate**: Fills audio gaps, maintains sample rate
- **hlssink2**: Generates HLS segments with proper timestamps

## Troubleshooting

### GStreamer Issues

Check GStreamer debug logs:
```bash
GST_DEBUG=3 ./egress-agent
```

Verify plugin availability:
```bash
gst-inspect-1.0 hlssink2
```

### Network Issues

Check UDP ports are available:
```bash
lsof -i :5004
lsof -i :5006
```

### Performance Tuning

- Increase jitter buffer for poor network conditions
- Use pass-through mode for minimal CPU usage
- Adjust segment duration based on latency requirements

## Environment Variables

```bash
export LIVEKIT_URL=ws://localhost:7880
export LIVEKIT_API_KEY=APIhZLy9N9dS7k
export LIVEKIT_API_SECRET=2PuQxDGCYnD3N96bQUW1OlKzBPJzUykpHGhf0Mhr9m1
```

## License

This project is part of the LiveKit Agent SDK Go examples.