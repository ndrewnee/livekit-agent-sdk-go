# Save to HLS Example

A simple application that shows how to receive H.264 video + Opus audio using Pion WebRTC and stream as HLS (HTTP Live Streaming). **Based on save-to-webm.go but outputs HLS with full audio+video support.**

## Overview

This example:
- Uses **pure Pion WebRTC** (no LiveKit required)
- Receives **H.264 video + Opus audio** via WebRTC
- Streams HLS using **fMP4 format** (supports Opus audio)
- **Built-in HTTP server** for VLC/browser playback (port 8080)
- Pure Go implementation (no ffmpeg required)
- Uses **manual signaling** (paste offer/answer in console)

## Features

✅ H.264 video support (1280x720, Main Profile)
✅ **Opus audio support** (48kHz, 2 channels via fMP4)
✅ HTTP streaming server (port 8080) - **required for playback**
✅ Pure Go (no external dependencies)
✅ Browser playback (HLS.js)
✅ VLC compatible via HTTP streaming

## Prerequisites

- **Go 1.22+**
- A WebRTC source (browser or test publisher)
- **VLC Media Player** (recommended for playback)

**NO LiveKit server or ffmpeg needed!**

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

### Step 3: Record and Stream

Once connected, the application will:
- Receive RTP packets (both video and audio)
- Cache SPS/PPS parameter sets for H.264
- Stream to HLS segments every 2 seconds
- Start **HTTP server on port 8080**

The HTTP server is **required** because fMP4 segments need dynamic init file serving.

### Step 4: Play the HLS stream

**Recommended: Use HTTP streaming** (fMP4 requires this for proper playback)

```bash
# Option 1: VLC via HTTP
vlc http://localhost:8080/index.m3u8

# Option 2: Browser
# Open: http://localhost:8080
# The page includes HLS.js player for in-browser playback

# Option 3: ffplay
ffplay http://localhost:8080/index.m3u8
```

**Note**: Direct file playback of saved segments is not supported. fMP4 HLS is designed for HTTP streaming.

## How It Works

```
WebRTC Offer/Answer (manual signaling)
          ↓
   Pion WebRTC Connection
          ↓
   ┌─────────────────┴─────────────────┐
   │                                   │
RTP Packets (H.264)           RTP Packets (Opus)
   │                                   │
SampleBuilder                  SampleBuilder
   │                                   │
NAL Unit Parser                Audio frames
(extract SPS/PPS)                     │
   │                                   │
   └─────────────────┬─────────────────┘
                     ↓
             gohlslib Muxer (fMP4)
                     ↓
        ┌────────────┴────────────┐
        │                         │
   video_init.mp4           audio_init.mp4
   video segments           audio segments
        │                         │
        └────────────┬────────────┘
                     ↓
          HTTP Server (port 8080)
                     ↓
          M3U8 Playlist + Segments
                     ↓
              VLC / Browser
```

### Key Implementation Details

