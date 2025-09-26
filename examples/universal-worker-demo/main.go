package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

var (
	url       = flag.String("url", os.Getenv("LIVEKIT_URL"), "LiveKit server URL")
	apiKey    = flag.String("api-key", os.Getenv("LIVEKIT_API_KEY"), "LiveKit API key")
	apiSecret = flag.String("api-secret", os.Getenv("LIVEKIT_API_SECRET"), "LiveKit API secret")
	agentName = flag.String("agent-name", "universal-demo", "Agent name")
	namespace = flag.String("namespace", "", "Agent namespace")
	maxJobs   = flag.Int("max-jobs", 5, "Maximum concurrent jobs")
)

// DemoHandler demonstrates all UniversalWorker capabilities
type DemoHandler struct {
	agent.BaseHandler
}

func (h *DemoHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	log.Printf("📥 Job request received: ID=%s Type=%s Room=%s", 
		job.Id, job.Type, job.Room.Name)
	
	// Accept all jobs and provide metadata
	metadata := &agent.JobMetadata{
		ParticipantIdentity: "universal-agent",
		ParticipantName:     "Universal Demo Agent",
		ParticipantMetadata: "Demo agent showcasing all capabilities",
		SupportsResume:      true,
	}
	
	return true, metadata
}

func (h *DemoHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	log.Printf("🚀 Job assigned: ID=%s Room=%s", 
		jobCtx.Job.Id, jobCtx.Job.Room.Name)
	
	// Demonstrate job type handling
	switch jobCtx.Job.Type {
	case livekit.JobType_JT_ROOM:
		log.Printf("   Type: ROOM job - Agent joins when room is created")
		h.handleRoomJob(ctx, jobCtx)
		
	case livekit.JobType_JT_PARTICIPANT:
		log.Printf("   Type: PARTICIPANT job - Agent handles specific participant")
		if jobCtx.Job.Participant != nil {
			log.Printf("   Target: %s", jobCtx.Job.Participant.Identity)
		}
		h.handleParticipantJob(ctx, jobCtx)
		
	case livekit.JobType_JT_PUBLISHER:
		log.Printf("   Type: PUBLISHER job - Agent handles media publishing")
		h.handlePublisherJob(ctx, jobCtx)
	}
	
	// Keep job running until context is cancelled
	<-ctx.Done()
	log.Printf("✅ Job completed: ID=%s", jobCtx.Job.Id)
	return nil
}

func (h *DemoHandler) handleRoomJob(ctx context.Context, jobCtx *agent.JobContext) {
	// Demonstrate room capabilities
	if jobCtx.Room != nil {
		log.Printf("   Room connected: %s", jobCtx.Room.Name())
		log.Printf("   Local participant: %s", jobCtx.Room.LocalParticipant.Identity())
		log.Printf("   Remote participants: %d", len(jobCtx.Room.GetRemoteParticipants()))
		
		// Send a data message to all participants
		err := jobCtx.Room.LocalParticipant.PublishData(
			[]byte("Hello from Universal Agent!"),
			lksdk.WithDataPublishReliable(true),
		)
		if err != nil {
			log.Printf("   ⚠️ Failed to send data: %v", err)
		} else {
			log.Printf("   📤 Sent greeting message to room")
		}
	}
}

func (h *DemoHandler) handleParticipantJob(ctx context.Context, jobCtx *agent.JobContext) {
	// Demonstrate participant-specific capabilities
	if jobCtx.TargetParticipant != nil {
		log.Printf("   Monitoring participant: %s", jobCtx.TargetParticipant.Identity())
		log.Printf("   Participant name: %s", jobCtx.TargetParticipant.Name())
		log.Printf("   Metadata: %s", jobCtx.TargetParticipant.Metadata())
	}
}

func (h *DemoHandler) handlePublisherJob(ctx context.Context, jobCtx *agent.JobContext) {
	// Demonstrate publisher capabilities
	log.Printf("   Ready to handle media publishing")
	// In a real implementation, you would publish audio/video tracks here
}

func (h *DemoHandler) OnJobTerminated(ctx context.Context, jobID string) {
	log.Printf("🛑 Job terminated: ID=%s", jobID)
}

// Participant events
func (h *DemoHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	log.Printf("👤 Participant joined: %s (%s)", participant.Identity(), participant.Name())
}

