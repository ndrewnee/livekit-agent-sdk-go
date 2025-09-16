package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// WebSocket connection methods for UniversalWorker

// connect establishes a WebSocket connection to the LiveKit server
func (w *UniversalWorker) connect(ctx context.Context) error {
	// Generate authentication token with expiry
	token, expiresAt, err := w.generateAuthToken()
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	// Store token information
	w.mu.Lock()
	w.tokenManagement.currentToken = token
	w.tokenManagement.expiresAt = expiresAt
	w.mu.Unlock()

	// Start token renewal timer
	w.startTokenRenewalTimer()

	// Connect to WebSocket
	wsURL := buildWebSocketURL(w.serverURL)
	dialer := websocket.DefaultDialer

	// Set connection parameters
	dialer.HandshakeTimeout = 10 * time.Second

	// Set authorization header
	headers := make(map[string][]string)
	headers["Authorization"] = []string{"Bearer " + token}

	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	w.mu.Lock()
	w.conn = conn
	w.wsState = WebSocketStateConnecting
	w.mu.Unlock()

	// Send initial registration
	if err := w.sendRegister(); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send registration: %w", err)
	}

	// Wait for registration response
	if err := w.waitForRegistration(ctx); err != nil {
		conn.Close()
		return err
	}

	w.mu.Lock()
	w.wsState = WebSocketStateConnected
	w.mu.Unlock()

	return nil
}

// sendRegister sends the worker registration message
func (w *UniversalWorker) sendRegister() error {
	msg := &livekit.RegisterWorkerRequest{
		Type:      workerTypeToProto(w.opts.JobType),
		AgentName: w.opts.AgentName,
		Version:   w.opts.Version,
	}

	// Only set namespace if it's not empty
	if w.opts.Namespace != "" {
		msg.Namespace = &w.opts.Namespace
	}

	w.logger.Info("[DEBUG] Sending registration",
		"jobType", w.opts.JobType,
		"agentName", w.opts.AgentName,
		"version", w.opts.Version,
		"namespace", w.opts.Namespace)

	return w.sendMessage(&livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Register{
			Register: msg,
		},
	})
}

// waitForRegistration waits for the registration response
func (w *UniversalWorker) waitForRegistration(ctx context.Context) error {
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return ErrRegistrationTimeout
		default:
			var msg livekit.ServerMessage
			if err := w.readMessage(&msg); err != nil {
				return fmt.Errorf("failed to read registration response: %w", err)
			}

			if reg, ok := msg.Message.(*livekit.ServerMessage_Register); ok && reg.Register != nil {
				// Check if registration was successful
				if reg.Register.WorkerId == "" {
					return fmt.Errorf("registration failed: no worker ID assigned")
				}
				w.mu.Lock()
				w.workerID = reg.Register.WorkerId
				w.savedState.WorkerID = reg.Register.WorkerId
				w.mu.Unlock()

				w.logger.Info("Worker registered",
					"workerID", reg.Register.WorkerId,
				)
				return nil
			}
		}
	}
}

// sendMessage sends a message to the server
func (w *UniversalWorker) sendMessage(msg *livekit.WorkerMessage) error {
	w.mu.Lock()
	conn := w.conn
	state := w.wsState
	w.mu.Unlock()

	// Allow sending messages when connecting (for registration) or connected
	if conn == nil || (state != WebSocketStateConnecting && state != WebSocketStateConnected) {
		return ErrNotConnected
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Protect WebSocket write with mutex to prevent concurrent writes
	w.mu.Lock()
	err = conn.WriteMessage(websocket.BinaryMessage, data)
	w.mu.Unlock()

	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

// readMessage reads a message from the server
func (w *UniversalWorker) readMessage(msg *livekit.ServerMessage) error {
	w.mu.RLock()
	conn := w.conn
	w.mu.RUnlock()

	if conn == nil {
		return ErrNotConnected
	}

	msgType, data, err := conn.ReadMessage()
	if err != nil {
		return err
	}

	w.logger.Info("[DEBUG] Read raw message from WebSocket",
		"messageType", msgType,
		"dataLength", len(data))

	if err := proto.Unmarshal(data, msg); err != nil {
		// Try JSON unmarshaling as fallback
		if jsonErr := protojson.Unmarshal(data, msg); jsonErr != nil {
			return fmt.Errorf("failed to unmarshal message: %w", err)
		}
	}

	return nil
}

// handleMessages processes incoming messages from the server
func (w *UniversalWorker) handleMessages(ctx context.Context) {
	w.logger.Info("[DEBUG] Starting message handler loop")
	messageCount := 0

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("[DEBUG] Message handler loop ending - context done")
			return
		case <-w.stopCh:
			w.logger.Info("[DEBUG] Message handler loop ending - stop signal")
			return
		default:
			var msg livekit.ServerMessage
			if err := w.readMessage(&msg); err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					w.logger.Info("WebSocket closed normally")
					return
				}
				w.logger.Error("Failed to read message", "error", err)
				w.handleConnectionError(err)
				return
			}

			messageCount++
			w.logger.Info("[DEBUG] Received message from server",
				"messageNumber", messageCount,
				"messageType", fmt.Sprintf("%T", msg.Message))

			if err := w.handleServerMessage(&msg); err != nil {
				w.logger.Error("Failed to handle message", "error", err)
			}
		}
	}
}

