# LiveKit Server Auto-Detection

Automatic detection of running LiveKit server with port and credential extraction.

## Quick Usage

```bash
# Detect server and show connection details
./tools/hls-player/detect-livekit.sh

# Example output:
# ✓ LiveKit server running (PID: 54825)
# 📡 Detected Ports:
#    WebSocket: 7880
#    HTTP:      7881
# ✓ Server running in --dev mode
# Default credentials:
#    API Key:    devkey
#    API Secret: secret
```

## What It Detects

1. **Server Status**: Checks if `livekit-server` process is running
2. **Ports**: Extracts port from `--port` flag or detects listening ports
3. **Dev Mode**: Identifies `--dev` flag for default credentials
4. **Config File**: Locates config file if running in production mode

## Detection Methods

### Port Detection (Priority Order)

1. **Command Line Args**: Parses `--port` flag from process command
   ```bash
   livekit-server --dev --port 7880
   # Detects: WebSocket=7880, HTTP=7881
   ```

2. **Process Listening Ports**: Uses `lsof` to find TCP ports
   ```bash
   lsof -Pan -p $PID -i TCP -sTCP:LISTEN
   ```

3. **Common Port Probing**: Tests standard ports (7880, 8080)

4. **Defaults**: Falls back to ws://localhost:7880

### Credential Detection

**Dev Mode Detection**:
```bash
ps -p $PID -o command= | grep "\-\-dev"
# If found: devkey/secret
```

**Production Mode**:
```bash
ps -p $PID -o command= | grep -oE '\-\-config[= ][^ ]+'
# Reads keys from YAML config file
```

## Integration with Scripts

The detection script is automatically used by:

### manual-test.sh
```bash
./tools/hls-player/manual-test.sh

# Internally calls:
#   1. detect-livekit.sh
#   2. Parses environment variables
#   3. Exports LIVEKIT_URL, LIVEKIT_API_KEY, LIVEKIT_API_SECRET
#   4. Runs TestE2EManualVerification
```

### Custom Scripts
```bash
# Source detection output
eval $(./tools/hls-player/detect-livekit.sh 2>&1 | grep "^export LIVEKIT_")

# Now use the variables
echo "Connecting to: $LIVEKIT_URL"
go test -v -tags=e2e ./pkg/egress -run TestE2EManualVerification
```

## Output Format

### Environment Variables (for eval)
```bash
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
```

### Exit Codes
- `0`: Server detected successfully (dev mode with credentials)
- `1`: Server not found or no credentials available

## Manual Override

You can always override detected values:

```bash
# Override before running tests
export LIVEKIT_URL="ws://custom-host:9999"
export LIVEKIT_API_KEY="custom-key"
export LIVEKIT_API_SECRET="custom-secret"

./tools/hls-player/manual-test.sh
```

## Troubleshooting

### "LiveKit server not detected"
**Cause**: No `livekit-server` process running

**Solutions**:
```bash
# Start local server
livekit-server --dev

# Or Docker
docker run -d -p 7880:7880 -p 7881:7881 livekit/livekit-server --dev

# Or continue with manual configuration
export LIVEKIT_URL="ws://remote-host:7880"
```

### Wrong Port Detected
**Cause**: Multiple LiveKit servers or non-standard ports

**Solution**: Check process manually
```bash
ps aux | grep livekit-server
# Look for --port flag

# Override
export LIVEKIT_URL="ws://localhost:CORRECT_PORT"
```

### Production Mode (No Credentials)
**Cause**: Server running without `--dev` flag

**Solution**: Set credentials manually
```bash
# Find config file
ps aux | grep livekit-server | grep -o '\-\-config [^ ]*'

# Read keys from config
cat /path/to/config.yaml

# Export
export LIVEKIT_API_KEY="your_key"
export LIVEKIT_API_SECRET="your_secret"
```

## Platform Support

| Platform | Port Detection | Credential Detection |
|----------|---------------|---------------------|
| macOS    | ✅ (lsof)     | ✅ (ps)             |
| Linux    | ✅ (lsof)     | ✅ (ps)             |
| Windows  | ⚠️ (fallback) | ⚠️ (fallback)       |

**Note**: Windows support uses port probing fallback. Install `lsof` via WSL for better detection.

## Security Notes

- Detection script only reads public process information
- Does NOT access credentials from config files directly
- Only displays credentials for `--dev` mode (known defaults)
- Production credentials must be provided manually

## Related Files

- `detect-livekit.sh`: Main detection script
- `manual-test.sh`: Uses detection for E2E tests
- `LIVEKIT_SETUP.md`: LiveKit server setup guide
- `QUICKSTART.md`: Quick start guide with detection examples
