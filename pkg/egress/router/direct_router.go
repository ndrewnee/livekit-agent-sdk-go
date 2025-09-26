package router

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pion/rtp"
)

// DirectRouter routes RTP packets directly to GStreamer pipeline via appsrc
// This eliminates UDP overhead and port conflicts, allowing unlimited concurrent workers
type DirectRouter struct {
	pipeline PipelineInjector
	stats    Stats
	mu       sync.RWMutex
	closed   bool
}

// PipelineInjector interface for injecting RTP packets into the pipeline
type PipelineInjector interface {
	InjectVideoRTP(packet *rtp.Packet) error
	InjectAudioRTP(packet *rtp.Packet) error
}

// NewDirectRouter creates a new direct RTP router
func NewDirectRouter(pipeline PipelineInjector) (*DirectRouter, error) {
	if pipeline == nil {
		return nil, fmt.Errorf("pipeline injector is required")
	}

	return &DirectRouter{
		pipeline: pipeline,
	}, nil
}

// RoutePacket routes an RTP packet directly to the pipeline
func (r *DirectRouter) RoutePacket(packet *rtp.Packet, kind TrackKind) error {
	// Check for nil packet first
	if packet == nil {
		atomic.AddUint64(&r.stats.PacketsDropped, 1)
		return fmt.Errorf("packet is nil")
	}

	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return fmt.Errorf("router is closed")
	}
	r.mu.RUnlock()

	// Route packet based on track kind
	var err error
	if kind == TrackKindVideo {
		err = r.pipeline.InjectVideoRTP(packet)
	} else {
		err = r.pipeline.InjectAudioRTP(packet)
	}

	if err != nil {
		atomic.AddUint64(&r.stats.PacketsDropped, 1)
		return fmt.Errorf("failed to inject packet: %w", err)
	}

	// Update statistics
	atomic.AddUint64(&r.stats.PacketsRouted, 1)
	// Calculate approximate packet size (header + payload)
	packetSize := 12 + len(packet.Payload) // RTP header is typically 12 bytes
	atomic.AddUint64(&r.stats.BytesRouted, uint64(packetSize))

	return nil
}

// GetStats returns current routing statistics
func (r *DirectRouter) GetStats() Stats {
	return Stats{
		PacketsRouted:  atomic.LoadUint64(&r.stats.PacketsRouted),
		PacketsDropped: atomic.LoadUint64(&r.stats.PacketsDropped),
		BytesRouted:    atomic.LoadUint64(&r.stats.BytesRouted),
	}
}

// Close closes the router
func (r *DirectRouter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil
	}

	r.closed = true
	return nil
}