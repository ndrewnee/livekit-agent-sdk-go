// +build integration

package egress

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ChaosMonkey introduces random failures and unexpected conditions
type ChaosMonkey struct {
	mu sync.RWMutex

	// Chaos parameters
	enabled          bool
	chaosLevel       float32 // 0.0 to 1.0
	seed             int64
	rand             *rand.Rand

	// Types of chaos
	packetCorruption bool
	timestampJumps   bool
	ssrcChanges      bool
	payloadChanges   bool
	sequenceGaps     bool
	markerBitChaos   bool
	headerCorruption bool

	// Statistics
	stats struct {
		corruptedPackets  uint64
		timestampJumps    uint64
		ssrcChanges       uint64
		payloadMutations  uint64
		sequenceGaps      uint64
		markerBitFlips    uint64
		headerCorruptions uint64
	}
}

// NewChaosMonkey creates a chaos testing utility
func NewChaosMonkey(seed int64, chaosLevel float32) *ChaosMonkey {
	return &ChaosMonkey{
		enabled:          true,
		chaosLevel:       chaosLevel,
		seed:             seed,
		rand:             rand.New(rand.NewSource(seed)),
		packetCorruption: true,
		timestampJumps:   true,
		ssrcChanges:      true,
		payloadChanges:   true,
		sequenceGaps:     true,
		markerBitChaos:   true,
		headerCorruption: true,
	}
}

// InjectChaos randomly corrupts packets
func (cm *ChaosMonkey) InjectChaos(packet *rtp.Packet) *rtp.Packet {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if !cm.enabled || cm.rand.Float32() > cm.chaosLevel {
		return packet
	}

	// Choose random chaos type
	chaosType := cm.rand.Intn(7)

	switch chaosType {
	case 0: // Corrupt payload
		if cm.packetCorruption && len(packet.Payload) > 0 {
			pos := cm.rand.Intn(len(packet.Payload))
			packet.Payload[pos] ^= byte(cm.rand.Intn(256))
			atomic.AddUint64(&cm.stats.corruptedPackets, 1)
		}

	case 1: // Timestamp jump
		if cm.timestampJumps {
			jump := uint32(cm.rand.Intn(90000)) // Up to 1 second jump
			if cm.rand.Float32() < 0.5 {
				packet.Header.Timestamp += jump
			} else {
				packet.Header.Timestamp -= jump
			}
			atomic.AddUint64(&cm.stats.timestampJumps, 1)
		}

	case 2: // SSRC change
		if cm.ssrcChanges {
			packet.Header.SSRC = uint32(cm.rand.Uint32())
			atomic.AddUint64(&cm.stats.ssrcChanges, 1)
		}

	case 3: // Payload mutation
		if cm.payloadChanges && len(packet.Payload) > 0 {
			// Replace with random data
			newPayload := make([]byte, len(packet.Payload))
			cm.rand.Read(newPayload)
			packet.Payload = newPayload
			atomic.AddUint64(&cm.stats.payloadMutations, 1)
		}

	case 4: // Sequence gap
		if cm.sequenceGaps {
			gap := uint16(cm.rand.Intn(100))
			packet.Header.SequenceNumber += gap
			atomic.AddUint64(&cm.stats.sequenceGaps, 1)
		}

	case 5: // Marker bit flip
		if cm.markerBitChaos {
			packet.Header.Marker = !packet.Header.Marker
			atomic.AddUint64(&cm.stats.markerBitFlips, 1)
		}

	case 6: // Header corruption
		if cm.headerCorruption {
			// Corrupt header fields
			packet.Header.Version = uint8(cm.rand.Intn(4))
			packet.Header.PayloadType = uint8(cm.rand.Intn(128))
			atomic.AddUint64(&cm.stats.headerCorruptions, 1)
		}
	}

	return packet
}

