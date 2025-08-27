package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWebRTCRoutingStageCreation tests the creation of WebRTC routing stage
func TestWebRTCRoutingStageCreation(t *testing.T) {
	stunServers := []string{"stun:stun.l.google.com:19302"}
	turnServers := []webrtc.ICEServer{
		{
			URLs:       []string{"turn:turnserver.com:3478"},
			Username:   "user",
			Credential: "pass",
		},
	}

	stage := NewWebRTCRoutingStage("test-router", 10, stunServers, turnServers)

	assert.NotNil(t, stage)
	assert.Equal(t, "test-router", stage.GetName())
	assert.Equal(t, 10, stage.GetPriority())
	assert.True(t, stage.CanProcess(MediaTypeAudio))
	assert.True(t, stage.CanProcess(MediaTypeVideo))
	assert.NotNil(t, stage.connections)
	assert.NotNil(t, stage.routingTable)
	assert.NotNil(t, stage.stats)

	// Check ICE configuration
	assert.Len(t, stage.config.ICEServers, 2) // 1 STUN + 1 TURN
}

// TestWebRTCRoutingStageProcess tests the Process method
func TestWebRTCRoutingStageProcess(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, nil, nil)

	ctx := context.Background()
	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("test-data"),
		Format: MediaFormat{
			SampleRate: 48000,
			Channels:   2,
		},
		Metadata: make(map[string]interface{}),
	}

	// Add RTP header to metadata
	rtpHeader := &rtp.Header{
		Version:        2,
		PayloadType:    111,
		SequenceNumber: 1234,
		Timestamp:      90000,
		SSRC:           12345678,
	}
	input.Metadata["rtp_header"] = rtpHeader

	output, err := stage.Process(ctx, input)

	assert.NoError(t, err)
	assert.Equal(t, input.TrackID, output.TrackID)
	assert.Equal(t, input.Data, output.Data)
	assert.True(t, output.Metadata["webrtc_routed"].(bool))
	assert.NotNil(t, output.Metadata["routed_at"])
}

// TestWebRTCCreateReceiver tests creating a new receiver connection
func TestWebRTCCreateReceiver(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, []string{"stun:stun.l.google.com:19302"}, nil)
	ctx := context.Background()

	// Test creating receiver without offer (sender-initiated)
	answer, err := stage.CreateReceiver(ctx, "receiver-1", nil)

	assert.NoError(t, err)
	assert.Nil(t, answer) // No answer when no offer provided

	// Verify connection was created
	stage.mu.RLock()
	conn, exists := stage.connections["receiver-1"]
	stage.mu.RUnlock()

	assert.True(t, exists)
	assert.NotNil(t, conn)
	assert.Equal(t, "receiver-1", conn.ID)
	assert.NotNil(t, conn.PeerConnection)
	assert.NotNil(t, conn.DataChannel)
	assert.NotNil(t, conn.localTracks)
	assert.NotNil(t, conn.packetChan)
	assert.NotNil(t, conn.closeChan)

	// Test duplicate receiver
	_, err = stage.CreateReceiver(ctx, "receiver-1", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already connected")

	// Cleanup
	stage.RemoveReceiver("receiver-1")
}

// TestWebRTCCreateReceiverWithOffer tests creating receiver with SDP offer
func TestWebRTCCreateReceiverWithOffer(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, []string{"stun:stun.l.google.com:19302"}, nil)
	ctx := context.Background()

	// Create a peer connection to generate an offer
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
	peerConn, err := webrtc.NewPeerConnection(config)
	require.NoError(t, err)
	defer peerConn.Close()

	// Add a data channel to force offer generation
	_, err = peerConn.CreateDataChannel("test", nil)
	require.NoError(t, err)

	// Create offer
	offer, err := peerConn.CreateOffer(nil)
	require.NoError(t, err)

	// Create receiver with offer
	answer, err := stage.CreateReceiver(ctx, "receiver-2", &offer)

	assert.NoError(t, err)
	assert.NotNil(t, answer)
	assert.Equal(t, webrtc.SDPTypeAnswer, answer.Type)
	assert.NotEmpty(t, answer.SDP)

	// Cleanup
	stage.RemoveReceiver("receiver-2")
}

