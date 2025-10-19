# Publisher HLS Agent

A production-ready LiveKit agent that records participant tracks to **HLS (HTTP Live Streaming)** format with optional S3 upload. Built with Go and GStreamer, this agent converts real-time WebRTC streams into adaptive bitrate HLS playlists optimized for playback compatibility.

## Features

- **🎥 HLS Recording**: Records H.264 video + Opus/AAC audio to HLS playlists (M3U8) and MPEG-TS segments
- **☁️ S3 Integration**: Real-time or batch upload to S3-compatible storage (AWS S3, MinIO, DigitalOcean Spaces)
- **🔄 Delayed Pipeline Start**: Ensures all HLS segments begin with valid keyframes for immediate playback
- **⚡ Pre-buffering**: Synchronizes audio/video streams for gapless segment 0 playback
- **🛠️ Timestamp Normalization**: Fixes GStreamer timestamp issues for player compatibility
- **🎯 Auto/Manual Activation**: Flexible recording control via configuration or API
- **📊 Multiple Participants**: Concurrent recording sessions with independent pipelines

## Architecture

### High-Level Overview

```mermaid
graph LR
    A[LiveKit Server] -->|WebRTC| B[Publisher HLS Agent]
    B -->|Subscribe| C[Participant Tracks]
    C -->|RTP Packets| D[GStreamer Pipeline]
    D -->|HLS Output| E[Local Storage]
    E -->|Upload| F[S3 Storage]

    style B fill:#4A90E2,color:#fff
    style D fill:#50C878,color:#fff
    style F fill:#FF6B6B,color:#fff
```

### Component Architecture

```mermaid
graph TB
    subgraph "LiveKit Agent SDK"
        A[Universal Worker] -->|JT_PUBLISHER| B[PublisherHLSHandler]
    end

    subgraph "Recording Session"
        B --> C[ParticipantRecorder]
        C --> D[Video Track Handler]
        C --> E[Audio Track Handler]
        D --> F[Pre-Video Buffer]
        E --> G[Pre-Audio Buffer]
    end

    subgraph "GStreamer Pipeline"
        F --> H[appsrc video]
        G --> I[appsrc audio]
        H --> J[rtph264depay]
        I --> K[rtpopusdepay]
        J --> L[h264parse]
        K --> M{Keep Opus?}
        M -->|Yes| N[opusparse]
        M -->|No| O[opusdec → aacenc]
        L --> P[mpegtsmux]
        N --> P
        O --> P
        P --> Q[tee]
        Q --> R[filesink output.ts]
        Q --> S[hlssink segments]
    end

    subgraph "Post-Processing"
        S --> T[Timestamp Normalization]
        T --> U[Playlist Fix]
        U --> V{S3 Enabled?}
        V -->|Yes| W[S3 Upload]
        V -->|No| X[Local Only]
    end

    style B fill:#4A90E2,color:#fff
    style C fill:#50C878,color:#fff
    style P fill:#9B59B6,color:#fff
    style W fill:#FF6B6B,color:#fff
```

### Recording Lifecycle

```mermaid
sequenceDiagram
    participant LK as LiveKit Server
    participant AG as Agent Worker
    participant H as Handler
    participant R as Recorder
    participant G as GStreamer
    participant S3 as S3 Storage

    LK->>AG: Dispatch JT_PUBLISHER Job
    AG->>H: OnJobRequest(job)
    H-->>AG: Accept Job

    AG->>H: OnJobAssigned(jobCtx)
    H->>R: NewParticipantRecorder()
    R->>G: Create Pipeline (PAUSED)

    H->>LK: ConnectToRoom()
    LK->>H: OnTrackSubscribed(video)
    H->>R: AttachVideoTrack()
    R->>R: Buffer in pre-video FIFO

    LK->>H: OnTrackSubscribed(audio)
    H->>R: AttachAudioTrack()
    R->>R: Buffer in pre-audio FIFO

    Note over R: Wait for first keyframe
    R->>R: Detect SPS/PPS/IDR
    R->>H: signalVideoReady()
    H->>R: ActivateRecording() [auto/manual]

    Note over R: Wait for next keyframe
    R->>G: Start Pipeline (PLAYING)
    R->>G: Push buffered packets
    G->>G: Generate HLS segments

    Note over LK,R: Streaming...

    LK->>H: OnParticipantDisconnected()
    H->>R: Stop()
    R->>G: Send EOS
    G->>G: Finalize segments

    R->>R: normalizeHLSTimestamps()
    R->>R: fixHLSPlaylist()

    alt S3 Real-time Upload
        Note over S3: Segments uploaded during recording
    else S3 Batch Upload
        R->>S3: Upload all files
    end

    R-->>H: RecordingSummary
    H->>AG: Job Complete
```