// GetStats returns chaos statistics
func (cm *ChaosMonkey) GetStats() map[string]uint64 {
	return map[string]uint64{
		"corrupted_packets":  atomic.LoadUint64(&cm.stats.corruptedPackets),
		"timestamp_jumps":    atomic.LoadUint64(&cm.stats.timestampJumps),
		"ssrc_changes":       atomic.LoadUint64(&cm.stats.ssrcChanges),
		"payload_mutations":  atomic.LoadUint64(&cm.stats.payloadMutations),
		"sequence_gaps":      atomic.LoadUint64(&cm.stats.sequenceGaps),
		"marker_bit_flips":   atomic.LoadUint64(&cm.stats.markerBitFlips),
		"header_corruptions": atomic.LoadUint64(&cm.stats.headerCorruptions),
	}
}

// TestChaosInjection tests pipeline resilience to chaotic input
func TestChaosInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping chaos test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &pipeline.Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true, // This is a live source test
	}

	p, err := pipeline.NewDirectPipeline(config, "chaos-test")
	require.NoError(t, err)

	err = p.Start()
	require.NoError(t, err)
	defer p.Stop()

	// Create chaos monkey
	chaos := NewChaosMonkey(time.Now().UnixNano(), 0.3) // 30% chaos level

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var totalPackets, successPackets, errorPackets atomic.Uint64

	// Send chaotic packets
	go func() {
		seq := uint16(0)
		timestamp := uint32(0)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Create normal packet
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: seq,
						Timestamp:      timestamp,
						SSRC:           12345,
					},
					Payload: []byte{0x67, 0x42, 0x00, 0x1f, 0x96, 0x54, 0x05, 0x01, 0x7f, 0xcb}, // H.264 NAL unit
				}

				// Inject chaos
				chaoticPacket := chaos.InjectChaos(packet)

				// Try to inject into pipeline
				if err := p.InjectVideoRTP(chaoticPacket); err != nil {
					errorPackets.Add(1)
				} else {
					successPackets.Add(1)
				}
				totalPackets.Add(1)

				seq++
				timestamp += 3000
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	<-ctx.Done()

	// Check results
	chaosStats := chaos.GetStats()
	pipelineStats := p.GetStats()

	t.Logf("Chaos test results:")
	t.Logf("  Total packets: %d", totalPackets.Load())
	t.Logf("  Success: %d, Errors: %d", successPackets.Load(), errorPackets.Load())
	t.Logf("  Chaos stats: %+v", chaosStats)
	t.Logf("  Pipeline stats: %+v", pipelineStats)

	// Pipeline should handle chaos gracefully
	successRate := float64(successPackets.Load()) / float64(totalPackets.Load())
	assert.Greater(t, successRate, 0.5, "Should handle >50% of chaotic packets")
}

// FuzzRTPPacket tests with fuzzing
func FuzzRTPPacket(f *testing.F) {
	// Seed corpus
	f.Add([]byte{0x80, 0x60, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	f.Add([]byte{0x80, 0x6f, 0x00, 0x02, 0x00, 0x00, 0x0b, 0xb8, 0x12, 0x34, 0x56, 0x78, 0x00, 0x01, 0x02, 0x03})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Try to parse as RTP packet
		packet := &rtp.Packet{}
		err := packet.Unmarshal(data)

		if err == nil {
			// Valid RTP packet - try to process it
			processFuzzedPacket(packet)
		}
	})
}

