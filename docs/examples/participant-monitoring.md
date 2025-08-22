# Participant Monitoring Agent Example

A specialized participant agent that provides personalized monitoring, connection quality tracking, and intelligent assistance for individual participants. This example demonstrates participant-focused job handling and stateful interactions.

## What This Example Demonstrates

- Participant-specific job handling (JT_PARTICIPANT)
- Individual participant connection monitoring
- Speaking detection and activity tracking
- Connection quality analysis
- Personalized notifications and assistance
- State management for individual users
- Adaptive behavior based on participant metadata

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
    
    // Create participant monitor
    monitor := NewParticipantMonitor()
    
    // Create job handler
    handler := &ParticipantMonitoringHandler{
        monitor: monitor,
        config:  config,
    }
    
    // Create worker - configured for participant jobs
    worker := agent.NewWorker(
        config.LiveKitURL,
        config.APIKey,
        config.APISecret,
        handler,
        agent.WorkerOptions{
            AgentName: "participant-monitor",
            JobTypes:  []livekit.JobType{livekit.JobType_JT_PARTICIPANT},
            MaxConcurrentJobs: 50, // Can monitor many participants
        },
    )
    
    // Set up graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    // Start monitoring service
    go monitor.StartService(ctx)
    
    // Start worker in goroutine
    errChan := make(chan error, 1)
    go func() {
        log.Println("Starting participant monitoring agent...")
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
    
    // Print final monitoring summary
    monitor.PrintSummary()
    log.Println("Participant monitoring agent stopped")
}

