# LiveKit Agent Egress Requirements - Comprehensive Specification

This document provides a complete specification for implementing a LiveKit agent optimized for zero-transcode pass-through (stream-copy) muxing with HLS output.

## Executive Summary

The egress agent is built using the livekit-agent-sdk-go framework, implementing the `agent.UniversalHandler` interface to handle room-level jobs. It joins LiveKit rooms, subscribes to participant media streams at highest quality, and produces HLS output with minimal CPU usage through stream-copy processing. Key features include codec verification (H.264/Opus/MP3), gap filling without re-encoding, A/V synchronization, and periodic screenshot extraction.

## 1. Scope and Goals

### 1.1 Primary Objectives
- Provide a production-ready agent example using this repository's agent runtime
- Demonstrate end-to-end egress of room media to HLS with zero transcoding
- Maintain transparency about codec compatibility and trade-offs
- Achieve robustness with predictable output suitable for storage and playback
- Minimize CPU usage through stream-copy operations

### 1.2 Success Criteria
- < 5% CPU usage per stream in stream-copy mode
- < 50ms A/V synchronization drift
- < 100ms end-to-end latency (room to HLS segment)
- 99.9% uptime for continuous recordings
- Zero data loss with proper gap filling

## 2. System Architecture

### 2.1 Component Overview
```
LiveKit Server → Agent Worker → Job Handler → Room Session → GStreamer → HLS Output
        ↓             ↓              ↓            ↓             ↓           ↓
    Job Request   Accept Job    OnJobAssigned  Subscribe   Zero Copy   Segments
                               (JobContext)    to Tracks   Pipeline   & Playlists
```

### 2.2 Core Components
- **Agent Worker**: Based on `agent.UniversalWorker` from livekit-agent-sdk-go
- **Egress Handler**: Implements `agent.UniversalHandler` interface for job handling
- **Room Session**: Manages individual room recording using `agent.JobContext`
- **Track Processor**: Codec verification using LiveKit SDK track subscriptions
- **GStreamer Bridge**: Pipeline management via go-gst or subprocess
- **Output Manager**: HLS segmentation via hlssink2, playlist generation
- **Screenshot Service**: Periodic frame extraction via GStreamer tee
- **Monitoring Service**: Metrics collection integrated with agent framework

### 2.3 Data Flow
1. Agent connects to room with appropriate permissions
2. Auto-subscribes to participant tracks at highest quality
3. Verifies codec compatibility (H.264/Opus/MP3)
4. Routes compatible streams through pass-through pipeline
5. Fills gaps via duplication or pre-encoded frames
6. Outputs synchronized HLS with periodic screenshots

### 2.4 Codec Handling Policy
- **No codec changes mid-stream allowed**
- **Initial codec lock**: First received codec is used for entire session
- **Track republishing**: If participant republishes with different codec:
  - Current recording continues with original tracks
  - New codec tracks are rejected with warning
  - Operator must restart recording for codec changes
- **Rationale**: Prevents HLS corruption and maintains zero-transcode guarantee

## 3. LiveKit Integration

