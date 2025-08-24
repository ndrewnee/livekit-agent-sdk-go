# Advanced Features

This guide covers advanced features of the LiveKit Agent SDK including load balancing, job recovery, resource management, and scaling strategies.

## Load Balancing

The SDK provides sophisticated load balancing capabilities to distribute jobs efficiently across workers.

### Load Calculation Strategies

#### 1. Default Load Calculator

Simple job count-based load calculation:

```go
type DefaultLoadCalculator struct{}

func (d *DefaultLoadCalculator) Calculate(metrics LoadMetrics) float32 {
    if metrics.MaxJobs > 0 {
        return float32(metrics.ActiveJobs) / float32(metrics.MaxJobs)
    }
    // For unlimited jobs, use heuristic
    return min(float32(metrics.ActiveJobs)*0.1, 1.0)
}
```

#### 2. CPU/Memory Load Calculator

Combines system resources with job count:

```go
calculator := agent.NewCPUMemoryLoadCalculator()
calculator.JobWeight = 0.4     // 40% weight to job count
calculator.CPUWeight = 0.3     // 30% weight to CPU usage
calculator.MemoryWeight = 0.3  // 30% weight to memory usage

worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    LoadCalculator: calculator,
})
```

#### 3. Predictive Load Calculator

Uses historical data to predict future load:

```go
baseCalculator := agent.NewCPUMemoryLoadCalculator()
predictiveCalc := agent.NewPredictiveLoadCalculator(baseCalculator, 10) // 10 samples

worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    LoadCalculator: predictiveCalc,
})
```

#### 4. Custom Load Calculator

Implement your own load calculation logic:

```go
type CustomLoadCalculator struct {
    // Custom fields
    activeConnections int
    processingRate    float64
    errorRate         float64
}

func (c *CustomLoadCalculator) Calculate(metrics LoadMetrics) float32 {
    // Factor in multiple metrics
    jobLoad := float32(metrics.ActiveJobs) / float32(max(metrics.MaxJobs, 1))
    
    // Consider processing duration
    var avgDuration float64
    for _, duration := range metrics.JobDuration {
        avgDuration += duration.Seconds()
    }
    if len(metrics.JobDuration) > 0 {
        avgDuration /= float64(len(metrics.JobDuration))
    }
    
    // Penalize long-running jobs
    durationFactor := float32(min(avgDuration/300.0, 1.0)) // 5 min threshold
    
    // Consider error rate
    errorFactor := float32(min(c.errorRate*2, 1.0))
    
    // Weighted calculation
    load := jobLoad*0.5 + durationFactor*0.3 + errorFactor*0.2
    
    return min(load, 1.0)
}
```

### Load-Based Job Distribution

The server uses worker load to make assignment decisions:

```go
// Configure load reporting
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    LoadReportInterval: 5 * time.Second,  // How often to report load
    LoadBatcher: agent.NewLoadBatcher(worker, 5*time.Second), // Batch updates
})

// Workers automatically report their load
// Server assigns jobs to workers with lowest load
```

### Multi-Region Load Balancing

Deploy workers across regions for global distribution:

```go
type RegionalWorkerConfig struct {
    Region         string
    ServerURL      string
    WorkerCount    int
    MaxJobsPerWorker int
}

func deployRegionalWorkers(configs []RegionalWorkerConfig) []*agent.Worker {
    var workers []*agent.Worker
    
    for _, config := range configs {
        for i := 0; i < config.WorkerCount; i++ {
            worker := agent.NewWorker(
                config.ServerURL,
                apiKey,
                apiSecret,
                handler,
                agent.WorkerOptions{
                    AgentName: fmt.Sprintf("%s-worker-%d", config.Region, i),
                    JobType: livekit.JobType_JT_ROOM,
                    MaxJobs: config.MaxJobsPerWorker,
                    Metadata: map[string]string{
                        "region": config.Region,
                        "zone":   getAvailabilityZone(),
                    },
                },
            )
            workers = append(workers, worker)
        }
    }
    
    return workers
}
```

