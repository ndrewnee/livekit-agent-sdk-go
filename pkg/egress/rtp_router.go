package egress

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/livekit/protocol/logger"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// RTPRouter routes RTP packets from LiveKit tracks to the GStreamer pipeline
// Implements the routing architecture from PLAN.md Milestone 2
type RTPRouter struct {
	config   *Config
	pipeline *pipeline.DirectPipeline // Direct injection pipeline

	// UDP mode connections (deprecated, but kept for compatibility)
	videoConn *net.UDPConn
	audioConn *net.UDPConn
	videoAddr *net.UDPAddr
	audioAddr *net.UDPAddr

	// State management
	running  atomic.Bool
	stopOnce sync.Once
	wg       sync.WaitGroup

	// Statistics
	stats RouterStats
	mu    sync.RWMutex

	// Rate limiting for error logging
	lastErrorLog   time.Time
	errorLogPeriod time.Duration
}

// RouterStats holds router statistics
type RouterStats struct {
	PacketsReceived  uint64 `json:"packets_received"`
	PacketsForwarded uint64 `json:"packets_forwarded"`
	PacketsDropped   uint64 `json:"packets_dropped"`
	BytesReceived    uint64 `json:"bytes_received"`
	BytesForwarded   uint64 `json:"bytes_forwarded"`
	VideoPackets     uint64 `json:"video_packets"`
	AudioPackets     uint64 `json:"audio_packets"`
	Errors           uint64 `json:"errors"`
	LastPacketTime   int64  `json:"last_packet_time"` // Unix timestamp
}

// NewRTPRouter creates a new RTP router
func NewRTPRouter(config *Config, pipeline *pipeline.DirectPipeline) *RTPRouter {
	return &RTPRouter{
		config:         config,
		pipeline:       pipeline,
		errorLogPeriod: 10 * time.Second, // Log errors at most every 10 seconds
	}
}

// Start initializes the router
func (r *RTPRouter) Start() error {
	fmt.Printf("===== RTP Router Start() called =====\n")
	if !r.running.CompareAndSwap(false, true) {
		fmt.Printf("===== Router already running! =====\n")
		return fmt.Errorf("router already running")
	}
	fmt.Printf("===== RTP Router started successfully, running=%v =====\n", r.running.Load())

	// If using UDP mode (deprecated), set up UDP connections
	if !r.config.NetworkConfig.UseDirectInjection {
		if err := r.setupUDPConnections(); err != nil {
			r.running.Store(false)
			return fmt.Errorf("failed to setup UDP connections: %w", err)
		}
	}

	logger.Infow("RTP router started",
		"directInjection", r.config.NetworkConfig.UseDirectInjection,
		"videoPort", r.config.NetworkConfig.VideoRTPPort,
		"audioPort", r.config.NetworkConfig.AudioRTPPort)

	return nil
}

// setupUDPConnections sets up UDP connections for legacy UDP mode
func (r *RTPRouter) setupUDPConnections() error {
	var err error

	// Create video UDP connection
	// Use configurable host, default to localhost if not specified
	host := r.config.NetworkConfig.BindAddress
	if host == "" {
		host = "127.0.0.1"
	}
	r.videoAddr, err = net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, r.config.NetworkConfig.VideoRTPPort))
	if err != nil {
		return fmt.Errorf("failed to resolve video UDP address: %w", err)
	}

	r.videoConn, err = net.DialUDP("udp", nil, r.videoAddr)
	if err != nil {
		return fmt.Errorf("failed to create video UDP connection: %w", err)
	}

	// Create audio UDP connection
	r.audioAddr, err = net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", host, r.config.NetworkConfig.AudioRTPPort))
	if err != nil {
		return fmt.Errorf("failed to resolve audio UDP address: %w", err)
	}

	r.audioConn, err = net.DialUDP("udp", nil, r.audioAddr)
	if err != nil {
		r.videoConn.Close()
		return fmt.Errorf("failed to create audio UDP connection: %w", err)
	}

	return nil
}

// Stop stops the router
func (r *RTPRouter) Stop() {
	r.stopOnce.Do(func() {
		logger.Infow("stopping RTP router")
		r.running.Store(false)

		// Close UDP connections if used
		if r.videoConn != nil {
			r.videoConn.Close()
		}
		if r.audioConn != nil {
			r.audioConn.Close()
		}

		// Wait for all goroutines to finish
		r.wg.Wait()

		logger.Infow("RTP router stopped",
			"packetsReceived", atomic.LoadUint64(&r.stats.PacketsReceived),
			"packetsForwarded", atomic.LoadUint64(&r.stats.PacketsForwarded),
			"packetsDropped", atomic.LoadUint64(&r.stats.PacketsDropped))
	})
}

