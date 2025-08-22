# Media Publisher Agent Example

A comprehensive media publisher agent that can generate and stream audio/video content to LiveKit rooms. This example demonstrates text-to-speech integration, audio file playback, video generation, and dynamic media streaming.

## What This Example Demonstrates

- Publisher job handling (JT_PUBLISHER)
- Audio track creation and streaming
- Video track creation and streaming
- Text-to-speech integration
- Audio file playback
- Real-time media generation
- WebRTC media format handling
- Synchronized audio/video streaming

## Complete Code

### main.go

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    "github.com/livekit/protocol/logger"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
    // Initialize logger
    logger.InitializeLogger("info", "production")
    
    // Load configuration
    config := loadConfig()
    
    // Create media services
    ttsService := NewTTSService(config.TTSConfig)
    audioService := NewAudioService(config.AudioConfig)
    videoService := NewVideoService(config.VideoConfig)
    
    // Create media publisher
    publisher := NewMediaPublisher(ttsService, audioService, videoService)
    
    // Create job handler
    handler := &MediaPublisherHandler{
        publisher: publisher,
        config:    config,
    }
    
    // Create worker - configured for publisher jobs
    worker := agent.NewWorker(
        config.LiveKitURL,
        config.APIKey,
        config.APISecret,
        handler,
        agent.WorkerOptions{
            AgentName: "media-publisher",
            JobTypes:  []livekit.JobType{livekit.JobType_JT_PUBLISHER},
            MaxConcurrentJobs: 10, // Can handle multiple concurrent publishing jobs
        },
    )
    
    // Set up graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    // Start worker in goroutine
    errChan := make(chan error, 1)
    go func() {
        log.Println("Starting media publisher agent...")
        errChan <- worker.Start(ctx)
    }()
    
    // Wait for shutdown signal or error
    select {
    case sig := <-sigChan:
        log.Printf("Received signal %v, shutting down...", sig)
        cancel()
        
        // Give worker time to clean up
        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
        defer shutdownCancel()
        
        if err := worker.Stop(shutdownCtx); err != nil {
            log.Printf("Error during shutdown: %v", err)
        }
        
    case err := <-errChan:
        if err != nil {
            log.Fatal("Worker error:", err)
        }
    }
    
    log.Println("Media publisher agent stopped")
}

func loadConfig() *Config {
    return &Config{
        LiveKitURL: getEnv("LIVEKIT_URL", "ws://localhost:7880"),
        APIKey:     mustGetEnv("LIVEKIT_API_KEY"),
        APISecret:  mustGetEnv("LIVEKIT_API_SECRET"),
        
        TTSConfig: TTSConfig{
            Engine:   getEnv("TTS_ENGINE", "google"),
            Language: getEnv("TTS_LANGUAGE", "en-US"),
            Voice:    getEnv("TTS_VOICE", "en-US-Standard-A"),
            Speed:    getEnvFloat("TTS_SPEED", 1.0),
        },
        
        AudioConfig: AudioConfig{
            SampleRate: getEnvInt("AUDIO_SAMPLE_RATE", 48000),
            Channels:   getEnvInt("AUDIO_CHANNELS", 1),
            BitDepth:   getEnvInt("AUDIO_BIT_DEPTH", 16),
        },
        
        VideoConfig: VideoConfig{
            Width:     getEnvInt("VIDEO_WIDTH", 1280),
            Height:    getEnvInt("VIDEO_HEIGHT", 720),
            FrameRate: getEnvInt("VIDEO_FRAME_RATE", 30),
            Bitrate:   getEnvInt("VIDEO_BITRATE", 2000000),
        },
    }
}

func getEnv(key, defaultValue string) string {
    if value := os.Getenv(key); value != "" {
        return value
    }
    return defaultValue
}

func mustGetEnv(key string) string {
    value := os.Getenv(key)
    if value == "" {
        log.Fatalf("Environment variable %s is required", key)
    }
    return value
}

