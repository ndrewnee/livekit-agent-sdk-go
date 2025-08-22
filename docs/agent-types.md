# Agent Types

LiveKit supports three distinct agent types, each designed for specific use cases. This guide explores each type in detail, helping you choose the right agent for your application.

## Overview

| Agent Type | Job Type | Purpose | Key Features |
|------------|----------|---------|--------------|
| Room Agent | JT_ROOM | Monitor and manage entire rooms | Access all participants, room-wide operations |
| Participant Agent | JT_PARTICIPANT | Individual participant interactions | Per-participant logic, targeted processing |
| Publisher Agent | JT_PUBLISHER | Publish media to rooms | Audio/video generation, synthetic participants |

## Room Agents

Room agents operate at the room level, processing events and data for all participants. They're ideal for services that need a complete view of room activity.

### When to Use Room Agents

- **Recording/Streaming**: Capture all room activity
- **Transcription**: Process audio from all participants  
- **Analytics**: Collect room-wide metrics
- **Moderation**: Monitor all participants for compliance
- **Coordination**: Orchestrate multi-participant interactions

### Implementation

```go
type RoomAgentHandler struct {
    // Your custom fields
    transcriptionService TranscriptionService
    analyticsCollector   AnalyticsCollector
}

func (h *RoomAgentHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    if job.Type != livekit.JobType_JT_ROOM {
        return fmt.Errorf("expected room job, got %v", job.Type)
    }
    
    log.Printf("Room agent started for room: %s", room.Name())
    
    // Set up room event handlers
    room.Callback.OnParticipantConnected = h.handleParticipantConnected
    room.Callback.OnParticipantDisconnected = h.handleParticipantDisconnected
    room.Callback.OnTrackPublished = h.handleTrackPublished
    room.Callback.OnTrackUnpublished = h.handleTrackUnpublished
    room.Callback.OnDataReceived = h.handleDataReceived
    room.Callback.OnConnectionQualityChanged = h.handleQualityChange
    
    // Process existing participants
    for _, participant := range room.GetRemoteParticipants() {
        h.processParticipant(participant)
    }
    
    // Run until context is cancelled
    <-ctx.Done()
    
    // Cleanup
    h.cleanup(room)
    return nil
}

func (h *RoomAgentHandler) handleTrackPublished(
    publication *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
) {
    if publication.Kind() == lksdk.TrackKindAudio {
        // Subscribe to audio for transcription
        if err := publication.SetSubscribed(true); err != nil {
            log.Printf("Failed to subscribe to audio: %v", err)
            return
        }
        
        track := publication.Track()
        if track != nil {
            go h.processAudioTrack(track, participant)
        }
    }
}
```

### Advanced Room Agent Features

#### 1. Selective Processing

Process only specific participants based on metadata:

```go
func (h *RoomAgentHandler) shouldProcessParticipant(p *lksdk.RemoteParticipant) bool {
    metadata := p.Metadata()
    if metadata == "" {
        return true // Process all by default
    }
    
    var meta map[string]interface{}
    if err := json.Unmarshal([]byte(metadata), &meta); err == nil {
        // Only process participants marked for recording
        if record, ok := meta["record"].(bool); ok {
            return record
        }
    }
    return true
}
```

#### 2. Resource Management

Manage resources efficiently for long-running room agents:

```go
type ResourceManagedRoomAgent struct {
    maxParticipants int
    maxDuration     time.Duration
    startTime       time.Time
}

func (a *ResourceManagedRoomAgent) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    a.startTime = time.Now()
    
    // Monitor resource usage
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    go func() {
        for {
            select {
            case <-ticker.C:
                if a.shouldTerminate(room) {
                    log.Println("Resource limits reached, terminating agent")
                    cancel() // Cancel context to trigger shutdown
                }
            case <-ctx.Done():
                return
            }
        }
    }()
    
    // ... rest of implementation
}
```

#### 3. State Synchronization

Maintain synchronized state across room events:

