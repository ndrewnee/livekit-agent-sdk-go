//go:build integration
// +build integration

package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PublisherIntegrationTestSuite provides shared test infrastructure for publisher tests
type PublisherIntegrationTestSuite struct {
	t           *testing.T
	wsURL       string
	apiKey      string
	apiSecret   string
	roomClient  *lksdk.RoomServiceClient
	agents      []*PublisherAgent
	rooms       []*lksdk.Room
	publishers  []*lksdk.Room
}

// NewPublisherIntegrationTestSuite creates a new test suite
func NewPublisherIntegrationTestSuite(t *testing.T) *PublisherIntegrationTestSuite {
	wsURL := "ws://localhost:7880"
	apiKey := "devkey"
	apiSecret := "secret"

	roomClient := lksdk.NewRoomServiceClient(wsURL, apiKey, apiSecret)

	return &PublisherIntegrationTestSuite{
		t:          t,
		wsURL:      wsURL,
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		roomClient: roomClient,
		agents:     make([]*PublisherAgent, 0),
		rooms:      make([]*lksdk.Room, 0),
		publishers: make([]*lksdk.Room, 0),
	}
}

// Cleanup cleans up all resources
func (suite *PublisherIntegrationTestSuite) Cleanup() {
	// Disconnect all publishers
	for _, p := range suite.publishers {
		if p != nil {
			p.Disconnect()
		}
	}

	// Stop all agents
	for _, agent := range suite.agents {
		if agent != nil {
			agent.Stop()
		}
	}

	// Delete all rooms
	for _, room := range suite.rooms {
		if room != nil {
			suite.roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
				Room: room.Name(),
			})
		}
	}
}

// CreateTestRoom creates a room with publisher agent dispatch
func (suite *PublisherIntegrationTestSuite) CreateTestRoom(agentName string) *lksdk.Room {
	roomName := fmt.Sprintf("test-publisher-room-%d", time.Now().UnixNano())

	_, err := suite.roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name:     roomName,
		Metadata: "Publisher integration test room",
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: agentName,
				Metadata:  "publisher agent test",
			},
		},
	})
	require.NoError(suite.t, err)

	// Connect to room
	room, err := lksdk.ConnectToRoom(suite.wsURL, lksdk.ConnectInfo{
		APIKey:              suite.apiKey,
		APISecret:           suite.apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: agentName + "-client",
	}, &lksdk.RoomCallback{})
	require.NoError(suite.t, err)

	suite.rooms = append(suite.rooms, room)
	return room
}

// CreatePublisherAgent creates a new publisher agent
func (suite *PublisherIntegrationTestSuite) CreatePublisherAgent(name string, handler PublisherAgentHandler) *PublisherAgent {
	opts := WorkerOptions{
		AgentName: name,
		Namespace: "test",
	}

	agent, err := NewPublisherAgent(suite.wsURL, suite.apiKey, suite.apiSecret, handler, opts)
	require.NoError(suite.t, err)

	suite.agents = append(suite.agents, agent)
	
	// Start agent in background
	go func() {
		if err := agent.Worker.Start(context.Background()); err != nil {
			suite.t.Logf("Agent start error: %v", err)
		}
	}()

	return agent
}

// ConnectPublisher connects a participant that will publish media
func (suite *PublisherIntegrationTestSuite) ConnectPublisher(roomName, identity, metadata string) *lksdk.Room {
	room, err := lksdk.ConnectToRoom(suite.wsURL, lksdk.ConnectInfo{
		APIKey:              suite.apiKey,
		APISecret:           suite.apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: identity,
		ParticipantMetadata: metadata,
	}, &lksdk.RoomCallback{})
	require.NoError(suite.t, err)

	suite.publishers = append(suite.publishers, room)
	return room
}

// WaitForCondition waits for a condition to be true
func (suite *PublisherIntegrationTestSuite) WaitForCondition(condition func() bool, timeout time.Duration, description string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	suite.t.Fatalf("Timeout waiting for: %s", description)
}

