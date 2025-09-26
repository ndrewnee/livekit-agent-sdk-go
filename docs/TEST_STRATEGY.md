# Comprehensive Testing Strategy for Zero-Transcode HLS Egress Agent

## Executive Summary

This document defines a comprehensive testing strategy covering unit tests, integration tests, and end-to-end tests with real video clips for the zero-transcode HLS egress agent.

## 1. Test Infrastructure

### 1.1 Test Data Repository
```
test/
├── fixtures/
│   ├── videos/
│   │   ├── sample_h264_opus.mp4      # H.264 + Opus test file
│   │   ├── sample_h264_mp3.mp4       # H.264 + MP3 test file
│   │   ├── sample_1080p_30fps.mp4    # Full HD test
│   │   ├── sample_720p_60fps.mp4     # HD high framerate
│   │   ├── sample_4k_hdr.mp4         # 4K test file
│   │   └── sample_with_gaps.mp4      # File with intentional gaps
│   ├── rtp/
│   │   ├── h264_packets.pcap         # Captured H.264 RTP packets
│   │   ├── opus_packets.pcap         # Captured Opus RTP packets
│   │   └── mixed_stream.pcap         # A/V synchronized stream
│   └── expected/
│       ├── hls_output/                # Expected HLS segments
│       └── screenshots/               # Expected screenshots
├── mocks/
│   ├── livekit_server.go             # Mock LiveKit server
│   ├── participant.go                # Mock participant
│   └── track_publisher.go            # Mock track publisher
└── utils/
    ├── video_generator.go             # Generate test videos
    ├── rtp_simulator.go               # Simulate RTP streams
    └── validator.go                   # Validate HLS output
```

### 1.2 Test Video Generation
```go
package testutils

import (
    "os/exec"
    "fmt"
)

type VideoGenerator struct {
    ffmpegPath string
}

// GenerateH264OpusVideo creates a test video with H.264 video and Opus audio
func (vg *VideoGenerator) GenerateH264OpusVideo(output string, duration int) error {
    cmd := exec.Command(vg.ffmpegPath,
        "-f", "lavfi", "-i", "testsrc=duration="+fmt.Sprint(duration)+":size=1280x720:rate=30",
        "-f", "lavfi", "-i", "sine=frequency=1000:duration="+fmt.Sprint(duration),
        "-c:v", "libx264", "-preset", "ultrafast", "-profile:v", "baseline",
        "-c:a", "libopus", "-b:a", "128k",
        "-f", "mp4", output,
    )
    return cmd.Run()
}

// GenerateVideoWithGaps creates a video with intentional packet loss
func (vg *VideoGenerator) GenerateVideoWithGaps(output string) error {
    // Generate base video
    baseVideo := "base.mp4"
    if err := vg.GenerateH264OpusVideo(baseVideo, 30); err != nil {
        return err
    }

    // Add gaps using video filters
    cmd := exec.Command(vg.ffmpegPath,
        "-i", baseVideo,
        "-vf", "select='not(between(t,5,5.5)+between(t,10,10.2)+between(t,15,15.1))'",
        "-af", "aselect='not(between(t,5,5.5)+between(t,10,10.2)+between(t,15,15.1))'",
        "-c:v", "libx264", "-c:a", "libopus",
        output,
    )
    return cmd.Run()
}

// GenerateMultiResolutionVideo creates videos at different resolutions
func (vg *VideoGenerator) GenerateMultiResolutionVideo() error {
    resolutions := []string{"1920x1080", "1280x720", "640x480"}
    for _, res := range resolutions {
        output := fmt.Sprintf("test_%s.mp4", res)
        cmd := exec.Command(vg.ffmpegPath,
            "-f", "lavfi", "-i", fmt.Sprintf("testsrc=duration=10:size=%s:rate=30", res),
            "-f", "lavfi", "-i", "sine=frequency=1000:duration=10",
            "-c:v", "libx264", "-c:a", "libopus",
            output,
        )
        if err := cmd.Run(); err != nil {
            return err
        }
    }
    return nil
}
```

## 2. Unit Tests

### 2.1 RTP Router Tests
```go
package rtp_test

import (
    "testing"
    "net"
    "github.com/pion/rtp"
    "github.com/stretchr/testify/assert"
    "github.com/livekit/agent-sdk-go/pkg/egress/rtp"
)

func TestRTPRouter_RoutePacket(t *testing.T) {
    tests := []struct {
        name        string
        packet      *rtp.Packet
        trackKind   string
        expectError bool
    }{
        {
            name: "route video packet successfully",
            packet: &rtp.Packet{
                Header: rtp.Header{
                    Version:        2,
                    PayloadType:    96,
                    SequenceNumber: 1000,
                    Timestamp:      90000,
                    SSRC:           12345,
                },
                Payload: []byte{0x00, 0x00, 0x00, 0x01, 0x67}, // H.264 SPS NAL
            },
            trackKind:   "video",
            expectError: false,
        },
        {
            name: "route audio packet successfully",
            packet: &rtp.Packet{
                Header: rtp.Header{
                    Version:        2,
                    PayloadType:    111,
                    SequenceNumber: 500,
                    Timestamp:      48000,
                    SSRC:           54321,
                },
                Payload: []byte{0x78, 0x80}, // Opus TOC
            },
            trackKind:   "audio",
            expectError: false,
        },
        {
            name: "handle corrupted packet",
            packet: &rtp.Packet{
                Header: rtp.Header{Version: 99}, // Invalid version
            },
            trackKind:   "video",
            expectError: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // Create UDP listener for testing
            videoAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:15004")
            audioAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:15006")

            videoConn, _ := net.ListenUDP("udp", videoAddr)
            audioConn, _ := net.ListenUDP("udp", audioAddr)
            defer videoConn.Close()
            defer audioConn.Close()

            // Create router
            router := rtp.NewRouter(15004, 15006)
            defer router.Close()

            // Route packet
            err := router.RoutePacket(tt.packet, tt.trackKind)

            if tt.expectError {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)

                // Verify packet received
                buf := make([]byte, 1500)
                conn := videoConn
                if tt.trackKind == "audio" {
                    conn = audioConn
                }

                conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
                n, _, err := conn.ReadFromUDP(buf)
                assert.NoError(t, err)
                assert.Greater(t, n, 0)

                // Verify statistics
                stats := router.GetStatistics()
                assert.Equal(t, uint64(1), stats.PacketsRouted)
                assert.Equal(t, uint64(n), stats.BytesRouted)
            }
        })
    }
}

func TestRTPRouter_Throughput(t *testing.T) {
    router := rtp.NewRouter(15004, 15006)
    defer router.Close()

    // Generate 1000 packets
    packets := generateTestPackets(1000)

    start := time.Now()
    for _, pkt := range packets {
        err := router.RoutePacket(pkt, "video")
        assert.NoError(t, err)
    }
    duration := time.Since(start)

    // Should route 1000 packets in less than 100ms
    assert.Less(t, duration, 100*time.Millisecond)

    stats := router.GetStatistics()
    assert.Equal(t, uint64(1000), stats.PacketsRouted)
    assert.Equal(t, uint64(0), stats.PacketsDropped)
}
```

