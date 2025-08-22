# Troubleshooting Guide

Common issues and solutions when working with the LiveKit Agent SDK.

## Table of Contents

- [Connection Issues](#connection-issues)
- [Job Processing Problems](#job-processing-problems)
- [Performance Issues](#performance-issues)
- [Media Processing Problems](#media-processing-problems)
- [Deployment Issues](#deployment-issues)
- [Recovery and Resilience](#recovery-and-resilience)
- [Debugging Tools](#debugging-tools)
- [Common Error Codes](#common-error-codes)

## Connection Issues

### Agent Can't Connect to LiveKit Server

**Symptoms:**
- Worker fails to start with connection errors
- "failed to connect to LiveKit server" messages
- WebSocket connection timeouts

**Solutions:**

1. **Verify Server URL Format**
   ```go
   // Correct formats
   url := "ws://localhost:7880"       // Local development
   url := "wss://my-server.com"       // Production with TLS
   
   // Incorrect formats (will fail)
   url := "http://localhost:7880"     // Wrong protocol
   url := "localhost:7880"            // Missing protocol
   ```

2. **Check API Credentials**
   ```go
   // Verify credentials are set
   if apiKey == "" || apiSecret == "" {
       log.Fatal("API key and secret are required")
   }
   
   // Test credentials with server SDK
   client := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)
   rooms, err := client.ListRooms(context.Background(), &livekit.ListRoomsRequest{})
   if err != nil {
       log.Fatal("Invalid credentials or server unavailable:", err)
   }
   ```

3. **Network Connectivity**
   ```bash
   # Test basic connectivity
   curl -I http://localhost:7880/health
   
   # Test WebSocket endpoint
   wscat -c ws://localhost:7880
   ```

4. **Firewall and Proxy Issues**
   ```go
   // Configure proxy if needed
   import "net/http"
   import "net/url"
   
   proxyURL, _ := url.Parse("http://proxy:8080")
   transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
   
   // Use with WebSocket dialer
   dialer := &websocket.Dialer{
       NetDial: transport.Dial,
   }
   ```

### Connection Drops Frequently

**Symptoms:**
- Frequent reconnection attempts
- Jobs being terminated unexpectedly
- "connection lost" errors in logs

**Solutions:**

1. **Network Stability**
   ```go
   // Enable connection monitoring
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       HealthCheckInterval: 30 * time.Second,  // More frequent health checks
   })
   ```

2. **Keepalive Configuration**
   ```go
   // Custom WebSocket keepalive
   dialer := &websocket.Dialer{
       HandshakeTimeout: 45 * time.Second,
       EnableCompression: false,
   }
   ```

3. **Retry Logic**
   ```go
   // Implement exponential backoff
   type RetryConfig struct {
       MaxRetries int
       BaseDelay  time.Duration
       MaxDelay   time.Duration
   }
   
   func connectWithRetry(config RetryConfig) error {
       for attempt := 0; attempt < config.MaxRetries; attempt++ {
           if err := worker.Start(ctx); err == nil {
               return nil
           }
           
           delay := time.Duration(attempt) * config.BaseDelay
           if delay > config.MaxDelay {
               delay = config.MaxDelay
           }
           time.Sleep(delay)
       }
       return errors.New("max retries exceeded")
   }
   ```

## Job Processing Problems

### Agent Not Receiving Jobs

**Symptoms:**
- Worker connects successfully but no jobs are assigned
- "waiting for jobs" messages persist
- Jobs appear in LiveKit but not processed

**Solutions:**

1. **Job Type Mismatch**
   ```go
   // Ensure job types match
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       JobTypes: []livekit.JobType{
           livekit.JobType_JT_ROOM,        // Must match dispatched jobs
           livekit.JobType_JT_PARTICIPANT,
       },
   })
   ```

2. **Namespace Configuration**
   ```go
   // Check namespace matches
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       Namespace: "production",  // Must match job namespace
   })
   ```

3. **Agent Registration**
   ```go
   // Verify agent name is unique and descriptive
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       AgentName: "my-unique-agent-v2",  // Should be unique
   })
   ```

4. **Load Balancing Issues**
   ```go
   // Check load calculation
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       LoadCalculator: &CustomLoadCalculator{
           Calculator: func(stats WorkerStats) float64 {
               log.Printf("Current load: %f", stats.CurrentLoad)
               return stats.CurrentLoad
           },
       },
   })
   ```

### Jobs Timing Out

**Symptoms:**
- Jobs start but terminate before completion
- Context deadline exceeded errors
- Partial job execution

**Solutions:**

1. **Respect Context Cancellation**
   ```go
   func (h *MyHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
       for {
           select {
           case <-ctx.Done():
               log.Println("Job cancelled, cleaning up...")
               return ctx.Err()  // Return context error
           case work := <-workChan:
               // Process work...
           }
       }
   }
   ```

2. **Increase Timeouts**
   ```go
   // Configure longer shutdown timeout
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       ShutdownTimeout: 5 * time.Minute,  // Allow more cleanup time
   })
   ```

3. **Break Down Long Tasks**
   ```go
   func (h *MyHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
       // Process in chunks with context checks
       for i := 0; i < totalWork; i += chunkSize {
           select {
           case <-ctx.Done():
               return ctx.Err()
           default:
               // Process chunk
               processChunk(i, min(i+chunkSize, totalWork))
           }
       }
       return nil
   }
   ```

### Memory Leaks in Job Handlers

**Symptoms:**
- Memory usage continuously increases
- Out of memory errors
- Slow performance over time

**Solutions:**

1. **Proper Resource Cleanup**
   ```go
   func (h *MyHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
       // Use defer for cleanup
       resources := acquireResources()
       defer resources.Release()
       
       // Close channels
       dataChan := make(chan []byte, 100)
       defer close(dataChan)
       
       // Cancel goroutines
       workerCtx, cancel := context.WithCancel(ctx)
       defer cancel()
       
       return processJob(workerCtx, job, room)
   }
   ```

2. **Track Subscription Cleanup**
   ```go
   func (h *MyHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
       var subscriptions []*lksdk.RemoteTrackPublication
       
       defer func() {
           // Unsubscribe from all tracks
           for _, pub := range subscriptions {
               pub.SetSubscribed(false)
           }
       }()
       
       // Subscribe to tracks
       for _, p := range room.GetRemoteParticipants() {
           for _, pub := range p.TrackPublications() {
               pub.SetSubscribed(true)
               subscriptions = append(subscriptions, pub)
           }
       }
       
       return nil
   }
   ```

3. **Memory Monitoring**
   ```go
   import "runtime"
   
   func (h *MyHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
       var m runtime.MemStats
       runtime.ReadMemStats(&m)
       log.Printf("Memory before job: %d KB", m.Alloc/1024)
       
       defer func() {
           runtime.ReadMemStats(&m)
           log.Printf("Memory after job: %d KB", m.Alloc/1024)
           runtime.GC() // Force garbage collection
       }()
       
       return processJob(ctx, job, room)
   }
   ```

## Performance Issues

### High CPU Usage

**Symptoms:**
- CPU usage consistently above 80%
- Slow job processing
- System becomes unresponsive

**Solutions:**

1. **Resource Limits**
   ```go
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       ResourceLimiter: agent.NewResourceLimiter(agent.ResourceLimits{
           MaxCPUPercent: 70,  // Limit CPU usage
           OnCPULimit: func() {
               log.Println("CPU limit reached, throttling...")
               // Implement throttling logic
           },
       }),
   })
   ```

2. **Concurrent Job Limiting**
   ```go
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       MaxConcurrentJobs: 2,  // Reduce concurrent jobs
   })
   ```

3. **Optimize Media Processing**
   ```go
   // Use buffered channels to prevent blocking
   processChan := make(chan AudioFrame, 1000)
   
   // Process in batches
   var batch []AudioFrame
   for frame := range processChan {
       batch = append(batch, frame)
       if len(batch) >= batchSize {
           processBatch(batch)
           batch = batch[:0]  // Reset slice
       }
   }
   ```

### High Memory Usage

**Symptoms:**
- Memory usage grows continuously
- Out of memory crashes
- Garbage collection pauses

**Solutions:**

1. **Memory Limits**
   ```go
   worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
       ResourceLimiter: agent.NewResourceLimiter(agent.ResourceLimits{
           MaxMemoryMB: 1024,  // Limit memory usage
           OnMemoryLimit: func() {
               runtime.GC()  // Force garbage collection
           },
       }),
   })
   ```

2. **Buffer Pool Pattern**
   ```go
   var audioBufferPool = sync.Pool{
       New: func() interface{} {
           return make([]byte, 4800)  // Standard audio buffer size
       },
   }
   
   func processAudio(data []byte) {
       buf := audioBufferPool.Get().([]byte)
       defer audioBufferPool.Put(buf)
       
       // Use buf for processing...
   }
   ```

3. **Streaming Processing**
   ```go
   // Process data in streams instead of loading everything
   func processLargeFile(reader io.Reader) error {
       scanner := bufio.NewScanner(reader)
       scanner.Buffer(make([]byte, 4096), 64*1024)  // 64KB max line
       
       for scanner.Scan() {
           if err := processLine(scanner.Bytes()); err != nil {
               return err
           }
       }
       return scanner.Err()
   }
   ```

## Media Processing Problems

### Audio/Video Sync Issues

**Symptoms:**
- Audio and video are out of sync
- Choppy playback
- Frame drops

**Solutions:**

1. **Proper Timestamp Handling**
   ```go
   func (p *MediaProcessor) processFrame(frame *MediaFrame) error {
       // Use wall clock time for synchronization
       frame.Timestamp = time.Now()
       
       // Maintain consistent intervals
       expectedInterval := time.Second / time.Duration(frameRate)
       actualInterval := frame.Timestamp.Sub(p.lastFrameTime)
       
       if actualInterval < expectedInterval {
           time.Sleep(expectedInterval - actualInterval)
       }
       
       p.lastFrameTime = frame.Timestamp
       return nil
   }
   ```

2. **Buffer Management**
   ```go
   type SyncBuffer struct {
       audioFrames []AudioFrame
       videoFrames []VideoFrame
       maxDelay    time.Duration
   }
   
   func (b *SyncBuffer) AddAudioFrame(frame AudioFrame) {
       b.audioFrames = append(b.audioFrames, frame)
       b.trySync()
   }
   
   func (b *SyncBuffer) trySync() {
       // Match audio and video frames by timestamp
       for len(b.audioFrames) > 0 && len(b.videoFrames) > 0 {
           audio := b.audioFrames[0]
           video := b.videoFrames[0]
           
           if abs(audio.Timestamp.Sub(video.Timestamp)) < b.maxDelay {
               // Frames are synchronized, output them
               b.outputFrame(audio, video)
               b.audioFrames = b.audioFrames[1:]
               b.videoFrames = b.videoFrames[1:]
           } else if audio.Timestamp.Before(video.Timestamp) {
               b.audioFrames = b.audioFrames[1:]
           } else {
               b.videoFrames = b.videoFrames[1:]
           }
       }
   }
   ```

### Poor Audio Quality

**Symptoms:**
- Distorted or choppy audio
- Audio dropouts
- Echo or feedback

**Solutions:**

1. **Sample Rate Matching**
   ```go
   // Ensure consistent sample rate
   const StandardSampleRate = 48000
   
   func normalizeAudioFormat(samples []int16, inputSampleRate int) []int16 {
       if inputSampleRate == StandardSampleRate {
           return samples
       }
       
       // Resample to standard rate
       return resample(samples, inputSampleRate, StandardSampleRate)
   }
   ```

2. **Buffer Size Optimization**
   ```go
   // Use appropriate buffer sizes
   const (
       AudioBufferSizeMs = 20  // 20ms buffers for low latency
       SamplesPerBuffer  = StandardSampleRate * AudioBufferSizeMs / 1000
   )
   
   audioBuffer := make([]int16, SamplesPerBuffer)
   ```

3. **Echo Cancellation**
   ```go
   type EchoCanceller struct {
       playbackHistory []int16
       maxDelay        int
   }
   
   func (ec *EchoCanceller) ProcessAudio(input []int16) []int16 {
       // Simple echo cancellation
       for i, sample := range input {
           if len(ec.playbackHistory) > ec.maxDelay {
               echo := ec.playbackHistory[len(ec.playbackHistory)-ec.maxDelay]
               input[i] = sample - int16(float64(echo)*0.3)  // Reduce echo by 30%
           }
       }
       
       ec.playbackHistory = append(ec.playbackHistory, input...)
       if len(ec.playbackHistory) > ec.maxDelay*2 {
           ec.playbackHistory = ec.playbackHistory[len(input):]
       }
       
       return input
   }
   ```

## Deployment Issues

### Docker Container Problems

**Symptoms:**
- Agent doesn't start in container
- Network connectivity issues
- Permission errors

**Solutions:**

1. **Dockerfile Optimization**
   ```dockerfile
   FROM golang:1.21-alpine AS builder
   
   WORKDIR /app
   COPY go.mod go.sum ./
   RUN go mod download
   
   COPY . .
   RUN CGO_ENABLED=0 GOOS=linux go build -o agent main.go
   
   FROM alpine:latest
   RUN apk --no-cache add ca-certificates tzdata
   WORKDIR /root/
   
   COPY --from=builder /app/agent .
   
   # Run as non-root user
   RUN adduser -D -s /bin/sh appuser
   USER appuser
   
   CMD ["./agent"]
   ```

2. **Network Configuration**
   ```yaml
   # docker-compose.yml
   version: '3.8'
   services:
     livekit-agent:
       build: .
       environment:
         - LIVEKIT_URL=ws://livekit-server:7880
         - LIVEKIT_API_KEY=${LIVEKIT_API_KEY}
         - LIVEKIT_API_SECRET=${LIVEKIT_API_SECRET}
       networks:
         - livekit-network
       depends_on:
         - livekit-server
   
   networks:
     livekit-network:
       driver: bridge
   ```

3. **Health Checks**
   ```dockerfile
   HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
     CMD curl -f http://localhost:8080/health || exit 1
   ```

### Kubernetes Deployment Issues

**Symptoms:**
- Pods failing to start
- Service discovery problems
- Resource limits exceeded

**Solutions:**

1. **Resource Configuration**
   ```yaml
   apiVersion: apps/v1
   kind: Deployment
   metadata:
     name: livekit-agent
   spec:
     replicas: 3
     template:
       spec:
         containers:
         - name: agent
           image: livekit-agent:latest
           resources:
             requests:
               cpu: 100m
               memory: 256Mi
             limits:
               cpu: 500m
               memory: 1Gi
           env:
           - name: LIVEKIT_URL
             value: "ws://livekit-server:7880"
           livenessProbe:
             httpGet:
               path: /health
               port: 8080
             initialDelaySeconds: 30
             periodSeconds: 10
   ```

2. **Service Discovery**
   ```yaml
   apiVersion: v1
   kind: Service
   metadata:
     name: livekit-server
   spec:
     selector:
       app: livekit-server
     ports:
     - port: 7880
       targetPort: 7880
   ```

## Recovery and Resilience

### Job Recovery Failures

**Symptoms:**
- Jobs not recovering after failures
- Duplicate job executions
- Lost job state

**Solutions:**

1. **Custom Recovery Handler**
   ```go
   type MyRecoveryHandler struct {
       maxRetries int
       backoff    time.Duration
   }
   
   func (h *MyRecoveryHandler) OnJobFailed(job *livekit.Job, err error) agent.RecoveryAction {
       log.Printf("Job %s failed: %v", job.Id, err)
       
       // Check if recoverable
       if isRecoverable(err) && job.RetryCount < h.maxRetries {
           return agent.RecoveryActionRetry
       }
       
       return agent.RecoveryActionSkip
   }
   
   func isRecoverable(err error) bool {
       return !errors.Is(err, context.Canceled) &&
              !errors.Is(err, agent.ErrInvalidJobType)
   }
   ```

2. **State Persistence**
   ```go
   type StatefulHandler struct {
       storage StateStorage
   }
   
   func (h *StatefulHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
       // Load previous state
       state, err := h.storage.LoadState(job.Id)
       if err != nil {
           state = NewInitialState()
       }
       
       // Save state periodically
       ticker := time.NewTicker(30 * time.Second)
       defer ticker.Stop()
       
       go func() {
           for {
               select {
               case <-ticker.C:
                   h.storage.SaveState(job.Id, state)
               case <-ctx.Done():
                   return
               }
           }
       }()
       
       return h.processWithState(ctx, job, room, state)
   }
   ```

### Circuit Breaker Pattern

**Symptoms:**
- Cascading failures
- Resource exhaustion from retries
- System instability

**Solutions:**

```go
type CircuitBreaker struct {
    threshold    int
    timeout      time.Duration
    failures     int
    lastFailTime time.Time
    state        CBState
}

type CBState int
const (
    CBClosed CBState = iota
    CBOpen
    CBHalfOpen
)

func (cb *CircuitBreaker) Call(fn func() error) error {
    if cb.state == CBOpen {
        if time.Since(cb.lastFailTime) > cb.timeout {
            cb.state = CBHalfOpen
        } else {
            return errors.New("circuit breaker open")
        }
    }
    
    err := fn()
    if err != nil {
        cb.failures++
        cb.lastFailTime = time.Now()
        
        if cb.failures >= cb.threshold {
            cb.state = CBOpen
        }
        return err
    }
    
    // Success - reset circuit breaker
    cb.failures = 0
    cb.state = CBClosed
    return nil
}
```

## Debugging Tools

### Enable Debug Logging

```go
import "github.com/livekit/protocol/logger"

func init() {
    logger.InitializeLogger("debug", "development")
}

// In your handler
func (h *MyHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    logger := logger.GetLogger().WithValues(
        "job_id", job.Id,
        "room", room.Name(),
    )
    
    logger.Info("Starting job processing")
    
    // Process job...
    
    logger.Info("Job completed successfully")
    return nil
}
```

### WebSocket Traffic Monitoring

```go
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    WebSocketDebug: true,  // Enables WebSocket message logging
})
```

### Performance Profiling

```go
import _ "net/http/pprof"

func init() {
    go func() {
        log.Println(http.ListenAndServe("localhost:6060", nil))
    }()
}

// Access profiles at:
// http://localhost:6060/debug/pprof/
// http://localhost:6060/debug/pprof/heap
// http://localhost:6060/debug/pprof/goroutine
```

### Custom Metrics

```go
type MetricsHandler struct {
    base    agent.JobHandler
    metrics *prometheus.Registry
}

func (h *MetricsHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    start := time.Now()
    
    defer func() {
        duration := time.Since(start)
        h.recordJobDuration(job.Type.String(), duration)
    }()
    
    return h.base.OnJob(ctx, job, room)
}
```

## Common Error Codes

### Connection Errors
- `1006`: WebSocket connection closed abnormally
- `1001`: Server going away
- `1011`: Server error

### Authentication Errors
- `401`: Invalid API key or secret
- `403`: Insufficient permissions

### Job Processing Errors
- `INVALID_REQUEST`: Malformed job data
- `RESOURCE_EXHAUSTED`: System resource limits exceeded
- `DEADLINE_EXCEEDED`: Job timeout

### Recovery Actions
- `RETRY`: Retry the operation with backoff
- `SKIP`: Skip the failed operation
- `TERMINATE`: Shut down the worker
- `BACKOFF`: Apply exponential backoff

## Getting Help

If you're still experiencing issues:

1. Check the [examples](examples/) for reference implementations
2. Review the [API documentation](api-reference.md)
3. Join the [LiveKit Community Slack](https://livekit.io/slack)
4. File an issue on [GitHub](https://github.com/livekit/agent-sdk-go/issues)

Include the following information when seeking help:
- Agent SDK version
- Go version
- LiveKit server version
- Complete error messages and stack traces
- Minimal reproduction code
- Network and deployment environment details