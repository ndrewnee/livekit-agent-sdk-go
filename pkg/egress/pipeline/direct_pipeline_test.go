package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectPipelineCreation(t *testing.T) {
	// Initialize GStreamer for testing
	gst.Init(nil)

	tmpDir := t.TempDir()

	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		EnableScreenshots:  false,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
		AACBitrate:         192,
		MP3Bitrate:         192,
	}

	pipeline, err := NewDirectPipeline(config, "test-session")
	require.NoError(t, err)
	assert.NotNil(t, pipeline)

	// Verify output directory was created
	outputDir := filepath.Join(tmpDir, "test-session")
	_, err = os.Stat(outputDir)
	assert.NoError(t, err)

	// Clean up
	pipeline.Stop()
}

func TestDirectPipelineStartStop(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	pipeline, err := NewDirectPipeline(config, "test-start-stop")
	require.NoError(t, err)

	// Start pipeline
	err = pipeline.Start()
	assert.NoError(t, err)

	// Send test packets to trigger state transition
	go func() {
		time.Sleep(100 * time.Millisecond)
		GenerateTestRTPPackets(pipeline, 5)
	}()

	// Wait for state to reach PLAYING
	playing := WaitForState(pipeline, StatePlaying, 2*time.Second)
	if !playing {
		// For live pipelines without data, PAUSED is also acceptable
		state := pipeline.GetState()
		assert.Contains(t, []State{StatePlaying, StatePaused}, state,
			"Pipeline should be in PLAYING or PAUSED state")
	} else {
		assert.Equal(t, StatePlaying, pipeline.GetState())
	}

	// Stop pipeline
	err = pipeline.Stop()
	assert.NoError(t, err)

	// Check state after stop
	state := pipeline.GetState()
	assert.Equal(t, StateStopped, state)
}

func TestDirectPipelineRTPInjection(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()

	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	pipeline, err := NewDirectPipeline(config, "test-rtp")
	require.NoError(t, err)

	// Start pipeline
	err = pipeline.Start()
	require.NoError(t, err)
	defer pipeline.Stop()

	// Wait a bit for pipeline to stabilize
	time.Sleep(200 * time.Millisecond)

	// Create test RTP packets
	videoPacket := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: 1000,
			Timestamp:      90000,
			SSRC:           12345678,
		},
		Payload: []byte{0x00, 0x00, 0x00, 0x01, 0x67}, // Minimal H.264 SPS NAL
	}

	audioPacket := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: 2000,
			Timestamp:      48000,
			SSRC:           87654321,
		},
		Payload: []byte{0x00, 0x01}, // Minimal Opus payload
	}

	// Wait for pipeline to be ready
	time.Sleep(500 * time.Millisecond)

	// Check pipeline state before injection
	state := pipeline.GetState()
	t.Logf("Pipeline state before injection: %v", state)

	// Inject packets
	err = pipeline.InjectVideoRTP(videoPacket)
	if err != nil {
		t.Logf("Video injection error: %v", err)
	}
	assert.NoError(t, err)

	err = pipeline.InjectAudioRTP(audioPacket)
	if err != nil {
		t.Logf("Audio injection error: %v", err)
	}
	assert.NoError(t, err)

	// Check statistics
	stats := pipeline.GetStats()
	assert.Equal(t, uint64(1), stats.VideoPacketsReceived)
	assert.Equal(t, uint64(1), stats.AudioPacketsReceived)
}

func TestDirectPipelineMultipleWorkers(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	// Create multiple pipelines (workers) - this should work without port conflicts
	numWorkers := 5
	pipelines := make([]*DirectPipeline, numWorkers)

	for i := 0; i < numWorkers; i++ {
		sessionID := fmt.Sprintf("worker-%d", i)
		pipeline, err := NewDirectPipeline(config, sessionID)
		require.NoError(t, err, "Failed to create pipeline %d", i)
		pipelines[i] = pipeline

		err = pipeline.Start()
		assert.NoError(t, err, "Failed to start pipeline %d", i)
	}

	// All pipelines should be running without conflicts
	// For live pipelines without data, PAUSED state is acceptable
	for i, pipeline := range pipelines {
		state := pipeline.GetState()
		assert.Contains(t, []State{StatePlaying, StatePaused}, state,
			"Pipeline %d should be in PLAYING or PAUSED state", i)
	}

	// Clean up
	for _, pipeline := range pipelines {
		pipeline.Stop()
	}
}

