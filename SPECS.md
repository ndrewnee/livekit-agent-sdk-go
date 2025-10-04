# Zero-Transcode HLS Egress Agent - Technical Specifications

## Executive Summary

This specification defines a LiveKit agent that performs zero-transcode HLS egress using GStreamer for optimal streaming performance. The agent is implemented using the livekit-agent-sdk-go framework, following the established patterns of agent.UniversalWorker and agent.UniversalHandler interfaces for room-level job handling.

## 1. System Requirements

### 1.1 Core Technologies
- **Streaming Engine**: GStreamer 1.20+ (subprocess), Pure Go muxer (future)
- **Language**: Go 1.21+
- **WebRTC**: Pion WebRTC v3 (already in monorepo)
- **LiveKit SDK**: server-sdk-go/v2 (already in monorepo)
- **Integration**: Native monorepo agent using existing worker infrastructure

### 1.2 Performance Targets
| Metric | Target | Measurement |
|--------|--------|-------------|
| CPU Usage | < 3% per stream | Zero-transcode mode |
| Memory | < 100MB per stream | Including jitter buffers |
| Recording Quality | No gaps > 500ms | Automatic gap filling |
| A/V Sync | Perfect sync | Maintained throughout |
| Startup Time | < 2s | Connection to first segment |
| Reliability | 99.9% uptime | Per recording session |

Note: **No latency requirements** - Focus is on recording quality, not playback latency.

### 1.3 Supported Codecs (Zero-Transcode)
| Media | Codec | Format | Parameters |
|-------|-------|--------|------------|
| Video | H.264 | AVC | Baseline/Main/High, Level ≤5.2 |
| Audio | Opus | WebRTC | 48kHz, 1-2 channels (Safari 15.4+ compatible) |
| Audio | MP3 | MPEG-1 Layer 3 | 44.1/48kHz, 64-320kbps |

### 1.4 Codec Change Policy
- **No codec changes mid-stream allowed**
- First received codec locks the recording session
- Tracks republished with different codecs are rejected
- Restart recording required for codec changes
- Ensures zero-transcode guarantee and HLS integrity

## 2. Architecture

### 2.1 System Architecture
```
┌─────────────────────────────────────────────────────────┐
│                   LiveKit Room                           │
└──────────────────────┬──────────────────────────────────┘
                       │ WebRTC/RTP
┌──────────────────────▼──────────────────────────────────┐
│         LiveKit Agent SDK Go (Monorepo)                  │
│                                                          │
│  ┌────────────────────────────────────────────────┐    │
│  │           pkg/agent/worker.go                   │    │
│  │  • Existing worker infrastructure              │    │
│  │  • Connection management                       │    │
│  │  • Handler registration                        │    │
│  └──────────────────┬─────────────────────────────┘    │
│                     │                                    │
│  ┌──────────────────▼─────────────────────────────┐    │
│  │        pkg/egress/handler.go (New)             │    │
│  │  • Track subscription management               │    │
│  │  • Codec verification (H.264/Opus/MP3)        │    │
│  │  • RTP packet routing                          │    │
│  └──────────────────┬─────────────────────────────┘    │
│                     │                                    │
│  ┌──────────────────▼─────────────────────────────┐    │
│  │      pkg/egress/rtp/router.go (New)            │    │
│  │  • Direct RTP forwarding to UDP                │    │
│  │  • Zero-copy packet routing                    │    │
│  │  • Statistics collection                       │    │
│  └──────────────────┬─────────────────────────────┘    │
└─────────────────────┬────────────────────────────────────┘
                      │ RTP/UDP (localhost:5004,5006)
┌─────────────────────▼────────────────────────────────────┐
│              GStreamer Pipeline (Subprocess)              │
│                                                           │
│  rtpbin → rtph264depay → h264parse → videorate →        │
│         → mpegtsmux → hlssink2                           │
│  rtpbin → rtpopusdepay → opusparse → audiorate →        │
└───────────────────────────────────────────────────────────┘
                      │
┌─────────────────────▼────────────────────────────────────┐
│                 HLS Output (Filesystem)                   │
│  • fMP4/TS segments                                      │
│  • HLS playlists (master.m3u8, media.m3u8)              │
│  • Screenshots (optional)                                │
└───────────────────────────────────────────────────────────┘
```

