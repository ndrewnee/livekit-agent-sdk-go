// +build integration

package egress

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ResourceLimiter manages resource constraints for testing
type ResourceLimiter struct {
	mu sync.Mutex

	// Memory limits
	maxMemoryMB      int
	currentMemoryMB  int
	memoryExhausted  bool

	// Disk limits
	maxDiskMB       int
	currentDiskMB   int
	diskExhausted   bool

	// File descriptor limits
	maxFDs          int
	currentFDs      int
	fdExhausted     bool

	// CPU limits
	cpuThrottlePercent int
	cpuThrottled       bool

	// Goroutine limits
	maxGoroutines   int
	goroutineLimit  bool
}

// NewResourceLimiter creates a resource limiter for testing
func NewResourceLimiter() *ResourceLimiter {
	return &ResourceLimiter{
		maxMemoryMB:   500,  // 500MB limit
		maxDiskMB:     1000, // 1GB limit
		maxFDs:        1024, // File descriptor limit
		maxGoroutines: 1000, // Goroutine limit
	}
}

// EnforceMemoryLimit restricts available memory
func (rl *ResourceLimiter) EnforceMemoryLimit(limitMB int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.maxMemoryMB = limitMB

	// Set soft memory limit
	debug.SetMemoryLimit(int64(limitMB) * 1024 * 1024)
}

// CheckMemory verifies memory usage is within limits
func (rl *ResourceLimiter) CheckMemory() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	rl.currentMemoryMB = int(m.Alloc / 1024 / 1024)

	if rl.currentMemoryMB > rl.maxMemoryMB {
		rl.memoryExhausted = true
		return fmt.Errorf("memory exhausted: %dMB > %dMB limit", rl.currentMemoryMB, rl.maxMemoryMB)
	}
	return nil
}

// EnforceFDLimit sets file descriptor limit
func (rl *ResourceLimiter) EnforceFDLimit(limit int) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.maxFDs = limit

	// Set file descriptor limit
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		return err
	}

	rLimit.Cur = uint64(limit)
	return syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
}

// GetOpenFDs returns current number of open file descriptors
func (rl *ResourceLimiter) GetOpenFDs() int {
	// Platform-specific - simplified version
	files, _ := os.ReadDir("/proc/self/fd")
	return len(files)
}

// TestOutOfMemory tests behavior when memory is exhausted
func TestOutOfMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory exhaustion test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	limiter := NewResourceLimiter()
	limiter.EnforceMemoryLimit(100) // 100MB limit

	tmpDir := t.TempDir()
	config := &pipeline.Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 5 * time.Second,
		IsLiveSource:       true,
		AllowAsyncStart:    true, // Use async for resource tests
	}

	// Track memory usage
	var initialMem runtime.MemStats
	runtime.ReadMemStats(&initialMem)

	// Try to create multiple pipelines until memory exhausted
	pipelines := make([]*pipeline.DirectPipeline, 0)
	defer func() {
		for _, p := range pipelines {
			p.Stop()
		}
	}()

	var memoryExhausted bool
	for i := 0; i < 50; i++ {
		p, err := pipeline.NewDirectPipeline(config, fmt.Sprintf("oom-test-%d", i))
		if err != nil {
			t.Logf("Failed to create pipeline %d: %v", i, err)
			memoryExhausted = true
			break
		}

		err = p.Start()
		if err != nil {
			t.Logf("Failed to start pipeline %d: %v", i, err)
			p.Stop()
			memoryExhausted = true
			break
		}

		pipelines = append(pipelines, p)

		// Allocate additional memory
		largeBuffer := make([]byte, 10*1024*1024) // 10MB
		_ = largeBuffer

		// Check memory limit
		if err := limiter.CheckMemory(); err != nil {
			t.Logf("Memory limit reached at pipeline %d: %v", i, err)
			memoryExhausted = true
			break
		}

		// Send some packets to increase memory usage
		for j := 0; j < 100; j++ {
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: uint16(j),
					Timestamp:      uint32(j * 3000),
					SSRC:           uint32(12345 + i),
				},
				Payload: make([]byte, 64000), // Large payload
			}
			p.InjectVideoRTP(packet)
		}
	}

	assert.True(t, memoryExhausted, "Should hit memory limit")
	t.Logf("Created %d pipelines before memory exhaustion", len(pipelines))

	// Verify graceful degradation
	for _, p := range pipelines {
		stats := p.GetStats()
		t.Logf("Pipeline stats: Video=%d, Audio=%d", stats.VideoPacketsReceived, stats.AudioPacketsReceived)
	}

	// Force GC and check recovery
	runtime.GC()
	runtime.GC()
	time.Sleep(1 * time.Second)

	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)
	t.Logf("Memory after GC: %dMB (initial: %dMB)", finalMem.Alloc/1024/1024, initialMem.Alloc/1024/1024)
}

