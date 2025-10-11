# Participant HLS Recorder Agent

An agent that monitors individual participants and automatically records their audio and video tracks to HLS-compatible MPEG-TS format using GStreamer. This example combines participant-specific job handling with real-time media recording.

## Overview

This agent performs participant-specific job handling (JT_PARTICIPANT) and automatically:
- Waits for a target participant to join the room
- Subscribes to their audio and video tracks
- Records tracks to MPEG-TS format using GStreamer
- Transcodes Opus audio to AAC for HLS compatibility
- Creates separate recordings per participant

## Features

- **Participant-specific recording** (JT_PARTICIPANT job type)
- **Automatic track detection** and subscription
- **GStreamer-based recording** with proper A/V sync
- **Opus to AAC transcoding** for HLS compatibility
- **RTP jitter buffer** for packet ordering and timing
- **Configurable audio/video** recording options
- **Per-participant output** directories
- **Graceful handling** of participant disconnect

## Prerequisites

- Go 1.22 or later
- LiveKit server running (locally or cloud)
- Valid LiveKit API credentials
- **GStreamer 1.0** with the following plugins:
  - gst-plugins-base (rtpjitterbuffer, audioconvert)
  - gst-plugins-good (rtph264depay, rtpopusdepay)
  - gst-plugins-bad (mpegtsmux, h264parse)
  - gst-plugins-ugly (aacparse)
  - gst-libav (avenc_aac for AAC encoding, opusdec for Opus decoding)

### Installing GStreamer

**macOS (using Homebrew):**
```bash
brew install gstreamer gst-plugins-base gst-plugins-good gst-plugins-bad gst-plugins-ugly gst-libav
```

**Ubuntu/Debian:**
```bash
sudo apt-get update
sudo apt-get install -y \
  libgstreamer1.0-dev \
  libgstreamer-plugins-base1.0-dev \
  libgstreamer-plugins-good1.0-dev \
  libgstreamer-plugins-bad1.0-dev \
  gstreamer1.0-plugins-ugly \
  gstreamer1.0-libav
```

## Configuration

The agent uses environment variables for configuration:

```bash
# Required
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="your-api-key"
export LIVEKIT_API_SECRET="your-api-secret"

# Optional (with defaults)
export OUTPUT_DIR="recordings"                # Base directory for recordings
export INACTIVITY_TIMEOUT="30s"              # Timeout waiting for participant
export ENABLE_AUDIO="true"                   # Record audio tracks
export ENABLE_VIDEO="true"                   # Record video tracks
```

## Running the Agent

### Quick Start with Test

```bash
# Run the test which simulates a participant publishing tracks
go test -v
```

### Manual Usage

1. Install dependencies:
   ```bash
   go mod download
   ```

2. Run the agent:
   ```bash
   export LIVEKIT_URL="ws://localhost:7880"
   export LIVEKIT_API_KEY="devkey"
   export LIVEKIT_API_SECRET="secret"
   go run .
   ```

3. Dispatch a participant job to start recording:
   ```bash
   # Using LiveKit CLI or API, dispatch a JT_PARTICIPANT job with metadata:
   {
     "participant_identity": "user123",
     "record_audio": true,
     "record_video": true,
     "end_on_disconnect": true
   }
   ```

## Job Metadata

When dispatching a participant job, include metadata specifying the target participant:

```json
{
  "participant_identity": "user123",
  "record_audio": true,
  "record_video": true,
  "end_on_disconnect": true
}
```

### Metadata Fields

- `participant_identity` (required): The identity of the participant to record
- `record_audio` (optional): Whether to record audio tracks (default: config.EnableAudio)
- `record_video` (optional): Whether to record video tracks (default: config.EnableVideo)
- `end_on_disconnect` (optional): Whether to end the job when participant disconnects

## Output Format

Recordings are saved in the following structure:

```
recordings/
├── room-name/
│   ├── participant1/
│   │   └── output.ts          # MPEG-TS file with H.264 video and AAC audio
│   ├── participant2/
│   │   └── output.ts
│   └── ...
```