### 2.2 Component Specifications

#### 2.2.1 Agent Worker Integration
```go
// Implements agent.UniversalHandler interface
type EgressHandler struct {
    agent.BaseHandler // Embed base handler for default implementations

    config   *EgressConfig
    sessions map[string]*RecordingSession // Active recording sessions
    mu       sync.RWMutex
}

// OnJobRequest decides whether to accept a room recording job
func (h *EgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    return true, &agent.JobMetadata{
        ParticipantIdentity: fmt.Sprintf("egress-agent-%s", job.Id),
        ParticipantName:     "HLS Egress Agent",
    }
}

// OnJobAssigned handles the assigned recording job
func (h *EgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    session := NewRecordingSession(jobCtx, h.config)
    h.sessions[jobCtx.Job.Id] = session

    // Set up track handling via room callbacks
    jobCtx.Room.Callback.OnTrackSubscribed = session.OnTrackSubscribed

    return session.Start(ctx)
}

func (h *EgressHandler) HandleJob(ctx context.Context, job *agent.Job) error {
    // Handle egress job using existing worker patterns
}
```

#### 2.2.2 Track Manager
```go
type TrackManager struct {
    tracks      map[string]*TrackInfo
    subscribers map[string]*Subscriber
    quality     livekit.VideoQuality
}

type TrackInfo struct {
    ID          string
    ParticipantID string
    Kind        livekit.TrackKind
    Codec       string
    MimeType    string
}
```

#### 2.2.3 RTP Router
```go
type RTPRouter struct {
    videoPort   int  // Default: 5004
    audioPort   int  // Default: 5006
    videoConn   *net.UDPConn
    audioConn   *net.UDPConn
}

func (r *RTPRouter) RoutePacket(packet *rtp.Packet, trackKind string) error {
    // Direct UDP forwarding to GStreamer
}
```

## 3. GStreamer Pipeline Specifications

### 3.1 Core Pipeline
```bash
# Primary GStreamer pipeline for zero-transcode HLS
gst-launch-1.0 \
  rtpbin name=rtpbin latency=200 do-lost=true drop-on-latency=false \
  \
  udpsrc port=5004 caps="application/x-rtp,media=video,clock-rate=90000,encoding-name=H264" \
  ! rtpbin.recv_rtp_sink_0 \
  rtpbin. ! rtph264depay ! h264parse config-interval=-1 \
  ! videorate drop-only=false duplicate-on-gap=true skip-to-first=true \
  ! video/x-h264,framerate=30/1 \
  ! queue max-size-time=2000000000 \
  ! mpegtsmux name=mux alignment=7 \
  ! hlssink2 \
    location=/recordings/%Y%m%d-%H%M%S/segment%05d.ts \
    playlist-location=/recordings/%Y%m%d-%H%M%S/playlist.m3u8 \
    target-duration=4 \
    max-files=0 \
  \
  udpsrc port=5006 caps="application/x-rtp,media=audio,clock-rate=48000,encoding-name=OPUS" \
  ! rtpbin.recv_rtp_sink_1 \
  rtpbin. ! rtpopusdepay ! opusparse \
  ! audiorate tolerance=40000000 add=true silent=false \
  ! audio/x-opus,rate=48000,channels=2 \
  ! queue max-size-time=2000000000 \
  ! mux.
```

### 3.2 Gap Filling Configuration
```
videorate properties:
  drop-only=false       # Allow duplication
  duplicate-on-gap=true # Duplicate last frame on gap
  max-rate=30          # Maximum output framerate

audiorate properties:
  tolerance=40000000   # 40ms tolerance
  add=true            # Add silence for gaps
  silent=false        # Log gap events
```

## 4. Implementation Specifications