## Job Recovery

The SDK provides robust job recovery mechanisms for handling disconnections and failures.

### Basic Recovery Setup

```go
// Create recovery handler
recoveryHandler := &MyRecoveryHandler{}

// Configure worker with recovery
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    RecoveryHandler: recoveryHandler,
    EnableJobRecovery: true,
})
```

### Custom Recovery Handler

```go
type MyRecoveryHandler struct {
    storage         RecoveryStorage
    maxRecoveryTime time.Duration
}

func (h *MyRecoveryHandler) OnJobRecoveryAttempt(ctx context.Context, jobID string, jobState *agent.JobState) bool {
    // Check if job should be recovered
    if time.Since(jobState.StartedAt) > h.maxRecoveryTime {
        log.Printf("Job %s too old for recovery", jobID)
        return false
    }
    
    // Check job type
    if jobState.Type == livekit.JobType_JT_PUBLISHER {
        // Publisher jobs might need fresh state
        return false
    }
    
    // Check if job data is recoverable
    if checkpoint, err := h.storage.LoadCheckpoint(jobID); err == nil {
        return checkpoint.IsRecoverable()
    }
    
    return true
}

func (h *MyRecoveryHandler) OnJobRecovered(ctx context.Context, job *livekit.Job, room *lksdk.Room) {
    log.Printf("Successfully recovered job %s", job.Id)
    
    // Restore application state
    if checkpoint, err := h.storage.LoadCheckpoint(job.Id); err == nil {
        // Resume from checkpoint
        h.restoreFromCheckpoint(checkpoint, room)
    }
    
    // Notify monitoring
    metrics.RecordJobRecovery(job.Id)
}

func (h *MyRecoveryHandler) OnJobRecoveryFailed(ctx context.Context, jobID string, err error) {
    log.Printf("Failed to recover job %s: %v", jobID, err)
    
    // Clean up partial state
    h.storage.DeleteCheckpoint(jobID)
    
    // Alert operations team
    alerts.SendJobRecoveryFailure(jobID, err)
}
```

### Checkpointing for Recovery

Implement checkpointing for stateful jobs:

```go
type CheckpointingHandler struct {
    checkpointInterval time.Duration
    storage           CheckpointStorage
}

func (h *CheckpointingHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Create checkpoint manager
    checkpoint := agent.NewJobCheckpoint(job.Id)
    
    // Load existing checkpoint if recovering
    if existing, err := h.storage.Load(job.Id); err == nil {
        checkpoint = existing
        log.Printf("Loaded checkpoint for job %s", job.Id)
    }
    
    // Periodic checkpoint saving
    ticker := time.NewTicker(h.checkpointInterval)
    defer ticker.Stop()
    
    go func() {
        for {
            select {
            case <-ticker.C:
                h.saveCheckpoint(checkpoint)
            case <-ctx.Done():
                return
            }
        }
    }()
    
    // Process job with checkpointing
    return h.processWithCheckpoint(ctx, job, room, checkpoint)
}

func (h *CheckpointingHandler) processWithCheckpoint(
    ctx context.Context,
    job *livekit.Job,
    room *lksdk.Room,
    checkpoint *agent.JobCheckpoint,
) error {
    // Restore state
    processedCount := 0
    if val, ok := checkpoint.Load("processed_count"); ok {
        processedCount = val.(int)
    }
    
    // Continue processing
    for {
        select {
        case <-ctx.Done():
            return nil
            
        case event := <-room.EventChan():
            processedCount++
            checkpoint.Save("processed_count", processedCount)
            checkpoint.Save("last_event_time", time.Now())
            
            if err := h.processEvent(event); err != nil {
                checkpoint.Save("last_error", err.Error())
                return err
            }
        }
    }
}
```

### Partial Message Recovery

Handle partial WebSocket messages during reconnection:

