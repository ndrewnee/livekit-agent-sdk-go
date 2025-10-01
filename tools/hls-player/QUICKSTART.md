# HLS Player Quick Start Guide

A 5-minute guide to verify your HLS egress output works correctly.

## Step 1: Run a Test

```bash
cd /Users/alexeysokolov/GolandProjects/livekit-agent-sdk-go

# Run E2E test (creates HLS files in temp directory)
go test -v -tags=e2e ./pkg/egress -run TestE2EPipelineHLSGeneration
```

**Look for this in the output:**
```
Output directory: /var/folders/jt/w9x2t6tn4vv_t2r_y00424nh0000gn/T/TestE2EPipelineHLSGeneration3682446227/001
Session ID: e2e-session-1759319817
✓ HLS output VERIFIED successfully:
  - Playlist: playlist.m3u8
  - Segments: 2
```

**Copy the full path to your playlist:**
```
/var/folders/jt/.../001/e2e-session-1759319817/playlist.m3u8
```

## Step 2: Start the Player Server

Open a new terminal:

```bash
cd /Users/alexeysokolov/GolandProjects/livekit-agent-sdk-go

# Easy way - use the launch script
./tools/hls-player/play.sh

# Or manually with Go
go run tools/hls-player/server.go

# Or with Python
python3 -m http.server 8080
```

You should see:
```
🎥 HLS Player Server
Serving files from: /Users/alexeysokolov/GolandProjects/livekit-agent-sdk-go
Listening on: http://localhost:8080

Quick links:
  Player: http://localhost:8080/tools/hls-player/player.html
```

## Step 3: Open the Player

Your browser should open automatically. If not:

```bash
open http://localhost:8080/tools/hls-player/player.html
```

## Step 4: Load Your Stream

1. **Paste the playlist path** from Step 1 into the input field
2. **Click "Load Stream"**
3. **Video should play!** 🎉

Example path:
```
/var/folders/jt/w9x2t6tn4vv_t2r_y00424nh0000gn/T/TestE2EPipelineHLSGeneration3682446227/001/e2e-session-1759319817/playlist.m3u8
```

## What to Verify

✅ **Video plays smoothly** (no stuttering)
✅ **Duration shows**: ~2-3 seconds
✅ **Resolution shows**: 640x480 or similar
✅ **Audio works**: Unmute and check
✅ **No errors** in the stats panel
✅ **Dropped frames**: Should be 0

## Testing MinIO Upload

If you want to test MinIO upload:

```bash
# Terminal 1: Start MinIO
docker run -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  quay.io/minio/minio server /data --console-address ":9001"

# Terminal 2: Run test
go test -v -tags=e2e ./pkg/egress -run TestE2ECompletePipeline

# Terminal 3: Start player
./tools/hls-player/play.sh
```

**In the player, use MinIO URL:**
```
http://localhost:9000/egress-test/complete-e2e-1759328065/playlist.m3u8
```

## Troubleshooting

### "404 Not Found"
- **Cause**: Path is wrong or test temp files were cleaned up
- **Fix**: Run the test again and use the new path immediately

### "Network Error"
- **Cause**: Server not started or wrong port
- **Fix**: Make sure server is running on port 8080

### "CORS Error"
- **Cause**: Opening player.html directly (file:// protocol)
- **Fix**: Use the HTTP server (go/python/node)

### Video doesn't play
- **Check browser console** (F12) for detailed errors
- **Try a different browser** (Chrome works best)
- **Verify test passed** and created files

### No audio
- **Click the 🔊 button** to unmute
- **Check browser isn't muted**
- **Verify test injected audio packets**

## Advanced: Browse Test Output

If you forgot the path, find recent test outputs:

```bash
# macOS
ls -lt /var/folders/jt/*/T/TestE2E* | head -20

# Linux
ls -lt /tmp/TestE2E* | head -20

# Look for directories with playlist.m3u8 inside
find /var/folders -name "playlist.m3u8" -mmin -10 2>/dev/null
```

## Quick Reference

| Action | Command |
|--------|---------|
| Run E2E test | `go test -v -tags=e2e ./pkg/egress -run TestE2EPipelineHLSGeneration` |
| Start player | `./tools/hls-player/play.sh` |
| Start with Go | `go run tools/hls-player/server.go` |
| Start with Python | `python3 -m http.server 8080` |
| Open player | `open http://localhost:8080/tools/hls-player/player.html` |
| Find recent output | `find /var/folders -name "playlist.m3u8" -mmin -10` |

## Example Complete Workflow

```bash
# 1. Run test
go test -v -tags=e2e ./pkg/egress -run TestE2EPipelineHLSGeneration

# Output: playlist at /var/folders/.../e2e-session-123/playlist.m3u8

# 2. Start server (in new terminal)
go run tools/hls-player/server.go

# 3. Open player
open http://localhost:8080/tools/hls-player/player.html

# 4. Paste path and click "Load Stream"
# /var/folders/.../e2e-session-123/playlist.m3u8

# 5. Verify playback! ✅
```

## Next Steps

- Test with real LiveKit room (see `e2e_real_test.go`)
- Verify MinIO upload functionality
- Check different quality settings
- Test with longer recordings
- Verify segment timing is correct

## Support

If you encounter issues:
1. Check test actually created HLS files
2. Verify server is running and accessible
3. Look at browser console for errors
4. Try with a minimal example first
5. Check the full README.md for detailed troubleshooting
