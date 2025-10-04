# Full-Scale E2E Test Guide

Comprehensive end-to-end test that validates the complete egress workflow with real LiveKit infrastructure.

## What This Test Does

The full-scale E2E test (`TestE2EFullScale`) validates:

1. ✅ **LiveKit Server**: Connects to local LiveKit server (dev mode)
2. ✅ **Egress Worker**: Starts and registers egress agent with LiveKit
3. ✅ **Multiple Participants**: Launches N participants publishing real media
4. ✅ **Job Verification**: Confirms egress jobs are created for each participant
5. ✅ **HLS Output**: Captures video/audio tracks to HLS format
6. ✅ **MinIO Storage**: Uploads HLS segments to MinIO S3
7. ✅ **URL Generation**: Provides MinIO URLs for manual verification

## Quick Start

### Prerequisites

**1. LiveKit Server Running**
```bash
# Check if running
curl http://localhost:7881/validate

# Start if needed
livekit-server --dev
```

**2. MinIO Running (optional - script will start it)**
```bash
# Check if running
curl http://localhost:9000/minio/health/live

# Or script will start Docker container automatically
```

**3. Test Media File**
```bash
# Verify test.mp4 exists
ls -lh examples/egress-agent/test-data/test.mp4
# Should show: 34MB, 1280x720 H.264 + Opus audio
```

### Run the Test

**Option 1: Use the Script (Recommended)**
```bash
# Automated setup and execution
./tools/hls-player/fullscale-test.sh

# The script will:
#   ✓ Detect LiveKit server
#   ✓ Start MinIO if needed
#   ✓ Configure MinIO bucket
#   ✓ Run the test
#   ✓ Display MinIO URLs
#   ✓ Offer to start HLS player
```

**Option 2: Run Test Directly**
```bash
# Export environment variables
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
export MINIO_ENDPOINT="localhost:9000"
export MINIO_ACCESS_KEY="minioadmin"
export MINIO_SECRET_KEY="minioadmin"
export MINIO_BUCKET="egress-test"

# Run test
go test -v -tags=e2e ./pkg/egress -run TestE2EFullScale -timeout 5m
```

## Test Flow

### Step-by-Step Execution

**1. Server Verification**
```
✓ LiveKit server connected
```
- Connects to LiveKit server
- Verifies API access with ListRooms

**2. Worker Registration**
```
✓ Egress worker started and registered
```
- Creates egress worker with config
- Registers handler with LiveKit
- Waits for registration confirmation

**3. Room Creation**
```
✓ Room created: fullscale-test-1759340000 (SID: RM_xxxxx)
```
- Creates unique room for test
- Auto-deletes room after test

**4. Participant Launch**
```
  Connecting participant: participant-0
  ✓ Participant connected: participant-0
  Connecting participant: participant-1
  ✓ Participant connected: participant-1
  Connecting participant: participant-2
  ✓ Participant connected: participant-2
✓ All 3 participants connected and publishing
```
- Launches participants in parallel
- Each publishes H.264 video from test.mp4
- Each publishes Opus audio from test.mp4
- Duration: 15 seconds

**5. Job Verification**
```
  Active egress jobs: 3
    - Egress ID: EG_xxxxx, Status: EGRESS_ACTIVE
    - Egress ID: EG_yyyyy, Status: EGRESS_ACTIVE
    - Egress ID: EG_zzzzz, Status: EGRESS_ACTIVE
```
- Queries LiveKit for active egress jobs
- Confirms one job per participant

**6. Recording Progress**
```
  Recording in progress...
  Recording in progress...
  Recording in progress...
✓ Recording complete
```
- Records for 15 seconds
- Shows progress every 5 seconds

**7. Finalization**
```
  Disconnected participant-0
  Disconnected participant-1
  Disconnected participant-2
✓ Sessions finalized
```
- Disconnects participants to trigger EOS
- Waits for egress sessions to flush

**8. MinIO Verification**
```
  Found 9 objects in MinIO:
    - fullscale-1759340000/participant-0/playlist.m3u8
    - fullscale-1759340000/participant-0/segment00000.ts
    - fullscale-1759340000/participant-0/segment00001.ts
    - fullscale-1759340000/participant-1/playlist.m3u8
    ...
```
- Lists uploaded files
- Verifies playlists and segments

**9. URL Generation**
```
Participant 0:
  http://localhost:9000/egress-test/fullscale-1759340000/participant-0/playlist.m3u8

Participant 1:
  http://localhost:9000/egress-test/fullscale-1759340000/participant-1/playlist.m3u8

Participant 2:
  http://localhost:9000/egress-test/fullscale-1759340000/participant-2/playlist.m3u8
```
- Generates MinIO URLs for each playlist
- Ready for manual playback verification

## Configuration

### Test Parameters

```go
participantCount := 3           // Number of participants
recordingDuration := 15 * time.Second  // Recording time per participant
```

**Adjust for different scenarios:**

```bash
# Light test (1 participant, 10 seconds)
# Edit e2e_fullscale_test.go:
participantCount := 1
recordingDuration := 10 * time.Second

# Stress test (10 participants, 30 seconds)
participantCount := 10
recordingDuration := 30 * time.Second
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LIVEKIT_URL` | `ws://localhost:7880` | LiveKit WebSocket URL |
| `LIVEKIT_API_KEY` | `devkey` | API key (dev mode) |
| `LIVEKIT_API_SECRET` | `secret` | API secret (dev mode) |
| `MINIO_ENDPOINT` | `localhost:9000` | MinIO endpoint |
| `MINIO_ACCESS_KEY` | `minioadmin` | MinIO access key |
| `MINIO_SECRET_KEY` | `minioadmin` | MinIO secret key |
| `MINIO_BUCKET` | `egress-test` | S3 bucket name |

