package agent

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

// MockRoom implements a mock LiveKit Room for testing
type MockRoom struct {
	mu                   sync.RWMutex
	sid                  string
	name                 string
	localParticipant     *lksdk.LocalParticipant
	remoteParticipants   map[string]*MockRemoteParticipant
	metadata             string
	connectionState      lksdk.ConnectionState
	dataReceivedCallback func(data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind)
	callbacks            *lksdk.RoomCallback
}

func NewMockRoom() *MockRoom {
	return &MockRoom{
		sid:                "mock-room-sid",
		name:               "mock-room",
		remoteParticipants: make(map[string]*MockRemoteParticipant),
		connectionState:    lksdk.ConnectionStateConnected,
	}
}

func (r *MockRoom) SID() string {
	return r.sid
}

func (r *MockRoom) Name() string {
	return r.name
}

func (r *MockRoom) LocalParticipant() *lksdk.LocalParticipant {
	return r.localParticipant
}

func (r *MockRoom) GetRemoteParticipants() []*lksdk.RemoteParticipant {
	r.mu.RLock()
	defer r.mu.RUnlock()

	participants := make([]*lksdk.RemoteParticipant, 0, len(r.remoteParticipants))
	for _, p := range r.remoteParticipants {
		participants = append(participants, p.ToRemoteParticipant())
	}
	return participants
}

func (r *MockRoom) GetRemoteParticipant(identity string) *lksdk.RemoteParticipant {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if p, ok := r.remoteParticipants[identity]; ok {
		return p.ToRemoteParticipant()
	}
	return nil
}

func (r *MockRoom) Disconnect() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connectionState = lksdk.ConnectionStateDisconnected
}

func (r *MockRoom) PublishData(data []byte, opts ...lksdk.DataPublishOption) error {
	// Mock implementation
	return nil
}

func (r *MockRoom) PublishDataPacket(packet *livekit.DataPacket, opts ...lksdk.DataPublishOption) error {
	// Mock implementation
	return nil
}

func (r *MockRoom) UpdateMetadata(metadata string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metadata = metadata
}

func (r *MockRoom) GetMetadata() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.metadata
}

func (r *MockRoom) ConnectionState() lksdk.ConnectionState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connectionState
}

func (r *MockRoom) OnDataReceived(callback func(data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dataReceivedCallback = callback
}

// Helper method to add a mock participant
func (r *MockRoom) AddMockParticipant(identity string) *MockRemoteParticipant {
	r.mu.Lock()
	defer r.mu.Unlock()

	participant := &MockRemoteParticipant{
		identity: identity,
		sid:      "participant-" + identity,
		metadata: "",
		tracks:   make(map[string]*lksdk.RemoteTrackPublication),
	}
	r.remoteParticipants[identity] = participant
	return participant
}

// MockRemoteParticipant implements a mock RemoteParticipant
type MockRemoteParticipant struct {
	mu       sync.RWMutex
	identity string
	sid      string
	metadata string
	tracks   map[string]*lksdk.RemoteTrackPublication
}

func (p *MockRemoteParticipant) ToRemoteParticipant() *lksdk.RemoteParticipant {
	// This is a simplified mock - in real implementation would need to return actual RemoteParticipant
	return nil
}

func (p *MockRemoteParticipant) Identity() string {
	return p.identity
}

func (p *MockRemoteParticipant) SID() string {
	return p.sid
}

func (p *MockRemoteParticipant) GetMetadata() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.metadata
}

func (p *MockRemoteParticipant) SetMetadata(metadata string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.metadata = metadata
}

// MockWebSocketConn implements a mock WebSocket connection
type MockWebSocketConn struct {
	mu            sync.Mutex
	messages      [][]byte
	closed        bool
	closeErr      error
	readDeadline  time.Time
	writeDeadline time.Time
	responses     map[string][]byte // Map of request -> response for testing
}

func NewMockWebSocketConn() *MockWebSocketConn {
	return &MockWebSocketConn{
		messages:  make([][]byte, 0),
		responses: make(map[string][]byte),
	}
}

func (c *MockWebSocketConn) ReadMessage() (messageType int, p []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return 0, nil, websocket.ErrCloseSent
	}

	if len(c.messages) > 0 {
		msg := c.messages[0]
		c.messages = c.messages[1:]
		return websocket.TextMessage, msg, nil
	}

	// Simulate blocking read
	time.Sleep(10 * time.Millisecond)
	return 0, nil, nil
}

func (c *MockWebSocketConn) WriteMessage(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return websocket.ErrCloseSent
	}

	// Store the message for verification
	c.messages = append(c.messages, data)
	return nil
}

func (c *MockWebSocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	return c.closeErr
}

func (c *MockWebSocketConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	return nil
}

func (c *MockWebSocketConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	return nil
}

func (c *MockWebSocketConn) SetCloseError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeErr = err
}

func (c *MockWebSocketConn) AddResponse(request string, response []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responses[request] = response
}

func (c *MockWebSocketConn) SimulateMessage(msg []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, msg)
}

