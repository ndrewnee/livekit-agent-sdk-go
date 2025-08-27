// proper_agent.go - Complete LiveKit Agent using livekit-agent-sdk-go
// This demonstrates proper agent registration, job waiting, processing, and completion
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	agent "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/joho/godotenv"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// CloudAgentHandler handles agent jobs from LiveKit Cloud
type CloudAgentHandler struct {
	agent.BaseHandler // Embed base to get default implementations

	agentName string
	metrics   *AgentJobMetrics
	mu        sync.RWMutex
}

// AgentJobMetrics tracks agent performance
type AgentJobMetrics struct {
	JobsReceived  int
	JobsAccepted  int
	JobsRejected  int
	JobsCompleted int
	JobsFailed    int
	DataMessages  int
	StartTime     time.Time
}

// NewCloudAgentHandler creates a new agent handler
func NewCloudAgentHandler() *CloudAgentHandler {
	return &CloudAgentHandler{
		agentName: "livekit-cloud-agent",
		metrics: &AgentJobMetrics{
			StartTime: time.Now(),
		},
	}
}

// OnJobRequest is called when a job is offered to the agent
func (h *CloudAgentHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	h.mu.Lock()
	h.metrics.JobsReceived++
	h.mu.Unlock()

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🎯 JOB REQUEST RECEIVED")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Job ID: %s\n", job.Id)
	fmt.Printf("Job Type: %s\n", getJobType(job))

	if job.Room != nil {
		fmt.Printf("Room SID: %s\n", job.Room.Sid)
		fmt.Printf("Room Name: %s\n", job.Room.Name)
	}

	if job.Participant != nil {
		fmt.Printf("Target Participant: %s\n", job.Participant.Identity)
	}

	if job.Metadata != "" {
		fmt.Printf("Metadata: %s\n", job.Metadata)
	}

	// Accept the job
	h.mu.Lock()
	h.metrics.JobsAccepted++
	h.mu.Unlock()

	fmt.Println("✅ JOB ACCEPTED")
	fmt.Println(strings.Repeat("=", 60))

	// Return metadata about how we'll handle the job
	// Make sure to return non-nil metadata
	metadata := &agent.JobMetadata{
		ParticipantIdentity: fmt.Sprintf("%s-worker", h.agentName),
		ParticipantName:     h.agentName,
		ParticipantMetadata: `{"type": "agent", "version": "1.0.0"}`,
	}

	return true, metadata
}

// OnJobAssigned is called when the job is officially assigned
func (h *CloudAgentHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	fmt.Println("\n📋 JOB ASSIGNED - Starting Processing")
	fmt.Printf("Job ID: %s\n", jobCtx.Job.Id)
	fmt.Printf("Room: %s\n", jobCtx.Room.Name())
	fmt.Printf("Local Participant: %s\n", jobCtx.Room.LocalParticipant.Identity())

	// Send initial status
	h.sendStatusUpdate(jobCtx.Room, "started", jobCtx.Job.Id)

	// Perform agent work
	go h.performAgentWork(ctx, jobCtx)

	// Wait for context cancellation (job termination)
	<-ctx.Done()

	h.mu.Lock()
	h.metrics.JobsCompleted++
	h.mu.Unlock()

	fmt.Printf("\n✅ JOB COMPLETED: %s\n", jobCtx.Job.Id)
	return nil
}

