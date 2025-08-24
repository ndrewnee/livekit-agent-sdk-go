package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func testMain() {
	// Configuration
	host := getEnvTest("LIVEKIT_URL", "http://localhost:7880")
	apiKey := getEnvTest("LIVEKIT_API_KEY", "devkey")
	apiSecret := getEnvTest("LIVEKIT_API_SECRET", "secret")
	roomName := getEnvTest("ROOM_NAME", "test-participant-room")
	participantIdentity := getEnvTest("PARTICIPANT_IDENTITY", "test-user-1")

	fmt.Println("Setting up participant monitoring test...")
	fmt.Printf("Room: %s\n", roomName)
	fmt.Printf("Target participant: %s\n", participantIdentity)
	fmt.Println()

	// Create room service client
	roomClient := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)

	// First, create a room
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Test room for participant monitoring",
	})

	if err != nil {
		log.Fatal("Failed to create room:", err)
	}

	fmt.Printf("✓ Room created: %s (SID: %s)\n", room.Name, room.Sid)

	// Create participant-specific agent dispatch
	// Note: In a real scenario, this would be triggered when a participant joins
	// For testing, we'll create the dispatch metadata
	jobMetadata := JobMetadata{
		ParticipantIdentity: participantIdentity,
		EndOnDisconnect:     true,
		MonitoringFeatures:  []string{"audio_level", "speaking_detection", "quality_tracking"},
	}

	metadataJSON, _ := json.Marshal(jobMetadata)

	// Create agent dispatch for the participant
	// In production, this would be done through room configuration or API
	fmt.Println("\n✓ Participant job metadata prepared:")
	fmt.Printf("  %s\n", string(metadataJSON))

	// Generate tokens
	// Token for the target participant
	participantToken := generateToken(apiKey, apiSecret, roomName, participantIdentity)

	// Token for another participant
	otherToken := generateToken(apiKey, apiSecret, roomName, "other-user")

	fmt.Println("\nTo test the participant monitor:")
	fmt.Println("1. Make sure the participant-monitoring-agent is running")
	fmt.Println("2. The agent will monitor the specific participant:", participantIdentity)
	fmt.Println()
	fmt.Println("Connect the TARGET participant with this token:")
	fmt.Printf("%s\n", participantToken)
	fmt.Println()
	fmt.Println("LiveKit Meet URL for TARGET participant:")
	fmt.Printf("https://meet.livekit.io/custom?liveKitUrl=ws://localhost:7880&token=%s\n", participantToken)
	fmt.Println()
	fmt.Println("Connect ANOTHER participant with this token:")
	fmt.Printf("%s\n", otherToken)
	fmt.Println()
	fmt.Println("LiveKit Meet URL for OTHER participant:")
	fmt.Printf("https://meet.livekit.io/custom?liveKitUrl=ws://localhost:7880&token=%s\n", otherToken)
	fmt.Println()
	fmt.Println("The agent will only monitor the TARGET participant!")
}

func generateToken(apiKey, apiSecret, roomName, identity string) string {
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     roomName,
	}
	at.AddGrant(grant).
		SetIdentity(identity).
		SetValidFor(3600)

	token, err := at.ToJWT()
	if err != nil {
		log.Fatal("Failed to create token:", err)
	}
	return token
}

func getEnvTest(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Run this as a separate program
func init() {
	if len(os.Args) > 1 && os.Args[1] == "dispatch" {
		testMain()
		os.Exit(0)
	}
}
