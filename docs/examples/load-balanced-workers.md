# Load-Balanced Workers Example

A comprehensive example demonstrating how to deploy multiple workers with load balancing, auto-scaling, health monitoring, and high availability. This example shows production-ready deployment patterns for the LiveKit Agent SDK.

## What This Example Demonstrates

- Multiple worker deployment with different specializations
- Custom load calculation and distribution strategies
- Auto-scaling based on system metrics and job queue depth
- Health monitoring and automatic failover
- Graceful worker shutdown and job migration
- Redis-based coordination and state sharing
- Prometheus metrics integration
- Kubernetes deployment patterns

## Complete Code

### main.go

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"
    "sync"
    "syscall"
    "time"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    "github.com/livekit/protocol/logger"
    lksdk "github.com/livekit/server-sdk-go/v2"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "github.com/go-redis/redis/v8"
    "net/http"
)

func main() {
    // Initialize logger
    logger.InitializeLogger("info", "production")
    
    // Load configuration
    config := loadConfig()
    
    // Create services
    redisClient := createRedisClient(config.RedisURL)
    defer redisClient.Close()
    
    coordinator := NewWorkerCoordinator(redisClient, config)
    healthMonitor := NewHealthMonitor(config)
    metricsCollector := NewMetricsCollector()
    autoScaler := NewAutoScaler(coordinator, metricsCollector, config)
    
    // Register Prometheus metrics
    prometheus.MustRegister(metricsCollector.GetCollectors()...)
    
    // Start metrics server
    go startMetricsServer(config.MetricsPort)
    
    // Start health monitor
    go healthMonitor.Start(context.Background())
    
    // Start auto-scaler
    go autoScaler.Start(context.Background())
    
    // Create worker pool based on configuration
    workerPool := NewWorkerPool(coordinator, healthMonitor, metricsCollector, config)
    
    // Set up graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    // Start worker pool
    errChan := make(chan error, 1)
    go func() {
        log.Printf("Starting worker pool with %d initial workers...", config.InitialWorkerCount)
        errChan <- workerPool.Start(ctx)
    }()
    
    // Wait for shutdown signal or error
    select {
    case sig := <-sigChan:
        log.Printf("Received signal %v, shutting down...", sig)
        cancel()
        
        // Graceful shutdown with timeout
        shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
        defer shutdownCancel()
        
        if err := workerPool.Shutdown(shutdownCtx); err != nil {
            log.Printf("Error during shutdown: %v", err)
        }
        
    case err := <-errChan:
        if err != nil {
            log.Fatal("Worker pool error:", err)
        }
    }
    
    log.Println("Load-balanced workers stopped")
}

func loadConfig() *Config {
    return &Config{
        LiveKitURL:         mustGetEnv("LIVEKIT_URL"),
        APIKey:            mustGetEnv("LIVEKIT_API_KEY"),
        APISecret:         mustGetEnv("LIVEKIT_API_SECRET"),
        RedisURL:          getEnv("REDIS_URL", "redis://localhost:6379"),
        
        InitialWorkerCount: getEnvInt("INITIAL_WORKER_COUNT", 3),
        MinWorkerCount:     getEnvInt("MIN_WORKER_COUNT", 2),
        MaxWorkerCount:     getEnvInt("MAX_WORKER_COUNT", 20),
        
        LoadThresholdHigh:  getEnvFloat("LOAD_THRESHOLD_HIGH", 0.8),
        LoadThresholdLow:   getEnvFloat("LOAD_THRESHOLD_LOW", 0.3),
        
        ScaleUpCooldown:    getEnvDuration("SCALE_UP_COOLDOWN", 2*time.Minute),
        ScaleDownCooldown:  getEnvDuration("SCALE_DOWN_COOLDOWN", 5*time.Minute),
        
        HealthCheckInterval: getEnvDuration("HEALTH_CHECK_INTERVAL", 30*time.Second),
        MetricsPort:        getEnvInt("METRICS_PORT", 9090),
        
        WorkerTypes: []WorkerTypeConfig{
            {
                Type:        "room-processor",
                JobTypes:    []livekit.JobType{livekit.JobType_JT_ROOM},
                MaxJobs:     getEnvInt("ROOM_WORKER_MAX_JOBS", 5),
                MinCount:    1,
                MaxCount:    10,
                CPUWeight:   0.4,
                MemWeight:   0.3,
                JobWeight:   0.3,
            },
            {
                Type:        "participant-monitor",
                JobTypes:    []livekit.JobType{livekit.JobType_JT_PARTICIPANT},
                MaxJobs:     getEnvInt("PARTICIPANT_WORKER_MAX_JOBS", 20),
                MinCount:    1,
                MaxCount:    15,
                CPUWeight:   0.3,
                MemWeight:   0.2,
                JobWeight:   0.5,
            },
            {
                Type:        "media-publisher",
                JobTypes:    []livekit.JobType{livekit.JobType_JT_PUBLISHER},
                MaxJobs:     getEnvInt("PUBLISHER_WORKER_MAX_JOBS", 3),
                MinCount:    1,
                MaxCount:    8,
                CPUWeight:   0.5,
                MemWeight:   0.4,
                JobWeight:   0.1,
            },
        },
    }
}