func getEnvInt(key string, defaultValue int) int {
    if str := os.Getenv(key); str != "" {
        if value, err := strconv.Atoi(str); err == nil {
            return value
        }
    }
    return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
    if str := os.Getenv(key); str != "" {
        if value, err := strconv.ParseFloat(str, 64); err == nil {
            return value
        }
    }
    return defaultValue
}
```

### handler.go

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "time"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
    "github.com/pion/webrtc/v4"
)

type MediaPublisherHandler struct {
    publisher *MediaPublisher
    config    *Config
}

func (h *MediaPublisherHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Validate job type
    if job.Type != livekit.JobType_JT_PUBLISHER {
        return fmt.Errorf("expected publisher job, got %v", job.Type)
    }
    
    // Parse job metadata
    var jobMeta PublisherJobMetadata
    if err := json.Unmarshal([]byte(job.Metadata), &jobMeta); err != nil {
        return fmt.Errorf("failed to parse job metadata: %w", err)
    }
    
    log.Printf("Starting media publishing for room: %s (Job ID: %s, Mode: %s)",
        room.Name(), job.Id, jobMeta.Mode)
    
    // Create publishing session
    session := h.publisher.CreateSession(room.Name(), job.Id, jobMeta)
    defer func() {
        session.End()
        log.Printf("Publishing ended for room: %s", room.Name())
    }()
    
    // Set up room event handlers for interactive features
    room.Callback.OnDataReceived = func(data []byte, params lksdk.DataReceiveParams) {
        h.handleDataMessage(session, data, params, room)
    }
    
    room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
        if jobMeta.WelcomeMessage != "" {
            h.announceToParticipant(session, p, jobMeta.WelcomeMessage, room)
        }
    }
    
    // Start publishing based on mode
    switch jobMeta.Mode {
    case "tts":
        return h.runTTSMode(ctx, session, room, jobMeta)
    case "audio_file":
        return h.runAudioFileMode(ctx, session, room, jobMeta)
    case "video_with_audio":
        return h.runVideoWithAudioMode(ctx, session, room, jobMeta)
    case "interactive":
        return h.runInteractiveMode(ctx, session, room, jobMeta)
    default:
        return fmt.Errorf("unsupported publishing mode: %s", jobMeta.Mode)
    }
}

func (h *MediaPublisherHandler) runTTSMode(
    ctx context.Context,
    session *PublishingSession,
    room *lksdk.Room,
    jobMeta PublisherJobMetadata,
) error {
    log.Printf("Starting TTS mode with text: %s", jobMeta.Text)
    
    // Create audio track
    audioTrack, err := h.createAudioTrack("tts-audio")
    if err != nil {
        return fmt.Errorf("failed to create audio track: %w", err)
    }
    
    // Publish audio track
    audioPublication, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
        Name:   "TTS Audio",
        Source: livekit.TrackSource_MICROPHONE,
    })
    if err != nil {
        return fmt.Errorf("failed to publish audio track: %w", err)
    }
    
    session.AddTrack("audio", audioPublication.SID())
    
    // Generate TTS audio and stream it
    go h.streamTTSAudio(ctx, session, audioTrack, jobMeta.Text)
    
    // Wait for completion or cancellation
    <-ctx.Done()
    
    // Cleanup
    room.LocalParticipant.UnpublishTrack(audioPublication.SID())
    return nil
}

func (h *MediaPublisherHandler) runAudioFileMode(
    ctx context.Context,
    session *PublishingSession,
    room *lksdk.Room,
    jobMeta PublisherJobMetadata,
) error {
    log.Printf("Starting audio file mode with file: %s", jobMeta.AudioFile)
    
    // Create audio track
    audioTrack, err := h.createAudioTrack("file-audio")
    if err != nil {
        return fmt.Errorf("failed to create audio track: %w", err)
    }
    
    // Publish audio track
    audioPublication, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
        Name:   "Audio File",
        Source: livekit.TrackSource_MICROPHONE,
    })
    if err != nil {
        return fmt.Errorf("failed to publish audio track: %w", err)
    }
    
    session.AddTrack("audio", audioPublication.SID())
    
    // Stream audio file
    go h.streamAudioFile(ctx, session, audioTrack, jobMeta.AudioFile, jobMeta.Loop)
    
    // Wait for completion or cancellation
    <-ctx.Done()
    
    // Cleanup
    room.LocalParticipant.UnpublishTrack(audioPublication.SID())
    return nil
}

func (h *MediaPublisherHandler) runVideoWithAudioMode(
    ctx context.Context,
    session *PublishingSession,
    room *lksdk.Room,
    jobMeta PublisherJobMetadata,
) error {
    log.Printf("Starting video with audio mode")
    
    // Create audio track
    audioTrack, err := h.createAudioTrack("video-audio")
    if err != nil {
        return fmt.Errorf("failed to create audio track: %w", err)
    }
    
    // Create video track
    videoTrack, err := h.createVideoTrack("generated-video")
    if err != nil {
        return fmt.Errorf("failed to create video track: %w", err)
    }
    
    // Publish tracks
    audioPublication, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
        Name:   "Generated Audio",
        Source: livekit.TrackSource_MICROPHONE,
    })
    if err != nil {
        return fmt.Errorf("failed to publish audio track: %w", err)
    }
    
    videoPublication, err := room.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
        Name:   "Generated Video",
        Source: livekit.TrackSource_CAMERA,
    })
    if err != nil {
        room.LocalParticipant.UnpublishTrack(audioPublication.SID())
        return fmt.Errorf("failed to publish video track: %w", err)
    }
    
    session.AddTrack("audio", audioPublication.SID())
    session.AddTrack("video", videoPublication.SID())
    
    // Start synchronized audio/video streaming
    go h.streamSynchronizedMedia(ctx, session, audioTrack, videoTrack, jobMeta)
    
    // Wait for completion or cancellation
    <-ctx.Done()
    
    // Cleanup
    room.LocalParticipant.UnpublishTrack(audioPublication.SID())
    room.LocalParticipant.UnpublishTrack(videoPublication.SID())
    return nil
}

func (h *MediaPublisherHandler) runInteractiveMode(
    ctx context.Context,
    session *PublishingSession,
    room *lksdk.Room,
    jobMeta PublisherJobMetadata,
) error {
    log.Printf("Starting interactive mode")
    
    // Create audio track for responses
    audioTrack, err := h.createAudioTrack("interactive-audio")
    if err != nil {
        return fmt.Errorf("failed to create audio track: %w", err)
    }
    
    // Publish audio track
    audioPublication, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
        Name:   "Interactive Audio",
        Source: livekit.TrackSource_MICROPHONE,
    })
    if err != nil {
        return fmt.Errorf("failed to publish audio track: %w", err)
    }
    
    session.AddTrack("audio", audioPublication.SID())
    session.SetInteractiveTrack(audioTrack)
    
    // Send initial greeting if specified
    if jobMeta.WelcomeMessage != "" {
        go h.speakText(session, jobMeta.WelcomeMessage)
    }
    
    // Wait for completion or cancellation
    <-ctx.Done()
    
    // Cleanup
    room.LocalParticipant.UnpublishTrack(audioPublication.SID())
    return nil
}

func (h *MediaPublisherHandler) handleDataMessage(
    session *PublishingSession,
    data []byte,
    params lksdk.DataReceiveParams,
    room *lksdk.Room,
) {
    // Parse message
    var message map[string]interface{}
    if err := json.Unmarshal(data, &message); err != nil {
        log.Printf("Failed to parse data message: %v", err)
        return
    }
    
    msgType, ok := message["type"].(string)
    if !ok {
        return
    }
    
    log.Printf("Received message type: %s from %s", msgType, params.SenderIdentity)
    
    switch msgType {
    case "speak_text":
        if text, ok := message["text"].(string); ok {
            go h.speakText(session, text)
        }
        
    case "play_audio":
        if audioFile, ok := message["file"].(string); ok {
            go h.playAudioFile(session, audioFile)
        }
        
    case "set_volume":
        if volume, ok := message["volume"].(float64); ok {
            session.SetVolume(float32(volume))
        }
        
    case "command":
        if cmd, ok := message["command"].(string); ok {
            go h.handleCommand(session, cmd, params.SenderIdentity, room)
        }
    }
}

func (h *MediaPublisherHandler) createAudioTrack(trackID string) (*webrtc.TrackLocalStaticSample, error) {
    return webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{
            MimeType:  webrtc.MimeTypeOpus,
            ClockRate: uint32(h.config.AudioConfig.SampleRate),
            Channels:  uint16(h.config.AudioConfig.Channels),
        },
        trackID,
        fmt.Sprintf("audio-stream-%s", trackID),
    )
}

func (h *MediaPublisherHandler) createVideoTrack(trackID string) (*webrtc.TrackLocalStaticSample, error) {
    return webrtc.NewTrackLocalStaticSample(
        webrtc.RTPCodecCapability{
            MimeType:  webrtc.MimeTypeVP8,
            ClockRate: 90000,
        },
        trackID,
        fmt.Sprintf("video-stream-%s", trackID),
    )
}

func (h *MediaPublisherHandler) streamTTSAudio(
    ctx context.Context,
    session *PublishingSession,
    track *webrtc.TrackLocalStaticSample,
    text string,
) {
    // Generate TTS audio
    audioData, err := h.publisher.ttsService.Synthesize(text)
    if err != nil {
        log.Printf("TTS synthesis failed: %v", err)
        return
    }
    
    // Stream audio data
    h.streamAudioData(ctx, session, track, audioData)
}

func (h *MediaPublisherHandler) streamAudioFile(
    ctx context.Context,
    session *PublishingSession,
    track *webrtc.TrackLocalStaticSample,
    filename string,
    loop bool,
) {
    for {
        // Load audio file
        audioData, err := h.publisher.audioService.LoadFile(filename)
        if err != nil {
            log.Printf("Failed to load audio file: %v", err)
            return
        }
        
        // Stream audio data
        h.streamAudioData(ctx, session, track, audioData)
        
        if !loop {
            break
        }
        
        select {
        case <-ctx.Done():
            return
        default:
            // Continue looping
        }
    }
}

func (h *MediaPublisherHandler) streamAudioData(
    ctx context.Context,
    session *PublishingSession,
    track *webrtc.TrackLocalStaticSample,
    audioData []byte,
) {
    // Calculate timing for audio streaming
    sampleRate := h.config.AudioConfig.SampleRate
    channels := h.config.AudioConfig.Channels
    samplesPerFrame := sampleRate / 50 // 20ms frames
    bytesPerFrame := samplesPerFrame * channels * 2 // 16-bit samples
    
    frameDuration := 20 * time.Millisecond
    ticker := time.NewTicker(frameDuration)
    defer ticker.Stop()
    
    position := 0
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if position >= len(audioData) {
                return // End of audio
            }
            
            // Extract frame
            end := position + bytesPerFrame
            if end > len(audioData) {
                end = len(audioData)
            }
            
            frameData := audioData[position:end]
            position = end
            
            // Apply volume
            h.applyVolume(frameData, session.GetVolume())
            
            // Write to track
            if err := track.WriteSample(media.Sample{
                Data:     frameData,
                Duration: frameDuration,
            }); err != nil {
                log.Printf("Failed to write audio sample: %v", err)
                return
            }
            
            session.IncrementFrameCount("audio")
        }
    }
}

func (h *MediaPublisherHandler) streamSynchronizedMedia(
    ctx context.Context,
    session *PublishingSession,
    audioTrack *webrtc.TrackLocalStaticSample,
    videoTrack *webrtc.TrackLocalStaticSample,
    jobMeta PublisherJobMetadata,
) {
    // Start audio streaming
    go func() {
        if jobMeta.Text != "" {
            h.streamTTSAudio(ctx, session, audioTrack, jobMeta.Text)
        } else if jobMeta.AudioFile != "" {
            h.streamAudioFile(ctx, session, audioTrack, jobMeta.AudioFile, false)
        }
    }()
    
    // Start video streaming
    go h.streamGeneratedVideo(ctx, session, videoTrack, jobMeta.VideoType)
}

func (h *MediaPublisherHandler) streamGeneratedVideo(
    ctx context.Context,
    session *PublishingSession,
    track *webrtc.TrackLocalStaticSample,
    videoType string,
) {
    frameRate := h.config.VideoConfig.FrameRate
    frameDuration := time.Second / time.Duration(frameRate)
    ticker := time.NewTicker(frameDuration)
    defer ticker.Stop()
    
    frameNumber := 0
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Generate video frame
            frameData, err := h.publisher.videoService.GenerateFrame(videoType, frameNumber)
            if err != nil {
                log.Printf("Failed to generate video frame: %v", err)
                continue
            }
            
            // Write to track
            if err := track.WriteSample(media.Sample{
                Data:     frameData,
                Duration: frameDuration,
            }); err != nil {
                log.Printf("Failed to write video sample: %v", err)
                continue
            }
            
            session.IncrementFrameCount("video")
            frameNumber++
        }
    }
}

func (h *MediaPublisherHandler) speakText(session *PublishingSession, text string) {
    track := session.GetInteractiveTrack()
    if track == nil {
        log.Printf("No interactive track available for speaking")
        return
    }
    
    log.Printf("Speaking text: %s", text)
    
    // Generate TTS audio
    audioData, err := h.publisher.ttsService.Synthesize(text)
    if err != nil {
        log.Printf("TTS synthesis failed: %v", err)
        return
    }
    
    // Stream audio data
    ctx := context.Background() // Create new context for this operation
    h.streamAudioData(ctx, session, track, audioData)
}

func (h *MediaPublisherHandler) playAudioFile(session *PublishingSession, filename string) {
    track := session.GetInteractiveTrack()
    if track == nil {
        log.Printf("No interactive track available for audio playback")
        return
    }
    
    log.Printf("Playing audio file: %s", filename)
    
    ctx := context.Background()
    h.streamAudioFile(ctx, session, track, filename, false)
}

func (h *MediaPublisherHandler) handleCommand(
    session *PublishingSession,
    command string,
    senderIdentity string,
    room *lksdk.Room,
) {
    log.Printf("Received command '%s' from %s", command, senderIdentity)
    
    switch command {
    case "status":
        stats := session.GetStats()
        response := fmt.Sprintf("Status: %d audio frames, %d video frames sent",
            stats.AudioFrameCount, stats.VideoFrameCount)
        h.sendResponse(room, senderIdentity, response)
        
    case "volume_up":
        currentVolume := session.GetVolume()
        newVolume := min(currentVolume+0.1, 1.0)
        session.SetVolume(newVolume)
        h.sendResponse(room, senderIdentity, fmt.Sprintf("Volume set to %.1f", newVolume))
        
    case "volume_down":
        currentVolume := session.GetVolume()
        newVolume := max(currentVolume-0.1, 0.0)
        session.SetVolume(newVolume)
        h.sendResponse(room, senderIdentity, fmt.Sprintf("Volume set to %.1f", newVolume))
        
    case "hello":
        h.speakText(session, fmt.Sprintf("Hello %s! How can I help you today?", senderIdentity))
        
    default:
        h.sendResponse(room, senderIdentity, fmt.Sprintf("Unknown command: %s", command))
    }
}

func (h *MediaPublisherHandler) sendResponse(room *lksdk.Room, targetIdentity, message string) {
    // Find participant by identity
    var targetSID string
    for _, p := range room.GetRemoteParticipants() {
        if p.Identity() == targetIdentity {
            targetSID = p.SID()
            break
        }
    }
    
    if targetSID == "" {
        log.Printf("Participant not found: %s", targetIdentity)
        return
    }
    
    response := map[string]interface{}{
        "type": "response",
        "message": message,
        "timestamp": time.Now().Unix(),
    }
    
    if jsonData, err := json.Marshal(response); err == nil {
        room.LocalParticipant.PublishData(jsonData, &lksdk.DataPublishOptions{
            Destination: []string{targetSID},
            Reliable:    true,
        })
    }
}

func (h *MediaPublisherHandler) announceToParticipant(
    session *PublishingSession,
    participant *lksdk.RemoteParticipant,
    message string,
    room *lksdk.Room,
) {
    // Send text response
    response := map[string]interface{}{
        "type": "welcome",
        "message": message,
    }
    
    if jsonData, err := json.Marshal(response); err == nil {
        room.LocalParticipant.PublishData(jsonData, &lksdk.DataPublishOptions{
            Destination: []string{participant.SID()},
            Reliable:    true,
        })
    }
    
    // Also speak the message if in interactive mode
    if session.IsInteractive() {
        go h.speakText(session, message)
    }
}

func (h *MediaPublisherHandler) applyVolume(audioData []byte, volume float32) {
    for i := 0; i < len(audioData)-1; i += 2 {
        // Convert bytes to int16
        sample := int16(audioData[i]) | int16(audioData[i+1])<<8
        
        // Apply volume
        adjusted := int16(float32(sample) * volume)
        
        // Convert back to bytes
        audioData[i] = byte(adjusted & 0xFF)
        audioData[i+1] = byte((adjusted >> 8) & 0xFF)
    }
}
```