### 4.1 Monorepo Package Structure
```
livekit-agent-sdk-go/
├── examples/
│   └── egress-agent/
│       ├── main.go                 # Example using egress handler
│       ├── config.yaml             # Configuration example
│       └── README.md               # Usage guide
├── pkg/
│   ├── agent/                      # Existing agent framework
│   │   ├── worker.go              # Reuse existing worker
│   │   └── handler.go             # Base handler interface
│   └── egress/                     # New egress package
│       ├── handler.go              # Egress handler implementation
│       ├── config.go               # Configuration structures
│       ├── track/
│       │   ├── manager.go          # Track subscription
│       │   └── codec.go            # Codec verification
│       ├── rtp/
│       │   ├── router.go           # RTP to UDP routing
│       │   └── stats.go            # Statistics
│       ├── gstreamer/
│       │   ├── pipeline.go         # Pipeline management
│       │   ├── builder.go          # Pipeline string builder
│       │   └── process.go          # Process management
│       └── monitoring/
│           ├── metrics.go          # Prometheus metrics
│           └── health.go           # Health checks
```

### 4.2 Core Implementation

#### 4.2.1 Egress Handler (Monorepo Integration)
```go
package egress

import (
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    "github.com/livekit/server-sdk-go/v2/lksdk"
    "github.com/pion/webrtc/v3"
)

type EgressHandler struct {
    agent.BaseHandler // Embed base handler for default implementations

    config   *Config
    sessions map[string]*RecordingSession
    mu       sync.RWMutex
}

// NewEgressHandler creates an egress handler
func NewEgressHandler(config *Config) *EgressHandler {
    return &EgressHandler{
        config:   config,
        sessions: make(map[string]*RecordingSession),
    }
}

// OnJobRequest implements agent.UniversalHandler
func (h *EgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    return true, &agent.JobMetadata{
        ParticipantIdentity: fmt.Sprintf("egress-agent-%s", job.Id),
        ParticipantName:     "HLS Egress Agent",
        ParticipantMetadata: `{"agent_type": "egress"}`,
    }
}

// OnJobAssigned implements agent.UniversalHandler
func (h *EgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
    session := NewRecordingSession(jobCtx, h.config)

    h.mu.Lock()
    h.sessions[jobCtx.Job.Id] = session
    h.mu.Unlock()

    // Set up track subscription callbacks
    jobCtx.Room.Callback.OnTrackSubscribed = session.OnTrackSubscribed
    jobCtx.Room.Callback.OnTrackUnsubscribed = session.OnTrackUnsubscribed

    return session.Start(ctx)
}

// OnJobTerminated implements agent.UniversalHandler
func (h *EgressHandler) OnJobTerminated(ctx context.Context, jobID string) {
    h.mu.Lock()
    session, exists := h.sessions[jobID]
    if exists {
        delete(h.sessions, jobID)
    }
    h.mu.Unlock()

    if exists {
        session.Stop()
    }
}

func (h *Handler) onTrackSubscribed(
    track *webrtc.TrackRemote,
    publication *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
) {
    // Verify codec
    if !h.isCodecSupported(track.Codec()) {
        return
    }

    // Start RTP forwarding
    go h.forwardRTP(track)
}
```

#### 4.2.2 GStreamer Pipeline Manager
```go
package gstreamer

import (
    "context"
    "os/exec"
)

type PipelineManager struct {
    config  PipelineConfig
    process *exec.Cmd
    ctx     context.Context
    cancel  context.CancelFunc
}

func (pm *PipelineManager) Start() error {
    // Build pipeline string
    pipeline := pm.buildPipeline()

    // Start GStreamer as subprocess
    pm.process = exec.CommandContext(pm.ctx, "gst-launch-1.0", "-e", pipeline)

    // Set environment
    pm.process.Env = append(os.Environ(),
        "GST_DEBUG=2",
        "GST_DEBUG_FILE=/var/log/gstreamer.log",
    )

    return pm.process.Start()
}

func (pm *PipelineManager) buildPipeline() string {
    // Build GStreamer pipeline string
    return fmt.Sprintf(`
        rtpbin name=rtpbin latency=%d do-lost=true \
        udpsrc port=%d caps="application/x-rtp,media=video,encoding-name=H264" \
        ! rtpbin.recv_rtp_sink_0 \
        rtpbin. ! rtph264depay ! h264parse \
        ! videorate drop-only=false duplicate-on-gap=true \
        ! mpegtsmux name=mux \
        ! hlssink2 location=%s/segment%%05d.ts \
        udpsrc port=%d caps="application/x-rtp,media=audio,encoding-name=OPUS" \
        ! rtpbin.recv_rtp_sink_1 \
        rtpbin. ! rtpopusdepay ! opusparse \
        ! audiorate tolerance=40000000 add=true \
        ! mux.
    `, pm.config.JitterBuffer, pm.config.VideoPort,
       pm.config.OutputDir, pm.config.AudioPort)
}
```

