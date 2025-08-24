package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// This demonstrates how to dispatch a publisher job
func dispatchJob() {
	// Configuration
	host := getEnvTest("LIVEKIT_URL", "http://localhost:7880")
	apiKey := getEnvTest("LIVEKIT_API_KEY", "devkey")
	apiSecret := getEnvTest("LIVEKIT_API_SECRET", "secret")
	roomName := getEnvTest("ROOM_NAME", "test-publisher-room")

	// Create room service client
	roomClient := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)

	// Create room with publisher agent dispatch
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Room with media publisher",
		// Configure agent to handle publisher jobs
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "media-publisher",
				Metadata: fmt.Sprintf(`{
					"mode": "audio_tone",
					"tone_frequency": 440,
					"volume": 0.5,
					"welcome_message": "Media publisher agent is streaming audio!"
				}`),
			},
		},
	})

	if err != nil {
		log.Fatal("Failed to create room:", err)
	}

	fmt.Printf("✓ Room created with media publisher enabled\n")
	fmt.Printf("  Room SID: %s\n", room.Sid)
	fmt.Printf("  Room Name: %s\n", room.Name)
	fmt.Println()

	// Show different publishing modes
	fmt.Println("Publishing modes available:")
	fmt.Println("  - audio_tone: Generates a sine wave tone")
	fmt.Println("  - video_pattern: Generates video test patterns")
	fmt.Println("  - both: Generates both audio and video")
	fmt.Println("  - interactive: Responds to data messages")
	fmt.Println()

	// Create participant token to join and observe
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     roomName,
	}
	at.AddGrant(grant).SetIdentity("observer")

	token, err := at.ToJWT()
	if err != nil {
		log.Fatal("Failed to create token:", err)
	}

	fmt.Println("Join the room to see the media publisher in action:")
	fmt.Printf("  Token: %s\n", token)
	fmt.Println()
	fmt.Println("Or use the LiveKit CLI:")
	fmt.Printf("  lk room join --url %s --api-key %s --api-secret %s %s\n",
		host, apiKey, apiSecret, roomName)
}

func getEnvTest(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Run this to dispatch a job
func init() {
	if len(os.Args) > 1 && os.Args[1] == "dispatch" {
		dispatchJob()
		os.Exit(0)
	}
}
