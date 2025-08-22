package main

import (
	"log"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
	// Use the token and URL from the latest create-room-with-agent output
	url := "ws://localhost:7880"
	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTU3Mjc2MTYsImlkZW50aXR5IjoidGVzdC1wYXJ0aWNpcGFudCIsImlzcyI6ImRldmtleSIsIm5iZiI6MTc1NTcwNjAxNiwic3ViIjoidGVzdC1wYXJ0aWNpcGFudCIsInZpZGVvIjp7InJvb20iOiJ0ZXN0LXJvb20td2l0aC1hZ2VudCIsInJvb21Kb2luIjp0cnVlfX0.Ithf65FGIfdyW3U8sIV1ZDkXBLr8C5eqeIcrfOZjmSc"

	// Create a room with participant to trigger agent dispatch
	room := lksdk.NewRoom(&lksdk.RoomCallback{})
	
	log.Println("Connecting to room with token to trigger agent dispatch...")
	
	if err := room.JoinWithToken(url, token); err != nil {
		log.Fatal("Failed to join room:", err)
	}
	
	log.Println("Successfully joined room - agent should be dispatched now")
	log.Printf("Room name: %s, SID: %s", room.Name(), room.SID())
	log.Printf("Local participant: %s", room.LocalParticipant.Identity())
	
	// Stay connected for longer to ensure agent has time to be dispatched
	time.Sleep(15 * time.Second)
	
	room.Disconnect()
	log.Println("Disconnected from room")
}