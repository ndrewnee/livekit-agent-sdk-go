package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

func dispatchJob() {
	host := getEnv("LIVEKIT_URL", "http://localhost:7880")
	apiKey := mustGetEnv("LIVEKIT_API_KEY")
	apiSecret := mustGetEnv("LIVEKIT_API_SECRET")
	roomName := getEnv("ROOM_NAME", "publisher-hls-room")

	client := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)

	room, err := client.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: getEnv("AGENT_NAME", "publisher-hls-recorder"),
				Metadata:  `{"record_audio":true,"record_video":true}`,
			},
		},
	})
	if err != nil {
		log.Fatalf("failed to create room: %v", err)
	}

	fmt.Println("✓ Room created for publisher HLS recording")
	fmt.Printf("  Room SID: %s\n", room.Sid)
	fmt.Printf("  Room Name: %s\n", room.Name)
	fmt.Println()
	fmt.Println("When a participant publishes audio/video tracks in this room, the agent will be dispatched automatically.")
}

func init() {
	if len(os.Args) > 1 && os.Args[1] == "dispatch-job" {
		dispatchJob()
		os.Exit(0)
	}
}
