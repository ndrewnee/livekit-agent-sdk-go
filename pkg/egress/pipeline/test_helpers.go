package pipeline

import (
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/pion/rtp"
)

// GenerateTestRTPPackets generates test RTP packets for testing
func GenerateTestRTPPackets(pipeline *DirectPipeline, count int) error {
	// Generate minimal valid H.264 NAL units for testing
	// This is a simple SPS (Sequence Parameter Set) NAL unit
	h264SPSPayload := []byte{
		0x67, 0x42, 0x00, 0x1f, 0x96, 0x54, 0x05, 0x01,
		0xe8, 0x80, 0x00, 0x00, 0x03, 0x00, 0x80, 0x00,
		0x00, 0x1e, 0x07, 0x00, 0x00, 0x00, 0x00, 0x00,
	}

	// Generate minimal valid Opus payload
	opusPayload := []byte{
		0xfc, 0x00, 0x00, 0x00, // TOC byte + empty frame
	}

	// Send test packets to trigger state transition
	for i := 0; i < count; i++ {
		// Send video packet
		videoPacket := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * 3000), // 90kHz clock
				SSRC:           12345,
			},
			Payload: h264SPSPayload,
		}

		// Send audio packet
		audioPacket := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    111,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * 960), // 48kHz clock, 20ms frames
				SSRC:           54321,
			},
			Payload: opusPayload,
		}

		pipeline.InjectVideoRTP(videoPacket)
		pipeline.InjectAudioRTP(audioPacket)

		// Small delay between packets
		time.Sleep(20 * time.Millisecond)
	}

	return nil
}

// WaitForState waits for pipeline to reach a specific state with timeout
func WaitForState(pipeline *DirectPipeline, targetState State, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pipeline.GetState() == targetState {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// CreateTestPipeline creates a pipeline suitable for testing
func CreateTestPipeline(config *Config, sessionID string) (*DirectPipeline, error) {
	// Initialize GStreamer if not already done
	gst.Init(nil)

	pipeline, err := NewDirectPipeline(config, sessionID)
	if err != nil {
		return nil, err
	}

	// For testing, we may want to use fakesink instead of hlssink2
	// This avoids filesystem dependencies in unit tests
	// But for now, we'll use the real pipeline to test actual functionality

	return pipeline, nil
}