```go
type RoomState struct {
    mu sync.RWMutex
    participants map[string]*ParticipantInfo
    tracks       map[string]*TrackInfo
    startTime    time.Time
    eventCount   int64
}

func (s *RoomState) AddParticipant(p *lksdk.RemoteParticipant) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    s.participants[p.SID()] = &ParticipantInfo{
        SID:      p.SID(),
        Identity: p.Identity(),
        JoinedAt: time.Now(),
        Metadata: p.Metadata(),
    }
    s.eventCount++
}
```

## Participant Agents

Participant agents focus on individual participants, enabling personalized interactions and targeted processing.

### When to Use Participant Agents

- **Personal Assistants**: One agent per user
- **Language Translation**: Individual translation preferences
- **Accessibility**: Participant-specific accommodations
- **Gaming**: Player-specific NPCs or companions
- **Education**: Personalized tutoring

### Implementation

```go
type ParticipantAgentHandler struct {
    agentID string
    targetParticipantIdentity string
}

func (h *ParticipantAgentHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    if job.Type != livekit.JobType_JT_PARTICIPANT {
        return fmt.Errorf("expected participant job, got %v", job.Type)
    }
    
    // Extract target participant from job metadata
    var metadata map[string]string
    if err := json.Unmarshal([]byte(job.Metadata), &metadata); err == nil {
        h.targetParticipantIdentity = metadata["participant_identity"]
    }
    
    log.Printf("Participant agent started for: %s", h.targetParticipantIdentity)
    
    // Wait for target participant
    var targetParticipant *lksdk.RemoteParticipant
    
    room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
        if p.Identity() == h.targetParticipantIdentity {
            targetParticipant = p
            h.handleTargetParticipantConnected(p)
        }
    }
    
    room.Callback.OnParticipantDisconnected = func(p *lksdk.RemoteParticipant) {
        if p.Identity() == h.targetParticipantIdentity {
            log.Println("Target participant disconnected, ending agent")
            cancel() // End the agent
        }
    }
    
    // Check if participant is already in room
    for _, p := range room.GetRemoteParticipants() {
        if p.Identity() == h.targetParticipantIdentity {
            targetParticipant = p
            h.handleTargetParticipantConnected(p)
            break
        }
    }
    
    // Run until cancelled
    <-ctx.Done()
    return nil
}

func (h *ParticipantAgentHandler) handleTargetParticipantConnected(p *lksdk.RemoteParticipant) {
    log.Printf("Target participant connected: %s", p.Identity())
    
    // Subscribe to participant's tracks
    p.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication) {
        if pub.Kind() == lksdk.TrackKindAudio {
            pub.SetSubscribed(true)
            // Process audio for this specific participant
            go h.processParticipantAudio(pub.Track(), p)
        }
    }
    
    // Send personalized greeting
    data := map[string]interface{}{
        "type": "greeting",
        "message": fmt.Sprintf("Hello %s, I'm your personal assistant!", p.Identity()),
    }
    
    if jsonData, err := json.Marshal(data); err == nil {
        room.LocalParticipant.PublishData(jsonData, &lksdk.DataPublishOptions{
            Destination: []string{p.SID()}, // Send only to target participant
            Reliable:    true,
        })
    }
}
```

### Advanced Participant Agent Features

#### 1. Stateful Interactions

Maintain conversation context and state:

```go
type ConversationState struct {
    History      []Message
    Context      map[string]interface{}
    Preferences  UserPreferences
    LastActivity time.Time
}

type StatefulParticipantAgent struct {
    conversations map[string]*ConversationState
    mu           sync.RWMutex
}

func (a *StatefulParticipantAgent) getOrCreateConversation(participantID string) *ConversationState {
    a.mu.Lock()
    defer a.mu.Unlock()
    
    if conv, exists := a.conversations[participantID]; exists {
        conv.LastActivity = time.Now()
        return conv
    }
    
    conv := &ConversationState{
        History:      make([]Message, 0),
        Context:      make(map[string]interface{}),
        LastActivity: time.Now(),
    }
    a.conversations[participantID] = conv
    return conv
}
```

