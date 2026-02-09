package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	agent "github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type publisherRejoinAgentHandler struct {
	agent.BaseHandler

	agentName string

	cancelOnTargetLeave bool

	mu              sync.RWMutex
	jobsSeen        int
	jobsAssigned    int
	jobsEnded       int
	activeJobID     string
	activeJobCancel context.CancelFunc
	targetIdentity  string
}

func (h *publisherRejoinAgentHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	_ = ctx

	if job == nil {
		fmt.Println("⚠️  JOB REQUEST RECEIVED: <nil>")
		return false, nil
	}

	h.mu.Lock()
	h.jobsSeen++
	h.mu.Unlock()

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🎯 JOB REQUEST RECEIVED")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Job ID: %s\n", job.Id)
	fmt.Printf("Job Type: %s\n", job.Type.String())
	fmt.Printf("Dispatch ID: %s\n", job.DispatchId)

	if job.Room != nil {
		fmt.Printf("Room: %s\n", job.Room.Name)
	}
	if job.Participant != nil {
		fmt.Printf("Participant: %s\n", job.Participant.Identity)
	}

	rejectReason := ""
	switch {
	case job.Type != livekit.JobType_JT_PUBLISHER:
		rejectReason = "unsupported job type"
	case job.Room == nil || job.Room.Name == "":
		rejectReason = "missing room information"
	case job.Participant == nil || job.Participant.Identity == "":
		rejectReason = "missing participant information"
	}

	if rejectReason != "" {
		fmt.Printf("❌ JOB REJECTED: %s\n", rejectReason)
		fmt.Println(strings.Repeat("=", 60))
		return false, nil
	}

	fmt.Println("✅ JOB ACCEPTED (JT_PUBLISHER)")
	fmt.Println(strings.Repeat("=", 60))

	return true, &agent.JobMetadata{
		ParticipantIdentity: fmt.Sprintf("%s-%s", h.agentName, job.Id),
		ParticipantName:     h.agentName,
		ParticipantMetadata: `{"type":"publisher-rejoin-agent"}`,
	}
}

func (h *publisherRejoinAgentHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	if jobCtx == nil || jobCtx.Job == nil || jobCtx.Room == nil {
		return fmt.Errorf("invalid job context")
	}

	h.mu.Lock()
	h.jobsAssigned++
	h.activeJobID = ""
	h.activeJobCancel = nil
	h.targetIdentity = ""
	if jobCtx != nil && jobCtx.Job != nil {
		h.activeJobID = jobCtx.Job.Id
		h.activeJobCancel = jobCtx.Cancel
		if jobCtx.Job.Participant != nil {
			h.targetIdentity = jobCtx.Job.Participant.Identity
		}
	}
	h.mu.Unlock()

	participant := ""
	if jobCtx.Job.Participant != nil {
		participant = jobCtx.Job.Participant.Identity
	}

	fmt.Printf("\n📋 JOB ASSIGNED: %s participant=%s room=%s\n", jobCtx.Job.Id, participant, jobCtx.Room.Name())

	<-ctx.Done()

	h.mu.Lock()
	h.jobsEnded++
	if h.activeJobID == jobCtx.Job.Id {
		h.activeJobID = ""
		h.activeJobCancel = nil
		h.targetIdentity = ""
	}
	h.mu.Unlock()

	fmt.Printf("🛑 JOB CONTEXT DONE: %s err=%v\n", jobCtx.Job.Id, ctx.Err())
	return nil
}

func (h *publisherRejoinAgentHandler) OnJobTerminated(ctx context.Context, jobID string) {
	_ = ctx
	fmt.Printf("⚠️  JOB TERMINATED: %s\n", jobID)
}

func (h *publisherRejoinAgentHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	_ = ctx
	if participant == nil {
		return
	}
	fmt.Printf("👤 Participant joined: %s\n", participant.Identity())
}