// TestWebRTCAddTrackToReceiver tests adding tracks to receivers
func TestWebRTCAddTrackToReceiver(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, []string{"stun:stun.l.google.com:19302"}, nil)
	ctx := context.Background()

	// Create receiver first
	_, err := stage.CreateReceiver(ctx, "receiver-1", nil)
	require.NoError(t, err)

	// Add audio track
	err = stage.AddTrackToReceiver("receiver-1", "audio-track-1", webrtc.RTPCodecTypeAudio)
	assert.NoError(t, err)

	// Verify track was added
	stage.mu.RLock()
	conn := stage.connections["receiver-1"]
	localTrack, exists := conn.localTracks["audio-track-1"]
	routingEntry := stage.routingTable["audio-track-1"]
	stage.mu.RUnlock()

	assert.True(t, exists)
	assert.NotNil(t, localTrack)
	assert.Contains(t, routingEntry, "receiver-1")

	// Test adding duplicate track
	err = stage.AddTrackToReceiver("receiver-1", "audio-track-1", webrtc.RTPCodecTypeAudio)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already added")

	// Add video track
	err = stage.AddTrackToReceiver("receiver-1", "video-track-1", webrtc.RTPCodecTypeVideo)
	assert.NoError(t, err)

	// Test adding track to non-existent receiver
	err = stage.AddTrackToReceiver("non-existent", "track-1", webrtc.RTPCodecTypeAudio)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Cleanup
	stage.RemoveReceiver("receiver-1")
}

// TestWebRTCRemoveReceiver tests removing receiver connections
func TestWebRTCRemoveReceiver(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, []string{"stun:stun.l.google.com:19302"}, nil)
	ctx := context.Background()

	// Create receiver with tracks
	_, err := stage.CreateReceiver(ctx, "receiver-1", nil)
	require.NoError(t, err)

	err = stage.AddTrackToReceiver("receiver-1", "track-1", webrtc.RTPCodecTypeAudio)
	require.NoError(t, err)

	// Remove receiver
	err = stage.RemoveReceiver("receiver-1")
	assert.NoError(t, err)

	// Verify receiver was removed
	stage.mu.RLock()
	_, exists := stage.connections["receiver-1"]
	routingEntry := stage.routingTable["track-1"]
	stage.mu.RUnlock()

	assert.False(t, exists)
	assert.Empty(t, routingEntry)

	// Test removing non-existent receiver
	err = stage.RemoveReceiver("non-existent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestWebRTCPacketRouting tests packet routing to multiple receivers
func TestWebRTCPacketRouting(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, []string{"stun:stun.l.google.com:19302"}, nil)
	ctx := context.Background()

	// Create multiple receivers
	_, err := stage.CreateReceiver(ctx, "receiver-1", nil)
	require.NoError(t, err)
	_, err = stage.CreateReceiver(ctx, "receiver-2", nil)
	require.NoError(t, err)

	// Add same track to both receivers
	err = stage.AddTrackToReceiver("receiver-1", "shared-track", webrtc.RTPCodecTypeAudio)
	require.NoError(t, err)
	err = stage.AddTrackToReceiver("receiver-2", "shared-track", webrtc.RTPCodecTypeAudio)
	require.NoError(t, err)

	// Create test packet
	packet := &WebRTCPacket{
		TrackID:   "shared-track",
		Timestamp: time.Now(),
		Data:      []byte("test-packet-data"),
		RTPHeader: &rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: 5678,
			Timestamp:      180000,
			SSRC:           87654321,
		},
	}

	// Route packet
	stage.routePacket(packet)

	// Give some time for async routing
	time.Sleep(10 * time.Millisecond)

	// Verify routing table has both receivers
	stage.mu.RLock()
	receivers := stage.routingTable["shared-track"]
	stage.mu.RUnlock()

	assert.Len(t, receivers, 2)
	assert.Contains(t, receivers, "receiver-1")
	assert.Contains(t, receivers, "receiver-2")

	// Cleanup
	stage.RemoveReceiver("receiver-1")
	stage.RemoveReceiver("receiver-2")
}

