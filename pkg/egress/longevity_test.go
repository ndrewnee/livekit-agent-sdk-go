// +build longevity

package egress

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MemoryTracker tracks memory usage over time
type MemoryTracker struct {
	samples      []MemorySample
	startMem     runtime.MemStats
	goroutineStart int
}

type MemorySample struct {
	Time       time.Time
	AllocMB    uint64
	TotalAlloc uint64
	NumGC      uint32
	Goroutines int
	FDs        int // File descriptors
}

// NewMemoryTracker creates a memory tracker for leak detection
func NewMemoryTracker() *MemoryTracker {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return &MemoryTracker{
		startMem:       m,
		goroutineStart: runtime.NumGoroutine(),
		samples:        make([]MemorySample, 0, 1000),
	}
}

// Sample takes a memory sample
func (mt *MemoryTracker) Sample() MemorySample {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	sample := MemorySample{
		Time:       time.Now(),
		AllocMB:    m.Alloc / 1024 / 1024,
		TotalAlloc: m.TotalAlloc,
		NumGC:      m.NumGC,
		Goroutines: runtime.NumGoroutine(),
		FDs:        getOpenFileDescriptors(),
	}

	mt.samples = append(mt.samples, sample)
	return sample
}

// CheckForLeaks analyzes samples for memory/goroutine leaks
func (mt *MemoryTracker) CheckForLeaks() (bool, string) {
	if len(mt.samples) < 10 {
		return false, "insufficient samples"
	}

	// Check memory growth rate
	firstSamples := mt.samples[:10]
	lastSamples := mt.samples[len(mt.samples)-10:]

	var firstAvgMem, lastAvgMem uint64
	var firstAvgGoroutines, lastAvgGoroutines int

	for _, s := range firstSamples {
		firstAvgMem += s.AllocMB
		firstAvgGoroutines += s.Goroutines
	}
	firstAvgMem /= 10
	firstAvgGoroutines /= 10

	for _, s := range lastSamples {
		lastAvgMem += s.AllocMB
		lastAvgGoroutines += s.Goroutines
	}
	lastAvgMem /= 10
	lastAvgGoroutines /= 10

	// Check for significant memory growth (>100MB)
	memGrowth := int64(lastAvgMem) - int64(firstAvgMem)
	if memGrowth > 100 {
		return true, fmt.Sprintf("Memory leak detected: grew by %dMB", memGrowth)
	}

	// Check for goroutine leak (>100 goroutines)
	goroutineGrowth := lastAvgGoroutines - firstAvgGoroutines
	if goroutineGrowth > 100 {
		return true, fmt.Sprintf("Goroutine leak detected: grew by %d", goroutineGrowth)
	}

	// Check for FD leak
	if len(mt.samples) > 0 {
		lastFDs := mt.samples[len(mt.samples)-1].FDs
		firstFDs := mt.samples[0].FDs
		fdGrowth := lastFDs - firstFDs
		if fdGrowth > 100 {
			return true, fmt.Sprintf("File descriptor leak detected: grew by %d", fdGrowth)
		}
	}

	return false, "no leaks detected"
}