func createRedisClient(redisURL string) *redis.Client {
    opts, err := redis.ParseURL(redisURL)
    if err != nil {
        log.Fatalf("Invalid Redis URL: %v", err)
    }
    
    client := redis.NewClient(opts)
    
    // Test connection
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    if err := client.Ping(ctx).Err(); err != nil {
        log.Fatalf("Failed to connect to Redis: %v", err)
    }
    
    return client
}

func startMetricsServer(port int) {
    http.Handle("/metrics", promhttp.Handler())
    http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("OK"))
    })
    
    addr := fmt.Sprintf(":%d", port)
    log.Printf("Starting metrics server on %s", addr)
    if err := http.ListenAndServe(addr, nil); err != nil {
        log.Fatalf("Metrics server failed: %v", err)
    }
}

// Utility functions for environment variables
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

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
    if str := os.Getenv(key); str != "" {
        if value, err := time.ParseDuration(str); err == nil {
            return value
        }
    }
    return defaultValue
}
```

### worker_pool.go

```go
package main

import (
    "context"
    "fmt"
    "log"
    "sync"
    "time"
    
    "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
    "github.com/google/uuid"
)

type WorkerPool struct {
    coordinator     *WorkerCoordinator
    healthMonitor   *HealthMonitor
    metricsCollector *MetricsCollector
    config          *Config
    
    mu              sync.RWMutex
    workers         map[string]*ManagedWorker
    workersByType   map[string][]*ManagedWorker
    shutdownCh      chan struct{}
    wg              sync.WaitGroup
}

type ManagedWorker struct {
    ID              string
    Type            string
    Worker          *agent.Worker
    LoadCalculator  *CustomLoadCalculator
    Handler         agent.JobHandler
    StartTime       time.Time
    LastHealthCheck time.Time
    Status          WorkerStatus
    JobCount        int
    Load            float32
    
    ctx             context.Context
    cancel          context.CancelFunc
    errCh           chan error
}

type WorkerStatus string

const (
    WorkerStatusStarting  WorkerStatus = "starting"
    WorkerStatusHealthy   WorkerStatus = "healthy"
    WorkerStatusUnhealthy WorkerStatus = "unhealthy"
    WorkerStatusShutdown  WorkerStatus = "shutdown"
)

func NewWorkerPool(
    coordinator *WorkerCoordinator,
    healthMonitor *HealthMonitor,
    metricsCollector *MetricsCollector,
    config *Config,
) *WorkerPool {
    return &WorkerPool{
        coordinator:      coordinator,
        healthMonitor:    healthMonitor,
        metricsCollector: metricsCollector,
        config:           config,
        workers:          make(map[string]*ManagedWorker),
        workersByType:    make(map[string][]*ManagedWorker),
        shutdownCh:       make(chan struct{}),
    }
}

func (wp *WorkerPool) Start(ctx context.Context) error {
    // Start initial workers for each type
    for _, workerType := range wp.config.WorkerTypes {
        for i := 0; i < workerType.MinCount; i++ {
            if err := wp.startWorker(ctx, workerType); err != nil {
                log.Printf("Failed to start initial worker of type %s: %v", workerType.Type, err)
                continue
            }
        }
    }
    
    // Start management loop
    wp.wg.Add(1)
    go wp.managementLoop(ctx)
    
    // Wait for shutdown
    <-wp.shutdownCh
    return nil
}

func (wp *WorkerPool) Shutdown(ctx context.Context) error {
    log.Println("Starting graceful worker pool shutdown...")
    
    close(wp.shutdownCh)
    
    // Create a channel to signal when all workers are stopped
    done := make(chan struct{})
    go func() {
        wp.wg.Wait()
        close(done)
    }()
    
    // Shutdown all workers gracefully
    wp.mu.Lock()
    for _, worker := range wp.workers {
        wp.shutdownWorker(worker, false) // Don't remove from maps yet
    }
    wp.mu.Unlock()
    
    // Wait for shutdown completion or timeout
    select {
    case <-done:
        log.Println("All workers shutdown gracefully")
        return nil
    case <-ctx.Done():
        log.Println("Shutdown timeout reached, forcing termination")
        
        // Force shutdown remaining workers
        wp.mu.Lock()
        for _, worker := range wp.workers {
            if worker.Status != WorkerStatusShutdown {
                worker.cancel()
            }
        }
        wp.mu.Unlock()
        
        return ctx.Err()
    }
}