// TestWebRTCStatistics tests statistics collection
func TestWebRTCStatistics(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, nil, nil)
	ctx := context.Background()

	// Initial stats should be zero
	stats := stage.GetRoutingStats()
	assert.Equal(t, uint64(0), stats.TotalConnections)
	assert.Equal(t, uint64(0), stats.ActiveConnections)
	assert.Equal(t, uint64(0), stats.PacketsRouted)

	// Create receiver
	_, err := stage.CreateReceiver(ctx, "receiver-1", nil)
	require.NoError(t, err)

	// Stats should be updated
	stats = stage.GetRoutingStats()
	assert.Equal(t, uint64(1), stats.TotalConnections)
	assert.Equal(t, uint64(1), stats.ActiveConnections)

	// Process a packet with timestamp for latency calculation
	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now().Add(-5 * time.Millisecond), // 5ms ago
		Data:      []byte("test"),
		Metadata:  make(map[string]interface{}),
	}

	_, err = stage.Process(ctx, input)
	assert.NoError(t, err)

	// Check latency was calculated
	stats = stage.GetRoutingStats()
	assert.Greater(t, stats.AverageLatency, float64(0))
	assert.Greater(t, stats.MaxLatency, float64(0))
	assert.Greater(t, stats.MinLatency, float64(0))

	// Remove receiver
	stage.RemoveReceiver("receiver-1")

	stats = stage.GetRoutingStats()
	assert.Equal(t, uint64(1), stats.TotalConnections)
	assert.Equal(t, uint64(0), stats.ActiveConnections)
}

// TestWebRTCConnectionStateHandling tests connection state change handling
func TestWebRTCConnectionStateHandling(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, []string{"stun:stun.l.google.com:19302"}, nil)
	ctx := context.Background()

	// Create receiver
	_, err := stage.CreateReceiver(ctx, "receiver-1", nil)
	require.NoError(t, err)

	// Simulate failed connection state
	stage.handleConnectionStateChange("receiver-1", webrtc.PeerConnectionStateFailed)

	// Wait a bit for async removal
	time.Sleep(50 * time.Millisecond)

	// Check stats
	stats := stage.GetRoutingStats()
	assert.Equal(t, uint64(1), stats.FailedConnections)

	// Connection should be removed
	stage.mu.RLock()
	_, exists := stage.connections["receiver-1"]
	stage.mu.RUnlock()
	assert.False(t, exists)
}

// TestWebRTCMultipleStagesInPipeline tests WebRTC stage in a full pipeline
func TestWebRTCMultipleStagesInPipeline(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Add passthrough stage before routing
	passthroughStage := NewPassthroughStage("passthrough", 5)
	pipeline.AddStage(passthroughStage)

	// Add WebRTC routing stage
	routingStage := NewWebRTCRoutingStage("router", 10, nil, nil)
	pipeline.AddStage(routingStage)

	// Verify stages are ordered correctly
	pipeline.mu.RLock()
	stages := pipeline.stages
	pipeline.mu.RUnlock()

	assert.Len(t, stages, 2)
	assert.Equal(t, "passthrough", stages[0].GetName())
	assert.Equal(t, "router", stages[1].GetName())
}