#### 4.2.3 Example Main
```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress"
    "github.com/livekit/protocol/livekit"
)

func main() {
    // Create egress handler configuration
    config := &egress.Config{
        LiveKitURL:      os.Getenv("LIVEKIT_URL"),
        APIKey:          os.Getenv("LIVEKIT_API_KEY"),
        APISecret:       os.Getenv("LIVEKIT_API_SECRET"),
        VideoPort:       5004,
        AudioPort:       5006,
        OutputDir:       "./recordings",
        SegmentDuration: 4,
    }

    // Create egress handler
    handler := egress.NewEgressHandler(config)

    // Create worker using livekit-agent-sdk-go
    worker := agent.NewUniversalWorker(
        config.LiveKitURL,
        config.APIKey,
        config.APISecret,
        handler,
        agent.WorkerOptions{
            AgentName: "egress-agent",
            JobType:   livekit.JobType_JT_ROOM,
            MaxJobs:   10, // Max concurrent recordings
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
    cancel()
    handler.Shutdown()
    log.Println("Shutdown complete")
}
```

## 5. Configuration

### 5.1 Configuration Schema
```yaml
# config.yaml
connection:
  url: "${LIVEKIT_URL}"
  api_key: "${LIVEKIT_API_KEY}"
  api_secret: "${LIVEKIT_API_SECRET}"
  room_name: "${ROOM_NAME}"

subscription:
  auto_subscribe: true
  video_quality: HIGH

pipeline:
  engine: gstreamer
  video_port: 5004
  audio_port: 5006
  jitter_buffer_ms: 200

output:
  base_dir: "/recordings"
  segment_duration: 4
  cleanup:
    enabled: true
    max_age_hours: 168

monitoring:
  metrics_port: 9090
  health_port: 8080
```

### 5.2 Environment Variables
```bash
export LIVEKIT_URL=wss://example.livekit.cloud
export LIVEKIT_API_KEY=APIxxxxx
export LIVEKIT_API_SECRET=secret
export ROOM_NAME=my-room
export GST_DEBUG=2
```

## 6. Monitoring & Observability

### 6.1 Metrics (Prometheus)
```
# Track metrics
egress_tracks_active{type="video"} 1
egress_packets_routed_total{track="xxx"} 150000
egress_packets_dropped_total{track="xxx"} 5

# Pipeline metrics
egress_pipeline_state{state="playing"} 1
egress_segments_written_total 150

# System metrics
egress_cpu_usage_percent 2.5
egress_memory_usage_mb 95
```

### 6.2 Health Endpoints
```go
// GET /health
{
    "status": "healthy",
    "pipeline": "playing",
    "tracks": {
        "video": 1,
        "audio": 1
    },
    "segments_written": 150
}
```

## 7. Error Handling

### 7.1 Error Recovery Matrix
| Error | Detection | Recovery | Impact |
|-------|-----------|----------|---------|
| Network loss | RTP timeout | Buffer & reconnect | Gap filled |
| Pipeline crash | Process exit | Restart pipeline | < 2s interruption |
| Codec change | MIME check | Restart with new codec | New session |
| Disk full | Write error | Alert & stop | Recording ends |

### 7.2 Gap Filling Strategy
- **Short gaps (< 100ms)**: Handled by GStreamer videorate/audiorate
- **Medium gaps (100ms - 1s)**: Jitter buffer recovery
- **Long gaps (> 1s)**: Insert HLS discontinuity

## 8. Deployment