func (h *publisherRejoinAgentHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	_ = ctx
	if participant == nil {
		return
	}
	fmt.Printf("👤 Participant left: %s\n", participant.Identity())

	if !h.cancelOnTargetLeave {
		return
	}

	var cancel context.CancelFunc
	var jobID string
	h.mu.RLock()
	if participant.Identity() == h.targetIdentity && h.activeJobCancel != nil {
		cancel = h.activeJobCancel
		jobID = h.activeJobID
	}
	h.mu.RUnlock()

	if cancel != nil {
		fmt.Printf("🧨 Target participant left; ending job: %s target=%s\n", jobID, participant.Identity())
		cancel()
	}
}

func (h *publisherRejoinAgentHandler) OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	_ = ctx
	if participant == nil || publication == nil {
		return
	}
	fmt.Printf("📡 Track published: sid=%s kind=%s participant=%s\n", publication.SID(), publication.Kind(), participant.Identity())
}

func (h *publisherRejoinAgentHandler) PrintMetrics() {
	h.mu.RLock()
	defer h.mu.RUnlock()

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("📊 PUBLISHER REJOIN AGENT METRICS")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Jobs seen: %d\n", h.jobsSeen)
	fmt.Printf("Jobs assigned: %d\n", h.jobsAssigned)
	fmt.Printf("Jobs ended: %d\n", h.jobsEnded)
	fmt.Println(strings.Repeat("=", 60))
}

func RunPublisherRejoinAgent(args []string) error {
	fs := flag.NewFlagSet("publisher-agent", flag.ContinueOnError)
	agentName := fs.String("agent-name", envOrDefault("PUBLISHER_AGENT_NAME", "publisher-rejoin-agent"), "agent name used for dispatch")
	maxJobs := fs.Int("max-jobs", 10, "max concurrent jobs")
	cancelOnLeave := fs.Bool("cancel-on-target-leave", false, "end the job when the target participant leaves the room (to test redispatch on rejoin)")
	namespace := fs.String("namespace", os.Getenv("LIVEKIT_NAMESPACE"), "LiveKit namespace (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger.InitFromConfig(&logger.Config{
		Level: "info",
		JSON:  false,
	}, "publisher-rejoin-agent")

	if err := loadEnv(); err != nil {
		return err
	}

	serverURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("🤖 PUBLISHER REJOIN AGENT (JT_PUBLISHER)")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Server: %s\n", serverURL)
	fmt.Printf("AgentName: %s\n", *agentName)
	fmt.Printf("Namespace: %s\n", *namespace)
	fmt.Printf("MaxJobs: %d\n", *maxJobs)
	fmt.Println(strings.Repeat("=", 60))

	handler := &publisherRejoinAgentHandler{
		agentName:           *agentName,
		cancelOnTargetLeave: *cancelOnLeave,
	}

	opts := agent.WorkerOptions{
		AgentName:    *agentName,
		JobType:      livekit.JobType_JT_PUBLISHER,
		Version:      "e2e",
		MaxJobs:      *maxJobs,
		Namespace:    *namespace,
		PingInterval: 10 * time.Second,
		PingTimeout:  2 * time.Second,
	}

	worker := agent.NewUniversalWorker(serverURL, apiKey, apiSecret, handler, opts)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	errChan := make(chan error, 1)
	go func() {
		if err := worker.Start(context.Background()); err != nil {
			errChan <- err
		}
	}()

	select {
	case <-sigChan:
		fmt.Println("\n🛑 Shutdown signal received")
		_ = worker.Stop()
		handler.PrintMetrics()
		return nil
	case err := <-errChan:
		handler.PrintMetrics()
		return err
	}
}