// TestWebRTCPacketChannelOverflow tests handling of packet channel overflow
func TestWebRTCPacketChannelOverflow(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, nil, nil)
	ctx := context.Background()

	// Create receiver
	_, err := stage.CreateReceiver(ctx, "receiver-1", nil)
	require.NoError(t, err)

	// Add track
	err = stage.AddTrackToReceiver("receiver-1", "track-1", webrtc.RTPCodecTypeAudio)
	require.NoError(t, err)

	// Stop the packet forwarding goroutine to simulate blocking
	stage.mu.RLock()
	conn := stage.connections["receiver-1"]
	stage.mu.RUnlock()

	// Close the forwarding goroutine
	close(conn.closeChan)
	conn.wg.Wait()

	// Now the channel won't be consumed, fill it to capacity
	packetsSent := 0
	for i := 0; i < 200; i++ { // Try to send more than channel capacity
		packet := &WebRTCPacket{
			TrackID:   "track-1",
			Timestamp: time.Now(),
			Data:      []byte(fmt.Sprintf("packet-%d", i)),
		}

		select {
		case conn.packetChan <- packet:
			packetsSent++
		default:
			// Channel full
			break
		}
	}

	// Channel capacity is 100, so we should have sent exactly 100
	assert.LessOrEqual(t, packetsSent, 100)
	assert.Greater(t, packetsSent, 0) // Should have sent some packets

	// Now try to route more packets - should drop them since channel is full
	initialDropped := stage.GetRoutingStats().PacketsDropped

	// Send packets that should be dropped
	for i := 0; i < 10; i++ {
		packet := &WebRTCPacket{
			TrackID:   "track-1",
			Timestamp: time.Now(),
			Data:      []byte(fmt.Sprintf("overflow-packet-%d", i)),
		}
		stage.routePacket(packet)
	}

	// Stats should show dropped packets
	stats := stage.GetRoutingStats()
	assert.GreaterOrEqual(t, stats.PacketsDropped, initialDropped+10)

	// Cleanup - manually remove since normal removal expects working goroutine
	stage.mu.Lock()
	conn.PeerConnection.Close()
	delete(stage.connections, "receiver-1")
	stage.mu.Unlock()
}

// TestWebRTCControlMessages tests handling of control messages
func TestWebRTCControlMessages(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, nil, nil)

	// Test control message handling (currently just logs)
	stage.handleControlMessage("receiver-1", []byte("test-control-message"))

	// No error should occur
	assert.NotPanics(t, func() {
		stage.handleControlMessage("receiver-1", []byte("another-message"))
	})
}