func loadConfig() *Config {
    return &Config{
        LiveKitURL:                getEnv("LIVEKIT_URL", "ws://localhost:7880"),
        APIKey:                    mustGetEnv("LIVEKIT_API_KEY"),
        APISecret:                 mustGetEnv("LIVEKIT_API_SECRET"),
        ConnectionQualityThreshold: getEnvFloat("CONNECTION_QUALITY_THRESHOLD", 0.5),
        InactivityTimeout:         getEnvDuration("INACTIVITY_TIMEOUT", 5*time.Minute),
        SpeakingThreshold:         getEnvFloat("SPEAKING_THRESHOLD", 0.01),
        EnableNotifications:       getEnvBool("ENABLE_NOTIFICATIONS", true),
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

func getEnvFloat(key string, defaultValue float64) float64 {
    if str := os.Getenv(key); str != "" {
        if value, err := strconv.ParseFloat(str, 64); err == nil {
            return value
        }
    }
    return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
    if str := os.Getenv(key); str != "" {
        if value, err := time.ParseDuration(str); err == nil {
            return value
        }
    }
    return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
    if str := os.Getenv(key); str != "" {
        if value, err := strconv.ParseBool(str); err == nil {
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
)

type ParticipantMonitoringHandler struct {
    monitor *ParticipantMonitor
    config  *Config
}

func (h *ParticipantMonitoringHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Validate job type
    if job.Type != livekit.JobType_JT_PARTICIPANT {
        return fmt.Errorf("expected participant job, got %v", job.Type)
    }
    
    // Extract target participant from job metadata
    var jobMeta JobMetadata
    if err := json.Unmarshal([]byte(job.Metadata), &jobMeta); err != nil {
        return fmt.Errorf("failed to parse job metadata: %w", err)
    }
    
    if jobMeta.ParticipantIdentity == "" {
        return fmt.Errorf("participant identity not specified in job metadata")
    }
    
    log.Printf("Starting monitoring for participant: %s in room: %s (Job ID: %s)",
        jobMeta.ParticipantIdentity, room.Name(), job.Id)
    
    // Create monitoring session
    session := h.monitor.CreateSession(
        jobMeta.ParticipantIdentity,
        room.Name(),
        job.Id,
        jobMeta,
    )
    defer func() {
        session.End()
        log.Printf("Monitoring ended for participant: %s", jobMeta.ParticipantIdentity)
    }()
    
    // Wait for target participant or check if already present
    var targetParticipant *lksdk.RemoteParticipant
    
    // Check existing participants
    for _, p := range room.GetRemoteParticipants() {
        if p.Identity() == jobMeta.ParticipantIdentity {
            targetParticipant = p
            break
        }
    }
    
    if targetParticipant != nil {
        h.startMonitoring(session, targetParticipant, room)
    }
    
    // Set up event handlers for participant connection
    participantConnected := make(chan *lksdk.RemoteParticipant, 1)
    participantDisconnected := make(chan *lksdk.RemoteParticipant, 1)
    
    room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
        if p.Identity() == jobMeta.ParticipantIdentity {
            participantConnected <- p
        }
    }
    
    room.Callback.OnParticipantDisconnected = func(p *lksdk.RemoteParticipant) {
        if p.Identity() == jobMeta.ParticipantIdentity {
            participantDisconnected <- p
        }
    }
    
    // Main monitoring loop
    inactivityTimer := time.NewTimer(h.config.InactivityTimeout)
    defer inactivityTimer.Stop()
    
    for {
        select {
        case <-ctx.Done():
            log.Printf("Job cancelled for participant: %s", jobMeta.ParticipantIdentity)
            return nil
            
        case p := <-participantConnected:
            log.Printf("Target participant connected: %s", p.Identity())
            targetParticipant = p
            session.SetConnected(true)
            h.startMonitoring(session, p, room)
            
            // Send welcome message
            if h.config.EnableNotifications {
                h.sendWelcomeMessage(room, p, jobMeta)
            }
            
            // Reset inactivity timer
            inactivityTimer.Reset(h.config.InactivityTimeout)
            
        case p := <-participantDisconnected:
            log.Printf("Target participant disconnected: %s", p.Identity())
            targetParticipant = nil
            session.SetConnected(false)
            
            // Check if we should end monitoring
            if jobMeta.EndOnDisconnect {
                log.Printf("Ending monitoring due to participant disconnect")
                return nil
            }
            
        case <-inactivityTimer.C:
            if targetParticipant == nil {
                log.Printf("Participant %s never connected within timeout, ending job", 
                    jobMeta.ParticipantIdentity)
                return fmt.Errorf("participant connection timeout")
            }
            
            // Reset for next check
            inactivityTimer.Reset(h.config.InactivityTimeout)
        }
    }
}

func (h *ParticipantMonitoringHandler) startMonitoring(
    session *ParticipantSession,
    participant *lksdk.RemoteParticipant,
    room *lksdk.Room,
) {
    // Set up track monitoring
    participant.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication) {
        log.Printf("Track published by %s: %s (%s)", 
            participant.Identity(), pub.SID(), pub.Kind())
        
        session.AddTrack(pub.SID(), pub.Kind().String())
        
        // Subscribe to track for monitoring
        if err := pub.SetSubscribed(true); err != nil {
            log.Printf("Failed to subscribe to track %s: %v", pub.SID(), err)
        } else {
            h.monitorTrack(session, pub, participant, room)
        }
    }
    
    participant.OnTrackUnpublished = func(pub *lksdk.RemoteTrackPublication) {
        log.Printf("Track unpublished by %s: %s", participant.Identity(), pub.SID())
        session.RemoveTrack(pub.SID())
    }
    
    // Monitor existing tracks
    for _, pub := range participant.TrackPublications() {
        if pub.Track() != nil {
            session.AddTrack(pub.SID(), pub.Kind().String())
            pub.SetSubscribed(true)
            h.monitorTrack(session, pub, participant, room)
        }
    }
}

func (h *ParticipantMonitoringHandler) monitorTrack(
    session *ParticipantSession,
    publication *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
    room *lksdk.Room,
) {
    track := publication.Track()
    if track == nil {
        return
    }
    
    // Start track-specific monitoring
    go func() {
        if publication.Kind() == lksdk.TrackKindAudio {
            h.monitorAudioTrack(session, track, participant, room)
        } else if publication.Kind() == lksdk.TrackKindVideo {
            h.monitorVideoTrack(session, track, participant, room)
        }
    }()
}

func (h *ParticipantMonitoringHandler) monitorAudioTrack(
    session *ParticipantSession,
    track *webrtc.TrackRemote,
    participant *lksdk.RemoteParticipant,
    room *lksdk.Room,
) {
    log.Printf("Starting audio monitoring for %s", participant.Identity())
    
    // Audio level detection for speaking detection
    var audioBuffer []byte
    var lastSpeakingTime time.Time
    speakingThreshold := float32(h.config.SpeakingThreshold)
    
    for {
        // Read audio samples
        sample, _, err := track.ReadSample()
        if err != nil {
            log.Printf("Audio monitoring ended for %s: %v", participant.Identity(), err)
            return
        }
        
        audioBuffer = append(audioBuffer, sample.Data...)
        
        // Process audio buffer every 100ms
        if len(audioBuffer) >= 4800 { // ~100ms at 48kHz
            audioLevel := h.calculateAudioLevel(audioBuffer)
            session.UpdateAudioLevel(audioLevel)
            
            // Speaking detection
            if audioLevel > speakingThreshold {
                if time.Since(lastSpeakingTime) > 2*time.Second {
                    log.Printf("Participant %s started speaking (level: %.3f)", 
                        participant.Identity(), audioLevel)
                    session.SetSpeaking(true)
                    
                    // Send speaking notification if enabled
                    if h.config.EnableNotifications {
                        h.sendSpeakingNotification(room, participant, true)
                    }
                }
                lastSpeakingTime = time.Now()
            } else {
                // Stop speaking after 1 second of silence
                if session.IsSpeaking() && time.Since(lastSpeakingTime) > time.Second {
                    log.Printf("Participant %s stopped speaking", participant.Identity())
                    session.SetSpeaking(false)
                    
                    if h.config.EnableNotifications {
                        h.sendSpeakingNotification(room, participant, false)
                    }
                }
            }
            
            // Clear buffer
            audioBuffer = audioBuffer[:0]
        }
    }
}

func (h *ParticipantMonitoringHandler) monitorVideoTrack(
    session *ParticipantSession,
    track *webrtc.TrackRemote,
    participant *lksdk.RemoteParticipant,
    room *lksdk.Room,
) {
    log.Printf("Starting video monitoring for %s", participant.Identity())
    
    var lastFrameTime time.Time
    frameCount := 0
    
    for {
        // Read video samples
        sample, _, err := track.ReadSample()
        if err != nil {
            log.Printf("Video monitoring ended for %s: %v", participant.Identity(), err)
            return
        }
        
        frameCount++
        now := time.Now()
        
        // Calculate frame rate every 5 seconds
        if frameCount%150 == 0 { // Assuming ~30fps
            if !lastFrameTime.IsZero() {
                duration := now.Sub(lastFrameTime)
                fps := float64(150) / duration.Seconds()
                session.UpdateFrameRate(fps)
                
                log.Printf("Video FPS for %s: %.1f", participant.Identity(), fps)
            }
            lastFrameTime = now
        }
        
        // Update video activity
        session.UpdateLastVideoFrame(now)
    }
}

func (h *ParticipantMonitoringHandler) calculateAudioLevel(samples []byte) float32 {
    if len(samples) == 0 {
        return 0.0
    }
    
    // Simple RMS calculation
    var sum float64
    for i := 0; i < len(samples)-1; i += 2 {
        // Convert 16-bit samples to float
        sample := int16(samples[i]) | int16(samples[i+1])<<8
        normalized := float64(sample) / 32768.0
        sum += normalized * normalized
    }
    
    rms := math.Sqrt(sum / float64(len(samples)/2))
    return float32(rms)
}

func (h *ParticipantMonitoringHandler) sendWelcomeMessage(
    room *lksdk.Room,
    participant *lksdk.RemoteParticipant,
    jobMeta JobMetadata,
) {
    message := map[string]interface{}{
        "type": "participant_monitoring",
        "action": "welcome",
        "message": fmt.Sprintf("Hello %s! Your connection is being monitored for optimal experience.", 
            participant.Identity()),
        "features": []string{"connection_quality", "speaking_detection", "activity_tracking"},
    }
    
    if jsonData, err := json.Marshal(message); err == nil {
        room.LocalParticipant.PublishData(jsonData, &lksdk.DataPublishOptions{
            Destination: []string{participant.SID()},
            Reliable:    true,
        })
    }
}

func (h *ParticipantMonitoringHandler) sendSpeakingNotification(
    room *lksdk.Room,
    participant *lksdk.RemoteParticipant,
    speaking bool,
) {
    message := map[string]interface{}{
        "type": "speaking_detection",
        "participant": participant.Identity(),
        "speaking": speaking,
        "timestamp": time.Now().Unix(),
    }
    
    if jsonData, err := json.Marshal(message); err == nil {
        // Broadcast to all participants except the speaker
        var destinations []string
        for _, p := range room.GetRemoteParticipants() {
            if p.SID() != participant.SID() {
                destinations = append(destinations, p.SID())
            }
        }
        
        if len(destinations) > 0 {
            room.LocalParticipant.PublishData(jsonData, &lksdk.DataPublishOptions{
                Destination: destinations,
                Reliable:    true,
            })
        }
    }
}
```

### monitor.go

```go
package main

import (
    "context"
    "log"
    "sync"
    "time"
)

type ParticipantMonitor struct {
    mu       sync.RWMutex
    sessions map[string]*ParticipantSession
}

type ParticipantSession struct {
    mu                  sync.RWMutex
    participantIdentity string
    roomName           string
    jobID              string
    startTime          time.Time
    endTime            *time.Time
    connected          bool
    speaking           bool
    tracks             map[string]*TrackInfo
    audioLevel         float32
    frameRate          float64
    lastVideoFrame     time.Time
    connectionQuality   string
    metadata           JobMetadata
    
    // Statistics
    speakingDuration   time.Duration
    lastSpeakingStart  time.Time
    totalFrames        uint64
    qualityChanges     []QualityChange
}

type TrackInfo struct {
    SID         string
    Type        string
    PublishedAt time.Time
    Active      bool
}

type QualityChange struct {
    From      string
    To        string
    Timestamp time.Time
    Reason    string
}

type JobMetadata struct {
    ParticipantIdentity string                 `json:"participant_identity"`
    MonitoringType      string                 `json:"monitoring_type"`
    EndOnDisconnect     bool                   `json:"end_on_disconnect"`
    NotificationPrefs   map[string]interface{} `json:"notification_preferences"`
    CustomSettings      map[string]interface{} `json:"custom_settings"`
}

type ParticipantStats struct {
    Connected           bool
    Speaking            bool
    TrackCount          int
    AudioLevel          float32
    FrameRate           float64
    SpeakingDuration    time.Duration
    ConnectionQuality   string
    SessionDuration     time.Duration
}

func NewParticipantMonitor() *ParticipantMonitor {
    return &ParticipantMonitor{
        sessions: make(map[string]*ParticipantSession),
    }
}

func (m *ParticipantMonitor) CreateSession(
    participantIdentity, roomName, jobID string,
    metadata JobMetadata,
) *ParticipantSession {
    session := &ParticipantSession{
        participantIdentity: participantIdentity,
        roomName:           roomName,
        jobID:              jobID,
        startTime:          time.Now(),
        tracks:             make(map[string]*TrackInfo),
        metadata:           metadata,
        qualityChanges:     make([]QualityChange, 0),
    }
    
    m.mu.Lock()
    m.sessions[participantIdentity] = session
    m.mu.Unlock()
    
    log.Printf("Created monitoring session for %s in room %s", participantIdentity, roomName)
    return session
}

func (m *ParticipantMonitor) StartService(ctx context.Context) {
    // Periodic health check and reporting
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            m.performHealthChecks()
            m.generateReports()
        }
    }
}