// handleServerMessage handles a message from the server
func (w *UniversalWorker) handleServerMessage(msg *livekit.ServerMessage) error {
	switch m := msg.Message.(type) {
	case *livekit.ServerMessage_Register:
		// Already handled during registration
		return nil

	case *livekit.ServerMessage_Availability:
		// Handle availability request
		return w.handleAvailabilityRequest(m.Availability)

	case *livekit.ServerMessage_Assignment:
		// Handle job assignment
		go w.handleJobAssignment(m.Assignment)
		return nil

	case *livekit.ServerMessage_Termination:
		// Handle job termination
		return w.handleJobTermination(m.Termination)

	case *livekit.ServerMessage_Pong:
		// Pong received, update health status
		w.mu.Lock()
		w.healthCheck.lastPong = time.Now()
		w.healthCheck.missedPings = 0
		w.healthCheck.isHealthy = true
		w.mu.Unlock()
		return nil

	default:
		if w.opts.StrictProtocolMode {
			return fmt.Errorf("unknown message type: %T", m)
		}
		// Ignore unknown messages in non-strict mode
		return nil
	}
}

// handleAvailabilityRequest handles an availability check from the server
func (w *UniversalWorker) handleAvailabilityRequest(req *livekit.AvailabilityRequest) error {
	// Log detailed availability request information
	w.logger.Info("[DEBUG] Received availability request",
		"jobId", req.Job.Id,
		"jobType", req.Job.Type.String(),
		"room", req.Job.Room.Name,
		"currentLoad", w.GetCurrentLoad(),
		"activeJobs", len(w.activeJobs),
		"workerStatus", w.status,
	)

	// Check if we can accept the job based on current state
	w.mu.RLock()
	canAcceptBasedOnLoad := w.status == WorkerStatusAvailable
	activeJobCount := len(w.activeJobs)
	maxJobs := w.opts.MaxJobs
	w.mu.RUnlock()

	// Ask handler if it wants to accept the job
	accept, metadata := w.handler.OnJobRequest(context.Background(), req.Job)

	// Override handler decision if we're at capacity
	if accept && !canAcceptBasedOnLoad {
		w.logger.Info("[DEBUG] Handler accepted job but worker is not available",
			"jobId", req.Job.Id,
			"workerStatus", w.status,
			"activeJobs", activeJobCount,
			"maxJobs", maxJobs,
		)
		accept = false
	}

	// Build response
	resp := &livekit.AvailabilityResponse{
		JobId:     req.Job.Id,
		Available: accept,
	}

	// Add metadata fields if metadata is provided
	if metadata != nil {
		resp.SupportsResume = metadata.SupportsResume
		resp.ParticipantIdentity = metadata.ParticipantIdentity
		resp.ParticipantName = metadata.ParticipantName
		resp.ParticipantMetadata = metadata.ParticipantMetadata
	}

	// Log response details
	w.logger.Info("[DEBUG] Sending availability response",
		"jobId", req.Job.Id,
		"available", accept,
		"reason", func() string {
			if !accept {
				if !canAcceptBasedOnLoad {
					return "worker not available"
				}
				return "handler rejected"
			}
			return "accepted"
		}(),
	)

	return w.sendMessage(&livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: resp,
		},
	})
}

// handleJobTermination handles a job termination request
func (w *UniversalWorker) handleJobTermination(term *livekit.JobTermination) error {
	// Use robust cleanup method
	w.cleanupJob(term.JobId)

	// Notify handler
	w.handler.OnJobTerminated(context.Background(), term.JobId)

	// Update load
	w.updateLoad()

	return nil
}

// sendPong sends a pong response
func (w *UniversalWorker) sendPong(timestamp int64) error {
	// In LiveKit protocol, the worker doesn't send pongs
	// The server sends pongs in response to worker pings
	return nil
}

// sendJobAccept sends a job acceptance message
func (w *UniversalWorker) sendJobAccept(jobID string, accept *JobAcceptInfo) error {
	// Convert JobAcceptInfo to the expected livekit types
	// For now, we'll send an availability response instead
	return w.sendMessage(&livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: &livekit.AvailabilityResponse{
				JobId:               jobID,
				Available:           true,
				ParticipantIdentity: accept.Identity,
				ParticipantName:     accept.Name,
				ParticipantMetadata: accept.Metadata,
			},
		},
	})
}

