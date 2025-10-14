# Changelog

All notable changes to the Publisher HLS Agent will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial release of Publisher HLS Agent
- Real-time HLS generation from LiveKit publisher tracks
- GStreamer-based media processing pipeline
- H.264 video encoding with proper SPS/PPS injection
- AAC audio transcoding from Opus
- S3-compatible storage upload support (AWS S3, MinIO, etc.)
- Auto-activation mode for immediate recording start
- Manual activation API for programmatic control
- Delayed pipeline start mechanism for valid HLS segments
- Comprehensive E2E integration tests with MinIO
- HIGH video quality requests from publishers
- PLI (Picture Loss Indication) keyframe requests
- Detailed recording statistics and summaries

### Features

#### Core Recording
- **Delayed Pipeline Start**: GStreamer pipeline starts only when the first recording keyframe arrives, ensuring all HLS segments begin with valid H.264 data containing SPS/PPS headers
- **Dual Output**: Generates both a complete MPEG-TS file (`output.ts`) and HLS segments with playlist
- **Parameter Set Injection**: Automatically injects SPS/PPS from codec `fmtp` if not present in RTP stream
- **Timestamp Management**: Proper PTS calculation for synchronized audio/video playback

#### HLS Generation
- **Configurable Segment Duration**: Default 2 seconds, adjustable via `HLS_SEGMENT_DURATION`
- **Unlimited Playlist**: Default keeps all segments, configurable via `HLS_MAX_SEGMENTS`
- **Standard Compliance**: Generates HLS playlists compatible with all major players (VLC, hls.js, Safari, etc.)

#### S3 Upload
- **S3-Compatible Storage**: Works with AWS S3, MinIO, DigitalOcean Spaces, etc.
- **Automatic Upload**: Uploads all recording files (playlist, segments, MPEG-TS) on completion
- **Configurable ACLs**: Support for canned ACLs like `public-read`
- **Path-Style URLs**: Support for MinIO and other S3-compatible services

#### Quality & Reliability
- **High Quality Video**: Requests `VideoQuality_HIGH` from publishers
- **Keyframe Management**: Proactive PLI requests to ensure keyframe availability
- **Handshake Protocol**: Waits for initial keyframe before allowing recording activation
- **Error Handling**: Graceful handling of track disconnections and pipeline errors

### Technical Details

#### GStreamer Pipeline
```
Video: appsrc → rtpjitterbuffer → rtph264depay → h264parse → capsfilter → queue
Audio: appsrc → rtpjitterbuffer → rtpopusdepay → opusdec → audioconvert → avenc_aac → aacparse → queue
Mux: mpegtsmux → tee → [filesink (output.ts), hlssink (segments)]
```

#### H.264 Configuration
- Stream format: `byte-stream` (Annex-B start codes for MPEG-TS)
- Alignment: Access Unit (AU)
- Config interval: `-1` (SPS/PPS before every keyframe via h264parse)

#### Audio Configuration
- Input: Opus @ 48kHz
- Output: AAC @ 128kbps
- Ensures broad player compatibility

### Fixed
- **HLS Playback Issues**: Fixed invalid first segments by implementing delayed pipeline start
- **Caps Negotiation**: Changed from `stream-format=avc` to `stream-format=byte-stream` for MPEG-TS compatibility
- **mpegtsmux Linking**: Properly use request pads instead of static pads for mpegtsmux element
- **SPS/PPS Headers**: Ensure all HLS segments start with proper H.264 parameter sets

### Testing
- Comprehensive E2E test suite with synthetic media publisher
- Automated S3 upload verification with MinIO
- HLS playback validation with ffprobe
- Integration with LiveKit agent framework

### Documentation
- Comprehensive README with architecture diagrams
- API documentation (godoc) for all exported types
- Example configuration file with detailed comments
- Deployment guides for Docker and systemd
- Troubleshooting section for common issues

## [0.1.0] - 2025-10-14

### Initial Development
- Project structure and build configuration
- GStreamer pipeline implementation
- LiveKit agent integration
- Basic recording functionality

---

## Release Notes

### How to Use

1. **Build**:
   ```bash
   go build -o publisher-hls-agent
   ```

2. **Configure**:
   ```bash
   cp config.env.example config.env
   # Edit config.env with your settings
   source config.env
   ```

3. **Run**:
   ```bash
   ./publisher-hls-agent
   ```

4. **Dispatch Job**:
   ```bash
   livekit-cli agent dispatch \
     --room "your-room" \
     --participant-identity "publisher-to-record" \
     --agent-name "publisher-hls-recorder" \
     --job-type JT_PUBLISHER
   ```

### Known Limitations

- Requires H.264 video codec (does not support VP8/VP9)
- Requires Opus audio codec
- Local storage required before S3 upload (no direct streaming to S3)
- GStreamer 1.26+ dependency

### Future Enhancements

- [ ] Direct S3 streaming without local storage
- [ ] VP8/VP9 codec support
- [ ] Multiple quality tiers (adaptive bitrate)
- [ ] Thumbnail generation
- [ ] Recording metadata in manifest
- [ ] WebM/DASH output formats
- [ ] GPU-accelerated encoding

[Unreleased]: https://github.com/am-sokolov/livekit-agent-sdk-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/am-sokolov/livekit-agent-sdk-go/releases/tag/v0.1.0