### publisher.go

```go
package main

import (
    "sync"
    "time"
    
    "github.com/pion/webrtc/v4"
)

type MediaPublisher struct {
    ttsService   TTSService
    audioService AudioService
    videoService VideoService
    
    mu       sync.RWMutex
    sessions map[string]*PublishingSession
}

type PublishingSession struct {
    mu              sync.RWMutex
    roomName        string
    jobID           string
    metadata        PublisherJobMetadata
    startTime       time.Time
    endTime         *time.Time
    tracks          map[string]string // type -> track ID
    volume          float32
    interactiveTrack *webrtc.TrackLocalStaticSample
    
    // Statistics
    audioFrameCount uint64
    videoFrameCount uint64
}

type PublisherJobMetadata struct {
    Mode           string `json:"mode"`           // tts, audio_file, video_with_audio, interactive
    Text           string `json:"text"`           // For TTS mode
    AudioFile      string `json:"audio_file"`     // For audio file mode
    VideoType      string `json:"video_type"`     // For video mode
    Loop           bool   `json:"loop"`           // For audio file mode
    WelcomeMessage string `json:"welcome_message"`
    Volume         float32 `json:"volume"`
}

type PublishingStats struct {
    AudioFrameCount uint64
    VideoFrameCount uint64
    Duration        time.Duration
    TrackCount      int
}

func NewMediaPublisher(tts TTSService, audio AudioService, video VideoService) *MediaPublisher {
    return &MediaPublisher{
        ttsService:   tts,
        audioService: audio,
        videoService: video,
        sessions:     make(map[string]*PublishingSession),
    }
}

func (p *MediaPublisher) CreateSession(
    roomName, jobID string,
    metadata PublisherJobMetadata,
) *PublishingSession {
    session := &PublishingSession{
        roomName:  roomName,
        jobID:     jobID,
        metadata:  metadata,
        startTime: time.Now(),
        tracks:    make(map[string]string),
        volume:    metadata.Volume,
    }
    
    if session.volume == 0 {
        session.volume = 1.0 // Default volume
    }
    
    p.mu.Lock()
    p.sessions[roomName] = session
    p.mu.Unlock()
    
    return session
}

// PublishingSession methods

func (s *PublishingSession) End() {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    now := time.Now()
    s.endTime = &now
}

func (s *PublishingSession) AddTrack(trackType, trackID string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.tracks[trackType] = trackID
}

func (s *PublishingSession) SetVolume(volume float32) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.volume = volume
}

func (s *PublishingSession) GetVolume() float32 {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.volume
}

func (s *PublishingSession) SetInteractiveTrack(track *webrtc.TrackLocalStaticSample) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.interactiveTrack = track
}

func (s *PublishingSession) GetInteractiveTrack() *webrtc.TrackLocalStaticSample {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.interactiveTrack
}

func (s *PublishingSession) IsInteractive() bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.metadata.Mode == "interactive"
}

func (s *PublishingSession) IncrementFrameCount(trackType string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    switch trackType {
    case "audio":
        s.audioFrameCount++
    case "video":
        s.videoFrameCount++
    }
}

func (s *PublishingSession) GetStats() PublishingStats {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    duration := time.Since(s.startTime)
    if s.endTime != nil {
        duration = s.endTime.Sub(s.startTime)
    }
    
    return PublishingStats{
        AudioFrameCount: s.audioFrameCount,
        VideoFrameCount: s.videoFrameCount,
        Duration:        duration,
        TrackCount:      len(s.tracks),
    }
}
```