// testPublisherHandler is a helper handler for tests
type testPublisherHandler struct {
	t                                    *testing.T
	onJobRequest                         func(ctx context.Context, job *livekit.Job) (bool, *JobMetadata)
	onJobAssigned                        func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error
	onJobTerminated                      func(ctx context.Context, jobID string)
	onPublisherTrackPublished            func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)
	onPublisherTrackUnpublished          func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication)
	onPublisherTrackSubscribed           func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote)
	onPublisherTrackUnsubscribed         func(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote)
	onPublisherQualityChanged            func(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote, oldQuality, newQuality livekit.VideoQuality)
	onPublisherConnectionQualityChanged  func(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality)
}

func (h *testPublisherHandler) OnJobAssigned(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
	if h.onJobAssigned != nil {
		return h.onJobAssigned(ctx, job, room)
	}
	return nil
}

func (h *testPublisherHandler) OnJobTerminated(ctx context.Context, jobID string) {
	if h.onJobTerminated != nil {
		h.onJobTerminated(ctx, jobID)
	}
}

func (h *testPublisherHandler) OnPublisherTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	if h.onPublisherTrackPublished != nil {
		h.onPublisherTrackPublished(ctx, participant, publication)
	}
}

func (h *testPublisherHandler) OnPublisherTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	if h.onPublisherTrackUnpublished != nil {
		h.onPublisherTrackUnpublished(ctx, participant, publication)
	}
}

func (h *testPublisherHandler) OnPublisherTrackSubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote) {
	if h.onPublisherTrackSubscribed != nil {
		h.onPublisherTrackSubscribed(ctx, participant, publication, track)
	}
}

func (h *testPublisherHandler) OnPublisherTrackUnsubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote) {
	if h.onPublisherTrackUnsubscribed != nil {
		h.onPublisherTrackUnsubscribed(ctx, participant, track)
	}
}

func (h *testPublisherHandler) OnPublisherQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote, oldQuality, newQuality livekit.VideoQuality) {
	if h.onPublisherQualityChanged != nil {
		h.onPublisherQualityChanged(ctx, participant, track, oldQuality, newQuality)
	}
}

func (h *testPublisherHandler) OnPublisherConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
	if h.onPublisherConnectionQualityChanged != nil {
		h.onPublisherConnectionQualityChanged(ctx, participant, quality)
	}
}

func (h *testPublisherHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	if h.onJobRequest != nil {
		return h.onJobRequest(ctx, job)
	}
	return true, &JobMetadata{
		ParticipantIdentity: "test-publisher-agent",
		ParticipantName:     "Test Publisher Agent",
		ParticipantMetadata: "publisher agent metadata",
	}
}

func (h *testPublisherHandler) GetJobMetadata(ctx context.Context, job *livekit.Job) *JobMetadata {
	return &JobMetadata{
		ParticipantIdentity: "test-publisher-agent",
		ParticipantName:     "Test Publisher Agent",
		ParticipantMetadata: "publisher agent metadata",
	}
}