func (wp *WorkerPool) startWorker(ctx context.Context, workerType WorkerTypeConfig) error {
    workerID := fmt.Sprintf("%s-%s", workerType.Type, uuid.New().String()[:8])
    
    // Create custom load calculator
    loadCalc := NewCustomLoadCalculator(CustomLoadConfig{
        CPUWeight:    workerType.CPUWeight,
        MemoryWeight: workerType.MemWeight,
        JobWeight:    workerType.JobWeight,
    })
    
    // Create handler based on worker type
    handler := wp.createHandler(workerType)
    
    // Create worker context
    workerCtx, cancel := context.WithCancel(ctx)
    
    // Create agent worker
    agentWorker := agent.NewWorker(
        wp.config.LiveKitURL,
        wp.config.APIKey,
        wp.config.APISecret,
        handler,
        agent.WorkerOptions{
            AgentName:         workerID,
            JobTypes:         workerType.JobTypes,
            MaxConcurrentJobs: workerType.MaxJobs,
            LoadCalculator:    loadCalc,
            Metadata: map[string]string{
                "worker_type":    workerType.Type,
                "worker_id":      workerID,
                "deployment_id":  wp.config.DeploymentID,
            },
        },
    )
    
    // Create managed worker
    managedWorker := &ManagedWorker{
        ID:              workerID,
        Type:            workerType.Type,
        Worker:          agentWorker,
        LoadCalculator:  loadCalc,
        Handler:         handler,
        StartTime:       time.Now(),
        LastHealthCheck: time.Now(),
        Status:          WorkerStatusStarting,
        ctx:             workerCtx,
        cancel:          cancel,
        errCh:           make(chan error, 1),
    }
    
    // Start worker
    go func() {
        defer func() {
            managedWorker.Status = WorkerStatusShutdown
            wp.removeWorker(managedWorker.ID)
        }()
        
        log.Printf("Starting worker %s (%s)", workerID, workerType.Type)
        managedWorker.errCh <- agentWorker.Start(workerCtx)
    }()
    
    // Add to pool
    wp.mu.Lock()
    wp.workers[workerID] = managedWorker
    wp.workersByType[workerType.Type] = append(wp.workersByType[workerType.Type], managedWorker)
    wp.mu.Unlock()
    
    // Register with coordinator
    wp.coordinator.RegisterWorker(WorkerInfo{
        ID:        workerID,
        Type:      workerType.Type,
        StartTime: managedWorker.StartTime,
        MaxJobs:   workerType.MaxJobs,
    })
    
    // Update metrics
    wp.metricsCollector.WorkerStarted(workerType.Type)
    
    log.Printf("Worker %s started successfully", workerID)
    return nil
}

func (wp *WorkerPool) createHandler(workerType WorkerTypeConfig) agent.JobHandler {
    // Create specialized handlers based on worker type
    switch workerType.Type {
    case "room-processor":
        return &RoomProcessorHandler{
            metricsCollector: wp.metricsCollector,
        }
    case "participant-monitor":
        return &ParticipantMonitorHandler{
            metricsCollector: wp.metricsCollector,
        }
    case "media-publisher":
        return &MediaPublisherHandler{
            metricsCollector: wp.metricsCollector,
        }
    default:
        return &GenericHandler{
            workerType:       workerType.Type,
            metricsCollector: wp.metricsCollector,
        }
    }
}

func (wp *WorkerPool) managementLoop(ctx context.Context) {
    defer wp.wg.Done()
    
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-wp.shutdownCh:
            return
        case <-ticker.C:
            wp.performHealthChecks()
            wp.updateMetrics()
            wp.rebalanceIfNeeded()
        }
    }
}

func (wp *WorkerPool) performHealthChecks() {
    wp.mu.RLock()
    workers := make([]*ManagedWorker, 0, len(wp.workers))
    for _, worker := range wp.workers {
        workers = append(workers, worker)
    }
    wp.mu.RUnlock()
    
    for _, worker := range workers {
        wp.checkWorkerHealth(worker)
    }
}

func (wp *WorkerPool) checkWorkerHealth(worker *ManagedWorker) {
    // Check if worker is still responsive
    select {
    case err := <-worker.errCh:
        if err != nil {
            log.Printf("Worker %s failed: %v", worker.ID, err)
            worker.Status = WorkerStatusUnhealthy
            wp.metricsCollector.WorkerFailed(worker.Type, err.Error())
            
            // Restart worker if it's not being shut down
            select {
            case <-wp.shutdownCh:
                // Shutdown in progress, don't restart
            default:
                go wp.restartWorker(worker)
            }
        }
    default:
        // No error, update health status
        if worker.Status == WorkerStatusStarting {
            // Mark as healthy after startup period
            if time.Since(worker.StartTime) > 30*time.Second {
                worker.Status = WorkerStatusHealthy
                log.Printf("Worker %s is now healthy", worker.ID)
            }
        }
        
        worker.LastHealthCheck = time.Now()
        
        // Update load metrics
        load := worker.LoadCalculator.GetCurrentLoad()
        worker.Load = load
        wp.coordinator.UpdateWorkerLoad(worker.ID, load)
        wp.metricsCollector.UpdateWorkerLoad(worker.Type, worker.ID, load)
    }
}

func (wp *WorkerPool) restartWorker(failedWorker *ManagedWorker) {
    log.Printf("Restarting failed worker %s (%s)", failedWorker.ID, failedWorker.Type)
    
    // Find worker type config
    var workerTypeConfig WorkerTypeConfig
    for _, wt := range wp.config.WorkerTypes {
        if wt.Type == failedWorker.Type {
            workerTypeConfig = wt
            break
        }
    }
    
    // Shutdown the failed worker
    wp.shutdownWorker(failedWorker, true)
    
    // Start a new worker of the same type
    if err := wp.startWorker(context.Background(), workerTypeConfig); err != nil {
        log.Printf("Failed to restart worker: %v", err)
        wp.metricsCollector.WorkerRestartFailed(failedWorker.Type, err.Error())
    } else {
        wp.metricsCollector.WorkerRestarted(failedWorker.Type)
    }
}