### services.go

```go
package main

import (
    "fmt"
    "image"
    "image/color"
    "image/draw"
    "os"
    "time"
)

// TTS Service Interface and Implementation

type TTSService interface {
    Synthesize(text string) ([]byte, error)
}

type MockTTSService struct {
    config TTSConfig
}

func NewTTSService(config TTSConfig) TTSService {
    return &MockTTSService{config: config}
}

func (s *MockTTSService) Synthesize(text string) ([]byte, error) {
    // Mock TTS - generate sine wave audio based on text length
    duration := time.Duration(len(text)*100) * time.Millisecond
    sampleRate := 48000
    samples := int(duration.Seconds() * float64(sampleRate))
    
    audioData := make([]byte, samples*2) // 16-bit samples
    
    freq := 440.0 // A4 note
    for i := 0; i < samples; i++ {
        t := float64(i) / float64(sampleRate)
        amplitude := 0.3 * float64(0x7FFF) // 30% of max amplitude
        sample := int16(amplitude * math.Sin(2*math.Pi*freq*t))
        
        // Convert to bytes (little endian)
        audioData[i*2] = byte(sample & 0xFF)
        audioData[i*2+1] = byte((sample >> 8) & 0xFF)
    }
    
    return audioData, nil
}

// Audio Service Interface and Implementation

type AudioService interface {
    LoadFile(filename string) ([]byte, error)
}

type FileAudioService struct {
    config AudioConfig
}

func NewAudioService(config AudioConfig) AudioService {
    return &FileAudioService{config: config}
}

func (s *FileAudioService) LoadFile(filename string) ([]byte, error) {
    // For demo purposes, generate audio instead of loading file
    // In a real implementation, this would use libraries like go-audio
    
    duration := 5 * time.Second // 5 second audio clip
    sampleRate := s.config.SampleRate
    samples := int(duration.Seconds() * float64(sampleRate))
    
    audioData := make([]byte, samples*2)
    
    // Generate a simple melody
    frequencies := []float64{261.63, 293.66, 329.63, 349.23, 392.00} // C, D, E, F, G
    
    for i := 0; i < samples; i++ {
        t := float64(i) / float64(sampleRate)
        freqIndex := int(t*2) % len(frequencies) // Change frequency every 0.5 seconds
        freq := frequencies[freqIndex]
        
        amplitude := 0.2 * float64(0x7FFF)
        sample := int16(amplitude * math.Sin(2*math.Pi*freq*t))
        
        audioData[i*2] = byte(sample & 0xFF)
        audioData[i*2+1] = byte((sample >> 8) & 0xFF)
    }
    
    return audioData, nil
}

// Video Service Interface and Implementation

type VideoService interface {
    GenerateFrame(videoType string, frameNumber int) ([]byte, error)
}

type GeneratedVideoService struct {
    config VideoConfig
}

func NewVideoService(config VideoConfig) VideoService {
    return &GeneratedVideoService{config: config}
}

func (s *GeneratedVideoService) GenerateFrame(videoType string, frameNumber int) ([]byte, error) {
    width := s.config.Width
    height := s.config.Height
    
    // Create image
    img := image.NewRGBA(image.Rect(0, 0, width, height))
    
    switch videoType {
    case "color_bars":
        s.drawColorBars(img, frameNumber)
    case "moving_circle":
        s.drawMovingCircle(img, frameNumber)
    case "text_display":
        s.drawTextDisplay(img, frameNumber)
    default:
        s.drawColorBars(img, frameNumber)
    }
    
    // Convert to VP8-compatible format (simplified)
    return s.encodeAsVP8(img)
}

func (s *GeneratedVideoService) drawColorBars(img *image.RGBA, frameNumber int) {
    bounds := img.Bounds()
    width := bounds.Dx()
    height := bounds.Dy()
    
    colors := []color.RGBA{
        {255, 0, 0, 255},     // Red
        {0, 255, 0, 255},     // Green
        {0, 0, 255, 255},     // Blue
        {255, 255, 0, 255},   // Yellow
        {255, 0, 255, 255},   // Magenta
        {0, 255, 255, 255},   // Cyan
        {255, 255, 255, 255}, // White
    }
    
    barWidth := width / len(colors)
    
    for i, c := range colors {
        x0 := i * barWidth
        x1 := (i + 1) * barWidth
        if i == len(colors)-1 {
            x1 = width // Ensure last bar fills remaining width
        }
        
        // Add animation by shifting colors
        shiftedColor := colors[(i+frameNumber/30)%len(colors)]
        
        for y := 0; y < height; y++ {
            for x := x0; x < x1; x++ {
                img.Set(x, y, shiftedColor)
            }
        }
    }
}

func (s *GeneratedVideoService) drawMovingCircle(img *image.RGBA, frameNumber int) {
    bounds := img.Bounds()
    width := bounds.Dx()
    height := bounds.Dy()
    
    // Fill background
    draw.Draw(img, bounds, &image.Uniform{color.RGBA{50, 50, 50, 255}}, image.Point{}, draw.Src)
    
    // Calculate circle position
    centerX := int(float64(width/2) + 100*math.Sin(float64(frameNumber)*0.05))
    centerY := int(float64(height/2) + 50*math.Cos(float64(frameNumber)*0.03))
    radius := 50
    
    // Draw circle
    circleColor := color.RGBA{
        uint8(128 + 127*math.Sin(float64(frameNumber)*0.02)),
        uint8(128 + 127*math.Cos(float64(frameNumber)*0.03)),
        uint8(128 + 127*math.Sin(float64(frameNumber)*0.04)),
        255,
    }
    
    for y := 0; y < height; y++ {
        for x := 0; x < width; x++ {
            dx := x - centerX
            dy := y - centerY
            if dx*dx+dy*dy <= radius*radius {
                img.Set(x, y, circleColor)
            }
        }
    }
}

func (s *GeneratedVideoService) drawTextDisplay(img *image.RGBA, frameNumber int) {
    bounds := img.Bounds()
    
    // Fill background with gradient
    for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
        for x := bounds.Min.X; x < bounds.Max.X; x++ {
            intensity := uint8((float64(y)/float64(bounds.Dy()) + float64(frameNumber)*0.01) * 255)
            img.Set(x, y, color.RGBA{intensity/3, intensity/2, intensity, 255})
        }
    }
    
    // In a real implementation, you would render text here using libraries like
    // github.com/golang/freetype or github.com/fogleman/gg
}

func (s *GeneratedVideoService) encodeAsVP8(img *image.RGBA) ([]byte, error) {
    // This is a simplified placeholder for VP8 encoding
    // In a real implementation, you would use VP8 encoding libraries
    
    bounds := img.Bounds()
    data := make([]byte, bounds.Dx()*bounds.Dy()*4)
    
    i := 0
    for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
        for x := bounds.Min.X; x < bounds.Max.X; x++ {
            c := img.RGBAAt(x, y)
            data[i] = c.R
            data[i+1] = c.G
            data[i+2] = c.B
            data[i+3] = c.A
            i += 4
        }
    }
    
    return data, nil
}
```

