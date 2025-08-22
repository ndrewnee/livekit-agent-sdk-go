package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
	// Get LiveKit server URL from environment or use default
	url := os.Getenv("LIVEKIT_URL")
	if url == "" {
		url = "http://localhost:7880"
	}

	// API credentials
	apiKey := "devkey"
	apiSecret := "secret"

	// Create room service client
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)

	// Create room with agent dispatch
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: fmt.Sprintf("test-room-%d", time.Now().Unix()),
		Metadata: "Room created with agent dispatch",
		// This is the key part - specify which agents should be dispatched
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "simple-agent", // Must match the agent's registered name
				Metadata:  "test agent metadata",
			},
		},
	})
	if err != nil {
		log.Fatal("Failed to create room:", err)
	}

	fmt.Printf("Created room: %s (sid: %s)\n", room.Name, room.Sid)
	fmt.Println("Agent dispatch has been configured for this room")
	fmt.Println("\nNow when participants join this room, agent jobs will be dispatched")
	
	// Generate a token for a participant to join
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     room.Name,
	}
	at.AddGrant(grant).SetIdentity("test-participant")
	
	token, err := at.ToJWT()
	if err != nil {
		log.Fatal("Failed to create token:", err)
	}
	
	fmt.Printf("\nJoin URL: %s?token=%s\n", url, token)
}