func (wp *WorkerPool) shutdownWorker(worker *ManagedWorker, removeFromMaps bool) {
    log.Printf("Shutting down worker %s", worker.ID)
    
    worker.Status = WorkerStatusShutdown
    worker.cancel()
    
    // Unregister from coordinator
    wp.coordinator.UnregisterWorker(worker.ID)
    
    if removeFromMaps {
        wp.removeWorker(worker.ID)
    }
    
    wp.metricsCollector.WorkerStopped(worker.Type)
}

func (wp *WorkerPool) removeWorker(workerID string) {
    wp.mu.Lock()
    defer wp.mu.Unlock()
    
    worker, exists := wp.workers[workerID]
    if !exists {
        return
    }
    
    // Remove from main map
    delete(wp.workers, workerID)
    
    // Remove from type-specific map
    typeWorkers := wp.workersByType[worker.Type]
    for i, w := range typeWorkers {
        if w.ID == workerID {
            wp.workersByType[worker.Type] = append(typeWorkers[:i], typeWorkers[i+1:]...)
            break
        }
    }
}

func (wp *WorkerPool) updateMetrics() {
    wp.mu.RLock()
    defer wp.mu.RUnlock()
    
    // Count workers by type and status
    typeCounts := make(map[string]map[WorkerStatus]int)
    
    for _, worker := range wp.workers {
        if typeCounts[worker.Type] == nil {
            typeCounts[worker.Type] = make(map[WorkerStatus]int)
        }
        typeCounts[worker.Type][worker.Status]++
    }
    
    // Update Prometheus metrics
    for workerType, statusCounts := range typeCounts {
        for status, count := range statusCounts {
            wp.metricsCollector.SetWorkerCount(workerType, string(status), count)
        }
    }
}

func (wp *WorkerPool) rebalanceIfNeeded() {
    // This is a placeholder for rebalancing logic
    // In a production system, you might:
    // - Check if certain worker types are overloaded
    // - Move jobs between workers
    // - Adjust worker priorities based on current demand
    
    wp.mu.RLock()
    defer wp.mu.RUnlock()
    
    for workerType, workers := range wp.workersByType {
        healthyCount := 0
        totalLoad := float32(0)
        
        for _, worker := range workers {
            if worker.Status == WorkerStatusHealthy {
                healthyCount++
                totalLoad += worker.Load
            }
        }
        
        if healthyCount > 0 {
            avgLoad := totalLoad / float32(healthyCount)
            log.Printf("Worker type %s: %d healthy workers, average load: %.2f",
                workerType, healthyCount, avgLoad)
        }
    }
}

func (wp *WorkerPool) ScaleUp(workerType string) error {
    // Find worker type config
    var workerTypeConfig WorkerTypeConfig
    for _, wt := range wp.config.WorkerTypes {
        if wt.Type == workerType {
            workerTypeConfig = wt
            break
        }
    }
    
    if workerTypeConfig.Type == "" {
        return fmt.Errorf("unknown worker type: %s", workerType)
    }
    
    // Check if we can scale up
    wp.mu.RLock()
    currentCount := len(wp.workersByType[workerType])
    wp.mu.RUnlock()
    
    if currentCount >= workerTypeConfig.MaxCount {
        return fmt.Errorf("already at maximum worker count for type %s", workerType)
    }
    
    log.Printf("Scaling up worker type %s (current: %d, max: %d)",
        workerType, currentCount, workerTypeConfig.MaxCount)
    
    return wp.startWorker(context.Background(), workerTypeConfig)
}

func (wp *WorkerPool) ScaleDown(workerType string) error {
    wp.mu.RLock()
    workers := wp.workersByType[workerType]
    wp.mu.RUnlock()
    
    if len(workers) == 0 {
        return fmt.Errorf("no workers of type %s to scale down", workerType)
    }
    
    // Find worker type config to check minimum
    var minCount int
    for _, wt := range wp.config.WorkerTypes {
        if wt.Type == workerType {
            minCount = wt.MinCount
            break
        }
    }
    
    if len(workers) <= minCount {
        return fmt.Errorf("already at minimum worker count for type %s", workerType)
    }
    
    // Find the worker with the lowest load
    var targetWorker *ManagedWorker
    lowestLoad := float32(1.0)
    
    for _, worker := range workers {
        if worker.Status == WorkerStatusHealthy && worker.Load < lowestLoad {
            lowestLoad = worker.Load
            targetWorker = worker
        }
    }
    
    if targetWorker == nil {
        return fmt.Errorf("no suitable worker found to scale down")
    }
    
    log.Printf("Scaling down worker %s (load: %.2f)", targetWorker.ID, targetWorker.Load)
    wp.shutdownWorker(targetWorker, true)
    
    return nil
}

