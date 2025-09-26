package main

import (
	"fmt"
	"net"
	"sync/atomic"

	"github.com/pion/rtp"
)

// TrackKind represents the kind of track
type TrackKind int

const (
	TrackKindVideo TrackKind = iota
	TrackKindAudio
)

// RouterStats holds routing statistics
type RouterStats struct {
	PacketsRouted  uint64
	PacketsDropped uint64
	BytesRouted    uint64
}

// RTPRouter routes RTP packets to GStreamer via UDP
type RTPRouter struct {
	videoConn *net.UDPConn
	audioConn *net.UDPConn
	videoAddr *net.UDPAddr
	audioAddr *net.UDPAddr
	stats     RouterStats
}

// NewRTPRouter creates a new RTP router
func NewRTPRouter(videoPort, audioPort int) (*RTPRouter, error) {
	// Create UDP addresses for GStreamer
	videoAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", videoPort))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve video address: %w", err)
	}

	audioAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", audioPort))
	if err != nil {
		return nil, fmt.Errorf("failed to resolve audio address: %w", err)
	}

	// Create UDP connections
	videoConn, err := net.DialUDP("udp", nil, videoAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to create video connection: %w", err)
	}

	audioConn, err := net.DialUDP("udp", nil, audioAddr)
	if err != nil {
		videoConn.Close()
		return nil, fmt.Errorf("failed to create audio connection: %w", err)
	}

	// Set buffer sizes for better performance
	videoConn.SetWriteBuffer(2 * 1024 * 1024) // 2MB
	audioConn.SetWriteBuffer(512 * 1024)       // 512KB

	return &RTPRouter{
		videoConn: videoConn,
		audioConn: audioConn,
		videoAddr: videoAddr,
		audioAddr: audioAddr,
	}, nil
}

// RoutePacket routes an RTP packet to the appropriate UDP port
func (r *RTPRouter) RoutePacket(packet *rtp.Packet, kind TrackKind) error {
	// Marshal the packet
	data, err := packet.Marshal()
	if err != nil {
		atomic.AddUint64(&r.stats.PacketsDropped, 1)
		return fmt.Errorf("failed to marshal packet: %w", err)
	}

	// Select the appropriate connection
	var conn *net.UDPConn
	if kind == TrackKindVideo {
		conn = r.videoConn
	} else {
		conn = r.audioConn
	}

	// Send the packet
	_, err = conn.Write(data)
	if err != nil {
		atomic.AddUint64(&r.stats.PacketsDropped, 1)
		return fmt.Errorf("failed to write packet: %w", err)
	}

	// Update statistics
	atomic.AddUint64(&r.stats.PacketsRouted, 1)
	atomic.AddUint64(&r.stats.BytesRouted, uint64(len(data)))

	return nil
}

// GetStatistics returns current routing statistics
func (r *RTPRouter) GetStatistics() RouterStats {
	return RouterStats{
		PacketsRouted:  atomic.LoadUint64(&r.stats.PacketsRouted),
		PacketsDropped: atomic.LoadUint64(&r.stats.PacketsDropped),
		BytesRouted:    atomic.LoadUint64(&r.stats.BytesRouted),
	}
}

// Close closes the UDP connections
func (r *RTPRouter) Close() error {
	if r.videoConn != nil {
		r.videoConn.Close()
	}
	if r.audioConn != nil {
		r.audioConn.Close()
	}
	return nil
}