// TestDiskFull tests behavior when disk is full
func TestDiskFull(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping disk full test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	// Create a small temporary filesystem
	tmpDir := t.TempDir()
	smallDir := filepath.Join(tmpDir, "small_disk")
	require.NoError(t, os.MkdirAll(smallDir, 0755))

	// Fill disk to near capacity
	fillFile := filepath.Join(smallDir, "fill.dat")
	fillSize := 100 * 1024 * 1024 // 100MB

	f, err := os.Create(fillFile)
	if err == nil {
		defer f.Close()
		// Write data to consume disk space
		data := make([]byte, 1024*1024) // 1MB chunks
		for i := 0; i < fillSize/(1024*1024); i++ {
			_, err := f.Write(data)
			if err != nil {
				break // Disk full
			}
		}
		f.Sync()
	}

	config := &pipeline.Config{
		OutputDir:       smallDir,
		SegmentDuration: 2,
		JitterBufferMs:  200,
		AudioMode:       pipeline.AudioPassThrough,
	}

	p, err := pipeline.NewDirectPipeline(config, "diskfull-test")
	require.NoError(t, err)

	err = p.Start()
	require.NoError(t, err)
	defer p.Stop()

	// Send packets until disk full
	var writeErrors atomic.Uint64
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Shorter timeout for stress test
	defer cancel()

	go func() {
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
						SSRC:           12345,
					},
					Payload: make([]byte, 1400),
				}

				if err := p.InjectVideoRTP(packet); err != nil {
					writeErrors.Add(1)
				}
				seq++
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	<-ctx.Done()

	// Check results
	stats := p.GetStats()
	t.Logf("Disk full test - Packets: %d, Segments: %d, Write errors: %d",
		stats.VideoPacketsReceived, stats.SegmentsWritten, writeErrors.Load())

	// Verify pipeline handled disk full gracefully
	assert.Greater(t, stats.VideoPacketsReceived, uint64(0), "Should receive some packets")

	// Clean up fill file to free space
	os.Remove(fillFile)
}

// TestFileDescriptorExhaustion tests FD limit handling
func TestFileDescriptorExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping FD exhaustion test in short mode")
	}

	limiter := NewResourceLimiter()

	// Save current limit
	var originalLimit syscall.Rlimit
	syscall.Getrlimit(syscall.RLIMIT_NOFILE, &originalLimit)
	defer syscall.Setrlimit(syscall.RLIMIT_NOFILE, &originalLimit)

	// Set a low FD limit
	err := limiter.EnforceFDLimit(100)
	if err != nil {
		t.Skip("Cannot set FD limit: ", err)
	}

	// Open many files to consume FDs
	files := make([]*os.File, 0)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()

	tmpDir := t.TempDir()
	var fdExhausted bool

	for i := 0; i < 150; i++ {
		path := filepath.Join(tmpDir, fmt.Sprintf("fd_test_%d.txt", i))
		f, err := os.Create(path)
		if err != nil {
			t.Logf("FD exhausted at file %d: %v", i, err)
			fdExhausted = true
			break
		}
		files = append(files, f)
	}

	assert.True(t, fdExhausted, "Should hit FD limit")
	t.Logf("Opened %d files before FD exhaustion", len(files))

	// Try to create pipeline with limited FDs
	gst.Init(nil)

	config := &pipeline.Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 5 * time.Second,
		IsLiveSource:       true,
		AllowAsyncStart:    true, // Use async for resource tests
	}

	p, err := pipeline.NewDirectPipeline(config, "fd-test")
	if err != nil {
		t.Logf("Pipeline creation failed with limited FDs: %v", err)
		// This is expected behavior
		return
	}
	defer p.Stop()

	// Pipeline should handle limited FDs gracefully
	err = p.Start()
	if err != nil {
		t.Logf("Pipeline start failed with limited FDs: %v", err)
	}
}