## Manual Verification

### Verify HLS Playback

**1. Set up MinIO public access:**
```bash
./tools/hls-player/setup-minio.sh
```

**2. Start HLS player:**
```bash
./tools/hls-player/play.sh
```

**3. Open in browser:**
```
http://localhost:8080/tools/hls-player/player.html
```

**4. Load each playlist URL:**
- Copy URL from test output
- Paste into player
- Click "Load Stream"

**5. Verify quality:**
- ✅ Video plays smoothly
- ✅ Audio is clear (not garbled)
- ✅ Duration is ~15 seconds
- ✅ No dropped frames
- ✅ Audio/video sync correct

### Verify MinIO Console

**1. Open MinIO Console:**
```
http://localhost:9001
```

**2. Login:**
- Username: `minioadmin`
- Password: `minioadmin`

**3. Browse bucket:**
- Navigate to `egress-test` bucket
- Check session directory (e.g., `fullscale-1759340000/`)
- Verify files for each participant

**4. Download for analysis:**
- Download `.m3u8` playlist
- Download `.ts` segments
- Analyze with ffprobe or VLC

## Troubleshooting

### Test Fails: "Failed to connect to LiveKit server"

**Cause**: LiveKit not running or wrong credentials

**Solution:**
```bash
# Check server
curl http://localhost:7881/validate

# Start server
livekit-server --dev

# Or detect automatically
./tools/hls-player/detect-livekit.sh
```

### Test Fails: "Failed to create S3 client"

**Cause**: MinIO not running or wrong endpoint

**Solution:**
```bash
# Start MinIO
docker run -d -p 9000:9000 -p 9001:9001 \
  --name minio-egress-test \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  quay.io/minio/minio server /data --console-address ":9001"

# Verify
curl http://localhost:9000/minio/health/live
```

### Test Fails: "Worker failed to start"

**Cause**: Worker registration error or config issue

**Solution:**
```bash
# Check LiveKit logs
# Look for worker registration messages

# Verify config in test
# Check MaxConcurrentSessions >= participantCount
```

### Test Passes but No MinIO Files

**Cause**: Upload failed or bucket doesn't exist

**Solution:**
```bash
# Create bucket manually
mc alias set myminio http://localhost:9000 minioadmin minioadmin
mc mb myminio/egress-test

# Check worker logs for upload errors
```

### Video Plays but Audio is Garbled

**Cause**: Recording too short for AAC encoder

**Solution:**
```go
// Increase recording duration
recordingDuration := 20 * time.Second  // Was 15
```

### "Access Denied" in Browser

**Cause**: Bucket not public

**Solution:**
```bash
# Run bucket setup
./tools/hls-player/setup-minio.sh

# Or set policy manually
mc anonymous set download myminio/egress-test
```

## Performance Expectations

### Resource Usage

**Per Participant:**
- CPU: ~15% (Opus→AAC transcoding + H.264 muxing)
- Memory: ~50MB (GStreamer pipeline buffers)
- Network: ~2 Mbps upload to MinIO

**Total for 3 Participants:**
- CPU: ~45%
- Memory: ~150MB
- Network: ~6 Mbps

**Stress Test (10 Participants):**
- CPU: ~150% (multi-core)
- Memory: ~500MB
- Network: ~20 Mbps

### Timing

| Phase | Duration |
|-------|----------|
| Setup | ~5s |
| Participant connection | ~3s |
| Recording | 15s (configurable) |
| Finalization | ~10s |
| **Total** | **~33s** |

## Advanced Usage

### Custom Participant Count

```bash
# Edit test file
vim pkg/egress/e2e_fullscale_test.go

# Change line:
participantCount := 10  // Was 3
```

### Different Video Source

```go
// Use different MP4 file
mp4File := "path/to/your/video.mp4"

// Or use synthetic RTP (faster, no file needed)
// See e2e_simple_test.go for examples
```

### Production LiveKit Server

```bash
# Set production credentials
export LIVEKIT_URL="wss://your-server.livekit.cloud"
export LIVEKIT_API_KEY="your-api-key"
export LIVEKIT_API_SECRET="your-api-secret"

# Run test
./tools/hls-player/fullscale-test.sh
```

### AWS S3 Instead of MinIO

```bash
# Set S3 credentials
export MINIO_ENDPOINT=""  # Empty for AWS
export MINIO_ACCESS_KEY="your-aws-key"
export MINIO_SECRET_KEY="your-aws-secret"
export MINIO_BUCKET="your-s3-bucket"

# Run test
go test -v -tags=e2e ./pkg/egress -run TestE2EFullScale
```

## Related Files

- `pkg/egress/e2e_fullscale_test.go`: Main test implementation
- `pkg/egress/e2e_real_helpers.go`: Helper functions for media publishing
- `pkg/egress/s3_helpers.go`: MinIO/S3 integration
- `tools/hls-player/fullscale-test.sh`: Automated test runner
- `tools/hls-player/setup-minio.sh`: MinIO bucket configuration

## Next Steps

After successful full-scale test:

1. ✅ Test with real production LiveKit server
2. ✅ Test with AWS S3 instead of MinIO
3. ✅ Stress test with 20+ participants
4. ✅ Test with different video codecs (VP8, VP9)
5. ✅ Test with different audio codecs (AAC source)
6. ✅ Test with adaptive bitrate (multiple quality tracks)