// ForwardTrack starts forwarding RTP packets from a track
func (r *RTPRouter) ForwardTrack(track *webrtc.TrackRemote) {
	fmt.Printf("===== ForwardTrack called for track %s, router running: %v =====\n", track.ID(), r.running.Load())
	if !r.running.Load() {
		fmt.Printf("===== Router not running, cannot forward track %s =====\n", track.ID())
		logger.Debugw("router not running, cannot forward track", "trackID", track.ID())
		return
	}

	fmt.Printf("===== Spawning goroutine to forward track %s =====\n", track.ID())
	r.wg.Add(1)
	go r.forwardRTPPackets(track)
}

// forwardRTPPackets reads and forwards RTP packets from a track
func (r *RTPRouter) forwardRTPPackets(track *webrtc.TrackRemote) {
	defer r.wg.Done()

	fmt.Printf("===== Starting RTP forwarding for track %s (kind: %v, codec: %s) =====\n",
		track.ID(), track.Kind(), track.Codec().MimeType)

	logger.Debugw("starting RTP forwarding",
		"trackID", track.ID(),
		"kind", track.Kind(),
		"codec", track.Codec().MimeType)

	// Read RTP packets from the track
	packetCount := 0
	for r.running.Load() {
		// Read RTP packet
		packet, _, readErr := track.ReadRTP()
		if readErr != nil {
			if r.running.Load() {
				// Only log if we're still supposed to be running
				fmt.Printf("===== Error reading RTP packet from track %s: %v =====\n", track.ID(), readErr)
				r.logError("failed to read RTP packet", readErr)
			}
			break
		}

		packetCount++
		if packetCount == 1 || packetCount %100 == 0 {
			fmt.Printf("===== Received packet #%d from track %s (kind: %v) =====\n", packetCount, track.ID(), track.Kind())
		}

		// Update statistics
		atomic.AddUint64(&r.stats.PacketsReceived, 1)
		atomic.AddUint64(&r.stats.BytesReceived, uint64(len(packet.Payload)+12)) // 12 bytes RTP header

		// Route the packet
		if err := r.routePacket(packet, track.Kind()); err != nil {
			atomic.AddUint64(&r.stats.PacketsDropped, 1)
			atomic.AddUint64(&r.stats.Errors, 1)
			r.logError("failed to route packet", err)
		} else {
			atomic.AddUint64(&r.stats.PacketsForwarded, 1)
			atomic.AddUint64(&r.stats.BytesForwarded, uint64(len(packet.Payload)+12))

			// Update packet type counter
			if track.Kind() == webrtc.RTPCodecTypeVideo {
				atomic.AddUint64(&r.stats.VideoPackets, 1)
			} else {
				atomic.AddUint64(&r.stats.AudioPackets, 1)
			}
		}

		// Update last packet time
		r.mu.Lock()
		r.stats.LastPacketTime = time.Now().Unix()
		r.mu.Unlock()
	}

	logger.Debugw("stopped RTP forwarding",
		"trackID", track.ID(),
		"packetsForwarded", atomic.LoadUint64(&r.stats.PacketsForwarded))
}

// routePacket routes an RTP packet to the appropriate destination
func (r *RTPRouter) routePacket(packet *rtp.Packet, kind webrtc.RTPCodecType) error {
	if packet == nil {
		return fmt.Errorf("nil packet")
	}

	// Use direct injection if configured (recommended)
	if r.config.NetworkConfig.UseDirectInjection {
		return r.routePacketDirect(packet, kind)
	}

	// Legacy UDP mode
	return r.routePacketUDP(packet, kind)
}

// routePacketDirect uses direct injection to the pipeline (recommended)
func (r *RTPRouter) routePacketDirect(packet *rtp.Packet, kind webrtc.RTPCodecType) error {
	if r.pipeline == nil {
		fmt.Printf("===== ERROR: pipeline is nil in routePacketDirect =====\n")
		return fmt.Errorf("pipeline not available for direct injection")
	}

	// Route based on codec type
	var err error
	switch kind {
	case webrtc.RTPCodecTypeVideo:
		err = r.pipeline.InjectVideoRTP(packet)
		if err != nil {
			fmt.Printf("===== ERROR injecting video RTP: %v =====\n", err)
		}
		return err
	case webrtc.RTPCodecTypeAudio:
		err = r.pipeline.InjectAudioRTP(packet)
		if err != nil {
			fmt.Printf("===== ERROR injecting audio RTP: %v =====\n", err)
		}
		return err
	default:
		return fmt.Errorf("unsupported codec type: %v", kind)
	}
}

