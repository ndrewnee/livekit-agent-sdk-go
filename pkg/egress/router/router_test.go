package router

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRouter(t *testing.T) {
	router, err := New(5004, 5006)
	require.NoError(t, err)
	defer router.Close()

	assert.NotNil(t, router.videoConn)
	assert.NotNil(t, router.audioConn)
	assert.False(t, router.closed)
}

func TestRouterRoutePacket(t *testing.T) {
	// Start UDP listeners to receive packets
	videoAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:5104")
	audioAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:5106")

	videoListener, err := net.ListenUDP("udp", videoAddr)
	require.NoError(t, err)
	defer videoListener.Close()

	audioListener, err := net.ListenUDP("udp", audioAddr)
	require.NoError(t, err)
	defer audioListener.Close()

	// Create router
	router, err := New(5104, 5106)
	require.NoError(t, err)
	defer router.Close()

	// Create test RTP packets
	videoPacket := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Padding:        false,
			Extension:      false,
			Marker:         false,
			PayloadType:    96,
			SequenceNumber: 1234,
			Timestamp:      5678,
			SSRC:           87654321,
		},
		Payload: []byte("video data"),
	}

	audioPacket := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Padding:        false,
			Extension:      false,
			Marker:         false,
			PayloadType:    111,
			SequenceNumber: 4321,
			Timestamp:      8765,
			SSRC:           12345678,
		},
		Payload: []byte("audio data"),
	}

	// Set up goroutines to receive packets
	videoReceived := make(chan bool, 1)
	audioReceived := make(chan bool, 1)

	go func() {
		buf := make([]byte, 1500)
		videoListener.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := videoListener.ReadFromUDP(buf)
		if err == nil && n > 0 {
			videoReceived <- true
		} else {
			videoReceived <- false
		}
	}()

	go func() {
		buf := make([]byte, 1500)
		audioListener.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := audioListener.ReadFromUDP(buf)
		if err == nil && n > 0 {
			audioReceived <- true
		} else {
			audioReceived <- false
		}
	}()

	// Route packets
	err = router.RoutePacket(videoPacket, TrackKindVideo)
	assert.NoError(t, err)

	err = router.RoutePacket(audioPacket, TrackKindAudio)
	assert.NoError(t, err)

	// Check if packets were received
	assert.True(t, <-videoReceived, "Video packet not received")
	assert.True(t, <-audioReceived, "Audio packet not received")

	// Check statistics
	stats := router.GetStats()
	assert.Equal(t, uint64(2), stats.PacketsRouted)
	assert.Equal(t, uint64(0), stats.PacketsDropped)
	assert.Greater(t, stats.BytesRouted, uint64(0))
}

func TestRouterClose(t *testing.T) {
	router, err := New(5204, 5206)
	require.NoError(t, err)

	// Close router
	err = router.Close()
	assert.NoError(t, err)
	assert.True(t, router.closed)

	// Try to route packet after close
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			PayloadType: 96,
		},
		Payload: []byte("test"),
	}

	err = router.RoutePacket(packet, TrackKindVideo)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "router is closed")

	// Close again should be no-op
	err = router.Close()
	assert.NoError(t, err)
}

func TestRouterStatistics(t *testing.T) {
	router, err := New(5304, 5306)
	require.NoError(t, err)
	defer router.Close()

	// Initial stats should be zero
	stats := router.GetStats()
	assert.Equal(t, uint64(0), stats.PacketsRouted)
	assert.Equal(t, uint64(0), stats.PacketsDropped)
	assert.Equal(t, uint64(0), stats.BytesRouted)

	// Route some packets
	for i := 0; i < 10; i++ {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
			},
			Payload: make([]byte, 100),
		}
		router.RoutePacket(packet, TrackKindVideo)
	}

	// Check updated stats
	stats = router.GetStats()
	assert.Equal(t, uint64(10), stats.PacketsRouted)
	assert.Greater(t, stats.BytesRouted, uint64(1000))
}

func TestRouterInvalidPacket(t *testing.T) {
	router, err := New(5404, 5406)
	require.NoError(t, err)
	defer router.Close()

	// Try to route nil packet
	err = router.RoutePacket(nil, TrackKindVideo)
	assert.Error(t, err)

	// Check that dropped counter increased
	stats := router.GetStats()
	assert.Equal(t, uint64(1), stats.PacketsDropped)
}