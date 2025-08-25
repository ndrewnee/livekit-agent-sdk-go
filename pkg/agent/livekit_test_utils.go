package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// TestRoomManager manages LiveKit rooms for testing
type TestRoomManager struct {
	URL       string
	APIKey    string
	APISecret string
	rooms     map[string]*lksdk.Room
}

// NewTestRoomManager creates a new test room manager for local LiveKit server
func NewTestRoomManager() *TestRoomManager {
	return &TestRoomManager{
		URL:       "ws://localhost:7880",
		APIKey:    "devkey",
		APISecret: "secret",
		rooms:     make(map[string]*lksdk.Room),
	}
}

// CreateRoom creates a new test room
func (trm *TestRoomManager) CreateRoom(roomName string) (*lksdk.Room, error) {
	// Create connection info
	connectInfo := lksdk.ConnectInfo{
		APIKey:              trm.APIKey,
		APISecret:           trm.APISecret,
		RoomName:            roomName,
		ParticipantIdentity: "test-participant",
		ParticipantName:     "Test Participant",
	}

	// Create room client
	room, err := lksdk.ConnectToRoom(trm.URL, connectInfo, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(true))

	if err != nil {
		return nil, fmt.Errorf("failed to connect to room: %w", err)
	}

	trm.rooms[roomName] = room
	return room, nil
}

// CleanupRooms disconnects all rooms
func (trm *TestRoomManager) CleanupRooms() {
	for _, room := range trm.rooms {
		room.Disconnect()
	}
	trm.rooms = make(map[string]*lksdk.Room)
}

// PublishSyntheticAudioTrack publishes a synthetic audio track to a room
func (trm *TestRoomManager) PublishSyntheticAudioTrack(room *lksdk.Room, trackID string) (*lksdk.LocalTrackPublication, error) {
	// Create synthetic audio track
	track, err := NewSyntheticAudioTrack(trackID, 48000, 2)
	if err != nil {
		return nil, err
	}

	// Start generating audio
	track.StartGenerating(440.0)

	// Publish to room
	publication, err := room.LocalParticipant.PublishTrack(track.TrackLocalStaticRTP, &lksdk.TrackPublicationOptions{
		Name:   trackID,
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		track.StopGenerating()
		return nil, err
	}

	return publication, nil
}

// WaitForRemoteTrack waits for a remote track to appear
func (trm *TestRoomManager) WaitForRemoteTrack(room *lksdk.Room, trackID string, timeout time.Duration) (*webrtc.TrackRemote, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timeout waiting for track %s", trackID)
		case <-ticker.C:
			for _, participant := range room.GetRemoteParticipants() {
				for _, pub := range participant.TrackPublications() {
					if pub.Name() == trackID && pub.Track() != nil {
						if remoteTrack, ok := pub.Track().(*webrtc.TrackRemote); ok {
							return remoteTrack, nil
						}
					}
				}
			}
		}
	}
}

// CreateConnectedRooms creates two connected rooms with participants
func (trm *TestRoomManager) CreateConnectedRooms(roomName string) (*lksdk.Room, *lksdk.Room, error) {
	// Create first room (publisher)
	publisherRoom, err := trm.CreateRoom(roomName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create publisher room: %w", err)
	}

	// Create second room (subscriber) with different identity
	subscriberInfo := lksdk.ConnectInfo{
		APIKey:              trm.APIKey,
		APISecret:           trm.APISecret,
		RoomName:            roomName,
		ParticipantIdentity: "test-subscriber",
		ParticipantName:     "Test Subscriber",
	}

	subscriberRoom, err := lksdk.ConnectToRoom(trm.URL, subscriberInfo, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(true))

	if err != nil {
		publisherRoom.Disconnect()
		return nil, nil, fmt.Errorf("failed to create subscriber room: %w", err)
	}

	// Room is now connected

	trm.rooms[roomName+"-pub"] = publisherRoom
	trm.rooms[roomName+"-sub"] = subscriberRoom

	return publisherRoom, subscriberRoom, nil
}

// TestLiveKitConnection tests if we can connect to the local LiveKit server
func TestLiveKitConnection() error {
	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	room, err := manager.CreateRoom("test-connection")
	if err != nil {
		return fmt.Errorf("failed to connect to LiveKit server: %w", err)
	}

	// Successfully connected
	room.Disconnect()
	return nil
}
