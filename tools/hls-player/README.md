# HLS Player - LiveKit Egress Verification Tool

A simple, browser-based HLS player for verifying LiveKit egress output stored in MinIO.

## Features

- ✅ HLS.js integration for broad browser support
- ✅ Native HLS support detection (Safari)
- ✅ Real-time playback statistics
- ✅ Quality and bitrate monitoring
- ✅ Fullscreen support
- ✅ Playback controls
- ✅ Direct MinIO integration
- ✅ Error handling and debugging

## Usage

### 1. Start a Local Web Server

```bash
cd tools/hls-player
python3 -m http.server 8080
```

Or use any other static file server.

### 2. Open in Browser

Navigate to: `http://localhost:8080/player.html`

### 3. Load HLS Stream

Enter the full URL to your HLS playlist (`.m3u8` file) from MinIO:

```
http://localhost:9000/egress-test/session-123456/playlist.m3u8
```

Click "Load Stream" to start playback.

## MinIO Integration

### Direct URLs

The player can access HLS streams directly from MinIO if:

1. MinIO is accessible from the browser (same network or public)
2. CORS is configured (see below)
3. The bucket has appropriate access policy

### Example URLs

**Local MinIO:**
```
http://localhost:9000/egress-test/session-123/playlist.m3u8
```

**MinIO Console (to browse files):**
```
http://localhost:9001
```
Username: `minioadmin`
Password: `minioadmin`

## CORS Configuration

For browser access to MinIO, you need to configure CORS:

### Option 1: Public Access (Development Only)

```bash
mc anonymous set download myminio/egress-test
```

### Option 2: Proper CORS (Recommended)

Create a CORS configuration file `cors.json`:

```json
{
  "CORSRules": [
    {
      "AllowedOrigins": ["http://localhost:8080"],
      "AllowedMethods": ["GET", "HEAD"],
      "AllowedHeaders": ["*"],
      "ExposeHeaders": ["ETag"]
    }
  ]
}
```

Apply it:

```bash
mc admin config set myminio/ cors < cors.json
mc admin service restart myminio/
```

### Option 3: MinIO Browser Proxy

Use MinIO's built-in console at `http://localhost:9001` which handles CORS automatically.

## Features Explained

### Real-time Statistics

The player shows:
- **Duration**: Total stream length
- **Current Time**: Playback position
- **Buffered**: Amount of content buffered
- **Quality**: Video resolution (e.g., 1920x1080)
- **Bitrate**: Current bitrate in Mbps
- **Dropped Frames**: Playback quality indicator

### Playback Controls

- ▶ Play/Pause
- ⏮ Restart from beginning
- 🔊 Mute/Unmute
- ⛶ Fullscreen toggle

### Error Handling

The player provides detailed error messages for:
- Network errors (connection issues, CORS)
- Media errors (corrupt stream)
- Browser compatibility issues

## Supported Formats

- **Video**: H.264, VP8, VP9 (browser-dependent)
- **Audio**: Opus, AAC
- **Container**: MPEG-TS segments
- **Playlist**: HLS (HTTP Live Streaming)

## Browser Compatibility

| Browser | Support | Notes |
|---------|---------|-------|
| Chrome/Edge | ✅ Full | Via HLS.js |
| Firefox | ✅ Full | Via HLS.js |
| Safari | ✅ Native | Native HLS support |
| Opera | ✅ Full | Via HLS.js |
| Mobile Safari | ✅ Native | Native HLS support |
| Mobile Chrome | ✅ Full | Via HLS.js |

## Troubleshooting

### "Network error: Failed to fetch"

**Cause**: CORS not configured or MinIO not accessible
**Solution**:
1. Check MinIO is running: `curl http://localhost:9000/minio/health/live`
2. Configure CORS (see above)
3. Check browser console for detailed error

### "Media error: Could not decode stream"

**Cause**: Corrupted or incompatible media
**Solution**:
1. Verify HLS files with `ffprobe`
2. Check segment encoding
3. Ensure proper codec support

### "HLS is not supported in this browser"

**Cause**: Very old browser
**Solution**: Update browser or use modern alternative

### Playback Stuttering

**Possible causes**:
- Network bandwidth issues
- High CPU usage
- Insufficient buffering

**Solutions**:
- Check network connection
- Close other tabs
- Try lower quality stream

## Development

### Customize Player

Edit `player.html` to:
- Change color scheme (CSS variables)
- Modify default settings
- Add custom controls
- Integrate with other services

### Key JavaScript Functions

```javascript
// Load a stream
loadStream()

// Handle errors
showError(message)

// Update statistics
startStatsUpdate()

// Toggle fullscreen
toggleFullscreen()
```

## Integration Examples

### URL Parameters

Load a stream automatically:

```html
http://localhost:8080/player.html?url=http://localhost:9000/egress-test/session-123/playlist.m3u8
```

### Embedding

Embed the player in your application:

```html
<iframe
  src="http://localhost:8080/player.html?url=YOUR_STREAM_URL"
  width="100%"
  height="600px"
  frameborder="0">
</iframe>
```

### API Integration

Use programmatically:

```javascript
// In your page
const player = document.createElement('iframe');
player.src = `http://localhost:8080/player.html?url=${streamUrl}`;
document.body.appendChild(player);
```

## Performance

- **Low latency**: < 2 seconds with proper HLS configuration
- **Adaptive streaming**: Automatically adjusts quality
- **Efficient buffering**: Minimal memory usage
- **Hardware acceleration**: Uses browser's native decoders

## Security Notes

- Use HTTPS in production
- Configure proper CORS policies
- Use signed URLs for MinIO (in production)
- Don't expose MinIO credentials to browser

## Related Tools

- **ffprobe**: Inspect stream details
- **VLC**: Alternative player for testing
- **MinIO Console**: Browse uploaded files
- **curl**: Test HTTP access

## License

This player uses:
- [HLS.js](https://github.com/video-dev/hls.js/) - Apache License 2.0
- Standard web APIs (no additional licenses)

## Support

For issues:
1. Check browser console for errors
2. Verify MinIO accessibility
3. Test stream with VLC/ffmpeg
4. Review CORS configuration
5. Check network connectivity