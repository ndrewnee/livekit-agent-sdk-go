# GStreamer Integration for HLS Egress

## Executive Summary

GStreamer is a powerful alternative to FFmpeg that offers better programmatic control, superior pipeline flexibility, and excellent stream-copy performance for the zero-transcode HLS egress use case.

## Why GStreamer is Excellent for This Project

### 1. **Superior Pipeline Architecture**
- Designed for streaming from the ground up
- Plugin-based architecture perfect for stream-copy
- Better real-time performance
- Native RTP handling

### 2. **Zero-Transcode Optimized**
- Passthrough elements designed for stream-copy
- Minimal CPU usage
- Direct RTP to HLS without transcoding
- Better buffer management

### 3. **Go Integration Options**
- Better Go bindings than FFmpeg
- Pipeline string API (no CGO required)
- WebRTC integration via Pion

## GStreamer vs FFmpeg Comparison

| Feature | FFmpeg | GStreamer | Winner |
|---------|--------|-----------|--------|
| Stream-copy performance | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | GStreamer |
| RTP handling | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | GStreamer |
| Go integration | ⭐⭐ | ⭐⭐⭐⭐ | GStreamer |
| Pipeline flexibility | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | GStreamer |
| HLS support | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | FFmpeg |
| Documentation | ⭐⭐⭐⭐ | ⭐⭐⭐ | FFmpeg |
| Learning curve | ⭐⭐⭐⭐ | ⭐⭐ | FFmpeg |
| Container size | 500MB | 300MB | GStreamer |

## Implementation Approaches

### Approach 1: GStreamer Pipeline (Subprocess)

```go
package gstreamer

import (
    "fmt"
    "os/exec"
)

type GStreamerPipeline struct {
    cmd *exec.Cmd
}

func (g *GStreamerPipeline) StartHLSStreamCopy() error {
    // Beautiful pipeline for zero-transcode HLS
    pipeline := `
        rtpbin name=rtpbin \
        ! udpsrc port=5004 caps="application/x-rtp,media=video,encoding-name=H264" \
        ! rtpbin.recv_rtp_sink_0 \
        rtpbin. \
        ! rtph264depay \
        ! h264parse \
        ! mpegtsmux name=mux \
        ! hlssink2 \
            location=segment%05d.ts \
            playlist-location=playlist.m3u8 \
            max-files=10 \
            target-duration=4 \
        udpsrc port=5006 caps="application/x-rtp,media=audio,encoding-name=OPUS" \
        ! rtpbin.recv_rtp_sink_1 \
        rtpbin. \
        ! rtpopusdepay \
        ! opusparse \
        ! mux.
    `

    g.cmd = exec.Command("gst-launch-1.0", "-e", pipeline)
    return g.cmd.Start()
}
```

### Approach 2: Go-GStreamer Bindings

```go
import "github.com/go-gst/go-gstreamer/gst"

func CreateNativePipeline() (*gst.Pipeline, error) {
    gst.Init(nil)

    pipeline, err := gst.NewPipeline("egress")
    if err != nil {
        return nil, err
    }

    // Create elements
    rtpbin := gst.ElementFactoryMake("rtpbin", "rtpbin")
    videoSrc := gst.ElementFactoryMake("udpsrc", "video-src")
    h264depay := gst.ElementFactoryMake("rtph264depay", "h264depay")
    h264parse := gst.ElementFactoryMake("h264parse", "h264parse")

    audioSrc := gst.ElementFactoryMake("udpsrc", "audio-src")
    opusdepay := gst.ElementFactoryMake("rtpopusdepay", "opusdepay")
    opusparse := gst.ElementFactoryMake("opusparse", "opusparse")

    mux := gst.ElementFactoryMake("mpegtsmux", "mux")
    hlssink := gst.ElementFactoryMake("hlssink2", "hlssink")

    // Configure elements
    videoSrc.SetProperty("port", 5004)
    audioSrc.SetProperty("port", 5006)
    hlssink.SetProperty("location", "segment%05d.ts")
    hlssink.SetProperty("playlist-location", "playlist.m3u8")
    hlssink.SetProperty("target-duration", uint(4))

    // Add to pipeline
    pipeline.AddMany(rtpbin, videoSrc, h264depay, h264parse,
                     audioSrc, opusdepay, opusparse, mux, hlssink)

    // Link elements
    videoSrc.Link(rtpbin)
    rtpbin.Link(h264depay)
    h264depay.Link(h264parse)
    h264parse.Link(mux)

    audioSrc.Link(rtpbin)
    rtpbin.Link(opusdepay)
    opusdepay.Link(opusparse)
    opusparse.Link(mux)

    mux.Link(hlssink)

    return pipeline, nil
}
```