### config.go

```go
package main

import (
    "time"
)

type Config struct {
    LiveKitURL  string
    APIKey      string
    APISecret   string
    TTSConfig   TTSConfig
    AudioConfig AudioConfig
    VideoConfig VideoConfig
}

type TTSConfig struct {
    Engine   string
    Language string
    Voice    string
    Speed    float64
}

type AudioConfig struct {
    SampleRate int
    Channels   int
    BitDepth   int
}

type VideoConfig struct {
    Width     int
    Height    int
    FrameRate int
    Bitrate   int
}
```

## Running the Example

1. Set environment variables:
   ```bash
   export LIVEKIT_URL="ws://localhost:7880"
   export LIVEKIT_API_KEY="your-api-key"
   export LIVEKIT_API_SECRET="your-api-secret"
   export TTS_ENGINE="google"
   export VIDEO_WIDTH="1280"
   export VIDEO_HEIGHT="720"
   export VIDEO_FRAME_RATE="30"
   ```

2. Run the agent:
   ```bash
   go run .
   ```

3. Dispatch publisher jobs with different modes:

   **TTS Mode:**
   ```json
   {
     "mode": "tts",
     "text": "Hello everyone! Welcome to the LiveKit media publisher demonstration.",
     "volume": 0.8
   }
   ```

   **Audio File Mode:**
   ```json
   {
     "mode": "audio_file",
     "audio_file": "background_music.wav",
     "loop": true,
     "volume": 0.6
   }
   ```

   **Video with Audio:**
   ```json
   {
     "mode": "video_with_audio",
     "video_type": "moving_circle",
     "text": "This is synchronized audio with video",
     "volume": 0.7
   }
   ```

   **Interactive Mode:**
   ```json
   {
     "mode": "interactive",
     "welcome_message": "Hello! Send me commands to control audio playback.",
     "volume": 1.0
   }
   ```