### 2.2 GStreamer Pipeline Tests
```go
package gstreamer_test

import (
    "testing"
    "os"
    "os/exec"
    "github.com/stretchr/testify/assert"
    "github.com/livekit/agent-sdk-go/pkg/egress/gstreamer"
)

func TestPipelineBuilder_BuildPipeline(t *testing.T) {
    tests := []struct {
        name   string
        config gstreamer.PipelineConfig
        verify func(t *testing.T, pipeline string)
    }{
        {
            name: "basic pipeline",
            config: gstreamer.PipelineConfig{
                VideoPort:       5004,
                AudioPort:       5006,
                OutputDir:       "/tmp/test",
                SegmentDuration: 4,
                JitterBufferMs:  200,
            },
            verify: func(t *testing.T, pipeline string) {
                assert.Contains(t, pipeline, "rtpbin")
                assert.Contains(t, pipeline, "latency=200")
                assert.Contains(t, pipeline, "port=5004")
                assert.Contains(t, pipeline, "port=5006")
                assert.Contains(t, pipeline, "hlssink2")
                assert.Contains(t, pipeline, "target-duration=4")
            },
        },
        {
            name: "pipeline with screenshots",
            config: gstreamer.PipelineConfig{
                VideoPort:          5004,
                AudioPort:          5006,
                OutputDir:          "/tmp/test",
                EnableScreenshots:  true,
                ScreenshotInterval: 5,
            },
            verify: func(t *testing.T, pipeline string) {
                assert.Contains(t, pipeline, "tee name=video_tee")
                assert.Contains(t, pipeline, "jpegenc")
                assert.Contains(t, pipeline, "multifilesink")
                assert.Contains(t, pipeline, "framerate=1/5")
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            builder := gstreamer.NewPipelineBuilder(tt.config)
            pipeline := builder.Build()

            // Verify pipeline structure
            tt.verify(t, pipeline)

            // Validate pipeline syntax
            cmd := exec.Command("gst-launch-1.0", "--gst-parse-only", pipeline)
            err := cmd.Run()
            assert.NoError(t, err, "Pipeline should have valid GStreamer syntax")
        })
    }
}

func TestPipelineManager_StartStop(t *testing.T) {
    config := gstreamer.PipelineConfig{
        VideoPort:       5004,
        AudioPort:       5006,
        OutputDir:       t.TempDir(),
        SegmentDuration: 2,
        JitterBufferMs:  100,
    }

    manager := gstreamer.NewPipelineManager(config)

    // Start pipeline
    err := manager.Start()
    assert.NoError(t, err)
    assert.Equal(t, gstreamer.StatePlaying, manager.GetState())

    // Let it run briefly
    time.Sleep(100 * time.Millisecond)

    // Stop pipeline
    err = manager.Stop()
    assert.NoError(t, err)
    assert.Equal(t, gstreamer.StateStopped, manager.GetState())
}

func TestPipelineManager_CrashRecovery(t *testing.T) {
    config := gstreamer.PipelineConfig{
        VideoPort:       5004,
        AudioPort:       5006,
        OutputDir:       t.TempDir(),
        EnableCrashRecovery: true,
        MaxRestarts:     3,
    }

    manager := gstreamer.NewPipelineManager(config)

    // Track restart attempts
    restartCount := 0
    manager.OnRestart(func() {
        restartCount++
    })

    // Start pipeline
    err := manager.Start()
    assert.NoError(t, err)

    // Simulate crash
    manager.SimulateCrash()

    // Wait for recovery
    time.Sleep(2 * time.Second)

    // Should have restarted
    assert.Greater(t, restartCount, 0)
    assert.Equal(t, gstreamer.StatePlaying, manager.GetState())

    // Clean up
    manager.Stop()
}
```

### 2.3 Codec Verification Tests
```go
package track_test

import (
    "testing"
    "github.com/pion/webrtc/v3"
    "github.com/stretchr/testify/assert"
    "github.com/livekit/agent-sdk-go/pkg/egress/track"
)

func TestCodecVerifier_IsSupported(t *testing.T) {
    verifier := track.NewCodecVerifier()

    tests := []struct {
        name      string
        codec     webrtc.RTPCodecParameters
        supported bool
    }{
        {
            name: "H.264 baseline",
            codec: webrtc.RTPCodecParameters{
                MimeType: "video/H264",
                SDPFmtpLine: "profile-level-id=42e01e",
            },
            supported: true,
        },
        {
            name: "H.264 high profile",
            codec: webrtc.RTPCodecParameters{
                MimeType: "video/H264",
                SDPFmtpLine: "profile-level-id=640028",
            },
            supported: true,
        },
        {
            name: "Opus 48kHz",
            codec: webrtc.RTPCodecParameters{
                MimeType: "audio/opus",
                ClockRate: 48000,
                Channels: 2,
            },
            supported: true,
        },
        {
            name: "MP3",
            codec: webrtc.RTPCodecParameters{
                MimeType: "audio/mpeg",
                ClockRate: 44100,
            },
            supported: true,
        },
        {
            name: "VP8 (unsupported)",
            codec: webrtc.RTPCodecParameters{
                MimeType: "video/VP8",
            },
            supported: false,
        },
        {
            name: "VP9 (unsupported)",
            codec: webrtc.RTPCodecParameters{
                MimeType: "video/VP9",
            },
            supported: false,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := verifier.IsSupported(tt.codec)
            assert.Equal(t, tt.supported, result)
        })
    }
}
```

## 3. Integration Tests

### 3.1 Real LiveKit Server Setup

#### Docker Compose Configuration
```yaml
# test/integration/docker-compose.yaml
version: '3.8'

services:
  livekit:
    image: livekit/livekit-server:latest
    ports:
      - "7880:7880"  # WebRTC
      - "7881:7881"  # HTTP API
    environment:
      - LIVEKIT_KEYS="devkey:secret"
      - LIVEKIT_PORT=7880
      - LIVEKIT_BIND=0.0.0.0
      - LIVEKIT_LOG_LEVEL=info
    command: --dev --bind 0.0.0.0
```

