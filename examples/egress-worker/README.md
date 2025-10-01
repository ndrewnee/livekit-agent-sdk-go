# LiveKit Egress Worker

A LiveKit agent worker that automatically records room sessions with HLS output. The worker monitors rooms and creates individual recording jobs for each participant.

## Features

- **Automatic Participant Recording**: Creates a recording job for each participant that joins
- **HLS Output**: Records to HLS format with configurable segment duration
- **Multiple Storage Backends**: Supports local, S3, GCS, and hybrid storage
- **Quality Control**: Configurable video/audio quality settings
- **Adaptive Bitrate**: Adjusts quality based on network conditions
- **Screenshot Capture**: Periodic screenshots during recording
- **Resilient**: Automatic reconnection and error recovery

## Architecture

The Egress Worker operates as a LiveKit agent that:

1. **Accepts ROOM jobs**: Monitors rooms for participant activity
2. **Creates PARTICIPANT jobs**: Automatically creates a recording job for each participant
3. **Records to HLS**: Uses GStreamer pipeline for efficient recording
4. **Manages Storage**: Handles segment upload and playlist management

## Quick Start

### Prerequisites

- Go 1.21+
- LiveKit server running
- GStreamer installed (for video processing)

### Installation

```bash
# Install dependencies
go mod download

# Build the worker
go build -o egress-worker main.go
```

### Running

```bash
# Using environment variables
export LIVEKIT_URL=ws://localhost:7880
export LIVEKIT_API_KEY=devkey
export LIVEKIT_API_SECRET=secret

./egress-worker

# Or using command line flags
./egress-worker \
  --url ws://localhost:7880 \
  --api-key devkey \
  --api-secret secret \
  --config config.yaml
```

## Configuration

The worker can be configured via `config.yaml` or programmatically. See [config.yaml](config.yaml) for all available options.

### Key Configuration Options

#### Recording Settings
```yaml
recording:
  record_video: true        # Record video tracks
  record_audio: true        # Record audio tracks
  record_screen_share: true # Record screen share tracks
  auto_start: true          # Start recording automatically
```

#### Storage Settings
```yaml
storage:
  type: local              # local, s3, gcs, or hybrid
  local:
    path: ./recordings     # Where to store recordings
```

#### Quality Settings
```yaml
video_quality: 2           # 0=LOW, 1=MEDIUM, 2=HIGH
pipeline:
  segment_duration: 4      # HLS segment duration in seconds
  playlist_type: event     # event or vod
```

## How It Works

### Job Flow

1. **Room Monitoring**:
   - Worker accepts ROOM jobs from LiveKit
   - Monitors the room for participant events

2. **Participant Recording**:
   - When a participant joins, creates a recording job
   - Subscribes to participant's audio/video tracks
   - Records to HLS format

3. **Storage Management**:
   - Writes segments to configured storage
   - Updates HLS playlists
   - Handles upload retries and failures

### Recording Process

```
Participant Joins → Create Job → Subscribe Tracks → Record to HLS → Upload Segments
```

## API Usage

### Programmatic Usage

```go
package main

import (
    "context"
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress"
)

func main() {
    // Create configuration
    config := egress.DefaultConfig()
    config.MaxConcurrentSessions = 10
    config.StorageConfig.Type = "s3"

    // Create worker
    worker := egress.NewEgressWorker(config)

    // Start worker
    ctx := context.Background()
    err := worker.Start(ctx, livekitURL, apiKey, apiSecret)
    if err != nil {
        panic(err)
    }

    // Worker will now automatically handle recording jobs
    // ...

    // Stop worker
    worker.Stop()
}
```

### Custom Handler

You can extend the egress functionality by implementing a custom handler:

```go
type MyEgressHandler struct {
    egress.BaseEgressHandler
}

func (h *MyEgressHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
    // Custom logic when participant joins
    log.Printf("Starting recording for %s", participant.Identity())
}

func (h *MyEgressHandler) OnJobTerminated(ctx context.Context, jobID string) {
    // Custom cleanup when job ends
    log.Printf("Recording ended for job %s", jobID)
}
```

## Storage Backends

### Local Storage
Records directly to local filesystem:
```yaml
storage:
  type: local
  local:
    path: ./recordings
```

### S3 Storage
Uploads segments to Amazon S3:
```yaml
storage:
  type: s3
  s3:
    bucket: my-recordings
    region: us-west-2
    access_key_id: ${AWS_ACCESS_KEY_ID}
    secret_access_key: ${AWS_SECRET_ACCESS_KEY}
```

### GCS Storage
Uploads to Google Cloud Storage:
```yaml
storage:
  type: gcs
  gcs:
    bucket: my-recordings
    project_id: my-project
    credentials_file: /path/to/credentials.json
```

### Hybrid Storage
Records locally and uploads to cloud:
```yaml
storage:
  type: hybrid
  local:
    path: ./recordings
    buffer_on_failure: true
  s3:
    bucket: my-recordings
  enable_upload: true
  upload_interval: 60s
```

## Monitoring

### Health Check
The worker exposes health metrics:
```bash
curl http://localhost:9090/health
```

### Metrics
Prometheus metrics available at:
```bash
curl http://localhost:9090/metrics
```

Key metrics:
- `egress_sessions_active`: Current recording sessions
- `egress_sessions_total`: Total sessions started
- `egress_bytes_written`: Bytes written to storage
- `egress_segments_uploaded`: Segments uploaded
- `egress_errors_total`: Total errors

## Troubleshooting

### Common Issues

1. **Worker not receiving jobs**:
   - Verify LiveKit connection settings
   - Check API key/secret permissions
   - Ensure worker namespace matches job routing

2. **Recording quality issues**:
   - Adjust `video_quality` setting
   - Enable `adaptive_bitrate`
   - Check network bandwidth

3. **Storage failures**:
   - Verify storage permissions
   - Check available disk space
   - Enable `buffer_on_failure` for resilience

4. **High CPU/Memory usage**:
   - Reduce `max_concurrent_sessions`
   - Lower video quality settings
   - Enable resource limits

### Debug Logging

Enable debug logging for troubleshooting:
```yaml
logging:
  level: debug
  format: text
```

## Development

### Building from Source

```bash
# Clone repository
git clone https://github.com/livekit/agent-sdk-go
cd agent-sdk-go

# Build egress worker
cd examples/egress-worker
go build -o egress-worker main.go
```

### Running Tests

```bash
# Run all tests
go test ./pkg/egress/...

# Run with coverage
go test -cover ./pkg/egress/...
```

### Contributing

1. Fork the repository
2. Create your feature branch
3. Add tests for new functionality
4. Ensure all tests pass
5. Submit a pull request

## License

Apache 2.0 - See LICENSE for details

## Support

- Documentation: https://docs.livekit.io
- Community: https://livekit.io/community
- Issues: https://github.com/livekit/agent-sdk-go/issues