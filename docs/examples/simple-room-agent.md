# Simple Room Agent Example

A basic room agent that monitors room activity, tracks participants, and collects simple analytics. This example demonstrates the fundamental concepts of the LiveKit Agent SDK.

## What This Example Demonstrates

- Basic worker setup and configuration
- Room-level job handling
- Event processing (participants, tracks, data)
- Simple analytics collection
- Graceful shutdown
- Error handling

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
    
    // Create analytics collector
    analytics := NewAnalyticsCollector()
    
    // Create job handler
    handler := &RoomAnalyticsHandler{
        analytics: analytics,
    }
    
    // Create worker
    worker := agent.NewWorker(
        config.LiveKitURL,
        config.APIKey,
        config.APISecret,
        handler,
        agent.WorkerOptions{
            AgentName: "simple-room-agent",
            JobTypes:  []livekit.JobType{livekit.JobType_JT_ROOM},
        },
    )
    
    // Set up graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    // Start analytics reporter
    go analytics.StartReporter(ctx)
    
    // Start worker in goroutine
    errChan := make(chan error, 1)
    go func() {
        log.Println("Starting room analytics agent...")
        errChan <- worker.Start(ctx)
    }()
    
    // Wait for shutdown signal or error
    select {
    case sig := <-sigChan:
        log.Printf("Received signal %v, shutting down...", sig)
        cancel()
        
        // Give worker time to clean up
        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer shutdownCancel()
        
        if err := worker.Stop(shutdownCtx); err != nil {
            log.Printf("Error during shutdown: %v", err)
        }
        
    case err := <-errChan:
        if err != nil {
            log.Fatal("Worker error:", err)
        }
    }
    
    // Print final analytics
    analytics.PrintSummary()
    log.Println("Agent stopped")
}

