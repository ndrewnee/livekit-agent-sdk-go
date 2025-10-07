# Save to HLS - Test Documentation

This document describes the automated test for the save-to-hls example.

## Test Overview

The test (`main_test.go`) validates the complete HLS recording workflow:

1. **LiveKit Server Verification** - Checks server is running and accessible
2. **Source File Validation** - Verifies test.mp4 exists
3. **Room Setup** - Creates a test room
4. **HLS Recording** - Runs the complete recording process
5. **Segment Validation** - Validates each generated segment file
6. **Playlist Validation** - Checks HLS playlist structure
7. **Playability Check** - Verifies stream can be played
8. **Summary Generation** - Generates detailed report

## Prerequisites

- **LiveKit server** running on `ws://localhost:7880`
- **FFmpeg** and **FFprobe** installed
- **Go 1.22+**
- **Test video** at `examples/egress-agent/test-data/test.mp4`

## Running the Test

### Quick Run
```bash
cd examples/save-to-hls
go test -v -tags e2e -run TestSaveToHLS
```

### With Timeout
```bash
go test -v -tags e2e -run TestSaveToHLS -timeout 60s
```

### Save Output to File
```bash
go test -v -tags e2e -run TestSaveToHLS 2>&1 | tee test-output.log
```

## Test Steps Explained

### Step 1: Verify LiveKit Server
Attempts to connect to the LiveKit server to ensure it's running and accessible.

**Expected**: Connection succeeds within 5 seconds

### Step 2: Verify test.mp4
Checks that the source video file exists.

**Expected**: File exists at `../../examples/egress-agent/test-data/test.mp4`

### Step 3: Create LiveKit Room
Creates a test room for the recording.

**Expected**: Room created successfully, cleaned up after test

### Step 4: Run HLS Recording
Performs the complete workflow:
- Connects publisher and recorder
- Extracts H.264 from test.mp4
- Publishes video to room
- Records HLS segments
- Records for 15 seconds

**Expected**: At least 3 segments generated

### Step 5: Validate Segment Files
For each segment:
- Checks file exists and has size > 0
- Runs ffprobe to validate format
- Verifies H.264 video stream present
- Checks video dimensions
- Validates duration

**Expected**:
- All segments are valid TS files
- Each contains H.264 video
- Resolution is 1280x720
- Duration is ~2-4 seconds per segment