#### Test Publisher Client
```go
package integration

import (
    "context"
    "os"
    "os/exec"
    "time"
    "github.com/livekit/server-sdk-go/v2/lksdk"
    "github.com/pion/webrtc/v3"
    "github.com/pion/rtp"
)

type TestPublisher struct {
    room     *lksdk.Room
    identity string
}

func NewTestPublisher(url, apiKey, apiSecret, roomName, identity string) (*TestPublisher, error) {
    room, err := lksdk.ConnectToRoomWithToken(url,
        lksdk.AccessToken(apiKey, apiSecret).Grant(lksdk.RoomJoin(roomName)),
        lksdk.WithAutoSubscribe(false),
    )
    if err != nil {
        return nil, err
    }

    return &TestPublisher{
        room:     room,
        identity: identity,
    }, nil
}

func (p *TestPublisher) PublishVideoFile(filename string, codec string) error {
    // Use GStreamer to stream video file
    pipeline := fmt.Sprintf(`
        gst-launch-1.0 \
        filesrc location=%s ! decodebin ! videorate ! video/x-raw,framerate=30/1 \
        ! x264enc tune=zerolatency ! rtph264pay \
        ! udpsink host=127.0.0.1 port=5004
    `, filename)

    cmd := exec.Command("sh", "-c", pipeline)
    return cmd.Start()
}

func (p *TestPublisher) PublishAudioFile(filename string) error {
    // Use GStreamer to stream audio file
    pipeline := fmt.Sprintf(`
        gst-launch-1.0 \
        filesrc location=%s ! decodebin ! audioconvert ! audioresample \
        ! opusenc ! rtpopuspay \
        ! udpsink host=127.0.0.1 port=5006
    `, filename)

    cmd := exec.Command("sh", "-c", pipeline)
    return cmd.Start()
}

func (p *TestPublisher) Close() {
    p.room.Disconnect()
}
```

### 3.2 Integration Test Suite
```go
package integration_test

import (
    "testing"
    "context"
    "time"
    "path/filepath"
    "os/exec"
    "github.com/stretchr/testify/suite"
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress"
    "github.com/livekit/agent-sdk-go/test/integration"
)

type EgressIntegrationSuite struct {
    suite.Suite
    lkServer  *exec.Cmd
    agent     *agent.Worker
    outputDir string
    url       string
    apiKey    string
    apiSecret string
}

func (suite *EgressIntegrationSuite) SetupSuite() {
    // Start LiveKit server using docker-compose
    suite.lkServer = exec.Command("docker-compose",
        "-f", "test/integration/docker-compose.yaml",
        "up", "-d")
    err := suite.lkServer.Run()
    suite.NoError(err)

    // Wait for server to be ready
    time.Sleep(5 * time.Second)

    suite.url = "ws://localhost:7880"
    suite.apiKey = "devkey"
    suite.apiSecret = "secret"
}

func (suite *EgressIntegrationSuite) TearDownSuite() {
    // Stop LiveKit server
    cmd := exec.Command("docker-compose",
        "-f", "test/integration/docker-compose.yaml",
        "down")
    cmd.Run()
}

func (suite *EgressIntegrationSuite) SetupTest() {
    suite.outputDir = suite.T().TempDir()

    config := egress.Config{
        VideoPort:       5004,
        AudioPort:       5006,
        OutputDir:       suite.outputDir,
        SegmentDuration: 2,
    }

    handler := egress.NewEgressHandler(config)
    suite.agent = agent.NewUniversalWorker(
        suite.url,
        suite.apiKey,
        suite.apiSecret,
        handler,
        agent.WorkerOptions{
            AgentName: "test-egress-agent",
            JobType:   livekit.JobType_JT_ROOM,
            MaxJobs:   5,
        },
    )
}

func (suite *EgressIntegrationSuite) TestSingleParticipantRecording() {
    ctx := context.Background()

    // Start agent to connect to LiveKit
    go suite.agent.Run(ctx)
    time.Sleep(2 * time.Second) // Wait for agent to connect

    // Create test publisher and join room
    publisher, err := integration.NewTestPublisher(
        suite.url,
        suite.apiKey,
        suite.apiSecret,
        "test-room",
        "user-1",
    )
    suite.NoError(err)
    defer publisher.Close()

    // Publish video and audio from test files
    err = publisher.PublishVideoFile("test/fixtures/videos/sample_1080p.mp4", "h264")
    suite.NoError(err)

    err = publisher.PublishAudioFile("test/fixtures/audio/sample_opus.ogg")
    suite.NoError(err)

    // Let it record for 10 seconds
    time.Sleep(10 * time.Second)

    // Stop recording
    suite.agent.Stop()

    // Verify HLS output
    suite.verifyHLSOutput()

    // Verify segments created
    segments := filepath.Glob(filepath.Join(suite.outputDir, "*.ts"))
    suite.Greater(len(segments), 0, "Should have created HLS segments")

    // Verify playlist
    playlistPath := filepath.Join(suite.outputDir, "playlist.m3u8")
    suite.FileExists(playlistPath)
}

func (suite *EgressIntegrationSuite) TestMultipleParticipants() {
    ctx := context.Background()

    // Start agent
    go suite.agent.Run(ctx)
    time.Sleep(2 * time.Second)

    // Create 3 test publishers
    publishers := make([]*integration.TestPublisher, 3)

    for i := 0; i < 3; i++ {
        publisher, err := integration.NewTestPublisher(
            suite.url,
            suite.apiKey,
            suite.apiSecret,
            "test-room",
            fmt.Sprintf("user-%d", i+1),
        )
        suite.NoError(err)
        publishers[i] = publisher

        // Each publisher streams different content
        videoFile := fmt.Sprintf("test/fixtures/videos/sample_%dp.mp4", []int{1080, 720, 480}[i])
        err = publisher.PublishVideoFile(videoFile, "h264")
        suite.NoError(err)

        err = publisher.PublishAudioFile("test/fixtures/audio/sample_opus.ogg")
        suite.NoError(err)
    }

    // Let it record for 10 seconds
    time.Sleep(10 * time.Second)

    // Stop agent and publishers
    suite.agent.Stop()
    for _, pub := range publishers {
        pub.Close()
    }

    // Verify HLS output exists for all participants
    suite.verifyHLSOutput()

    // Check that we have content from multiple sources
    segments := filepath.Glob(filepath.Join(suite.outputDir, "*.ts"))
    suite.GreaterOrEqual(len(segments), 3, "Should have multiple segments")

    // Verify metrics show all tracks
    metrics := suite.agent.GetMetrics()
    suite.Equal(6, metrics.TracksActive) // 3 video + 3 audio
}

func (suite *EgressIntegrationSuite) TestGapHandling() {
    ctx := context.Background()

    // Start agent
    go suite.agent.Run(ctx)
    time.Sleep(2 * time.Second)

    // Create publisher that will simulate network issues
    publisher, err := integration.NewTestPublisher(
        suite.url,
        suite.apiKey,
        suite.apiSecret,
        "test-room",
        "user-1",
    )
    suite.NoError(err)
    defer publisher.Close()

    // Start streaming with simulated gaps
    go suite.simulateNetworkGaps(publisher)

    // Start normal audio streaming
    err = publisher.PublishAudioFile("test/fixtures/audio/sample_opus.ogg")
    suite.NoError(err)

    // Stream with gaps
    go videoTrack.StreamRTPWithGaps("test/fixtures/rtp/h264_with_gaps.pcap")

    time.Sleep(15 * time.Second)
    suite.agent.Stop()

    // Verify gaps were filled
    suite.verifyGapFilling()
}

func (suite *EgressIntegrationSuite) verifyHLSOutput() {
    // Check master playlist exists
    masterPlaylist := filepath.Join(suite.outputDir, "master.m3u8")
    suite.FileExists(masterPlaylist)

    // Check media playlist
    mediaPlaylist := filepath.Join(suite.outputDir, "media.m3u8")
    suite.FileExists(mediaPlaylist)

    // Check segments
    segments, err := filepath.Glob(filepath.Join(suite.outputDir, "segment*.ts"))
    suite.NoError(err)
    suite.Greater(len(segments), 0)

    // Validate HLS with ffprobe
    cmd := exec.Command("ffprobe", "-v", "error", mediaPlaylist)
    err = cmd.Run()
    suite.NoError(err, "HLS output should be valid")
}

func (suite *EgressIntegrationSuite) verifyGapFilling() {
    // Analyze output for gap filling
    mediaPlaylist := filepath.Join(suite.outputDir, "media.m3u8")

    // Use ffprobe to check for discontinuities
    cmd := exec.Command("ffprobe",
        "-v", "error",
        "-show_entries", "frame=pts_time,pkt_duration_time",
        "-of", "json",
        mediaPlaylist,
    )

    output, err := cmd.Output()
    suite.NoError(err)

    // Parse and verify no large gaps
    var result struct {
        Frames []struct {
            PtsTime         string `json:"pts_time"`
            PktDurationTime string `json:"pkt_duration_time"`
        } `json:"frames"`
    }
    json.Unmarshal(output, &result)

    // Check frame continuity
    for i := 1; i < len(result.Frames); i++ {
        prev, _ := strconv.ParseFloat(result.Frames[i-1].PtsTime, 64)
        curr, _ := strconv.ParseFloat(result.Frames[i].PtsTime, 64)
        gap := curr - prev

        // Gap should be less than 100ms (filled)
        suite.Less(gap, 0.1, "Gap should be filled")
    }
}

func TestEgressIntegrationSuite(t *testing.T) {
    suite.Run(t, new(EgressIntegrationSuite))
}
```