### 3.1 Authentication & Permissions
- **Required Permissions**:
  - `CanSubscribe: true` (mandatory)
  - `CanPublish: false` (agent doesn't publish media)
  - `CanPublishData: true` (for status updates)
  - `Hidden: true` (optional, hide from participant list)
- **Token Generation**:
  - Use server API key/secret with appropriate room grants
  - Identity pattern: `egress-agent-{instance-id}`
  - Expiry: 24 hours (renewable)

### 3.2 Agent Integration
- **Worker Configuration**:
  ```go
  worker := agent.NewUniversalWorker(
      config.LiveKitURL,
      config.APIKey,
      config.APISecret,
      egressHandler,
      agent.WorkerOptions{
          AgentName: "egress-agent",
          JobType:   livekit.JobType_JT_ROOM,
          MaxJobs:   10, // Concurrent room recordings
      },
  )
  ```
- **Connection Management**: Handled by agent SDK with automatic reconnection
- **Error Handling**: Framework provides retry logic and connection management

## 4. Track Management

### 4.1 Discovery & Subscription
- **Handler Implementation**:
  ```go
  // OnJobAssigned handles room recording jobs
  func (h *EgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
      // Room is provided via jobCtx.Room
      room := jobCtx.Room

      // Set up track subscriptions
      room.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote,
          pub *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {

          // Forward RTP packets to GStreamer
          go h.handleTrack(track, pub)
      }

      // Start recording session
      return h.startRecording(ctx, jobCtx)
  }
  ```

### 4.2 Quality Selection
- **Video Quality Layers**:
  - HIGH: Primary selection for recording
  - MEDIUM: Fallback if HIGH unavailable
  - LOW: Emergency fallback only
- **Adaptive Strategy**:
  - Monitor actual received quality via RTCP
  - Request quality upgrade when bandwidth permits
  - Log quality changes for troubleshooting

### 4.3 Codec Verification
- **Supported Codecs (Stream-Copy)**:
  | Media | Codec | MIME Type | Parameters |
  |-------|-------|-----------|------------|
  | Video | H.264 | video/H264 | Profile: Baseline/Main/High, Level: ≤5.2 |
  | Audio | Opus | audio/opus | Sample Rate: 48kHz, Channels: 1-2 |
  | Audio | MP3 | audio/mpeg | Sample Rate: 44.1/48kHz, Bitrate: 64-320kbps |

- **Unsupported Codec Handling**:
  - Log warning with participant/track details
  - Skip track or enable compatibility mode (transcode)
  - Notify monitoring system

### 4.4 Track Lifecycle
- **Subscription Events**:
  - `OnTrackSubscribed`: Initialize pipeline
  - `OnTrackUnsubscribed`: Cleanup resources
  - `OnTrackMuted/Unmuted`: Handle gaps appropriately
- **Quality Changes**:
  - `OnVideoQualityChanged`: Log and adapt
  - Handle resolution changes with new init segments

## 5. Media Pipeline

### 5.1 RTP Processing (Handled by LiveKit SDK)
- **Note**: LiveKit SDK handles all RTP/WebRTC complexity:
  - Jitter buffering and packet reordering
  - Packet loss detection and recovery
  - RTCP processing and feedback
  - The agent receives decoded media frames
- **What We Handle**:
  - Frame-level gap detection
  - Timestamp continuity for HLS
  - A/V synchronization

### 5.2 Sample Assembly
- **H.264 Processing**:
  ```
  RTP → STAP-A/FU-A unpacking → NAL units → Access Units
  - Extract SPS/PPS from SDP or in-band
  - Detect IDR frames for segment boundaries
  - Maintain Annex-B format for FFmpeg
  ```
- **Opus Processing**:
  ```
  RTP → Frame grouping → Duration calculation → Audio samples
  - Handle DTX (discontinuous transmission)
  - Compute frame durations from TOC
  ```

### 5.3 Gap Filling Strategy
- **Detection Thresholds**:
  - Video: > 1 frame duration (e.g., 33ms at 30fps)
  - Audio: > 20ms
- **Filling Methods**:
  1. **Duplication** (preferred):
     - Repeat last valid frame/packet
     - Adjust PTS/DTS accordingly
     - Maintain duration consistency
  2. **Pre-encoded Fillers** (backup):
     - Black frames: 1080p, 720p, 480p @ 30/25fps
     - Silence: Opus 20ms packets, MP3 frames
     - Store in embedded resources

### 5.4 Synchronization
- **Clock Management**:
  - NTP synchronization via RTCP SR
  - Local clock correlation
  - Drift compensation (< 1ms/minute)
- **A/V Alignment**:
  - Common timeline baseline
  - Lip-sync tolerance: ±40ms
  - Timestamp monotonicity enforcement

### 5.5 Output Specifications
- **HLS Configuration**:
  - Variant: fMP4 segments (CMAF compliant)
  - Segment duration: 2-6s (default 4s)
  - Playlist type: EVENT (live) or VOD (complete)
  - Flags: `#EXT-X-INDEPENDENT-SEGMENTS`
- **Directory Structure**:
  ```
  recordings/
  └── {room_name}/
      └── {session_id}/
          ├── master.m3u8
          ├── media_0.m3u8
          ├── init_0.mp4
          ├── segment_00001.m4s
          └── screenshots/
              └── frame_{timestamp}.jpg
  ```

## 6. GStreamer Integration

### 6.1 Implementation with go-gst Bindings (Recommended)
```go
import (
    "github.com/go-gst/go-gstreamer/gst"
    "github.com/go-gst/go-gstreamer/gst/app"
)

func CreatePipeline() (*gst.Pipeline, error) {
    gst.Init(nil)

    // Create pipeline using go-gst
    pipeline, _ := gst.NewPipeline("egress")

    // Create elements
    videoSrc := app.NewSource("videosrc")
    h264parse, _ := gst.NewElement("h264parse")
    audioSrc := app.NewSource("audiosrc")
    opusparse, _ := gst.NewElement("opusparse")
    mux, _ := gst.NewElement("mpegtsmux")
    hlssink, _ := gst.NewElement("hlssink2")

    // Configure HLS
    hlssink.SetProperty("location", "segment%05d.ts")
    hlssink.SetProperty("playlist-location", "playlist.m3u8")

    // Add to pipeline and link
    pipeline.AddMany(videoSrc, h264parse, audioSrc, opusparse, mux, hlssink)
    videoSrc.Link(h264parse)
    h264parse.Link(mux)
    audioSrc.Link(opusparse)
    opusparse.Link(mux)
    mux.Link(hlssink)

    return pipeline, nil
}
```

### 6.1.1 Alternative: UDP Forwarding
- **When go-gst is unavailable**:
  - Forward RTP packets to GStreamer via UDP
  - Use rtpjitterbuffer for proper handling

### 6.2 Process Management
- **Lifecycle**:
  - Pre-flight checks (binary, permissions)
  - Process spawning with resource limits
  - Health monitoring (CPU, memory, I/O)
  - Graceful shutdown with signal handling
- **Error Recovery**:
  - Restart on crash (max 3 attempts)
  - Exponential backoff
  - State preservation across restarts

### 6.3 Screenshot Extraction with Pluggable Handlers
- **Implementation Architecture**:
  ```go
  // Screenshot handler interface for pluggable processing
  type ScreenshotHandler interface {
      // Called when a new screenshot is extracted
      OnScreenshot(ctx context.Context, frame ScreenshotFrame) error

      // Configure extraction parameters
      GetConfig() ScreenshotConfig

      // Cleanup resources
      Close() error
  }

  type ScreenshotFrame struct {
      Data      []byte    // JPEG encoded image
      Timestamp time.Time // Frame timestamp
      PTS       uint64    // Presentation timestamp
      Room      string    // Room name
      SessionID string    // Recording session ID
  }

  type ScreenshotConfig struct {
      Enabled      bool          // Enable screenshot extraction
      Interval     time.Duration // Screenshot interval (default: 5s)
      Quality      int           // JPEG quality (1-100)
      MaxWidth     int           // Max width (0 = no limit)
      MaxHeight    int           // Max height (0 = no limit)
  }
  ```

- **Built-in Handlers**:
  1. **FileHandler** - Save to local filesystem
     ```go
     handler := &FileScreenshotHandler{
         OutputDir: "/recordings/screenshots",
         Pattern:   "frame_%d.jpg",
     }
     ```

  2. **S3Handler** - Upload directly to S3
     ```go
     handler := &S3ScreenshotHandler{
         Bucket: "screenshots",
         Prefix: "recordings/{room}/{session}/",
         UploadConcurrency: 3,
     }
     ```

  3. **CallbackHandler** - Custom processing
     ```go
     handler := &CallbackScreenshotHandler{
         OnFrame: func(frame ScreenshotFrame) error {
             // Custom processing: AI analysis, thumbnail generation, etc.
             return processFrame(frame)
         },
     }
     ```

- **GStreamer Integration**:
  ```go
  func (p *Pipeline) SetupScreenshots(handler ScreenshotHandler) error {
      config := handler.GetConfig()
      if !config.Enabled {
          return nil
      }

      // Add tee and screenshot branch to pipeline
      tee := gst.NewElement("tee")
      queue := gst.NewElement("queue")
      videorate := gst.NewElement("videorate")
      capsfilter := gst.NewElement("capsfilter")
      jpegenc := gst.NewElement("jpegenc")
      appsink := app.NewSink("screenshotsink")

      // Configure framerate
      caps := fmt.Sprintf("video/x-raw,framerate=1/%d", int(config.Interval.Seconds()))
      capsfilter.SetProperty("caps", gst.NewCapsFromString(caps))

      // Configure JPEG quality
      jpegenc.SetProperty("quality", config.Quality)

      // Set callback for frames
      appsink.SetCallbacks(&app.SinkCallbacks{
          NewSample: func(sink *app.Sink) gst.FlowReturn {
              sample := sink.PullSample()
              buffer := sample.GetBuffer()

              frame := ScreenshotFrame{
                  Data:      buffer.Bytes(),
                  Timestamp: time.Now(),
                  PTS:       buffer.PresentationTimestamp(),
                  Room:      p.roomName,
                  SessionID: p.sessionID,
              }

              // Process in goroutine to avoid blocking pipeline
              go handler.OnScreenshot(context.Background(), frame)

              return gst.FlowOK
          },
      })

      // Link elements
      p.pipeline.Add(tee, queue, videorate, capsfilter, jpegenc, appsink)
      tee.Link(queue)
      queue.Link(videorate)
      videorate.Link(capsfilter)
      capsfilter.Link(jpegenc)
      jpegenc.Link(appsink)

      return nil
  }
  ```

- **Example Custom Handler**:
  ```go
  // AI-powered scene detection handler
  type SceneDetectionHandler struct {
      aiClient     *AIClient
      storage      StorageBackend
      minInterval  time.Duration
      lastCapture  time.Time
  }

  func (h *SceneDetectionHandler) OnScreenshot(ctx context.Context, frame ScreenshotFrame) error {
      // Rate limiting
      if time.Since(h.lastCapture) < h.minInterval {
          return nil
      }

      // Run scene detection
      analysis, err := h.aiClient.AnalyzeFrame(frame.Data)
      if err != nil {
          return err
      }

      // Only save if scene changed significantly
      if analysis.SceneChangeScore > 0.7 {
          key := fmt.Sprintf("%s/%s/scene_%d.jpg",
              frame.Room, frame.SessionID, frame.PTS)

          err = h.storage.Save(key, frame.Data, map[string]string{
              "scene_type": analysis.SceneType,
              "confidence": fmt.Sprintf("%.2f", analysis.Confidence),
          })

          h.lastCapture = time.Now()
          return err
      }

      return nil
  }
  ```

## 7. Configuration

### 7.1 Required Parameters
```yaml
room:
  url: "wss://example.livekit.cloud"
  api_key: "${LIVEKIT_API_KEY}"
  api_secret: "${LIVEKIT_API_SECRET}"
  name: "my-room"

agent:
  identity: "egress-agent-001"
  auto_subscribe: true
  max_participants: 10

output:
  directory: "./recordings"
  segment_duration: 4
  playlist_type: "event"  # or "vod"

screenshots:
  enabled: true
  interval: 5  # seconds
  format: "jpeg"
  quality: 85

pipeline:
  jitter_buffer_ms: 200
  max_gap_fill_ms: 1000
  compatibility_mode: false
```

### 7.2 Advanced Options
```yaml
ffmpeg:
  binary: "/usr/local/bin/ffmpeg"
  hardware_accel: "auto"  # none, auto, videotoolbox, nvenc
  log_level: "warning"

monitoring:
  metrics_port: 9090
  health_check_interval: 10
  alert_webhook: "https://..."

resource_limits:
  max_cpu_percent: 80
  max_memory_mb: 2048
  max_disk_gb: 100
```

## 8. Error Handling & Recovery

### 8.1 Failure Scenarios
| Scenario | Detection | Recovery | Impact |
|----------|-----------|----------|--------|
| Network Loss | Timeout/RTCP | Buffer & reconnect | Gap in recording |
| Codec Change | MIME check | New init segment | Discontinuity |
| FFmpeg Crash | Process exit | Restart with state | Brief interruption |
| Disk Full | Write failure | Alert & stop | Incomplete recording |
| High Packet Loss | RTCP stats | Gap filling | Quality degradation |
| CPU Overload | Usage > 80% | Degrade quality | Potential drops |

### 8.2 Recovery Strategies
- **Graceful Degradation**:
  - Switch to lower quality
  - Disable screenshots
  - Increase segment duration
- **State Preservation**:
  - Checkpoint every segment
  - Resume from last good state
  - Maintain playlist continuity

## 9. Monitoring & Observability

### 9.1 Metrics (Prometheus Format)
```
# Track metrics
egress_tracks_active{type="video"} 2
egress_track_bitrate_bps{track_id="...",type="video"} 2500000
egress_track_packets_lost_total{track_id="..."} 42
egress_track_gaps_filled_total{track_id="..."} 5

# Pipeline metrics
egress_segments_written_total 150
egress_segment_duration_seconds{quantile="0.99"} 4.02
egress_av_sync_offset_ms{quantile="0.99"} 15
egress_jitter_buffer_depth_ms 120

# System metrics
egress_ffmpeg_restarts_total 0
egress_ffmpeg_cpu_percent 3.5
egress_disk_usage_bytes 1073741824
```

### 9.2 Logging
- **Structured Logging** (JSON):
  ```json
  {
    "level": "info",
    "timestamp": "2024-01-01T12:00:00Z",
    "component": "track_manager",
    "event": "track_subscribed",
    "track_id": "TR_ABCD123",
    "codec": "video/H264",
    "participant": "user-123"
  }
  ```

### 9.3 Health Checks
- **Endpoint**: `GET /health`
- **Response**:
  ```json
  {
    "status": "healthy",
    "uptime_seconds": 3600,
    "active_tracks": 2,
    "segments_written": 150,
    "last_segment_time": "2024-01-01T12:00:00Z"
  }
  ```

## 10. Testing Requirements

### 10.1 Unit Tests
- RTP assembly (corrupted packets, reordering)
- Jitter buffer (overflow, underflow)
- Gap detection (sequence gaps, timestamp jumps)
- SDP generation (codec parameters)

### 10.2 Integration Tests
- Mock LiveKit server connection
- Synthetic RTP stream processing
- FFmpeg process management
- End-to-end HLS generation

### 10.3 Performance Tests
- 10 concurrent tracks
- 1 hour continuous recording
- Network loss scenarios (1%, 5%, 10%)
- CPU/memory profiling

### 10.4 Validation Tests
- HLS compliance (Apple MediaStreamValidator)
- Player compatibility (Safari, Chrome, VLC)
- A/V sync verification
- Screenshot quality assessment

## 11. Performance Targets (Zero-Transcode)

### 11.1 Resource Usage
| Metric | Target | Note |
|--------|--------|------|
| CPU per track | < 3% | Zero-transcode remuxing only |
| Memory per track | < 100MB | Including jitter buffers |
| Disk I/O | Input rate | Direct segment writing |
| Network overhead | < 1% | Signaling only |

### 11.2 Recording Quality
- **No latency requirements** - Focus on quality over speed
- **Jitter buffering**: Use sufficient buffering for smooth output
- **Gap tolerance**: Up to 500ms gaps filled automatically
- **A/V sync**: Maintain perfect synchronization

## 12. Security Considerations

### 12.1 Input Validation
- Sanitize file paths (no directory traversal)
- Validate codec parameters (prevent buffer overflow)
- Rate limiting on track subscriptions

### 12.2 Process Isolation
- Run FFmpeg with minimal privileges
- Use separate user/group for agent
- Restrict file system access via chroot/containers

### 12.3 Data Protection
- Encrypt recordings at rest (optional)
- Secure token storage (environment variables)
- Audit logging for compliance

## 13. Deployment

### 13.1 Container Deployment
```dockerfile
FROM golang:1.21 AS builder
# Build agent

FROM ubuntu:22.04
RUN apt-get update && apt-get install -y ffmpeg
COPY --from=builder /agent /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/agent"]
```

### 13.2 Kubernetes Manifests
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: egress-agent
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: agent
        image: livekit/egress-agent:latest
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "2Gi"
            cpu: "2000m"
```

### 13.3 Scaling Strategy
- Horizontal: Multiple agents, room sharding
- Vertical: Increase resources per agent
- Auto-scaling based on CPU/memory metrics

## 14. Limitations & Known Issues

### 14.1 Codec Compatibility
- Opus in fMP4: Limited player support (Chrome yes, Safari no)
- MP3 in fMP4: Non-standard, compatibility varies
- Workaround: Optional AAC transcode for Safari

### 14.2 Technical Constraints
- Maximum participants: Limited by CPU/memory
- Maximum recording duration: Filesystem dependent
- Network requirements: Stable connection required

### 14.3 Player Compatibility Matrix
| Player | H.264 | Opus | MP3 | Notes |
|--------|-------|------|-----|-------|
| Safari 15.4+ | ✓ | ✓ | ✓ | Full Opus support |
| Chrome | ✓ | ✓ | ✓ | Full support |
| Firefox | ✓ | ✓ | ✓ | Full support |
| VLC | ✓ | ✓ | ✓ | Full support |
| iOS 15.4+ | ✓ | ✓ | ✓ | Full Opus support |

## 15. Future Roadmap

### 15.1 Short Term (v1.1)
- Hardware acceleration support (NVENC, VideoToolbox)
- S3/GCS upload integration
- Webhook notifications

### 15.2 Medium Term (v1.2)
- Multi-variant HLS (ABR)
- DASH output support
- Timed metadata injection

### 15.3 Long Term (v2.0)
- In-process MP4 muxer (remove FFmpeg dependency)
- WebRTC ingestion (replace LiveKit SDK)
- Real-time transcoding pipeline

## Appendix A: FFmpeg Commands

### A.1 Basic Stream-Copy HLS
```bash
ffmpeg \
  -protocol_whitelist file,udp,rtp \
  -i input.sdp \
  appsrc name=audiosrc ! opusparse ! mux. \
  hlssink2 \
    target-duration=4 \
    playlist-type=event \
    max-files=0 \
    playlist-location=playlist.m3u8 \
    location=segment%05d.ts
```

### A.2 With Screenshot Extraction
```bash
ffmpeg \
  -protocol_whitelist file,udp,rtp \
  -i input.sdp \
  t. ! queue2 ! avdec_h264 ! videorate ! \
  video/x-raw,framerate=1/5 ! \
  jpegenc ! multifilesink location=screenshot_%05d.jpg \
  appsrc name=audiosrc ! opusparse ! mux.
```

### A.3 Hardware Accelerated (macOS)
```bash
ffmpeg \
  -hwaccel videotoolbox \
  -protocol_whitelist file,udp,rtp \
  -i input.sdp \
  mpegtsmux ! hlssink2
```

## Appendix B: SDP Template

```sdp
v=0
o=- 0 0 IN IP4 127.0.0.1
s=LiveKit Stream
c=IN IP4 127.0.0.1
t=0 0
m=video 5004 RTP/AVP 96
a=rtpmap:96 H264/90000
a=fmtp:96 profile-level-id=42e01e;packetization-mode=1;sprop-parameter-sets=Z0KAH9oCgPRA,aM48gA==
m=audio 5006 RTP/AVP 97
a=rtpmap:97 opus/48000/2
a=fmtp:97 minptime=10;useinbandfec=1
```

## Appendix C: Error Codes

| Code | Description | Action |
|------|-------------|--------|
| E001 | Connection failed | Retry with backoff |
| E002 | Invalid codec | Skip or transcode |
| E003 | FFmpeg crash | Restart process |
| E004 | Disk full | Stop and alert |
| E005 | High packet loss | Fill gaps |
| E006 | Authentication failed | Check credentials |
| E007 | Room not found | Wait or exit |
| E008 | Resource limit | Degrade quality |
| E009 | S3 upload failed | Retry with exponential backoff |
| E010 | S3 credentials expired | Refresh credentials |

## 16. Cloud Storage Integration

### 16.1 S3 Storage Requirements

The egress agent MUST support direct upload of HLS segments and playlists to Amazon S3 (or S3-compatible storage).

#### 16.1.1 Core Requirements
- **Real-time Upload**: Upload HLS segments as they are created, not after recording completes
- **Playlist Updates**: Update m3u8 playlists in S3 after each segment
- **Multi-part Upload**: Support for large segments using S3 multi-part upload
- **Retry Logic**: Exponential backoff for failed uploads
- **Concurrent Uploads**: Upload multiple segments in parallel

#### 16.1.2 S3 Configuration
```yaml
s3:
  enabled: true
  bucket: "livekit-recordings"
  region: "us-west-2"
  endpoint: "" # Optional: for S3-compatible services
  prefix: "recordings/{room_name}/{egress_id}/"
  access_key_id: "${AWS_ACCESS_KEY_ID}"
  secret_access_key: "${AWS_SECRET_ACCESS_KEY}"
  session_token: "" # Optional: for temporary credentials

  # Upload settings
  upload:
    concurrent_uploads: 3
    retry_attempts: 3
    retry_backoff_ms: 1000
    multipart_threshold: 100MB # Use multipart for files > 100MB

  # Storage class options
  storage_class: "STANDARD" # STANDARD, STANDARD_IA, GLACIER_INSTANT

  # Object metadata
  metadata:
    Content-Type: "video/mp4" # for segments
    Cache-Control: "public, max-age=3600"
    tags:
      room: "{room_name}"
      participant: "{participant_id}"
      timestamp: "{start_time}"
```

#### 16.1.3 Upload Strategy
1. **Segment Upload Flow**:
   - Generate segment locally
   - Start upload to S3 immediately
   - Continue recording next segment
   - Verify upload completion
   - Delete local segment after successful upload (optional)

2. **Playlist Management**:
   - Keep master playlist in memory
   - Update playlist in S3 after each segment
   - Use S3 object versioning for playlist updates

3. **Failure Handling**:
   - Queue failed uploads for retry
   - Continue recording even if uploads fail
   - Alert on persistent upload failures
   - Fallback to local storage if S3 unavailable

#### 16.1.4 S3 API Integration
```go
type S3Uploader struct {
    client     *s3.Client
    bucket     string
    prefix     string
    uploadPool *ants.Pool // Goroutine pool for concurrent uploads
}

func (u *S3Uploader) UploadSegment(segment []byte, key string) error {
    ctx := context.WithTimeout(context.Background(), 30*time.Second)

    _, err := u.client.PutObject(ctx, &s3.PutObjectInput{
        Bucket:       aws.String(u.bucket),
        Key:          aws.String(u.prefix + key),
        Body:         bytes.NewReader(segment),
        ContentType:  aws.String("video/mp4"),
        StorageClass: types.StorageClassStandard,
    })

    return err
}

func (u *S3Uploader) UpdatePlaylist(playlist []byte, key string) error {
    // Use S3 conditional PUT to avoid race conditions
    ctx := context.WithTimeout(context.Background(), 10*time.Second)

    _, err := u.client.PutObject(ctx, &s3.PutObjectInput{
        Bucket:      aws.String(u.bucket),
        Key:         aws.String(u.prefix + key),
        Body:        bytes.NewReader(playlist),
        ContentType: aws.String("application/x-mpegURL"),
        CacheControl: aws.String("no-cache"),
    })

    return err
}
```

#### 16.1.5 Performance Targets
- **Upload Latency**: < 2 seconds per 4-second segment
- **Concurrent Uploads**: 3-5 simultaneous uploads
- **Retry Delay**: 1s, 2s, 4s (exponential backoff)
- **Memory Usage**: Buffer max 3 segments in memory
- **Bandwidth**: Adaptive based on available bandwidth

#### 16.1.6 Security Requirements
- **IAM Permissions**: Minimal required (s3:PutObject, s3:PutObjectAcl)
- **Encryption**: Support SSE-S3 and SSE-KMS
- **Presigned URLs**: Generate for playback access
- **Access Control**: Bucket policies for egress-specific paths

#### 16.1.7 Monitoring & Metrics
- Upload success/failure rate
- Upload latency percentiles (p50, p95, p99)
- Bandwidth usage
- S3 API error rates
- Storage costs tracking

### 16.2 Local Development and Testing with MinIO

For local development and integration testing, MinIO will be used as an S3-compatible storage backend:

#### MinIO Setup for Testing
```yaml
# docker-compose.yaml for local testing
services:
  minio:
    image: minio/minio:latest
    ports:
      - "9000:9000"  # S3 API endpoint
      - "9001:9001"  # Web console
    environment:
      - MINIO_ROOT_USER=minioadmin
      - MINIO_ROOT_PASSWORD=minioadmin
    command: server /data --console-address ":9001"
    volumes:
      - ./minio-data:/data
```

#### Configuration for MinIO
```yaml
# Local testing configuration
s3:
  enabled: true
  endpoint: "http://localhost:9000"  # MinIO endpoint
  bucket: "test-recordings"
  region: "us-east-1"
  access_key_id: "minioadmin"
  secret_access_key: "minioadmin"
  use_path_style: true  # Required for MinIO

  # Same upload settings as production
  upload:
    concurrent_uploads: 3
    retry_attempts: 3
```

#### Testing Benefits
- **Identical API**: MinIO implements S3 API completely
- **No Cloud Costs**: Run tests locally without AWS charges
- **Fast Iteration**: No network latency to cloud services
- **CI/CD Integration**: Easy to spin up in CI pipelines
- **Production Parity**: Same code paths as production S3

### 16.3 Alternative Storage Backends
While S3 is the primary requirement, the agent should support:
- **Google Cloud Storage** (GCS) - S3-compatible API
- **Azure Blob Storage** - via S3-compatible gateway
- **MinIO** - for on-premise S3-compatible storage and local testing
- **Local Filesystem** - fallback option when cloud storage unavailable

## 17. Complete Implementation Example with livekit-agent-sdk-go

### 17.1 Main Entry Point
```go
// examples/egress-agent/main.go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
)

