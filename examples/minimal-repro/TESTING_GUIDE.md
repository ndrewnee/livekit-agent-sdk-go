# Testing Guide: Minimal Reproduction for LiveKit Maintainers

## Overview

This guide shows how to run the minimal reproduction to demonstrate Issue #1 (Forwarder Layer Initialization) to LiveKit maintainers.

## Prerequisites

- Go 1.22+
- LiveKit server (both unpatched and patched versions)

## Test Setup

### 1. Unpatched Server Test

```bash
# Terminal 1: Start UNPATCHED LiveKit server
cd /path/to/livekit
git checkout master  # Ensure on latest master
livekit-server --dev

# Terminal 2: Run minimal reproduction
cd /Users/alexeysokolov/GolandProjects/livekit-agent-sdk-go/examples/minimal-repro
go run main.go
```

**Expected Output (UNPATCHED)**:
```
=== Minimal Reproduction: Forwarder Layer Initialization Bug ===
Server: ws://localhost:7880
Room: minimal-repro-room
[PUBLISHER] Connecting to room...
[PUBLISHER] Publishing H.264 video track...
[PUBLISHER] Publishing video packets at 30 FPS...
[AGENT] Connecting to room with MANUAL subscription...
[PUBLISHER] Sent keyframe #1
[AGENT] Found publisher, subscribing to video track...
[AGENT] Subscribed to video track
[AGENT] Requested HIGH video quality
[AGENT] Waiting for video packets...
[PUBLISHER] Sent keyframe #2
[PUBLISHER] Sent keyframe #3
...
[PUBLISHER] Finished publishing

=== RESULTS ===
Publisher sent: 300 packets
Agent received: 0 packets

❌ BUG REPRODUCED: Agent received 0 packets despite publisher sending
This indicates the forwarder layer initialization bug.
Run with PATCHED server to see packets received.
```

### 2. Patched Server Test

```bash
# Terminal 1: Start PATCHED LiveKit server
cd /tmp/livekit-fork
git checkout agent-recording-fixes  # Branch with patches
livekit-server --dev

# Terminal 2: Run minimal reproduction (same command)
cd /Users/alexeysokolov/GolandProjects/livekit-agent-sdk-go/examples/minimal-repro
go run main.go
```

**Expected Output (PATCHED)**:
```
=== Minimal Reproduction: Forwarder Layer Initialization Bug ===
Server: ws://localhost:7880
Room: minimal-repro-room
[PUBLISHER] Connecting to room...
[PUBLISHER] Publishing H.264 video track...
[PUBLISHER] Publishing video packets at 30 FPS...
[AGENT] Connecting to room with MANUAL subscription...
[PUBLISHER] Sent keyframe #1
[AGENT] Found publisher, subscribing to video track...
[AGENT] Subscribed to video track
[AGENT] Requested HIGH video quality
[AGENT] Waiting for video packets...
[AGENT] ✅ Received first video packet!
[PUBLISHER] Sent keyframe #2
[AGENT] Received 30 packets
[AGENT] Received 60 packets
...
[PUBLISHER] Finished publishing

=== RESULTS ===
Publisher sent: 300 packets
Agent received: 300 packets

✅ Working correctly: Agent receiving packets
Server has the fix applied or different code path triggered.
```

## Capturing Evidence for Maintainers

### Server Logs (Unpatched)

Run with additional logging to capture evidence:

```bash
# Start server with debug logging
cd /path/to/livekit
LIVEKIT_LOG_LEVEL=debug livekit-server --dev 2>&1 | tee /tmp/unpatched-server.log
```

Look for in logs:
- `SetMaxSpatialLayer` being called
- No corresponding packet forwarding
- `numDownTracks=1` but `writeCount=0`

### Server Logs (Patched)

```bash
# Start patched server with debug logging
cd /tmp/livekit-fork
LIVEKIT_LOG_LEVEL=debug livekit-server --dev 2>&1 | tee /tmp/patched-server.log
```

Look for in logs:
- `SetMaxSpatialLayer` being called
- Immediate packet forwarding
- `numDownTracks=1` and `writeCount=1` (or higher)

## Key Evidence to Provide Maintainers

1. **Client Output**: Screenshots/logs of both runs showing 0 vs 300 packets
2. **Server Logs**: Filtered logs showing forwarder behavior
3. **Timing Analysis**: Logs showing SetVideoQuality() called immediately after SetSubscribed()

## What Makes This Reproduce the Bug

The minimal repro triggers the bug by:

1. **Manual subscription**: `lksdk.WithAutoSubscribe(false)` - line 192
2. **Immediate quality request**: `SetVideoQuality(HIGH)` called < 100ms after `SetSubscribed(true)` - lines 229-243
3. **Race condition**: SetMaxSpatialLayer() called before stream allocator initializes layers

This timing is **not** seen in:
- Standard P2P calls (auto-subscribe enabled)
- Python agents SDK (uses auto_subscribe=true by default)
- Delayed quality requests (allocator has time to run)

## Uploading to Repository

The minimal repro is ready to push to your fork:

```bash
cd /Users/alexeysokolov/GolandProjects/livekit-agent-sdk-go
git add examples/minimal-repro/
git commit -m "Add minimal reproduction for forwarder layer initialization bug"
git push origin main  # Or your branch name
```

Then share the link with maintainers:
`https://github.com/am-sokolov/livekit-agent-sdk-go/tree/main/examples/minimal-repro`

## Questions for Maintainers

Based on their feedback, you can ask:

1. **Where should layer initialization happen?**
   - "You mentioned the stream allocator should set layers. Could you point me to where that initialization happens? I may have missed a code path."

2. **Is there a synchronization mechanism I'm missing?**
   - "Is there a specific ordering guarantee I should rely on instead of checking for invalid layers?"

3. **What logs would help?**
   - "What specific log output would help you diagnose this further?"

## Next Steps

1. Run both tests (unpatched and patched)
2. Capture outputs and logs
3. Upload evidence to GitHub issue or share via Slack
4. Update RESPONSE_TO_MAINTAINERS.md with actual logs
5. Submit as evidence to maintainers