## 4. End-to-End Tests with Real Video

### 4.1 Test Video Publisher
```go
package e2e_test

import (
    "os/exec"
    "fmt"
)

type VideoPublisher struct {
    ffmpegPath string
    rtpPort    int
}

// PublishVideoFile streams a video file as RTP
func (vp *VideoPublisher) PublishVideoFile(filename string, videoPort, audioPort int) (*exec.Cmd, error) {
    cmd := exec.Command(vp.ffmpegPath,
        "-re", // Real-time streaming
        "-i", filename,
        "-c:v", "copy", // No re-encoding
        "-c:a", "copy",
        "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d", videoPort),
        "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d", audioPort),
    )

    if err := cmd.Start(); err != nil {
        return nil, err
    }

    return cmd, nil
}

// PublishWebcam streams from a real webcam
func (vp *VideoPublisher) PublishWebcam(device string, duration int) (*exec.Cmd, error) {
    cmd := exec.Command(vp.ffmpegPath,
        "-f", "v4l2", // Linux webcam
        "-i", device,
        "-t", fmt.Sprint(duration),
        "-c:v", "libx264", "-preset", "ultrafast",
        "-f", "rtp", "rtp://127.0.0.1:5004",
    )

    return cmd, cmd.Start()
}

// PublishScreenCapture streams screen capture
func (vp *VideoPublisher) PublishScreenCapture(duration int) (*exec.Cmd, error) {
    cmd := exec.Command(vp.ffmpegPath,
        "-f", "x11grab", // Linux screen capture
        "-i", ":0.0",
        "-t", fmt.Sprint(duration),
        "-c:v", "libx264", "-preset", "ultrafast",
        "-f", "rtp", "rtp://127.0.0.1:5004",
    )

    return cmd, cmd.Start()
}
```

