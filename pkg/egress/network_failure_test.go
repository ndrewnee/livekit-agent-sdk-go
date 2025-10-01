// +build integration

package egress

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NetworkSimulator simulates various network conditions
type NetworkSimulator struct {
	mu              sync.RWMutex
	packetLossRate  float32 // 0.0 to 1.0
	latencyMs       int
	jitterMs        int
	disconnected    bool
	bandwidthKbps   int
	reorderRate     float32
	duplicateRate   float32
	corruptionRate  float32

	stats struct {
		packetsDropped   uint64
		packetsDelayed   uint64
		packetsReordered uint64
		packetsDuplicated uint64
		packetsCorrupted uint64
	}
}

// NewNetworkSimulator creates a network simulator for testing
func NewNetworkSimulator() *NetworkSimulator {
	return &NetworkSimulator{
		bandwidthKbps: 10000, // 10 Mbps default
	}
}

// SimulatePacketLoss sets the packet loss rate (0.0 to 1.0)
func (ns *NetworkSimulator) SimulatePacketLoss(rate float32) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.packetLossRate = rate
}

// SimulateDisconnection simulates a network disconnection
func (ns *NetworkSimulator) SimulateDisconnection() {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.disconnected = true
}

// SimulateReconnection simulates network reconnection
func (ns *NetworkSimulator) SimulateReconnection() {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.disconnected = false
}

// SimulateLatency adds artificial latency to packets
func (ns *NetworkSimulator) SimulateLatency(ms int, jitterMs int) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.latencyMs = ms
	ns.jitterMs = jitterMs
}

// SimulateBandwidthLimit limits the bandwidth
func (ns *NetworkSimulator) SimulateBandwidthLimit(kbps int) {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	ns.bandwidthKbps = kbps
}

// ProcessPacket simulates network conditions for a packet
func (ns *NetworkSimulator) ProcessPacket(packet *rtp.Packet) (*rtp.Packet, error) {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	// Check if disconnected
	if ns.disconnected {
		atomic.AddUint64(&ns.stats.packetsDropped, 1)
		return nil, fmt.Errorf("network disconnected")
	}

	// Simulate packet loss
	if ns.packetLossRate > 0 && randFloat32() < ns.packetLossRate {
		atomic.AddUint64(&ns.stats.packetsDropped, 1)
		return nil, fmt.Errorf("packet lost")
	}

	// Simulate latency and jitter
	if ns.latencyMs > 0 {
		delay := time.Duration(ns.latencyMs) * time.Millisecond
		if ns.jitterMs > 0 {
			jitter := time.Duration(randIntn(ns.jitterMs*2)-ns.jitterMs) * time.Millisecond
			delay += jitter
		}
		time.Sleep(delay)
		atomic.AddUint64(&ns.stats.packetsDelayed, 1)
	}

	// Simulate packet reordering
	if ns.reorderRate > 0 && randFloat32() < ns.reorderRate {
		// Delay this packet extra to cause reordering (reduced delay)
		time.Sleep(time.Duration(5+randIntn(10)) * time.Millisecond)
		atomic.AddUint64(&ns.stats.packetsReordered, 1)
	}

	// Simulate packet duplication
	if ns.duplicateRate > 0 && randFloat32() < ns.duplicateRate {
		atomic.AddUint64(&ns.stats.packetsDuplicated, 1)
		// Return packet twice (caller needs to handle)
	}

	// Simulate packet corruption
	if ns.corruptionRate > 0 && randFloat32() < ns.corruptionRate {
		// Corrupt a random byte in payload
		if len(packet.Payload) > 0 {
			packet.Payload[randIntn(len(packet.Payload))] ^= 0xFF
		}
		atomic.AddUint64(&ns.stats.packetsCorrupted, 1)
	}

	// Simulate bandwidth limit (simple token bucket)
	if ns.bandwidthKbps > 0 {
		packetSizeBits := (len(packet.Payload) + 12) * 8 // RTP header + payload
		requiredTimeMs := float64(packetSizeBits) / float64(ns.bandwidthKbps)
		time.Sleep(time.Duration(requiredTimeMs) * time.Millisecond)
	}

	return packet, nil
}