## Installation

### Prerequisites

- **Go 1.21+**
- **GStreamer 1.20+** with plugins:
  - `gst-plugins-base`: Basic elements
  - `gst-plugins-good`: RTP depayloaders, HLS sink
  - `gst-plugins-bad`: MPEG-TS muxer, HLS elements
  - `gst-plugins-ugly`: H.264 parsing (some systems)
  - `gst-libav`: AAC encoding

#### macOS Installation

```bash
brew install gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad gst-plugins-ugly gst-libav
```

#### Ubuntu/Debian Installation

```bash
sudo apt-get update
sudo apt-get install -y \
  libgstreamer1.0-dev \
  libgstreamer-plugins-base1.0-dev \
  gstreamer1.0-plugins-base \
  gstreamer1.0-plugins-good \
  gstreamer1.0-plugins-bad \
  gstreamer1.0-plugins-ugly \
  gstreamer1.0-libav
```

### Build

```bash
cd examples/publisher-hls-agent
go build -o publisher-hls-agent .
```

## Configuration

All configuration is via **environment variables**:

### Required

| Variable | Description | Example |
|----------|-------------|---------|
| `LIVEKIT_API_KEY` | LiveKit API key | `devkey` |
| `LIVEKIT_API_SECRET` | LiveKit API secret | `secret` |

### Optional - Agent

| Variable | Default | Description |
|----------|---------|-------------|
| `LIVEKIT_URL` | `ws://localhost:7880` | LiveKit server WebSocket URL |
| `AGENT_NAME` | `publisher-hls-recorder` | Agent name for job matching |
| `OUTPUT_DIR` | `publisher-hls-output` | Local recording output directory |
| `AUTO_ACTIVATE_RECORDING` | `false` | Auto-start recording when tracks ready |

### Optional - HLS

| Variable | Default | Description |
|----------|---------|-------------|
| `HLS_SEGMENT_DURATION` | `2` | Target segment duration (seconds) |
| `HLS_MAX_SEGMENTS` | `0` | Max playlist entries (0=unlimited) |
| `KEEP_OPUS` | `false` | Preserve Opus audio (no AAC transcode) |

### Optional - S3

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_ENDPOINT` | - | S3 endpoint (e.g., `s3.amazonaws.com`) |
| `S3_BUCKET` | - | S3 bucket name |
| `S3_REGION` | `us-east-1` | S3 region |
| `S3_ACCESS_KEY` | - | S3 access key ID |
| `S3_SECRET_KEY` | - | S3 secret access key |
| `S3_SESSION_TOKEN` | - | Optional STS session token |
| `S3_PREFIX` | - | S3 key prefix (e.g., `recordings/`) |
| `S3_USE_SSL` | `false` | Use HTTPS for S3 |
| `S3_FORCE_PATH_STYLE` | `true` | Path-style URLs (required for MinIO) |
| `S3_OBJECT_ACL` | - | Canned ACL (e.g., `public-read`) |
| `S3_REALTIME_UPLOAD` | `false` | Upload segments during recording |

## Usage

### Quick Start

```bash
# Set credentials
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
export LIVEKIT_URL="ws://localhost:7880"

# Enable auto-activation
export AUTO_ACTIVATE_RECORDING="true"

# Run agent
./publisher-hls-agent
```

### Dispatching Jobs

#### Option 1: CLI Tool (included)

```bash
# Create room with auto-dispatch
go run . dispatch-job
```

#### Option 2: LiveKit API

```go
import (
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

client := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)
_, err := client.CreateRoom(ctx, &livekit.CreateRoomRequest{
    Name: "my-room",
    Agents: []*livekit.RoomAgentDispatch{
        {
            AgentName: "publisher-hls-recorder",
            Metadata:  `{"record_audio":true,"record_video":true}`,
        },
    },
})
```

### S3 Upload Modes

#### Real-time Upload (Streaming)

Upload segments as they're created:

```bash
export S3_REALTIME_UPLOAD="true"
export S3_ENDPOINT="localhost:9000"
export S3_BUCKET="livekit-recordings"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_USE_SSL="false"