### 4.2 End-to-End Test Suite
```go
package e2e_test

import (
    "testing"
    "os"
    "path/filepath"
    "context"
    "time"
)

type E2ETestSuite struct {
    suite.Suite
    livekit      *RealLiveKitServer
    agent        *agent.Worker
    publisher    *VideoPublisher
    outputDir    string
}

func (suite *E2ETestSuite) SetupSuite() {
    // Start real LiveKit server (docker or local)
    suite.livekit = StartLiveKitServer()
    suite.publisher = &VideoPublisher{ffmpegPath: "ffmpeg"}
}

func (suite *E2ETestSuite) SetupTest() {
    suite.outputDir = suite.T().TempDir()

    config := egress.Config{
        URL:             suite.livekit.URL,
        APIKey:          suite.livekit.APIKey,
        APISecret:       suite.livekit.APISecret,
        VideoPort:       5004,
        AudioPort:       5006,
        OutputDir:       suite.outputDir,
        SegmentDuration: 4,
    }

    handler := egress.NewEgressHandler(config)
    suite.agent = agent.NewUniversalWorker(
        suite.livekit.URL,
        suite.livekit.APIKey,
        suite.livekit.APISecret,
        handler,
        agent.WorkerOptions{
            AgentName: "test-egress-agent",
            JobType:   livekit.JobType_JT_ROOM,
            MaxJobs:   5,
        },
    )
}

func (suite *E2ETestSuite) TestRealVideoFile() {
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
    defer cancel()

    // Start agent
    go suite.agent.Run(ctx)
    time.Sleep(2 * time.Second) // Wait for connection

    // Test with different video files
    testFiles := []struct {
        name     string
        file     string
        duration time.Duration
    }{
        {"1080p H.264", "test/fixtures/videos/sample_1080p_h264.mp4", 30 * time.Second},
        {"720p 60fps", "test/fixtures/videos/sample_720p_60fps.mp4", 30 * time.Second},
        {"4K HDR", "test/fixtures/videos/sample_4k_hdr.mp4", 15 * time.Second},
        {"With gaps", "test/fixtures/videos/sample_with_gaps.mp4", 30 * time.Second},
    }

    for _, test := range testFiles {
        suite.Run(test.name, func() {
            // Create room
            room := suite.livekit.CreateRoom(fmt.Sprintf("test-%s", test.name))

            // Join as publisher
            publisher := suite.livekit.JoinAsPublisher(room, "publisher")

            // Stream video file
            cmd, err := suite.publisher.PublishVideoFile(test.file, 5004, 5006)
            suite.NoError(err)

            // Wait for recording
            time.Sleep(test.duration)

            // Stop streaming
            cmd.Process.Kill()

            // Verify output
            suite.verifyRecording(test.name)
        })
    }
}

func (suite *E2ETestSuite) TestLongRunningRecording() {
    ctx, cancel := context.WithTimeout(context.Background(), 1*time.Hour)
    defer cancel()

    // Start agent
    go suite.agent.Run(ctx)

    // Create room
    room := suite.livekit.CreateRoom("long-test")

    // Stream 1-hour video
    longVideo := "test/fixtures/videos/1hour_test.mp4"
    cmd, err := suite.publisher.PublishVideoFile(longVideo, 5004, 5006)
    suite.NoError(err)
    defer cmd.Process.Kill()

    // Check periodically
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    startTime := time.Now()
    for {
        select {
        case <-ticker.C:
            elapsed := time.Since(startTime)
            suite.verifyProgressAt(elapsed)

            // Check resource usage
            suite.checkResourceUsage()

        case <-ctx.Done():
            // Final verification
            suite.verifyCompleteRecording()
            return
        }
    }
}

func (suite *E2ETestSuite) TestStressWithMultipleStreams() {
    ctx := context.Background()

    // Start agent
    go suite.agent.Run(ctx)

    room := suite.livekit.CreateRoom("stress-test")

    // Start 10 concurrent streams
    var commands []*exec.Cmd
    for i := 0; i < 10; i++ {
        videoPort := 5004 + i*2
        audioPort := 5006 + i*2

        cmd, err := suite.publisher.PublishVideoFile(
            "test/fixtures/videos/sample_720p.mp4",
            videoPort,
            audioPort,
        )
        suite.NoError(err)
        commands = append(commands, cmd)
    }

    // Record for 5 minutes
    time.Sleep(5 * time.Minute)

    // Stop all streams
    for _, cmd := range commands {
        cmd.Process.Kill()
    }

    // Verify
    suite.verifyConcurrentRecordings(10)

    // Check performance metrics
    metrics := suite.agent.GetMetrics()
    suite.Less(metrics.CPUUsage, 30.0) // Should stay under 30% even with 10 streams
}

func (suite *E2ETestSuite) TestNetworkConditions() {
    testCases := []struct {
        name      string
        setup     func()
        teardown  func()
    }{
        {
            name: "5% packet loss",
            setup: func() {
                exec.Command("tc", "qdisc", "add", "dev", "lo", "root", "netem", "loss", "5%").Run()
            },
            teardown: func() {
                exec.Command("tc", "qdisc", "del", "dev", "lo", "root").Run()
            },
        },
        {
            name: "High jitter",
            setup: func() {
                exec.Command("tc", "qdisc", "add", "dev", "lo", "root", "netem", "delay", "50ms", "20ms").Run()
            },
            teardown: func() {
                exec.Command("tc", "qdisc", "del", "dev", "lo", "root").Run()
            },
        },
        {
            name: "Bandwidth limit",
            setup: func() {
                exec.Command("tc", "qdisc", "add", "dev", "lo", "root", "tbf", "rate", "1mbit", "burst", "32kbit", "latency", "400ms").Run()
            },
            teardown: func() {
                exec.Command("tc", "qdisc", "del", "dev", "lo", "root").Run()
            },
        },
    }

    for _, tc := range testCases {
        suite.Run(tc.name, func() {
            // Apply network condition
            tc.setup()
            defer tc.teardown()

            ctx := context.Background()
            go suite.agent.Run(ctx)

            // Stream video
            cmd, _ := suite.publisher.PublishVideoFile(
                "test/fixtures/videos/sample_720p.mp4",
                5004, 5006,
            )

            time.Sleep(30 * time.Second)
            cmd.Process.Kill()

            // Verify recording quality
            suite.verifyRecordingQuality(tc.name)
        })
    }
}

func (suite *E2ETestSuite) verifyRecording(testName string) {
    outputPath := filepath.Join(suite.outputDir, testName)

    // Check HLS files
    suite.FileExists(filepath.Join(outputPath, "playlist.m3u8"))

    // Validate with ffprobe
    cmd := exec.Command("ffprobe",
        "-v", "error",
        "-show_entries", "stream=codec_name,width,height,r_frame_rate",
        "-of", "json",
        filepath.Join(outputPath, "playlist.m3u8"),
    )

    output, err := cmd.Output()
    suite.NoError(err)

    // Parse output
    var probe struct {
        Streams []struct {
            CodecName   string `json:"codec_name"`
            Width       int    `json:"width"`
            Height      int    `json:"height"`
            RFrameRate  string `json:"r_frame_rate"`
        } `json:"streams"`
    }
    json.Unmarshal(output, &probe)

    // Verify video properties preserved
    suite.Equal("h264", probe.Streams[0].CodecName)
    suite.Greater(probe.Streams[0].Width, 0)
    suite.Greater(probe.Streams[0].Height, 0)
}

func (suite *E2ETestSuite) checkResourceUsage() {
    // Get process stats
    pid := os.Getpid()
    statFile := fmt.Sprintf("/proc/%d/stat", pid)
    data, _ := os.ReadFile(statFile)

    // Parse CPU usage
    var utime, stime int64
    fmt.Sscanf(string(data), "%*d %*s %*c %*d %*d %*d %*d %*d %*u %*u %*u %*u %*u %d %d", &utime, &stime)

    cpuUsage := float64(utime+stime) / 100.0
    suite.Less(cpuUsage, 10.0, "CPU usage should be less than 10%")

    // Check memory
    statusFile := fmt.Sprintf("/proc/%d/status", pid)
    statusData, _ := os.ReadFile(statusFile)

    var vmRSS int64
    for _, line := range strings.Split(string(statusData), "\n") {
        if strings.HasPrefix(line, "VmRSS:") {
            fmt.Sscanf(line, "VmRSS: %d", &vmRSS)
            break
        }
    }

    suite.Less(vmRSS, int64(500*1024), "Memory usage should be less than 500MB")
}

func TestE2ESuite(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping E2E tests in short mode")
    }

    suite.Run(t, new(E2ETestSuite))
}
```