func main() {
    log.SetFlags(log.LstdFlags | log.Lshortfile)
    log.Println("Starting HLS Egress Agent")

    // Load configuration
    config := LoadConfig()

    // Create egress handler
    handler := NewEgressHandler(config)

    // Create worker using agent SDK
    worker := agent.NewUniversalWorker(
        config.LiveKitURL,
        config.APIKey,
        config.APISecret,
        handler,
        agent.WorkerOptions{
            AgentName: "egress-agent",
            JobType:   livekit.JobType_JT_ROOM,
            MaxJobs:   config.MaxConcurrentJobs,
        },
    )

    // Setup graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

    // Start worker
    errChan := make(chan error, 1)
    go func() {
        if err := worker.Start(ctx); err != nil {
            errChan <- err
        }
    }()

    log.Println("Egress worker started, waiting for room jobs...")

    // Wait for shutdown
    select {
    case <-sigChan:
        log.Println("Received shutdown signal")
    case err := <-errChan:
        log.Printf("Worker error: %v", err)
    }

    // Cleanup
    log.Println("Shutting down gracefully...")
    cancel()
    handler.Shutdown()
    log.Println("Shutdown complete")
}
```

### 17.2 Egress Handler Implementation
```go
// examples/egress-agent/handler.go
package main