func (m *ParticipantMonitor) performHealthChecks() {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    for identity, session := range m.sessions {
        stats := session.GetStats()
        
        // Check for connection issues
        if stats.Connected && stats.ConnectionQuality == "poor" {
            log.Printf("WARNING: Poor connection quality for participant %s", identity)
        }
        
        // Check for inactive video
        if session.hasVideoTrack() && time.Since(session.lastVideoFrame) > 10*time.Second {
            log.Printf("WARNING: No video frames received from %s for >10s", identity)
        }
        
        // Check session duration
        if stats.SessionDuration > 4*time.Hour {
            log.Printf("INFO: Long session detected for %s: %s", identity, stats.SessionDuration)
        }
    }
}

func (m *ParticipantMonitor) generateReports() {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    activeCount := 0
    speakingCount := 0
    poorQualityCount := 0
    
    for _, session := range m.sessions {
        stats := session.GetStats()
        if stats.Connected {
            activeCount++
        }
        if stats.Speaking {
            speakingCount++
        }
        if stats.ConnectionQuality == "poor" {
            poorQualityCount++
        }
    }
    
    if activeCount > 0 {
        log.Printf("Monitoring Report - Active: %d, Speaking: %d, Poor Quality: %d",
            activeCount, speakingCount, poorQualityCount)
    }
}

