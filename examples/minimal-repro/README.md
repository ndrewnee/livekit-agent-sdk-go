# Minimal Reproduction: Forwarder Layer Initialization Bug

This is a minimal, standalone program that reproduces the forwarder layer initialization bug in LiveKit server.

## The Bug

When an agent uses manual subscription (`AutoSubscribe: false`) and immediately calls `SetVideoQuality(HIGH)` after subscribing, the agent receives 0 video packets despite the publisher actively sending them.

## How to Run

### 1. Start LiveKit Server

```bash
# Start unpatched LiveKit server
livekit-server --dev
```

### 2. Run the Test

```bash
cd examples/minimal-repro
go mod download
go run main.go
```

### Expected Output (UNPATCHED server)

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

=== RESULTS ===
Publisher sent: 300 packets
Agent received: 0 packets

❌ BUG REPRODUCED: Agent received 0 packets despite publisher sending
This indicates the forwarder layer initialization bug.
Run with PATCHED server to see packets received.
```

### Expected Output (PATCHED server)

```
=== RESULTS ===
Publisher sent: 300 packets
Agent received: 300 packets

✅ Working correctly: Agent receiving packets
Server has the fix applied or different code path triggered.
```

## Root Cause

1. Agent subscribes with manual subscription (`AutoSubscribe: false`)
2. Agent immediately calls `SetVideoQuality(HIGH)`
3. Server calls `SetMaxSpatialLayer()` on the forwarder
4. **BUG**: Function only sets max layer, leaves target/current as `InvalidLayerSpatial`
5. Forwarder cannot make forwarding decisions without valid target layer
6. Result: 0 packets forwarded to agent

## The Fix

In `pkg/sfu/forwarder.go`, initialize target and current layers when max is set:

```go
func (f *Forwarder) SetMaxSpatialLayer(spatialLayer int32) (bool, buffer.VideoLayer) {
    f.vls.SetMaxSpatial(spatialLayer)
    newMax := f.vls.GetMax()

    // Initialize target if invalid
    if f.vls.GetTarget().Spatial == buffer.InvalidLayerSpatial {
        f.vls.SetTarget(newMax)
        if f.vls.GetCurrent().Spatial == buffer.InvalidLayerSpatial {
            f.vls.SetCurrent(newMax)
        }
    }

    return true, newMax
}
```

## Why Python SDK Doesn't Hit This

The Python agents SDK uses `auto_subscribe=true` by default, which follows a different code path that doesn't call `SetVideoQuality()` explicitly.

## Comparison

| Scenario | Auto-Subscribe | SetVideoQuality | Result |
|----------|----------------|-----------------|---------|
| Standard P2P | true | No | ✅ Works |
| Python Agent | true | No | ✅ Works |
| Go Agent (manual) | false | Yes (immediate) | ❌ Broken |
| Go Agent (delayed) | false | Yes (after delay) | ✅ Works |

The bug only affects manual subscription with immediate explicit video quality requests.