func loadConfig() *Config {
    return &Config{
        LiveKitURL: getEnv("LIVEKIT_URL", "ws://localhost:7880"),
        APIKey:     mustGetEnv("LIVEKIT_API_KEY"),
        APISecret:  mustGetEnv("LIVEKIT_API_SECRET"),
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

type RoomAnalyticsHandler struct {
    analytics *AnalyticsCollector
}

func (h *RoomAnalyticsHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Validate job type
    if job.Type != livekit.JobType_JT_ROOM {
        return fmt.Errorf("expected room job, got %v", job.Type)
    }
    
    log.Printf("Starting analytics for room: %s (Job ID: %s)", room.Name(), job.Id)
    
    // Create room session
    session := h.analytics.CreateSession(room.Name(), job.Id)
    defer func() {
        session.End()
        log.Printf("Analytics session ended for room: %s", room.Name())
    }()
    
    // Set up event handlers
    room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
        h.handleParticipantConnected(session, p)
    }
    
    room.Callback.OnParticipantDisconnected = func(p *lksdk.RemoteParticipant) {
        h.handleParticipantDisconnected(session, p)
    }
    
    room.Callback.OnTrackPublished = func(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
        h.handleTrackPublished(session, publication, rp)
    }
    
    room.Callback.OnTrackUnpublished = func(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
        h.handleTrackUnpublished(session, publication, rp)
    }
    
    room.Callback.OnDataReceived = func(data []byte, params lksdk.DataReceiveParams) {
        h.handleDataReceived(session, data, params)
    }
    
    room.Callback.OnConnectionQualityChanged = func(update *livekit.ConnectionQualityInfo) {
        h.handleConnectionQualityChanged(session, update)
    }
    
    room.Callback.OnRoomMetadataChanged = func(metadata string) {
        h.handleRoomMetadataChanged(session, metadata)
    }
    
    // Process existing participants
    for _, participant := range room.GetRemoteParticipants() {
        session.AddParticipant(participant.SID(), participant.Identity())
        log.Printf("Found existing participant: %s", participant.Identity())
        
        // Check existing tracks
        for _, pub := range participant.TrackPublications() {
            if pub.Track() != nil {
                session.AddTrack(pub.SID(), pub.Kind().String(), participant.SID())
            }
        }
    }
    
    // Periodic status report
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    // Main loop
    for {
        select {
        case <-ctx.Done():
            log.Printf("Job cancelled for room: %s", room.Name())
            return nil
            
        case <-ticker.C:
            h.reportStatus(session, room)
        }
    }
}

func (h *RoomAnalyticsHandler) handleParticipantConnected(session *RoomSession, p *lksdk.RemoteParticipant) {
    log.Printf("Participant connected: %s (SID: %s)", p.Identity(), p.SID())
    session.AddParticipant(p.SID(), p.Identity())
    
    // Log participant metadata if present
    if p.Metadata() != "" {
        var metadata map[string]interface{}
        if err := json.Unmarshal([]byte(p.Metadata()), &metadata); err == nil {
            log.Printf("Participant metadata: %+v", metadata)
        }
    }
}

func (h *RoomAnalyticsHandler) handleParticipantDisconnected(session *RoomSession, p *lksdk.RemoteParticipant) {
    log.Printf("Participant disconnected: %s", p.Identity())
    session.RemoveParticipant(p.SID())
}

func (h *RoomAnalyticsHandler) handleTrackPublished(
    session *RoomSession,
    publication *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
) {
    trackType := "unknown"
    source := "unknown"
    
    if publication.Kind() == lksdk.TrackKindAudio {
        trackType = "audio"
    } else if publication.Kind() == lksdk.TrackKindVideo {
        trackType = "video"
    }
    
    switch publication.Source() {
    case livekit.TrackSource_CAMERA:
        source = "camera"
    case livekit.TrackSource_MICROPHONE:
        source = "microphone"
    case livekit.TrackSource_SCREEN_SHARE:
        source = "screen"
    }
    
    log.Printf("Track published: %s %s by %s (SID: %s)",
        source, trackType, participant.Identity(), publication.SID())
    
    session.AddTrack(publication.SID(), trackType, participant.SID())
    
    // Subscribe to track for quality monitoring
    if publication.Track() == nil {
        publication.OnSubscriptionStatusChanged = func(status livekit.TrackPublication_SubscriptionStatus) {
            if status == livekit.TrackPublication_SUBSCRIBED {
                log.Printf("Successfully subscribed to track: %s", publication.SID())
            }
        }
        publication.SetSubscribed(true)
    }
}

func (h *RoomAnalyticsHandler) handleTrackUnpublished(
    session *RoomSession,
    publication *lksdk.RemoteTrackPublication,
    participant *lksdk.RemoteParticipant,
) {
    log.Printf("Track unpublished: %s by %s", publication.SID(), participant.Identity())
    session.RemoveTrack(publication.SID())
}

func (h *RoomAnalyticsHandler) handleDataReceived(
    session *RoomSession,
    data []byte,
    params lksdk.DataReceiveParams,
) {
    session.IncrementDataMessages()
    
    // Try to parse as JSON
    var message map[string]interface{}
    if err := json.Unmarshal(data, &message); err == nil {
        log.Printf("Data message from %s: %+v", params.SenderIdentity, message)
    } else {
        log.Printf("Data message from %s: %s", params.SenderIdentity, string(data))
    }
}

func (h *RoomAnalyticsHandler) handleConnectionQualityChanged(
    session *RoomSession,
    update *livekit.ConnectionQualityInfo,
) {
    quality := "unknown"
    switch update.Quality {
    case livekit.ConnectionQuality_EXCELLENT:
        quality = "excellent"
    case livekit.ConnectionQuality_GOOD:
        quality = "good"
    case livekit.ConnectionQuality_POOR:
        quality = "poor"
    }
    
    log.Printf("Connection quality changed for %s: %s", update.ParticipantSid, quality)
    session.UpdateConnectionQuality(update.ParticipantSid, quality)
}

func (h *RoomAnalyticsHandler) handleRoomMetadataChanged(session *RoomSession, metadata string) {
    log.Printf("Room metadata changed: %s", metadata)
    session.UpdateRoomMetadata(metadata)
}

func (h *RoomAnalyticsHandler) reportStatus(session *RoomSession, room *lksdk.Room) {
    stats := session.GetStats()
    log.Printf("Room status - Name: %s, Participants: %d, Tracks: %d, Messages: %d, Duration: %s",
        room.Name(),
        stats.ParticipantCount,
        stats.TrackCount,
        stats.DataMessageCount,
        stats.Duration.Round(time.Second),
    )
}
```

### analytics.go

```go
package main

import (
    "context"
    "fmt"
    "log"
    "sync"
    "time"
)

type AnalyticsCollector struct {
    mu       sync.RWMutex
    sessions map[string]*RoomSession
}

type RoomSession struct {
    mu               sync.RWMutex
    roomName         string
    jobID            string
    startTime        time.Time
    endTime          *time.Time
    participants     map[string]*ParticipantInfo
    tracks           map[string]*TrackInfo
    dataMessageCount int
    metadata         string
}

type ParticipantInfo struct {
    SID              string
    Identity         string
    JoinedAt         time.Time
    LeftAt           *time.Time
    ConnectionQuality string
}

type TrackInfo struct {
    SID           string
    Type          string
    ParticipantSID string
    PublishedAt   time.Time
    UnpublishedAt *time.Time
}

type SessionStats struct {
    ParticipantCount int
    TrackCount       int
    DataMessageCount int
    Duration         time.Duration
}

func NewAnalyticsCollector() *AnalyticsCollector {
    return &AnalyticsCollector{
        sessions: make(map[string]*RoomSession),
    }
}

func (a *AnalyticsCollector) CreateSession(roomName, jobID string) *RoomSession {
    session := &RoomSession{
        roomName:     roomName,
        jobID:        jobID,
        startTime:    time.Now(),
        participants: make(map[string]*ParticipantInfo),
        tracks:       make(map[string]*TrackInfo),
    }
    
    a.mu.Lock()
    a.sessions[roomName] = session
    a.mu.Unlock()
    
    return session
}

func (a *AnalyticsCollector) StartReporter(ctx context.Context) {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            a.printReport()
        }
    }
}

