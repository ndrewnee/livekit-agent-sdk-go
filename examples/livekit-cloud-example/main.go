// main.go - Comprehensive LiveKit Cloud SDK Example
// This example demonstrates all major features of the LiveKit Go SDK with LiveKit Cloud
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// CloudSDKExample demonstrates all major LiveKit SDK features
type CloudSDKExample struct {
	// Configuration
	url       string
	apiKey    string
	apiSecret string

	// Room management
	roomClient *lksdk.RoomServiceClient
	rooms      map[string]*lksdk.Room
	roomsMutex sync.RWMutex

	// Metrics
	metrics *Metrics

	// Context for shutdown
	ctx    context.Context
	cancel context.CancelFunc
}

// Metrics tracks usage and performance
type Metrics struct {
	mu                   sync.RWMutex
	RoomsCreated         int
	ParticipantsJoined   int
	TracksPublished      int
	TracksSubscribed     int
	DataMessagesSent     int
	DataMessagesReceived int
	BytesTransmitted     uint64
	ConnectionErrors     int
	StartTime            time.Time
}

func main() {
	// Check for agent/example mode
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "proper-agent":
			if err := RunProperAgent(); err != nil {
				log.Fatalf("Proper agent failed: %v", err)
			}
			return
		case "agent-demo":
			if err := RunAgentDemo(); err != nil {
				log.Fatalf("Agent demo failed: %v", err)
			}
			return
		case "create-room":
			if err := CreateRoomWithAgent(); err != nil {
				log.Fatalf("Room creation failed: %v", err)
			}
			return
		case "help":
			fmt.Println("LiveKit Cloud Example - Available Commands:")
			fmt.Println("  proper-agent          - Run the agent to handle jobs")
			fmt.Println("  agent-demo            - Run agent with automatic room creation")
			fmt.Println("  create-room           - Create a room with agent dispatch")
			fmt.Println("  help                  - Show this help message")
			return
		}
	}

	fmt.Println("\n================================================")
	fmt.Println("   🌩️  LiveKit Cloud SDK Comprehensive Example")
	fmt.Println("================================================\n")

	// Initialize logger
	logger.InitFromConfig(&logger.Config{
		Level: "info",
		JSON:  false,
	}, "livekit-cloud-example")

	// Load configuration
	if err := loadConfiguration(); err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create and run example
	example := NewCloudSDKExample()
	if err := example.Run(); err != nil {
		log.Fatalf("Example failed: %v", err)
	}
}

func loadConfiguration() error {
	// Try multiple locations for .env file
	envPaths := []string{
		".env",
		"../.env",
		"../../.env",
	}

	for _, path := range envPaths {
		if err := godotenv.Load(path); err == nil {
			absPath, _ := filepath.Abs(path)
			fmt.Printf("📁 Loaded configuration from: %s\n", absPath)
			return nil
		}
	}

	// Check if environment variables are already set
	if os.Getenv("LIVEKIT_URL") != "" {
		fmt.Println("📁 Using existing environment variables")
		return nil
	}

	return fmt.Errorf("configuration not found - please create .env file or set environment variables")
}

func NewCloudSDKExample() *CloudSDKExample {
	ctx, cancel := context.WithCancel(context.Background())

	return &CloudSDKExample{
		url:       os.Getenv("LIVEKIT_URL"),
		apiKey:    os.Getenv("LIVEKIT_API_KEY"),
		apiSecret: os.Getenv("LIVEKIT_API_SECRET"),
		rooms:     make(map[string]*lksdk.Room),
		metrics: &Metrics{
			StartTime: time.Now(),
		},
		ctx:    ctx,
		cancel: cancel,
	}
}