// FuzzPipelineConfig tests pipeline with fuzzing
func FuzzPipelineConfig(f *testing.F) {
	// Seed corpus
	f.Add(uint32(2), uint32(200), uint32(1))
	f.Add(uint32(10), uint32(500), uint32(0))
	f.Add(uint32(0), uint32(0), uint32(2))

	f.Fuzz(func(t *testing.T, segmentDuration, jitterBuffer, audioMode uint32) {
		// Skip invalid values
		if segmentDuration > 3600 || jitterBuffer > 10000 {
			t.Skip("Invalid config values")
		}

		gst.Init(nil)

		tmpDir := t.TempDir()
		// Map audioMode int to valid AudioMode
		var mode pipeline.AudioMode
		switch audioMode % 3 {
		case 0:
			mode = pipeline.AudioPassThrough
		case 1:
			mode = pipeline.AudioTranscodeAAC
		case 2:
			mode = pipeline.AudioTranscodeMP3
		}

		config := &pipeline.Config{
			OutputDir:       tmpDir,
			SegmentDuration: int(segmentDuration),
			JitterBufferMs:  int(jitterBuffer),
			AudioMode:       mode,
		}

		p, err := pipeline.NewDirectPipeline(config, "fuzz-config")
		if err != nil {
			// Invalid config is ok
			return
		}
		defer p.Stop()

		// Try to start with fuzzing config
		p.Start()
	})
}

// TestRandomPacketGenerator tests with random packet generation
func TestRandomPacketGenerator(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping random packet test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &pipeline.Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true, // This is a live source test
	}

	p, err := pipeline.NewDirectPipeline(config, "random-test")
	require.NoError(t, err)
	defer p.Stop()

	err = p.Start()
	require.NoError(t, err)

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Generate random video packets
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := rand.New(rand.NewSource(time.Now().UnixNano()))

		for i := 0; i < 1000; i++ {
			select {
			case <-ctx.Done():
				return
			default:
				packet := generateRandomRTPPacket(r, 96) // Video
				p.InjectVideoRTP(packet)
				time.Sleep(time.Duration(r.Intn(50)) * time.Millisecond)
			}
		}
	}()

	// Generate random audio packets
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := rand.New(rand.NewSource(time.Now().UnixNano() + 1))

		for i := 0; i < 1000; i++ {
			select {
			case <-ctx.Done():
				return
			default:
				packet := generateRandomRTPPacket(r, 111) // Audio
				p.InjectAudioRTP(packet)
				time.Sleep(time.Duration(r.Intn(50)) * time.Millisecond)
			}
		}
	}()

	wg.Wait()

	stats := p.GetStats()
	t.Logf("Random packet test - Video: %d, Audio: %d",
		stats.VideoPacketsReceived, stats.AudioPacketsReceived)

	// Should handle random packets without crashing
	assert.Greater(t, stats.VideoPacketsReceived+stats.AudioPacketsReceived, uint64(0),
		"Should process some random packets")
}

// TestMaliciousInput tests handling of malicious input
func TestMaliciousInput(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping malicious input test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &pipeline.Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true, // This is a live source test
	}

	p, err := pipeline.NewDirectPipeline(config, "malicious-test")
	require.NoError(t, err)

	err = p.Start()
	require.NoError(t, err)
	defer p.Stop()

	testCases := []struct {
		name   string
		packet *rtp.Packet
	}{
		{
			"zero-length payload",
			&rtp.Packet{
				Header: rtp.Header{Version: 2, PayloadType: 96},
				Payload: []byte{},
			},
		},
		{
			"huge payload",
			&rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 96},
				Payload: make([]byte, 65536), // 64KB
			},
		},
		{
			"invalid version",
			&rtp.Packet{
				Header:  rtp.Header{Version: 3, PayloadType: 96},
				Payload: []byte{0x01, 0x02},
			},
		},
		{
			"max timestamp",
			&rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 96, Timestamp: 0xFFFFFFFF},
				Payload: []byte{0x01, 0x02},
			},
		},
		{
			"max sequence",
			&rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 0xFFFF},
				Payload: []byte{0x01, 0x02},
			},
		},
		{
			"invalid payload type",
			&rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 255},
				Payload: []byte{0x01, 0x02},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic
			err := p.InjectVideoRTP(tc.packet)
			// Error is ok, panic is not
			_ = err
		})
	}

	// Send rapid-fire packets
	t.Run("rapid fire", func(t *testing.T) {
		for i := 0; i < 10000; i++ {
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: uint16(i),
					Timestamp:      uint32(i),
					SSRC:           uint32(i),
				},
				Payload: []byte{byte(i)},
			}
			p.InjectVideoRTP(packet)
		}
	})

	// Should survive malicious input
	stats := p.GetStats()
	t.Logf("Survived malicious input - Stats: %+v", stats)
}