// TestWebRTCConcurrentOperations tests thread safety
func TestWebRTCConcurrentOperations(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, []string{"stun:stun.l.google.com:19302"}, nil)
	ctx := context.Background()

	// Run concurrent operations
	done := make(chan bool)

	// Goroutine 1: Create/remove receivers
	go func() {
		for i := 0; i < 10; i++ {
			receiverID := fmt.Sprintf("receiver-%d", i)
			stage.CreateReceiver(ctx, receiverID, nil)
			time.Sleep(time.Millisecond)
			stage.RemoveReceiver(receiverID)
		}
		done <- true
	}()

	// Goroutine 2: Add tracks
	go func() {
		for i := 0; i < 10; i++ {
			receiverID := fmt.Sprintf("receiver-%d", i%3)
			trackID := fmt.Sprintf("track-%d", i)
			stage.CreateReceiver(ctx, receiverID, nil)
			stage.AddTrackToReceiver(receiverID, trackID, webrtc.RTPCodecTypeAudio)
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Goroutine 3: Route packets
	go func() {
		for i := 0; i < 20; i++ {
			packet := &WebRTCPacket{
				TrackID:   fmt.Sprintf("track-%d", i%5),
				Timestamp: time.Now(),
				Data:      []byte("data"),
			}
			stage.routePacket(packet)
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Goroutine 4: Get stats
	go func() {
		for i := 0; i < 20; i++ {
			_ = stage.GetRoutingStats()
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}

	// Clean up any remaining connections
	stage.mu.Lock()
	for id := range stage.connections {
		stage.connections[id].PeerConnection.Close()
		delete(stage.connections, id)
	}
	stage.mu.Unlock()
}

// TestWebRTCICEServerConfiguration tests ICE server configuration
func TestWebRTCICEServerConfiguration(t *testing.T) {
	stunServers := []string{
		"stun:stun1.l.google.com:19302",
		"stun:stun2.l.google.com:19302",
	}

	turnServers := []webrtc.ICEServer{
		{
			URLs:       []string{"turn:turn1.example.com:3478"},
			Username:   "user1",
			Credential: "pass1",
		},
		{
			URLs:       []string{"turn:turn2.example.com:3478", "turns:turn2.example.com:5349"},
			Username:   "user2",
			Credential: "pass2",
		},
	}

	stage := NewWebRTCRoutingStage("test-router", 10, stunServers, turnServers)

	// Verify ICE servers are configured correctly
	assert.Len(t, stage.config.ICEServers, 4) // 2 STUN + 2 TURN

	// Check STUN servers
	assert.Equal(t, []string{"stun:stun1.l.google.com:19302"}, stage.config.ICEServers[0].URLs)
	assert.Equal(t, []string{"stun:stun2.l.google.com:19302"}, stage.config.ICEServers[1].URLs)

	// Check TURN servers
	assert.Equal(t, []string{"turn:turn1.example.com:3478"}, stage.config.ICEServers[2].URLs)
	assert.Equal(t, "user1", stage.config.ICEServers[2].Username)

	assert.Equal(t, []string{"turn:turn2.example.com:3478", "turns:turn2.example.com:5349"}, stage.config.ICEServers[3].URLs)
	assert.Equal(t, "user2", stage.config.ICEServers[3].Username)

	// Check SDP semantics
	assert.Equal(t, webrtc.SDPSemanticsUnifiedPlan, stage.config.SDPSemantics)
}

// TestWebRTCLatencyCalculation tests latency calculation in stats
func TestWebRTCLatencyCalculation(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, nil, nil)

	// Process multiple packets with different latencies
	packets := []struct {
		delay    time.Duration
		expected float64 // approximate expected value
	}{
		{5 * time.Millisecond, 5.0},
		{10 * time.Millisecond, 10.0},
		{2 * time.Millisecond, 2.0},
		{15 * time.Millisecond, 15.0},
	}

	for _, p := range packets {
		packet := &WebRTCPacket{
			TrackID:   "track-1",
			Timestamp: time.Now().Add(-p.delay),
			Data:      []byte("test"),
		}
		stage.updateStats(packet)
	}

	stats := stage.GetRoutingStats()

	// Check min/max
	assert.InDelta(t, 2.0, stats.MinLatency, 1.0)
	assert.InDelta(t, 15.0, stats.MaxLatency, 1.0)

	// Average should be somewhere in the middle
	assert.Greater(t, stats.AverageLatency, stats.MinLatency)
	assert.Less(t, stats.AverageLatency, stats.MaxLatency)
}

// TestWebRTCEmptyPacketHandling tests handling of packets with missing data
func TestWebRTCEmptyPacketHandling(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, nil, nil)
	ctx := context.Background()

	// Test with nil metadata
	input := MediaData{
		Type:      MediaTypeAudio,
		TrackID:   "track-1",
		Timestamp: time.Now(),
		Data:      []byte("test"),
		Metadata:  nil,
	}

	output, err := stage.Process(ctx, input)

	assert.NoError(t, err)
	assert.NotNil(t, output.Metadata)
	assert.True(t, output.Metadata["webrtc_routed"].(bool))

	// Test with empty data
	input2 := MediaData{
		Type:      MediaTypeVideo,
		TrackID:   "track-2",
		Timestamp: time.Now(),
		Data:      []byte{},
		Metadata:  make(map[string]interface{}),
	}

	output2, err := stage.Process(ctx, input2)

	assert.NoError(t, err)
	assert.Empty(t, output2.Data)
	assert.True(t, output2.Metadata["webrtc_routed"].(bool))
}

// TestWebRTCRemoveReceiverWithPendingPackets tests removing receiver with packets in queue
func TestWebRTCRemoveReceiverWithPendingPackets(t *testing.T) {
	stage := NewWebRTCRoutingStage("test-router", 10, nil, nil)
	ctx := context.Background()

	// Create receiver
	_, err := stage.CreateReceiver(ctx, "receiver-1", nil)
	require.NoError(t, err)

	// Add track
	err = stage.AddTrackToReceiver("receiver-1", "track-1", webrtc.RTPCodecTypeAudio)
	require.NoError(t, err)

	// Send multiple packets
	for i := 0; i < 10; i++ {
		packet := &WebRTCPacket{
			TrackID:   "track-1",
			Timestamp: time.Now(),
			Data:      []byte(fmt.Sprintf("packet-%d", i)),
		}
		stage.routePacket(packet)
	}

	// Immediately remove receiver (with packets potentially in queue)
	err = stage.RemoveReceiver("receiver-1")
	assert.NoError(t, err)

	// Verify receiver is removed
	stage.mu.RLock()
	_, exists := stage.connections["receiver-1"]
	stage.mu.RUnlock()
	assert.False(t, exists)
}