func (e *CloudSDKExample) Run() error {
	// Validate configuration
	if err := e.validateConfig(); err != nil {
		return err
	}

	// Initialize clients
	e.initializeClients()

	// Setup signal handling
	e.setupSignalHandling()

	// Run demo scenarios
	fmt.Println("\n🚀 Starting SDK Feature Demonstrations...")
	fmt.Println("=" + strings.Repeat("=", 50))

	// Feature demonstrations
	features := []struct {
		name string
		fn   func() error
	}{
		{"Room Management", e.demonstrateRoomManagement},
		{"Participant & Tracks", e.demonstrateParticipantFeatures},
		{"Data Channels", e.demonstrateDataChannels},
		{"Recording & Egress", e.demonstrateRecordingEgress},
		{"Webhooks", e.demonstrateWebhooks},
		{"Access Control", e.demonstrateAccessControl},
		{"Room Metadata", e.demonstrateMetadata},
		{"Connection Quality", e.demonstrateConnectionQuality},
	}

	for i, feature := range features {
		fmt.Printf("\n📌 Demo %d: %s\n", i+1, feature.name)
		fmt.Println(strings.Repeat("-", 40))

		if err := feature.fn(); err != nil {
			fmt.Printf("⚠️  %s failed: %v\n", feature.name, err)
			// Continue with other features
		}

		// Brief pause between demos
		time.Sleep(2 * time.Second)
	}

	// Print final metrics
	e.printMetrics()

	fmt.Println("\n✅ All demonstrations complete!")
	fmt.Println("Press Ctrl+C to exit...")

	// Wait for shutdown
	<-e.ctx.Done()

	// Cleanup
	e.cleanup()

	return nil
}

func (e *CloudSDKExample) validateConfig() error {
	if e.url == "" {
		return fmt.Errorf("LIVEKIT_URL not set")
	}
	if e.apiKey == "" {
		return fmt.Errorf("LIVEKIT_API_KEY not set")
	}
	if e.apiSecret == "" {
		return fmt.Errorf("LIVEKIT_API_SECRET not set")
	}

	// Detect environment type
	if strings.HasPrefix(e.url, "wss://") && strings.Contains(e.url, "livekit.cloud") {
		fmt.Println("✅ Connected to LiveKit Cloud")
	} else if strings.HasPrefix(e.url, "ws://") {
		fmt.Println("⚠️  Connected to local LiveKit server")
	} else {
		return fmt.Errorf("invalid LIVEKIT_URL format: %s", e.url)
	}

	fmt.Printf("🔗 URL: %s\n", e.url)
	fmt.Printf("🔑 API Key: %s...\n", truncateString(e.apiKey, 8))

	return nil
}

func (e *CloudSDKExample) initializeClients() {
	// Initialize Room Service Client
	e.roomClient = lksdk.NewRoomServiceClient(e.url, e.apiKey, e.apiSecret)

	// Webhook handling would be initialized here if needed

	fmt.Println("✅ SDK clients initialized")
}

func (e *CloudSDKExample) setupSignalHandling() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n\n🛑 Shutdown signal received...")
		e.cancel()
	}()
}

// Feature 1: Room Management
func (e *CloudSDKExample) demonstrateRoomManagement() error {
	fmt.Println("Creating and managing rooms...")

	// Create a room
	roomName := fmt.Sprintf("sdk-demo-%d", time.Now().Unix())
	room, err := e.roomClient.CreateRoom(e.ctx, &livekit.CreateRoomRequest{
		Name:            roomName,
		EmptyTimeout:    300, // 5 minutes
		MaxParticipants: 10,
		Metadata:        `{"type": "demo", "created": "` + time.Now().Format(time.RFC3339) + `"}`,
		NodeId:          "", // Auto-select node
		MinPlayoutDelay: 0,
		MaxPlayoutDelay: 0,
	})

	if err != nil {
		return fmt.Errorf("failed to create room: %w", err)
	}

	e.metrics.mu.Lock()
	e.metrics.RoomsCreated++
	e.metrics.mu.Unlock()

	fmt.Printf("✅ Created room: %s (SID: %s)\n", room.Name, room.Sid)

	// List rooms
	rooms, err := e.roomClient.ListRooms(e.ctx, &livekit.ListRoomsRequest{})
	if err == nil {
		fmt.Printf("📋 Total rooms on server: %d\n", len(rooms.Rooms))
	}

	// Update room metadata
	_, err = e.roomClient.UpdateRoomMetadata(e.ctx, &livekit.UpdateRoomMetadataRequest{
		Room:     roomName,
		Metadata: `{"status": "active", "updated": "` + time.Now().Format(time.RFC3339) + `"}`,
	})
	if err == nil {
		fmt.Println("✅ Updated room metadata")
	}

	// Store room name for cleanup
	e.roomsMutex.Lock()
	e.rooms[roomName] = nil
	e.roomsMutex.Unlock()

	return nil
}