func (wp *WorkerPool) GetWorkerStats() map[string]WorkerTypeStats {
    wp.mu.RLock()
    defer wp.mu.RUnlock()
    
    stats := make(map[string]WorkerTypeStats)
    
    for workerType, workers := range wp.workersByType {
        typeStats := WorkerTypeStats{
            Total:   len(workers),
            Healthy: 0,
            Load:    make([]float32, 0, len(workers)),
        }
        
        var totalLoad float32
        for _, worker := range workers {
            if worker.Status == WorkerStatusHealthy {
                typeStats.Healthy++
            }
            typeStats.Load = append(typeStats.Load, worker.Load)
            totalLoad += worker.Load
        }
        
        if len(workers) > 0 {
            typeStats.AverageLoad = totalLoad / float32(len(workers))
        }
        
        stats[workerType] = typeStats
    }
    
    return stats
}

type WorkerTypeStats struct {
    Total       int
    Healthy     int
    AverageLoad float32
    Load        []float32
}
```

### auto_scaler.go

```go
package main

import (
    "context"
    "log"
    "time"
)

type AutoScaler struct {
    coordinator      *WorkerCoordinator
    metricsCollector *MetricsCollector
    config           *Config
    
    lastScaleUp      map[string]time.Time
    lastScaleDown    map[string]time.Time
}

func NewAutoScaler(
    coordinator *WorkerCoordinator,
    metricsCollector *MetricsCollector,
    config *Config,
) *AutoScaler {
    return &AutoScaler{
        coordinator:      coordinator,
        metricsCollector: metricsCollector,
        config:           config,
        lastScaleUp:      make(map[string]time.Time),
        lastScaleDown:    make(map[string]time.Time),
    }
}

func (as *AutoScaler) Start(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Minute) // Check every minute
    defer ticker.Stop()
    
    log.Println("Auto-scaler started")
    
    for {
        select {
        case <-ctx.Done():
            log.Println("Auto-scaler stopped")
            return
        case <-ticker.C:
            as.evaluateScaling()
        }
    }
}

func (as *AutoScaler) evaluateScaling() {
    stats := as.coordinator.GetClusterStats()
    
    for workerType, typeStats := range stats {
        as.evaluateWorkerTypeScaling(workerType, typeStats)
    }
}

func (as *AutoScaler) evaluateWorkerTypeScaling(workerType string, stats WorkerTypeStats) {
    now := time.Now()
    
    // Check if we're in cooldown period
    if lastUp, exists := as.lastScaleUp[workerType]; exists {
        if now.Sub(lastUp) < as.config.ScaleUpCooldown {
            return // Still in scale-up cooldown
        }
    }
    
    if lastDown, exists := as.lastScaleDown[workerType]; exists {
        if now.Sub(lastDown) < as.config.ScaleDownCooldown {
            return // Still in scale-down cooldown
        }
    }
    
    // Evaluate scaling decision
    if stats.AverageLoad > as.config.LoadThresholdHigh && stats.Healthy > 0 {
        // Scale up
        if err := as.coordinator.ScaleUp(workerType); err != nil {
            log.Printf("Failed to scale up %s: %v", workerType, err)
        } else {
            log.Printf("Auto-scaled up worker type: %s (load: %.2f)", workerType, stats.AverageLoad)
            as.lastScaleUp[workerType] = now
            as.metricsCollector.ScaleUpEvent(workerType, stats.AverageLoad)
        }
    } else if stats.AverageLoad < as.config.LoadThresholdLow && stats.Total > 1 {
        // Scale down
        if err := as.coordinator.ScaleDown(workerType); err != nil {
            log.Printf("Failed to scale down %s: %v", workerType, err)
        } else {
            log.Printf("Auto-scaled down worker type: %s (load: %.2f)", workerType, stats.AverageLoad)
            as.lastScaleDown[workerType] = now
            as.metricsCollector.ScaleDownEvent(workerType, stats.AverageLoad)
        }
    }
}
```

### coordinator.go

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "sync"
    "time"
    
    "github.com/go-redis/redis/v8"
)

type WorkerCoordinator struct {
    redis  *redis.Client
    config *Config
    
    mu      sync.RWMutex
    workers map[string]WorkerInfo
}

type WorkerInfo struct {
    ID        string    `json:"id"`
    Type      string    `json:"type"`
    Load      float32   `json:"load"`
    MaxJobs   int       `json:"max_jobs"`
    StartTime time.Time `json:"start_time"`
    LastSeen  time.Time `json:"last_seen"`
}

type ClusterStats struct {
    Workers map[string]WorkerTypeStats `json:"workers"`
    Updated time.Time                  `json:"updated"`
}

func NewWorkerCoordinator(redis *redis.Client, config *Config) *WorkerCoordinator {
    return &WorkerCoordinator{
        redis:   redis,
        config:  config,
        workers: make(map[string]WorkerInfo),
    }
}

func (wc *WorkerCoordinator) RegisterWorker(worker WorkerInfo) {
    wc.mu.Lock()
    defer wc.mu.Unlock()
    
    worker.LastSeen = time.Now()
    wc.workers[worker.ID] = worker
    
    // Store in Redis for cluster coordination
    key := fmt.Sprintf("workers:%s", worker.ID)
    data, _ := json.Marshal(worker)
    
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    wc.redis.Set(ctx, key, data, 10*time.Minute).Err()
    
    log.Printf("Registered worker %s (%s) with coordinator", worker.ID, worker.Type)
}

func (wc *WorkerCoordinator) UnregisterWorker(workerID string) {
    wc.mu.Lock()
    defer wc.mu.Unlock()
    
    delete(wc.workers, workerID)
    
    // Remove from Redis
    key := fmt.Sprintf("workers:%s", workerID)
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    wc.redis.Del(ctx, key).Err()
    
    log.Printf("Unregistered worker %s from coordinator", workerID)
}

func (wc *WorkerCoordinator) UpdateWorkerLoad(workerID string, load float32) {
    wc.mu.Lock()
    defer wc.mu.Unlock()
    
    if worker, exists := wc.workers[workerID]; exists {
        worker.Load = load
        worker.LastSeen = time.Now()
        wc.workers[workerID] = worker
        
        // Update in Redis
        key := fmt.Sprintf("workers:%s", workerID)
        data, _ := json.Marshal(worker)
        
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
        defer cancel()
        
        wc.redis.Set(ctx, key, data, 10*time.Minute).Err()
    }
}

func (wc *WorkerCoordinator) GetClusterStats() map[string]WorkerTypeStats {
    wc.mu.RLock()
    defer wc.mu.RUnlock()
    
    stats := make(map[string]WorkerTypeStats)
    
    for _, worker := range wc.workers {
        if typeStats, exists := stats[worker.Type]; exists {
            typeStats.Total++
            typeStats.Load = append(typeStats.Load, worker.Load)
            
            // Consider worker healthy if seen recently
            if time.Since(worker.LastSeen) < 2*time.Minute {
                typeStats.Healthy++
            }
            
            stats[worker.Type] = typeStats
        } else {
            healthy := 0
            if time.Since(worker.LastSeen) < 2*time.Minute {
                healthy = 1
            }
            
            stats[worker.Type] = WorkerTypeStats{
                Total:   1,
                Healthy: healthy,
                Load:    []float32{worker.Load},
            }
        }
    }
    
    // Calculate average loads
    for workerType, typeStats := range stats {
        if len(typeStats.Load) > 0 {
            var total float32
            for _, load := range typeStats.Load {
                total += load
            }
            typeStats.AverageLoad = total / float32(len(typeStats.Load))
            stats[workerType] = typeStats
        }
    }
    
    return stats
}

func (wc *WorkerCoordinator) ScaleUp(workerType string) error {
    // This would trigger scaling in the worker pool
    // For this example, we'll just log the intent
    log.Printf("Coordinator received scale-up request for %s", workerType)
    
    // In a real implementation, this might:
    // 1. Send a message to the worker pool manager
    // 2. Update a scaling queue in Redis
    // 3. Trigger a Kubernetes deployment scaling
    
    return nil
}

func (wc *WorkerCoordinator) ScaleDown(workerType string) error {
    log.Printf("Coordinator received scale-down request for %s", workerType)
    return nil
}
```

