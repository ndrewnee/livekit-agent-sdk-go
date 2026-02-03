# LiveKit E2EE Publisher Client

A complete test environment for publishing encrypted audio/video tracks to LiveKit and recording them with the publisher-hls-agent.

## Quick Start (One Command)

```bash
cd examples/publisher-hls-agent/web-player
./serve.sh
```

This script starts:
1. **MinIO** - S3-compatible storage for recordings
2. **LiveKit Server** - WebRTC SFU
3. **publisher-hls-agent** - HLS recorder with E2EE decryption
4. **HTTP Server** - Serves the web client

## Manual Setup

### 1. Start MinIO
```bash
docker run -d --name minio \
  -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin \
  -e MINIO_ROOT_PASSWORD=minioadmin \
  minio/minio server /data --console-address ":9001"
```

### 2. Start LiveKit Server
```bash
docker run -d --name livekit \
  -p 7880:7880 -p 7881:7881 -p 7882:7882/udp \
  -e "LIVEKIT_KEYS=devkey: secret" \
  livekit/livekit-server --dev
```

### 3. Start the Agent
```bash
export LIVEKIT_URL="ws://localhost:7880"
export LIVEKIT_API_KEY="devkey"
export LIVEKIT_API_SECRET="secret"
export E2EE_PASSPHRASE="test-e2ee-secret-123"
export S3_ENDPOINT="localhost:9000"
export S3_BUCKET="publisher-hls"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_FORCE_PATH_STYLE="true"
export S3_USE_SSL="false"
export AUTO_ACTIVATE_RECORDING="true"
export AGENT_NAME="e2ee-web-agent"

./publisher-hls-agent
```

### 4. Start HTTP Server
```bash
cd web-player
python3 -m http.server 8080
```

### 5. Open Web Client
Navigate to: http://localhost:8080/e2ee-publisher-simple.html

## Configuration

### Default Settings

| Setting | Value |
|---------|-------|
| LiveKit URL | `ws://localhost:7880` |
| Room Name | `e2ee-web-room` |
| Agent Name | `e2ee-web-agent` |
| E2EE Passphrase | `test-e2ee-secret-123` |
| MinIO Endpoint | `localhost:9000` |
| S3 Bucket | `publisher-hls` |

### Environment Variables (serve.sh)

```bash
LIVEKIT_URL="ws://localhost:7880"
LIVEKIT_API_KEY="devkey"
LIVEKIT_API_SECRET="secret"
E2EE_PASSPHRASE="test-e2ee-secret-123"
MINIO_PORT="9000"
MINIO_ACCESS_KEY="minioadmin"
MINIO_SECRET_KEY="minioadmin"
MINIO_BUCKET="publisher-hls"
WEB_PORT="8080"
AGENT_NAME="e2ee-web-agent"
ROOM_NAME="e2ee-web-room"
```

## How E2EE Works

### Key Derivation

The passphrase is converted to a 128-bit AES key using PBKDF2:

```
Input: "test-e2ee-secret-123"
Salt: "LKFrameEncryptionKey"
Iterations: 100,000
Hash: SHA-256
Output: 16-byte AES key
```

This derivation is identical across:
- LiveKit JS SDK (`ExternalE2EEKeyProvider`)
- LiveKit Go SDK (`lksdk.DeriveKeyFromString()`)
- publisher-hls-agent's E2EE decryption

### Encryption Format

**Audio (Opus):**
```
[TOC byte (1)] + [encrypted payload + 16-byte tag] + [IV (12)] + [ivLen (1)] + [keyID (1)]
```

**Video (H264):**
```
Per NAL unit:
[start code] + [NAL header (1)] + [encrypted payload + 16-byte tag] + [IV (12)] + [ivLen (1)] + [keyID (1)]
```

Note: SPS and PPS NAL units are NOT encrypted (needed for codec negotiation).

## Flow

```
┌─────────────────┐     E2EE encrypted     ┌──────────────┐     Decrypted HLS     ┌───────┐
│  Web Browser    │ ──────────────────────→│ LiveKit SFU  │ ───────────────────→ │ MinIO │
│  (Camera+Mic)   │    WebRTC (SRTP)       │              │   publisher-hls-agent │  (S3) │
└─────────────────┘                        └──────────────┘                       └───────┘
        │                                         │
        └── Same passphrase ──────────────────────┘
```

## Viewing Recordings

### MinIO Console
Open http://localhost:9001 and login with `minioadmin`/`minioadmin`

### Direct HLS Playback
After recording, the HLS playlist is available at:
```
http://localhost:9000/publisher-hls/web-e2ee-recordings/{room}/{participant}/video.m3u8
```

## Troubleshooting

### "E2EE worker failed to load"
The E2EE worker requires HTTPS or localhost. Make sure you're accessing via `http://localhost:8080`.

### "Failed to create room"
- Check LiveKit server is running: `curl http://localhost:7880`
- Verify API key/secret match

### "Agent not receiving job"
- Verify agent is registered: check agent.log
- Room must be created with `agents` dispatch configuration
- Agent name must match

### "Decryption errors in agent log"
- Passphrase must match exactly between web client and agent
- Check for typos in `E2EE_PASSPHRASE`

### Camera/Microphone not working
- Allow permissions in browser
- Some browsers require HTTPS for WebRTC (localhost is usually exempt)

## Files

```
web-player/
├── e2ee-publisher-simple.html  # Main E2EE publisher client
├── e2ee-publisher.html         # Full-featured version
├── serve.sh                    # Complete environment startup script
├── README-e2ee.md              # This file
└── agent.log                   # Agent output (created by serve.sh)
```