## 5. S3/MinIO Storage Integration Tests

### 5.1 MinIO Local Setup for Testing

#### Docker Compose Configuration
```yaml
# test/integration/docker-compose.yaml
version: '3.8'

services:
  livekit:
    image: livekit/livekit-server:latest
    ports:
      - "7880:7880"
      - "7881:7881"
    environment:
      - LIVEKIT_KEYS="devkey:secret"
      - LIVEKIT_PORT=7880
      - LIVEKIT_BIND=0.0.0.0
      - LIVEKIT_LOG_LEVEL=info
    command: --dev --bind 0.0.0.0

  minio:
    image: minio/minio:latest
    ports:
      - "9000:9000"     # S3 API
      - "9001:9001"     # Console
    environment:
      - MINIO_ROOT_USER=minioadmin
      - MINIO_ROOT_PASSWORD=minioadmin
    command: server /data --console-address ":9001"
    volumes:
      - minio_data:/data

volumes:
  minio_data:
```

### 5.2 MinIO Client Setup
```go
package storage_test

import (
    "context"
    "testing"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/s3"
    "github.com/aws/aws-sdk-go-v2/credentials"
    "github.com/stretchr/testify/suite"
)

type S3IntegrationSuite struct {
    suite.Suite
    s3Client   *s3.Client
    bucket     string
    minioURL   string
}

func (suite *S3IntegrationSuite) SetupSuite() {
    // Configure MinIO client
    suite.minioURL = "http://localhost:9000"
    suite.bucket = "test-recordings"

    cfg, err := config.LoadDefaultConfig(context.TODO(),
        config.WithCredentialsProvider(
            credentials.NewStaticCredentialsProvider(
                "minioadmin",
                "minioadmin",
                "",
            ),
        ),
        config.WithEndpointResolver(aws.EndpointResolverFunc(
            func(service, region string) (aws.Endpoint, error) {
                return aws.Endpoint{
                    URL:           suite.minioURL,
                    SigningRegion: "us-east-1",
                }, nil
            },
        )),
    )
    suite.NoError(err)

    suite.s3Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
        o.UsePathStyle = true // Required for MinIO
    })

    // Create test bucket
    ctx := context.Background()
    _, err = suite.s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
        Bucket: aws.String(suite.bucket),
    })
    if err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") {
        suite.NoError(err)
    }
}

func (suite *S3IntegrationSuite) TearDownSuite() {
    // Clean up test bucket
    ctx := context.Background()

    // Delete all objects
    objects, _ := suite.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
        Bucket: aws.String(suite.bucket),
    })

    for _, obj := range objects.Contents {
        suite.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
            Bucket: aws.String(suite.bucket),
            Key:    obj.Key,
        })
    }

    // Delete bucket
    suite.s3Client.DeleteBucket(ctx, &s3.DeleteBucketInput{
        Bucket: aws.String(suite.bucket),
    })
}
```

### 5.3 S3 Upload Integration Tests
```go
func (suite *S3IntegrationSuite) TestSegmentUpload() {
    ctx := context.Background()

    // Create S3 uploader
    uploader := storage.NewS3Uploader(storage.S3Config{
        Client: suite.s3Client,
        Bucket: suite.bucket,
        Prefix: "test-room/test-egress/",
    })

    // Generate test segment
    segment := generateTestHLSSegment(4 * time.Second)
    segmentKey := "segment_00001.m4s"

    // Upload segment
    err := uploader.UploadSegment(ctx, segment, segmentKey)
    suite.NoError(err)

    // Verify upload
    head, err := suite.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
        Bucket: aws.String(suite.bucket),
        Key:    aws.String("test-room/test-egress/" + segmentKey),
    })
    suite.NoError(err)
    suite.Equal(int64(len(segment)), head.ContentLength)
}

func (suite *S3IntegrationSuite) TestPlaylistUpdate() {
    ctx := context.Background()

    uploader := storage.NewS3Uploader(storage.S3Config{
        Client: suite.s3Client,
        Bucket: suite.bucket,
        Prefix: "test-room/test-egress/",
    })

    // Upload multiple segments and update playlist
    for i := 0; i < 5; i++ {
        segment := generateTestHLSSegment(4 * time.Second)
        segmentKey := fmt.Sprintf("segment_%05d.m4s", i)

        err := uploader.UploadSegment(ctx, segment, segmentKey)
        suite.NoError(err)

        // Update playlist
        playlist := generatePlaylist(i + 1)
        err = uploader.UpdatePlaylist(ctx, playlist, "playlist.m3u8")
        suite.NoError(err)
    }

    // Download and verify final playlist
    obj, err := suite.s3Client.GetObject(ctx, &s3.GetObjectInput{
        Bucket: aws.String(suite.bucket),
        Key:    aws.String("test-room/test-egress/playlist.m3u8"),
    })
    suite.NoError(err)

    playlistContent, _ := io.ReadAll(obj.Body)
    suite.Contains(string(playlistContent), "segment_00004.m4s")
}

func (suite *S3IntegrationSuite) TestConcurrentUploads() {
    ctx := context.Background()

    uploader := storage.NewS3Uploader(storage.S3Config{
        Client:           suite.s3Client,
        Bucket:           suite.bucket,
        Prefix:           "concurrent-test/",
        ConcurrentUploads: 3,
    })

    // Upload 10 segments concurrently
    var wg sync.WaitGroup
    uploadErrors := make([]error, 10)

    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            segment := generateTestHLSSegment(4 * time.Second)
            segmentKey := fmt.Sprintf("segment_%05d.m4s", idx)
            uploadErrors[idx] = uploader.UploadSegment(ctx, segment, segmentKey)
        }(i)
    }

    wg.Wait()

    // Verify all uploads succeeded
    for i, err := range uploadErrors {
        suite.NoError(err, "Upload %d failed", i)
    }

    // Verify all objects exist
    list, err := suite.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
        Bucket: aws.String(suite.bucket),
        Prefix: aws.String("concurrent-test/"),
    })
    suite.NoError(err)
    suite.Equal(10, len(list.Contents))
}

func (suite *S3IntegrationSuite) TestUploadRetry() {
    ctx := context.Background()

    // Create uploader with retry configuration
    uploader := storage.NewS3Uploader(storage.S3Config{
        Client:        suite.s3Client,
        Bucket:        suite.bucket,
        Prefix:        "retry-test/",
        RetryAttempts: 3,
        RetryBackoff:  100 * time.Millisecond,
    })

    // Simulate network failure by using invalid endpoint
    invalidClient := s3.NewFromConfig(aws.Config{
        EndpointResolver: aws.EndpointResolverFunc(
            func(service, region string) (aws.Endpoint, error) {
                return aws.Endpoint{
                    URL: "http://invalid-endpoint:9999",
                }, nil
            },
        ),
    })

    failingUploader := storage.NewS3Uploader(storage.S3Config{
        Client:        invalidClient,
        Bucket:        suite.bucket,
        RetryAttempts: 3,
        RetryBackoff:  100 * time.Millisecond,
    })

    segment := generateTestHLSSegment(4 * time.Second)

    // This should fail after retries
    err := failingUploader.UploadSegment(ctx, segment, "test.m4s")
    suite.Error(err)

    // Verify retry metrics
    metrics := failingUploader.GetMetrics()
    suite.Equal(3, metrics.RetryAttempts)
}
```