### Approach 3: WebRTC to GStreamer Bridge

```go
package bridge

import (
    "github.com/pion/webrtc/v3"
    "github.com/go-gst/go-gstreamer/gst"
    "github.com/go-gst/go-gstreamer/gst/app"
)

type WebRTCGStreamerBridge struct {
    pipeline *gst.Pipeline
    videoSrc *app.Source
    audioSrc *app.Source
}

func (b *WebRTCGStreamerBridge) Initialize() error {
    gst.Init(nil)

    pipelineStr := `
        appsrc name=videosrc ! h264parse ! mpegtsmux name=mux ! hlssink2 location=segment%05d.ts
        appsrc name=audiosrc ! opusparse ! mux.
    `

    pipeline, err := gst.NewPipelineFromString(pipelineStr)
    if err != nil {
        return err
    }

    b.pipeline = pipeline
    b.videoSrc = pipeline.GetByName("videosrc").(*app.Source)
    b.audioSrc = pipeline.GetByName("audiosrc").(*app.Source)

    return nil
}

func (b *WebRTCGStreamerBridge) OnTrack(track *webrtc.TrackRemote) {
    go func() {
        for {
            // Read RTP from WebRTC
            packet, _, err := track.ReadRTP()
            if err != nil {
                return
            }

            // Push to GStreamer
            buffer := gst.NewBufferFromBytes(packet.Payload)

            if track.Kind() == webrtc.RTPCodecTypeVideo {
                b.videoSrc.PushBuffer(buffer)
            } else {
                b.audioSrc.PushBuffer(buffer)
            }
        }
    }()
}
```

## GStreamer Pipeline for Zero-Transcode HLS

### Basic Stream-Copy Pipeline
```bash
# H.264 + Opus to HLS with zero transcoding
gst-launch-1.0 \
  udpsrc port=5004 ! application/x-rtp,media=video,encoding-name=H264 \
  ! rtph264depay ! h264parse ! mpegtsmux name=mux \
  ! hlssink2 location=segment%05d.ts playlist-location=playlist.m3u8 \
  udpsrc port=5006 ! application/x-rtp,media=audio,encoding-name=OPUS \
  ! rtpopusdepay ! opusparse ! mux.
```

### Advanced Pipeline with Gap Handling
```bash
# With jitter buffer and gap filling
gst-launch-1.0 \
  rtpbin name=rtpbin latency=200 \
  udpsrc port=5004 ! application/x-rtp,media=video \
  ! rtpbin.recv_rtp_sink_0 \
  rtpbin. ! rtph264depay ! h264parse \
  ! videorate ! video/x-h264,framerate=30/1 \
  ! mpegtsmux name=mux \
  ! hlssink2 location=segment%05d.ts \
  udpsrc port=5006 ! application/x-rtp,media=audio \
  ! rtpbin.recv_rtp_sink_1 \
  rtpbin. ! rtpopusdepay ! opusparse \
  ! audiorate ! audio/x-opus,rate=48000 \
  ! mux.
```