func (h *DemoHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	log.Printf("👤 Participant left: %s", participant.Identity())
}

func (h *DemoHandler) OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
	log.Printf("📝 Metadata changed for %s: %s -> %s", 
		participant.Identity(), oldMetadata, participant.Metadata())
}

// Track events
func (h *DemoHandler) OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	log.Printf("🎥 Track published by %s: %s (kind: %s)", 
		participant.Identity(), publication.SID(), publication.Kind())
}

func (h *DemoHandler) OnTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	log.Printf("📺 Subscribed to track from %s: %s", 
		participant.Identity(), publication.SID())
}

func (h *DemoHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	log.Printf("📨 Data received from %s: %s", participant.Identity(), string(data))
}

func main() {
	flag.Parse()
	
	// Validate configuration
	if *url == "" || *apiKey == "" || *apiSecret == "" {
		log.Fatal("LiveKit URL, API key, and API secret are required")
	}
	
	log.Printf("🚀 Starting Universal Worker Demo")
	log.Printf("   Server: %s", *url)
	log.Printf("   Agent: %s", *agentName)
	log.Printf("   Max jobs: %d", *maxJobs)
	
	// Create handler
	handler := &DemoHandler{}
	
	// Configure worker options
	opts := agent.WorkerOptions{
		AgentName:  *agentName,
		Namespace:  *namespace,
		MaxJobs:    *maxJobs,
		JobType:    livekit.JobType_JT_ROOM, // Accept all job types
		
		// Enable advanced features
		EnableJobQueue:           true,
		JobQueueSize:            10,
		EnableResourcePool:      false, // Set to true if you have resources to pool
		EnableJobRecovery:       true,
		EnableCPUMemoryLoad:     true,
		EnableResourceMonitoring: true,
		MemoryLimitMB:           512,
		
		// Permissions for the agent
		Permissions: &livekit.ParticipantPermission{
			CanSubscribe:   true,
			CanPublish:     true,
			CanPublishData: true,
		},
	}
	
	// Create UniversalWorker
	worker := agent.NewUniversalWorker(*url, *apiKey, *apiSecret, handler, opts)
	
	// Demonstrate additional capabilities
	log.Printf("📊 Worker capabilities:")
	log.Printf("   ✓ Job queueing enabled: %d queue size", opts.JobQueueSize)
	log.Printf("   ✓ CPU/Memory load tracking: %v", opts.EnableCPUMemoryLoad)
	log.Printf("   ✓ Job recovery: %v", opts.EnableJobRecovery)
	log.Printf("   ✓ Resource monitoring: %v", opts.EnableResourceMonitoring)
	
	// Check worker status
	if worker.IsConnected() {
		log.Printf("   ✓ Connected to server")
	} else {
		log.Printf("   ⚠️ Not yet connected")
	}
	
	// Get health metrics
	health := worker.Health()
	log.Printf("   Health: %+v", health)
	
	// Get metrics
	metrics := worker.GetMetrics()
	log.Printf("   Metrics: %+v", metrics)
	
	// Start worker
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	// Handle shutdown gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	
	go func() {
		<-sigChan
		log.Println("📴 Shutting down gracefully...")
		cancel()
	}()
	
	// Add shutdown hooks
	worker.AddShutdownHook(agent.ShutdownPhasePreStop, agent.ShutdownHook{
		Name:     "log-shutdown",
		Priority: 100,
		Handler: func(ctx context.Context) error {
			log.Println("🔄 Pre-stop hook: Preparing for shutdown...")
			return nil
		},
	})
	
	worker.AddShutdownHook(agent.ShutdownPhaseCleanup, agent.ShutdownHook{
		Name:     "final-cleanup",
		Priority: 50,
		Handler: func(ctx context.Context) error {
			log.Println("🧹 Cleanup hook: Final cleanup...")
			return nil
		},
	})
	
	// Run worker
	log.Println("✅ Worker started, waiting for jobs...")
	if err := worker.Start(ctx); err != nil {
		log.Fatalf("Worker failed: %v", err)
	}
	
	// Stop with timeout
	if err := worker.StopWithTimeout(5 * time.Second); err != nil {
		log.Printf("⚠️ Error during shutdown: %v", err)
	}
	
	log.Println("👋 Universal Worker Demo stopped")
}