// Feature 2: Participants and Tracks
func (e *CloudSDKExample) demonstrateParticipantFeatures() error {
	fmt.Println("Managing participants and tracks...")

	// Create a test room
	roomName := fmt.Sprintf("participant-demo-%d", time.Now().Unix())
	_, err := e.roomClient.CreateRoom(e.ctx, &livekit.CreateRoomRequest{
		Name: roomName,
	})
	if err != nil {
		return err
	}

	// Connect as participant
	token := e.createToken(roomName, "demo-participant", true, true)
	room, err := lksdk.ConnectToRoomWithToken(e.url, token, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				e.metrics.mu.Lock()
				e.metrics.TracksSubscribed++
				e.metrics.mu.Unlock()
				fmt.Printf("📡 Subscribed to %s track from %s\n", track.Kind(), rp.Identity())
			},
		},
		OnParticipantConnected: func(participant *lksdk.RemoteParticipant) {
			e.metrics.mu.Lock()
			e.metrics.ParticipantsJoined++
			e.metrics.mu.Unlock()
			fmt.Printf("👤 Participant joined: %s\n", participant.Identity())
		},
		OnRoomMetadataChanged: func(metadata string) {
			fmt.Printf("📝 Room metadata changed\n")
		},
	})

	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	e.roomsMutex.Lock()
	e.rooms[roomName] = room
	e.roomsMutex.Unlock()

	// Publish audio track
	audioTrack, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err == nil {
		pub, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
			Name:   "demo-audio",
			Source: livekit.TrackSource_MICROPHONE,
		})
		if err == nil {
			fmt.Printf("✅ Published audio track: %s\n", pub.SID())

			// Send some audio samples
			go e.generateAudioSamples(audioTrack)
		}
	}

	// Update participant metadata
	room.LocalParticipant.SetMetadata(`{"role": "presenter", "joinTime": "` + time.Now().Format(time.RFC3339) + `"}`)

	// List participants
	participants, err := e.roomClient.ListParticipants(e.ctx, &livekit.ListParticipantsRequest{
		Room: roomName,
	})
	if err == nil {
		fmt.Printf("👥 Participants in room: %d\n", len(participants.Participants))
	}

	return nil
}

// Feature 3: Data Channels
func (e *CloudSDKExample) demonstrateDataChannels() error {
	fmt.Println("Testing data channels...")

	// Use existing room or create new one
	var room *lksdk.Room
	e.roomsMutex.RLock()
	for _, r := range e.rooms {
		if r != nil {
			room = r
			break
		}
	}
	e.roomsMutex.RUnlock()

	if room == nil {
		fmt.Println("⚠️  No active room for data channel demo")
		return nil
	}

	// Send reliable data
	reliableData := map[string]interface{}{
		"type":      "status",
		"timestamp": time.Now().Unix(),
		"message":   "Hello from LiveKit Cloud SDK",
	}

	dataBytes, _ := json.Marshal(reliableData)
	err := room.LocalParticipant.PublishData(dataBytes, lksdk.WithDataPublishReliable(true))

	if err == nil {
		e.metrics.mu.Lock()
		e.metrics.DataMessagesSent++
		e.metrics.BytesTransmitted += uint64(len(dataBytes))
		e.metrics.mu.Unlock()
		fmt.Println("✅ Sent reliable data message")
	}

	// Send lossy data (for real-time updates)
	lossyData := []byte("real-time-update")
	err = room.LocalParticipant.PublishData(lossyData, lksdk.WithDataPublishReliable(false))

	if err == nil {
		e.metrics.mu.Lock()
		e.metrics.DataMessagesSent++
		e.metrics.BytesTransmitted += uint64(len(lossyData))
		e.metrics.mu.Unlock()
		fmt.Println("✅ Sent lossy data message")
	}

	return nil
}