./publisher-hls-agent
```

**Benefits**: Minimal local storage, immediate availability
**Use case**: Long recordings, cloud-native deployments

#### Batch Upload (Post-processing)

Upload all files after recording completes:

```bash
export S3_REALTIME_UPLOAD="false"  # or omit
export S3_ENDPOINT="s3.amazonaws.com"
export S3_BUCKET="my-bucket"
export S3_REGION="us-west-2"
export S3_ACCESS_KEY="AKIAIOSFODNN7EXAMPLE"
export S3_SECRET_KEY="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

./publisher-hls-agent
```

**Benefits**: No upload interruptions during recording, atomic uploads
**Use case**: Short recordings, reliable networks

## How It Works

### 1. Delayed Pipeline Start

The agent implements a **delayed start mechanism** to ensure all HLS segments are immediately playable:

```mermaid
stateDiagram-v2
    [*] --> Connecting: Job Assigned
    Connecting --> Buffering: Tracks Subscribed
    Buffering --> HandshakeReady: First Keyframe (SPS/PPS/IDR)
    HandshakeReady --> RecordingPending: ActivateRecording()
    RecordingPending --> Recording: Next Keyframe
    Recording --> Finalizing: Stop()
    Finalizing --> [*]: Upload Complete

    note right of Buffering
        Pre-buffer packets
        Request PLI for keyframes
    end note

    note right of Recording
        GStreamer PLAYING
        Generate HLS segments
    end note
```

**Why?** Starting immediately causes:
- P-frames without reference I-frames (playback fails)
- Missing SPS/PPS headers (decoder cannot initialize)

### 2. Pre-buffering Strategy

Ensures segment 0 contains synchronized audio and video:

```
Time:    0ms     33ms    66ms    100ms   [ActivateRecording]
Video:   [I]     [P]     [P]     [I]     <-- Buffer fills
Audio:   [A]     [A]     [A]     [A]     <-- Buffer fills

Pipeline Starts at Next Keyframe:
1. Push buffered video packets (I, P, P, I)
2. Push buffered audio packets (A, A, A, A)
3. Start live streaming

Result: Segment 0 = complete synchronized A/V
```

### 3. Timestamp Normalization

GStreamer preserves RTP timestamps (arbitrary large values), causing player incompatibility. The agent normalizes all PTS/PCR values:

**Before:**
```
segment00000.ts: PTS = 3840090000 (42 hours!)
segment00001.ts: PTS = 3840270000
```

**After:**
```
segment00000.ts: PTS = 0
segment00001.ts: PTS = 180000 (2 seconds)
```

**Implementation:** Scans MPEG-TS packets, subtracts offsets from all timestamps (handles 33-bit wraparound).

### 4. GStreamer Bug Workaround

GStreamer's `hlssink` writes invalid duration for the final segment on EOS. The agent detects and fixes this:

```go
// Before (GStreamer bug):
#EXTINF:18446743552.0000,
segment00010.ts

// After (fixed):
#EXTINF:1.9840,
segment00010.ts
```

**Method:** Reads MPEG-TS file, calculates actual duration from PTS timestamps, rewrites playlist.

## Testing

### Unit Tests

```bash
go test -v ./...
```

### Integration Tests

Requires LiveKit server running locally:

```bash
# Terminal 1: Start LiveKit
livekit-server --dev --config livekit-server-dev.yaml

# Terminal 2: Run tests
go test -v -run TestPublisherHLSAgentRecordsHLS
```

### E2E Tests with S3

Requires MinIO:

```bash
# Terminal 1: Start MinIO
docker run -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  quay.io/minio/minio server /data --console-address ":9001"

# Terminal 2: Run E2E tests
go test -v -run TestPublisherHLSAgentUploadsToS3
```

## Output Structure

### Local Storage

```
publisher-hls-output/
└── room_participant_20240101-120000.000/
    ├── output.ts              # Full recording (MPEG-TS)
    ├── playlist.m3u8          # HLS playlist
    ├── segment00000.ts        # Segment 0 (2s)
    ├── segment00001.ts        # Segment 1 (2s)
    └── segment00002.ts        # Segment 2 (2s)
```

### S3 Storage

```
s3://bucket/prefix/room/participant/
├── playlist.m3u8
├── segment00000.ts
├── segment00001.ts
└── segment00002.ts
```

**Note:** `output.ts` is not uploaded (redundant).

## Playback

### FFmpeg Validation

```bash
# Check streams
ffprobe -v error -show_entries stream=codec_name,width,height \
  hls-agent-recordings/room_user_*/playlist.m3u8

