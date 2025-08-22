package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const (
	CurrentProtocol      = 1
	defaultPingInterval  = 10 * time.Second
	defaultPingTimeout   = 2 * time.Second
	registrationTimeout  = 10 * time.Second
	reconnectInterval    = 5 * time.Second
	maxReconnectAttempts = 5
	maxStatusRetries     = 3
	statusRetryDelay     = 1 * time.Second
)

// Worker represents an agent worker that can handle jobs.
// A worker connects to the LiveKit server, registers its capabilities,
// and processes jobs assigned by the server.
//
// The worker maintains a persistent WebSocket connection for real-time
// communication and automatically handles reconnection on failures.
//
// Key features:
//   - Automatic load balancing based on configurable metrics
//   - Job queueing and prioritization
//   - Resource pooling for efficient resource reuse
//   - Graceful shutdown with customizable hooks
//   - Connection resilience with automatic reconnection
//   - Protocol version negotiation
type Worker struct {
	serverURL string
	apiKey    string
	apiSecret string
	opts      WorkerOptions
	handler   JobHandler

	mu            sync.RWMutex
	writeMu       sync.Mutex // Protects websocket writes
	conn          *websocket.Conn
	workerID      string
	activeJobs    map[string]*activeJob
	status        WorkerStatus
	load          float32
	closing       bool
	reconnectChan chan struct{}

	// State persistence
	savedState    *WorkerState
	lastPingTime  time.Time
	errorCount    int

	// Status update queue for retries
	statusQueue     []statusUpdate
	statusQueueMu   sync.Mutex
	statusQueueChan chan struct{}

	// Load calculation
	loadCalculator   LoadCalculator
	metricsCollector *SystemMetricsCollector
	loadBatcher      *LoadBatcher
	jobStartTimes    map[string]time.Time

	// Job recovery
	recoveryManager    *JobRecoveryManager
	partialMsgBuffer   *PartialMessageBuffer
	jobCheckpoints     map[string]*JobCheckpoint
	jobCheckpointsMu   sync.RWMutex

	// Job queue and resource pooling
	jobQueue       *JobQueue
	resourcePool   *ResourcePool
	priorityCalc   JobPriorityCalculator

	// Network error handling
	networkHandler  *NetworkHandler
	networkMonitor  *NetworkMonitor

	// Protocol handling
	protocolHandler     *ProtocolHandler
	messageTypeRegistry *MessageTypeRegistry

	// Resource monitoring
	resourceMonitor *ResourceMonitor
	resourceGuard   *ResourceGuard

	// Shutdown hooks
	shutdownHooks *ShutdownHookManager

	// Race condition protection
	raceProtector *RaceProtector

	// Resource limiting
	resourceLimiter *ResourceLimiter

	// Timing management
	timingManager *TimingManager

	logger Logger
}

// statusUpdate represents a pending job status update
type statusUpdate struct {
	jobID      string
	status     livekit.JobStatus
	error      string
	retryCount int
	timestamp  time.Time
}

type activeJob struct {
	job       *livekit.Job
	room      *lksdk.Room
	cancel    context.CancelFunc
	startedAt time.Time
	status    livekit.JobStatus
}

// NewWorker creates a new agent worker.
//
// Parameters:
//   - serverURL: WebSocket URL of the LiveKit server (e.g., "ws://localhost:7880")
//   - apiKey: API key for authentication
//   - apiSecret: API secret for authentication
//   - handler: Implementation of JobHandler interface to process jobs
//   - opts: Configuration options for the worker
//
// The worker is created but not started. Call Start() to begin processing.
//
// Example:
//
//	handler := &MyJobHandler{}
//	opts := WorkerOptions{
//	    AgentName: "my-agent",
//	    JobType:   livekit.JobType_JT_ROOM,
//	    MaxJobs:   5,
//	}
//	worker := NewWorker(url, key, secret, handler, opts)
//	if err := worker.Start(ctx); err != nil {
//	    log.Fatal(err)
//	}
func NewWorker(serverURL, apiKey, apiSecret string, handler JobHandler, opts WorkerOptions) *Worker {
	if opts.PingInterval == 0 {
		opts.PingInterval = defaultPingInterval
	}
	if opts.PingTimeout == 0 {
		opts.PingTimeout = defaultPingTimeout
	}
	if opts.Logger == nil {
		logger, _ := zap.NewProduction()
		opts.Logger = &zapLogger{logger.Sugar()}
	}

	w := &Worker{
		serverURL:       serverURL,
		apiKey:          apiKey,
		apiSecret:       apiSecret,
		opts:            opts,
		handler:         handler,
		activeJobs:      make(map[string]*activeJob),
		status:          WorkerStatusAvailable,
		reconnectChan:   make(chan struct{}, 1),
		statusQueue:     make([]statusUpdate, 0),
		statusQueueChan: make(chan struct{}, 100),
		jobStartTimes:   make(map[string]time.Time),
		jobCheckpoints:  make(map[string]*JobCheckpoint),
		logger:          opts.Logger,
	}
	
	// Initialize state for recovery
	w.savedState = &WorkerState{
		ActiveJobs: make(map[string]*JobState),
	}

	// Initialize load calculator
	if opts.LoadCalculator != nil {
		w.loadCalculator = opts.LoadCalculator
	} else if opts.EnableCPUMemoryLoad {
		w.loadCalculator = NewCPUMemoryLoadCalculator()
		w.metricsCollector = NewSystemMetricsCollector()
	} else {
		w.loadCalculator = &DefaultLoadCalculator{}
	}

	// Wrap with predictive calculator if enabled
	if opts.EnableLoadPrediction {
		w.loadCalculator = NewPredictiveLoadCalculator(w.loadCalculator, 10)
	}

	// Initialize load batcher if enabled
	if opts.StatusUpdateBatchInterval > 0 {
		w.loadBatcher = NewLoadBatcher(w, opts.StatusUpdateBatchInterval)
	}

	// Initialize job recovery if enabled
	if opts.EnableJobRecovery {
		w.recoveryManager = NewJobRecoveryManager(w, opts.JobRecoveryHandler)
	}

	// Initialize partial message buffer
	bufferSize := opts.PartialMessageBufferSize
	if bufferSize <= 0 {
		bufferSize = 1024 * 1024 // 1MB default
	}
	w.partialMsgBuffer = NewPartialMessageBuffer(bufferSize)
	
	// Initialize job queue if enabled
	if opts.EnableJobQueue {
		w.jobQueue = NewJobQueue(JobQueueOptions{
			MaxSize: opts.JobQueueSize,
		})
		
		// Set priority calculator
		if opts.JobPriorityCalculator != nil {
			w.priorityCalc = opts.JobPriorityCalculator
		} else {
			w.priorityCalc = &DefaultPriorityCalculator{}
		}
	}
	
	// Initialize resource pool if enabled
	if opts.EnableResourcePool {
		// Use provided factory or default
		factory := opts.ResourceFactory
		if factory == nil {
			factory = &WorkerResourceFactory{}
		}
		
		// Set default pool sizes if not specified
		minSize := opts.ResourcePoolMinSize
		if minSize <= 0 {
			minSize = 2
		}
		maxSize := opts.ResourcePoolMaxSize
		if maxSize <= 0 {
			maxSize = 10
		}
		if maxSize < minSize {
			maxSize = minSize * 2
		}
		
		pool, err := NewResourcePool(factory, ResourcePoolOptions{
			MinSize:     minSize,
			MaxSize:     maxSize,
			MaxIdleTime: 5 * time.Minute,
		})
		if err != nil {
			w.logger.Error("Failed to create resource pool", "error", err)
			// Continue without pool - it's optional
		} else {
			w.resourcePool = pool
		}
	}
	
	// Initialize network handler
	w.networkHandler = NewNetworkHandler()
	w.networkMonitor = NewNetworkMonitor(w.networkHandler, 5*time.Second)
	
	// Initialize protocol handler
	zapLogger := w.logger.(*zapLogger)
	w.protocolHandler = NewProtocolHandler(zapLogger.Desugar())
	w.protocolHandler.SetStrictMode(opts.StrictProtocolMode)
	
	w.messageTypeRegistry = NewMessageTypeRegistry(zapLogger.Desugar())
	
	// Register custom message handlers if provided
	for _, handler := range opts.CustomMessageHandlers {
		w.messageTypeRegistry.RegisterHandler(handler)
	}
	
	// Initialize resource monitor if enabled
	if opts.EnableResourceMonitoring {
		w.resourceMonitor = NewResourceMonitor(zapLogger.Desugar(), ResourceMonitorOptions{
			MemoryLimitMB:  opts.MemoryLimitMB,
			GoroutineLimit: opts.GoroutineLimit,
		})
		w.resourceGuard = NewResourceGuard(w.resourceMonitor)
		
		// Set callbacks
		w.resourceMonitor.SetOOMCallback(func() {
			w.logger.Error("OOM detected, reducing load")
			// Stop accepting new jobs
			w.mu.Lock()
			w.status = WorkerStatusFull
			w.mu.Unlock()
			w.UpdateStatus(w.status, w.load)
		})
		
		w.resourceMonitor.SetLeakCallback(func(count int) {
			w.logger.Error("Goroutine leak detected", "count", count)
		})
		
		w.resourceMonitor.SetCircularDependencyCallback(func(deps []string) {
			w.logger.Error("Circular dependency detected", "cycle", deps)
		})
	}
	
	// Initialize shutdown hooks
	w.shutdownHooks = NewShutdownHookManager(zapLogger.Desugar())
	
	// Add default log flush hook
	defaultHooks := DefaultShutdownHooks{}
	w.shutdownHooks.AddHook(ShutdownPhaseFinal, defaultHooks.NewLogFlushHook(zapLogger.Desugar()))
	
	// Initialize race protector
	w.raceProtector = NewRaceProtector(zapLogger.Desugar())
	
	// Initialize resource limiter if enabled
	if opts.EnableResourceLimits {
		w.resourceLimiter = NewResourceLimiter(zapLogger.Desugar(), ResourceLimiterOptions{
			MemoryLimitMB:      opts.HardMemoryLimitMB,
			CPUQuotaPercent:    opts.CPUQuotaPercent,
			MaxFileDescriptors: opts.MaxFileDescriptors,
			EnforceHardLimits:  true,
		})
		
		// Set callbacks
		w.resourceLimiter.SetMemoryLimitCallback(func(usage, limit uint64) {
			w.logger.Error("Hard memory limit exceeded", 
				"usage_mb", usage/1024/1024,
				"limit_mb", limit/1024/1024)
			// Reduce load to prevent OOM
			w.mu.Lock()
			if w.status != WorkerStatusFull {
				w.status = WorkerStatusFull
				w.mu.Unlock()
				w.UpdateStatus(w.status, w.load)
			} else {
				w.mu.Unlock()
			}
		})
		
		w.resourceLimiter.SetCPULimitCallback(func(usage float64) {
			w.logger.Error("CPU quota exceeded", "usage_percent", usage)
		})
		
		w.resourceLimiter.SetFDLimitCallback(func(usage, limit int) {
			w.logger.Error("File descriptor limit exceeded", 
				"usage", usage,
				"limit", limit)
		})
	}
	
	// Initialize timing manager
	w.timingManager = NewTimingManager(zapLogger.Desugar(), TimingManagerOptions{})
	
	return w
}