import (
    "context"
    "fmt"
    "log"
    "sync"

    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    "github.com/pion/webrtc/v3"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

type EgressHandler struct {
    agent.BaseHandler // Embed base handler for default implementations

    config   *Config
    mu       sync.RWMutex
    sessions map[string]*RecordingSession
}

func NewEgressHandler(config *Config) *EgressHandler {
    return &EgressHandler{
        config:   config,
        sessions: make(map[string]*RecordingSession),
    }
}

// OnJobRequest decides whether to accept a room recording job
func (h *EgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    log.Printf("Egress job request for room %s", job.Room.Name)

    // Accept all room jobs
    return true, &agent.JobMetadata{
        ParticipantIdentity: fmt.Sprintf("egress-agent-%s", job.Id),
        ParticipantName:     "HLS Egress Agent",
        ParticipantMetadata: `{"agent_type": "egress", "output": "hls"}`,
    }
}

// OnJobAssigned handles the assigned recording job
func (h *EgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    log.Printf("Starting egress for room %s (job: %s)", jobCtx.Job.Room.Name, jobCtx.Job.Id)

    // Create recording session
    session := &RecordingSession{
        job:        jobCtx.Job,
        room:       jobCtx.Room,
        config:     h.config,
        outputPath: fmt.Sprintf("%s/%s", h.config.OutputDir, jobCtx.Job.Room.Name),
        tracks:     make(map[string]*TrackInfo),
    }

    // Store session
    h.mu.Lock()
    h.sessions[jobCtx.Job.Id] = session
    h.mu.Unlock()

    // Set up room callbacks for track handling
    jobCtx.Room.Callback.OnTrackSubscribed = session.OnTrackSubscribed
    jobCtx.Room.Callback.OnTrackUnsubscribed = session.OnTrackUnsubscribed

    // Start recording
    return session.Start(ctx)
}

// OnJobTerminated handles job termination
func (h *EgressHandler) OnJobTerminated(ctx context.Context, jobID string) {
    log.Printf("Terminating egress job: %s", jobID)

    h.mu.Lock()
    session, exists := h.sessions[jobID]
    if exists {
        delete(h.sessions, jobID)
    }
    h.mu.Unlock()

    if exists && session != nil {
        session.Stop()
    }
}

func (h *EgressHandler) Shutdown() {
    h.mu.Lock()
    defer h.mu.Unlock()

    for _, session := range h.sessions {
        session.Stop()
    }
}
```

### 17.3 Recording Session Implementation
```go
// examples/egress-agent/session.go
package main

import (
    "context"
    "fmt"
    "log"
    "sync"

    "github.com/pion/webrtc/v3"
    "github.com/pion/rtp"
    lksdk "github.com/livekit/server-sdk-go/v2"
    "github.com/livekit/protocol/livekit"
)

type RecordingSession struct {
    job        *livekit.Job
    room       *lksdk.Room
    config     *Config
    outputPath string

    mu         sync.RWMutex
    tracks     map[string]*TrackInfo
    pipeline   *GStreamerPipeline
    cancelFunc context.CancelFunc
}

type TrackInfo struct {
    track       *webrtc.TrackRemote
    publication *lksdk.RemoteTrackPublication
    participant *lksdk.RemoteParticipant
    codec       string
    cancel      context.CancelFunc
}

func (s *RecordingSession) Start(ctx context.Context) error {
    sessionCtx, cancel := context.WithCancel(ctx)
    s.cancelFunc = cancel

    // Create output directory
    if err := os.MkdirAll(s.outputPath, 0755); err != nil {
        return fmt.Errorf("failed to create output dir: %w", err)
    }

    // Initialize GStreamer pipeline
    s.pipeline = NewGStreamerPipeline(PipelineConfig{
        VideoPort:       s.config.VideoPort,
        AudioPort:       s.config.AudioPort,
        OutputDir:       s.outputPath,
        SegmentDuration: s.config.SegmentDuration,
        EnableScreenshots: s.config.EnableScreenshots,
    })

    // Start pipeline
    if err := s.pipeline.Start(); err != nil {
        return fmt.Errorf("failed to start pipeline: %w", err)
    }

    // If S3 is configured, start uploader
    if s.config.S3.Enabled {
        go s.startS3Uploader(sessionCtx)
    }

    log.Printf("Recording session started for room %s", s.job.Room.Name)
    return nil
}

func (s *RecordingSession) OnTrackSubscribed(
    track *webrtc.TrackRemote,
    publication *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
) {
    log.Printf("Track subscribed: %s from %s", track.ID(), participant.Identity())

    // Verify codec compatibility
    codecName := track.Codec().MimeType
    if !s.isCodecSupported(codecName) {
        log.Printf("Unsupported codec %s, skipping track", codecName)
        return
    }

    // Check for codec change (not allowed mid-stream)
    s.mu.Lock()
    if existingTrack, exists := s.tracks[track.ID()]; exists {
        if existingTrack.codec != codecName {
            s.mu.Unlock()
            log.Printf("Codec change detected for track %s (%s -> %s), rejecting",
                track.ID(), existingTrack.codec, codecName)
            return
        }
    }

    // Create track info
    ctx, cancel := context.WithCancel(context.Background())
    info := &TrackInfo{
        track:       track,
        publication: publication,
        participant: participant,
        codec:       codecName,
        cancel:      cancel,
    }
    s.tracks[track.ID()] = info
    s.mu.Unlock()

    // Start forwarding RTP packets to GStreamer
    go s.forwardTrackToGStreamer(ctx, info)
}

func (s *RecordingSession) forwardTrackToGStreamer(ctx context.Context, info *TrackInfo) {
    port := s.config.VideoPort
    if info.track.Kind() == webrtc.RTPCodecTypeAudio {
        port = s.config.AudioPort
    }

    // Create UDP connection to GStreamer
    conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
    if err != nil {
        log.Printf("Failed to connect to GStreamer: %v", err)
        return
    }
    defer conn.Close()

    // Read RTP packets from track and forward to GStreamer
    for {
        select {
        case <-ctx.Done():
            return
        default:
            // Read RTP packet from WebRTC track
            packet, _, err := info.track.ReadRTP()
            if err != nil {
                log.Printf("Error reading RTP: %v", err)
                return
            }

            // Forward to GStreamer via UDP
            data, err := packet.Marshal()
            if err != nil {
                log.Printf("Error marshaling RTP: %v", err)
                continue
            }

            if _, err := conn.Write(data); err != nil {
                log.Printf("Error forwarding to GStreamer: %v", err)
                return
            }
        }
    }
}

func (s *RecordingSession) OnTrackUnsubscribed(
    track *webrtc.TrackRemote,
    publication *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
) {
    log.Printf("Track unsubscribed: %s", track.ID())

    s.mu.Lock()
    if info, exists := s.tracks[track.ID()]; exists {
        info.cancel()
        delete(s.tracks, track.ID())
    }
    s.mu.Unlock()
}

func (s *RecordingSession) Stop() {
    log.Printf("Stopping recording session for room %s", s.job.Room.Name)

    // Cancel all track forwarding
    s.mu.Lock()
    for _, info := range s.tracks {
        info.cancel()
    }
    s.tracks = make(map[string]*TrackInfo)
    s.mu.Unlock()

    // Stop GStreamer pipeline
    if s.pipeline != nil {
        s.pipeline.Stop()
    }

    // Cancel session context
    if s.cancelFunc != nil {
        s.cancelFunc()
    }

    log.Printf("Recording session stopped for room %s", s.job.Room.Name)
}

func (s *RecordingSession) isCodecSupported(mimeType string) bool {
    switch mimeType {
    case "video/H264", "audio/opus", "audio/mpeg":
        return true
    default:
        return false
    }
}
```

### 17.4 Directory Structure
```
livekit-agent-sdk-go/
├── examples/
│   └── egress-agent/
│       ├── main.go              # Entry point
│       ├── handler.go           # EgressHandler implementation
│       ├── session.go           # RecordingSession logic
│       ├── config.go            # Configuration management
│       ├── gstreamer.go         # GStreamer pipeline wrapper
│       ├── s3.go               # S3 upload functionality
│       ├── Dockerfile          # Container image
│       ├── docker-compose.yaml # Local testing setup
│       └── README.md           # Usage documentation
├── pkg/
│   └── egress/                 # Reusable egress components
│       ├── pipeline/           # GStreamer pipeline builders
│       ├── storage/            # S3/GCS/local storage
│       └── screenshot/         # Screenshot extraction
└── test/
    └── egress/
        ├── integration_test.go # Integration tests
        └── fixtures/           # Test video files
```

This implementation fully integrates with the livekit-agent-sdk-go framework, using the standard agent.UniversalWorker and agent.UniversalHandler patterns established in the repository.

---

*Document Version: 2.2*
*Last Updated: 2024*
*Status: Ready for Development*