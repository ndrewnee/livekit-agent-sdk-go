package main

import (
	"context"
	"log"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type PacketInfo struct {
	PacketNum  int
	SeqNum     uint16
	Timestamp  uint32
	PayloadLen int
	TotalBytes int
	FirstBytes []byte
}

// TestComprehensiveTrace reads 150 packets to find if ANY have payloads
func TestComprehensiveTrace(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping comprehensive trace test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	t.Log("=== COMPREHENSIVE TRACE: Read 150 packets and analyze ===")

	// Clean up
	os.RemoveAll("test-recordings")
	os.MkdirAll("test-recordings", 0755)

	// Create room
	roomServiceClient := lksdk.NewRoomServiceClient(testLiveKitURL2, testAPIKey2, testAPISecret2)
	_, _ = roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: testRoomName2,
	})
	_, err := roomServiceClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: testRoomName2,
	})
	if err != nil {
		t.Fatalf("Failed to create room: %v", err)
	}
	defer roomServiceClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: testRoomName2,
	})

	t.Log("✓ Room created")

	// Collect packet data
	var mu sync.Mutex
	packetsRead := make([]*PacketInfo, 0)
	packetsReadRTP := make([]*PacketInfo, 0)
	done := make(chan struct{})

	// Create recorder with comprehensive tracing
	recorderCallbacks := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if track.Kind() != webrtc.RTPCodecTypeVideo {
					return // Only trace video
				}

				t.Logf("✓ Video track subscribed: %s", track.Codec().MimeType)

				go func() {
					defer close(done)

					// Read 150 packets using track.Read()
					for i := 0; i < 150; i++ {
						buf := make([]byte, 1500)
						n, _, err := track.Read(buf)
						if err != nil {
							t.Logf("track.Read() error at packet %d: %v", i, err)
							return
						}

						packet := &rtp.Packet{}
						if err := packet.Unmarshal(buf[:n]); err != nil {
							t.Logf("Unmarshal error at packet %d: %v", i, err)
							continue
						}

						info := &PacketInfo{
							PacketNum:  i,
							SeqNum:     packet.SequenceNumber,
							Timestamp:  packet.Timestamp,
							PayloadLen: len(packet.Payload),
							TotalBytes: n,
							FirstBytes: make([]byte, min(8, len(packet.Payload))),
						}
						if len(packet.Payload) > 0 {
							copy(info.FirstBytes, packet.Payload[:min(8, len(packet.Payload))])
						}

						mu.Lock()
						packetsRead = append(packetsRead, info)
						mu.Unlock()

						// Now also read using ReadRTP()
						rtpPacket, _, err := track.ReadRTP()
						if err != nil {
							t.Logf("track.ReadRTP() error at packet %d: %v", i, err)
							return
						}

						infoRTP := &PacketInfo{
							PacketNum:  i,
							SeqNum:     rtpPacket.SequenceNumber,
							Timestamp:  rtpPacket.Timestamp,
							PayloadLen: len(rtpPacket.Payload),
							TotalBytes: 0, // ReadRTP doesn't give us total bytes
							FirstBytes: make([]byte, min(8, len(rtpPacket.Payload))),
						}
						if len(rtpPacket.Payload) > 0 {
							copy(infoRTP.FirstBytes, rtpPacket.Payload[:min(8, len(rtpPacket.Payload))])
						}

						mu.Lock()
						packetsReadRTP = append(packetsReadRTP, infoRTP)
						mu.Unlock()
					}
				}()
			},
		},
	}

	recorderRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "recorder-trace",
		ParticipantName:     "Recorder Trace",
	}, recorderCallbacks)
	if err != nil {
		t.Fatalf("Failed to connect recorder: %v", err)
	}
	defer recorderRoom.Disconnect()

	time.Sleep(2 * time.Second)

	// Connect publisher
	publisherRoom, err := lksdk.ConnectToRoom(testLiveKitURL2, lksdk.ConnectInfo{
		APIKey:              testAPIKey2,
		APISecret:           testAPISecret2,
		RoomName:            testRoomName2,
		ParticipantIdentity: "publisher-trace",
		ParticipantName:     "Publisher Trace",
	}, &lksdk.RoomCallback{})
	if err != nil {
		t.Fatalf("Failed to connect publisher: %v", err)
	}
	defer publisherRoom.Disconnect()

	// Create and publish video track
	testVideo := "../../examples/egress-agent/test-data/test.mp4"
	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		t.Fatalf("Failed to create video track: %v", err)
	}

	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, nil) // Video only
	if err != nil {
		t.Fatalf("Failed to create publisher: %v", err)
	}

	videoBound := make(chan struct{})
	videoTrack.OnBind(func() {
		close(videoBound)
	})

	_, err = publisherRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "test-video",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		t.Fatalf("Failed to publish video track: %v", err)
	}

	<-videoBound
	if err := publisher.Start(); err != nil {
		t.Fatalf("Failed to start publisher: %v", err)
	}

	t.Log("✓ Publisher started, waiting for packets...")

	// Wait for collection to complete (with timeout)
	select {
	case <-done:
		t.Log("✓ Packet collection complete")
	case <-time.After(30 * time.Second):
		t.Log("⚠ Timeout waiting for packets")
	}

	publisher.Stop()
	publisherRoom.Disconnect()
	recorderRoom.Disconnect()

	// Analyze collected data
	t.Log("\n========================================")
	t.Log("ANALYSIS: track.Read() packets")
	t.Log("========================================")

	mu.Lock()
	readPackets := packetsRead
	rtpPackets := packetsReadRTP
	mu.Unlock()

	if len(readPackets) == 0 {
		t.Log("❌ No packets collected!")
		return
	}

	t.Logf("Total packets read: %d", len(readPackets))

	// Count packets with payloads
	packetsWithPayload := 0
	packetsWithoutPayload := 0
	var seqNumbers []uint16
	for _, p := range readPackets {
		seqNumbers = append(seqNumbers, p.SeqNum)
		if p.PayloadLen > 0 {
			packetsWithPayload++
		} else {
			packetsWithoutPayload++
		}
	}

	t.Logf("Packets WITH payload: %d", packetsWithPayload)
	t.Logf("Packets WITHOUT payload: %d", packetsWithoutPayload)

	// Show sequence number range
	sort.Slice(seqNumbers, func(i, j int) bool { return seqNumbers[i] < seqNumbers[j] })
	t.Logf("Sequence number range: %d - %d", seqNumbers[0], seqNumbers[len(seqNumbers)-1])

	// Show first 10 packets with details
	t.Log("\nFirst 10 packets (track.Read):")
	for i := 0; i < min(10, len(readPackets)); i++ {
		p := readPackets[i]
		payloadStatus := "✅"
		if p.PayloadLen == 0 {
			payloadStatus = "❌"
		}
		t.Logf("  [%d] seq=%d ts=%d payload=%d bytes total=%d bytes %s",
			p.PacketNum, p.SeqNum, p.Timestamp, p.PayloadLen, p.TotalBytes, payloadStatus)
		if len(p.FirstBytes) > 0 {
			t.Logf("       first bytes: %02x", p.FirstBytes)
		}
	}

	// Show any packets WITH payloads (if any)
	if packetsWithPayload > 0 {
		t.Log("\nPackets WITH payloads (first 5):")
		count := 0
		for _, p := range readPackets {
			if p.PayloadLen > 0 {
				t.Logf("  [%d] seq=%d ts=%d payload=%d bytes total=%d bytes ✅",
					p.PacketNum, p.SeqNum, p.Timestamp, p.PayloadLen, p.TotalBytes)
				t.Logf("       first bytes: %02x", p.FirstBytes)
				count++
				if count >= 5 {
					break
				}
			}
		}
	}

	// Now analyze ReadRTP packets
	t.Log("\n========================================")
	t.Log("ANALYSIS: track.ReadRTP() packets")
	t.Log("========================================")

	if len(rtpPackets) == 0 {
		t.Log("❌ No RTP packets collected!")
		return
	}

	t.Logf("Total RTP packets read: %d", len(rtpPackets))

	packetsWithPayloadRTP := 0
	packetsWithoutPayloadRTP := 0
	var seqNumbersRTP []uint16
	for _, p := range rtpPackets {
		seqNumbersRTP = append(seqNumbersRTP, p.SeqNum)
		if p.PayloadLen > 0 {
			packetsWithPayloadRTP++
		} else {
			packetsWithoutPayloadRTP++
		}
	}

	t.Logf("RTP packets WITH payload: %d", packetsWithPayloadRTP)
	t.Logf("RTP packets WITHOUT payload: %d", packetsWithoutPayloadRTP)

	sort.Slice(seqNumbersRTP, func(i, j int) bool { return seqNumbersRTP[i] < seqNumbersRTP[j] })
	t.Logf("RTP sequence number range: %d - %d", seqNumbersRTP[0], seqNumbersRTP[len(seqNumbersRTP)-1])

	// Show first 10 RTP packets
	t.Log("\nFirst 10 packets (track.ReadRTP):")
	for i := 0; i < min(10, len(rtpPackets)); i++ {
		p := rtpPackets[i]
		payloadStatus := "✅"
		if p.PayloadLen == 0 {
			payloadStatus = "❌"
		}
		t.Logf("  [%d] seq=%d ts=%d payload=%d bytes %s",
			p.PacketNum, p.SeqNum, p.Timestamp, p.PayloadLen, payloadStatus)
		if len(p.FirstBytes) > 0 {
			t.Logf("       first bytes: %02x", p.FirstBytes)
		}
	}

	// Show any RTP packets WITH payloads (if any)
	if packetsWithPayloadRTP > 0 {
		t.Log("\nRTP packets WITH payloads (first 5):")
		count := 0
		for _, p := range rtpPackets {
			if p.PayloadLen > 0 {
				t.Logf("  [%d] seq=%d ts=%d payload=%d bytes ✅",
					p.PacketNum, p.SeqNum, p.Timestamp, p.PayloadLen)
				t.Logf("       first bytes: %02x", p.FirstBytes)
				count++
				if count >= 5 {
					break
				}
			}
		}
	}

	// Final verdict
	t.Log("\n========================================")
	t.Log("CONCLUSION")
	t.Log("========================================")

	if packetsWithPayload == 0 && packetsWithPayloadRTP == 0 {
		t.Log("❌ CRITICAL: ALL 150 packets have ZERO payloads!")
		t.Log("   This means the packets on the wire have no data.")
		t.Log("   Problem is BEFORE reaching track.Read()!")
	} else {
		t.Logf("✅ Found %d packets with payloads in track.Read()", packetsWithPayload)
		t.Logf("✅ Found %d packets with payloads in track.ReadRTP()", packetsWithPayloadRTP)
		if packetsWithPayload > 0 && packetsWithPayloadRTP == 0 {
			t.Log("⚠ track.Read() works but track.ReadRTP() strips payloads!")
		}
	}
}