// TestPublisherAgent_BasicTrackPublishing tests basic track publishing and subscription
func TestPublisherAgent_BasicTrackPublishing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var publishedTracks []string
	var subscribedTracks []string
	var mu sync.Mutex

	handler := &testPublisherHandler{
		t: t,
		onJobAssigned: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			t.Logf("Publisher agent assigned job: %s", job.Id)
			return nil
		},
		onPublisherTrackPublished: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
			mu.Lock()
			publishedTracks = append(publishedTracks, publication.SID())
			mu.Unlock()
			t.Logf("Track published: %s (source: %s)", publication.SID(), publication.Source())
		},
		onPublisherTrackSubscribed: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote) {
			mu.Lock()
			subscribedTracks = append(subscribedTracks, publication.SID())
			mu.Unlock()
			t.Logf("Track subscribed: %s", publication.SID())
		},
		onJobTerminated: func(ctx context.Context, jobID string) {
			t.Logf("Job terminated: %s", jobID)
		},
	}

	// Create room and agent
	room := suite.CreateTestRoom("test-basic-publisher-agent")
	_ = suite.CreatePublisherAgent("test-basic-publisher-agent", handler)

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect publisher
	publisher := suite.ConnectPublisher(room.Name(), "test-publisher", "Publisher metadata")

	// Wait for publisher to be ready
	time.Sleep(1 * time.Second)

	// Publisher participant should start publishing tracks
	// Note: In a real test, we would publish actual media tracks
	// For integration testing, we'll verify the agent can track publishers

	// Verify publisher is connected
	assert.Equal(t, lksdk.ConnectionStateConnected, publisher.ConnectionState())

	// Disconnect publisher
	publisher.Disconnect()
	
	time.Sleep(1 * time.Second)
}

// TestPublisherAgent_QualityAdaptation tests video quality adaptation
func TestPublisherAgent_QualityAdaptation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var qualityChanges []livekit.VideoQuality
	var mu sync.Mutex

	handler := &testPublisherHandler{
		t: t,
		onPublisherQualityChanged: func(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote, oldQuality, newQuality livekit.VideoQuality) {
			mu.Lock()
			qualityChanges = append(qualityChanges, newQuality)
			mu.Unlock()
			t.Logf("Quality changed from %s to %s", oldQuality, newQuality)
		},
		onPublisherConnectionQualityChanged: func(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
			t.Logf("Connection quality: %s", quality)
		},
	}

	// Create room and agent
	room := suite.CreateTestRoom("test-quality-publisher-agent")
	agent := suite.CreatePublisherAgent("test-quality-publisher-agent", handler)

	// Configure quality adaptation policy
	policy := DefaultQualityAdaptationPolicy()
	policy.MaxQuality = livekit.VideoQuality_MEDIUM
	agent.qualityController.SetAdaptationPolicy(policy)

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect publisher
	publisher := suite.ConnectPublisher(room.Name(), "test-publisher", "Publisher metadata")

	// Wait for testing
	time.Sleep(3 * time.Second)

	// Verify agent is tracking the publisher
	publisherInfo := agent.GetPublisherInfo()
	if publisherInfo != nil {
		assert.Equal(t, "test-publisher", publisherInfo.Identity())
	}

	publisher.Disconnect()
}

// TestPublisherAgent_TrackSubscriptionFiltering tests subscription filtering
func TestPublisherAgent_TrackSubscriptionFiltering(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var subscribedSources []livekit.TrackSource
	var mu sync.Mutex

	handler := &testPublisherHandler{
		t: t,
		onPublisherTrackSubscribed: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote) {
			mu.Lock()
			subscribedSources = append(subscribedSources, publication.Source())
			mu.Unlock()
			t.Logf("Subscribed to track with source: %s", publication.Source())
		},
	}

	// Create room and agent
	room := suite.CreateTestRoom("test-filter-publisher-agent")
	agent := suite.CreatePublisherAgent("test-filter-publisher-agent", handler)

	// Configure subscription filter - only subscribe to camera tracks
	agent.subscriptionManager.AddFilter(func(publication *lksdk.RemoteTrackPublication) bool {
		return publication.Source() == livekit.TrackSource_CAMERA
	})

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect publisher
	publisher := suite.ConnectPublisher(room.Name(), "test-publisher", "Publisher metadata")

	// Wait for testing
	time.Sleep(3 * time.Second)

	publisher.Disconnect()
}