### Screenshot Extraction Pipeline
```bash
# Tee for screenshots without affecting main stream
gst-launch-1.0 \
  udpsrc port=5004 ! application/x-rtp \
  ! rtph264depay ! h264parse ! tee name=t \
  t. ! queue ! mpegtsmux name=mux ! hlssink2 location=segment%05d.ts \
  t. ! queue ! avdec_h264 ! videorate ! video/x-raw,framerate=1/5 \
  ! jpegenc ! multifilesink location=screenshot_%05d.jpg \
  udpsrc port=5006 ! application/x-rtp \
  ! rtpopusdepay ! opusparse ! mux.
```

## Gap Filling in GStreamer

### Native Gap Filling Elements
```go
func CreateGapFillingPipeline() string {
    return `
        # Video with gap filling
        udpsrc port=5004 ! rtpjitterbuffer latency=200 do-lost=true \
        ! rtph264depay ! h264parse \
        ! videorate drop-only=false duplicate-on-gap=true \
        ! video/x-h264,framerate=30/1 \
        ! mux.

        # Audio with gap filling
        udpsrc port=5006 ! rtpjitterbuffer latency=200 do-lost=true \
        ! rtpopusdepay ! opusparse \
        ! audiorate tolerance=40000000 add=true \
        ! mux.
    `
}
```

### Custom Gap Filler Element
```go
// Create custom GStreamer element for sophisticated gap filling
type GapFillerElement struct {
    *gst.Element
    lastVideoFrame []byte
    lastAudioFrame []byte
}

func (g *GapFillerElement) ChainFunction(pad *gst.Pad, buffer *gst.Buffer) gst.FlowReturn {
    if buffer.IsGap() {
        // Duplicate last frame
        fillerBuffer := gst.NewBufferFromBytes(g.lastVideoFrame)
        fillerBuffer.SetPTS(buffer.PTS())
        fillerBuffer.SetDTS(buffer.DTS())
        return g.srcPad.Push(fillerBuffer)
    }

    // Store for potential duplication
    g.lastVideoFrame = buffer.Bytes()
    return g.srcPad.Push(buffer)
}
```

## Performance Optimization

### 1. Zero-Copy Pipeline
```go
func OptimizedPipeline() string {
    return `
        # Use queue2 for better buffering
        udpsrc buffer-size=2097152 ! queue2 max-size-time=500000000 \
        ! rtph264depay ! h264parse config-interval=-1 \
        ! mpegtsmux alignment=7 ! queue2 \
        ! hlssink2 target-duration=4 max-files=0
    `
}
```

### 2. Hardware Acceleration (Optional for Screenshots)
```go
func HardwareAcceleratedScreenshots() string {
    return `
        # Use VAAPI for screenshot decode only
        ... ! h264parse ! tee name=t \
        t. ! queue ! mux.  # Stream copy path
        t. ! queue ! vaapih264dec ! vaapipostproc \
        ! video/x-raw,framerate=1/5 ! jpegenc ! multifilesink
    `
}
```

## Deployment

### Docker Image
```dockerfile
FROM ubuntu:22.04

# Install GStreamer with required plugins
RUN apt-get update && apt-get install -y \
    gstreamer1.0-tools \
    gstreamer1.0-plugins-base \
    gstreamer1.0-plugins-good \
    gstreamer1.0-plugins-bad \
    gstreamer1.0-plugins-ugly \
    gstreamer1.0-libav \
    libgstreamer1.0-dev \
    libgstreamer-plugins-base1.0-dev

# Copy agent binary
COPY egress-agent /usr/local/bin/

ENTRYPOINT ["egress-agent"]
```

### Container Size Comparison
| Base Image | FFmpeg | GStreamer | Pure Go |
|------------|--------|-----------|---------|
| Ubuntu | 500MB | 300MB | N/A |
| Alpine | 450MB | 250MB | 50MB |
| Distroless | N/A | N/A | 15MB |

## Advantages of GStreamer

### 1. **Better Streaming Architecture**
- Designed specifically for streaming pipelines
- Superior buffer management
- Native RTP handling
- Better timing and synchronization

