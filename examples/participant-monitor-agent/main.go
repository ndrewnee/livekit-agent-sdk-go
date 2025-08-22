package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// ParticipantMonitorHandler monitors participants and their activities
type ParticipantMonitorHandler struct {
	agent *agent.ParticipantAgent
}

func (h *ParticipantMonitorHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	log.Printf("📋 Job assigned - Room: %s, Job ID: %s", job.Room.Name, job.Id)

	if job.Participant != nil {
		log.Printf("🎯 Monitoring specific participant: %s", job.Participant.Identity)
	} else {
		log.Printf("👥 Monitoring all participants in room")
	}

	// Set up permission policies
	permManager := h.agent.GetPermissionManager()

	// Add role-based policy
	rolePolicy := agent.NewRoleBasedPolicy()
	rolePolicy.SetRole("presenter", &livekit.ParticipantPermission{
		CanSubscribe:      true,
		CanPublish:        true,
		CanPublishData:    true,
		CanUpdateMetadata: true,
	})
	rolePolicy.SetRole("viewer", &livekit.ParticipantPermission{
		CanSubscribe:   true,
		CanPublish:     false,
		CanPublishData: true,
	})
	permManager.AddPolicy(rolePolicy)

	// Set up coordination rules
	coordinator := h.agent.GetCoordinationManager()

	// Create groups
	coordinator.CreateGroup("presenters", "Participants who can present")
	coordinator.CreateGroup("active_speakers", "Currently speaking participants")

	// Add coordination rule for speaking detection
	coordinator.AddCoordinationRule(agent.CoordinationRule{
		ID:          "track-speakers",
		Description: "Track who is speaking",
		Condition: func(activity agent.ParticipantActivity) bool {
			return activity.Type == agent.ActivityTypeSpeaking
		},
		Action: func(activity agent.ParticipantActivity) {
			if activity.Participant != nil {
				coordinator.AddParticipantToGroup(activity.Participant.Identity(), "active_speakers")
				log.Printf("🎤 %s started speaking", activity.Participant.Identity())
			}
		},
	})

	// Set up event processing
	processor := h.agent.GetEventProcessor()

	// Add custom event handler for important events
	processor.RegisterHandler(agent.EventTypeTrackPublished, func(event agent.ParticipantEvent) error {
		if event.Track != nil {
			log.Printf("📹 %s published %s track: %s",
				event.Participant.Identity(),
				event.Track.Kind(),
				event.Track.SID())
		}
		return nil
	})

	// Add batch processor for analytics
	processor.RegisterBatchProcessor(agent.BatchEventProcessor{
		ProcessBatch: func(events []agent.ParticipantEvent) error {
			log.Printf("📊 Processing batch of %d events", len(events))

			// Count event types
			eventCounts := make(map[agent.EventType]int)
			for _, event := range events {
				eventCounts[event.Type]++
			}

			for eventType, count := range eventCounts {
				log.Printf("   - %s: %d", eventType, count)
			}

			return nil
		},
		BatchSize:     10,
		BatchInterval: 5 * time.Second,
	})

	// Start periodic status reporting
	go h.reportStatus(ctx, room)

	return nil
}

func (h *ParticipantMonitorHandler) OnJobTerminated(ctx context.Context, termination livekit.JobTermination) {
	log.Printf("🛑 Job terminated - ID: %s, Reason: %s", termination.JobId, termination.Reason)
}

func (h *ParticipantMonitorHandler) GetJobMetadata(job *livekit.Job) *agent.JobMetadata {
	return &agent.JobMetadata{
		RequiresRoom: true,
		Priority:     1,
		Capabilities: []string{"participant-monitoring", "permission-management"},
	}
}

func (h *ParticipantMonitorHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	log.Printf("✅ Participant joined: %s (%s) - Metadata: %s",
		participant.Identity(),
		participant.Name(),
		participant.Metadata())

	// Check if they should be a presenter based on metadata
	if participant.Metadata() == "presenter" {
		coordinator := h.agent.GetCoordinationManager()
		coordinator.AddParticipantToGroup(participant.Identity(), "presenters")

		// Update their permissions
		permManager := h.agent.GetPermissionManager()
		if rolePolicy, ok := permManager.GetPolicy("role-based").(*agent.RoleBasedPolicy); ok {
			rolePolicy.AssignRole(participant.Identity(), "presenter")
		}
	}
}

func (h *ParticipantMonitorHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	log.Printf("❌ Participant left: %s", participant.Identity())
}

func (h *ParticipantMonitorHandler) OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
	log.Printf("📝 Metadata changed for %s: %s -> %s",
		participant.Identity(),
		oldMetadata,
		participant.Metadata())
}

func (h *ParticipantMonitorHandler) OnParticipantNameChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldName string) {
	log.Printf("✏️ Name changed for %s: %s -> %s",
		participant.Identity(),
		oldName,
		participant.Name())
}

