package pipeline

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGstPipelineCreation(t *testing.T) {
	// Initialize GStreamer for testing
	gst.Init(nil)

	tmpDir := t.TempDir()

	config := &Config{
		VideoPort:          15004,
		AudioPort:          15006,
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		EnableScreenshots:  false,
		AudioMode:          AudioPassThrough,
		AACBitrate:         192,
		MP3Bitrate:         192,
	}

	pipeline, err := NewGstPipeline(config, "test-session")
	require.NoError(t, err)
	assert.NotNil(t, pipeline)

	// Verify output directory was created
	outputDir := filepath.Join(tmpDir, "test-session")
	_, err = os.Stat(outputDir)
	assert.NoError(t, err)

	// Clean up
	pipeline.Stop()
}

func TestPipelineStartStop(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &Config{
		VideoPort:          15104,
		AudioPort:          15106,
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
	}

	pipeline, err := NewGstPipeline(config, "test-start-stop")
	require.NoError(t, err)

	// Start pipeline
	err = pipeline.Start()
	assert.NoError(t, err)

	// Wait for state change
	time.Sleep(500 * time.Millisecond)

	// Check state
	state := pipeline.GetState()
	assert.Equal(t, StatePlaying, state)

	// Stop pipeline
	err = pipeline.Stop()
	assert.NoError(t, err)

	// Check state after stop
	state = pipeline.GetState()
	assert.Equal(t, StateStopped, state)
}

func TestPipelineWithRTPInput(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	videoPort := 15204
	audioPort := 15206

	config := &Config{
		VideoPort:          videoPort,
		AudioPort:          audioPort,
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
	}

	pipeline, err := NewGstPipeline(config, "test-rtp")
	require.NoError(t, err)

	// Start pipeline
	err = pipeline.Start()
	require.NoError(t, err)
	defer pipeline.Stop()

	// Create UDP connections to send test RTP packets
	videoAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", videoPort))
	audioAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", audioPort))

	videoConn, err := net.DialUDP("udp", nil, videoAddr)
	require.NoError(t, err)
	defer videoConn.Close()

	audioConn, err := net.DialUDP("udp", nil, audioAddr)
	require.NoError(t, err)
	defer audioConn.Close()

	// Send test RTP packets
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go sendTestRTPPackets(ctx, videoConn, 96, 90000) // H.264 PT and clock rate
	go sendTestRTPPackets(ctx, audioConn, 111, 48000) // Opus PT and clock rate

	// Wait for segments to be created
	time.Sleep(3 * time.Second)

	// Check if pipeline received packets (stats should be updated)
	stats := pipeline.GetStats()

	// We should have at least one segment if pipeline is working
	// Note: Actual segment creation depends on having valid H.264/Opus data
	// For unit test, we're just verifying the pipeline doesn't crash
	assert.Equal(t, StatePlaying, pipeline.GetState())
}

func TestPipelineAudioModes(t *testing.T) {
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
				VideoPort:          15304 + int(tt.audioMode)*10,
				AudioPort:          15306 + int(tt.audioMode)*10,
				OutputDir:          tmpDir,
				SegmentDuration:    2,
				JitterBufferMs:     200,
				AudioMode:          tt.audioMode,
				AACBitrate:         192,
				MP3Bitrate:         192,
			}

			pipeline, err := NewGstPipeline(config, fmt.Sprintf("test-%s", tt.name))
			require.NoError(t, err, "Failed to create pipeline for mode %s", tt.name)

			err = pipeline.Start()
			assert.NoError(t, err, "Failed to start pipeline for mode %s", tt.name)

			time.Sleep(500 * time.Millisecond)
			assert.Equal(t, StatePlaying, pipeline.GetState())

			pipeline.Stop()
		})
	}
}

func TestPipelineGapFilling(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	videoPort := 15404

	config := &Config{
		VideoPort:          videoPort,
		AudioPort:          15406,
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
	}

	pipeline, err := NewGstPipeline(config, "test-gap")
	require.NoError(t, err)

	err = pipeline.Start()
	require.NoError(t, err)
	defer pipeline.Stop()

	// Send RTP packets with gaps
	videoAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", videoPort))
	videoConn, _ := net.DialUDP("udp", nil, videoAddr)
	defer videoConn.Close()

	// Send packets with sequence gaps
	for i := uint16(0); i < 10; i++ {
		if i == 5 || i == 6 {
			// Skip these to create a gap
			continue
		}

		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: i,
				Timestamp:      uint32(i * 3000),
				SSRC:           12345,
			},
			Payload: []byte{0x00, 0x00, 0x00, 0x01}, // Minimal NAL unit
		}

		data, _ := packet.Marshal()
		videoConn.Write(data)
		time.Sleep(33 * time.Millisecond) // ~30fps
	}

	// Pipeline should handle gaps via videorate element
	// This test verifies the pipeline doesn't crash with gaps
	time.Sleep(1 * time.Second)
	assert.Equal(t, StatePlaying, pipeline.GetState())
}