### 5.4 End-to-End S3 Recording Test
```go
func (suite *S3IntegrationSuite) TestFullRecordingToS3() {
    ctx := context.Background()

    // Start LiveKit and MinIO (via docker-compose)
    // Already running from SetupSuite

    // Configure agent with S3 storage
    agentConfig := egress.Config{
        VideoPort: 5004,
        AudioPort: 5006,
        Storage: storage.Config{
            Type: "s3",
            S3: storage.S3Config{
                Endpoint:  suite.minioURL,
                Bucket:    suite.bucket,
                AccessKey: "minioadmin",
                SecretKey: "minioadmin",
                UsePathStyle: true,
            },
        },
    }

    // Start egress agent
    agent := egress.NewAgent(agentConfig)
    go agent.Run(ctx)
    defer agent.Stop()

    // Create test publisher
    publisher, err := integration.NewTestPublisher(
        "ws://localhost:7880",
        "devkey",
        "secret",
        "s3-test-room",
        "user-1",
    )
    suite.NoError(err)
    defer publisher.Close()

    // Publish video for 30 seconds
    err = publisher.PublishVideoFile("test/fixtures/videos/sample_1080p.mp4", "h264")
    suite.NoError(err)

    time.Sleep(30 * time.Second)

    // Stop recording
    agent.Stop()
    publisher.Close()

    // Verify S3 contents
    list, err := suite.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
        Bucket: aws.String(suite.bucket),
        Prefix: aws.String("s3-test-room/"),
    })
    suite.NoError(err)

    // Should have playlist and multiple segments
    var hasPlaylist bool
    var segmentCount int

    for _, obj := range list.Contents {
        key := *obj.Key
        if strings.HasSuffix(key, ".m3u8") {
            hasPlaylist = true
        } else if strings.HasSuffix(key, ".m4s") || strings.HasSuffix(key, ".ts") {
            segmentCount++
        }
    }

    suite.True(hasPlaylist, "Should have uploaded playlist")
    suite.Greater(segmentCount, 5, "Should have multiple segments")

    // Download and validate playlist
    playlistObj, err := suite.s3Client.GetObject(ctx, &s3.GetObjectInput{
        Bucket: aws.String(suite.bucket),
        Key:    aws.String("s3-test-room/playlist.m3u8"),
    })
    suite.NoError(err)

    playlistContent, _ := io.ReadAll(playlistObj.Body)
    suite.Contains(string(playlistContent), "#EXTM3U")
    suite.Contains(string(playlistContent), "#EXT-X-VERSION")
}
```

### 5.5 MinIO Helper Scripts
```bash
#!/bin/bash
# scripts/start-minio.sh

echo "Starting MinIO for local S3 testing..."

docker run -d \
  --name minio-test \
  -p 9000:9000 \
  -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  minio/minio server /data --console-address ":9001"

echo "Waiting for MinIO to be ready..."
sleep 5

# Create default bucket
docker run --rm \
  --link minio-test:minio \
  --entrypoint sh \
  minio/mc -c "
    mc alias set local http://minio:9000 minioadmin minioadmin &&
    mc mb local/test-recordings &&
    mc policy set public local/test-recordings
  "

echo "MinIO is ready at http://localhost:9000"
echo "Console available at http://localhost:9001"
echo "Credentials: minioadmin/minioadmin"
```

## 6. Performance and Load Tests

### 6.1 Benchmark Tests
```go
package benchmark_test

import (
    "testing"
    "context"
)

func BenchmarkSingleStream(b *testing.B) {
    config := egress.Config{
        VideoPort: 5004,
        AudioPort: 5006,
        OutputDir: b.TempDir(),
    }

    handler := egress.NewEgressHandler(&config)
    worker := agent.NewUniversalWorker(
        "ws://localhost:7880",
        "devkey",
        "secret",
        handler,
        agent.WorkerOptions{
            AgentName: "benchmark-egress",
            JobType:   livekit.JobType_JT_ROOM,
            MaxJobs:   1,
        },
    )

    ctx := context.Background()
    go worker.Start(ctx)

    publisher := &VideoPublisher{}

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        // Stream 10 seconds of video
        cmd, _ := publisher.PublishVideoFile("test/fixtures/videos/sample_720p.mp4", 5004, 5006)
        time.Sleep(10 * time.Second)
        cmd.Process.Kill()
    }

    b.ReportMetric(float64(b.N*10), "seconds_recorded")
}

func BenchmarkConcurrentStreams(b *testing.B) {
    streamCounts := []int{1, 5, 10, 20}

    for _, count := range streamCounts {
        b.Run(fmt.Sprintf("streams_%d", count), func(b *testing.B) {
            config := egress.Config{
                VideoPort: 5004,
                AudioPort: 5006,
                OutputDir: b.TempDir(),
            }

            handler := egress.NewEgressHandler(&config)
            worker := agent.NewUniversalWorker(
                "ws://localhost:7880",
                "devkey",
                "secret",
                handler,
                agent.WorkerOptions{
                    AgentName: "benchmark-concurrent",
                    JobType:   livekit.JobType_JT_ROOM,
                    MaxJobs:   count,
                },
            )

            ctx := context.Background()
            go worker.Start(ctx)

            b.ResetTimer()

            // Start concurrent streams
            var commands []*exec.Cmd
            for i := 0; i < count; i++ {
                cmd, _ := publisher.PublishVideoFile(
                    "test/fixtures/videos/sample_720p.mp4",
                    5004+i*2,
                    5006+i*2,
                )
                commands = append(commands, cmd)
            }

            // Record for 1 minute
            time.Sleep(1 * time.Minute)

            // Stop all
            for _, cmd := range commands {
                cmd.Process.Kill()
            }

            // Report metrics (would need custom metrics collection in handler)
            // metrics := handler.GetMetrics()
            // b.ReportMetric(metrics.CPUUsage, "cpu_percent")
            // b.ReportMetric(float64(metrics.MemoryUsage), "memory_mb")
            // b.ReportMetric(float64(metrics.PacketsProcessed), "packets")
        })
    }
}
```