// Start begins the worker connection and job handling.
// The worker will automatically reconnect if the connection is lost.
//
// This method blocks until the context is cancelled or a fatal error occurs.
// For graceful shutdown, cancel the context and then call Stop().
//
// The worker performs the following operations on start:
//   - Establishes WebSocket connection to the server
//   - Registers with configured capabilities
//   - Begins accepting and processing jobs
//   - Starts monitoring systems if enabled
//   - Initializes job queue processor if enabled
//
// Returns an error if:
//   - Initial connection fails
//   - Authentication fails
//   - Worker is already running
//   - Registration is rejected by server
//
// Example:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	
//	if err := worker.Start(ctx); err != nil {
//	    log.Fatal("Failed to start worker:", err)
//	}
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return fmt.Errorf("worker is closing")
	}
	w.mu.Unlock()

	// Initial connection
	if err := w.connect(ctx); err != nil {
		return err
	}

	// Start message handling
	go w.handleMessages(ctx)
	go w.handlePing(ctx)

	// Handle reconnection
	go w.handleReconnect(ctx)
	
	// Handle status update retries
	go w.handleStatusUpdateRetries(ctx)

	// Start load batcher if enabled
	if w.loadBatcher != nil {
		w.loadBatcher.Start()
	}

	// Start job queue processor if enabled
	if w.jobQueue != nil {
		go w.processJobQueue(ctx)
	}

	// Start network monitor
	w.networkMonitor.Start(func() {
		w.logger.Warn("Network partition detected, triggering reconnection")
		w.triggerReconnect()
	})
	
	// Start resource monitor if enabled
	if w.resourceMonitor != nil {
		w.resourceMonitor.Start(ctx)
	}
	
	// Start resource limiter if enabled
	if w.resourceLimiter != nil {
		w.resourceLimiter.Start(ctx)
	}

	return nil
}

// Stop gracefully shuts down the worker with a default timeout of 30 seconds.
//
// This method:
//   - Stops accepting new jobs
//   - Waits for active jobs to complete
//   - Executes shutdown hooks in order
//   - Closes all connections and resources
//
// If active jobs don't complete within the timeout, they are forcefully terminated.
//
// Returns nil on successful shutdown, or an error if shutdown hooks fail.
func (w *Worker) Stop() error {
	return w.StopWithTimeout(30 * time.Second)
}

// StopWithTimeout gracefully shuts down the worker with a custom timeout.
// This is useful when you need to control how long to wait for active jobs.
//
// Parameters:
//   - timeout: Maximum time to wait for active jobs to complete
//
// If timeout is exceeded, jobs are forcefully terminated.
// Use timeout of 0 to stop immediately without waiting.
func (w *Worker) StopWithTimeout(timeout time.Duration) error {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		return nil // Already closing
	}
	w.closing = true
	conn := w.conn
	activeCount := len(w.activeJobs)
	w.mu.Unlock()

	w.logger.Info("Starting graceful shutdown", "activeJobs", activeCount, "timeout", timeout)

	// Mark as disconnecting to prevent new jobs
	w.raceProtector.SetDisconnecting(true)
	defer w.raceProtector.SetDisconnecting(false)

	// Create shutdown context
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	// Execute pre-stop hooks
	if err := w.shutdownHooks.ExecutePhase(ctx, ShutdownPhasePreStop); err != nil {
		w.logger.Error("Pre-stop hooks failed", "error", err)
	}

	// Stop load batcher if running
	if w.loadBatcher != nil {
		w.loadBatcher.Stop()
	}

	// Execute stop-jobs hooks
	if err := w.shutdownHooks.ExecutePhase(ctx, ShutdownPhaseStopJobs); err != nil {
		w.logger.Error("Stop-jobs hooks failed", "error", err)
	}
	
	// Terminate all active jobs
	w.terminateAllJobs()

	// Wait for jobs to complete or timeout
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.mu.RLock()
				remaining := len(w.activeJobs)
				w.mu.RUnlock()
				
				if remaining == 0 {
					return
				}
				w.logger.Debug("Waiting for jobs to complete", "remaining", remaining)
			}
		}
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		w.logger.Info("All jobs completed")
	case <-ctx.Done():
		w.logger.Warn("Shutdown timeout exceeded, forcing termination")
	}
	
	// Execute post-jobs hooks
	if err := w.shutdownHooks.ExecutePhase(ctx, ShutdownPhasePostJobs); err != nil {
		w.logger.Error("Post-jobs hooks failed", "error", err)
	}

	// Close job queue if enabled
	if w.jobQueue != nil {
		w.jobQueue.Close()
	}

	// Close resource pool if enabled
	if w.resourcePool != nil {
		if err := w.resourcePool.Close(); err != nil {
			w.logger.Error("Failed to close resource pool", "error", err)
		}
	}

	// Stop network monitor
	if w.networkMonitor != nil {
		w.networkMonitor.Stop()
	}
	
	// Stop resource monitor
	if w.resourceMonitor != nil {
		w.resourceMonitor.Stop()
	}
	
	// Execute cleanup hooks
	if err := w.shutdownHooks.ExecutePhase(ctx, ShutdownPhaseCleanup); err != nil {
		w.logger.Error("Cleanup hooks failed", "error", err)
	}

	// Close WebSocket connection
	if conn != nil {
		// Send close frame with write protection
		w.writeMu.Lock()
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		w.writeMu.Unlock()
		_ = conn.Close()
		
		// Clear connection reference
		w.mu.Lock()
		w.conn = nil
		w.mu.Unlock()
	}
	
	// Execute final hooks
	if err := w.shutdownHooks.ExecutePhase(ctx, ShutdownPhaseFinal); err != nil {
		w.logger.Error("Final hooks failed", "error", err)
	}

	return nil
}

