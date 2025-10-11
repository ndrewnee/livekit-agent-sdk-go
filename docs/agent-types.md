# Agent Types

LiveKit supports three distinct agent types, each designed for specific use cases. This guide explores each type in detail, helping you choose the right agent for your application.

## Overview

| Agent Type | Job Type       | Purpose | Key Features |
|------------|----------------|---------|--------------|
| Room Agent | JT_ROOM        | Monitor and manage entire rooms | Access all participants, room-wide operations |
| Participant Agent | JT_PARTICIPANT | Individual participant interactions | Per-participant logic, targeted processing |
| Publisher Agent | v              | Publish media to rooms | Audio/video generation, synthetic participants |

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

func (h *RoomAgentHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
    if job.Type != livekit.JobType_JT_ROOM {
        return false, nil // Only accept room jobs
    }
    
    return true, &agent.JobMetadata{
        ParticipantIdentity: "room-agent",
        ParticipantName:     "Room Agent",
        ParticipantMetadata: `{"agent_type": "room_monitor"}`,
    }
}

func (h *RoomAgentHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    log.Printf("Room agent started for room: %s", room.Name())
    
    // Since we can't modify callbacks after connection, use polling pattern
    go h.monitorRoom(ctx, room)
    
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

func (h *RoomAgentHandler) OnJobTerminated(ctx context.Context, jobID string) {
    log.Printf("Room agent job terminated: %s", jobID)
    // Perform any additional cleanup
}

func (h *RoomAgentHandler) monitorRoom(ctx context.Context, room *lksdk.Room) {
    ticker := time.NewTicker(500 * time.Millisecond)
    defer ticker.Stop()
    
    knownParticipants := make(map[string]*lksdk.RemoteParticipant)
    knownTracks := make(map[string]bool)
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Check for participant changes
            currentParticipants := room.GetRemoteParticipants()
            
            // New participants
            for _, p := range currentParticipants {
                if _, exists := knownParticipants[p.SID()]; !exists {
                    knownParticipants[p.SID()] = p
                    h.handleParticipantConnected(p)
                }
                
                // Check for new tracks
                for _, pub := range p.TrackPublications() {
                    trackKey := fmt.Sprintf("%s-%s", p.SID(), pub.TrackPublication().SID())
                    if !knownTracks[trackKey] {
                        knownTracks[trackKey] = true
                        h.handleTrackPublished(pub.TrackPublication(), p)
                    }
                }
            }
            
            // Disconnected participants
            for sid, p := range knownParticipants {
                found := false
                for _, current := range currentParticipants {
                    if current.SID() == sid {
                        found = true
                        break
                    }
                }
                if !found {
                    delete(knownParticipants, sid)
                    h.handleParticipantDisconnected(p)
                }
            }
        }
    }
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

func (a *ResourceManagedRoomAgent) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    a.startTime = time.Now()
    
    // Create cancellable context
    jobCtx, cancel := context.WithCancel(ctx)
    
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
            case <-jobCtx.Done():
                return
            }
        }
    }()
    
    // Wait for context cancellation
    <-jobCtx.Done()
    return nil
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

### Implementation with UniversalWorker

```go
// Using SimpleUniversalHandler for participant agents
var targetIdentity string // Store target participant identity

handler := &agent.SimpleUniversalHandler{
    JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
        if job.Type != livekit.JobType_JT_PARTICIPANT {
            return false, nil
        }
        
        return true, &agent.JobMetadata{
            ParticipantIdentity: "participant-agent-" + job.Id,
            ParticipantName:     "Participant Agent",
            ParticipantMetadata: `{"agent_type": "participant_monitor"}`,
        }
    },
    
    JobAssignedFunc: func(ctx context.Context, jobCtx *agent.JobContext) error {
        // Extract target participant from job metadata
        var metadata map[string]string
        if err := json.Unmarshal([]byte(jobCtx.Job.Metadata), &metadata); err == nil {
            targetIdentity = metadata["participant_identity"]
        }
        
        log.Printf("Participant agent started for: %s", targetIdentity)
        
        // Check if participant is already in room
        for _, p := range jobCtx.Room.GetRemoteParticipants() {
            if p.Identity() == targetIdentity {
                handleTargetParticipantConnected(p)
                break
            }
        }
        
        // Run until cancelled
        <-ctx.Done()
        return nil
    },
    
    JobTerminatedFunc: func(ctx context.Context, jobID string) {
        log.Printf("Participant agent job terminated: %s", jobID)
    },
    
    // Direct event handling for participants
    ParticipantJoinedFunc: func(ctx context.Context, p *lksdk.RemoteParticipant) {
        if p.Identity() == targetIdentity {
            handleTargetParticipantConnected(p)
        }
    },
    
    ParticipantLeftFunc: func(ctx context.Context, p *lksdk.RemoteParticipant) {
        if p.Identity() == targetIdentity {
            log.Printf("Target participant disconnected: %s", p.Identity())
        }
    },
    
    TrackPublishedFunc: func(ctx context.Context, pub *lksdk.RemoteTrackPublication, p *lksdk.RemoteParticipant) {
        if p.Identity() == targetIdentity {
            log.Printf("Target participant published track: %s", pub.SID())
            // Process track from target participant
        }
    },
}

// Create worker for participant agents
worker := agent.NewUniversalWorker(
    "ws://localhost:7880",
    "api-key",
    "api-secret",
    handler,
    agent.WorkerOptions{
        AgentName: "participant-agent",
        JobType:   livekit.JobType_JT_PARTICIPANT,
        MaxJobs:   20, // Handle many participants
    },
)

func handleTargetParticipantConnected(p *lksdk.RemoteParticipant) {
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

### Implementation with UniversalWorker

```go
// Using SimpleUniversalHandler for publisher agents
var audioTrack *webrtc.TrackLocalStaticSample
var videoTrack *webrtc.TrackLocalStaticSample