// MockUniversalHandler implements UniversalHandler for testing
type MockUniversalHandler struct {
	mu                   sync.Mutex
	jobRequests          []*livekit.Job
	jobAssignments       []*JobContext
	jobTerminations      []string
	roomConnections      []*lksdk.Room
	roomDisconnections   []*lksdk.Room
	participantJoins     []*lksdk.RemoteParticipant
	participantLeaves    []*lksdk.RemoteParticipant
	trackPublications    []*lksdk.RemoteTrackPublication
	trackUnpublications  []*lksdk.RemoteTrackPublication
	trackSubscriptions   []*webrtc.TrackRemote
	trackUnsubscriptions []*webrtc.TrackRemote
	dataReceived         [][]byte
	errors               []error

	// Configurable responses
	acceptJob   bool
	jobMetadata *JobMetadata
	assignError error
}

func NewMockUniversalHandler() *MockUniversalHandler {
	return &MockUniversalHandler{
		acceptJob:            true,
		jobRequests:          make([]*livekit.Job, 0),
		jobAssignments:       make([]*JobContext, 0),
		jobTerminations:      make([]string, 0),
		roomConnections:      make([]*lksdk.Room, 0),
		roomDisconnections:   make([]*lksdk.Room, 0),
		participantJoins:     make([]*lksdk.RemoteParticipant, 0),
		participantLeaves:    make([]*lksdk.RemoteParticipant, 0),
		trackPublications:    make([]*lksdk.RemoteTrackPublication, 0),
		trackUnpublications:  make([]*lksdk.RemoteTrackPublication, 0),
		trackSubscriptions:   make([]*webrtc.TrackRemote, 0),
		trackUnsubscriptions: make([]*webrtc.TrackRemote, 0),
		dataReceived:         make([][]byte, 0),
		errors:               make([]error, 0),
	}
}

func (h *MockUniversalHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *JobMetadata) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.jobRequests = append(h.jobRequests, job)
	return h.acceptJob, h.jobMetadata
}

func (h *MockUniversalHandler) OnJobAssigned(ctx context.Context, jobCtx *JobContext) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.jobAssignments = append(h.jobAssignments, jobCtx)
	return h.assignError
}

func (h *MockUniversalHandler) OnJobTerminated(ctx context.Context, jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.jobTerminations = append(h.jobTerminations, jobID)
}

func (h *MockUniversalHandler) OnRoomConnected(ctx context.Context, room *lksdk.Room) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.roomConnections = append(h.roomConnections, room)
}

func (h *MockUniversalHandler) OnRoomDisconnected(ctx context.Context, room *lksdk.Room, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.roomDisconnections = append(h.roomDisconnections, room)
}

func (h *MockUniversalHandler) OnRoomMetadataChanged(ctx context.Context, oldMetadata, newMetadata string) {
	// Mock implementation
}

func (h *MockUniversalHandler) OnParticipantJoined(ctx context.Context, participant *lksdk.RemoteParticipant) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.participantJoins = append(h.participantJoins, participant)
}

func (h *MockUniversalHandler) OnParticipantLeft(ctx context.Context, participant *lksdk.RemoteParticipant) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.participantLeaves = append(h.participantLeaves, participant)
}

func (h *MockUniversalHandler) OnParticipantMetadataChanged(ctx context.Context, participant *lksdk.RemoteParticipant, oldMetadata string) {
	// Mock implementation
}

func (h *MockUniversalHandler) OnParticipantSpeakingChanged(ctx context.Context, participant *lksdk.RemoteParticipant, speaking bool) {
	// Mock implementation
}

func (h *MockUniversalHandler) OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.trackPublications = append(h.trackPublications, publication)
}

func (h *MockUniversalHandler) OnTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.trackUnpublications = append(h.trackUnpublications, publication)
}

func (h *MockUniversalHandler) OnTrackSubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.trackSubscriptions = append(h.trackSubscriptions, track)
}

func (h *MockUniversalHandler) OnTrackUnsubscribed(ctx context.Context, track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.trackUnsubscriptions = append(h.trackUnsubscriptions, track)
}

func (h *MockUniversalHandler) OnTrackMuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {
	// Mock implementation
}

func (h *MockUniversalHandler) OnTrackUnmuted(ctx context.Context, publication lksdk.TrackPublication, participant lksdk.Participant) {
	// Mock implementation
}

func (h *MockUniversalHandler) OnDataReceived(ctx context.Context, data []byte, participant *lksdk.RemoteParticipant, kind livekit.DataPacket_Kind) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.dataReceived = append(h.dataReceived, data)
}

func (h *MockUniversalHandler) OnConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
	// Mock implementation
}

func (h *MockUniversalHandler) OnActiveSpeakersChanged(ctx context.Context, speakers []lksdk.Participant) {
	// Mock implementation
}

// Helper methods for verification
func (h *MockUniversalHandler) GetJobRequests() []*livekit.Job {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]*livekit.Job{}, h.jobRequests...)
}

func (h *MockUniversalHandler) GetJobAssignments() []*JobContext {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]*JobContext{}, h.jobAssignments...)
}

func (h *MockUniversalHandler) GetJobTerminations() []string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]string{}, h.jobTerminations...)
}

func (h *MockUniversalHandler) SetAcceptJob(accept bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.acceptJob = accept
}

func (h *MockUniversalHandler) SetJobMetadata(metadata *JobMetadata) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobMetadata = metadata
}

func (h *MockUniversalHandler) SetAssignError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.assignError = err
}