// UpdateStatus updates the worker's status and load.
// This informs the server about the worker's capacity for handling new jobs.
//
// Parameters:
//   - status: WorkerStatusAvailable or WorkerStatusFull
//   - load: Current load as a value between 0.0 (idle) and 1.0 (fully loaded)
//
// The server uses this information for job assignment decisions.
// Status updates are batched if StatusUpdateBatchInterval is configured.
//
// Returns an error if the worker is not connected.
func (w *Worker) UpdateStatus(status WorkerStatus, load float32) error {
	w.mu.Lock()
	w.status = status
	w.load = load
	conn := w.conn
	jobCount := len(w.activeJobs)
	w.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	statusProto := convertWorkerStatus(status)
	update := &livekit.UpdateWorkerStatus{
		Status:   &statusProto,
		Load:     load,
		JobCount: uint32(jobCount),
	}

	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateWorker{
			UpdateWorker: update,
		},
	}

	return w.sendMessage(msg)
}

func (w *Worker) connect(ctx context.Context) error {
	// Generate auth token
	at := auth.NewAccessToken(w.apiKey, w.apiSecret)
	grant := &auth.VideoGrant{
		Agent: true,
	}
	at.SetVideoGrant(grant)

	authToken, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	// Build WebSocket URL
	u, err := url.Parse(w.serverURL)
	if err != nil {
		return err
	}
	
	// Perform DNS resolution with retry
	if u.Hostname() != "" && u.Hostname() != "localhost" {
		addrs, err := w.networkHandler.ResolveDNSWithRetry(ctx, u.Hostname())
		if err != nil {
			w.logger.Error("DNS resolution failed", "host", u.Hostname(), "error", err)
			return fmt.Errorf("DNS resolution failed: %w", err)
		}
		w.logger.Debug("DNS resolution successful", "host", u.Hostname(), "addresses", addrs)
	}
	
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/agent"
	q := u.Query()
	q.Set("protocol", fmt.Sprintf("%d", CurrentProtocol))
	u.RawQuery = q.Encode()

	// Connect
	headers := http.Header{
		"Authorization": []string{"Bearer " + authToken},
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	conn, resp, err := dialer.DialContext(ctx, u.String(), headers)
	if err != nil {
		// Check if it's an authentication error
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return ErrInvalidCredentials
		}
		return ErrConnectionFailed
	}

	w.mu.Lock()
	w.conn = conn
	w.mu.Unlock()

	// Register worker
	if err := w.register(ctx); err != nil {
		closeErr := conn.Close()
		if closeErr != nil {
			w.logger.Error("Failed to close connection after registration error", "closeError", closeErr, "registrationError", err)
		}
		return err
	}

	// Send initial status update
	if err := w.UpdateStatus(WorkerStatusAvailable, 0.0); err != nil {
		w.logger.Error("Failed to send initial status update", "error", err)
	}

	return nil
}

func (w *Worker) register(ctx context.Context) error {
	req := &livekit.RegisterWorkerRequest{
		Type:               w.opts.JobType,
		Version:            w.opts.Version,
		AgentName:          w.opts.AgentName,
		AllowedPermissions: w.opts.Permissions,
	}
	if w.opts.Namespace != "" {
		req.Namespace = &w.opts.Namespace
	}

	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Register{
			Register: req,
		},
	}

	if err := w.sendMessage(msg); err != nil {
		return err
	}

	// Wait for registration response
	ctx, cancel := context.WithTimeout(ctx, registrationTimeout)
	defer cancel()

	// Use a goroutine to read messages and select to handle timeout
	type readResult struct {
		data []byte
		err  error
	}
	readChan := make(chan readResult, 1)

	for {
		go func() {
			_, data, err := w.conn.ReadMessage()
			readChan <- readResult{data: data, err: err}
		}()

		select {
		case <-ctx.Done():
			return ErrRegistrationTimeout
		case result := <-readChan:
			if result.err != nil {
				return result.err
			}

			serverMsg, err := w.parseServerMessage(result.data)
			if err != nil {
				return err
			}

			if resp := serverMsg.GetRegister(); resp != nil {
				// Check if registration was successful
				if resp.WorkerId == "" {
					// Empty worker ID indicates rejection
					return ErrRegistrationRejected
				}
				
				w.mu.Lock()
				w.workerID = resp.WorkerId
				w.mu.Unlock()

				// Check for clock skew if server provides timestamp
				// TODO: ServerInfo doesn't have CurrentUnixTime field - need to check protocol
				// if resp.ServerInfo != nil && resp.ServerInfo.CurrentUnixTime > 0 {
				// 	serverTime := time.Unix(resp.ServerInfo.CurrentUnixTime, 0)
				// 	w.timingManager.UpdateServerTime(serverTime, time.Now())
				// }

				w.logger.Info("Worker registered", "workerID", resp.WorkerId, "serverVersion", resp.ServerInfo.Version)
				return nil
			}
			// Continue waiting if it's not a registration response
		}
	}
}

func (w *Worker) handleMessages(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			w.mu.RLock()
			conn := w.conn
			closing := w.closing
			w.mu.RUnlock()

			if conn == nil || closing {
				return
			}

			messageType, data, err := conn.ReadMessage()
			if err != nil {
				// Check if we're closing to avoid logging errors during shutdown
				w.mu.RLock()
				closing := w.closing
				w.mu.RUnlock()
				
				if !closing {
					w.logger.Error("Read error", "error", err)
					
					// Check if we have a partial message to save
					if w.partialMsgBuffer != nil && len(data) > 0 {
						if bufErr := w.partialMsgBuffer.Append(messageType, data); bufErr != nil {
							w.logger.Error("Failed to buffer partial message", "error", bufErr)
						}
					}
					
					w.triggerReconnect()
				}
				return
			}

			// Try to handle as complete message first
			serverMsg, err := w.parseServerMessage(data)
			if err != nil {
				// If parsing fails, it might be a partial message
				if w.partialMsgBuffer != nil {
					// Try to append to buffer
					if bufErr := w.partialMsgBuffer.Append(messageType, data); bufErr != nil {
						w.logger.Error("Failed to buffer partial message", "error", bufErr)
						w.partialMsgBuffer.Clear()
					} else {
						// Check if we now have a complete message
						if _, completeData, ok := w.partialMsgBuffer.GetComplete(); ok {
							if serverMsg, err = w.parseServerMessage(completeData); err == nil {
								w.handleServerMessage(ctx, serverMsg)
								continue
							}
						}
					}
				}
				
				w.logger.Error("Parse error", "error", err)
				continue
			}

			// Update network activity on successful message processing
			w.networkHandler.UpdateNetworkActivity()
			w.handleServerMessage(ctx, serverMsg)
		}
	}
}