### Post-Processing to HLS

The agent records to a single MPEG-TS file. To create HLS segments, use ffmpeg:

```bash
ffmpeg -i recordings/room-name/participant1/output.ts \
  -c copy \
  -f hls \
  -hls_time 2 \
  -hls_list_size 0 \
  -hls_segment_filename "recordings/room-name/participant1/segment_%05d.ts" \
  recordings/room-name/participant1/playlist.m3u8
```

## Architecture

### Components

1. **ParticipantHLSHandler**: Handles participant job lifecycle
   - Validates job type and metadata
   - Creates recorder for target participant
   - Monitors participant connection and tracks

2. **RecorderManager**: Manages multiple participant recorders
   - Creates/removes recorders
   - Tracks active recordings
   - Generates summary reports

3. **ParticipantRecorder**: Records individual participant tracks
   - GStreamer pipeline management
   - RTP packet handling
   - Track-specific recording

### Recording Flow

1. Agent receives JT_PARTICIPANT job with target participant identity
2. Creates GStreamer pipeline with:
   - Video: appsrc → rtpjitterbuffer → rtpvp8depay → vp8dec → x264enc → mux
   - Audio: appsrc → rtpjitterbuffer → rtpopusdepay → opusdec → avenc_aac → mux
   - Output: mpegtsmux → filesink
3. Waits for participant to connect (or finds if already connected)
4. Subscribes to participant's audio/video tracks
5. Reads RTP packets from tracks
6. Pushes RTP packets to GStreamer pipeline
7. GStreamer handles:
   - Packet reordering (jitter buffer)
   - RTP depayloading
   - VP8 decoding and H.264 encoding
   - Opus to AAC transcoding
   - Audio/video synchronization
   - MPEG-TS muxing
8. Ends recording on disconnect or job completion

## Testing

The included test (`main_test.go`) simulates a complete recording scenario:

```bash
go test -v
```

The test:
1. Starts the HLS recording agent
2. Creates a test room
3. Dispatches a participant job
4. Simulates a participant joining and publishing tracks using GStreamer
5. Records for the duration of the test video
6. Validates the recorded output

## Troubleshooting

### Agent not receiving jobs
- Ensure job type is set to `JT_PARTICIPANT`
- Verify participant identity in metadata
- Check LiveKit server connectivity
- Ensure room is created with agent dispatch configuration

### No audio/video in recording
- Verify tracks are published by participant
- Check GStreamer installation and plugins
- Ensure subscription succeeds
- Check logs for RTP packet flow

### GStreamer errors
- Verify all required plugins are installed:
  ```bash
  gst-inspect-1.0 rtpjitterbuffer
  gst-inspect-1.0 rtph264depay
  gst-inspect-1.0 rtpopusdepay
  gst-inspect-1.0 avenc_aac
  gst-inspect-1.0 mpegtsmux
  ```
- Check GStreamer error messages in logs

### Audio/Video desynchronization
- The pipeline uses GStreamer's jitter buffer for synchronization
- Ensure proper RTP timestamps in input packets
- Check that both audio and video tracks are being recorded

## Supported Codecs

- **Video Input**: VP8 (video/VP8) → transcoded to H.264 for MPEG-TS
- **Audio Input**: Opus (audio/opus) → transcoded to AAC

**Note**: The test uses VP8 for video due to a known issue with LiveKit SDK's H.264 handling when using `WriteSample()`. In production, you can modify the recorder pipeline to accept H.264 directly if your participants publish H.264 natively.

## Next Steps

- See [Simple Room Agent](../simple-room-agent) for basic agent setup
- Explore [Participant Monitoring Agent](../participant-monitoring-agent) for monitoring without recording
- Review [Save to HLS GStreamer](../save-to-hls-gstreamer) for the core recording implementation
- Check [Advanced Features](../../docs/advanced-features.md) for production deployment

## License

Apache License 2.0