func (m *ParticipantMonitor) PrintSummary() {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    log.Println("=== Participant Monitoring Summary ===")
    for identity, session := range m.sessions {
        stats := session.GetStats()
        log.Printf("Participant: %s", identity)
        log.Printf("  Session Duration: %s", stats.SessionDuration.Round(time.Second))
        log.Printf("  Speaking Time: %s", stats.SpeakingDuration.Round(time.Second))
        log.Printf("  Track Count: %d", stats.TrackCount)
        log.Printf("  Final Quality: %s", stats.ConnectionQuality)
    }
    log.Println("=====================================")
}

// ParticipantSession methods

func (s *ParticipantSession) End() {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    now := time.Now()
    s.endTime = &now
    
    // Finalize speaking duration
    if s.speaking && !s.lastSpeakingStart.IsZero() {
        s.speakingDuration += now.Sub(s.lastSpeakingStart)
    }
}

func (s *ParticipantSession) SetConnected(connected bool) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.connected = connected
}

func (s *ParticipantSession) SetSpeaking(speaking bool) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    now := time.Now()
    if speaking && !s.speaking {
        // Started speaking
        s.speaking = true
        s.lastSpeakingStart = now
    } else if !speaking && s.speaking {
        // Stopped speaking
        s.speaking = false
        if !s.lastSpeakingStart.IsZero() {
            s.speakingDuration += now.Sub(s.lastSpeakingStart)
        }
    }
}