#### 2. Multi-Modal Interaction

Handle various input/output modalities:

```go
func (h *ParticipantAgentHandler) setupMultiModalHandlers(room *lksdk.Room, participant *lksdk.RemoteParticipant) {
    // Handle voice input
    participant.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication) {
        if pub.Kind() == lksdk.TrackKindAudio {
            go h.processVoiceCommands(pub.Track())
        }
    }
    
    // Handle text input via data channel
    participant.OnDataReceived = func(data []byte, params lksdk.DataReceiveParams) {
        go h.processTextCommand(data)
    }
    
    // Handle video for gesture recognition
    participant.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication) {
        if pub.Kind() == lksdk.TrackKindVideo {
            go h.processVideoForGestures(pub.Track())
        }
    }
}
```

## Publisher Agents

Publisher agents join rooms to publish media streams, acting as synthetic participants that generate audio, video, or both.

### When to Use Publisher Agents

- **AI Avatars**: Virtual participants with generated video
- **Music/Audio Playback**: Streaming audio content
- **Screen Sharing**: Automated presentations
- **Notifications**: Audio announcements
- **Interactive NPCs**: Game characters with voice

### Implementation

```go
type PublisherAgentHandler struct {
    audioTrack *webrtc.TrackLocalStaticSample
    videoTrack *webrtc.TrackLocalStaticSample
    ttsService TTSService
}

func (h *PublisherAgentHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    if job.Type != livekit.JobType_JT_PUBLISHER {
        return fmt.Errorf("expected publisher job, got %v", job.Type)
    }
    
    log.Printf("Publisher agent started for room: %s", room.Name())
    
    // Create and publish audio track
    audioTrack, err := h.createAudioTrack()
    if err != nil {
        return fmt.Errorf("failed to create audio track: %w", err)
    }
    
    audioPublication, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
        Name: "Agent Audio",
        Source: livekit.TrackSource_MICROPHONE,
    })
    if err != nil {
        return fmt.Errorf("failed to publish audio: %w", err)
    }
    
    // Create and publish video track (optional)
    videoTrack, err := h.createVideoTrack()
    if err == nil {
        videoPublication, err := room.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
            Name: "Agent Video",
            Source: livekit.TrackSource_CAMERA,
        })
        if err != nil {
            log.Printf("Failed to publish video: %v", err)
        }
    }
    
    // Set up interaction handlers
    room.Callback.OnDataReceived = h.handleDataReceived
    
    // Start media generation
    go h.generateAudioContent(ctx, audioTrack)
    if videoTrack != nil {
        go h.generateVideoContent(ctx, videoTrack)
    }
    
    // Run until cancelled
    <-ctx.Done()
    
    // Cleanup
    room.LocalParticipant.UnpublishTrack(audioPublication.SID())
    return nil
}

func (h *PublisherAgentHandler) createAudioTrack() (*webrtc.TrackLocalStaticSample, error) {
    return webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{
            MimeType:  webrtc.MimeTypeOpus,
            ClockRate: 48000,
            Channels:  2,
        },
        "audio-"+uuid.New().String(),
        "agent-audio-stream",
    )
}

func (h *PublisherAgentHandler) generateAudioContent(ctx context.Context, track *webrtc.TrackLocalStaticSample) {
    ticker := time.NewTicker(20 * time.Millisecond) // 50Hz for Opus
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Generate or fetch audio samples
            samples := h.getNextAudioSamples()
            if len(samples) > 0 {
                if err := track.WriteSample(media.Sample{
                    Data:     samples,
                    Duration: 20 * time.Millisecond,
                }); err != nil {
                    log.Printf("Failed to write audio sample: %v", err)
                }
            }
        }
    }
}
```

### Advanced Publisher Agent Features