func TestDirectPipelineAudioModes(t *testing.T) {
	gst.Init(nil)

	tests := []struct {
		name      string
		audioMode AudioMode
	}{
		{"PassThrough", AudioPassThrough},
		{"TranscodeAAC", AudioTranscodeAAC},
		{"TranscodeMP3", AudioTranscodeMP3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			config := &Config{
				OutputDir:          tmpDir,
				SegmentDuration:    2,
				JitterBufferMs:     200,
				AudioMode:          tt.audioMode,
				AACBitrate:         192,
				StateChangeTimeout: 10 * time.Second,
				IsLiveSource:       true,
				MP3Bitrate:      192,
			}

			pipeline, err := NewDirectPipeline(config, fmt.Sprintf("test-%s", tt.name))
			require.NoError(t, err, "Failed to create pipeline for mode %s", tt.name)

			err = pipeline.Start()
			assert.NoError(t, err, "Failed to start pipeline for mode %s", tt.name)

			// Wait for pipeline to start
			time.Sleep(500 * time.Millisecond)

			// For live pipelines without data, PAUSED state is acceptable
			state := pipeline.GetState()
			assert.Contains(t, []State{StatePlaying, StatePaused}, state,
				"Pipeline for mode %s should be in PLAYING or PAUSED state", tt.name)

			pipeline.Stop()
		})
	}
}

func TestDirectPipelineStreamContinuity(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	pipeline, err := NewDirectPipeline(config, "test-continuity")
	require.NoError(t, err)

	err = pipeline.Start()
	require.NoError(t, err)
	defer pipeline.Stop()

	// Send continuous stream of RTP packets
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		seq := uint16(1000)
		ts := uint32(0)
		ticker := time.NewTicker(20 * time.Millisecond) // 50 packets/sec
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: seq,
						Timestamp:      ts,
						SSRC:           12345,
					},
					Payload: []byte{0x00, 0x00, 0x00, 0x01, 0x67},
				}
				pipeline.InjectVideoRTP(packet)
				seq++
				ts += 90000 / 50 // Increment based on frame rate
			}
		}
	}()

	<-ctx.Done()

	// Check that packets were received
	stats := pipeline.GetStats()
	assert.Greater(t, stats.VideoPacketsReceived, uint64(50), "Should have received many packets")
	assert.Equal(t, uint64(0), stats.DroppedFrames, "Should not drop frames")
}

func TestDirectPipelineStatistics(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	pipeline, err := NewDirectPipeline(config, "test-stats")
	require.NoError(t, err)

	// Initial stats should be zero
	stats := pipeline.GetStats()
	assert.Equal(t, uint64(0), stats.VideoPacketsReceived)
	assert.Equal(t, uint64(0), stats.AudioPacketsReceived)
	assert.Equal(t, uint64(0), stats.SegmentsWritten)

	err = pipeline.Start()
	require.NoError(t, err)
	defer pipeline.Stop()

	// Wait for pipeline to be ready
	time.Sleep(200 * time.Millisecond)

	// Inject some packets
	for i := 0; i < 10; i++ {
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
		err := pipeline.InjectVideoRTP(packet)
		require.NoError(t, err, "Failed to inject packet %d", i)
	}

	// Stats should be updated
	stats = pipeline.GetStats()
	assert.Equal(t, uint64(10), stats.VideoPacketsReceived)
}

// Benchmark tests
func BenchmarkDirectPipelineCreation(b *testing.B) {
	gst.Init(nil)
	tmpDir := b.TempDir()

	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    4,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pipeline, _ := NewDirectPipeline(config, fmt.Sprintf("bench-%d", i))
		pipeline.Stop()
	}
}

func BenchmarkDirectRTPInjection(b *testing.B) {
	gst.Init(nil)
	tmpDir := b.TempDir()

	config := &Config{
		OutputDir:          tmpDir,
		SegmentDuration:    4,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	pipeline, _ := NewDirectPipeline(config, "bench-rtp")
	pipeline.Start()
	defer pipeline.Stop()

	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: 1,
			Timestamp:      0,
			SSRC:           12345,
		},
		Payload: make([]byte, 1400), // Typical RTP payload size
	}

	b.ResetTimer()
	b.SetBytes(int64(len(packet.Payload)))

	for i := 0; i < b.N; i++ {
		packet.Header.SequenceNumber = uint16(i)
		packet.Header.Timestamp = uint32(i * 3000)
		pipeline.InjectVideoRTP(packet)
	}
}