### handlers.go

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"
    
    "github.com/livekit/protocol/livekit"
    lksdk "github.com/livekit/server-sdk-go/v2"
)

// Specialized handlers for different worker types

type RoomProcessorHandler struct {
    metricsCollector *MetricsCollector
}

func (h *RoomProcessorHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    if job.Type != livekit.JobType_JT_ROOM {
        return fmt.Errorf("unexpected job type: %v", job.Type)
    }
    
    startTime := time.Now()
    h.metricsCollector.JobStarted("room-processor", job.Id)
    
    defer func() {
        duration := time.Since(startTime)
        h.metricsCollector.JobCompleted("room-processor", job.Id, duration)
    }()
    
    log.Printf("Room processor handling job %s for room %s", job.Id, room.Name())
    
    // Room processing logic here
    participantCount := 0
    trackCount := 0
    
    // Set up event handlers
    room.Callback.OnParticipantConnected = func(p *lksdk.RemoteParticipant) {
        participantCount++
        h.metricsCollector.ParticipantJoined("room-processor", room.Name())
    }
    
    room.Callback.OnParticipantDisconnected = func(p *lksdk.RemoteParticipant) {
        participantCount--
        h.metricsCollector.ParticipantLeft("room-processor", room.Name())
    }
    
    room.Callback.OnTrackPublished = func(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
        trackCount++
        h.metricsCollector.TrackPublished("room-processor", room.Name(), pub.Kind().String())
    }
    
    // Count existing participants and tracks
    for _, p := range room.GetRemoteParticipants() {
        participantCount++
        for _, pub := range p.TrackPublications() {
            if pub.Track() != nil {
                trackCount++
            }
        }
    }
    
    // Periodic reporting
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            log.Printf("Room processor job %s completed", job.Id)
            return nil
        case <-ticker.C:
            h.metricsCollector.RoomStats("room-processor", room.Name(), participantCount, trackCount)
        }
    }
}

type ParticipantMonitorHandler struct {
    metricsCollector *MetricsCollector
}

