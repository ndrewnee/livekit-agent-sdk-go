package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// dispatchJob creates a LiveKit room with automatic agent dispatch configuration.
//
// When called (via "go run . dispatch-job"), this function:
//  1. Reads LiveKit connection settings from environment variables
//  2. Creates a room with the specified name (default: publisher-hls-room)
//  3. Configures automatic agent dispatch for the publisher-hls-recorder
//  4. Prints room details and instructions
//
// The room is created with agent dispatch metadata, so when a participant
// publishes audio/video tracks, the HLS recording agent will be dispatched
// automatically by the LiveKit server.
//
// Required environment variables:
//   - LIVEKIT_URL: LiveKit server URL (default: http://localhost:7880)
//   - LIVEKIT_API_KEY: LiveKit API key
//   - LIVEKIT_API_SECRET: LiveKit API secret
//
// Optional environment variables:
//   - ROOM_NAME: Room name to create (default: publisher-hls-room)
//   - AGENT_NAME: Agent name for dispatch (default: publisher-hls-recorder)
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

// init is called automatically before main() and checks for the "dispatch-job" command.
// If "go run . dispatch-job" is executed, this function calls dispatchJob() and exits
// immediately without running the main agent loop. This provides a convenient CLI subcommand
// for creating pre-configured LiveKit rooms without modifying the main agent behavior.
func init() {
	if len(os.Args) > 1 && os.Args[1] == "dispatch-job" {
		dispatchJob()
		os.Exit(0)
	}
}
