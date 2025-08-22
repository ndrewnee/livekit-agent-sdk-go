package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
	url := "http://localhost:7880"
	apiKey := "devkey"
	apiSecret := "secret"

	// Create room service client
	roomClient := lksdk.NewRoomServiceClient(url, apiKey, apiSecret)

	// Create room with agent dispatch
	roomName := fmt.Sprintf("test-room-%d", time.Now().Unix())
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Room created with agent dispatch",
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "simple-agent",
				Metadata:  "test agent metadata",
			},
		},
	})
	if err != nil {
		log.Fatal("Failed to create room:", err)
	}

	fmt.Printf("✅ Created room: %s (sid: %s)\n", room.Name, room.Sid)
	fmt.Println("✅ Agent dispatch configured")

	// Generate a token for a participant
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     room.Name,
	}
	at.AddGrant(grant).SetIdentity("test-participant")
	token, _ := at.ToJWT()

	// Connect as participant to trigger agent
	fmt.Println("\n📡 Connecting as participant...")
	participantRoom, err := lksdk.ConnectToRoomWithToken(url, token, &lksdk.RoomCallback{
		OnDataPacketReceived: func(data lksdk.DataPacket, params lksdk.DataReceiveParams) {
			fmt.Printf("📨 Received data: %s\n", string(data.Payload))
		},
		ParticipantCallback: lksdk.ParticipantCallback{
			OnParticipantConnected: func(participant *lksdk.RemoteParticipant) {
				fmt.Printf("👤 Participant connected: %s (%s)\n", participant.Identity(), participant.Name())
			},
		},
	}, lksdk.WithAutoSubscribe(false))

	if err != nil {
		log.Fatal("Failed to connect:", err)
	}
	defer participantRoom.Disconnect()

	fmt.Printf("✅ Connected to room as %s\n", participantRoom.LocalParticipant.Identity())

	// List participants to see if agent joined
	time.Sleep(2 * time.Second)
	fmt.Println("\n👥 Room participants:")
	for _, p := range participantRoom.GetRemoteParticipants() {
		fmt.Printf("  - %s (%s) - Metadata: %s\n", p.Identity(), p.Name(), p.Metadata())
	}

	// Wait for messages from agent
	fmt.Println("\n⏳ Waiting for agent messages...")
	time.Sleep(15 * time.Second)

	fmt.Println("\n✅ Test completed successfully!")
}