### 8.1 Binary Deployment
```bash
# Build the agent binary
go build -o egress-agent ./examples/egress-agent

# Ensure GStreamer is installed on the host
# Ubuntu/Debian:
sudo apt-get install -y \
    gstreamer1.0-tools \
    gstreamer1.0-plugins-base \
    gstreamer1.0-plugins-good \
    gstreamer1.0-plugins-bad \
    gstreamer1.0-plugins-ugly \
    gstreamer1.0-libav

# macOS:
brew install gstreamer gst-plugins-base \
    gst-plugins-good gst-plugins-bad \
    gst-plugins-ugly gst-libav
```

### 8.2 Systemd Service (Production)
```ini
[Unit]
Description=LiveKit Egress Agent
After=network.target

[Service]
Type=simple
User=livekit
Group=livekit
WorkingDirectory=/opt/livekit
ExecStart=/opt/livekit/egress-agent --config /etc/livekit/egress.yaml
Restart=always
RestartSec=5
StandardOutput=append:/var/log/livekit/egress.log
StandardError=append:/var/log/livekit/egress-error.log

[Install]
WantedBy=multi-user.target
```

### 8.3 Direct Execution
```bash
# Run directly with configuration
./egress-agent \
  --url wss://example.livekit.cloud \
  --api-key $LIVEKIT_API_KEY \
  --api-secret $LIVEKIT_API_SECRET \
  --room-name my-room \
  --output-dir ./recordings
```

### 8.4 Process Management
```go
// Example integration with existing agent worker
package main

import (
    "github.com/livekit/agent-sdk-go/pkg/agent"
    "github.com/livekit/agent-sdk-go/pkg/egress"
)

func main() {
    // Use existing worker infrastructure
    worker := agent.NewWorker(
        agent.WithHandler(egress.NewHandler()),
        agent.WithLogger(logger),
    )

    if err := worker.Run(); err != nil {
        log.Fatal(err)
    }
}
```

### 8.5 Multi-Instance Deployment
```bash
# Run multiple agents on same host with different configs
./egress-agent --config /etc/livekit/egress-1.yaml --instance-id agent-1 &
./egress-agent --config /etc/livekit/egress-2.yaml --instance-id agent-2 &
./egress-agent --config /etc/livekit/egress-3.yaml --instance-id agent-3 &

# Or use systemd template units
systemctl start egress-agent@1
systemctl start egress-agent@2
systemctl start egress-agent@3
```

## 9. Comprehensive Testing Requirements

### 9.1 Test Infrastructure
```
test/
├── fixtures/           # Test data
│   ├── videos/        # Sample H.264/Opus/MP3 videos
│   ├── rtp/           # Captured RTP packets
│   └── expected/      # Expected outputs
├── mocks/             # Mock implementations
├── unit/              # Unit tests
├── integration/       # Integration tests
└── e2e/              # End-to-end tests
```

### 9.2 Unit Tests (Coverage Target: >80%)
```go
// RTP Router Tests
func TestRTPRouter_PacketForwarding(t *testing.T) {
    router := NewRouter(5004, 5006)

    // Test normal packet
    packet := &rtp.Packet{
        Header: rtp.Header{
            Version: 2,
            PayloadType: 96,
            SequenceNumber: 1000,
            Timestamp: 90000,
        },
        Payload: []byte{0x00, 0x00, 0x00, 0x01, 0x67}, // H.264 SPS
    }

    err := router.RoutePacket(packet, TrackKindVideo)
    assert.NoError(t, err)

    // Verify statistics
    stats := router.GetStatistics()
    assert.Equal(t, uint64(1), stats.PacketsRouted)
}

// GStreamer Pipeline Tests
func TestPipelineBuilder_Validation(t *testing.T) {
    builder := NewPipelineBuilder(config)
    pipeline := builder.Build()

    // Validate syntax
    cmd := exec.Command("gst-launch-1.0", "--gst-parse-only", pipeline)
    assert.NoError(t, cmd.Run())
}

// Gap Detection Tests
func TestGapDetection_SequenceGaps(t *testing.T) {
    detector := NewGapDetector()

    packets := []uint16{1, 2, 3, 5, 6, 7} // Missing 4
    gaps := detector.DetectGaps(packets)

    assert.Len(t, gaps, 1)
    assert.Equal(t, uint16(4), gaps[0].MissingSequence)
}
```