### 2. **Flexibility**
- Dynamic pipeline modification
- Plugin architecture
- Easy to add/remove features
- Better debugging tools (GST_DEBUG)

### 3. **Performance**
- Efficient zero-copy pipelines
- Better thread management
- Optimized for real-time
- Lower latency than FFmpeg

### 4. **Gap Handling**
- Native elements for gap filling
- `videorate` and `audiorate` elements
- RTP jitter buffer with loss handling
- No custom code needed

## Challenges and Solutions

### Challenge 1: Learning Curve
**Solution**: Start with simple pipelines, use gst-launch for prototyping

### Challenge 2: Plugin Dependencies
**Solution**: Use static linking or controlled container environment

### Challenge 3: Debugging
**Solution**: GST_DEBUG environment variable, pipeline graphs

## Migration Path from FFmpeg

### Phase 1: Prototype (Week 1)
```bash
# Test with gst-launch
gst-launch-1.0 filesrc location=input.mp4 \
  ! qtdemux ! h264parse ! mpegtsmux \
  ! hlssink2 location=segment%05d.ts
```

### Phase 2: Integration (Week 2)
```go
// Subprocess implementation
cmd := exec.Command("gst-launch-1.0", pipelineString)
```

### Phase 3: Optimization (Week 3)
```go
// Use Go bindings for better control
pipeline := gst.NewPipeline("egress")
// Add elements programmatically
```

## Recommendation

**GStreamer is EXCELLENT for your use case** because:

1. **Zero-transcode optimization** - Better than FFmpeg for stream-copy
2. **Native RTP handling** - Designed for your exact use case
3. **Built-in gap handling** - Less custom code needed
4. **Better Go integration** - Cleaner than FFmpeg bindings
5. **Lower latency** - Better for real-time streaming

### Suggested Implementation:
1. **Start with GStreamer subprocess** (gst-launch-1.0)
2. **Use go-gst bindings** for better control
3. **Keep FFmpeg as fallback** for edge cases

### Best Use Cases for GStreamer:
- ✅ RTP to HLS streaming (your case)
- ✅ Real-time pipeline processing
- ✅ Complex streaming workflows
- ✅ Dynamic pipeline modification
- ✅ Low-latency requirements

### When to Stick with FFmpeg:
- Simple file transcoding
- Broad codec support needed
- Team already knows FFmpeg
- Need specific FFmpeg filters

## Example: Complete GStreamer Implementation

```go
package egress

import (
    "fmt"
    "os/exec"
)

type GStreamerEgress struct {
    pipeline *exec.Cmd
    videoPort int
    audioPort int
}

func (g *GStreamerEgress) Start(outputDir string) error {
    pipeline := fmt.Sprintf(`
        rtpbin name=rtpbin latency=200 do-lost=true \
        udpsrc port=%d caps="application/x-rtp,media=video,encoding-name=H264" \
        ! rtpbin.recv_rtp_sink_0 \
        rtpbin. ! rtph264depay ! h264parse ! queue \
        ! mpegtsmux name=mux alignment=7 \
        ! hlssink2 \
            location=%s/segment%%05d.ts \
            playlist-location=%s/playlist.m3u8 \
            target-duration=4 \
            max-files=0 \
        udpsrc port=%d caps="application/x-rtp,media=audio,encoding-name=OPUS" \
        ! rtpbin.recv_rtp_sink_1 \
        rtpbin. ! rtpopusdepay ! opusparse ! queue ! mux.
    `, g.videoPort, outputDir, outputDir, g.audioPort)

    g.pipeline = exec.Command("gst-launch-1.0", "-e", pipeline)
    return g.pipeline.Start()
}

func (g *GStreamerEgress) Stop() error {
    if g.pipeline != nil {
        return g.pipeline.Process.Kill()
    }
    return nil
}
```

---

*Document Version: 1.0*
*Last Updated: 2024*
*Status: Technical Recommendation*