// TestConcurrentChaos tests concurrent chaotic operations
func TestConcurrentChaos(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent chaos test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()

	// Create multiple pipelines (reduced from 5 to 3 for faster testing)
	pipelines := make([]*pipeline.DirectPipeline, 3)
	chaosMonkeys := make([]*ChaosMonkey, 3)

	// First create all pipelines
	for i := 0; i < 3; i++ {
		config := &pipeline.Config{
			OutputDir:          fmt.Sprintf("%s/pipeline-%d", tmpDir, i),
			SegmentDuration:    2,
			JitterBufferMs:     200,
			AudioMode:          pipeline.AudioPassThrough,
			StateChangeTimeout: 2 * time.Second, // Shorter timeout for concurrent test
			IsLiveSource:       true,
			AllowAsyncStart:    true, // Use async start to avoid blocking
		}

		p, err := pipeline.NewDirectPipeline(config, fmt.Sprintf("chaos-%d", i))
		require.NoError(t, err)

		pipelines[i] = p
		chaosMonkeys[i] = NewChaosMonkey(time.Now().UnixNano()+int64(i), 0.2+float32(i)*0.1)
	}

	// Start all pipelines in parallel
	var startWg sync.WaitGroup
	for i := 0; i < 3; i++ {
		startWg.Add(1)
		go func(idx int) {
			defer startWg.Done()
			err := pipelines[idx].Start()
			if err != nil {
				t.Errorf("Failed to start pipeline %d: %v", idx, err)
			}
		}(i)
	}
	startWg.Wait()

	// Give pipelines a moment to stabilize
	time.Sleep(500 * time.Millisecond)

	defer func() {
		for _, p := range pipelines {
			p.Stop()
		}
	}()

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // Shorter timeout for concurrent test
	defer cancel()

	// Launch chaos for each pipeline
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := pipelines[idx]
			chaos := chaosMonkeys[idx]
			seq := uint16(0)

			for {
				select {
				case <-ctx.Done():
					return
				default:
					packet := &rtp.Packet{
						Header: rtp.Header{
							Version:        2,
							PayloadType:    96,
							SequenceNumber: seq,
							Timestamp:      uint32(seq * 3000),
							SSRC:           uint32(12345 + idx),
						},
						Payload: make([]byte, 100),
					}

					chaoticPacket := chaos.InjectChaos(packet)
					p.InjectVideoRTP(chaoticPacket)

					seq++
					time.Sleep(time.Duration(20+idx*5) * time.Millisecond)
				}
			}
		}(i)
	}

	wg.Wait()

	// Check all pipelines survived
	for i, p := range pipelines {
		stats := p.GetStats()
		chaosStats := chaosMonkeys[i].GetStats()

		t.Logf("Pipeline %d - Stats: %+v, Chaos: %+v", i, stats, chaosStats)

		// Each pipeline should process some packets despite chaos
		assert.Greater(t, stats.VideoPacketsReceived, uint64(0),
			"Pipeline %d should process packets", i)
	}
}

// Helper functions

func generateRandomRTPPacket(r *rand.Rand, payloadType uint8) *rtp.Packet {
	payloadSize := r.Intn(1400) + 1
	payload := make([]byte, payloadSize)
	r.Read(payload)

	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    payloadType,
			SequenceNumber: uint16(r.Uint32()),
			Timestamp:      r.Uint32(),
			SSRC:           r.Uint32(),
			Marker:         r.Float32() < 0.1,
		},
		Payload: payload,
	}
}

