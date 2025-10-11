package main

import (
	"fmt"
	"sync"
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/jitterbuffer"
	"github.com/pion/rtp"
)

// This test proves that the jitter buffer interceptor strips H.264 payloads
// during the Unmarshal->Push->Pop->MarshalTo cycle
func TestJitterBufferStripsH264Payload(t *testing.T) {
	// Create H.264 and Opus packets with payloads
	h264Packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    125,
			SequenceNumber: 1000,
			Timestamp:      123456,
			SSRC:           0x12345678,
		},
		Payload: []byte{0x67, 0x42, 0x00, 0x1F, 0x8B, 0x68, 0x02, 0x80}, // H.264 NAL unit
	}

	opusPacket := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: 2000,
			Timestamp:      789012,
			SSRC:           0x87654321,
		},
		Payload: []byte{0xF8, 0xFF, 0xFE}, // Opus audio data
	}

	// Test H.264
	fmt.Println("\n=== Testing H.264 Packet Through Jitter Buffer ===")
	testPacketThroughJitterBuffer(t, h264Packet, "H.264")

	// Test Opus
	fmt.Println("\n=== Testing Opus Packet Through Jitter Buffer ===")
	testPacketThroughJitterBuffer(t, opusPacket, "Opus")
}

func testPacketThroughJitterBuffer(t *testing.T, originalPacket *rtp.Packet, codecName string) {
	// Step 1: Marshal original packet to bytes
	originalBytes := make([]byte, 1500)
	originalLen, err := originalPacket.MarshalTo(originalBytes)
	if err != nil {
		t.Fatalf("Failed to marshal original packet: %v", err)
	}
	fmt.Printf("STEP 1 - Original packet marshaled: %d bytes (payload: %d bytes)\n",
		originalLen, len(originalPacket.Payload))

	// Step 2: Create jitter buffer interceptor with minimum 5 packets (not 50)
	factory, err := jitterbuffer.NewInterceptor()
	if err != nil {
		t.Fatalf("Failed to create jitter buffer factory: %v", err)
	}

	interceptorImpl, err := factory.NewInterceptor("")
	if err != nil {
		t.Fatalf("Failed to create jitter buffer interceptor: %v", err)
	}

	jbInterceptor, ok := interceptorImpl.(*jitterbuffer.ReceiverInterceptor)
	if !ok {
		t.Fatalf("Expected *jitterbuffer.ReceiverInterceptor, got %T", interceptorImpl)
	}

	// Create mock reader that generates multiple unique packets
	mockReader := &mockMultiPacketReader{
		basePacket: originalPacket,
		count:      0,
		attributes: make(interceptor.Attributes),
	}

	// Step 3: Bind the jitter buffer to our mock reader
	streamInfo := &interceptor.StreamInfo{
		SSRC:                originalPacket.SSRC,
		PayloadType:         originalPacket.PayloadType,
		RTPHeaderExtensions: []interceptor.RTPHeaderExtension{},
		MimeType:            getMimeType(codecName),
	}

	wrappedReader := jbInterceptor.BindRemoteStream(streamInfo, mockReader)

	// Step 4: Read through jitter buffer multiple times to fill buffer and start emitting
	readBuffer := make([]byte, 1500)
	var lastReadLen int

	fmt.Printf("STEP 2 - Reading through jitter buffer:\n")
	for i := 0; i < 60; i++ { // Read 60 times - buffer needs 50 packets to start emitting
		n, _, err := wrappedReader.Read(readBuffer, mockReader.attributes)

		if err == jitterbuffer.ErrPopWhileBuffering {
			fmt.Printf("  Read %d: Buffering... (packet pushed but not popped yet)\n", i+1)
			continue
		}

		if err != nil {
			fmt.Printf("  Read %d: Error: %v\n", i+1, err)
			continue
		}

		lastReadLen = n
		fmt.Printf("  Read %d: Got %d bytes\n", i+1, n)

		// Once we get bytes back, examine them
		if n > 0 {
			// Step 5: Unmarshal the returned bytes to examine payload
			returnedPacket := &rtp.Packet{}
			if err := returnedPacket.Unmarshal(readBuffer[:n]); err != nil {
				t.Fatalf("Failed to unmarshal returned packet: %v", err)
			}

			fmt.Printf("\nSTEP 3 - Packet after jitter buffer:\n")
			fmt.Printf("  Total bytes: %d\n", n)
			fmt.Printf("  Payload length: %d bytes\n", len(returnedPacket.Payload))
			fmt.Printf("  Payload content: %v\n", returnedPacket.Payload)

			// Step 6: Compare with original
			fmt.Printf("\nSTEP 4 - Comparison:\n")
			fmt.Printf("  Original payload: %d bytes - %v\n", len(originalPacket.Payload), originalPacket.Payload)
			fmt.Printf("  After jitter buf:  %d bytes - %v\n", len(returnedPacket.Payload), returnedPacket.Payload)

			if len(returnedPacket.Payload) == 0 {
				fmt.Printf("  ❌ PAYLOAD STRIPPED for %s!\n", codecName)
			} else if len(returnedPacket.Payload) == len(originalPacket.Payload) {
				fmt.Printf("  ✅ Payload preserved for %s\n", codecName)
			} else {
				fmt.Printf("  ⚠️  Payload length changed for %s\n", codecName)
			}

			break
		}
	}

	if lastReadLen == 0 {
		fmt.Printf("  ⚠️  Never entered emitting state - buffer didn't return packets\n")
	}
}

// Mock RTP reader that generates unique packets with incrementing sequence numbers
type mockMultiPacketReader struct {
	basePacket *rtp.Packet
	count      int
	attributes interceptor.Attributes
	mu         sync.Mutex
}

func (m *mockMultiPacketReader) Read(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create a new packet with incremented sequence number
	packet := &rtp.Packet{
		Header: rtp.Header{
			Version:        m.basePacket.Version,
			PayloadType:    m.basePacket.PayloadType,
			SequenceNumber: m.basePacket.SequenceNumber + uint16(m.count),
			Timestamp:      m.basePacket.Timestamp + uint32(m.count*160), // Increment timestamp
			SSRC:           m.basePacket.SSRC,
		},
		Payload: m.basePacket.Payload, // Same payload for all packets
	}
	m.count++

	// Marshal the packet to bytes
	n, err := packet.MarshalTo(b)
	if err != nil {
		return 0, nil, err
	}

	return n, m.attributes, nil
}

func getMimeType(codecName string) string {
	switch codecName {
	case "H.264":
		return "video/H264"
	case "Opus":
		return "audio/opus"
	default:
		return "application/octet-stream"
	}
}