```go
type RecoverableWebSocketHandler struct {
    partialBuffer *agent.PartialMessageBuffer
}

func (h *RecoverableWebSocketHandler) handleWebSocketReconnection() {
    // Create buffer for partial messages
    h.partialBuffer = agent.NewPartialMessageBuffer(1024 * 1024) // 1MB max
    
    // On message fragment
    onMessageFragment := func(messageType int, data []byte, isLast bool) {
        if err := h.partialBuffer.Append(messageType, data); err != nil {
            log.Printf("Buffer overflow: %v", err)
            h.partialBuffer.Clear()
            return
        }
        
        if isLast {
            // Try to get complete message
            if msgType, msgData, complete := h.partialBuffer.GetComplete(); complete {
                h.processCompleteMessage(msgType, msgData)
                h.partialBuffer.Clear()
            }
        }
    }
}
```

## Resource Management

### Resource Limiter

Prevent resource exhaustion with limits:

```go
// Create resource limiter
limiter := agent.NewResourceLimiter(agent.ResourceLimits{
    MaxMemoryMB:   2048,      // 2GB max memory
    MaxCPUPercent: 80,        // 80% max CPU
    MaxGoroutines: 1000,      // Max concurrent goroutines
    MaxOpenFiles:  500,       // Max file descriptors
})

// Apply to worker
worker := agent.NewWorker(url, key, secret, handler, agent.WorkerOptions{
    ResourceLimiter: limiter,
})

// Or use in handler
func (h *MyHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Check resources before intensive operation
    if err := h.limiter.CheckMemory(); err != nil {
        return &agent.RetryableError{
            Err: err,
            RetryAfter: 30 * time.Second,
        }
    }
    
    // Reserve resources
    reservation, err := h.limiter.Reserve(agent.ResourceRequest{
        MemoryMB:    512,
        CPUPercent:  20,
        Goroutines:  50,
    })
    if err != nil {
        return err
    }
    defer reservation.Release()
    
    // Process with reserved resources
    return h.processIntensiveJob(ctx, job, room)
}
```

### Memory Management

Implement memory-aware processing:

```go
type MemoryAwareHandler struct {
    memoryMonitor *MemoryMonitor
    gcThreshold   uint64
}

func (h *MemoryAwareHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Monitor memory usage
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            stats := h.memoryMonitor.GetStats()
            
            // Force GC if needed
            if stats.Allocated > h.gcThreshold {
                log.Printf("Memory usage high (%d MB), forcing GC", stats.Allocated/1024/1024)
                runtime.GC()
            }
            
            // Reduce processing if memory critical
            if stats.Allocated > stats.Limit*0.9 {
                h.reduceProcessingLoad()
            }
            
        case <-ctx.Done():
            return nil
        }
    }
}
```

### Connection Pooling

Efficiently manage connections:

```go
type ConnectionPool struct {
    mu          sync.RWMutex
    connections map[string]*PooledConnection
    maxPerHost  int
    maxTotal    int
    idleTimeout time.Duration
}

type PooledConnection struct {
    conn        *websocket.Conn
    lastUsed    time.Time
    inUse       bool
}

func (p *ConnectionPool) Get(url string) (*websocket.Conn, error) {
    p.mu.Lock()
    defer p.mu.Unlock()
    
    // Check for existing idle connection
    for key, pc := range p.connections {
        if !pc.inUse && strings.HasPrefix(key, url) {
            pc.inUse = true
            pc.lastUsed = time.Now()
            return pc.conn, nil
        }
    }
    
    // Create new connection if under limit
    if len(p.connections) < p.maxTotal {
        conn, err := p.createConnection(url)
        if err != nil {
            return nil, err
        }
        
        key := fmt.Sprintf("%s-%d", url, time.Now().UnixNano())
        p.connections[key] = &PooledConnection{
            conn:     conn,
            lastUsed: time.Now(),
            inUse:    true,
        }
        
        return conn, nil
    }
    
    return nil, errors.New("connection pool exhausted")
}
```

