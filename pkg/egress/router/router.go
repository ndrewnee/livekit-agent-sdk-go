package router

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"
)

// TrackKind represents the kind of track
type TrackKind int

const (
	TrackKindVideo TrackKind = iota
	TrackKindAudio
)

// Stats holds routing statistics
type Stats struct {
	PacketsRouted  uint64
	PacketsDropped uint64
	BytesRouted    uint64
}

// Router routes RTP packets to GStreamer via UDP
type Router struct {
	videoConn *net.UDPConn
	audioConn *net.UDPConn
	stats     Stats
	mu        sync.RWMutex
	closed    bool
}

// New creates a new RTP router
func New(videoPort, audioPort int) (*Router, error) {
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

	return &Router{
		videoConn: videoConn,
		audioConn: audioConn,
	}, nil
}

// RoutePacket routes an RTP packet to the appropriate UDP port
func (r *Router) RoutePacket(packet *rtp.Packet, kind TrackKind) error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return fmt.Errorf("router is closed")
	}
	r.mu.RUnlock()

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

// GetStats returns current routing statistics
func (r *Router) GetStats() Stats {
	return Stats{
		PacketsRouted:  atomic.LoadUint64(&r.stats.PacketsRouted),
		PacketsDropped: atomic.LoadUint64(&r.stats.PacketsDropped),
		BytesRouted:    atomic.LoadUint64(&r.stats.BytesRouted),
	}
}

// Close closes the UDP connections
func (r *Router) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}

	r.closed = true

	var errs []error
	if r.videoConn != nil {
		if err := r.videoConn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close video connection: %w", err))
		}
	}
	if r.audioConn != nil {
		if err := r.audioConn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close audio connection: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing router: %v", errs)
	}

	return nil
}