// Feature 4: Recording and Egress
func (e *CloudSDKExample) demonstrateRecordingEgress() error {
	fmt.Println("Demonstrating recording/egress capabilities...")

	// Note: Actual recording requires Egress service to be configured
	// This demonstrates the API calls

	var roomName string
	e.roomsMutex.RLock()
	for name := range e.rooms {
		roomName = name
		break
	}
	e.roomsMutex.RUnlock()

	if roomName == "" {
		fmt.Println("⚠️  No active room for recording demo")
		return nil
	}

	// Example: Start room composite recording (requires Egress service)
	fmt.Printf("📹 Would start recording for room: %s\n", roomName)
	fmt.Println("   (Actual recording requires Egress service configuration)")

	// Example: Track egress
	fmt.Println("📤 Track egress options available:")
	fmt.Println("   - Room composite recording")
	fmt.Println("   - Track recording")
	fmt.Println("   - Stream output (RTMP/HLS)")

	return nil
}

// Feature 5: Webhooks
func (e *CloudSDKExample) demonstrateWebhooks() error {
	fmt.Println("Demonstrating webhook handling...")

	// Webhook events are handled through HTTP endpoints
	// In production, you would verify the webhook signature
	fmt.Println("📨 Webhook verification ready")
	fmt.Println("   Event types supported:")
	fmt.Println("   - room_started")
	fmt.Println("   - room_finished")
	fmt.Println("   - participant_connected")
	fmt.Println("   - participant_disconnected")
	fmt.Println("   - track_published")
	fmt.Println("   - track_unpublished")
	fmt.Println("   - recording_started")
	fmt.Println("   - recording_finished")

	return nil
}

// Feature 6: Access Control
func (e *CloudSDKExample) demonstrateAccessControl() error {
	fmt.Println("Demonstrating access control...")

	roomName := fmt.Sprintf("access-demo-%d", time.Now().Unix())

	// Create tokens with different permissions
	tokens := []struct {
		identity     string
		canPublish   bool
		canSubscribe bool
		description  string
	}{
		{"viewer", false, true, "View-only participant"},
		{"presenter", true, true, "Can publish and subscribe"},
		{"publisher", true, false, "Can only publish"},
	}

	for _, t := range tokens {
		token := e.createToken(roomName, t.identity, t.canPublish, t.canSubscribe)
		fmt.Printf("🔐 Created token for %s: %s\n", t.identity, t.description)
		fmt.Printf("   Token (truncated): %s...\n", token[:20])
	}

	// Room-level permissions
	fmt.Println("\n🔒 Room-level permissions available:")
	fmt.Println("   - room:create")
	fmt.Println("   - room:list")
	fmt.Println("   - room:record")
	fmt.Println("   - participant:list")
	fmt.Println("   - participant:kick")

	return nil
}

// Feature 7: Room and Participant Metadata
func (e *CloudSDKExample) demonstrateMetadata() error {
	fmt.Println("Demonstrating metadata capabilities...")

	// Room metadata
	roomMetadata := map[string]interface{}{
		"theme":       "dark",
		"layout":      "gallery",
		"maxSpeakers": 2,
		"features": map[string]bool{
			"chat":        true,
			"recording":   false,
			"screenshare": true,
		},
	}

	metadataJSON, _ := json.MarshalIndent(roomMetadata, "", "  ")
	fmt.Printf("📋 Room metadata example:\n%s\n", metadataJSON)

	// Participant metadata
	participantMetadata := map[string]interface{}{
		"displayName": "John Doe",
		"avatar":      "https://example.com/avatar.jpg",
		"role":        "moderator",
		"permissions": []string{"mute_others", "kick", "record"},
	}

	participantJSON, _ := json.MarshalIndent(participantMetadata, "", "  ")
	fmt.Printf("\n👤 Participant metadata example:\n%s\n", participantJSON)

	return nil
}