## Auto-Scaling

### Horizontal Scaling

Scale workers based on load:

```go
type AutoScaler struct {
    minWorkers      int
    maxWorkers      int
    targetLoad      float32
    scaleUpThreshold float32
    scaleDownThreshold float32
    cooldownPeriod  time.Duration
    lastScaleTime   time.Time
    workers         []*agent.Worker
}

func (as *AutoScaler) Monitor(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            as.evaluateScaling()
        case <-ctx.Done():
            return
        }
    }
}

func (as *AutoScaler) evaluateScaling() {
    // Check cooldown
    if time.Since(as.lastScaleTime) < as.cooldownPeriod {
        return
    }
    
    // Calculate average load
    totalLoad := float32(0)
    activeWorkers := 0
    
    for _, worker := range as.workers {
        if worker.IsActive() {
            totalLoad += worker.GetLoad()
            activeWorkers++
        }
    }
    
    if activeWorkers == 0 {
        return
    }
    
    avgLoad := totalLoad / float32(activeWorkers)
    
    // Scale up
    if avgLoad > as.scaleUpThreshold && len(as.workers) < as.maxWorkers {
        as.scaleUp()
    }
    
    // Scale down
    if avgLoad < as.scaleDownThreshold && len(as.workers) > as.minWorkers {
        as.scaleDown()
    }
}

func (as *AutoScaler) scaleUp() {
    newWorker := as.createWorker()
    
    ctx, cancel := context.WithCancel(context.Background())
    go func() {
        if err := newWorker.Start(ctx); err != nil {
            log.Printf("Failed to start new worker: %v", err)
        }
    }()
    
    as.workers = append(as.workers, newWorker)
    as.lastScaleTime = time.Now()
    
    log.Printf("Scaled up to %d workers", len(as.workers))
}
```

### Kubernetes-Based Scaling

Use Kubernetes HPA for scaling:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: livekit-agent-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: livekit-agent
  minReplicas: 2
  maxReplicas: 20
  metrics:
  - type: Pods
    pods:
      metric:
        name: worker_load
      target:
        type: AverageValue
        averageValue: "0.7"
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
```

Expose metrics for HPA:

```go
func exposeMetricsForKubernetes(worker *agent.Worker) {
    http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
        metrics := worker.GetMetrics()
        
        // Prometheus format
        fmt.Fprintf(w, "# HELP worker_load Current worker load\n")
        fmt.Fprintf(w, "# TYPE worker_load gauge\n")
        fmt.Fprintf(w, "worker_load %f\n", metrics.Load)
        
        fmt.Fprintf(w, "# HELP active_jobs Number of active jobs\n")
        fmt.Fprintf(w, "# TYPE active_jobs gauge\n")
        fmt.Fprintf(w, "active_jobs %d\n", metrics.ActiveJobs)
    })
    
    log.Fatal(http.ListenAndServe(":9090", nil))
}
```

## Job Queuing

### Priority Queue Implementation

```go
type PriorityJobQueue struct {
    queue    *agent.JobQueue
    worker   *agent.Worker
    processor *JobProcessor
}

func NewPriorityJobQueue(worker *agent.Worker) *PriorityJobQueue {
    // Custom priority calculator
    priorityCalc := func(job *agent.JobQueueItem) agent.JobPriority {
        // Parse job metadata
        var metadata map[string]interface{}
        json.Unmarshal([]byte(job.Job.Metadata), &metadata)
        
        // Determine priority
        if priority, ok := metadata["priority"].(string); ok {
            switch priority {
            case "urgent":
                return agent.JobPriorityUrgent
            case "high":
                return agent.JobPriorityHigh
            case "low":
                return agent.JobPriorityLow
            }
        }
        
        // Default priority based on job type
        switch job.Job.Type {
        case livekit.JobType_JT_PUBLISHER:
            return agent.JobPriorityHigh
        default:
            return agent.JobPriorityNormal
        }
    }
    
    queue := agent.NewJobQueue(1000, priorityCalc)
    
    return &PriorityJobQueue{
        queue:  queue,
        worker: worker,
    }
}