// performAgentWork simulates agent processing
func (h *CloudAgentHandler) performAgentWork(ctx context.Context, jobCtx *agent.JobContext) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	updateCount := 0
	for {
		select {
		case <-ctx.Done():
			// Send final status before exiting
			h.sendStatusUpdate(jobCtx.Room, "completed", jobCtx.Job.Id)
			return

		case <-ticker.C:
			updateCount++

			// Send periodic update
			status := map[string]interface{}{
				"agent":     h.agentName,
				"job_id":    jobCtx.Job.Id,
				"update":    updateCount,
				"status":    "processing",
				"timestamp": time.Now().Unix(),
			}

			statusData, _ := json.Marshal(status)
			if err := jobCtx.Room.LocalParticipant.PublishData(
				statusData,
				lksdk.WithDataPublishReliable(true),
			); err == nil {
				fmt.Printf("   📤 Sent update #%d for job %s\n", updateCount, jobCtx.Job.Id)

				h.mu.Lock()
				h.metrics.DataMessages++
				h.mu.Unlock()
			}

			// Complete after 3 updates
			if updateCount >= 3 {
				fmt.Printf("\n🏁 Finishing job %s after %d updates\n", jobCtx.Job.Id, updateCount)

				// Send completion message
				completion := map[string]interface{}{
					"agent":     h.agentName,
					"job_id":    jobCtx.Job.Id,
					"status":    "completed",
					"updates":   updateCount,
					"result":    "success",
					"timestamp": time.Now().Unix(),
				}

				completionData, _ := json.Marshal(completion)
				jobCtx.Room.LocalParticipant.PublishData(
					completionData,
					lksdk.WithDataPublishReliable(true),
				)

				// Give time for the message to send
				time.Sleep(2 * time.Second)

				// Disconnect from room
				jobCtx.Room.Disconnect()
				return
			}
		}
	}
}

// Helper method to send status updates
func (h *CloudAgentHandler) sendStatusUpdate(room *lksdk.Room, status string, jobID string) {
	update := map[string]interface{}{
		"agent":     h.agentName,
		"job_id":    jobID,
		"status":    status,
		"timestamp": time.Now().Unix(),
	}

	data, _ := json.Marshal(update)
	room.LocalParticipant.PublishData(data, lksdk.WithDataPublishReliable(true))

	h.mu.Lock()
	h.metrics.DataMessages++
	h.mu.Unlock()
}

// Event handlers
func (h *CloudAgentHandler) OnJobTerminated(ctx context.Context, jobID string) {
	fmt.Printf("\n⚠️  JOB TERMINATED: %s\n", jobID)
}

func (h *CloudAgentHandler) OnRoomConnected(ctx context.Context, room *lksdk.Room) {
	fmt.Printf("🔗 Connected to room: %s\n", room.Name())
}

func (h *CloudAgentHandler) OnRoomDisconnected(ctx context.Context, room *lksdk.Room, reason string) {
	fmt.Printf("🔌 Disconnected from room: %s (reason: %s)\n", room.Name(), reason)
}

func (h *CloudAgentHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	fmt.Printf("👤 Participant joined: %s\n", participant.Identity())
}

func (h *CloudAgentHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	fmt.Printf("👤 Participant left: %s\n", participant.Identity())
}

func (h *CloudAgentHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	fmt.Printf("📨 Data received from %s: %s\n", participant.Identity(), string(data))

	// Parse and respond to commands
	var command map[string]interface{}
	if err := json.Unmarshal(data, &command); err == nil {
		if cmd, ok := command["command"].(string); ok {
			fmt.Printf("   ⚡ Command: %s\n", cmd)
		}
	}
}

// Print metrics
func (h *CloudAgentHandler) PrintMetrics() {
	h.mu.RLock()
	defer h.mu.RUnlock()

	elapsed := time.Since(h.metrics.StartTime)

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📊 AGENT METRICS")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("⏱️  Running time: %.1f seconds\n", elapsed.Seconds())
	fmt.Printf("📥 Jobs received: %d\n", h.metrics.JobsReceived)
	fmt.Printf("✅ Jobs accepted: %d\n", h.metrics.JobsAccepted)
	fmt.Printf("❌ Jobs rejected: %d\n", h.metrics.JobsRejected)
	fmt.Printf("🏁 Jobs completed: %d\n", h.metrics.JobsCompleted)
	fmt.Printf("💬 Data messages sent: %d\n", h.metrics.DataMessages)
	fmt.Println(strings.Repeat("=", 60))
}

// Helper function to get job type as string
func getJobType(job *livekit.Job) string {
	if job.Room != nil {
		return "ROOM"
	}
	if job.Participant != nil {
		return "PARTICIPANT"
	}
	return "UNKNOWN"
}

