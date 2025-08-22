package main

import (
	"log"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
	// Use the WebSocket URL for client connections
	url := "ws://localhost:7880"

	// Create a room with participant to trigger agent dispatch
	room := lksdk.NewRoom(&lksdk.RoomCallback{})
	
	log.Println("Connecting to room to trigger agent dispatch...")
	
	connectInfo := lksdk.ConnectInfo{
		APIKey:              "devkey",
		APISecret:           "secret", 
		RoomName:            "test-room-with-agent",
		ParticipantIdentity: "test-participant",
	}
	
	if err := room.Join(url, connectInfo); err != nil {
		log.Fatal("Failed to join room:", err)
	}
	
	log.Println("Successfully joined room - agent should be dispatched now")
	
	// Stay connected for a few seconds to allow agent to process
	time.Sleep(10 * time.Second)
	
	room.Disconnect()
	log.Println("Disconnected from room")
}