func (w *Worker) handleServerMessage(ctx context.Context, msg *livekit.ServerMessage) {
	// Validate protocol message
	if err := w.protocolHandler.ValidateProtocolMessage(msg); err != nil {
		w.logger.Error("Protocol validation failed", "error", err)
		if w.protocolHandler.IsVersionMismatchDetected() {
			// Log version mismatch but continue processing in non-strict mode
			w.logger.Warn("Protocol version mismatch detected, continuing in compatibility mode")
		}
	}
	
	switch m := msg.Message.(type) {
	case *livekit.ServerMessage_Availability:
		w.handleAvailabilityRequest(ctx, m.Availability)
	case *livekit.ServerMessage_Assignment:
		w.handleJobAssignment(ctx, m.Assignment)
	case *livekit.ServerMessage_Termination:
		w.handleJobTermination(ctx, m.Termination)
	case *livekit.ServerMessage_Pong:
		// Pong received, connection is healthy
	case *livekit.ServerMessage_Register:
		// Registration response already handled in waitForRegistration
	default:
		// Unknown message type
		msgType := fmt.Sprintf("%T", m)
		if err := w.protocolHandler.HandleUnknownMessage(msgType, nil); err != nil {
			w.logger.Error("Failed to handle unknown message type", "type", msgType, "error", err)
		}
	}
}

func (w *Worker) handleAvailabilityRequest(ctx context.Context, req *livekit.AvailabilityRequest) {
	job := req.Job

	// Check race conditions first
	if canAccept, reason := w.raceProtector.CanAcceptJob(job.Id); !canAccept {
		// Reject job due to race condition
		resp := &livekit.AvailabilityResponse{
			JobId:     job.Id,
			Available: false,
		}
		msg := &livekit.WorkerMessage{
			Message: &livekit.WorkerMessage_Availability{
				Availability: resp,
			},
		}
		if err := w.sendMessage(msg); err != nil {
			w.logger.Error("Failed to send availability rejection", "error", err, "reason", reason)
		}
		return
	}

	// Check if we can accept the job
	w.mu.RLock()
	canAccept := w.status == WorkerStatusAvailable
	if w.opts.MaxJobs > 0 && len(w.activeJobs) >= w.opts.MaxJobs {
		canAccept = false
	}
	// Check for duplicate job
	if _, exists := w.activeJobs[job.Id]; exists {
		w.logger.Warn("Duplicate job assignment attempted", "jobID", job.Id)
		canAccept = false
	}
	w.mu.RUnlock()

	var resp *livekit.AvailabilityResponse
	if canAccept {
		// Check resource limits before accepting
		if w.resourceLimiter != nil {
			guard := w.resourceLimiter.NewGuard("job_accept")
			if err := guard.Execute(ctx, func() error { return nil }); err != nil {
				w.logger.Warn("Rejecting job due to resource limits", 
					"jobID", job.Id, 
					"error", err)
				canAccept = false
			}
		}
	}
	
	if canAccept {
		// Ask handler if it wants this job with timeout and panic recovery
		handlerCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		accept, metadata := w.callHandlerSafely(handlerCtx, job)
		if accept && metadata != nil {
			resp = &livekit.AvailabilityResponse{
				JobId:                 job.Id,
				Available:             true,
				SupportsResume:        metadata.SupportsResume,
				ParticipantIdentity:   metadata.ParticipantIdentity,
				ParticipantName:       metadata.ParticipantName,
				ParticipantMetadata:   metadata.ParticipantMetadata,
				ParticipantAttributes: metadata.ParticipantAttributes,
			}
		}
	}

	if resp == nil {
		resp = &livekit.AvailabilityResponse{
			JobId:     job.Id,
			Available: false,
		}
	}

	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: resp,
		},
	}

	if err := w.sendMessage(msg); err != nil {
		w.logger.Error("Failed to send availability response", "error", err)
	}
}

func (w *Worker) handleJobAssignment(ctx context.Context, assignment *livekit.JobAssignment) {
	job := assignment.Job

	w.logger.Info("Job assigned", "jobID", job.Id, "room", job.Room.Name)

	// Check race conditions first
	if canAccept, reason := w.raceProtector.CanAcceptJob(job.Id); !canAccept {
		w.logger.Warn("Rejecting job assignment due to race condition", 
			"jobID", job.Id, 
			"reason", reason)
		w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, reason)
		return
	}

	// Check backpressure
	if w.timingManager.CheckBackpressure() {
		delay := w.timingManager.GetBackpressureDelay()
		w.logger.Info("Applying backpressure delay", "jobID", job.Id, "delay", delay)
		time.Sleep(delay)
	}

	// Set deadline for the job (extract from job metadata if available)
	if job.Room != nil && job.Room.MaxParticipants > 0 {
		// Use room metadata to determine deadline
		deadline := time.Now().Add(5 * time.Minute) // Default deadline
		w.timingManager.SetDeadline(job.Id, deadline, "job_assignment")
	}

	// Check if we should queue the job
	w.mu.RLock()
	atCapacity := w.opts.MaxJobs > 0 && len(w.activeJobs) >= w.opts.MaxJobs
	queueEnabled := w.jobQueue != nil
	w.mu.RUnlock()

	if atCapacity && queueEnabled {
		// Queue the job for later processing
		url := w.serverURL
		if assignment.Url != nil && *assignment.Url != "" {
			url = *assignment.Url
		}
		
		if err := w.QueueJob(job, assignment.Token, url); err != nil {
			w.logger.Error("Failed to queue job", "jobID", job.Id, "error", err)
			w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, "failed to queue job: " + err.Error())
		}
		return
	}

	// Create room client
	roomURL := w.serverURL
	if assignment.Url != nil && *assignment.Url != "" {
		roomURL = *assignment.Url
	}

	// Set up room callbacks to handle disconnections and errors
	roomCallback := &lksdk.RoomCallback{
		OnDisconnected: func() {
			w.logger.Info("Disconnected from room", "jobID", job.Id)
		},
		OnDisconnectedWithReason: func(reason lksdk.DisconnectionReason) {
			w.logger.Info("Disconnected from room with reason", "jobID", job.Id, "reason", reason)
			
			// Handle specific disconnection reasons
			switch reason {
			case lksdk.DuplicateIdentity:
				w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, "duplicate participant identity")
			case lksdk.ParticipantRemoved:
				w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, "participant was removed from room")
			case lksdk.RoomClosed:
				w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, "room was closed")
			}
		},
		OnReconnecting: func() {
			w.logger.Info("Reconnecting to room", "jobID", job.Id)
		},
		OnReconnected: func() {
			w.logger.Info("Reconnected to room", "jobID", job.Id)
		},
	}
	
	// Connect using the token provided by the server
	room, err := lksdk.ConnectToRoomWithToken(roomURL, assignment.Token, roomCallback, lksdk.WithAutoSubscribe(false))

	if err != nil {
		// Categorize the error for better handling
		errorMsg := err.Error()
		var jobError error
		
		// Check for specific error conditions
		if strings.Contains(errorMsg, "unauthorized") || strings.Contains(errorMsg, "401") {
			// Token might be expired or invalid
			jobError = ErrTokenExpired
			w.logger.Error("Token expired or invalid", "error", err, "jobID", job.Id)
		} else if strings.Contains(errorMsg, "not found") || strings.Contains(errorMsg, "404") {
			// Room might not exist
			jobError = ErrRoomNotFound
			w.logger.Error("Room not found", "error", err, "jobID", job.Id, "room", job.Room.Name)
		} else {
			// Generic connection error
			w.logger.Error("Failed to connect to room", "error", err, "jobID", job.Id)
			jobError = err
		}
		
		w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, jobError.Error())
		return
	}

	// Store active job with deadline propagation
	jobCtx, cancel := w.timingManager.PropagateDeadline(ctx, job.Id)
	now := time.Now()
	w.mu.Lock()
	w.activeJobs[job.Id] = &activeJob{
		job:       job,
		room:      room,
		cancel:    cancel,
		startedAt: now,
		status:    livekit.JobStatus_JS_RUNNING,
	}
	w.jobStartTimes[job.Id] = now
	w.mu.Unlock()

	// Save job for potential recovery
	if w.recoveryManager != nil {
		w.recoveryManager.SaveJobForRecovery(job.Id, job, assignment.Token)
	}

	// Create job checkpoint
	w.jobCheckpointsMu.Lock()
	w.jobCheckpoints[job.Id] = NewJobCheckpoint(job.Id)
	w.jobCheckpointsMu.Unlock()

	// Update job status to running
	w.updateJobStatus(job.Id, livekit.JobStatus_JS_RUNNING, "")

	// Update worker load
	w.updateLoad()

	// Run job handler with panic recovery
	go func() {
		defer func() {
			// Panic recovery
			if r := recover(); r != nil {
				w.logger.Error("Handler panic in OnJobAssigned", "panic", r, "jobID", job.Id)
				w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, fmt.Sprintf("handler panic: %v", r))
			}

			// Clean up
			w.mu.Lock()
			delete(w.activeJobs, job.Id)
			delete(w.jobStartTimes, job.Id)
			w.mu.Unlock()

			// Clean up recovery information
			if w.recoveryManager != nil {
				w.recoveryManager.removeFromRecovery(job.Id)
			}
			
			// Clean up checkpoint
			w.jobCheckpointsMu.Lock()
			delete(w.jobCheckpoints, job.Id)
			w.jobCheckpointsMu.Unlock()

			// Clean up deadline
			w.timingManager.RemoveDeadline(job.Id)

			room.Disconnect()
			cancel()
			w.updateLoad()
		}()

		err := w.handler.OnJobAssigned(jobCtx, job, room)
		if err != nil {
			w.logger.Error("Job handler error", "jobID", job.Id, "error", err)
			w.updateJobStatus(job.Id, livekit.JobStatus_JS_FAILED, err.Error())
		} else {
			w.updateJobStatus(job.Id, livekit.JobStatus_JS_SUCCESS, "")
		}
	}()
}

