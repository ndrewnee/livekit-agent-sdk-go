# Media Publisher Agent Example

This example demonstrates a comprehensive media publisher agent that can generate and stream audio/video content to LiveKit rooms. It showcases publisher job handling, real-time media generation, and interactive features.

## Features

- **Multiple Publishing Modes**:
  - `audio_tone`: Generates and streams audio tones
  - `video_pattern`: Generates and streams video test patterns
  - `both`: Streams both audio and video simultaneously
  - `interactive`: Responds to data messages from participants

- **Audio Generation**:
  - Sine wave tone generation at configurable frequencies
  - Volume control
  - Real-time frequency adjustment in interactive mode

- **Video Generation**:
  - Multiple test patterns: color bars, moving circle, checkerboard, gradient
  - Frame counter overlay
  - Pattern switching in interactive mode

- **Interactive Commands**:
  - Volume control (up/down)
  - Tone frequency adjustment
  - Video pattern switching
  - Status queries

## Prerequisites

- Go 1.18 or later
- LiveKit server running (locally or cloud)
- LiveKit API credentials

## Running the Agent

### Quick Start

Use the provided demo script:

```bash
./run_demo.sh
```

### Manual Setup

1. Set environment variables:
```bash
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="your-api-key"
export LIVEKIT_API_SECRET="your-api-secret"

# Optional configuration
export AUDIO_SAMPLE_RATE=48000
export AUDIO_CHANNELS=1
export VIDEO_WIDTH=1280
export VIDEO_HEIGHT=720
export VIDEO_FRAME_RATE=30
```

2. Run the agent:
```bash
go run .
```

3. Create a room and dispatch a publisher job:
```bash
# Use the dispatch script
./dispatch_publisher_job.sh

# This will create a room with agent dispatch and trigger a publisher job
```

## Testing the Agent

Jobs must be dispatched to rooms created with agent configuration. The dispatch script handles this automatically.

You can manually dispatch jobs with different configurations by modifying the metadata in `dispatch_publisher_job.go`:

### Audio Tone Mode
```json
{
  "type": "JT_PUBLISHER",
  "metadata": {
    "mode": "audio_tone",
    "tone_frequency": 440.0,
    "volume": 0.8
  }
}
```

### Video Pattern Mode
```json
{
  "type": "JT_PUBLISHER",
  "metadata": {
    "mode": "video_pattern",
    "video_type": "moving_circle"
  }
}
```

### Both Audio and Video
```json
{
  "type": "JT_PUBLISHER",
  "metadata": {
    "mode": "both",
    "tone_frequency": 440.0,
    "video_type": "color_bars",
    "volume": 0.7
  }
}
```

### Interactive Mode
```json
{
  "type": "JT_PUBLISHER",
  "metadata": {
    "mode": "interactive",
    "welcome_message": "Hello! Send me commands to control the media.",
    "tone_frequency": 440.0
  }
}
```

## Interactive Commands

In interactive mode, participants can send data messages to control the publisher:

### Set Tone Frequency
```json
{
  "type": "set_tone_frequency",
  "frequency": 880.0
}
```

### Set Volume
```json
{
  "type": "set_volume",
  "volume": 0.5
}
```

### Change Video Pattern
```json
{
  "type": "change_pattern",
  "pattern": "checkerboard"
}
```

### Simple Commands
```json
{
  "type": "command",
  "command": "volume_up"
}
```

Available commands:
- `volume_up`: Increase volume by 0.1
- `volume_down`: Decrease volume by 0.1
- `next_pattern`: Cycle to next video pattern
- `hello`: Get a greeting response
- `status`: Get current publishing statistics

## Configuration

The agent supports the following environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `LIVEKIT_URL` | `ws://localhost:7880` | LiveKit server URL |
| `LIVEKIT_API_KEY` | (required) | LiveKit API key |
| `LIVEKIT_API_SECRET` | (required) | LiveKit API secret |
| `AUDIO_SAMPLE_RATE` | `48000` | Audio sample rate in Hz |
| `AUDIO_CHANNELS` | `1` | Number of audio channels |
| `AUDIO_BIT_DEPTH` | `16` | Audio bit depth |
| `VIDEO_WIDTH` | `1280` | Video width in pixels |
| `VIDEO_HEIGHT` | `720` | Video height in pixels |
| `VIDEO_FRAME_RATE` | `30` | Video frame rate |
| `VIDEO_BITRATE` | `2000000` | Video bitrate in bits/sec |

## Architecture

The agent is structured into several components:

- **main.go**: Entry point, configuration, and worker setup
- **handler.go**: Job handler with publishing mode implementations
- **publisher.go**: Session management and state tracking
- **audio_generator.go**: Audio tone generation service
- **video_generator.go**: Video pattern generation service

## Extending the Example

This example can be extended with:

- Real audio file playback
- Text-to-speech integration
- More complex video patterns or animations
- Screen capture publishing
- External media source integration
- Advanced audio processing (effects, mixing)
- Video encoding with actual VP8/H264 codecs

## Troubleshooting

1. **Agent not receiving jobs**: 
   - Ensure rooms are created with agent dispatch configuration
   - Check that agent name matches in dispatch configuration
   - Verify job type is set to `JT_PUBLISHER`

2. **No audio/video in room**: 
   - Check that the agent has successfully published tracks
   - Look for "Successfully published" messages in logs
   - Verify participant permissions allow publishing

3. **Connection issues**: 
   - Verify LiveKit URL and credentials are correct
   - Check firewall settings for WebSocket connections

4. **Poor quality**: 
   - Adjust sample rates, frame rates, and bitrates via environment variables
   - Consider network bandwidth limitations

5. **Interactive commands not working**: 
   - Ensure data messages are properly formatted JSON
   - Check that agent is in interactive mode