### Step 6: Validate HLS Playlist
Checks the generated playlist:
- Required tags present (#EXTM3U, #EXT-X-VERSION, etc.)
- References all segment files
- Properly closed with #EXT-X-ENDLIST

**Expected**: Valid HLS playlist structure

### Step 7: Check Playability
Uses ffprobe to validate the complete stream:
- Reads the playlist as a whole
- Verifies streams are detected
- Checks total duration

**Expected**:
- Stream is recognized as HLS
- Duration is ~10-12 seconds (may be less due to timestamp issues)

### Step 8: Generate Summary
Creates a detailed summary with:
- Per-segment statistics (size, duration)
- Total statistics
- Format information

## Expected Output

### Successful Test
```
=== Step 1: Verify LiveKit server is running ===
✓ LiveKit server is running

=== Step 2: Verify test.mp4 exists ===
✓ Test video exists

=== Step 3: Create LiveKit room ===
✓ Room created

=== Step 4: Run HLS recording ===
  → Connecting publisher...
  → Publishing video track...
  → Track published: TR_xxxxx
  → Connecting recorder...
  → Video track received
  → Starting HLS saver...
  → Recording for 15 seconds...
  → Recording in progress...
  → Recording timeout reached
  → Finalizing HLS output...
✓ Recording complete: 3 segments generated

=== Step 5: Validate segment files ===
  → Found 3 segments
  → Segment 0: xxx_main_seg0.ts (0.78 MB)
    • Video: h264 1280x720
    • Duration: 3.98 seconds
  → Segment 1: xxx_main_seg1.ts (0.81 MB)
    • Video: h264 1280x720
    • Duration: 4.11 seconds
  → Segment 2: xxx_main_seg2.ts (0.78 MB)
    • Video: h264 1280x720
    • Duration: 3.75 seconds
✓ All segments are valid

=== Step 6: Validate HLS playlist ===
  → Playlist has required tags
  → Playlist references all 3 segments
  → Playlist is properly closed
✓ Playlist is valid

=== Step 7: Check playability ===
  → Format: hls
  → Streams: 1
  → Total duration: 11.84 seconds
✓ Stream is playable

=== Step 8: Generate summary ===
═══════════════════════════════════════════════════════════
                    HLS Recording Summary
═══════════════════════════════════════════════════════════

Segment 0: xxx_main_seg0.ts
  Size:     0.78 MB
  Duration: 3.98 seconds

Segment 1: xxx_main_seg1.ts
  Size:     0.81 MB
  Duration: 4.11 seconds

Segment 2: xxx_main_seg2.ts
  Size:     0.78 MB
  Duration: 3.75 seconds

═══════════════════════════════════════════════════════════
Total Segments: 3
Total Size:     2.37 MB
Total Duration: 11.84 seconds
═══════════════════════════════════════════════════════════

✅ All tests passed!
```

## Known Issues

### Short Recording Duration
The test may produce ~11-12 seconds instead of the expected 15 seconds due to timestamp accumulation issues in the HLS saver. This is a known limitation and doesn't affect playability.

### Empty Last Segment
The last segment (seg2.ts) may have a shorter duration or be empty. This is expected when the recording is stopped mid-segment.

### Warning Messages
You may see warnings about duration being short. This is acceptable as long as:
- At least 3 segments are generated
- Each segment is valid
- Total duration is > 8 seconds

## Troubleshooting

### Test Fails: "LiveKit server not available"
**Solution**: Start the LiveKit server:
```bash
cd /path/to/livekit
./livekit-server --dev
```

### Test Fails: "Test video not found"
**Solution**: Verify the test video exists:
```bash
ls ../../examples/egress-agent/test-data/test.mp4
```

### Test Fails: "No segments found"
**Possible Causes**:
1. HLS saver failed to start
2. No video packets received
3. Segment duration too long

**Debug**:
```bash
# Check test output directory
ls -lh /tmp/hls-test-output/

# Check for error messages in test output
go test -v -tags e2e -run TestSaveToHLS 2>&1 | grep -i error
```

### Test Fails: Segment validation
**Possible Causes**:
1. FFprobe not installed
2. Corrupt segment files
3. Invalid TS format

**Debug**:
```bash
# Manually check a segment
ffprobe /tmp/hls-test-output/hls-test-*/segment_file.ts

# Check segment hex
xxd /tmp/hls-test-output/hls-test-*/segment_file.ts | head
```

### Test Timeout
**Solution**: Increase timeout:
```bash
go test -v -tags e2e -run TestSaveToHLS -timeout 120s
```

## Test Output Files

The test creates temporary files in `/tmp/hls-test-output/`:
```
/tmp/hls-test-output/
└── hls-test-{timestamp}_final/
    ├── {hash}_main_seg0.ts
    ├── {hash}_main_seg1.ts
    ├── {hash}_main_seg2.ts
    └── playlist.m3u8
```

These files are automatically cleaned up after the test completes.

## CI/CD Integration

### GitHub Actions Example
```yaml
name: HLS Test
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.22'

      - name: Install FFmpeg
        run: sudo apt-get update && sudo apt-get install -y ffmpeg

      - name: Start LiveKit Server
        run: |
          wget https://github.com/livekit/livekit/releases/download/v1.5.0/livekit_1.5.0_linux_amd64.tar.gz
          tar -xzf livekit_1.5.0_linux_amd64.tar.gz
          ./livekit-server --dev &
          sleep 5

      - name: Run HLS Test
        run: |
          cd examples/save-to-hls
          go test -v -tags e2e -run TestSaveToHLS -timeout 60s
```

## Performance Benchmarks

Typical test execution times:
- **Full test**: ~20-25 seconds
- **Room setup**: ~1 second
- **Recording**: ~15 seconds
- **Validation**: ~2-3 seconds

## See Also

- [Main README](README.md) - Usage instructions for the example
- [HLS Saver Implementation](../../pkg/egress/hls_saver.go) - Core recording logic
- [E2E Full-Scale Test](../../pkg/egress/e2e_fullscale_test.go) - Multi-participant test