func (s *ParticipantSession) IsSpeaking() bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.speaking
}

func (s *ParticipantSession) AddTrack(sid, trackType string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    s.tracks[sid] = &TrackInfo{
        SID:         sid,
        Type:        trackType,
        PublishedAt: time.Now(),
        Active:      true,
    }
}

func (s *ParticipantSession) RemoveTrack(sid string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    if track, exists := s.tracks[sid]; exists {
        track.Active = false
    }
}

func (s *ParticipantSession) UpdateAudioLevel(level float32) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.audioLevel = level
}

func (s *ParticipantSession) UpdateFrameRate(fps float64) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.frameRate = fps
    s.totalFrames++
}

func (s *ParticipantSession) UpdateLastVideoFrame(timestamp time.Time) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.lastVideoFrame = timestamp
}

func (s *ParticipantSession) UpdateConnectionQuality(quality string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    if s.connectionQuality != quality {
        s.qualityChanges = append(s.qualityChanges, QualityChange{
            From:      s.connectionQuality,
            To:        quality,
            Timestamp: time.Now(),
            Reason:    "network_conditions",
        })
        s.connectionQuality = quality
    }
}

func (s *ParticipantSession) hasVideoTrack() bool {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    for _, track := range s.tracks {
        if track.Type == "video" && track.Active {
            return true
        }
    }
    return false
}

