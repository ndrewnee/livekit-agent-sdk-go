package main

import (
	"log"
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
	// Use the token and URL from the latest create-room-with-agent output
	url := "ws://localhost:7880"
	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTU3MjQ1OTcsImlkZW50aXR5IjoidGVzdC1wYXJ0aWNpcGFudCIsImlzcyI6ImRldmtleSIsIm5iZiI6MTc1NTcwMjk5Nywic3ViIjoidGVzdC1wYXJ0aWNpcGFudCIsInZpZGVvIjp7InJvb20iOiJ0ZXN0LXJvb20td2l0aC1hZ2VudCIsInJvb21Kb2luIjp0cnVlfX0.rC8EfoN3w7t3wX85DiJTUgStNFGGV7yiFpnnazdhi2o"

	room := lksdk.NewRoom(&lksdk.RoomCallback{})
	
	log.Println("Connecting to room and waiting for agent dispatch...")
	
	if err := room.JoinWithToken(url, token); err != nil {
		log.Fatal("Failed to join room:", err)
	}
	
	log.Println("Successfully joined room!")
	log.Printf("Room name: %s, SID: %s", room.Name(), room.SID())
	log.Printf("Local participant: %s", room.LocalParticipant.Identity())
	
	// Stay connected for a longer period to allow agent to join
	log.Println("Waiting for agent to join... (will wait 30 seconds)")
	for i := 1; i <= 30; i++ {
		time.Sleep(1 * time.Second)
		participants := room.GetRemoteParticipants()
		log.Printf("Second %d - Remote participants in room: %d", i, len(participants))
		
		if len(participants) > 0 {
			log.Println("Agent has joined the room!")
			for identity, participant := range participants {
				log.Printf("  - %s (%s)", identity, participant.Name())
			}
			// Stay connected a bit longer to see agent activity
			time.Sleep(10 * time.Second)
			break
		}
	}
	
	room.Disconnect()
	log.Println("Disconnected from room")
}