#### 1. Interactive Voice Response

Implement conversational AI with voice:

```go
type VoiceAssistantAgent struct {
    stt          STTService
    tts          TTSService
    llm          LLMService
    audioBuffer  *AudioBuffer
    conversation *ConversationManager
}

func (a *VoiceAssistantAgent) processUserSpeech(track *webrtc.TrackRemote) {
    // Buffer incoming audio
    samples := make([]byte, 1920) // 20ms @ 48kHz
    
    for {
        _, _, err := track.Read(samples)
        if err != nil {
            return
        }
        
        a.audioBuffer.Write(samples)
        
        // Detect speech end (VAD)
        if a.audioBuffer.IsSpeechComplete() {
            // Transcribe
            text, err := a.stt.Transcribe(a.audioBuffer.GetSpeech())
            if err != nil {
                continue
            }
            
            // Generate response
            response, err := a.llm.GenerateResponse(text, a.conversation.GetContext())
            if err != nil {
                continue
            }
            
            // Convert to speech
            audioData, err := a.tts.Synthesize(response)
            if err != nil {
                continue
            }
            
            // Publish response
            a.publishAudioResponse(audioData)
        }
    }
}
```

#### 2. Dynamic Media Generation

Generate video content on-the-fly:

```go
type AvatarVideoGenerator struct {
    encoder      *VideoEncoder
    frameRate    int
    lastFrame    time.Time
}

func (g *AvatarVideoGenerator) generateVideoContent(ctx context.Context, track *webrtc.TrackLocalStaticSample) {
    frameDuration := time.Second / time.Duration(g.frameRate)
    ticker := time.NewTicker(frameDuration)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Generate next frame
            frame := g.generateAvatarFrame()
            
            // Encode frame
            encoded, err := g.encoder.EncodeFrame(frame)
            if err != nil {
                continue
            }
            
            // Write to track
            if err := track.WriteSample(media.Sample{
                Data:     encoded,
                Duration: frameDuration,
            }); err != nil {
                log.Printf("Failed to write video sample: %v", err)
            }
        }
    }
}

func (g *AvatarVideoGenerator) generateAvatarFrame() image.Image {
    // Generate avatar frame based on:
    // - Lip sync with audio
    // - Facial expressions
    // - Body movements
    // - Background changes
    
    frame := image.NewRGBA(image.Rect(0, 0, 1280, 720))
    // ... frame generation logic
    return frame
}
```

#### 3. Media Synchronization

Synchronize multiple media streams:

```go
type SynchronizedPublisher struct {
    audioTrack   *webrtc.TrackLocalStaticSample
    videoTrack   *webrtc.TrackLocalStaticSample
    syncClock    *MediaClock
    audioBuffer  chan *AudioFrame
    videoBuffer  chan *VideoFrame
}

func (p *SynchronizedPublisher) startSynchronizedPlayback(ctx context.Context) {
    p.syncClock.Start()
    
    // Audio playback goroutine
    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            case frame := <-p.audioBuffer:
                // Wait for correct timestamp
                p.syncClock.WaitUntil(frame.Timestamp)
                p.audioTrack.WriteSample(media.Sample{
                    Data:     frame.Data,
                    Duration: frame.Duration,
                })
            }
        }
    }()
    
    // Video playback goroutine
    go func() {
        for {
            select {
            case <-ctx.Done():
                return
            case frame := <-p.videoBuffer:
                // Wait for correct timestamp
                p.syncClock.WaitUntil(frame.Timestamp)
                p.videoTrack.WriteSample(media.Sample{
                    Data:     frame.Data,
                    Duration: frame.Duration,
                })
            }
        }
    }()
}
```

## Choosing the Right Agent Type

### Decision Matrix

| Criteria | Room Agent | Participant Agent | Publisher Agent |
|----------|------------|-------------------|-----------------|
| Scope | Entire room | Single participant | Media generation |
| Use Case | Recording, analytics | Personal assistant | AI avatar, TTS |
| Resource Usage | Higher (all participants) | Lower (one participant) | Variable (media processing) |
| Scalability | One per room | One per participant | Based on media complexity |
| State Management | Room-wide state | Per-participant state | Media generation state |