func CreateRoomForPublisherAgent(args []string) error {
	fs := flag.NewFlagSet("create-room-publisher", flag.ContinueOnError)
	agentName := fs.String("agent-name", envOrDefault("PUBLISHER_AGENT_NAME", "publisher-rejoin-agent"), "agent name used for dispatch")
	emptyTimeout := fs.Uint("empty-timeout", 60, "empty timeout in seconds")
	dispatchMode := fs.String("dispatch-mode", "api", "dispatch mode: api|room_agents")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := loadEnv(); err != nil {
		return err
	}

	serverURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	roomClient := lksdk.NewRoomServiceClient(serverURL, apiKey, apiSecret)
	roomName := fmt.Sprintf("publisher-rejoin-%d", time.Now().Unix())

	createReq := &livekit.CreateRoomRequest{
		Name: roomName,
		// EmptyTimeout controls how long the room stays open if nobody joins.
		// DepartureTimeout controls how long it stays open after everyone leaves.
		EmptyTimeout:     60,
		DepartureTimeout: uint32(*emptyTimeout),
		Metadata:         `{"purpose":"publisher-rejoin-test"}`,
	}

	switch strings.ToLower(strings.TrimSpace(*dispatchMode)) {
	case "room_agents":
		// Older mechanism: embed agent dispatch config into room creation.
		createReq.Agents = []*livekit.RoomAgentDispatch{
			{
				AgentName: *agentName,
				Metadata:  `{"purpose":"publisher-rejoin-test"}`,
			},
		}
	case "api":
		// Recommended mechanism: create room, then create an agent dispatch via API.
	default:
		return fmt.Errorf("invalid --dispatch-mode %q (expected api|room_agents)", *dispatchMode)
	}

	room, err := roomClient.CreateRoom(context.Background(), createReq)
	if err != nil {
		return err
	}

	fmt.Printf("✅ Room created successfully!\n")
	fmt.Printf("   Name: %s\n", room.Name)
	fmt.Printf("   SID: %s\n", room.Sid)
	fmt.Printf("   Agent: %s\n", *agentName)

	if strings.ToLower(strings.TrimSpace(*dispatchMode)) == "api" {
		dispatchClient := lksdk.NewAgentDispatchServiceClient(serverURL, apiKey, apiSecret)
		dispatch, err := dispatchClient.CreateDispatch(context.Background(), &livekit.CreateAgentDispatchRequest{
			AgentName: *agentName,
			Room:      roomName,
			Metadata:  `{"purpose":"publisher-rejoin-test"}`,
		})
		if err != nil {
			return fmt.Errorf("failed to create agent dispatch: %w", err)
		}
		fmt.Printf("   Dispatch ID: %s\n", dispatch.Id)
	}

	return nil
}

func ListDispatch(args []string) error {
	fs := flag.NewFlagSet("list-dispatch", flag.ContinueOnError)
	roomName := fs.String("room", "", "room name to inspect")
	dispatchID := fs.String("dispatch-id", "", "optional dispatch id filter")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *roomName == "" {
		return fmt.Errorf("--room is required")
	}

	if err := loadEnv(); err != nil {
		return err
	}

	serverURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	client := lksdk.NewAgentDispatchServiceClient(serverURL, apiKey, apiSecret)
	resp, err := client.ListDispatch(context.Background(), &livekit.ListAgentDispatchRequest{
		DispatchId: *dispatchID,
		Room:       *roomName,
	})
	if err != nil {
		return err
	}

	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("📋 AGENT DISPATCHES for room=%s\n", *roomName)
	fmt.Println(strings.Repeat("=", 60))

	if resp == nil || len(resp.AgentDispatches) == 0 {
		fmt.Println("(none)")
		return nil
	}

	for _, d := range resp.AgentDispatches {
		if d == nil {
			continue
		}
		fmt.Printf("Dispatch: id=%s agent=%s room=%s\n", d.Id, d.AgentName, d.Room)
		if d.Metadata != "" {
			fmt.Printf("  metadata=%s\n", d.Metadata)
		}
		if d.State == nil {
			fmt.Println("  state=<nil>")
			continue
		}
		fmt.Printf("  created_at=%d deleted_at=%d jobs=%d\n", d.State.CreatedAt, d.State.DeletedAt, len(d.State.Jobs))
		for _, j := range d.State.Jobs {
			if j == nil {
				continue
			}
			participant := ""
			if j.Participant != nil {
				participant = j.Participant.Identity
			}
			status := ""
			workerID := ""
			jobErr := ""
			startedAt := int64(0)
			endedAt := int64(0)
			updatedAt := int64(0)
			if st := j.GetState(); st != nil {
				status = st.GetStatus().String()
				workerID = st.GetWorkerId()
				jobErr = st.GetError()
				startedAt = st.GetStartedAt()
				endedAt = st.GetEndedAt()
				updatedAt = st.GetUpdatedAt()
			}
			fmt.Printf("  Job: id=%s type=%s participant=%s status=%s worker=%s started_at=%d ended_at=%d updated_at=%d error=%q\n",
				j.Id, j.Type.String(), participant, status, workerID, startedAt, endedAt, updatedAt, jobErr)
		}
	}

	return nil
}