// maintainConnection maintains the WebSocket connection with reconnection logic
func (w *UniversalWorker) maintainConnection(ctx context.Context) {
	pingTicker := time.NewTicker(w.opts.PingInterval)
	defer pingTicker.Stop()

	var statusRefreshTicker *time.Ticker
	if w.opts.StatusRefreshInterval > 0 {
		statusRefreshTicker = time.NewTicker(w.opts.StatusRefreshInterval)
		defer statusRefreshTicker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-pingTicker.C:
			// Send ping
			if err := w.sendPing(); err != nil {
				w.logger.Error("Failed to send ping", "error", err)
				w.handleConnectionError(err)
			}
		case <-w.reconnectChan:
			// Reconnection requested
			if err := w.reconnect(ctx); err != nil {
				w.logger.Error("Failed to reconnect", "error", err)
				// Retry after delay
				time.Sleep(5 * time.Second)
				select {
				case w.reconnectChan <- struct{}{}:
				default:
				}
			}
		}

		// Handle status refresh ticker separately to avoid blocking
		if statusRefreshTicker != nil {
			select {
			case <-statusRefreshTicker.C:
				// Check overall connection health before refreshing status
				if !w.isConnectionHealthy() {
					w.logger.Error("Connection unhealthy, triggering reconnection")
					w.handleConnectionError(fmt.Errorf("connection health check failed"))
					return
				}
				// Refresh worker status to prevent server timeout
				w.refreshWorkerStatus()
			default:
			}
		}
	}
}

// sendPing sends a ping message
func (w *UniversalWorker) sendPing() error {
	// Update last ping time
	w.mu.Lock()
	w.healthCheck.lastPing = time.Now()

	// Check if we've missed too many pongs
	if !w.healthCheck.lastPong.IsZero() &&
		time.Since(w.healthCheck.lastPong) > w.opts.PingTimeout {
		w.healthCheck.missedPings++
		if w.healthCheck.missedPings > 3 {
			w.healthCheck.isHealthy = false
			w.mu.Unlock()
			// Connection is unhealthy, trigger reconnect
			w.handleConnectionError(fmt.Errorf("ping timeout: missed %d pings", w.healthCheck.missedPings))
			return nil
		}
	}
	w.mu.Unlock()

	return w.sendMessage(&livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Ping{
			Ping: &livekit.WorkerPing{
				Timestamp: time.Now().Unix(),
			},
		},
	})
}

// handleConnectionError handles connection errors
func (w *UniversalWorker) handleConnectionError(err error) {
	w.mu.Lock()
	w.wsState = WebSocketStateDisconnected
	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
	w.mu.Unlock()

	// Trigger reconnection
	select {
	case w.reconnectChan <- struct{}{}:
	default:
		// Already queued
	}
}

// reconnect attempts to reconnect to the server
func (w *UniversalWorker) reconnect(ctx context.Context) error {
	w.logger.Info("Attempting to reconnect")

	// Close existing connection
	w.mu.Lock()
	if w.conn != nil {
		w.conn.Close()
		w.conn = nil
	}
	w.wsState = WebSocketStateReconnecting
	w.mu.Unlock()

	// Attempt to connect
	if err := w.connect(ctx); err != nil {
		return err
	}

	// Recover jobs if enabled
	// TODO: Recovery manager needs to be updated for UniversalWorker
	// if w.recoveryManager != nil {
	//     if err := w.recoveryManager.RecoverJobs(ctx); err != nil {
	//         w.logger.Error("Failed to recover jobs", "error", err)
	//     }
	// }

	// Process any queued status updates
	if w.raceProtector != nil {
		w.raceProtector.ProcessQueuedUpdates(func(jobID string, status livekit.JobStatus, error string) {
			w.updateJobStatus(jobID, status, error)
		})
	}

	w.logger.Info("Successfully reconnected")
	return nil
}

// handleStatusUpdateRetries processes queued status updates
func (w *UniversalWorker) handleStatusUpdateRetries(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-w.statusQueueChan:
			w.processStatusQueue()
		case <-ticker.C:
			// Periodic check
			w.processStatusQueue()
		}
	}
}

// processStatusQueue processes queued status updates
func (w *UniversalWorker) processStatusQueue() {
	w.mu.Lock()
	queue := w.statusQueue
	w.statusQueue = nil
	w.mu.Unlock()

	for _, update := range queue {
		if update.retryCount >= 3 {
			w.logger.Error("Max retries exceeded for job status update",
				"jobID", update.jobID,
				"status", update.status,
				"retries", update.retryCount,
			)
			continue
		}

		// Retry the update
		if err := w.sendMessage(&livekit.WorkerMessage{
			Message: &livekit.WorkerMessage_UpdateJob{
				UpdateJob: &livekit.UpdateJobStatus{
					JobId:  update.jobID,
					Status: update.status,
					Error:  update.error,
				},
			},
		}); err != nil {
			// Re-queue with incremented retry count
			update.retryCount++
			w.queueStatusUpdate(update)
		}
	}
}