func (a *AnalyticsCollector) printReport() {
    a.mu.RLock()
    defer a.mu.RUnlock()
    
    if len(a.sessions) == 0 {
        return
    }
    
    log.Println("=== Analytics Report ===")
    for roomName, session := range a.sessions {
        stats := session.GetStats()
        status := "active"
        if session.endTime != nil {
            status = "ended"
        }
        
        log.Printf("Room: %s (%s) - Participants: %d, Tracks: %d, Messages: %d, Duration: %s",
            roomName,
            status,
            stats.ParticipantCount,
            stats.TrackCount,
            stats.DataMessageCount,
            stats.Duration.Round(time.Second),
        )
    }
    log.Println("=======================")
}

func (a *AnalyticsCollector) PrintSummary() {
    a.mu.RLock()
    defer a.mu.RUnlock()
    
    totalRooms := len(a.sessions)
    totalParticipants := 0
    totalTracks := 0
    totalMessages := 0
    totalDuration := time.Duration(0)
    
    for _, session := range a.sessions {
        stats := session.GetStats()
        totalParticipants += stats.ParticipantCount
        totalTracks += stats.TrackCount
        totalMessages += stats.DataMessageCount
        totalDuration += stats.Duration
    }
    
    log.Println("=== Final Summary ===")
    log.Printf("Total rooms monitored: %d", totalRooms)
    log.Printf("Total participants: %d", totalParticipants)
    log.Printf("Total tracks: %d", totalTracks)
    log.Printf("Total data messages: %d", totalMessages)
    log.Printf("Total monitoring time: %s", totalDuration.Round(time.Second))
    log.Println("====================")
}

// RoomSession methods

func (s *RoomSession) End() {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    now := time.Now()
    s.endTime = &now
    
    // Mark all active participants as left
    for _, p := range s.participants {
        if p.LeftAt == nil {
            p.LeftAt = &now
        }
    }
}

func (s *RoomSession) AddParticipant(sid, identity string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    s.participants[sid] = &ParticipantInfo{
        SID:      sid,
        Identity: identity,
        JoinedAt: time.Now(),
    }
}