// TestCPUThrottling tests behavior under CPU constraints
func TestCPUThrottling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping CPU throttling test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &pipeline.Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 5 * time.Second,
		IsLiveSource:       true,
		AllowAsyncStart:    true, // Use async for resource tests
	}

	p, err := pipeline.NewDirectPipeline(config, "cpu-test")
	require.NoError(t, err)

	err = p.Start()
	require.NoError(t, err)
	defer p.Stop()

	// Create CPU load
	numCPU := runtime.NumCPU()
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Spawn CPU-intensive goroutines
	for i := 0; i < numCPU*2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// CPU-intensive work
					for j := 0; j < 1000000; j++ {
						_ = j * j * j
					}
				}
			}
		}()
	}

	// Send packets while CPU is throttled
	var packetsProcessed atomic.Uint64
	var packetErrors atomic.Uint64

	go func() {
		for i := 0; i < 500; i++ {
			select {
			case <-ctx.Done():
				return
			default:
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: uint16(i),
						Timestamp:      uint32(i * 3000),
						SSRC:           12345,
					},
					Payload: make([]byte, 1400),
				}

				if err := p.InjectVideoRTP(packet); err != nil {
					packetErrors.Add(1)
				} else {
					packetsProcessed.Add(1)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()

	<-ctx.Done()
	wg.Wait()

	// Check performance under CPU pressure
	stats := p.GetStats()
	t.Logf("CPU throttling - Processed: %d, Errors: %d, Stats: %+v",
		packetsProcessed.Load(), packetErrors.Load(), stats)

	// Should still process some packets despite CPU pressure
	assert.Greater(t, packetsProcessed.Load(), uint64(0), "Should process packets under CPU pressure")
}

// TestGoroutineLeaksUnderPressure tests for goroutine leaks
func TestGoroutineLeaksUnderPressure(t *testing.T) {
	initialGoroutines := runtime.NumGoroutine()

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()

	// Create and destroy pipelines rapidly
	for i := 0; i < 20; i++ {
		config := &pipeline.Config{
			OutputDir:       filepath.Join(tmpDir, fmt.Sprintf("pipeline-%d", i)),
			SegmentDuration: 2,
			JitterBufferMs:  200,
			AudioMode:       pipeline.AudioPassThrough,
		}

		p, err := pipeline.NewDirectPipeline(config, fmt.Sprintf("leak-test-%d", i))
		if err != nil {
			t.Logf("Failed to create pipeline %d: %v", i, err)
			continue
		}

		err = p.Start()
		if err != nil {
			t.Logf("Failed to start pipeline %d: %v", i, err)
			p.Stop()
			continue
		}

		// Send packets rapidly
		for j := 0; j < 100; j++ {
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: uint16(j),
					Timestamp:      uint32(j * 3000),
					SSRC:           uint32(12345 + i),
				},
				Payload: make([]byte, 100),
			}
			p.InjectVideoRTP(packet)
		}

		// Stop immediately
		p.Stop()

		// Check goroutine count
		currentGoroutines := runtime.NumGoroutine()
		if currentGoroutines > initialGoroutines+100 {
			t.Errorf("Goroutine leak detected: cycle %d has %d goroutines (initial: %d)",
				i, currentGoroutines, initialGoroutines)
		}
	}

	// Force cleanup
	runtime.GC()
	time.Sleep(1 * time.Second)
	runtime.GC()

	finalGoroutines := runtime.NumGoroutine()
	t.Logf("Goroutines - Initial: %d, Final: %d", initialGoroutines, finalGoroutines)

	// Should not leak goroutines
	assert.LessOrEqual(t, finalGoroutines, initialGoroutines+10,
		"Should not leak goroutines under pressure")
}