// TestLongRunningStability tests stability over extended period
func TestLongRunningStability(t *testing.T) {
	// This test runs for 1+ hours
	if testing.Short() {
		t.Skip("Skipping long-running test in short mode")
	}

	duration := getTestDuration()
	t.Logf("Running stability test for %v", duration)

	// Initialize GStreamer
	gst.Init(nil)

	// Create pipeline
	tmpDir := t.TempDir()
	config := &pipeline.Config{
		OutputDir:       tmpDir,
		SegmentDuration: 4,
		JitterBufferMs:  200,
		AudioMode:       pipeline.AudioPassThrough,
	}

	p, err := pipeline.NewDirectPipeline(config, "longevity-test")
	require.NoError(t, err)
	require.NotNil(t, p)

	// Start pipeline
	err = p.Start()
	require.NoError(t, err)
	defer p.Stop()

	// Create memory tracker
	tracker := NewMemoryTracker()

	// Statistics
	var (
		totalPackets  atomic.Uint64
		failedPackets atomic.Uint64
		totalErrors   atomic.Uint64
	)

	// Start packet injection goroutine
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	go func() {
		seq := uint16(0)
		timestamp := uint32(0)
		ticker := time.NewTicker(20 * time.Millisecond) // 50 packets/sec
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Video packet
				videoPacket := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: seq,
						Timestamp:      timestamp,
						SSRC:           12345,
					},
					Payload: generateH264NALUnit(),
				}

				if err := p.InjectVideoRTP(videoPacket); err != nil {
					failedPackets.Add(1)
					totalErrors.Add(1)
				} else {
					totalPackets.Add(1)
				}

				// Audio packet
				audioPacket := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    111,
						SequenceNumber: seq,
						Timestamp:      timestamp / 2,
						SSRC:           54321,
					},
					Payload: generateOpusFrame(),
				}

				if err := p.InjectAudioRTP(audioPacket); err != nil {
					failedPackets.Add(1)
					totalErrors.Add(1)
				} else {
					totalPackets.Add(1)
				}

				seq++
				timestamp += 3000
			}
		}
	}()

	// Memory sampling goroutine
	sampleTicker := time.NewTicker(30 * time.Second)
	defer sampleTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sampleTicker.C:
				sample := tracker.Sample()
				t.Logf("Memory: %dMB, Goroutines: %d, FDs: %d, Packets: %d",
					sample.AllocMB, sample.Goroutines, sample.FDs, totalPackets.Load())
			}
		}
	}()

	// Periodic health checks
	healthTicker := time.NewTicker(5 * time.Minute)
	defer healthTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Test completed
			goto completed
		case <-healthTicker.C:
			// Check pipeline health
			stats := p.GetStats()
			errorRate := float64(failedPackets.Load()) / float64(totalPackets.Load())

			// Log current state
			t.Logf("Health check - Packets: %d, Failed: %d, Error rate: %.2f%%",
				totalPackets.Load(), failedPackets.Load(), errorRate*100)
			t.Logf("Pipeline stats: Video: %d, Audio: %d, Segments: %d",
				stats.VideoPacketsReceived, stats.AudioPacketsReceived, stats.SegmentsWritten)

			// Check error threshold (should be < 1%)
			if errorRate > 0.01 {
				t.Errorf("Error rate %.2f%% exceeds 1%% threshold", errorRate*100)
			}

			// Take memory sample and check for leaks
			sample := tracker.Sample()
			if hasLeak, reason := tracker.CheckForLeaks(); hasLeak {
				t.Errorf("Leak detected after %v: %s", time.Since(tracker.samples[0].Time), reason)
			}

			// Force GC to check for cleanup
			runtime.GC()
			runtime.Gosched()

			// Verify segments are being written
			if stats.SegmentsWritten == 0 {
				t.Error("No segments written")
			}
		}
	}

completed:
	// Final statistics
	finalStats := p.GetStats()
	t.Logf("=== Final Statistics ===")
	t.Logf("Total packets sent: %d", totalPackets.Load())
	t.Logf("Failed packets: %d", failedPackets.Load())
	t.Logf("Total errors: %d", totalErrors.Load())
	t.Logf("Video packets received: %d", finalStats.VideoPacketsReceived)
	t.Logf("Audio packets received: %d", finalStats.AudioPacketsReceived)
	t.Logf("Segments written: %d", finalStats.SegmentsWritten)

	// Final leak check
	finalSample := tracker.Sample()
	t.Logf("Final memory: %dMB (started: %dMB)", finalSample.AllocMB, tracker.startMem.Alloc/1024/1024)
	t.Logf("Final goroutines: %d (started: %d)", finalSample.Goroutines, tracker.goroutineStart)

	if hasLeak, reason := tracker.CheckForLeaks(); hasLeak {
		t.Errorf("Memory leak in long-running test: %s", reason)
	}

	// Verify continuous operation
	assert.Greater(t, totalPackets.Load(), uint64(100000), "Should process >100k packets")
	assert.Less(t, float64(failedPackets.Load())/float64(totalPackets.Load()), 0.01, "Error rate should be <1%")
}