handler := &agent.SimpleUniversalHandler{
    JobRequestFunc: func(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
        if job.Type != livekit.JobType_JT_PUBLISHER {
            return false, nil
        }
        
        return true, &agent.JobMetadata{
            ParticipantIdentity: "publisher-agent",
            ParticipantName:     "Media Publisher",
            ParticipantMetadata: `{"agent_type": "publisher"}`,
        }
    },
    
    JobAssignedFunc: func(ctx context.Context, jobCtx *agent.JobContext) error {
        log.Printf("Publisher agent started for room: %s", jobCtx.Room.Name())
        
        // Create and publish audio track
        var err error
        audioTrack, err = createAudioTrack()
        if err != nil {
            return fmt.Errorf("failed to create audio track: %w", err)
        }
        
        audioPublication, err := jobCtx.Room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
            Name: "Agent Audio",
            Source: livekit.TrackSource_MICROPHONE,
        })
        if err != nil {
            return fmt.Errorf("failed to publish audio: %w", err)
        }
        
        // Create and publish video track (optional)
        videoTrack, err = createVideoTrack()
        if err == nil {
            _, err := jobCtx.Room.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
                Name: "Agent Video",
                Source: livekit.TrackSource_CAMERA,
            })
            if err != nil {
                log.Printf("Failed to publish video: %v", err)
            }
        }
        
        // Start media generation
        go generateAudioContent(ctx, audioTrack)
        if videoTrack != nil {
            go generateVideoContent(ctx, videoTrack)
        }
        
        // Run until cancelled
        <-ctx.Done()
        
        // Cleanup
        jobCtx.Room.LocalParticipant.UnpublishTrack(audioPublication.SID())
        return nil
    },
    
    JobTerminatedFunc: func(ctx context.Context, jobID string) {
        log.Printf("Publisher job terminated: %s", jobID)
    },
    
    // Handle data messages for controlling publishing
    DataReceivedFunc: func(ctx context.Context, data []byte, p *lksdk.RemoteParticipant) {
        // Process control messages
        var msg map[string]interface{}
        if err := json.Unmarshal(data, &msg); err == nil {
            // Handle commands like start/stop publishing, change content, etc.
            log.Printf("Received command from %s: %v", p.Identity(), msg)
        }
    },
}

// Create worker for publisher agents
worker := agent.NewUniversalWorker(
    "ws://localhost:7880",
    "api-key",
    "api-secret",
    handler,
    agent.WorkerOptions{
        AgentName: "publisher-agent",
        JobType:   livekit.JobType_JT_PUBLISHER,
        MaxJobs:   10,
    },
)

func createAudioTrack() (*webrtc.TrackLocalStaticSample, error) {
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

func generateAudioContent(ctx context.Context, track *webrtc.TrackLocalStaticSample) {
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
roomWorker := agent.NewUniversalWorker(url, key, secret, roomHandler, agent.WorkerOptions{
    AgentName: "recorder",
    JobType:   livekit.JobType_JT_ROOM,
    MaxJobs:   5,
})

// Deploy participant agents for personal assistance  
participantWorker := agent.NewUniversalWorker(url, key, secret, assistantHandler, agent.WorkerOptions{
    AgentName: "assistant",
    JobType:   livekit.JobType_JT_PARTICIPANT,
    MaxJobs:   20,
})

// Deploy publisher agent for announcements
publisherWorker := agent.NewUniversalWorker(url, key, secret, announcerHandler, agent.WorkerOptions{
    AgentName: "announcer",
    JobType:   livekit.JobType_JT_PUBLISHER,
    MaxJobs:   10,
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

func (a *LifecycleAwareAgent) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
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
    
    // Monitor tracks using polling pattern
    go func() {
        ticker := time.NewTicker(500 * time.Millisecond)
        defer ticker.Stop()
        
        knownTracks := make(map[string]bool)
        
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                for _, p := range room.GetRemoteParticipants() {
                    for _, pub := range p.TrackPublications() {
                        trackID := pub.TrackPublication().SID()
                        if !knownTracks[trackID] {
                            knownTracks[trackID] = true
                            
                            if subscriptions >= maxSubscriptions {
                                log.Printf("Subscription limit reached, skipping track")
                                continue
                            }
                            
                            // Only subscribe to needed tracks
                            if shouldSubscribe(pub.TrackPublication(), p) {
                                pub.TrackPublication().SetSubscribed(true)
                                subscriptions++
                            }
                        }
                    }
                }
            }
        }
    }()
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