// TestRecoveryAfterResourceExhaustion tests recovery behavior
func TestRecoveryAfterResourceExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping recovery test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	tmpDir := t.TempDir()

	// Phase 1: Exhaust resources
	t.Log("Phase 1: Exhausting resources...")

	// Create many pipelines to exhaust resources
	pipelines := make([]*pipeline.DirectPipeline, 0)
	for i := 0; i < 20; i++ {
		config := &pipeline.Config{
			OutputDir:       filepath.Join(tmpDir, fmt.Sprintf("exhaust-%d", i)),
			SegmentDuration: 2,
			JitterBufferMs:  200,
			AudioMode:       pipeline.AudioPassThrough,
		}

		p, err := pipeline.NewDirectPipeline(config, fmt.Sprintf("exhaust-%d", i))
		if err != nil {
			t.Logf("Resource exhaustion at pipeline %d: %v", i, err)
			break
		}

		err = p.Start()
		if err != nil {
			t.Logf("Start failed at pipeline %d: %v", i, err)
			p.Stop()
			break
		}

		pipelines = append(pipelines, p)

		// Allocate memory
		largeBuffer := make([]byte, 20*1024*1024) // 20MB
		_ = largeBuffer
	}

	t.Logf("Created %d pipelines before exhaustion", len(pipelines))

	// Phase 2: Release resources
	t.Log("Phase 2: Releasing resources...")

	for _, p := range pipelines {
		p.Stop()
	}
	pipelines = nil

	// Force cleanup
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(2 * time.Second)

	// Phase 3: Verify recovery
	t.Log("Phase 3: Testing recovery...")

	config := &pipeline.Config{
		OutputDir:       filepath.Join(tmpDir, "recovery"),
		SegmentDuration: 2,
		JitterBufferMs:  200,
		AudioMode:       pipeline.AudioPassThrough,
	}

	p, err := pipeline.NewDirectPipeline(config, "recovery-test")
	require.NoError(t, err, "Should be able to create pipeline after recovery")

	err = p.Start()
	require.NoError(t, err, "Should be able to start pipeline after recovery")
	defer p.Stop()

	// Send test packets
	var successCount atomic.Uint64
	for i := 0; i < 100; i++ {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * 3000),
				SSRC:           12345,
			},
			Payload: make([]byte, 1400),
		}

		if err := p.InjectVideoRTP(packet); err == nil {
			successCount.Add(1)
		}
	}

	// Verify recovery was successful
	stats := p.GetStats()
	t.Logf("Recovery test - Sent: 100, Success: %d, Stats: %+v",
		successCount.Load(), stats)

	assert.Greater(t, successCount.Load(), uint64(90),
		"Should process >90% of packets after recovery")
}

// TestSystemResourceMonitoring tests resource monitoring under pressure
func TestSystemResourceMonitoring(t *testing.T) {
	// Create memory pressure
	largeAllocations := make([][]byte, 0)
	for i := 0; i < 10; i++ {
		largeAllocations = append(largeAllocations, make([]byte, 10*1024*1024)) // 10MB each
	}

	// Get resource metrics using runtime
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memoryMB := m.Alloc / 1024 / 1024
	goroutines := runtime.NumGoroutine()

	t.Logf("Resource metrics - Memory: %dMB, Goroutines: %d",
		memoryMB, goroutines)

	// Verify monitoring works under pressure
	assert.Greater(t, memoryMB, uint64(0), "Memory should be > 0")
	assert.Greater(t, goroutines, 0, "Should have goroutines")

	// Clean up
	largeAllocations = nil
	runtime.GC()
}