func (h *ParticipantMonitorHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    if job.Type != livekit.JobType_JT_PARTICIPANT {
        return fmt.Errorf("unexpected job type: %v", job.Type)
    }
    
    startTime := time.Now()
    h.metricsCollector.JobStarted("participant-monitor", job.Id)
    
    defer func() {
        duration := time.Since(startTime)
        h.metricsCollector.JobCompleted("participant-monitor", job.Id, duration)
    }()
    
    log.Printf("Participant monitor handling job %s", job.Id)
    
    // Participant monitoring logic
    monitoredParticipant := "unknown"
    
    // Extract participant info from job metadata if available
    if job.Metadata != "" {
        // Parse metadata to get participant identity
        log.Printf("Monitoring participant based on metadata: %s", job.Metadata)
    }
    
    // Monitor specific participant events
    activityCount := 0
    
    room.Callback.OnDataReceived = func(data []byte, params lksdk.DataReceiveParams) {
        if params.SenderIdentity == monitoredParticipant || monitoredParticipant == "unknown" {
            activityCount++
            h.metricsCollector.ParticipantActivity("participant-monitor", params.SenderIdentity)
        }
    }
    
    // Periodic health reporting
    ticker := time.NewTicker(15 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            log.Printf("Participant monitor job %s completed (activities: %d)", job.Id, activityCount)
            return nil
        case <-ticker.C:
            h.metricsCollector.MonitoringStats("participant-monitor", monitoredParticipant, activityCount)
        }
    }
}

type MediaPublisherHandler struct {
    metricsCollector *MetricsCollector
}

func (h *MediaPublisherHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    if job.Type != livekit.JobType_JT_PUBLISHER {
        return fmt.Errorf("unexpected job type: %v", job.Type)
    }
    
    startTime := time.Now()
    h.metricsCollector.JobStarted("media-publisher", job.Id)
    
    defer func() {
        duration := time.Since(startTime)
        h.metricsCollector.JobCompleted("media-publisher", job.Id, duration)
    }()
    
    log.Printf("Media publisher handling job %s for room %s", job.Id, room.Name())
    
    // Media publishing logic
    publishedTracks := 0
    framesSent := uint64(0)
    
    // Simulate media publishing
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            log.Printf("Media publisher job %s completed (tracks: %d, frames: %d)", 
                job.Id, publishedTracks, framesSent)
            return nil
        case <-ticker.C:
            // Simulate frame sending
            framesSent += 30 // Simulate 30fps
            h.metricsCollector.MediaFramesSent("media-publisher", job.Id, 30)
        }
    }
}

type GenericHandler struct {
    workerType       string
    metricsCollector *MetricsCollector
}

func (h *GenericHandler) OnJob(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
    startTime := time.Now()
    h.metricsCollector.JobStarted(h.workerType, job.Id)
    
    defer func() {
        duration := time.Since(startTime)
        h.metricsCollector.JobCompleted(h.workerType, job.Id, duration)
    }()
    
    log.Printf("Generic handler (%s) processing job %s", h.workerType, job.Id)
    
    // Generic job processing
    <-ctx.Done()
    
    return nil
}
```

### config.go

```go
package main

import (
    "time"
    
    "github.com/livekit/protocol/livekit"
)

type Config struct {
    LiveKitURL     string
    APIKey         string
    APISecret      string
    RedisURL       string
    DeploymentID   string
    
    InitialWorkerCount int
    MinWorkerCount     int
    MaxWorkerCount     int
    
    LoadThresholdHigh  float64
    LoadThresholdLow   float64
    
    ScaleUpCooldown    time.Duration
    ScaleDownCooldown  time.Duration
    
    HealthCheckInterval time.Duration
    MetricsPort        int
    
    WorkerTypes []WorkerTypeConfig
}

type WorkerTypeConfig struct {
    Type      string
    JobTypes  []livekit.JobType
    MaxJobs   int
    MinCount  int
    MaxCount  int
    CPUWeight float32
    MemWeight float32
    JobWeight float32
}
```

## Docker Compose Deployment

### docker-compose.yml

```yaml
version: '3.8'

services:
  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    volumes:
      - redis_data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

  prometheus:
    image: prom/prometheus
    ports:
      - "9091:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml
      - prometheus_data:/prometheus
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--storage.tsdb.path=/prometheus'
      - '--web.console.libraries=/etc/prometheus/console_libraries'
      - '--web.console.templates=/etc/prometheus/consoles'

  grafana:
    image: grafana/grafana
    ports:
      - "3000:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
    volumes:
      - grafana_data:/var/lib/grafana

  load-balanced-workers:
    build: .
    environment:
      - LIVEKIT_URL=ws://host.docker.internal:7880
      - LIVEKIT_API_KEY=${LIVEKIT_API_KEY}
      - LIVEKIT_API_SECRET=${LIVEKIT_API_SECRET}
      - REDIS_URL=redis://redis:6379
      - INITIAL_WORKER_COUNT=3
      - MIN_WORKER_COUNT=2
      - MAX_WORKER_COUNT=10
      - LOAD_THRESHOLD_HIGH=0.8
      - LOAD_THRESHOLD_LOW=0.3
      - METRICS_PORT=9090
    ports:
      - "9090:9090"
    depends_on:
      redis:
        condition: service_healthy
    deploy:
      replicas: 1
      resources:
        limits:
          cpus: '2'
          memory: 1G
        reservations:
          cpus: '0.5'
          memory: 512M

volumes:
  redis_data:
  prometheus_data:
  grafana_data:
