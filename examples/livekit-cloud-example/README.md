# LiveKit Cloud SDK Comprehensive Example

This example demonstrates all major features of the LiveKit Go SDK when used with LiveKit Cloud.

## Features Demonstrated

### 1. **Room Management**
- Creating rooms with configuration
- Listing rooms
- Updating room metadata
- Deleting rooms

### 2. **Participants & Tracks**
- Connecting participants
- Publishing audio/video tracks
- Subscribing to remote tracks
- Managing participant metadata
- Track publication options

### 3. **Data Channels**
- Reliable data messages
- Lossy (real-time) data messages
- Broadcasting to all participants
- Targeted messages

### 4. **Recording & Egress**
- Room composite recording setup
- Track recording configuration
- Stream output (RTMP/HLS)

### 5. **Webhooks**
- Event types and handling
- Webhook verification
- Processing room and participant events

### 6. **Access Control**
- Token generation with permissions
- Publish/subscribe restrictions
- Room-level permissions
- Participant capabilities

### 7. **Metadata**
- Room metadata for configuration
- Participant metadata for user info
- Dynamic metadata updates
- Structured JSON metadata

### 8. **Connection Quality**
- Monitoring connection state
- Quality level detection
- Adaptive quality strategies
- Reconnection handling

## Prerequisites

1. **LiveKit Cloud Account**
   - Sign up at https://livekit.io
   - Create a project
   - Get your API credentials

2. **Go 1.19+**
   - Install from https://golang.org

3. **Environment Setup**
   - Copy `.env.example` to `.env`
   - Fill in your LiveKit Cloud credentials

## Quick Start

1. **Clone and setup:**
```bash
cd livekit-cloud-example
cp .env.example .env
# Edit .env with your credentials
```

2. **Install dependencies:**
```bash
go mod init livekit-cloud-example
go get github.com/livekit/server-sdk-go/v2
go get github.com/livekit/protocol
go get github.com/joho/godotenv
go get github.com/pion/webrtc/v4
```

3. **Run the example:**
```bash
./full-test.sh
```

## What Happens

When you run the example, it will:

1. ✅ Validate your LiveKit Cloud connection
2. 📝 Create demo rooms
3. 👤 Connect participants
4. 📤 Publish tracks
5. 💬 Send data messages
6. 🔐 Demonstrate access control
7. 📊 Show metrics and statistics
8. 🧹 Clean up all resources

## Configuration Options

### Environment Variables

| Variable | Description | Required |
|----------|-------------|----------|
| `LIVEKIT_URL` | Your LiveKit Cloud URL | Yes |
| `LIVEKIT_API_KEY` | API Key from dashboard | Yes |
| `LIVEKIT_API_SECRET` | API Secret from dashboard | Yes |

### Room Options

```go
room, err := roomClient.CreateRoom(ctx, &livekit.CreateRoomRequest{
    Name:            "my-room",
    EmptyTimeout:    300,        // Seconds before empty room closes
    MaxParticipants: 10,         // Maximum participants allowed
    Metadata:        "{}",       // JSON metadata
    NodeId:          "",         // Auto-select node
    MinPlayoutDelay: 0,          // Minimum playout delay (ms)
    MaxPlayoutDelay: 0,          // Maximum playout delay (ms)
})
```

### Token Permissions

```go
grant := &auth.VideoGrant{
    RoomJoin:     true,           // Can join room
    Room:         "room-name",    // Specific room or "*" for any
    CanPublish:   &canPublish,    // Can publish tracks
    CanSubscribe: &canSubscribe,  // Can subscribe to tracks
    CanPublishData: &canPubData,  // Can send data messages
    Hidden:       false,           // Hidden from others
    Recorder:     false,           // Is recorder
}
```

## SDK Features Reference

### Connecting to a Room

```go
room, err := lksdk.ConnectToRoomWithToken(url, token, &lksdk.RoomCallback{
    OnTrackSubscribed: func(track *webrtc.TrackRemote, ...) {
        // Handle subscribed track
    },
    OnTrackPublished: func(pub *lksdk.LocalTrackPublication, ...) {
        // Handle published track
    },
    OnParticipantConnected: func(p *lksdk.RemoteParticipant) {
        // Handle new participant
    },
    OnDataReceived: func(data []byte, ...) {
        // Handle data message
    },
})
```

### Publishing Tracks

```go
// Audio track
audioTrack, _ := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
    MimeType:  webrtc.MimeTypeOpus,
    ClockRate: 48000,
    Channels:  2,
})

pub, _ := room.LocalParticipant.PublishTrack(audioTrack, 
    &lksdk.TrackPublicationOptions{
        Name:   "microphone",
        Source: livekit.TrackSource_MICROPHONE,
    })
```

### Sending Data Messages

```go
// Reliable data
room.LocalParticipant.PublishDataPacket(&lksdk.DataPacket{
    Payload: []byte("message"),
    Kind:    lksdk.KindReliable,
})

// Lossy data (real-time)
room.LocalParticipant.PublishDataPacket(&lksdk.DataPacket{
    Payload: []byte("update"),
    Kind:    lksdk.KindLossy,
})
```

## Monitoring & Debugging

### Connection States

- `Connecting` - Initial connection
- `Connected` - Successfully connected
- `Reconnecting` - Temporary disconnection
- `Disconnected` - Fully disconnected

### Quality Levels

- `EXCELLENT` - No issues
- `GOOD` - Minor packet loss
- `POOR` - Noticeable degradation  
- `LOST` - Connection lost

### Metrics

The example tracks:
- Rooms created/deleted
- Participants joined
- Tracks published/subscribed
- Data messages sent/received
- Bytes transmitted
- Connection errors

## Production Considerations

1. **Error Handling**
   - Implement retry logic
   - Handle network interruptions
   - Log errors appropriately

2. **Security**
   - Never expose API secrets
   - Use short-lived tokens
   - Validate webhook signatures

3. **Performance**
   - Monitor bandwidth usage
   - Implement adaptive bitrate
   - Use appropriate codecs

4. **Scaling**
   - Use region selection
   - Implement load balancing
   - Monitor room capacity

## Troubleshooting

### Connection Failed
- Check credentials in `.env`
- Verify network connectivity
- Check firewall/proxy settings

### No Audio/Video
- Verify publish permissions
- Check codec compatibility
- Monitor track state

### High Latency
- Check network quality
- Use nearest region
- Optimize encoding settings

## Resources

- [LiveKit Documentation](https://docs.livekit.io)
- [Go SDK Reference](https://pkg.go.dev/github.com/livekit/server-sdk-go/v2)
- [LiveKit Cloud Dashboard](https://cloud.livekit.io)
- [Community Support](https://livekit.io/community)

## License

This example is provided as-is for demonstration purposes.