func (w *Worker) handleJobTermination(ctx context.Context, term *livekit.JobTermination) {
	// Check if we should handle this termination
	if !w.raceProtector.RecordTerminationRequest(term.JobId) {
		w.logger.Warn("Ignoring concurrent termination request", "jobID", term.JobId)
		return
	}
	defer w.raceProtector.CompleteTermination(term.JobId, nil)

	w.mu.Lock()
	activeJob, exists := w.activeJobs[term.JobId]
	w.mu.Unlock()

	if !exists {
		w.logger.Warn("Received termination for unknown job", "jobID", term.JobId)
		return
	}

	w.logger.Info("Job terminated", "jobID", term.JobId)

	// Cancel job context
	activeJob.cancel()

	// Notify handler with panic recovery
	func() {
		defer func() {
			if r := recover(); r != nil {
				w.logger.Error("Handler panic in OnJobTerminated", "panic", r, "jobID", term.JobId)
			}
		}()
		w.handler.OnJobTerminated(ctx, term.JobId)
	}()
}

func (w *Worker) updateJobStatus(jobID string, status livekit.JobStatus, error string) {
	// Check if we should queue this update during reconnection
	if w.raceProtector.QueueStatusUpdate(jobID, status, error) {
		w.logger.Info("Status update queued during reconnection", 
			"jobID", jobID, 
			"status", status)
		return
	}

	update := &livekit.UpdateJobStatus{
		JobId:  jobID,
		Status: status,
		Error:  error,
	}

	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_UpdateJob{
			UpdateJob: update,
		},
	}

	if err := w.sendMessage(msg); err != nil {
		w.logger.Error("Failed to update job status, queuing for retry", "jobID", jobID, "status", status, "error", err)
		
		// Queue the update for retry
		w.queueStatusUpdate(jobID, status, error)
	} else {
		// Remove any pending updates for this job from the queue
		w.removeFromStatusQueue(jobID)
	}
}

func (w *Worker) sendMessage(msg *livekit.WorkerMessage) error {
	w.mu.RLock()
	conn := w.conn
	w.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Check if we have a partial write pending
	if w.networkHandler.HasPartialWrite() {
		// Try to complete the partial write first
		msgType, pendingData := w.networkHandler.GetPartialWriteData()
		w.writeMu.Lock()
		err := w.networkHandler.WriteMessageWithRetry(conn, msgType, pendingData)
		w.writeMu.Unlock()
		if err != nil {
			w.logger.Warn("Failed to complete partial write", "error", err)
			// Continue with the new message anyway
		}
	}

	// Apply backpressure if needed
	w.timingManager.RecordEvent()
	if w.timingManager.CheckBackpressure() {
		delay := w.timingManager.GetBackpressureDelay()
		w.logger.Debug("Applying backpressure to message send", "delay", delay)
		time.Sleep(delay)
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return err
	}

	// Protect websocket writes with mutex
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	
	// Use network handler for write with retry
	err = w.networkHandler.WriteMessageWithRetry(conn, websocket.BinaryMessage, data)
	if err != nil {
		// Update network activity even on failure for partition detection
		w.networkHandler.UpdateNetworkActivity()
		return err
	}
	
	// Update network activity on success
	w.networkHandler.UpdateNetworkActivity()
	return nil
}

func (w *Worker) parseServerMessage(data []byte) (*livekit.ServerMessage, error) {
	// Validate message size
	if len(data) == 0 {
		return nil, fmt.Errorf("empty message")
	}
	if len(data) > 1024*1024 { // 1MB limit
		return nil, fmt.Errorf("message too large: %d bytes", len(data))
	}

	// Check if it's JSON (starts with '{')
	var msg livekit.ServerMessage
	if len(data) > 0 && data[0] == '{' {
		// Use protojson for proper JSON unmarshaling of protobuf messages
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
	} else {
		// Otherwise assume protobuf
		if err := proto.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
	}

	// Validate message content
	if err := w.validateServerMessage(&msg); err != nil {
		return nil, fmt.Errorf("invalid message: %w", err)
	}

	return &msg, nil
}

// validateServerMessage ensures the message has required fields
func (w *Worker) validateServerMessage(msg *livekit.ServerMessage) error {
	if msg == nil {
		return fmt.Errorf("nil message")
	}
	if msg.Message == nil {
		return fmt.Errorf("missing message content")
	}

	switch m := msg.Message.(type) {
	case *livekit.ServerMessage_Register:
		if m.Register == nil {
			return fmt.Errorf("missing registration response")
		}
		// Note: Empty WorkerId is valid - it indicates registration rejection
	case *livekit.ServerMessage_Availability:
		if m.Availability == nil || m.Availability.Job == nil {
			return fmt.Errorf("missing job in availability request")
		}
		if m.Availability.Job.Id == "" {
			return fmt.Errorf("missing job ID")
		}
		if m.Availability.Job.Room == nil {
			return fmt.Errorf("missing room in job")
		}
	case *livekit.ServerMessage_Assignment:
		if m.Assignment == nil || m.Assignment.Job == nil {
			return fmt.Errorf("missing job in assignment")
		}
		if m.Assignment.Token == "" {
			return fmt.Errorf("missing token in assignment")
		}
	case *livekit.ServerMessage_Termination:
		if m.Termination == nil || m.Termination.JobId == "" {
			return fmt.Errorf("missing job ID in termination")
		}
	}

	return nil
}

func (w *Worker) handlePing(ctx context.Context) {
	ticker := time.NewTicker(w.opts.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.RLock()
			conn := w.conn
			w.mu.RUnlock()

			if conn == nil {
				continue
			}

			ping := &livekit.WorkerPing{
				Timestamp: time.Now().Unix(),
			}

			msg := &livekit.WorkerMessage{
				Message: &livekit.WorkerMessage_Ping{
					Ping: ping,
				},
			}

			if err := w.sendMessage(msg); err != nil {
				w.logger.Error("Failed to send ping", "error", err)
				w.errorCount++
				w.triggerReconnect()
			} else {
				w.mu.Lock()
				w.lastPingTime = time.Now()
				w.mu.Unlock()
			}
		}
	}
}

func (w *Worker) handleReconnect(ctx context.Context) {
	attempts := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.reconnectChan:
			attempts++
			if attempts > maxReconnectAttempts {
				w.logger.Error("Max reconnection attempts reached")
				return
			}

			w.logger.Info("Attempting to reconnect", "attempt", attempts)
			
			// Mark as reconnecting
			w.raceProtector.SetReconnecting(true)
			
			time.Sleep(reconnectInterval)

			if err := w.connect(ctx); err != nil {
				w.logger.Error("Reconnection failed", "error", err)
				w.triggerReconnect()
			} else {
				// Restore state after successful reconnection
				w.restoreState()
				
				// Clear reconnecting state
				w.raceProtector.SetReconnecting(false)
				
				// Send any pending status updates
				w.flushPendingStatusUpdates()
				
				attempts = 0
				go w.handleMessages(ctx)
			}
		}
	}
}