# Play with FFplay
ffplay hls-agent-recordings/room_user_*/playlist.m3u8
```

### HLS Players

```html
<!-- Video.js -->
<video id="player" class="video-js vjs-default-skin" controls>
  <source src="https://s3.amazonaws.com/bucket/room/participant/playlist.m3u8" type="application/x-mpegURL">
</video>
<script src="https://vjs.zencdn.net/7.20.3/video.min.js"></script>
```

### Safari/iOS Native

```html
<video controls>
  <source src="playlist.m3u8" type="application/x-mpegURL">
</video>
```

## Troubleshooting

### No segments generated

**Symptoms:** Empty output directory, no errors
**Cause:** Recording never activated or no keyframes received

**Solution:**
```bash
# Enable auto-activation
export AUTO_ACTIVATE_RECORDING="true"

# Check logs for "requesting initial keyframe via PLI"
```

### Segments not playable

**Symptoms:** Player errors, black screen, stuttering
**Cause:** Missing SPS/PPS, incorrect timestamps

**Solution:**
- Verify `h264parse config-interval=-1` in pipeline (forces SPS/PPS)
- Check timestamp normalization ran (`normalized HLS timestamps` in logs)
- Inspect with: `ffprobe -v error -show_frames segment00000.ts`

### S3 upload fails

**Symptoms:** `failed to upload recording to S3` errors
**Cause:** Incorrect credentials, bucket permissions, network

**Solution:**
```bash
# Test credentials
export AWS_ACCESS_KEY_ID=$S3_ACCESS_KEY
export AWS_SECRET_ACCESS_KEY=$S3_SECRET_KEY
aws s3 ls s3://$S3_BUCKET

# Check bucket policy (S3)
# Check minio server logs (MinIO)
```

### Audio/video desync

**Symptoms:** Audio plays but video frozen, or vice versa
**Cause:** Missing pre-buffer, incorrect track attachment

**Solution:**
- Check logs: both `video track subscribed` and `audio track subscribed` should appear
- Verify auto-activation waits for both tracks (`markActivatedIfReady`)
- Inspect segment: `ffprobe -show_streams segment00000.ts` should show both streams

## Advanced Configuration

### Custom GStreamer Pipeline

Modify `recorder.go` line 165-420 to customize the pipeline:

```go
// Example: Add watermark
videoQueue, err := gst.NewElement("queue")
textOverlay, err := gst.NewElement("textoverlay")
textOverlay.SetProperty("text", "RECORDED")
textOverlay.SetProperty("valignment", "top")

if err := gst.ElementLinkMany(h264parse, textOverlay, videoCapsFilter, videoQueue); err != nil {
    return nil, err
}
```

### Multiple Codecs

Currently supports H.264 video + Opus/AAC audio. To add VP8/VP9:

1. Add depayloader: `rtpvp8depay` or `rtpvp9depay`
2. Add parser: `vp8parse` or `vp9parse`
3. Update muxer codec support check

## Performance

### Resource Usage (per recording)

- **CPU**: ~15-30% (1 core, H.264 passthrough + AAC transcode), ~6% (1 core, H.264 + Opus passthrough)
- **Memory**: ~50-100 MB (buffering + GStreamer)
- **Disk I/O**: ~500 KB/s (2s segments, 720p30)
- **Network**: ~1-2 Mbps (WebRTC ingress + S3 upload)

### Scaling

- **Concurrent sessions**: Tested with 4+ simultaneous recordings
- **Max duration**: Tested with 2+ hour recordings
- **Segment count**: No hard limit (tested 1000+ segments)

## License

See repository root for license information.

## Contributing

This is an example implementation. For production use:
- Add metrics/monitoring (Prometheus, DataDog)
- Implement error recovery (restart on pipeline failure)
- Add health checks (liveness/readiness probes)
- Configure log levels (structured logging with slog)
- Set resource limits (ulimit, cgroups)

## References

- [LiveKit Agent SDK](https://github.com/livekit/agent-sdk-go)
- [GStreamer Documentation](https://gstreamer.freedesktop.org/documentation/)
- [HLS Specification (RFC 8216)](https://datatracker.ietf.org/doc/html/rfc8216)
- [H.264 Specification (ITU-T H.264)](https://www.itu.int/rec/T-REC-H.264)
- [MPEG-TS Specification (ISO/IEC 13818-1)](https://www.iso.org/standard/74427.html)