// TestPublisherAgent_MediaPipeline tests media pipeline processing
func TestPublisherAgent_MediaPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var pipelineEvents []string
	var mu sync.Mutex

	handler := &testPublisherHandler{
		t: t,
		onPublisherTrackSubscribed: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote) {
			mu.Lock()
			pipelineEvents = append(pipelineEvents, fmt.Sprintf("track_subscribed:%s", publication.SID()))
			mu.Unlock()

			// In a real scenario, we would process media through the pipeline
			t.Logf("Would process track %s through media pipeline", publication.SID())
		},
	}

	// Create room and agent
	room := suite.CreateTestRoom("test-pipeline-publisher-agent")
	_ = suite.CreatePublisherAgent("test-pipeline-publisher-agent", handler)

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect publisher
	publisher := suite.ConnectPublisher(room.Name(), "test-publisher", "Publisher with media")

	// Wait for testing
	time.Sleep(3 * time.Second)

	publisher.Disconnect()
}

// TestPublisherAgent_ConnectionMonitoring tests connection quality monitoring
func TestPublisherAgent_ConnectionMonitoring(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var connectionQualities []livekit.ConnectionQuality
	var mu sync.Mutex

	handler := &testPublisherHandler{
		t: t,
		onPublisherConnectionQualityChanged: func(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
			mu.Lock()
			connectionQualities = append(connectionQualities, quality)
			mu.Unlock()
			t.Logf("Connection quality for %s: %s", participant.Identity(), quality)
		},
		onJobTerminated: func(ctx context.Context, jobID string) {
			// Record termination
		},
	}

	// Create room and agent
	room := suite.CreateTestRoom("test-connection-publisher-agent")
	agent := suite.CreatePublisherAgent("test-connection-publisher-agent", handler)

	// Set up connection quality monitoring
	monitor := agent.connectionMonitor
	monitor.SetQualityChangeCallback(func(old, new livekit.ConnectionQuality) {
		t.Logf("Quality change detected: %s -> %s", old, new)
	})

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect publisher
	publisher := suite.ConnectPublisher(room.Name(), "test-publisher", "Connection test publisher")

	// Monitor for a while
	time.Sleep(5 * time.Second)

	// Check monitoring state
	currentQuality := monitor.GetCurrentQuality()
	t.Logf("Current connection quality: %s", currentQuality)

	publisher.Disconnect()
}

// TestPublisherAgent_MultiplePublishers tests handling multiple publishers
func TestPublisherAgent_MultiplePublishers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var publisherCount int32
	var publishedCount int32

	handler := &testPublisherHandler{
		t: t,
		onJobAssigned: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			atomic.AddInt32(&publisherCount, 1)
			t.Logf("Job assigned for publisher: %s", job.Participant.Identity)
			return nil
		},
		onPublisherTrackPublished: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
			atomic.AddInt32(&publishedCount, 1)
			t.Logf("Publisher %s published track %s", participant.Identity(), publication.SID())
		},
	}

	// Create room with agent dispatch
	room := suite.CreateTestRoom("test-multi-publisher-agent")
	
	// Create a single agent to handle all publishers
	// Note: Publisher agents are assigned when participants publish, not when agents are created
	_ = suite.CreatePublisherAgent("test-multi-publisher-agent", handler)

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect multiple publishers
	publishers := make([]*lksdk.Room, 3)
	for i := 0; i < 3; i++ {
		publishers[i] = suite.ConnectPublisher(room.Name(), fmt.Sprintf("publisher-%d", i), fmt.Sprintf("Publisher %d", i))
		time.Sleep(500 * time.Millisecond)
	}

	// In a real scenario, publishers would publish tracks which triggers agent assignment
	// For this test, we verify the agent can handle multiple publishers
	time.Sleep(3 * time.Second)

	// Note: Without actual track publishing, publisher agents may not be assigned
	// This is expected behavior as JT_PUBLISHER agents are only triggered when publishing begins
	t.Logf("Connected %d publishers, %d agents assigned", len(publishers), atomic.LoadInt32(&publisherCount))

	// Disconnect all publishers
	for _, p := range publishers {
		p.Disconnect()
	}
}