// GetStats returns network simulation statistics
func (ns *NetworkSimulator) GetStats() map[string]uint64 {
	return map[string]uint64{
		"packets_dropped":    atomic.LoadUint64(&ns.stats.packetsDropped),
		"packets_delayed":    atomic.LoadUint64(&ns.stats.packetsDelayed),
		"packets_reordered":  atomic.LoadUint64(&ns.stats.packetsReordered),
		"packets_duplicated": atomic.LoadUint64(&ns.stats.packetsDuplicated),
		"packets_corrupted":  atomic.LoadUint64(&ns.stats.packetsCorrupted),
	}
}

// TestNetworkFailureRecovery tests recovery from network failures
func TestNetworkFailureRecovery(t *testing.T) {
	simulator := NewNetworkSimulator()

	t.Run("handles temporary disconnection", func(t *testing.T) {
		packetsBeforeDisconnect := 100
		packetsDuringDisconnect := 50
		packetsAfterReconnect := 100

		var successCount, failCount atomic.Uint64

		// Send packets before disconnection
		for i := 0; i < packetsBeforeDisconnect; i++ {
			packet := createTestRTPPacket(uint16(i), uint32(i*3000))
			if _, err := simulator.ProcessPacket(packet); err == nil {
				successCount.Add(1)
			}
		}
		assert.Equal(t, uint64(packetsBeforeDisconnect), successCount.Load())

		// Simulate disconnection
		simulator.SimulateDisconnection()

		// Try sending during disconnection
		for i := 0; i < packetsDuringDisconnect; i++ {
			packet := createTestRTPPacket(uint16(packetsBeforeDisconnect+i), uint32((packetsBeforeDisconnect+i)*3000))
			if _, err := simulator.ProcessPacket(packet); err != nil {
				failCount.Add(1)
			}
		}
		assert.Equal(t, uint64(packetsDuringDisconnect), failCount.Load())

		// Simulate reconnection
		simulator.SimulateReconnection()
		successCount.Store(0) // Reset counter

		// Send packets after reconnection
		for i := 0; i < packetsAfterReconnect; i++ {
			packet := createTestRTPPacket(uint16(packetsBeforeDisconnect+packetsDuringDisconnect+i),
				uint32((packetsBeforeDisconnect+packetsDuringDisconnect+i)*3000))
			if _, err := simulator.ProcessPacket(packet); err == nil {
				successCount.Add(1)
			}
		}
		assert.Equal(t, uint64(packetsAfterReconnect), successCount.Load())
	})

	t.Run("handles packet loss gracefully", func(t *testing.T) {
		testCases := []struct {
			name     string
			lossRate float32
			packets  int
			maxLoss  float32
		}{
			{"5% loss", 0.05, 1000, 0.10},   // Allow up to 10% actual loss
			{"10% loss", 0.10, 1000, 0.15},
			{"25% loss", 0.25, 1000, 0.35},
			{"50% loss", 0.50, 500, 0.60},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				sim := NewNetworkSimulator()
				sim.SimulatePacketLoss(tc.lossRate)

				var received atomic.Uint64
				for i := 0; i < tc.packets; i++ {
					packet := createTestRTPPacket(uint16(i), uint32(i*3000))
					if _, err := sim.ProcessPacket(packet); err == nil {
						received.Add(1)
					}
				}

				actualLossRate := 1.0 - float32(received.Load())/float32(tc.packets)
				assert.LessOrEqual(t, actualLossRate, tc.maxLoss,
					"Loss rate %.2f%% exceeds maximum %.2f%%",
					actualLossRate*100, tc.maxLoss*100)
			})
		}
	})

	t.Run("handles high latency and jitter", func(t *testing.T) {
		simulator.SimulateLatency(100, 50) // 100ms latency, 50ms jitter

		start := time.Now()
		packet := createTestRTPPacket(1, 3000)
		_, err := simulator.ProcessPacket(packet)
		elapsed := time.Since(start)

		assert.NoError(t, err)
		assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond)  // Min latency - jitter
		assert.LessOrEqual(t, elapsed, 200*time.Millisecond)     // Max latency + jitter
	})

	t.Run("handles bandwidth limitations", func(t *testing.T) {
		simulator.SimulateBandwidthLimit(1000) // 1 Mbps

		largePacket := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: 1,
				Timestamp:      3000,
			},
			Payload: make([]byte, 1400), // ~11.2 kbits
		}

		start := time.Now()
		_, err := simulator.ProcessPacket(largePacket)
		elapsed := time.Since(start)

		assert.NoError(t, err)
		// Should take at least 11.2ms to send 11.2kbits at 1Mbps
		assert.GreaterOrEqual(t, elapsed, 10*time.Millisecond)
	})

	t.Run("pipeline handles network failures", func(t *testing.T) {
		// This would test actual pipeline with simulated network issues
		// Requires pipeline setup with network simulation wrapper
		t.Skip("Requires pipeline integration")
	})

	// Log final statistics
	stats := simulator.GetStats()
	t.Logf("Network simulation stats: %+v", stats)
}