func (s *RoomSession) RemoveParticipant(sid string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    if p, exists := s.participants[sid]; exists {
        now := time.Now()
        p.LeftAt = &now
    }
}

func (s *RoomSession) AddTrack(sid, trackType, participantSID string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    s.tracks[sid] = &TrackInfo{
        SID:            sid,
        Type:           trackType,
        ParticipantSID: participantSID,
        PublishedAt:    time.Now(),
    }
}

func (s *RoomSession) RemoveTrack(sid string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    if t, exists := s.tracks[sid]; exists {
        now := time.Now()
        t.UnpublishedAt = &now
    }
}

func (s *RoomSession) IncrementDataMessages() {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.dataMessageCount++
}

func (s *RoomSession) UpdateConnectionQuality(participantSID, quality string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    
    if p, exists := s.participants[participantSID]; exists {
        p.ConnectionQuality = quality
    }
}

func (s *RoomSession) UpdateRoomMetadata(metadata string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.metadata = metadata
}

func (s *RoomSession) GetStats() SessionStats {
    s.mu.RLock()
    defer s.mu.RUnlock()
    
    activeParticipants := 0
    for _, p := range s.participants {
        if p.LeftAt == nil {
            activeParticipants++
        }
    }
    
    activeTracks := 0
    for _, t := range s.tracks {
        if t.UnpublishedAt == nil {
            activeTracks++
        }
    }
    
    duration := time.Since(s.startTime)
    if s.endTime != nil {
        duration = s.endTime.Sub(s.startTime)
    }
    
    return SessionStats{
        ParticipantCount: activeParticipants,
        TrackCount:       activeTracks,
        DataMessageCount: s.dataMessageCount,
        Duration:         duration,
    }
}
```

### config.go

```go
package main

type Config struct {
    LiveKitURL string
    APIKey     string
    APISecret  string
}
```

## Running the Example

1. Set environment variables:
   ```bash
   export LIVEKIT_URL="ws://localhost:7880"
   export LIVEKIT_API_KEY="your-api-key"
   export LIVEKIT_API_SECRET="your-api-secret"
   ```

2. Run the agent:
   ```bash
   go run .
   ```

3. Create a room and connect participants to see the agent in action.

## Expected Output

```
2024/01/15 10:30:00 Starting room analytics agent...
2024/01/15 10:30:01 Starting analytics for room: test-room (Job ID: job-123)
2024/01/15 10:30:05 Participant connected: user1 (SID: PA_xxx)
2024/01/15 10:30:06 Track published: microphone audio by user1 (SID: TR_xxx)
2024/01/15 10:30:07 Track published: camera video by user1 (SID: TR_yyy)
2024/01/15 10:30:15 Participant connected: user2 (SID: PA_yyy)
2024/01/15 10:30:20 Connection quality changed for PA_xxx: excellent
2024/01/15 10:30:30 Room status - Name: test-room, Participants: 2, Tracks: 3, Messages: 0, Duration: 30s
2024/01/15 10:31:00 === Analytics Report ===
2024/01/15 10:31:00 Room: test-room (active) - Participants: 2, Tracks: 3, Messages: 0, Duration: 1m0s
2024/01/15 10:31:00 =======================
```

## Key Concepts Illustrated

1. **Worker Creation**: Basic setup with API credentials and job type filtering
2. **Job Handler**: Implementing the OnJob method to process room jobs
3. **Event Callbacks**: Setting up handlers for all major room events
4. **State Management**: Tracking participants, tracks, and metrics
5. **Graceful Shutdown**: Proper cleanup when the agent stops
6. **Error Handling**: Validating job types and handling edge cases
7. **Logging**: Structured logging of events and periodic status reports

## Extending This Example

- Add database persistence for analytics data
- Implement webhooks to notify external systems
- Add more detailed track quality metrics
- Export metrics to Prometheus
- Add room recording triggers based on events
- Implement participant limit enforcement

## Next Steps

- Try the [Participant Monitoring](participant-monitoring.md) example for participant-focused agents
- See [Media Publisher](media-publisher.md) for publishing audio/video
- Explore [Load-Balanced Workers](load-balanced-workers.md) for scaling