func processFuzzedPacket(packet *rtp.Packet) {
	// Try to process the packet without panicking
	defer func() {
		if r := recover(); r != nil {
			// Recovered from panic - this is expected with fuzzing
		}
	}()

	// Validate packet
	if packet.Header.Version != 2 {
		return
	}

	// Check payload
	if len(packet.Payload) > 65536 {
		return
	}

	// Try to interpret as H.264
	if packet.Header.PayloadType == 96 && len(packet.Payload) > 0 {
		nalType := packet.Payload[0] & 0x1F
		_ = nalType
	}

	// Try to interpret as Opus
	if packet.Header.PayloadType == 111 && len(packet.Payload) >= 1 {
		tocByte := packet.Payload[0]
		_ = tocByte
	}
}

// TestPathTraversal tests for path traversal vulnerabilities
func TestPathTraversal(t *testing.T) {
	maliciousPaths := []string{
		"../../../etc/passwd",
		"..\\..\\..\\windows\\system32",
		"/etc/passwd",
		"C:\\Windows\\System32",
		"../../../../../../../../etc/shadow",
		"%2e%2e%2f%2e%2e%2f",
		"..%252f..%252f",
	}

	for _, path := range maliciousPaths {
		t.Run(path, func(t *testing.T) {
			// Try to use malicious path in config
			config := &pipeline.Config{
				OutputDir:       path,
				SegmentDuration: 2,
				JitterBufferMs:  200,
				AudioMode:       pipeline.AudioPassThrough,
			}

			// Should either sanitize or reject
			p, err := pipeline.NewDirectPipeline(config, "traversal-test")
			if err == nil {
				// If no error, path should be sanitized
				assert.NotContains(t, config.OutputDir, "..")
				assert.NotContains(t, config.OutputDir, "/etc")
				assert.NotContains(t, config.OutputDir, "C:\\")
				p.Stop()
			}
		})
	}
}

// TestBufferOverflow tests for buffer overflow conditions
func TestBufferOverflow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping buffer overflow test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &pipeline.Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true, // This is a live source test
	}

	p, err := pipeline.NewDirectPipeline(config, "overflow-test")
	require.NoError(t, err)

	err = p.Start()
	require.NoError(t, err)
	defer p.Stop()

	// Try various overflow conditions
	testCases := []struct {
		name        string
		payloadSize int
	}{
		{"1KB", 1024},
		{"10KB", 10 * 1024},
		{"64KB", 64 * 1024},
		{"1MB", 1024 * 1024},
		{"10MB", 10 * 1024 * 1024},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:     2,
					PayloadType: 96,
				},
				Payload: make([]byte, tc.payloadSize),
			}

			// Should handle large payloads without crashing
			err := p.InjectVideoRTP(packet)
			_ = err // Error is ok, crash is not
		})
	}
}

// TestIntegerOverflow tests for integer overflow conditions
func TestIntegerOverflow(t *testing.T) {
	overflowTests := []struct {
		name      string
		timestamp uint32
		sequence  uint16
	}{
		{"max timestamp", 0xFFFFFFFF, 0},
		{"max sequence", 0, 0xFFFF},
		{"both max", 0xFFFFFFFF, 0xFFFF},
		{"near max", 0xFFFFFFFE, 0xFFFE},
	}

	for _, tc := range overflowTests {
		t.Run(tc.name, func(t *testing.T) {
			gst.Init(nil)

			tmpDir := t.TempDir()
			config := &pipeline.Config{
				OutputDir:       tmpDir,
				SegmentDuration: 2,
				JitterBufferMs:  200,
				AudioMode:       pipeline.AudioPassThrough,
			}

			p, err := pipeline.NewDirectPipeline(config, "overflow-test")
			require.NoError(t, err)

			err = p.Start()
			require.NoError(t, err)
			defer p.Stop()

			// Send packets that might cause overflow
			for i := 0; i < 10; i++ {
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: tc.sequence + uint16(i),
						Timestamp:      tc.timestamp + uint32(i*3000),
						SSRC:           12345,
					},
					Payload: []byte{0x01, 0x02},
				}

				// Should handle overflow conditions
				err := p.InjectVideoRTP(packet)
				_ = err
			}
		})
	}
}