func (h *ParticipantMonitorHandler) OnParticipantPermissionsChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldPermissions *livekit.ParticipantPermission) {
	newPerms := participant.Permissions()
	log.Printf("🔐 Permissions changed for %s:", participant.Identity())
	log.Printf("   - Can Publish: %v -> %v", oldPermissions.CanPublish, newPerms.CanPublish)
	log.Printf("   - Can Subscribe: %v -> %v", oldPermissions.CanSubscribe, newPerms.CanSubscribe)
}

func (h *ParticipantMonitorHandler) OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool) {
	if speaking {
		log.Printf("🎤 %s started speaking", participant.Identity())
	} else {
		log.Printf("🔇 %s stopped speaking", participant.Identity())
	}
}

func (h *ParticipantMonitorHandler) OnParticipantTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	log.Printf("📡 %s published %s track (SID: %s, Source: %s)",
		participant.Identity(),
		publication.Kind(),
		publication.SID(),
		publication.Source())

	// If it's a screen share, add to presenters
	if publication.Source() == livekit.TrackSource_SCREEN_SHARE {
		coordinator := h.agent.GetCoordinationManager()
		coordinator.AddParticipantToGroup(participant.Identity(), "presenters")
	}
}

func (h *ParticipantMonitorHandler) OnParticipantTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	log.Printf("📴 %s unpublished %s track (SID: %s)",
		participant.Identity(),
		publication.Kind(),
		publication.SID())
}

func (h *ParticipantMonitorHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	log.Printf("💬 Data from %s (%s): %s",
		participant.Identity(),
		kind.String(),
		string(data))

	// Echo back to confirm receipt
	if err := h.agent.SendDataToParticipant(participant.Identity(),
		[]byte(fmt.Sprintf("Received: %s", string(data))),
		kind == livekit.DataPacket_RELIABLE); err != nil {
		log.Printf("Failed to send response: %v", err)
	}
}

func (h *ParticipantMonitorHandler) reportStatus(ctx context.Context, room *lksdk.Room) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.printStatus()
		}
	}
}

func (h *ParticipantMonitorHandler) printStatus() {
	log.Println("\n📊 === STATUS REPORT ===")

	// Participant summary
	participants := h.agent.GetAllParticipants()
	log.Printf("Total participants: %d", len(participants))

	for identity, info := range participants {
		log.Printf("  👤 %s:", identity)
		log.Printf("     - Joined: %s ago", time.Since(info.JoinedAt).Round(time.Second))
		log.Printf("     - Tracks: %d", info.TrackCount)
		log.Printf("     - Speaking: %v", info.IsSpeaking)
		log.Printf("     - Can Publish: %v", info.Permissions.CanPublish)
	}

	// Group summary
	coordinator := h.agent.GetCoordinationManager()
	log.Println("\n👥 Groups:")
	for _, group := range []string{"presenters", "active_speakers"} {
		members := coordinator.GetGroupMembers(group)
		log.Printf("  - %s: %d members", group, len(members))
		for _, member := range members {
			log.Printf("    • %s", member)
		}
	}

	// Event metrics
	metrics := h.agent.GetEventProcessor().GetMetrics()
	log.Println("\n📈 Event Metrics:")
	log.Printf("  - Total Events: %d", metrics.TotalEvents)
	log.Printf("  - Processed: %d", metrics.ProcessedEvents)
	log.Printf("  - Dropped: %d", metrics.DroppedEvents)

	// Activity metrics
	activityMetrics := coordinator.GetActivityMetrics()
	log.Println("\n🎯 Activity Metrics:")
	log.Printf("  - Total Activities: %d", activityMetrics.TotalActivities)
	log.Printf("  - Active Participants: %d", activityMetrics.ActiveParticipants)

	log.Println("======================\n")
}

func main() {
	// Parse command line flags
	serverURL := flag.String("url", "http://localhost:7880", "LiveKit server URL")
	apiKey := flag.String("api-key", "devkey", "LiveKit API key")
	apiSecret := flag.String("api-secret", "secret", "LiveKit API secret")
	agentName := flag.String("agent-name", "participant-monitor", "Agent name")
	flag.Parse()

	log.Printf("🚀 Starting Participant Monitor Agent")
	log.Printf("   Server: %s", *serverURL)
	log.Printf("   Agent: %s", *agentName)

	// Create handler
	handler := &ParticipantMonitorHandler{}

	// Create participant agent
	agentInstance, err := agent.NewParticipantAgent(*serverURL, *apiKey, *apiSecret, handler, agent.WorkerOptions{
		Name:      *agentName,
		Namespace: "monitoring",
		Metadata: map[string]string{
			"version": "1.0",
			"purpose": "participant-monitoring",
		},
	})
	if err != nil {
		log.Fatal("Failed to create agent:", err)
	}

	// Store agent reference for handler
	handler.agent = agentInstance

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start agent in background
	go func() {
		log.Println("✅ Agent started and waiting for jobs...")
		if err := agentInstance.Start(); err != nil {
			log.Printf("Agent error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sig := <-sigChan
	log.Printf("\n🛑 Received signal %v, shutting down...", sig)

	// Graceful shutdown
	if err := agentInstance.Stop(); err != nil {
		log.Printf("Error stopping agent: %v", err)
	}

	log.Println("👋 Agent stopped")
}