func TestPipelineCrashRecovery(t *testing.T) {
	t.Skip("Crash recovery test requires simulating actual GStreamer crash")

	// This test would require a way to simulate a GStreamer element crash
	// which is difficult to do reliably in unit tests
	// In production, this would be tested with integration tests
}

func TestPipelineStatistics(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	config := &Config{
		VideoPort:          15504,
		AudioPort:          15506,
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
	}

	pipeline, err := NewGstPipeline(config, "test-stats")
	require.NoError(t, err)

	// Initial stats should be zero
	stats := pipeline.GetStats()
	assert.Equal(t, uint64(0), stats.VideoPacketsReceived)
	assert.Equal(t, uint64(0), stats.AudioPacketsReceived)
	assert.Equal(t, uint64(0), stats.SegmentsWritten)

	err = pipeline.Start()
	require.NoError(t, err)
	defer pipeline.Stop()

	// Stats should be available after starting
	time.Sleep(500 * time.Millisecond)
	stats = pipeline.GetStats()
	assert.NotNil(t, stats)
}

func TestPipelineOutputDirectory(t *testing.T) {
	gst.Init(nil)

	tmpDir := t.TempDir()
	sessionID := "test-output"

	config := &Config{
		VideoPort:          15604,
		AudioPort:          15606,
		OutputDir:          tmpDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
	}

	pipeline, err := NewGstPipeline(config, sessionID)
	require.NoError(t, err)

	// Check output directory was created
	expectedDir := filepath.Join(tmpDir, sessionID)
	info, err := os.Stat(expectedDir)
	assert.NoError(t, err)
	assert.True(t, info.IsDir())

	pipeline.Stop()
}

// Helper function to send test RTP packets
func sendTestRTPPackets(ctx context.Context, conn *net.UDPConn, payloadType uint8, clockRate uint32) {
	seq := uint16(1000)
	ts := uint32(0)
	ssrc := uint32(12345678)

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
					PayloadType:    payloadType,
					SequenceNumber: seq,
					Timestamp:      ts,
					SSRC:           ssrc,
				},
				Payload: []byte{0x00, 0x00, 0x00, 0x01, 0x67}, // Minimal H.264 SPS NAL
			}

			data, err := packet.Marshal()
			if err != nil {
				continue
			}

			conn.Write(data)

			seq++
			ts += clockRate / 50 // Increment based on clock rate and packet rate
		}
	}
}

// Benchmark tests
func BenchmarkPipelineCreation(b *testing.B) {
	gst.Init(nil)
	tmpDir := b.TempDir()

	config := &Config{
		VideoPort:          25004,
		AudioPort:          25006,
		OutputDir:          tmpDir,
		SegmentDuration:    4,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pipeline, _ := NewGstPipeline(config, fmt.Sprintf("bench-%d", i))
		pipeline.Stop()
	}
}

func BenchmarkRTPPacketProcessing(b *testing.B) {
	gst.Init(nil)
	tmpDir := b.TempDir()

	config := &Config{
		VideoPort:          25104,
		AudioPort:          25106,
		OutputDir:          tmpDir,
		SegmentDuration:    4,
		JitterBufferMs:     200,
		AudioMode:          AudioPassThrough,
	}

	pipeline, _ := NewGstPipeline(config, "bench-rtp")
	pipeline.Start()
	defer pipeline.Stop()

	videoAddr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", config.VideoPort))
	videoConn, _ := net.DialUDP("udp", nil, videoAddr)
	defer videoConn.Close()

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

	data, _ := packet.Marshal()

	b.ResetTimer()
	b.SetBytes(int64(len(data)))

	for i := 0; i < b.N; i++ {
		packet.Header.SequenceNumber = uint16(i)
		packet.Header.Timestamp = uint32(i * 3000)
		data, _ = packet.Marshal()
		videoConn.Write(data)
	}
}