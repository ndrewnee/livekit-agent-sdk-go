// Minimal reproduction of forwarder layer initialization bug
//
// This program demonstrates the issue where agents using manual subscription
// with immediate SetVideoQuality() receive 0 video packets.
//
// Run with UNPATCHED server: 0 packets received
// Run with PATCHED server: Packets received immediately
//
// Usage:
//
//	go run main.go
package main

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

const (
	serverURL = "ws://localhost:7880"
	apiKey    = "devkey"
	apiSecret = "secret"
	roomName  = "minimal-repro-room"
)

var (
	publisherPacketsSent atomic.Int64
	agentPacketsReceived atomic.Int64
)

func main() {
	log.Println("=== Minimal Reproduction: Forwarder Layer Initialization Bug ===")
	log.Printf("Server: %s", serverURL)
	log.Printf("Room: %s", roomName)

	// Start publisher
	publisherDone := make(chan error, 1)
	go runPublisher(publisherDone)

	// Wait for publisher to be ready
	time.Sleep(2 * time.Second)

	// Start agent subscriber
	agentDone := make(chan error, 1)
	go runAgent(agentDone)

	// Wait 10 seconds and check results
	time.Sleep(10 * time.Second)

	sent := publisherPacketsSent.Load()
	received := agentPacketsReceived.Load()

	log.Println("\n=== RESULTS ===")
	log.Printf("Publisher sent: %d packets", sent)
	log.Printf("Agent received: %d packets", received)

	if received == 0 {
		log.Println("\n❌ BUG REPRODUCED: Agent received 0 packets despite publisher sending")
		log.Println("This indicates the forwarder layer initialization bug.")
		log.Println("Run with PATCHED server to see packets received.")
	} else {
		log.Println("\n✅ Working correctly: Agent receiving packets")
		log.Println("Server has the fix applied or different code path triggered.")
	}
}

func runPublisher(done chan<- error) {
	defer close(done)

	log.Println("[PUBLISHER] Connecting to room...")
	room, err := lksdk.ConnectToRoom(serverURL, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: "publisher",
	}, lksdk.NewRoomCallback())
	if err != nil {
		done <- err
		return
	}
	defer room.Disconnect()

	log.Println("[PUBLISHER] Publishing H.264 video track...")

	// Create H.264 track
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video",
		"publisher-video",
	)
	if err != nil {
		done <- err
		return
	}

	_, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "test-video",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		done <- err
		return
	}

	log.Println("[PUBLISHER] Publishing video packets at 30 FPS...")

	// Send simple H.264 frames at 30 FPS
	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()

	// Simple H.264 keyframe (SPS + PPS + IDR)
	sps := []byte{0x00, 0x00, 0x00, 0x01, 0x67, 0x64, 0x00, 0x1f, 0xac, 0xd1, 0x00, 0x78, 0x02, 0x27, 0xe5, 0x84}
	pps := []byte{0x00, 0x00, 0x00, 0x01, 0x68, 0xee, 0x3c, 0x80}
	idr := []byte{0x00, 0x00, 0x00, 0x01, 0x65, 0x88, 0x84, 0x00, 0xff, 0xff, 0xff, 0xff}

	keyframe := append(append(sps, pps...), idr...)

	for i := 0; i < 300; i++ { // 10 seconds worth
		select {
		case <-ticker.C:
			var frame []byte
			if i%30 == 0 {
				// Keyframe every second
				frame = keyframe
			} else {
				// Simple P-frame
				frame = []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9a, 0x24, 0x6c, 0xff}
			}

			if err := track.WriteSample(media.Sample{
				Data:     frame,
				Duration: 33 * time.Millisecond,
			}); err != nil {
				log.Printf("[PUBLISHER] Error writing sample: %v", err)
				continue
			}

			publisherPacketsSent.Add(1)

			if i%30 == 0 {
				log.Printf("[PUBLISHER] Sent keyframe #%d", i/30+1)
			}
		}
	}

	log.Println("[PUBLISHER] Finished publishing")
}

func runAgent(done chan<- error) {
	defer close(done)

	log.Println("[AGENT] Connecting to room with MANUAL subscription...")

	// Create room callback
	roomCallback := lksdk.NewRoomCallback()

	// KEY: Listen for tracks
	roomCallback.ParticipantCallback.OnTrackSubscribed = func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
		log.Printf("[AGENT] Track subscribed: %s", publication.Name())

		// Count packets
		go func() {
			for {
				_, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				count := agentPacketsReceived.Add(1)
				if count == 1 {
					log.Println("[AGENT] ✅ Received first video packet!")
				}
				if count%30 == 0 {
					log.Printf("[AGENT] Received %d packets", count)
				}
			}
		}()
	}

	// KEY: Use manual subscription (WithAutoSubscribe(false))
	room, err := lksdk.ConnectToRoom(serverURL, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: "agent-subscriber",
	}, roomCallback, lksdk.WithAutoSubscribe(false))
	if err != nil {
		done <- err
		return
	}
	defer room.Disconnect()

	// Wait for publisher to be available
	time.Sleep(1 * time.Second)

	// Find publisher participant
	publisherParticipant := room.GetParticipantByIdentity("publisher")

	if publisherParticipant == nil {
		log.Println("[AGENT] Publisher not found")
		done <- fmt.Errorf("publisher not found")
		return
	}

	log.Println("[AGENT] Found publisher, subscribing to video track...")

	// Subscribe to video track
	var videoPublication *lksdk.RemoteTrackPublication
	for _, pub := range publisherParticipant.TrackPublications() {
		if pub.Kind() == lksdk.TrackKindVideo {
			videoPublication = pub.(*lksdk.RemoteTrackPublication)
			break
		}
	}

	if videoPublication == nil {
		log.Println("[AGENT] Video track not found")
		done <- fmt.Errorf("video track not found")
		return
	}

	// KEY: Manually subscribe
	if err := videoPublication.SetSubscribed(true); err != nil {
		log.Printf("[AGENT] Error subscribing: %v", err)
		done <- err
		return
	}

	log.Println("[AGENT] Subscribed to video track")

	// KEY: Immediately request HIGH quality
	// This is where the bug occurs with unpatched server
	if err := videoPublication.SetVideoQuality(livekit.VideoQuality_HIGH); err != nil {
		log.Printf("[AGENT] Error setting video quality: %v", err)
		done <- err
		return
	}

	log.Println("[AGENT] Requested HIGH video quality")
	log.Println("[AGENT] Waiting for video packets...")

	// Wait for packets
	time.Sleep(10 * time.Second)
}