// TestMemoryUnderPressure tests behavior under memory pressure
func TestMemoryUnderPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory pressure test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	// Create multiple pipelines to increase memory pressure
	pipelines := make([]*pipeline.DirectPipeline, 0)
	defer func() {
		for _, p := range pipelines {
			p.Stop()
		}
	}()

	tmpDir := t.TempDir()

	// Track memory before creating pipelines
	var beforeMem runtime.MemStats
	runtime.ReadMemStats(&beforeMem)

	// Create pipelines until we hit memory pressure
	maxPipelines := 10
	for i := 0; i < maxPipelines; i++ {
		config := &pipeline.Config{
			OutputDir:       fmt.Sprintf("%s/pipeline-%d", tmpDir, i),
			SegmentDuration: 2,
			JitterBufferMs:  200,
			AudioMode:       pipeline.AudioPassThrough,
		}

		p, err := pipeline.NewDirectPipeline(config, fmt.Sprintf("pressure-test-%d", i))
		if err != nil {
			t.Logf("Failed to create pipeline %d: %v", i, err)
			break
		}

		err = p.Start()
		if err != nil {
			t.Logf("Failed to start pipeline %d: %v", i, err)
			p.Stop()
			break
		}

		pipelines = append(pipelines, p)

		// Check memory usage
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		allocMB := m.Alloc / 1024 / 1024

		t.Logf("Created pipeline %d, memory: %dMB", i, allocMB)

		// Inject some packets
		for j := 0; j < 100; j++ {
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: uint16(j),
					Timestamp:      uint32(j * 3000),
					SSRC:           uint32(12345 + i),
				},
				Payload: make([]byte, 1400),
			}
			p.InjectVideoRTP(packet)
		}

		// Force GC
		runtime.GC()
		runtime.Gosched()

		// Stop if memory usage is too high (>500MB)
		if allocMB > 500 {
			t.Logf("Stopping at %d pipelines due to memory limit", i+1)
			break
		}
	}

	// Let pipelines run for a bit
	time.Sleep(10 * time.Second)

	// Check final memory
	var afterMem runtime.MemStats
	runtime.ReadMemStats(&afterMem)

	memGrowthMB := (afterMem.Alloc - beforeMem.Alloc) / 1024 / 1024
	memPerPipeline := memGrowthMB / uint64(len(pipelines))

	t.Logf("Total memory growth: %dMB for %d pipelines", memGrowthMB, len(pipelines))
	t.Logf("Average memory per pipeline: %dMB", memPerPipeline)

	// Memory per pipeline should be reasonable (<50MB)
	assert.Less(t, memPerPipeline, uint64(50), "Memory per pipeline should be <50MB")
}

// TestGoroutineLeaks checks for goroutine leaks
func TestGoroutineLeaks(t *testing.T) {
	initialGoroutines := runtime.NumGoroutine()

	// Run multiple create/destroy cycles
	for cycle := 0; cycle < 10; cycle++ {
		func() {
			// Initialize GStreamer
			gst.Init(nil)

			tmpDir := t.TempDir()
			config := &pipeline.Config{
				OutputDir:       tmpDir,
				SegmentDuration: 2,
				JitterBufferMs:  200,
				AudioMode:       pipeline.AudioPassThrough,
			}

			// Create and start pipeline
			p, err := pipeline.NewDirectPipeline(config, fmt.Sprintf("leak-test-%d", cycle))
			require.NoError(t, err)

			err = p.Start()
			require.NoError(t, err)

			// Send some packets
			for i := 0; i < 1000; i++ {
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: uint16(i),
						Timestamp:      uint32(i * 3000),
						SSRC:           12345,
					},
					Payload: make([]byte, 100),
				}
				p.InjectVideoRTP(packet)
			}

			// Stop and cleanup
			err = p.Stop()
			assert.NoError(t, err)
		}()

		// Force cleanup
		runtime.GC()
		runtime.Gosched()
		time.Sleep(100 * time.Millisecond)

		// Check goroutine count
		currentGoroutines := runtime.NumGoroutine()
		t.Logf("Cycle %d: %d goroutines", cycle, currentGoroutines)

		// Should not grow significantly
		if currentGoroutines > initialGoroutines+50 {
			t.Errorf("Goroutine leak: started with %d, now %d", initialGoroutines, currentGoroutines)
		}
	}

	// Final check
	time.Sleep(1 * time.Second)
	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()

	t.Logf("Initial goroutines: %d, Final: %d", initialGoroutines, finalGoroutines)
	assert.LessOrEqual(t, finalGoroutines, initialGoroutines+10, "Should not leak goroutines")
}

// Helper functions

func getTestDuration() time.Duration {
	// Default 1 hour, can be overridden by env var
	durationStr := getEnv("LONGEVITY_TEST_DURATION", "1h")
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		return 1 * time.Hour
	}
	return duration
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getOpenFileDescriptors() int {
	// Platform-specific implementation
	// This is a simplified version
	return 0
}

func generateH264NALUnit() []byte {
	// Simplified H.264 NAL unit (SPS)
	return []byte{0x67, 0x42, 0x00, 0x1f, 0x96, 0x54, 0x05, 0x01, 0x7f, 0xcb}
}

func generateOpusFrame() []byte {
	// Simplified Opus frame
	return []byte{0xfc, 0xff, 0xfe}
}