func (pjq *PriorityJobQueue) Start(ctx context.Context) {
    // Process queue
    for {
        select {
        case <-ctx.Done():
            return
            
        default:
            item, err := pjq.queue.Dequeue(ctx)
            if err != nil {
                continue
            }
            
            // Check worker capacity
            if pjq.worker.CanAcceptJob() {
                go pjq.processJob(item)
            } else {
                // Re-queue if at capacity
                pjq.queue.Enqueue(item)
                time.Sleep(100 * time.Millisecond)
            }
        }
    }
}
```

### Distributed Queue

Use Redis for distributed job queuing:

```go
type RedisJobQueue struct {
    client      *redis.Client
    queueKey    string
    processingKey string
    workerID    string
}

func (q *RedisJobQueue) Enqueue(job *agent.JobQueueItem) error {
    data, err := json.Marshal(job)
    if err != nil {
        return err
    }
    
    // Add to sorted set with priority as score
    score := float64(job.Priority) + float64(job.EnqueueTime.UnixNano())/1e9
    
    return q.client.ZAdd(context.Background(), q.queueKey, &redis.Z{
        Score:  score,
        Member: data,
    }).Err()
}

func (q *RedisJobQueue) Dequeue(ctx context.Context) (*agent.JobQueueItem, error) {
    // Atomic pop from sorted set
    script := `
        local item = redis.call('zpopmin', KEYS[1])
        if #item > 0 then
            redis.call('hset', KEYS[2], item[1], ARGV[1])
            return item[1]
        end
        return nil
    `
    
    result, err := q.client.Eval(ctx, script, 
        []string{q.queueKey, q.processingKey}, 
        q.workerID).Result()
    
    if err != nil || result == nil {
        return nil, err
    }
    
    var item agent.JobQueueItem
    if err := json.Unmarshal([]byte(result.(string)), &item); err != nil {
        return nil, err
    }
    
    return &item, nil
}
```

## Monitoring and Observability

### Custom Metrics Collection

```go
type MetricsCollector struct {
    jobsStarted     prometheus.Counter
    jobsCompleted   prometheus.Counter
    jobsFailed      prometheus.Counter
    jobDuration     prometheus.Histogram
    activeJobs      prometheus.Gauge
    workerLoad      prometheus.Gauge
}

func NewMetricsCollector() *MetricsCollector {
    mc := &MetricsCollector{
        jobsStarted: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "livekit_agent_jobs_started_total",
            Help: "Total number of jobs started",
        }),
        jobsCompleted: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "livekit_agent_jobs_completed_total",
            Help: "Total number of jobs completed successfully",
        }),
        jobsFailed: prometheus.NewCounter(prometheus.CounterOpts{
            Name: "livekit_agent_jobs_failed_total",
            Help: "Total number of jobs failed",
        }),
        jobDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
            Name:    "livekit_agent_job_duration_seconds",
            Help:    "Job processing duration in seconds",
            Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
        }),
        activeJobs: prometheus.NewGauge(prometheus.GaugeOpts{
            Name: "livekit_agent_active_jobs",
            Help: "Number of currently active jobs",
        }),
        workerLoad: prometheus.NewGauge(prometheus.GaugeOpts{
            Name: "livekit_agent_worker_load",
            Help: "Current worker load (0-1)",
        }),
    }
    
    // Register metrics
    prometheus.MustRegister(
        mc.jobsStarted,
        mc.jobsCompleted,
        mc.jobsFailed,
        mc.jobDuration,
        mc.activeJobs,
        mc.workerLoad,
    )
    
    return mc
}
```

### Distributed Tracing

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/trace"
)

type TracingHandler struct {
    tracer trace.Tracer
    handler agent.JobHandler
}

func (h *TracingHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Start span
    ctx, span := h.tracer.Start(ctx, "job.process",
        trace.WithAttributes(
            attribute.String("job.id", job.Id),
            attribute.String("job.type", job.Type.String()),
            attribute.String("room.name", room.Name()),
        ),
    )
    defer span.End()
    
    // Record job start
    span.AddEvent("job.started")
    
    // Process job
    err := h.handler.OnJobAssigned(ctx, job, room)
    
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
    } else {
        span.SetStatus(codes.Ok, "Job completed successfully")
    }
    
    return err
}
```

