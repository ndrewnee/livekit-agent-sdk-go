package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// This demonstrates how to dispatch a participant monitoring job
// In production, this would typically be triggered by your application
// when you want to start monitoring a specific participant
func dispatchJob() {
	// Configuration
	host := getEnvTest("LIVEKIT_URL", "http://localhost:7880")
	apiKey := getEnvTest("LIVEKIT_API_KEY", "devkey")
	apiSecret := getEnvTest("LIVEKIT_API_SECRET", "secret")
	roomName := getEnvTest("ROOM_NAME", "test-participant-room")
	participantIdentity := getEnvTest("PARTICIPANT_IDENTITY", "test-user-1")

	// Create room service client
	roomClient := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)

	// Create room with participant agent dispatch
	// Note: For participant-level agents, you typically dispatch jobs
	// after participants join, not during room creation

	// First create the room
	room, err := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Room with participant monitoring",
		// For participant agents, we configure them differently
		// They are dispatched per participant, not per room
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "participant-monitor",
				// This metadata would be used to configure participant-specific monitoring
				Metadata: fmt.Sprintf(`{"participant_pattern": "*", "auto_monitor": true}`),
			},
		},
	})

	if err != nil {
		log.Fatal("Failed to create room:", err)
	}

	fmt.Printf("✓ Room created with participant monitoring enabled\n")
	fmt.Printf("  Room SID: %s\n", room.Sid)
	fmt.Printf("  Room Name: %s\n", room.Name)
	fmt.Println()

	// In a real application, you would dispatch participant-specific jobs
	// when participants join or based on specific events
	jobMetadata := JobMetadata{
		ParticipantIdentity: participantIdentity,
		EndOnDisconnect:     true,
		MonitoringFeatures:  []string{"audio_level", "speaking_detection", "quality_tracking"},
	}

	metadataJSON, _ := json.Marshal(jobMetadata)
	fmt.Println("Participant job configuration:")
	fmt.Printf("%s\n", string(metadataJSON))
	fmt.Println()
	fmt.Println("When participants join this room, the agent will monitor them based on the configuration.")
}

// Run this to dispatch a job
func init() {
	if len(os.Args) > 1 && os.Args[1] == "dispatch-job" {
		dispatchJob()
		os.Exit(0)
	}
}