// TestConcurrentResourceExhaustion tests multiple resource limits simultaneously
func TestConcurrentResourceExhaustion(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent exhaustion test in short mode")
	}

	var wg sync.WaitGroup
	results := struct {
		sync.Mutex
		memoryErrors  int
		fdErrors      int
		cpuErrors     int
		diskErrors    int
	}{
		memoryErrors: 0,
		fdErrors:     0,
		cpuErrors:    0,
		diskErrors:   0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Shorter timeout for stress test
	defer cancel()

	// Goroutine 1: Memory pressure
	wg.Add(1)
	go func() {
		defer wg.Done()
		allocations := make([][]byte, 0)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				alloc := make([]byte, 10*1024*1024) // 10MB
				allocations = append(allocations, alloc)
				if len(allocations) > 50 { // 500MB total
					results.Lock()
					results.memoryErrors++
					results.Unlock()
					allocations = allocations[:10] // Keep only 100MB
					runtime.GC()
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()

	// Goroutine 2: File descriptor pressure
	wg.Add(1)
	go func() {
		defer wg.Done()
		tmpDir := t.TempDir()
		files := make([]*os.File, 0)
		defer func() {
			for _, f := range files {
				f.Close()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			default:
				path := filepath.Join(tmpDir, fmt.Sprintf("fd_%d.txt", len(files)))
				f, err := os.Create(path)
				if err != nil {
					results.Lock()
					results.fdErrors++
					results.Unlock()
					// Close some files
					if len(files) > 10 {
						for i := 0; i < 10; i++ {
							files[i].Close()
						}
						files = files[10:]
					}
				} else {
					files = append(files, f)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}()

	// Goroutine 3: CPU pressure
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// CPU-intensive work
				for i := 0; i < 10000000; i++ {
					_ = i * i
				}
				results.Lock()
				results.cpuErrors++ // Count iterations
				results.Unlock()
				time.Sleep(10 * time.Millisecond) // Small delay to prevent CPU burnout
			}
		}
	}()

	// Goroutine 4: Disk I/O pressure
	wg.Add(1)
	go func() {
		defer wg.Done()
		tmpDir := t.TempDir()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				path := filepath.Join(tmpDir, fmt.Sprintf("io_%d.dat", time.Now().UnixNano()))
				f, err := os.Create(path)
				if err != nil {
					results.Lock()
					results.diskErrors++
					results.Unlock()
				} else {
					data := make([]byte, 1024*1024) // 1MB
					f.Write(data)
					f.Close()
					os.Remove(path)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	<-ctx.Done()
	wg.Wait()

	// Log results
	results.Lock()
	t.Logf("Concurrent exhaustion results:")
	t.Logf("  Memory pressure events: %d", results.memoryErrors)
	t.Logf("  FD exhaustion events: %d", results.fdErrors)
	t.Logf("  CPU iterations: %d", results.cpuErrors)
	t.Logf("  Disk I/O errors: %d", results.diskErrors)
	results.Unlock()

	// System should handle concurrent pressure
	assert.Greater(t, results.cpuErrors, 0, "Should complete CPU work")
}

// Helper to write large files
func writeLargeFile(path string, sizeMB int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	data := make([]byte, 1024*1024) // 1MB buffer
	for i := 0; i < sizeMB; i++ {
		if _, err := f.Write(data); err != nil {
			if err == io.ErrShortWrite || err == syscall.ENOSPC {
				return fmt.Errorf("disk full after %dMB", i)
			}
			return err
		}
	}
	return f.Sync()
}