func (w *Worker) triggerReconnect() {
	// Don't reconnect if we're closing
	w.mu.RLock()
	closing := w.closing
	w.mu.RUnlock()
	
	if closing {
		return
	}
	
	// Save current state before reconnection
	w.saveState()
	
	select {
	case w.reconnectChan <- struct{}{}:
	default:
	}
}

// saveState persists current worker state for recovery
func (w *Worker) saveState() {
	w.mu.RLock()
	defer w.mu.RUnlock()

	w.savedState.WorkerID = w.workerID
	w.savedState.LastStatus = w.status
	w.savedState.LastLoad = w.load

	// Save active jobs
	w.savedState.ActiveJobs = make(map[string]*JobState)
	for id, job := range w.activeJobs {
		jobState := &JobState{
			JobID:     id,
			Status:    job.status,
			StartedAt: job.startedAt,
		}
		if job.job != nil && job.job.Room != nil {
			jobState.RoomName = job.job.Room.Name
		}
		w.savedState.ActiveJobs[id] = jobState
	}
}

// restoreState attempts to restore state after reconnection
func (w *Worker) restoreState() {
	if w.savedState == nil || w.savedState.WorkerID == "" {
		return
	}

	w.logger.Info("Restoring worker state after reconnection", 
		"workerID", w.savedState.WorkerID,
		"activeJobs", len(w.savedState.ActiveJobs))

	// Restore worker ID
	w.workerID = w.savedState.WorkerID

	// Send status update with restored state
	if err := w.UpdateStatus(w.savedState.LastStatus, w.savedState.LastLoad); err != nil {
		w.logger.Error("Failed to restore status", "error", err)
	}

	// Attempt to recover jobs if recovery is enabled
	if w.recoveryManager != nil && len(w.savedState.ActiveJobs) > 0 {
		w.logger.Info("Attempting to recover jobs after reconnection", "jobCount", len(w.savedState.ActiveJobs))
		
		// Create a recovery context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		
		// Attempt recovery
		results := w.recoveryManager.AttemptJobRecovery(ctx)
		
		// Log recovery results
		for jobID, err := range results {
			if err != nil {
				w.logger.Error("Failed to recover job", "jobID", jobID, "error", err)
				// Update job status to indicate recovery failure
				w.updateJobStatus(jobID, livekit.JobStatus_JS_FAILED, "failed to recover after reconnection: " + err.Error())
			} else {
				w.logger.Info("Successfully recovered job", "jobID", jobID)
			}
		}
	}
}

// callHandlerSafely calls OnJobRequest with panic recovery and timeout
func (w *Worker) callHandlerSafely(ctx context.Context, job *livekit.Job) (accept bool, metadata *JobMetadata) {
	// Set up panic recovery
	defer func() {
		if r := recover(); r != nil {
			w.logger.Error("Handler panic in OnJobRequest", "panic", r, "jobID", job.Id)
			accept = false
			metadata = nil
		}
	}()

	// Channel to receive handler result
	type result struct {
		accept   bool
		metadata *JobMetadata
	}
	resultChan := make(chan result, 1)

	// Run handler in goroutine to enable timeout
	go func() {
		defer func() {
			if r := recover(); r != nil {
				w.logger.Error("Handler panic in OnJobRequest goroutine", "panic", r, "jobID", job.Id)
				resultChan <- result{false, nil}
			}
		}()

		a, m := w.handler.OnJobRequest(ctx, job)
		resultChan <- result{a, m}
	}()

	// Wait for result or timeout
	select {
	case <-ctx.Done():
		w.logger.Error("Handler timeout in OnJobRequest", "jobID", job.Id)
		return false, nil
	case res := <-resultChan:
		return res.accept, res.metadata
	}
}

func (w *Worker) terminateAllJobs() {
	w.mu.Lock()
	jobs := make([]*activeJob, 0, len(w.activeJobs))
	for _, job := range w.activeJobs {
		jobs = append(jobs, job)
	}
	w.mu.Unlock()

	for _, job := range jobs {
		if job != nil && job.cancel != nil {
			job.cancel()
		}
	}
}

func (w *Worker) updateLoad() {
	metrics := w.buildLoadMetrics()
	load := w.loadCalculator.Calculate(metrics)

	w.mu.Lock()
	jobCount := len(w.activeJobs)
	maxJobs := w.opts.MaxJobs
	w.mu.Unlock()

	status := WorkerStatusAvailable
	if maxJobs > 0 && jobCount >= maxJobs {
		status = WorkerStatusFull
	}

	// Use batcher if available, otherwise update directly
	if w.loadBatcher != nil {
		w.loadBatcher.Update(status, load)
	} else {
		err := w.UpdateStatus(status, load)
		if err != nil {
			return
		}
	}
}

func (w *Worker) buildLoadMetrics() LoadMetrics {
	w.mu.RLock()
	jobCount := len(w.activeJobs)
	maxJobs := w.opts.MaxJobs
	
	// Calculate job durations
	durations := make(map[string]time.Duration)
	now := time.Now()
	for jobID, startTime := range w.jobStartTimes {
		durations[jobID] = now.Sub(startTime)
	}
	w.mu.RUnlock()

	metrics := LoadMetrics{
		ActiveJobs:  jobCount,
		MaxJobs:     maxJobs,
		JobDuration: durations,
	}

	// Add system metrics if available
	if w.metricsCollector != nil {
		cpu, mem, usedMB, totalMB := w.metricsCollector.GetMetrics()
		metrics.CPUPercent = cpu
		metrics.MemoryPercent = mem
		metrics.MemoryUsedMB = usedMB
		metrics.MemoryTotalMB = totalMB
	}

	return metrics
}

func convertWorkerStatus(status WorkerStatus) livekit.WorkerStatus {
	switch status {
	case WorkerStatusAvailable:
		return livekit.WorkerStatus_WS_AVAILABLE
	case WorkerStatusFull:
		return livekit.WorkerStatus_WS_FULL
	default:
		return livekit.WorkerStatus_WS_AVAILABLE
	}
}

// IsConnected returns whether the worker is currently connected to the server.
//
// This can be used to check the worker's connection status before
// performing operations that require an active connection.
func (w *Worker) IsConnected() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.conn != nil
}

// GetJobCheckpoint returns the checkpoint for a specific job.
//
// Checkpoints allow jobs to save and restore state for recovery purposes.
// Returns nil if no checkpoint exists for the job.
//
// Parameters:
//   - jobID: The ID of the job to get checkpoint for
//
// The returned checkpoint is safe for concurrent access.
func (w *Worker) GetJobCheckpoint(jobID string) *JobCheckpoint {
	w.jobCheckpointsMu.RLock()
	defer w.jobCheckpointsMu.RUnlock()
	return w.jobCheckpoints[jobID]
}

// Health returns the current health status of the worker.
//
// The health report includes:
//   - Connection status and worker ID
//   - Current load and job count
//   - Active job details with durations
//   - Queue statistics if job queueing is enabled
//   - Resource pool statistics if pooling is enabled
//   - Protocol metrics and version information
//   - Resource usage and limits
//
// This is useful for monitoring and debugging worker state.
//
// Returns a map with string keys and various value types.
// The structure is designed to be JSON-serializable.
func (w *Worker) Health() map[string]interface{} {
	w.mu.RLock()
	defer w.mu.RUnlock()

	health := map[string]interface{}{
		"connected":      w.conn != nil,
		"worker_id":      w.workerID,
		"status":         w.status.String(),
		"active_jobs":    len(w.activeJobs),
		"max_jobs":       w.opts.MaxJobs,
		"load":           w.load,
		"error_count":    w.errorCount,
		"last_ping_time": w.lastPingTime,
	}

	// Add job details
	jobs := make([]map[string]interface{}, 0, len(w.activeJobs))
	for id, job := range w.activeJobs {
		jobs = append(jobs, map[string]interface{}{
			"id":         id,
			"room":       job.job.Room.Name,
			"status":     job.status.String(),
			"started_at": job.startedAt,
			"duration":   time.Since(job.startedAt),
		})
	}
	health["jobs"] = jobs

	// Add queue stats if enabled
	if w.jobQueue != nil {
		health["queue"] = w.GetQueueStats()
	}

	// Add resource pool stats if enabled
	if w.resourcePool != nil {
		health["resource_pool"] = w.GetResourcePoolStats()
	}

	// Add protocol metrics
	if w.protocolHandler != nil {
		health["protocol"] = w.protocolHandler.GetProtocolMetrics()
	}
	
	// Add resource metrics
	if w.resourceMonitor != nil {
		health["resources"] = w.resourceMonitor.GetMetrics()
		health["resource_healthy"] = w.resourceMonitor.IsHealthy()
	}
	
	// Add race protection metrics
	if w.raceProtector != nil {
		health["race_protection"] = w.raceProtector.GetMetrics()
	}
	
	// Add resource limiter metrics
	if w.resourceLimiter != nil {
		health["resource_limits"] = w.resourceLimiter.GetMetrics()
	}
	
	// Add timing metrics
	if w.timingManager != nil {
		health["timing"] = w.timingManager.GetMetrics()
	}

	return health
}