## Security Features

### JWT Token Rotation

```go
type TokenRotator struct {
    apiKey       string
    apiSecret    string
    rotationInterval time.Duration
    currentToken string
    mu           sync.RWMutex
}

func (tr *TokenRotator) Start(ctx context.Context) {
    // Initial token
    tr.refreshToken()
    
    ticker := time.NewTicker(tr.rotationInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            tr.refreshToken()
        case <-ctx.Done():
            return
        }
    }
}

func (tr *TokenRotator) refreshToken() {
    at := auth.NewAccessToken(tr.apiKey, tr.apiSecret)
    
    grant := &auth.VideoGrant{
        RoomJoin: true,
    }
    at.AddGrant(grant)
    at.SetValidFor(tr.rotationInterval + time.Minute) // Add buffer
    
    token, err := at.ToJWT()
    if err != nil {
        log.Printf("Failed to generate token: %v", err)
        return
    }
    
    tr.mu.Lock()
    tr.currentToken = token
    tr.mu.Unlock()
}
```

### Rate Limiting

```go
type RateLimitedHandler struct {
    handler agent.JobHandler
    limiter *rate.Limiter
    burstLimiter map[string]*rate.Limiter
    mu      sync.RWMutex
}

func (h *RateLimitedHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    // Global rate limit
    if err := h.limiter.Wait(ctx); err != nil {
        return &agent.RetryableError{
            Err: err,
            RetryAfter: time.Second,
        }
    }
    
    // Per-room rate limit
    roomLimiter := h.getRoomLimiter(room.Name())
    if err := roomLimiter.Wait(ctx); err != nil {
        return &agent.RetryableError{
            Err: err,
            RetryAfter: time.Second,
        }
    }
    
    return h.handler.OnJobAssigned(ctx, job, room)
}

func (h *RateLimitedHandler) getRoomLimiter(roomName string) *rate.Limiter {
    h.mu.RLock()
    limiter, exists := h.burstLimiter[roomName]
    h.mu.RUnlock()
    
    if exists {
        return limiter
    }
    
    h.mu.Lock()
    defer h.mu.Unlock()
    
    // Double-check
    if limiter, exists := h.burstLimiter[roomName]; exists {
        return limiter
    }
    
    // Create new limiter for room
    limiter = rate.NewLimiter(rate.Every(100*time.Millisecond), 10)
    h.burstLimiter[roomName] = limiter
    
    return limiter
}
```

## Best Practices

1. **Load Calculation**
   - Use appropriate weights for your use case
   - Consider both system resources and application metrics
   - Update load frequently but batch updates

2. **Job Recovery**
   - Implement checkpointing for long-running jobs
   - Set reasonable recovery timeouts
   - Clean up failed recovery attempts

3. **Resource Management**
   - Set conservative limits initially
   - Monitor actual usage patterns
   - Implement graceful degradation

4. **Auto-Scaling**
   - Use predictive scaling when possible
   - Set appropriate cooldown periods
   - Monitor scaling events

5. **Monitoring**
   - Export comprehensive metrics
   - Implement distributed tracing
   - Set up alerting for anomalies

## Next Steps

- Learn about [Media Processing](media-processing.md) capabilities
- Explore complete [Examples](examples/README.md)
- Review the [API Reference](api-reference.md)
- Check [Troubleshooting](troubleshooting.md) for common issues