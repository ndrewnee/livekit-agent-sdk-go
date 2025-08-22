package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	// Generate auth token
	at := auth.NewAccessToken("devkey", "secret")
	grant := &auth.VideoGrant{
		Agent: true,
	}
	at.SetVideoGrant(grant)
	authToken, _ := at.ToJWT()

	// Build WebSocket URL
	u, _ := url.Parse("http://localhost:7880")
	u.Scheme = "ws"
	u.Path = "/agent"
	q := u.Query()
	q.Set("protocol", "1")
	u.RawQuery = q.Encode()

	// Connect
	headers := http.Header{
		"Authorization": []string{"Bearer " + authToken},
	}

	log.Printf("Connecting to %s", u.String())
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), headers)
	if err != nil {
		log.Fatal("dial:", err)
	}
	defer conn.Close()

	// Send registration
	req := &livekit.RegisterWorkerRequest{
		Type:      livekit.JobType_JT_ROOM,
		Version:   "1.0.0",
		AgentName: "simple-agent",
	}
	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Register{
			Register: req,
		},
	}
	
	data, _ := protojson.Marshal(msg)
	log.Printf("Sending registration: %s", string(data))
	
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Fatal("write:", err)
	}

	// Read messages
	log.Println("Waiting for messages...")
	go func() {
		for {
			msgType, message, err := conn.ReadMessage()
			if err != nil {
				log.Println("read error:", err)
				return
			}
			
			if msgType == websocket.TextMessage {
				var serverMsg livekit.ServerMessage
				if err := protojson.Unmarshal(message, &serverMsg); err == nil {
					log.Printf("Received ServerMessage: %s", formatMessage(&serverMsg))
				} else {
					log.Printf("Received text: %s", string(message))
				}
			} else {
				log.Printf("Received binary message of %d bytes", len(message))
			}
		}
	}()

	// Send periodic pings
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	
	for {
		select {
		case <-ticker.C:
			ping := &livekit.WorkerMessage{
				Message: &livekit.WorkerMessage_Ping{
					Ping: &livekit.WorkerPing{
						Timestamp: time.Now().Unix(),
					},
				},
			}
			data, _ := protojson.Marshal(ping)
			log.Printf("Sending ping")
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				log.Println("ping error:", err)
				return
			}
		}
	}
}

func formatMessage(msg *livekit.ServerMessage) string {
	data, _ := json.MarshalIndent(msg, "", "  ")
	return string(data)
}