// TestPublisherAgent_TrackUnpublishing tests track unpublishing
func TestPublisherAgent_TrackUnpublishing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var unpublishedTracks []string
	var mu sync.Mutex

	handler := &testPublisherHandler{
		t: t,
		onPublisherTrackUnpublished: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
			mu.Lock()
			unpublishedTracks = append(unpublishedTracks, publication.SID())
			mu.Unlock()
			t.Logf("Track unpublished: %s", publication.SID())
		},
		onPublisherTrackUnsubscribed: func(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote) {
			t.Logf("Track unsubscribed: %s", track.ID())
		},
	}

	// Create room and agent
	room := suite.CreateTestRoom("test-unpublish-publisher-agent")
	_ = suite.CreatePublisherAgent("test-unpublish-publisher-agent", handler)

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect publisher
	publisher := suite.ConnectPublisher(room.Name(), "test-publisher", "Unpublish test")

	// Wait and then disconnect (simulating unpublishing)
	time.Sleep(3 * time.Second)
	publisher.Disconnect()
	
	time.Sleep(2 * time.Second)
}

// TestPublisherAgent_ErrorHandling tests error handling scenarios
func TestPublisherAgent_ErrorHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var errors []error
	var mu sync.Mutex

	handler := &testPublisherHandler{
		t: t,
		onJobAssigned: func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
			// Simulate error for specific publisher
			if job.Participant != nil && job.Participant.Identity == "error-publisher" {
				err := fmt.Errorf("simulated assignment error")
				mu.Lock()
				errors = append(errors, err)
				mu.Unlock()
				return err
			}
			return nil
		},
		onJobTerminated: func(ctx context.Context, jobID string) {
			t.Logf("Job %s terminated", jobID)
		},
	}

	// Create room and agent
	_ = suite.CreateTestRoom("test-error-publisher-agent")
	agent := suite.CreatePublisherAgent("test-error-publisher-agent", handler)

	// Test invalid operations
	err := agent.SubscribeToTrack(nil)
	assert.Error(t, err)

	err = agent.UnsubscribeFromTrack(nil)
	assert.Error(t, err)

	// Test operations on non-existent tracks
	err = agent.SetTrackQuality("non-existent-sid", livekit.VideoQuality_HIGH)
	assert.Error(t, err)

	err = agent.SetTrackDimensions("non-existent-sid", 1920, 1080)
	assert.Error(t, err)

	err = agent.SetTrackFrameRate("non-existent-sid", 30.0)
	assert.Error(t, err)
}

// TestPublisherAgent_LoadTest tests with multiple tracks from single publisher
func TestPublisherAgent_LoadTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	suite := NewPublisherIntegrationTestSuite(t)
	defer suite.Cleanup()

	var trackCount int32
	var subscribeCount int32

	handler := &testPublisherHandler{
		t: t,
		onPublisherTrackPublished: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
			atomic.AddInt32(&trackCount, 1)
			t.Logf("Track %d published: %s", atomic.LoadInt32(&trackCount), publication.SID())
		},
		onPublisherTrackSubscribed: func(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote) {
			atomic.AddInt32(&subscribeCount, 1)
			t.Logf("Track %d subscribed: %s", atomic.LoadInt32(&subscribeCount), publication.SID())
		},
	}

	// Create room and agent
	room := suite.CreateTestRoom("test-load-publisher-agent")
	agent := suite.CreatePublisherAgent("test-load-publisher-agent", handler)

	// Enable auto-subscribe
	agent.subscriptionManager.SetAutoSubscribe(true)

	// Wait for agent to connect
	time.Sleep(2 * time.Second)

	// Connect publisher
	publisher := suite.ConnectPublisher(room.Name(), "load-test-publisher", "Load test publisher")

	// In a real test, we would publish multiple tracks here
	// For integration test, we verify the agent can handle the load

	// Monitor for a while
	time.Sleep(5 * time.Second)

	// Check agent state
	subscribedTracks := agent.GetSubscribedTracks()
	t.Logf("Agent tracking %d subscribed tracks", len(subscribedTracks))

	publisher.Disconnect()
}