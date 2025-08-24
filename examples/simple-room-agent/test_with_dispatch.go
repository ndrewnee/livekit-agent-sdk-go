// test_with_dispatch.go - Test program to create a room with agent dispatch
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

func testMain() {
	// Configuration
	host := getEnvTest("LIVEKIT_URL", "http://localhost:7880")
	apiKey := getEnvTest("LIVEKIT_API_KEY", "devkey")
	apiSecret := getEnvTest("LIVEKIT_API_SECRET", "secret")
	roomName := getEnvTest("ROOM_NAME", "test-room-with-agent")

	fmt.Println("Creating room with agent dispatch...")
	fmt.Printf("Room name: %s\n", roomName)
	fmt.Printf("Agent name: simple-room-agent\n")
	fmt.Println()

	// Create room service client
	roomClient := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)

	// Create room with agent dispatch
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Room created with agent dispatch",
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "simple-room-agent",
				Metadata:  `{"purpose": "analytics", "test": true}`,
			},
		},
	})

	if err != nil {
		log.Fatal("Failed to create room:", err)
	}

	fmt.Printf("✓ Room created successfully!\n")
	fmt.Printf("  Room SID: %s\n", room.Sid)
	fmt.Printf("  Room Name: %s\n", room.Name)
	fmt.Println()

	// Generate a token
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     roomName,
	}
	at.AddGrant(grant).
		SetIdentity("test-user").
		SetValidFor(3600)

	token, err := at.ToJWT()
	if err != nil {
		log.Fatal("Failed to create token:", err)
	}

	fmt.Println("To connect to the room:")
	fmt.Printf("Token: %s\n", token)
	fmt.Println()
	fmt.Println("LiveKit Meet URL:")
	fmt.Printf("https://meet.livekit.io/custom?liveKitUrl=ws://localhost:7880&token=%s\n", token)
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