// Feature 8: Connection Quality Monitoring
func (e *CloudSDKExample) demonstrateConnectionQuality() error {
	fmt.Println("Demonstrating connection quality monitoring...")

	// Simulated quality levels
	qualityLevels := []struct {
		quality     string
		description string
		action      string
	}{
		{"EXCELLENT", "No packet loss, low latency", "Full quality streams"},
		{"GOOD", "Minimal packet loss", "Maintain current quality"},
		{"POOR", "Noticeable packet loss", "Reduce video quality"},
		{"LOST", "Connection lost", "Attempt reconnection"},
	}

	fmt.Println("📊 Connection quality levels:")
	for _, q := range qualityLevels {
		fmt.Printf("   %s: %s → %s\n", q.quality, q.description, q.action)
	}

	// In a real scenario, you would monitor actual connection quality
	var room *lksdk.Room
	e.roomsMutex.RLock()
	for _, r := range e.rooms {
		if r != nil {
			room = r
			break
		}
	}
	e.roomsMutex.RUnlock()

	if room != nil {
		state := "CONNECTED"
		if room.LocalParticipant == nil {
			state = "DISCONNECTED"
		}
		fmt.Printf("\n🔌 Current connection state: %s\n", state)
	}

	return nil
}

// Helper functions
func (e *CloudSDKExample) createToken(room, identity string, canPublish, canSubscribe bool) string {
	at := auth.NewAccessToken(e.apiKey, e.apiSecret)

	pubPtr := &canPublish
	subPtr := &canSubscribe

	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         room,
		CanPublish:   pubPtr,
		CanSubscribe: subPtr,
	}

	at.AddGrant(grant).
		SetIdentity(identity).
		SetValidFor(time.Hour)

	token, _ := at.ToJWT()
	return token
}

func (e *CloudSDKExample) generateAudioSamples(track *lksdk.LocalSampleTrack) {
	// Generate silence for demo
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	buffer := make([]byte, 960*2*2) // 20ms of stereo 16-bit audio at 48kHz
	count := 0

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			if count >= 100 { // Send 100 frames (2 seconds)
				return
			}

			sample := media.Sample{
				Data:      buffer,
				Duration:  20 * time.Millisecond,
				Timestamp: time.Now(),
			}

			if err := track.WriteSample(sample, nil); err != nil {
				return
			}

			e.metrics.mu.Lock()
			e.metrics.BytesTransmitted += uint64(len(buffer))
			e.metrics.mu.Unlock()

			count++
		}
	}
}

func (e *CloudSDKExample) printMetrics() {
	e.metrics.mu.RLock()
	defer e.metrics.mu.RUnlock()

	elapsed := time.Since(e.metrics.StartTime)

	fmt.Println("\n" + strings.Repeat("=", 50))
	fmt.Println("📊 METRICS SUMMARY")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("⏱️  Duration: %.1f seconds\n", elapsed.Seconds())
	fmt.Printf("🏠 Rooms created: %d\n", e.metrics.RoomsCreated)
	fmt.Printf("👥 Participants joined: %d\n", e.metrics.ParticipantsJoined)
	fmt.Printf("📤 Tracks published: %d\n", e.metrics.TracksPublished)
	fmt.Printf("📡 Tracks subscribed: %d\n", e.metrics.TracksSubscribed)
	fmt.Printf("💬 Data messages sent: %d\n", e.metrics.DataMessagesSent)
	fmt.Printf("📊 Bytes transmitted: %s\n", formatBytes(e.metrics.BytesTransmitted))
	fmt.Printf("❌ Connection errors: %d\n", e.metrics.ConnectionErrors)
	fmt.Println(strings.Repeat("=", 50))
}

func (e *CloudSDKExample) cleanup() {
	fmt.Println("\n🧹 Cleaning up...")

	// Disconnect all rooms
	e.roomsMutex.Lock()
	for name, room := range e.rooms {
		if room != nil {
			room.Disconnect()
		}

		// Delete room from server
		_, err := e.roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
			Room: name,
		})
		if err == nil {
			fmt.Printf("   Deleted room: %s\n", name)
		}
	}
	e.roomsMutex.Unlock()

	fmt.Println("✅ Cleanup complete")
}

// Utility functions
func truncateString(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[:length]
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
