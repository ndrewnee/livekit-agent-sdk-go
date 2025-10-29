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
	// Generate authentication token
	at := auth.NewAccessToken(w.apiKey, w.apiSecret)
	grant := &auth.VideoGrant{
		Agent: true,
	}
	at.SetVideoGrant(grant)
	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to generate token: %w", err)
	}

	// Connect to WebSocket
	wsURL := buildWebSocketURL(w.serverURL, w.opts.WebSocketPath)
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
		// Route unknown/custom messages to message handler for extensibility
		if w.messageHandler != nil {
			_ = w.messageHandler.HandleMessage(&ServerMessage{Message: msg.Message})
		}
		return nil
	}
}

// handleAvailabilityRequest handles an availability check from the server
func (w *UniversalWorker) handleAvailabilityRequest(req *livekit.AvailabilityRequest) error {
	// Log the availability request
	if w.logger != nil {
		w.logger.Info("[DEBUG] Received availability request", "jobId", req.Job.Id)
	}

	// Ask handler if it wants to accept the job
	accept, metadata := w.handler.OnJobRequest(context.Background(), req.Job)

	// Build response
	resp := &livekit.AvailabilityResponse{
		JobId:     req.Job.Id,
		Available: accept,
	}

	// Add metadata fields; generate sane defaults if not provided to avoid empty identity tokens
	if metadata != nil {
		resp.SupportsResume = metadata.SupportsResume
		resp.ParticipantIdentity = metadata.ParticipantIdentity
		resp.ParticipantName = metadata.ParticipantName
		resp.ParticipantMetadata = metadata.ParticipantMetadata
		resp.ParticipantAttributes = metadata.ParticipantAttributes
	} else {
		// Default non-empty identity so assignment token is valid
		resp.ParticipantIdentity = fmt.Sprintf("agent-%s", req.Job.Id)
		if w.opts.AgentName != "" {
			resp.ParticipantName = w.opts.AgentName
		} else {
			resp.ParticipantName = "Agent"
		}
		resp.SupportsResume = false
	}

	return w.sendMessage(&livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Availability{
			Availability: resp,
		},
	})
}

// handleJobTermination handles a job termination request
func (w *UniversalWorker) handleJobTermination(term *livekit.JobTermination) error {
	w.mu.Lock()
	jobCtx, exists := w.activeJobs[term.JobId]
	if exists {
		delete(w.activeJobs, term.JobId)
		delete(w.jobStartTimes, term.JobId)
		if jobCtx.Room != nil {
			delete(w.rooms, jobCtx.Room.Name())
			delete(w.participantTrackers, jobCtx.Room.Name())
		}
	}
	w.mu.Unlock()

	if exists && jobCtx.Cancel != nil {
		jobCtx.Cancel()
	}

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
	// No explicit accept message is required by the protocol after assignment.
	// Availability was already confirmed via AvailabilityResponse.
	// Keep this as a no-op to avoid sending misleading messages.
	_ = jobID
	_ = accept
	return nil
}

// maintainConnection maintains the WebSocket connection with reconnection logic
func (w *UniversalWorker) maintainConnection(ctx context.Context) {
	pingTicker := time.NewTicker(w.opts.PingInterval)
	defer pingTicker.Stop()

	// Exponential backoff settings
	var attempts int
	const baseDelay = 500 * time.Millisecond
	const maxDelay = 30 * time.Second

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
			// Reconnection requested with backoff
			if err := w.reconnect(ctx); err != nil {
				attempts++
				// Compute backoff with jitter
				delay := baseDelay << (attempts - 1)
				if delay > maxDelay {
					delay = maxDelay
				}
				// add simple jitter +/- 20%
				jitter := time.Duration(float64(delay) * (0.8 + 0.4*(float64(time.Now().UnixNano()%100)/100)))
				w.logger.Error("Failed to reconnect", "error", err, "attempt", attempts, "backoff", jitter)
				time.Sleep(jitter)
				select {
				case w.reconnectChan <- struct{}{}:
				default:
				}
			} else {
				// Success: reset attempts
				attempts = 0
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

	// Restart message handler goroutine (critical for receiving jobs)
	go w.handleMessages(ctx)

	// Send initial load update to announce availability
	w.updateLoad()

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