```

## Kubernetes Deployment

### k8s-deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: load-balanced-workers
  labels:
    app: load-balanced-workers
spec:
  replicas: 2
  selector:
    matchLabels:
      app: load-balanced-workers
  template:
    metadata:
      labels:
        app: load-balanced-workers
    spec:
      containers:
      - name: load-balanced-workers
        image: load-balanced-workers:latest
        ports:
        - containerPort: 9090
          name: metrics
        env:
        - name: LIVEKIT_URL
          valueFrom:
            configMapKeyRef:
              name: livekit-config
              key: url
        - name: LIVEKIT_API_KEY
          valueFrom:
            secretKeyRef:
              name: livekit-secrets
              key: api-key
        - name: LIVEKIT_API_SECRET
          valueFrom:
            secretKeyRef:
              name: livekit-secrets
              key: api-secret
        - name: REDIS_URL
          value: "redis://redis-service:6379"
        - name: DEPLOYMENT_ID
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        resources:
          requests:
            memory: "512Mi"
            cpu: "500m"
          limits:
            memory: "1Gi"
            cpu: "2"
        readinessProbe:
          httpGet:
            path: /health
            port: 9090
          initialDelaySeconds: 30
          periodSeconds: 10
        livenessProbe:
          httpGet:
            path: /health
            port: 9090
          initialDelaySeconds: 60
          periodSeconds: 30

---
apiVersion: v1
kind: Service
metadata:
  name: load-balanced-workers-service
spec:
  selector:
    app: load-balanced-workers
  ports:
  - port: 9090
    targetPort: 9090
    name: metrics

---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: load-balanced-workers-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: load-balanced-workers
  minReplicas: 2
  maxReplicas: 10
  metrics:
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

## Running the Example

### Local Development

1. Start dependencies:
   ```bash
   docker-compose up -d redis prometheus grafana
   ```

2. Set environment variables:
   ```bash
   export LIVEKIT_URL="ws://localhost:7880"
   export LIVEKIT_API_KEY="your-api-key"
   export LIVEKIT_API_SECRET="your-api-secret"
   export REDIS_URL="redis://localhost:6379"
   export INITIAL_WORKER_COUNT="3"
   ```

3. Run the load-balanced workers:
   ```bash
   go run .
   ```

### Docker Deployment

1. Build the image:
   ```bash
   docker build -t load-balanced-workers .
   ```

2. Run with docker-compose:
   ```bash
   docker-compose up
   ```

### Kubernetes Deployment

1. Apply the configuration:
   ```bash
   kubectl apply -f k8s-deployment.yaml
   ```

2. Monitor the deployment:
   ```bash
   kubectl get pods -l app=load-balanced-workers
   kubectl logs -l app=load-balanced-workers
   ```

## Expected Output

```
2024/01/15 10:30:00 Starting worker pool with 3 initial workers...
2024/01/15 10:30:01 Starting worker room-processor-abc123 (room-processor)
2024/01/15 10:30:01 Starting worker participant-monitor-def456 (participant-monitor)
2024/01/15 10:30:01 Starting worker media-publisher-ghi789 (media-publisher)
2024/01/15 10:30:02 Worker room-processor-abc123 started successfully
2024/01/15 10:30:02 Registered worker room-processor-abc123 (room-processor) with coordinator
2024/01/15 10:30:02 Worker participant-monitor-def456 started successfully
2024/01/15 10:30:02 Worker media-publisher-ghi789 started successfully
2024/01/15 10:30:30 Auto-scaler started
2024/01/15 10:30:32 Worker room-processor-abc123 is now healthy
2024/01/15 10:30:32 Worker participant-monitor-def456 is now healthy
2024/01/15 10:30:32 Worker media-publisher-ghi789 is now healthy
2024/01/15 10:31:00 Worker type room-processor: 1 healthy workers, average load: 0.45
2024/01/15 10:31:00 Worker type participant-monitor: 1 healthy workers, average load: 0.23
2024/01/15 10:31:00 Worker type media-publisher: 1 healthy workers, average load: 0.67
2024/01/15 10:32:00 Auto-scaled up worker type: media-publisher (load: 0.85)
2024/01/15 10:32:01 Starting worker media-publisher-jkl012 (media-publisher)
```

## Key Features Demonstrated

1. **Multi-Worker Deployment**: Different worker types with specialized handlers
2. **Load-Based Scaling**: Automatic scaling based on worker load and system metrics
3. **Health Monitoring**: Continuous health checks with automatic worker restart
4. **Redis Coordination**: Distributed state management and worker coordination
5. **Prometheus Metrics**: Comprehensive metrics collection and monitoring
6. **Graceful Shutdown**: Proper cleanup and job migration during shutdown
7. **Production Deployment**: Docker Compose and Kubernetes deployment examples
8. **High Availability**: Redundant workers and automatic failover

## Monitoring and Metrics

Access the monitoring dashboards:
- **Prometheus**: http://localhost:9091
- **Grafana**: http://localhost:3000 (admin/admin)
- **Worker Metrics**: http://localhost:9090/metrics

## Next Steps

- Review [API Reference](../api-reference.md) for detailed SDK documentation
- Check [Troubleshooting](../troubleshooting.md) for common deployment issues
- Explore production deployment patterns in [Advanced Features](../advanced-features.md)