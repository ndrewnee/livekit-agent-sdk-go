package main

import (
	"bytes"
	"crypto/aes"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

func TestH264FrameAssemblerDecryptsPublisherEncryptedFrame(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 16)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}

	// Minimal Annex B access unit:
	//  - SPS (type 7)
	//  - PPS (type 8)
	//  - IDR slice (type 5)
	//
	// The exact contents don't need to be a valid decodable stream for AES-GCM;
	// they just need stable NAL boundaries and types so unencrypted-bytes logic
	// matches FrameAssembler's expectations.
	startCode := []byte{0x00, 0x00, 0x00, 0x01}
	sps := append([]byte{0x67, 0x64, 0x00, 0x1f}, bytes.Repeat([]byte{0xaa}, 20)...)
	pps := append([]byte{0x68, 0xee, 0x3c, 0x80}, bytes.Repeat([]byte{0xbb}, 8)...)

	// Large enough to trigger FU-A fragmentation at common MTUs.
	idrPayload := make([]byte, 5000)
	for i := range idrPayload {
		idrPayload[i] = byte((i*31 + 7) & 0xff)
	}
	// IDR NAL header (type 5) + a couple bytes of slice header
	idr := append([]byte{0x65, 0x88, 0x84}, idrPayload...)

	plain := make([]byte, 0, len(startCode)*3+len(sps)+len(pps)+len(idr))
	plain = append(plain, startCode...)
	plain = append(plain, sps...)
	plain = append(plain, startCode...)
	plain = append(plain, pps...)
	plain = append(plain, startCode...)
	plain = append(plain, idr...)

	plain = normalizeH264AnnexBStartCodes(plain)

	publisher := &GStreamerPublisher{cipherBlock: block}
	encrypted, err := publisher.encryptSample(plain, e2eeUnencryptedVideoH264)
	if err != nil {
		t.Fatalf("encryptSample: %v", err)
	}

	mtu := uint16(1200)
	payloadType := uint8(96)
	ssrc := uint32(12345)
	clockRate := uint32(90000)

	packetizer := rtp.NewPacketizer(
		mtu,
		payloadType,
		ssrc,
		&codecs.H264Payloader{},
		rtp.NewRandomSequencer(),
		clockRate,
	)
	pkts := packetizer.Packetize(encrypted, 3000)
	if len(pkts) == 0 {
		t.Fatalf("packetizer.Packetize returned 0 packets")
	}
	if !pkts[len(pkts)-1].Marker {
		t.Fatalf("expected marker bit set on last packet")
	}

	assembler := NewFrameAssembler(block, nil, "test")
	var got *DecryptedFrame
	for _, pkt := range pkts {
		frame, err := assembler.AddPacket(pkt)
		if err != nil {
			t.Fatalf("FrameAssembler.AddPacket: %v", err)
		}
		if frame != nil {
			got = frame
		}
	}
	if got == nil {
		t.Fatalf("expected a decrypted frame, got nil")
	}

	if !got.IsKeyframe {
		t.Fatalf("expected keyframe=true")
	}
	if !bytes.Equal(got.Data, plain) {
		t.Fatalf("decrypted frame mismatch: got=%d bytes want=%d bytes", len(got.Data), len(plain))
	}
}