func DeleteRoom(args []string) error {
	fs := flag.NewFlagSet("delete-room", flag.ContinueOnError)
	roomName := fs.String("room", "", "room name to delete")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *roomName == "" {
		return fmt.Errorf("--room is required")
	}

	if err := loadEnv(); err != nil {
		return err
	}

	serverURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	roomClient := lksdk.NewRoomServiceClient(serverURL, apiKey, apiSecret)
	_, err := roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: *roomName})
	if err != nil {
		return err
	}

	fmt.Printf("✅ Room deleted: %s\n", *roomName)
	return nil
}

func RunPublisherClient(args []string) error {
	fs := flag.NewFlagSet("publisher-client", flag.ContinueOnError)
	roomName := fs.String("room", "", "room name to join")
	identity := fs.String("identity", envOrDefault("PUBLISHER_IDENTITY", "rejoin-publisher"), "participant identity")
	duration := fs.Duration("duration", 5*time.Second, "how long to publish before disconnecting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *roomName == "" {
		return fmt.Errorf("--room is required")
	}

	if err := loadEnv(); err != nil {
		return err
	}

	serverURL := os.Getenv("LIVEKIT_URL")
	apiKey := os.Getenv("LIVEKIT_API_KEY")
	apiSecret := os.Getenv("LIVEKIT_API_SECRET")

	token, err := createRoomToken(apiKey, apiSecret, *roomName, *identity, true, false)
	if err != nil {
		return err
	}

	fmt.Printf("🔗 Connecting publisher %q to room %q\n", *identity, *roomName)
	room, err := lksdk.ConnectToRoomWithToken(serverURL, token, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(false))
	if err != nil {
		return err
	}
	defer room.Disconnect()

	track, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		return err
	}

	pub, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "publisher-audio",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		return err
	}
	fmt.Printf("📤 Published audio track: %s\n", pub.SID())

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	go publishSilence(ctx, track)

	<-ctx.Done()
	fmt.Printf("🔌 Disconnecting publisher %q from room %q\n", *identity, *roomName)
	return nil
}

func publishSilence(ctx context.Context, track *lksdk.LocalSampleTrack) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	// 20ms of stereo 16-bit audio at 48kHz
	buffer := make([]byte, 960*2*2)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = track.WriteSample(media.Sample{
				Data:      buffer,
				Duration:  20 * time.Millisecond,
				Timestamp: time.Now(),
			}, nil)
		}
	}
}

func createRoomToken(apiKey, apiSecret, roomName, identity string, canPublish, canSubscribe bool) (string, error) {
	at := auth.NewAccessToken(apiKey, apiSecret)

	pubPtr := &canPublish
	subPtr := &canSubscribe

	at.AddGrant(&auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanPublish:   pubPtr,
		CanSubscribe: subPtr,
	}).SetIdentity(identity).SetValidFor(time.Hour)

	token, err := at.ToJWT()
	if err != nil {
		return "", err
	}
	return token, nil
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