### 9.3 Integration Tests with Real LiveKit Server
```go
// test/integration/setup.go
func SetupLiveKitServer(t *testing.T) (url string, cleanup func()) {
    // Start LiveKit server in Docker
    cmd := exec.Command("docker", "run", "-d",
        "-p", "7880:7880", "-p", "7881:7881",
        "-e", "LIVEKIT_KEYS=devkey:secret",
        "--name", "livekit-test",
        "livekit/livekit-server", "--dev")
    require.NoError(t, cmd.Run())

    // Wait for server readiness
    waitForServer("localhost:7880", 30*time.Second)

    cleanup = func() {
        exec.Command("docker", "stop", "livekit-test").Run()
        exec.Command("docker", "rm", "livekit-test").Run()
    }

    return "ws://localhost:7880", cleanup
}

func TestIntegration_SingleParticipant(t *testing.T) {
    url, cleanup := SetupLiveKitServer(t)
    defer cleanup()

    // Start egress agent
    agent := NewEgressAgent(
        WithLiveKitURL(url),
        WithAPIKey("devkey"),
        WithAPISecret("secret"),
    )
    go agent.Run()
    defer agent.Stop()

    // Create test publisher
    publisher := NewTestPublisher(url, "devkey", "secret", "test-room", "user1")
    defer publisher.Disconnect()

    // Publish video and audio streams from files
    err := publisher.PublishVideoFile("test/fixtures/sample_1080p.mp4")
    require.NoError(t, err)

    err = publisher.PublishAudioFile("test/fixtures/sample_opus.ogg")
    require.NoError(t, err)

    // Wait for HLS generation
    time.Sleep(10 * time.Second)

    // Verify HLS output
    assert.FileExists(t, "output/playlist.m3u8")
    assert.NoError(t, ValidateHLS("output/playlist.m3u8"))
    assertSegmentCount(t, "output", 5) // Should have ~5 segments for 10 seconds
}

func TestIntegration_NetworkResilience(t *testing.T) {
    url, cleanup := SetupLiveKitServer(t)
    defer cleanup()

    // Start agent with gap filling enabled
    agent := NewEgressAgent(
        WithLiveKitURL(url),
        WithGapFilling(true),
    )
    go agent.Run()

    // Simulate network packet loss
    publisher := NewTestPublisher(url, "devkey", "secret", "test-room", "user1")
    publisher.SimulatePacketLoss(0.05) // 5% packet loss

    // Stream video with gaps
    publisher.PublishVideoWithGaps("test/fixtures/video_with_gaps.mp4")

    time.Sleep(10 * time.Second)

    // Verify gap filling worked - no discontinuities in output
    assertNoContinuityErrors(t, "output/playlist.m3u8")
}
```

### 9.4 End-to-End Tests with Real Video
```go
func TestE2E_RealVideoStreaming(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping E2E test")
    }

    // Test videos with different characteristics
    testCases := []struct {
        name     string
        file     string
        verify   func(*testing.T, string)
    }{
        {
            "1080p_30fps_h264_opus",
            "test/fixtures/videos/sample_1080p.mp4",
            verify1080pOutput,
        },
        {
            "720p_60fps_h264_opus",
            "test/fixtures/videos/sample_720p_60fps.mp4",
            verify60fpsOutput,
        },
        {
            "4k_hdr_h264",
            "test/fixtures/videos/sample_4k_hdr.mp4",
            verify4kOutput,
        },
        {
            "video_with_gaps",
            "test/fixtures/videos/sample_gaps.mp4",
            verifyGapFilling,
        },
    }

    for _, tc := range testCases {
        t.Run(tc.name, func(t *testing.T) {
            // Stream video file as RTP
            streamer := NewVideoStreamer()
            streamer.StreamFile(tc.file, 5004, 5006)

            // Start egress agent
            agent := StartEgressAgent(t)

            // Record for duration of video
            duration := GetVideoDuration(tc.file)
            time.Sleep(duration)

            // Verify output
            outputDir := agent.GetOutputDir()
            tc.verify(t, outputDir)
        })
    }
}

func TestE2E_LongRunning(t *testing.T) {
    if !*longTest {
        t.Skip("Skipping long-running test")
    }

    // 1-hour recording test
    agent := StartEgressAgent(t)
    streamer := NewVideoStreamer()

    // Stream 1-hour video
    go streamer.StreamFile("test/fixtures/videos/1hour.mp4", 5004, 5006)

    // Check every 5 minutes
    for i := 0; i < 12; i++ {
        time.Sleep(5 * time.Minute)

        // Verify ongoing recording
        assert.True(t, agent.IsRecording())

        // Check resource usage
        metrics := agent.GetMetrics()
        assert.Less(t, metrics.CPUUsage, 10.0)
        assert.Less(t, metrics.MemoryMB, 200)
    }
}
```