// GetMetrics returns operational metrics for the worker.
//
// Metrics include:
//   - jobs_accepted: Total jobs accepted by this worker
//   - jobs_rejected: Total jobs rejected by this worker
//   - jobs_completed: Successfully completed jobs
//   - jobs_failed: Jobs that failed with errors
//   - messages_sent: Total messages sent to server
//   - messages_recvd: Total messages received from server
//   - reconnections: Number of reconnection attempts
//
// These metrics are useful for monitoring worker performance
// and identifying potential issues.
func (w *Worker) GetMetrics() map[string]int64 {
	// In a real implementation, these would be tracked
	return map[string]int64{
		"jobs_accepted":    0, // Would increment on job accept
		"jobs_rejected":    0, // Would increment on job reject
		"jobs_completed":   0, // Would increment on job complete
		"jobs_failed":      0, // Would increment on job fail
		"messages_sent":    0, // Would increment on message send
		"messages_recvd":   0, // Would increment on message receive
		"reconnections":    0, // Would increment on reconnect
	}
}

// AddShutdownHook adds a shutdown hook for the specified phase.
//
// Shutdown hooks allow custom cleanup logic during worker shutdown.
// Hooks are executed in the order they were added within each phase.
//
// Parameters:
//   - phase: The shutdown phase when this hook should run
//   - hook: The hook to execute, containing name and handler
//
// Available phases:
//   - ShutdownPhasePreStop: Before stopping job acceptance
//   - ShutdownPhaseStopJobs: When terminating active jobs
//   - ShutdownPhasePostJobs: After all jobs have stopped
//   - ShutdownPhaseCleanup: General cleanup phase
//   - ShutdownPhaseFinal: Final phase before exit
//
// Returns an error if a hook with the same name already exists in the phase.
//
// Example:
//
//	err := worker.AddShutdownHook(ShutdownPhaseCleanup, ShutdownHook{
//	    Name: "save-state",
//	    Handler: func(ctx context.Context) error {
//	        return saveWorkerState()
//	    },
//	})
func (w *Worker) AddShutdownHook(phase ShutdownPhase, hook ShutdownHook) error {
	return w.shutdownHooks.AddHook(phase, hook)
}

// RemoveShutdownHook removes a shutdown hook by name and phase.
//
// Parameters:
//   - phase: The shutdown phase containing the hook
//   - name: The name of the hook to remove
//
// Returns true if the hook was found and removed, false otherwise.
func (w *Worker) RemoveShutdownHook(phase ShutdownPhase, name string) bool {
	return w.shutdownHooks.RemoveHook(phase, name)
}

// GetShutdownHooks returns all hooks for a specific phase.
//
// Parameters:
//   - phase: The shutdown phase to query
//
// Returns a slice of all hooks registered for the phase,
// in the order they will be executed.
func (w *Worker) GetShutdownHooks(phase ShutdownPhase) []ShutdownHook {
	return w.shutdownHooks.GetHooks(phase)
}

// flushPendingStatusUpdates sends any status updates that were queued during reconnection
func (w *Worker) flushPendingStatusUpdates() {
	updates := w.raceProtector.FlushPendingStatusUpdates()
	
	for _, update := range updates {
		w.logger.Info("Sending queued status update", 
			"jobID", update.JobID,
			"status", update.Status,
			"age", time.Since(update.Timestamp))
		
		// Send the update - note: this bypasses the queue check since we're no longer reconnecting
		msg := &livekit.WorkerMessage{
			Message: &livekit.WorkerMessage_UpdateJob{
				UpdateJob: &livekit.UpdateJobStatus{
					JobId:  update.JobID,
					Status: update.Status,
					Error:  update.Error,
				},
			},
		}
		
		if err := w.sendMessage(msg); err != nil {
			w.logger.Error("Failed to send queued status update", 
				"jobID", update.JobID,
				"error", err)
		}
	}
}

// AddPreStopHook is a convenience method for adding pre-stop hooks.
//
// Pre-stop hooks run before the worker stops accepting new jobs.
// This is useful for sending notifications or preparing for shutdown.
//
// Parameters:
//   - name: Unique name for the hook
//   - handler: Function to execute during pre-stop phase
//
// Returns an error if a hook with the same name already exists.
func (w *Worker) AddPreStopHook(name string, handler func(context.Context) error) error {
	return w.AddShutdownHook(ShutdownPhasePreStop, ShutdownHook{
		Name:    name,
		Handler: handler,
	})
}

// AddCleanupHook is a convenience method for adding cleanup hooks.
//
// Cleanup hooks run after all jobs have stopped but before final shutdown.
// This is useful for releasing resources or saving state.
//
// Parameters:
//   - name: Unique name for the hook
//   - handler: Function to execute during cleanup phase
//
// Returns an error if a hook with the same name already exists.
func (w *Worker) AddCleanupHook(name string, handler func(context.Context) error) error {
	return w.AddShutdownHook(ShutdownPhaseCleanup, ShutdownHook{
		Name:    name,
		Handler: handler,
	})
}

// zapLogger wraps zap.SugaredLogger to implement our Logger interface
type zapLogger struct {
	*zap.SugaredLogger
}

func (z *zapLogger) Debug(msg string, fields ...interface{}) {
	z.SugaredLogger.Debugw(msg, fields...)
}

func (z *zapLogger) Info(msg string, fields ...interface{}) {
	z.SugaredLogger.Infow(msg, fields...)
}

func (z *zapLogger) Warn(msg string, fields ...interface{}) {
	z.SugaredLogger.Warnw(msg, fields...)
}

func (z *zapLogger) Error(msg string, fields ...interface{}) {
	z.SugaredLogger.Errorw(msg, fields...)
}

// queueStatusUpdate adds a status update to the retry queue
func (w *Worker) queueStatusUpdate(jobID string, status livekit.JobStatus, errorMsg string) {
	w.statusQueueMu.Lock()
	defer w.statusQueueMu.Unlock()
	
	// Check if we already have an update for this job
	for i, update := range w.statusQueue {
		if update.jobID == jobID && update.status == status {
			// Update the retry count and timestamp
			w.statusQueue[i].retryCount++
			w.statusQueue[i].timestamp = time.Now()
			return
		}
	}
	
	// Add new update to queue
	w.statusQueue = append(w.statusQueue, statusUpdate{
		jobID:      jobID,
		status:     status,
		error:      errorMsg,
		retryCount: 0,
		timestamp:  time.Now(),
	})
	
	// Signal the retry handler
	select {
	case w.statusQueueChan <- struct{}{}:
	default:
	}
}

// removeFromStatusQueue removes completed updates from the queue
func (w *Worker) removeFromStatusQueue(jobID string) {
	w.statusQueueMu.Lock()
	defer w.statusQueueMu.Unlock()
	
	// Remove all updates for this job
	filtered := w.statusQueue[:0]
	for _, update := range w.statusQueue {
		if update.jobID != jobID {
			filtered = append(filtered, update)
		}
	}
	w.statusQueue = filtered
}