// routePacketUDP uses UDP forwarding (legacy mode)
func (r *RTPRouter) routePacketUDP(packet *rtp.Packet, kind webrtc.RTPCodecType) error {
	// Select the appropriate UDP connection
	var conn *net.UDPConn
	switch kind {
	case webrtc.RTPCodecTypeVideo:
		conn = r.videoConn
	case webrtc.RTPCodecTypeAudio:
		conn = r.audioConn
	default:
		return fmt.Errorf("unsupported codec type: %v", kind)
	}

	if conn == nil {
		return fmt.Errorf("UDP connection not available")
	}

	// Marshal the packet
	data, err := packet.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal RTP packet: %w", err)
	}

	// Send via UDP
	_, err = conn.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write to UDP: %w", err)
	}

	return nil
}

// GetStats returns router statistics
func (r *RTPRouter) GetStats() RouterStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Create a copy with atomic loads for thread-safe access
	return RouterStats{
		PacketsReceived:  atomic.LoadUint64(&r.stats.PacketsReceived),
		PacketsForwarded: atomic.LoadUint64(&r.stats.PacketsForwarded),
		PacketsDropped:   atomic.LoadUint64(&r.stats.PacketsDropped),
		BytesReceived:    atomic.LoadUint64(&r.stats.BytesReceived),
		BytesForwarded:   atomic.LoadUint64(&r.stats.BytesForwarded),
		VideoPackets:     atomic.LoadUint64(&r.stats.VideoPackets),
		AudioPackets:     atomic.LoadUint64(&r.stats.AudioPackets),
		Errors:           atomic.LoadUint64(&r.stats.Errors),
		LastPacketTime:   r.stats.LastPacketTime,
	}
}

// GetPacketLossRate returns the packet loss rate as a percentage
func (r *RTPRouter) GetPacketLossRate() float64 {
	received := atomic.LoadUint64(&r.stats.PacketsReceived)
	if received == 0 {
		return 0
	}

	dropped := atomic.LoadUint64(&r.stats.PacketsDropped)
	return float64(dropped) / float64(received) * 100.0
}

// IsHealthy checks if the router is operating within acceptable parameters
// Per PLAN.md: RTP packets routed with <0.1% loss
func (r *RTPRouter) IsHealthy() bool {
	if !r.running.Load() {
		return false
	}

	// Check packet loss rate
	lossRate := r.GetPacketLossRate()
	if lossRate > 0.1 {
		logger.Debugw("router unhealthy: packet loss exceeds 0.1%",
			"lossRate", lossRate,
			"packetsReceived", atomic.LoadUint64(&r.stats.PacketsReceived),
			"packetsDropped", atomic.LoadUint64(&r.stats.PacketsDropped))
		return false
	}

	// Check if we're receiving packets (no stall)
	r.mu.RLock()
	lastPacketTime := r.stats.LastPacketTime
	r.mu.RUnlock()

	if lastPacketTime > 0 {
		timeSinceLastPacket := time.Since(time.Unix(lastPacketTime, 0))
		if timeSinceLastPacket > 30*time.Second {
			logger.Debugw("router unhealthy: no packets received recently",
				"timeSinceLastPacket", timeSinceLastPacket)
			return false
		}
	}

	return true
}

// logError logs errors with rate limiting
func (r *RTPRouter) logError(msg string, err error) {
	now := time.Now()
	r.mu.Lock()
	shouldLog := now.Sub(r.lastErrorLog) > r.errorLogPeriod
	if shouldLog {
		r.lastErrorLog = now
	}
	r.mu.Unlock()

	if shouldLog {
		logger.Errorw(msg, err,
			"totalErrors", atomic.LoadUint64(&r.stats.Errors))
	}
}

// ResetStats resets the router statistics (useful for testing)
func (r *RTPRouter) ResetStats() {
	r.mu.Lock()
	defer r.mu.Unlock()

	atomic.StoreUint64(&r.stats.PacketsReceived, 0)
	atomic.StoreUint64(&r.stats.PacketsForwarded, 0)
	atomic.StoreUint64(&r.stats.PacketsDropped, 0)
	atomic.StoreUint64(&r.stats.BytesReceived, 0)
	atomic.StoreUint64(&r.stats.BytesForwarded, 0)
	atomic.StoreUint64(&r.stats.VideoPackets, 0)
	atomic.StoreUint64(&r.stats.AudioPackets, 0)
	atomic.StoreUint64(&r.stats.Errors, 0)
	r.stats.LastPacketTime = 0
}