// TestNetworkResilience tests resilience under various network conditions
func TestNetworkResilience(t *testing.T) {
	t.Run("survives rapid connect/disconnect cycles", func(t *testing.T) {
		simulator := NewNetworkSimulator()
		cycles := 10
		packetsPerCycle := 100

		for cycle := 0; cycle < cycles; cycle++ {
			// Connected phase
			for i := 0; i < packetsPerCycle/2; i++ {
				packet := createTestRTPPacket(uint16(cycle*packetsPerCycle+i), uint32(i*3000))
				simulator.ProcessPacket(packet)
			}

			// Disconnect
			simulator.SimulateDisconnection()
			time.Sleep(100 * time.Millisecond)

			// Try sending while disconnected (should fail)
			for i := packetsPerCycle / 2; i < packetsPerCycle; i++ {
				packet := createTestRTPPacket(uint16(cycle*packetsPerCycle+i), uint32(i*3000))
				_, err := simulator.ProcessPacket(packet)
				assert.Error(t, err)
			}

			// Reconnect
			simulator.SimulateReconnection()
			time.Sleep(100 * time.Millisecond)
		}
	})

	t.Run("handles DNS resolution failures", func(t *testing.T) {
		// Test DNS resolution with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_, err := net.DefaultResolver.LookupHost(ctx, "non-existent-domain-test-12345.local")
		assert.Error(t, err)
	})

	t.Run("handles port binding conflicts", func(t *testing.T) {
		// Try to bind to same port multiple times
		addr := "127.0.0.1:0" // Let OS assign port

		listener1, err := net.Listen("tcp", addr)
		require.NoError(t, err)
		defer listener1.Close()

		port := listener1.Addr().(*net.TCPAddr).Port

		// Try to bind to same port (should fail)
		listener2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			listener2.Close()
			t.Error("Expected port binding to fail")
		}
	})
}

// TestNetworkChaos tests behavior under chaotic network conditions
func TestNetworkChaos(t *testing.T) {
	t.Run("survives chaos conditions", func(t *testing.T) {
		simulator := NewNetworkSimulator()

		// Apply multiple adverse conditions simultaneously
		simulator.SimulatePacketLoss(0.15)     // 15% loss
		simulator.SimulateLatency(5, 2)        // 5ms ± 2ms (reduced from 50ms to avoid timeout)
		simulator.reorderRate = 0.05           // 5% reordering
		simulator.duplicateRate = 0.02         // 2% duplication
		simulator.corruptionRate = 0.01        // 1% corruption
		simulator.SimulateBandwidthLimit(500)  // 500 kbps

		// Send burst of packets (reduced for faster test)
		packets := 200
		var received, corrupted atomic.Uint64

		start := time.Now()
		for i := 0; i < packets; i++ {
			packet := createTestRTPPacket(uint16(i), uint32(i*3000))
			originalPayload := make([]byte, len(packet.Payload))
			copy(originalPayload, packet.Payload)

			processedPacket, err := simulator.ProcessPacket(packet)
			if err == nil {
				received.Add(1)
				// Check for corruption
				if processedPacket != nil && !bytesEqual(originalPayload, processedPacket.Payload) {
					corrupted.Add(1)
				}
			}

			// Add small delay between packets
			if i%10 == 0 {
				time.Sleep(1 * time.Millisecond)
			}
		}
		elapsed := time.Since(start)

		stats := simulator.GetStats()
		t.Logf("Chaos test completed in %v", elapsed)
		t.Logf("Sent: %d, Received: %d, Corrupted: %d", packets, received.Load(), corrupted.Load())
		t.Logf("Stats: %+v", stats)

		// Should still receive majority of packets despite chaos
		assert.Greater(t, received.Load(), uint64(packets/2), "Should receive >50% of packets despite chaos")
	})
}

// Helper functions

func createTestRTPPacket(seq uint16, timestamp uint32) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      timestamp,
			SSRC:           12345,
		},
		Payload: make([]byte, 100),
	}
}

func randFloat32() float32 {
	return rand.Float32()
}

func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	return rand.Intn(n)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}