// RunProperAgent runs the complete agent with SDK
func RunProperAgent() error {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🤖 LIVEKIT CLOUD AGENT (Using SDK)")
	fmt.Println(strings.Repeat("=", 60) + "\n")

	// Initialize logger
	logger.InitFromConfig(&logger.Config{
		Level: "info",
		JSON:  false,
	}, "livekit-agent")

	// Load configuration
	if err := loadEnv(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	serverURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	fmt.Printf("📌 Configuration:\n")
	fmt.Printf("   Server: %s\n", serverURL)
	fmt.Printf("   API Key: %s...\n", apiKey[:min(8, len(apiKey))])
	fmt.Println()

	// Create handler
	handler := NewCloudAgentHandler()

	// Create worker options
	// Set the agent name to match what we dispatch in room creation
	opts := agent.WorkerOptions{
		AgentName: "livekit-cloud-agent",   // Must match the dispatch agent name
		JobType:   livekit.JobType_JT_ROOM, // Handle room jobs
		Version:   "1.0.0",
	}

	// Create the universal worker
	worker := agent.NewUniversalWorker(serverURL, apiKey, apiSecret, handler, opts)

	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start worker
	fmt.Println("🚀 Starting agent worker...")
	fmt.Println("   Agent will register with LiveKit Cloud")
	fmt.Println("   Waiting for jobs...")
	fmt.Println()

	// Run worker in goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := worker.Start(context.Background()); err != nil {
			errChan <- fmt.Errorf("worker error: %w", err)
		}
	}()

	// Wait for shutdown or error
	select {
	case <-sigChan:
		fmt.Println("\n🛑 Shutdown signal received")
		worker.Stop()
		handler.PrintMetrics()

	case err := <-errChan:
		return err
	}

	return nil
}

func loadEnv() error {
	envPaths := []string{".env", "../.env", "../../.env"}
	for _, path := range envPaths {
		if err := godotenv.Load(path); err == nil {
			fmt.Printf("📁 Loaded config from: %s\n", path)
			return nil
		}
	}
	if os.Getenv("LIVEKIT_URL") == "" {
		return fmt.Errorf("LIVEKIT_URL not set")
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ============================================================
// AUXILIARY: Room Creator with Agent Dispatch
// ============================================================

// CreateRoomWithAgent creates a room that dispatches to our agent
func CreateRoomWithAgent() error {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📋 CREATING ROOM WITH AGENT DISPATCH")
	fmt.Println(strings.Repeat("=", 60) + "\n")

	if err := loadEnv(); err != nil {
		return err
	}

	serverURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	// Create room client
	roomClient := lksdk.NewRoomServiceClient(serverURL, apiKey, apiSecret)

	roomName := fmt.Sprintf("agent-job-room-%d", time.Now().Unix())

	// Create room with agent dispatch
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:         roomName,
		EmptyTimeout: 60,
		Metadata:     `{"purpose": "agent-test", "requires_processing": true}`,
		// This tells LiveKit to dispatch an agent to this room
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "livekit-cloud-agent", // Must match agent name
				Metadata:  `{"task": "process", "priority": "high"}`,
			},
		},
	})

	if err != nil {
		return fmt.Errorf("failed to create room: %w", err)
	}

	fmt.Printf("✅ Room created successfully!\n")
	fmt.Printf("   Name: %s\n", room.Name)
	fmt.Printf("   SID: %s\n", room.Sid)
	fmt.Printf("   Agent: livekit-cloud-agent\n")
	fmt.Println("\n🎯 Agent should receive job for this room")
	fmt.Println(strings.Repeat("=", 60))

	return nil
}

// RunAgentDemo runs both the agent and creates test rooms
func RunAgentDemo() error {
	// First, create a room with agent dispatch
	if err := CreateRoomWithAgent(); err != nil {
		log.Printf("Warning: Failed to create initial room: %v", err)
	}

	// Give a moment for the room to be created
	time.Sleep(2 * time.Second)

	// Now run the agent
	return RunProperAgent()
}