1. **SPS/PPS Caching**: H.264 parameter sets are cached from the first keyframe
2. **Audio/Video Synchronization**: Audio writes wait for first video frame (fMP4 requirement)
3. **Muxer Initialization**: HLS muxer starts with both video and audio tracks
4. **fMP4 Format**: Uses fragmented MP4 for Opus audio support (MPEG-TS doesn't support Opus)
5. **HTTP Streaming**: Required for proper playback - init files served dynamically
6. **Init Files**: Separate video_init.mp4 and audio_init.mp4 contain codec parameters

### Comparison with save-to-webm.go

| Feature | save-to-webm.go | save-to-hls (this) |
|---------|-----------------|---------------------|
| Transport | Pion WebRTC | Pion WebRTC |
| Signaling | stdin/stdout | stdin/stdout |
| Video codec | VP8 or H.264 | H.264 |
| Audio codec | Opus | Opus |
| Output format | WebM | HLS (fMP4) |
| Output structure | Single file | Multiple segments + playlist + init files |
| Streaming | No | Yes (HTTP server on port 8080) |
| Playlist | N/A | Auto-generated M3U8 |
| Playback | Direct file | HTTP streaming required |

## Output Structure

```
./hls-output/                      # Working directory (served by HTTP)
├── index.m3u8                     # Auto-generated M3U8 playlist
├── video_init.mp4                 # Video initialization segment (SPS/PPS)
├── audio_init.mp4                 # Audio initialization segment (Opus config)
├── {hash}_video1_seg0.mp4         # Video segment 0 (2 seconds)
├── {hash}_video1_seg1.mp4         # Video segment 1 (2 seconds)
├── {hash}_audio2_seg0.mp4         # Audio segment 0 (2 seconds)
├── {hash}_audio2_seg1.mp4         # Audio segment 1 (2 seconds)
└── ...
```

### Example M3U8 Playlist (auto-generated)

```m3u8
#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:3
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MAP:URI="audio_init.mp4"
#EXTINF:2.000,
978774f1c69f_audio2_seg0.mp4
#EXTINF:2.000,
978774f1c69f_audio2_seg1.mp4
#EXT-X-MAP:URI="video_init.mp4"
#EXTINF:2.000,
978774f1c69f_video1_seg0.mp4
#EXTINF:2.000,
978774f1c69f_video1_seg1.mp4
#EXT-X-ENDLIST
```

**Note**: The playlist uses HLS v6 with `#EXT-X-MAP` directives for fMP4 init files.

## Testing

The example includes automated E2E tests that extract real H.264 video from test.mp4 using pure Go.

### Run automated test

```bash
cd examples/save-to-hls
go test -tags=e2e -v
```

The test will:
1. Create a WebRTC publisher and receiver
2. Extract H.264 video from test.mp4 using go-mp4 (pure Go, no ffmpeg)
3. Loop video frames to reach 700 frames (~23s)
4. Publish synthetic Opus audio (1150 frames = 23s)
5. Stream to HLS segments via HTTP
6. Validate output (segment count and init files)

**Expected output**:
- ~20 segments total (10 audio + 10 video)
- video_init.mp4 and audio_init.mp4
- **4.4MB total** (real video data with compression variations)
- Video segments: 446-485KB (varying sizes showing real H.264 compression)
- Pure Go implementation (no ffmpeg)

See `main_test.go` for the complete E2E test implementation.

## Configuration

Edit `main.go` to customize:

```go
outputDir := "./hls-output"
hlsFile, err := newHLSSaver(outputDir)
```

**Parameters**:
- `outputDir`: Output directory for HLS segments and playlist

**Muxer configuration** (in `initMuxer()`):
```go
s.muxer = &gohlslib.Muxer{
    Variant:            gohlslib.MuxerVariantFMP4,  // Required for Opus audio
    SegmentCount:       100,                        // Max segments to keep in memory
    SegmentMinDuration: 2 * time.Second,
    Directory:          s.outputDir,
    Tracks:             []*gohlslib.Track{s.videoTrack, s.audioTrack},
}
```

## Known Limitations

- **HTTP streaming required**: fMP4 segments cannot be played as standalone files - HTTP server is required
- **H.264 only**: Only H.264 video codec is supported (not VP8/VP9)
- **Opus only**: Only Opus audio codec is supported (not AAC)
- **Manual signaling**: Uses stdin/stdout for WebRTC signaling (not production-ready)
- **Segment duration variation**: gohlslib may warn about segment duration changes, which is expected for variable bitrate content

## Troubleshooting

### No segments generated

**Check**:
1. Was WebRTC connection established? (Look for "Connection State has changed connected")
2. Was video track received? (Look for "Track has started, of type ... video/H264")
3. Was audio track received? (Look for "Track has started, of type ... audio/opus")
4. Were SPS/PPS cached? (Look for "Cached SPS" and "Cached PPS" messages)
5. Was muxer started? (Look for "HLS muxer started")
6. Is HTTP server running? (Look for "HTTP server started on :8080")
7. Check output directory: `ls -lh hls-output/`

**Debug steps**:
```bash
# Check if segments are being created
ls -lh hls-output/

# Should see:
# - video_init.mp4 and audio_init.mp4
# - *_video1_seg*.mp4 files
# - *_audio2_seg*.mp4 files
# - index.m3u8 playlist
```

### HTTP server issues

**Possible causes**:
1. Port 8080 already in use
2. Firewall blocking port 8080

**Fix**:
```bash
# Check if port is in use
lsof -i :8080

# Or change port in main.go:
http.ListenAndServe(":9090", nil)
```

### Playback issues

**Most common issue**: Trying to play saved segments directly (not supported with fMP4)

**Solution**: Always use HTTP streaming:
```bash
# Correct way (via HTTP)
vlc http://localhost:8080/index.m3u8

# Wrong way (direct file access - won't work)
vlc hls-output/index.m3u8  # ❌ Will fail
```

**Why?** fMP4 segments reference init files dynamically and require HTTP serving.

**Test HTTP stream**:
```bash
# Test with curl
curl http://localhost:8080/index.m3u8

# Should return the playlist with audio and video segments
```

### WebRTC connection fails

**Common issues**:
1. NAT/Firewall blocking UDP
2. Incorrect offer/answer format (must be base64-encoded JSON)
3. STUN server unreachable
4. H.264 codec not supported by sender

**Debug WebRTC**:
```bash
# Check ICE connection state in logs
# Look for: "Connection State has changed checking"
# Then: "Connection State has changed connected"

# If connection never establishes, try localhost test
```

## See Also

- [save-to-webm Pion example](https://github.com/pion/example-webrtc-applications/tree/master/save-to-webm) - The reference example this is based on
- [gohlslib](https://github.com/bluenviron/gohlslib) - HLS muxer library used for segment generation
- [Test file](main_test.go) - Automated E2E test with WebRTC publisher
- [Test runner script](run-test.sh) - Automated test script with validation
- [Pion WebRTC documentation](https://github.com/pion/webrtc)