## 6. Test Execution Strategy

### 6.1 Test Stages
```makefile
# Makefile
.PHONY: test test-unit test-integration test-e2e test-all

# Quick unit tests (< 1 minute)
test-unit:
	go test -v -short ./pkg/...

# Integration tests with mocks (< 5 minutes)
test-integration:
	go test -v ./test/integration/...

# E2E tests with real video (< 30 minutes)
test-e2e:
	@echo "Starting LiveKit server..."
	docker-compose up -d livekit
	sleep 5
	go test -v -timeout 30m ./test/e2e/...
	docker-compose down

# Performance benchmarks
test-bench:
	go test -bench=. -benchmem ./test/benchmark/...

# Full test suite
test-all: test-unit test-integration test-e2e test-bench

# Coverage report
test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
```

### 6.2 CI/CD Pipeline
```yaml
# .github/workflows/test.yml
name: Test

on:
  push:
    branches: [main]
  pull_request:

jobs:
  unit-tests:
    runs-on: ubuntu-latest
    steps:
    - uses: actions/checkout@v3
    - uses: actions/setup-go@v4
      with:
        go-version: '1.21'
    - name: Install GStreamer
      run: |
        sudo apt-get update
        sudo apt-get install -y gstreamer1.0-tools gstreamer1.0-plugins-good
    - name: Run unit tests
      run: make test-unit

  integration-tests:
    runs-on: ubuntu-latest
    steps:
    - uses: actions/checkout@v3
    - uses: actions/setup-go@v4
    - name: Install dependencies
      run: |
        sudo apt-get install -y gstreamer1.0-tools gstreamer1.0-plugins-good ffmpeg
    - name: Run integration tests
      run: make test-integration

  e2e-tests:
    runs-on: ubuntu-latest
    if: github.event_name == 'push'
    steps:
    - uses: actions/checkout@v3
    - uses: actions/setup-go@v4
    - name: Install dependencies
      run: |
        sudo apt-get install -y gstreamer1.0-tools gstreamer1.0-plugins-* ffmpeg
    - name: Download test videos
      run: |
        wget -P test/fixtures/videos/ https://example.com/test-videos.tar.gz
        tar -xzf test/fixtures/videos/test-videos.tar.gz -C test/fixtures/videos/
    - name: Run E2E tests
      run: make test-e2e
    - name: Upload recordings
      if: failure()
      uses: actions/upload-artifact@v3
      with:
        name: test-recordings
        path: /tmp/test-recordings/
```

## 7. Test Data Management

### 7.1 Generate Test Videos Script
```bash
#!/bin/bash
# scripts/generate-test-videos.sh

OUTPUT_DIR="test/fixtures/videos"
mkdir -p $OUTPUT_DIR

# 1080p 30fps H.264 + Opus
ffmpeg -f lavfi -i testsrc=duration=30:size=1920x1080:rate=30 \
       -f lavfi -i sine=frequency=1000:duration=30 \
       -c:v libx264 -preset ultrafast -profile:v baseline \
       -c:a libopus -b:a 128k \
       $OUTPUT_DIR/sample_1080p_h264_opus.mp4

# 720p 60fps H.264 + Opus
ffmpeg -f lavfi -i testsrc=duration=30:size=1280x720:rate=60 \
       -f lavfi -i sine=frequency=1000:duration=30 \
       -c:v libx264 -preset ultrafast \
       -c:a libopus \
       $OUTPUT_DIR/sample_720p_60fps.mp4

# Video with gaps (simulate packet loss)
ffmpeg -f lavfi -i testsrc=duration=30:size=1280x720:rate=30 \
       -f lavfi -i sine=frequency=1000:duration=30 \
       -vf "select='not(between(t,5,5.5)+between(t,10,10.2)+between(t,15,15.1))'" \
       -af "aselect='not(between(t,5,5.5)+between(t,10,10.2)+between(t,15,15.1))'" \
       -c:v libx264 -c:a libopus \
       $OUTPUT_DIR/sample_with_gaps.mp4

# 4K test (shorter duration)
ffmpeg -f lavfi -i testsrc=duration=10:size=3840x2160:rate=30 \
       -f lavfi -i sine=frequency=1000:duration=10 \
       -c:v libx264 -preset ultrafast \
       -c:a libopus \
       $OUTPUT_DIR/sample_4k.mp4

echo "Test videos generated in $OUTPUT_DIR"
```

### 7.2 RTP Packet Capture
```bash
#!/bin/bash
# scripts/capture-rtp-packets.sh

# Capture H.264 RTP packets
ffmpeg -re -i test/fixtures/videos/sample_720p.mp4 \
       -c:v copy -an \
       -f rtp rtp://127.0.0.1:15004 &

FFMPEG_PID=$!

tcpdump -i lo -w test/fixtures/rtp/h264_packets.pcap \
        port 15004 -c 1000

kill $FFMPEG_PID

# Capture Opus RTP packets
ffmpeg -re -i test/fixtures/videos/sample_720p.mp4 \
       -vn -c:a copy \
       -f rtp rtp://127.0.0.1:15006 &

FFMPEG_PID=$!

tcpdump -i lo -w test/fixtures/rtp/opus_packets.pcap \
        port 15006 -c 1000

kill $FFMPEG_PID
```

---

*Document Version: 1.0*
*Last Updated: 2024*
*Status: Comprehensive Test Strategy*