### 9.5 Performance & Load Tests
```go
func BenchmarkRTPRouting(b *testing.B) {
    router := NewRouter(5004, 5006)
    packet := GenerateTestPacket()

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        router.RoutePacket(packet, TrackKindVideo)
    }

    b.ReportMetric(float64(b.N), "packets/op")
}

func TestLoad_ConcurrentStreams(t *testing.T) {
    agent := StartEgressAgent(t)

    // Start 10 concurrent streams
    for i := 0; i < 10; i++ {
        go func(id int) {
            videoPort := 5004 + id*2
            audioPort := 5006 + id*2

            streamer := NewVideoStreamer()
            streamer.StreamFile(
                "test/fixtures/videos/sample.mp4",
                videoPort,
                audioPort,
            )
        }(i)
    }

    // Monitor for 5 minutes
    time.Sleep(5 * time.Minute)

    metrics := agent.GetMetrics()
    assert.Less(t, metrics.CPUUsage, 30.0) // <30% for 10 streams
}
```

### 9.6 Test Execution Matrix
| Test Type | Frequency | Duration | Coverage |
|-----------|-----------|----------|----------|
| Unit Tests | Every commit | <1 min | >80% |
| Integration | Every PR | <5 min | Critical paths |
| E2E (Short) | Every PR | <10 min | Basic scenarios |
| E2E (Full) | Nightly | <30 min | All scenarios |
| Load Tests | Weekly | <1 hour | Stress testing |
| Long-running | Weekly | 1+ hours | Stability |

## 10. Migration Plan

### Phase 1: Core Implementation (Week 1-2)
- Implement egress handler in monorepo
- Add RTP routing logic
- GStreamer pipeline management
- **Testing**: Unit tests for each component (>80% coverage)

### Phase 2: Integration (Week 3-4)
- Integrate with existing agent worker
- Add configuration management
- Implement monitoring
- **Testing**: Integration tests with mock LiveKit server

### Phase 3: Testing (Week 5-6)
- **Unit Tests**: Complete coverage of all modules
- **Integration Tests**: Mock server scenarios
- **E2E Tests**: Real video streaming tests
- **Performance Tests**: Benchmarks and load testing
- **Long-running Tests**: 1+ hour stability tests

### Phase 4: Deployment (Week 7-8)
- Documentation
- Example applications
- Production rollout
- **Testing**: Production smoke tests

## 11. Future Enhancements

### Version 1.1
- Pure Go muxer (remove GStreamer dependency)
- Cloud storage integration
- Webhook notifications

### Version 2.0
- Multi-track recording
- Adaptive bitrate
- Real-time transcoding option

## Appendix A: GStreamer Commands

### Debug Pipeline
```bash
GST_DEBUG=3 gst-launch-1.0 -v [pipeline]
```

### Test with File
```bash
gst-launch-1.0 filesrc location=test.mp4 \
  ! qtdemux ! h264parse ! mpegtsmux \
  ! hlssink2 location=segment%05d.ts
```

## Appendix B: Troubleshooting

### Common Issues

#### No Output
```bash
# Check GStreamer pipeline
GST_DEBUG=3 ./egress-agent

# Verify UDP ports
netstat -lunp | grep 5004
```

#### High CPU
```bash
# Ensure stream-copy mode
# Check for accidental transcoding
ps aux | grep gst-launch
```

---

*Document Version: 4.0*
*Last Updated: 2024*
*Status: Final Specification*
*Deployment: Monorepo Agent Application*