// refreshWorkerStatus periodically refreshes the worker status to prevent server timeout
func (w *UniversalWorker) refreshWorkerStatus() {
	if w.wsState != WebSocketStateConnected {
		return
	}

	// Verify worker state consistency
	w.verifyWorkerState()

	// Force updateLoad to recalculate and send status
	w.updateLoad()

	w.logger.Info("[DEBUG] Refreshed worker status",
		"workerID", w.workerID,
		"status", w.status,
	)
}

// generateAuthToken creates a new JWT token with expiry
func (w *UniversalWorker) generateAuthToken() (string, time.Time, error) {
	at := auth.NewAccessToken(w.apiKey, w.apiSecret)
	grant := &auth.VideoGrant{
		Agent: true,
	}
	at.SetVideoGrant(grant)

	// Set token to expire in 24 hours
	expiresAt := time.Now().Add(24 * time.Hour)
	at.SetValidFor(24 * time.Hour)

	token, err := at.ToJWT()
	if err != nil {
		return "", time.Time{}, err
	}

	return token, expiresAt, nil
}

// startTokenRenewalTimer starts a timer to renew the token before it expires
func (w *UniversalWorker) startTokenRenewalTimer() {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Stop existing timer if any
	if w.tokenManagement.renewalTimer != nil {
		w.tokenManagement.renewalTimer.Stop()
	}

	// Calculate renewal time (renew 1 hour before expiry, minimum 30 minutes)
	renewalDuration := time.Until(w.tokenManagement.expiresAt) - time.Hour
	if renewalDuration < 30*time.Minute {
		renewalDuration = 30 * time.Minute
	}

	w.tokenManagement.renewalTimer = time.AfterFunc(renewalDuration, func() {
		if err := w.renewToken(); err != nil {
			w.logger.Error("Failed to renew authentication token", "error", err)
			// Trigger reconnection to get a fresh token
			select {
			case w.reconnectChan <- struct{}{}:
			default:
			}
		}
	})

	w.logger.Info("[DEBUG] Token renewal timer started",
		"expiresAt", w.tokenManagement.expiresAt,
		"renewIn", renewalDuration,
	)
}

// renewToken renews the authentication token
func (w *UniversalWorker) renewToken() error {
	w.logger.Info("[DEBUG] Renewing authentication token")

	// Generate new token
	token, expiresAt, err := w.generateAuthToken()
	if err != nil {
		return fmt.Errorf("failed to generate new token: %w", err)
	}

	// Store new token information
	w.mu.Lock()
	w.tokenManagement.currentToken = token
	w.tokenManagement.expiresAt = expiresAt
	w.mu.Unlock()

	// Start new renewal timer
	w.startTokenRenewalTimer()

	w.logger.Info("[DEBUG] Authentication token renewed successfully", "expiresAt", expiresAt)

	// Note: For a complete implementation, we would need to re-establish the WebSocket connection
	// with the new token. For now, we rely on the reconnection mechanism.
	return nil
}

// isConnectionHealthy checks if the connection is healthy based on various metrics
func (w *UniversalWorker) isConnectionHealthy() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	// Check WebSocket state
	if w.wsState != WebSocketStateConnected {
		w.logger.Info("[DEBUG] Connection unhealthy: not connected", "wsState", w.wsState)
		return false
	}

	// Check if we're missing too many pings
	if w.healthCheck.missedPings > 5 {
		w.logger.Info("[DEBUG] Connection unhealthy: too many missed pings", "missedPings", w.healthCheck.missedPings)
		return false
	}

	// Check if last pong is too old
	if !w.healthCheck.lastPong.IsZero() && time.Since(w.healthCheck.lastPong) > 2*w.opts.PingInterval {
		w.logger.Info("[DEBUG] Connection unhealthy: stale pong",
			"lastPong", w.healthCheck.lastPong,
			"timeSince", time.Since(w.healthCheck.lastPong),
		)
		return false
	}

	// Check token expiry
	if !w.tokenManagement.expiresAt.IsZero() && time.Until(w.tokenManagement.expiresAt) < 5*time.Minute {
		w.logger.Info("[DEBUG] Connection unhealthy: token near expiry",
			"expiresAt", w.tokenManagement.expiresAt,
			"timeLeft", time.Until(w.tokenManagement.expiresAt),
		)
		return false
	}

	return w.healthCheck.isHealthy
}