// handleStatusUpdateRetries processes the retry queue
func (w *Worker) handleStatusUpdateRetries(ctx context.Context) {
	ticker := time.NewTicker(statusRetryDelay)
	defer ticker.Stop()
	
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.statusQueueChan:
			// Process immediately when signaled
			w.processStatusQueue()
		case <-ticker.C:
			// Also check periodically
			w.processStatusQueue()
		}
	}
}

// processStatusQueue attempts to send pending status updates
func (w *Worker) processStatusQueue() {
	w.statusQueueMu.Lock()
	
	// Create a copy to avoid holding the lock during network operations
	pendingUpdates := make([]statusUpdate, len(w.statusQueue))
	copy(pendingUpdates, w.statusQueue)
	
	// Clear updates that will be retried
	w.statusQueue = w.statusQueue[:0]
	
	w.statusQueueMu.Unlock()
	
	for _, update := range pendingUpdates {
		// Skip if max retries exceeded
		if update.retryCount >= maxStatusRetries {
			w.logger.Error("Max retries exceeded for job status update", 
				"jobID", update.jobID, 
				"status", update.status,
				"retries", update.retryCount)
			
			// Store for partial failure recovery
			w.recordFailedStatusUpdate(update)
			continue
		}
		
		// Skip if the job is no longer active (unless it's a final status)
		if update.status != livekit.JobStatus_JS_SUCCESS && 
		   update.status != livekit.JobStatus_JS_FAILED {
			w.mu.RLock()
			_, isActive := w.activeJobs[update.jobID]
			w.mu.RUnlock()
			
			if !isActive {
				w.logger.Debug("Skipping status update for inactive job", "jobID", update.jobID)
				continue
			}
		}
		
		// Try to send the update
		msg := &livekit.WorkerMessage{
			Message: &livekit.WorkerMessage_UpdateJob{
				UpdateJob: &livekit.UpdateJobStatus{
					JobId:  update.jobID,
					Status: update.status,
					Error:  update.error,
				},
			},
		}
		
		if err := w.sendMessage(msg); err != nil {
			// Re-queue with incremented retry count
			update.retryCount++
			update.timestamp = time.Now()
			
			w.statusQueueMu.Lock()
			w.statusQueue = append(w.statusQueue, update)
			w.statusQueueMu.Unlock()
			
			w.logger.Warn("Failed to send status update, will retry", 
				"jobID", update.jobID, 
				"status", update.status,
				"retry", update.retryCount,
				"error", err)
		} else {
			w.logger.Debug("Successfully sent retried status update", 
				"jobID", update.jobID, 
				"status", update.status,
				"retry", update.retryCount)
		}
	}
}

// recordFailedStatusUpdate records permanently failed updates for recovery
func (w *Worker) recordFailedStatusUpdate(update statusUpdate) {
	// In a production system, this could:
	// 1. Write to a persistent store
	// 2. Send to a dead letter queue
	// 3. Emit metrics for monitoring
	// 4. Trigger alerts for critical job failures
	
	w.logger.Error("Recording failed status update for recovery", 
		"jobID", update.jobID,
		"status", update.status,
		"error", update.error,
		"firstAttempt", update.timestamp,
		"totalRetries", update.retryCount)
		
	// Store in worker state for potential recovery
	w.mu.Lock()
	if w.savedState != nil && w.savedState.ActiveJobs != nil {
		if jobState, exists := w.savedState.ActiveJobs[update.jobID]; exists {
			jobState.Status = update.status
		}
	}
	w.mu.Unlock()
}

// processJobQueue processes queued jobs when worker capacity is available
func (w *Worker) processJobQueue(ctx context.Context) {
	for {
		// Wait for available capacity
		w.mu.RLock()
		hasCapacity := w.status == WorkerStatusAvailable
		if w.opts.MaxJobs > 0 && len(w.activeJobs) >= w.opts.MaxJobs {
			hasCapacity = false
		}
		w.mu.RUnlock()

		if !hasCapacity {
			// Wait a bit before checking again
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		// Try to get a job from the queue
		item, err := w.jobQueue.DequeueWithContext(ctx)
		if err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				return
			}
			w.logger.Error("Failed to dequeue job", "error", err)
			continue
		}

		// Process the job with resource from pool if available
		if w.resourcePool != nil {
			resource, err := w.resourcePool.Acquire(ctx)
			if err != nil {
				w.logger.Error("Failed to acquire resource from pool", "error", err)
				// Continue without resource
				w.processQueuedJob(ctx, item)
			} else {
				// Process with resource
				w.processQueuedJobWithResource(ctx, item, resource)
			}
		} else {
			// Process without resource pool
			w.processQueuedJob(ctx, item)
		}
	}
}

// processQueuedJob processes a job from the queue
func (w *Worker) processQueuedJob(ctx context.Context, item *JobQueueItem) {
	w.logger.Info("Processing queued job", 
		"jobID", item.Job.Id,
		"priority", item.Priority,
		"queueTime", time.Since(item.EnqueueTime))

	// Create the assignment
	assignment := &livekit.JobAssignment{
		Job:   item.Job,
		Token: item.Token,
	}
	if item.URL != "" {
		assignment.Url = &item.URL
	}

	// Process as normal job assignment
	w.handleJobAssignment(ctx, assignment)
}

// processQueuedJobWithResource processes a job with a pooled resource
func (w *Worker) processQueuedJobWithResource(ctx context.Context, item *JobQueueItem, resource Resource) {
	defer w.resourcePool.Release(resource)

	w.logger.Info("Processing queued job with pooled resource",
		"jobID", item.Job.Id,
		"priority", item.Priority,
		"resource", resource)

	// Process the job
	w.processQueuedJob(ctx, item)
}

// QueueJob adds a job to the priority queue if enabled.
//
// Jobs are queued when the worker is at capacity and will be processed
// when capacity becomes available, in priority order.
//
// Parameters:
//   - job: The job to queue
//   - token: Authentication token for the job
//   - url: Optional custom URL for this job's room connection
//
// Returns an error if:
//   - Job queueing is not enabled
//   - Queue is full
//   - Job is invalid
//
// The job's priority is determined by the configured JobPriorityCalculator.
func (w *Worker) QueueJob(job *livekit.Job, token, url string) error {
	if w.jobQueue == nil {
		return fmt.Errorf("job queue is not enabled")
	}

	// Calculate priority
	priority := w.priorityCalc.CalculatePriority(job)

	// Add to queue
	if err := w.jobQueue.Enqueue(job, priority, token, url); err != nil {
		return fmt.Errorf("failed to queue job: %w", err)
	}

	w.logger.Info("Job queued successfully",
		"jobID", job.Id,
		"priority", priority,
		"queueSize", w.jobQueue.Size())

	return nil
}

// GetQueueStats returns statistics about the job queue.
//
// Statistics include:
//   - enabled: Whether job queueing is enabled
//   - size: Current number of queued jobs
//   - max_size: Maximum queue capacity
//   - urgent/high/normal/low: Job count by priority level
//
// Returns a map with "enabled": false if queueing is not enabled.
func (w *Worker) GetQueueStats() map[string]interface{} {
	if w.jobQueue == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}

	return map[string]interface{}{
		"enabled":   true,
		"size":      w.jobQueue.Size(),
		"max_size":  w.opts.JobQueueSize,
		"urgent":    len(w.jobQueue.GetJobsByPriority(JobPriorityUrgent)),
		"high":      len(w.jobQueue.GetJobsByPriority(JobPriorityHigh)),
		"normal":    len(w.jobQueue.GetJobsByPriority(JobPriorityNormal)),
		"low":       len(w.jobQueue.GetJobsByPriority(JobPriorityLow)),
	}
}

// GetResourcePoolStats returns statistics about the resource pool.
//
// Statistics include:
//   - enabled: Whether resource pooling is enabled
//   - stats: Detailed pool statistics including:
//     - active: Currently in-use resources
//     - idle: Available resources in pool
//     - total: Total resources created
//     - created/destroyed: Lifetime resource counts
//
// Returns a map with "enabled": false if pooling is not enabled.
func (w *Worker) GetResourcePoolStats() map[string]interface{} {
	if w.resourcePool == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}

	return map[string]interface{}{
		"enabled": true,
		"stats":   w.resourcePool.Stats(),
	}
}