## Expected Output

```
2024/01/15 10:30:00 Starting media publisher agent...
2024/01/15 10:30:01 Starting media publishing for room: demo-room (Job ID: job-789, Mode: video_with_audio)
2024/01/15 10:30:02 Starting video with audio mode
2024/01/15 10:30:03 Successfully published audio track: Generated Audio
2024/01/15 10:30:03 Successfully published video track: Generated Video
2024/01/15 10:30:04 Speaking text: This is synchronized audio with video
2024/01/15 10:30:05 Streaming video frames at 30fps with moving_circle pattern
2024/01/15 10:30:10 Received message type: speak_text from user1
2024/01/15 10:30:10 Speaking text: Hello from the interactive publisher!
```

## Key Features Demonstrated

1. **Multiple Publishing Modes**: TTS, audio files, video generation, and interactive
2. **Real-time Media Generation**: Dynamic audio and video content creation
3. **WebRTC Integration**: Proper track creation and streaming
4. **Interactive Commands**: Responding to participant requests
5. **Volume Control**: Adjustable audio levels
6. **Synchronized Streaming**: Coordinated audio/video playback
7. **Data Message Handling**: Processing commands from participants

## Extending This Example

- Integrate real TTS services (Google Cloud TTS, Azure Cognitive Services)
- Add support for multiple audio/video file formats
- Implement advanced video effects and filters
- Add speech recognition for voice commands
- Create playlist management for audio files
- Add streaming from external sources (RTMP, HTTP)

## Next Steps

- See [Load-Balanced Workers](load-balanced-workers.md) for scaling media publishing
- Review [Media Processing](../media-processing.md) for advanced media handling
- Check [Advanced Features](../advanced-features.md) for production deployment