### Combining Agent Types

You can deploy multiple agent types together:

```go
// Deploy room agent for recording
roomWorker := agent.NewWorker(url, key, secret, roomHandler, agent.WorkerOptions{
    AgentName: "recorder",
    JobTypes: []livekit.JobType{livekit.JobType_JT_ROOM},
})

// Deploy participant agents for personal assistance  
participantWorker := agent.NewWorker(url, key, secret, assistantHandler, agent.WorkerOptions{
    AgentName: "assistant",
    JobTypes: []livekit.JobType{livekit.JobType_JT_PARTICIPANT},
})

// Deploy publisher agent for announcements
publisherWorker := agent.NewWorker(url, key, secret, announcerHandler, agent.WorkerOptions{
    AgentName: "announcer",
    JobTypes: []livekit.JobType{livekit.JobType_JT_PUBLISHER},
})
```

## Best Practices

### 1. Agent Lifecycle Management

```go
type LifecycleAwareAgent struct {
    onStart    func()
    onStop     func()
    onError    func(error)
    healthCheck func() error
}

func (a *LifecycleAwareAgent) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Initialize
    a.onStart()
    defer a.onStop()
    
    // Health monitoring
    healthTicker := time.NewTicker(30 * time.Second)
    defer healthTicker.Stop()
    
    errChan := make(chan error, 1)
    
    go func() {
        for {
            select {
            case <-healthTicker.C:
                if err := a.healthCheck(); err != nil {
                    errChan <- err
                    return
                }
            case <-ctx.Done():
                return
            }
        }
    }()
    
    // Main logic
    select {
    case err := <-errChan:
        a.onError(err)
        return err
    case <-ctx.Done():
        return nil
    }
}
```

### 2. Resource Optimization

```go
// For Room Agents - selective subscription
func optimizeRoomAgent(room *lksdk.Room, maxSubscriptions int) {
    subscriptions := 0
    
    room.Callback.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication, p *lksdk.RemoteParticipant) {
        if subscriptions >= maxSubscriptions {
            log.Printf("Subscription limit reached, skipping track")
            return
        }
        
        // Only subscribe to needed tracks
        if shouldSubscribe(pub, p) {
            pub.SetSubscribed(true)
            subscriptions++
        }
    }
}

// For Publisher Agents - adaptive quality
func adaptivePublisher(track *webrtc.TrackLocalStaticSample, connectionQuality livekit.ConnectionQuality) {
    switch connectionQuality {
    case livekit.ConnectionQuality_POOR:
        // Reduce bitrate/quality
        setLowQualityMode(track)
    case livekit.ConnectionQuality_GOOD:
        // Standard quality
        setStandardQualityMode(track)
    case livekit.ConnectionQuality_EXCELLENT:
        // High quality
        setHighQualityMode(track)
    }
}
```

### 3. Error Recovery

```go
type ResilientAgent struct {
    maxRetries   int
    retryDelay   time.Duration
    lastError    error
    errorCount   int
}

func (a *ResilientAgent) handleError(err error, operation string) error {
    a.errorCount++
    a.lastError = err
    
    if a.errorCount > a.maxRetries {
        return fmt.Errorf("max retries exceeded for %s: %w", operation, err)
    }
    
    log.Printf("Error in %s (attempt %d/%d): %v", operation, a.errorCount, a.maxRetries, err)
    time.Sleep(a.retryDelay * time.Duration(a.errorCount))
    
    return nil // Continue with retry
}
```

## Next Steps

- Learn about [Job Handling](job-handling.md) patterns
- Explore [Advanced Features](advanced-features.md)
- See [Examples](examples/README.md) for complete implementations
- Review [API Reference](api-reference.md) for detailed documentation