func (s *ParticipantSession) GetStats() ParticipantStats {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    activeTrackCount := 0
    for _, track := range s.tracks {
        if track.Active {
            activeTrackCount++
        }
    }
    
    duration := time.Since(s.startTime)
    if s.endTime != nil {
        duration = s.endTime.Sub(s.startTime)
    }
    
    speakingDuration := s.speakingDuration
    if s.speaking && !s.lastSpeakingStart.IsZero() {
        speakingDuration += time.Since(s.lastSpeakingStart)
    }
    
    return ParticipantStats{
        Connected:         s.connected,
        Speaking:          s.speaking,
        TrackCount:        activeTrackCount,
        AudioLevel:        s.audioLevel,
        FrameRate:         s.frameRate,
        SpeakingDuration:  speakingDuration,
        ConnectionQuality: s.connectionQuality,
        SessionDuration:   duration,
    }
}
```

### config.go

```go
package main

import (
    "time"
)

type Config struct {
    LiveKitURL                string
    APIKey                    string
    APISecret                 string
    ConnectionQualityThreshold float64
    InactivityTimeout         time.Duration
    SpeakingThreshold         float64
    EnableNotifications       bool
}
```

## Running the Example

1. Set environment variables:
   ```bash
   export LIVEKIT_URL="ws://localhost:7880"
   export LIVEKIT_API_KEY="your-api-key"
   export LIVEKIT_API_SECRET="your-api-secret"
   export CONNECTION_QUALITY_THRESHOLD="0.5"
   export INACTIVITY_TIMEOUT="5m"
   export SPEAKING_THRESHOLD="0.01"
   export ENABLE_NOTIFICATIONS="true"
   ```

2. Run the agent:
   ```bash
   go run .
   ```

3. Dispatch a participant job with appropriate metadata:
   ```json
   {
     "participant_identity": "user123",
     "monitoring_type": "full",
     "end_on_disconnect": false,
     "notification_preferences": {
       "speaking_alerts": true,
       "quality_alerts": true
     }
   }
   ```

## Expected Output

```
2024/01/15 10:30:00 Starting participant monitoring agent...
2024/01/15 10:30:01 Starting monitoring for participant: user123 in room: test-room (Job ID: job-456)
2024/01/15 10:30:02 Created monitoring session for user123 in room test-room
2024/01/15 10:30:05 Target participant connected: user123
2024/01/15 10:30:06 Track published by user123: TR_audio_123 (audio)
2024/01/15 10:30:06 Starting audio monitoring for user123
2024/01/15 10:30:07 Track published by user123: TR_video_456 (video)
2024/01/15 10:30:07 Starting video monitoring for user123
2024/01/15 10:30:12 Participant user123 started speaking (level: 0.045)
2024/01/15 10:30:15 Participant user123 stopped speaking
2024/01/15 10:30:17 Video FPS for user123: 29.8
2024/01/15 10:30:30 Monitoring Report - Active: 1, Speaking: 0, Poor Quality: 0
```

## Key Features Demonstrated

1. **Participant-Specific Jobs**: Handling JT_PARTICIPANT job types with targeted monitoring
2. **Real-time Audio Analysis**: Speaking detection using audio level analysis
3. **Video Quality Monitoring**: Frame rate tracking and video activity detection
4. **Connection Quality Tracking**: Monitoring and alerting on connection issues
5. **Stateful Session Management**: Maintaining per-participant state and statistics
6. **Personalized Notifications**: Sending targeted messages to specific participants
7. **Health Monitoring**: Periodic health checks and reporting
8. **Configurable Behavior**: Environment-based configuration for thresholds and preferences

## Extending This Example

- Add machine learning for emotion detection from audio
- Implement bandwidth usage optimization based on connection quality
- Add integration with external notification services
- Create dashboards for real-time monitoring visualization
- Add participant behavior analytics and insights
- Implement automatic quality adjustments based on device capabilities

## Use Cases

- **Customer Support**: Monitor support agent performance and connection quality
- **Education**: Track student engagement and participation in online classes
- **Healthcare**: Monitor patient connection quality during telemedicine sessions
- **Broadcasting**: Ensure presenter connection quality during live streams
- **Gaming**: Monitor player connection stability in competitive games

## Next Steps

- See [Media Publisher](media-publisher.md) for publishing audio/video content
- Explore [Load-Balanced Workers](load-balanced-workers.md) for scaling monitoring
- Review [Advanced Features](../advanced-features.md) for production deployment