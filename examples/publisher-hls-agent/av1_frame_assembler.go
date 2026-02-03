package main

import (
	"fmt"
	"sort"
	"sync"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// AV1FrameAssembler collects RTP packets for a single AV1 stream and assembles
// complete OBU-stream frames (with OBU size fields) on marker-bit boundaries.
type AV1FrameAssembler struct {
	mu sync.Mutex

	pendingPackets map[uint32][]*rtp.Packet

	logPrefix string
}

// AV1AssembledFrame represents a single assembled AV1 frame as an OBU stream.
type AV1AssembledFrame struct {
	Data      []byte
	Timestamp uint32
}

// NewAV1FrameAssembler constructs a new AV1FrameAssembler instance.
func NewAV1FrameAssembler(logPrefix string) *AV1FrameAssembler {
	return &AV1FrameAssembler{
		pendingPackets: make(map[uint32][]*rtp.Packet),
		logPrefix:      logPrefix,
	}
}

// AddPacket adds an RTP packet to the assembler and returns a complete frame when the marker bit is set.
func (a *AV1FrameAssembler) AddPacket(pkt *rtp.Packet) (*AV1AssembledFrame, error) {
	if pkt == nil || len(pkt.Payload) == 0 {
		return nil, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.pendingPackets[pkt.Timestamp] = append(a.pendingPackets[pkt.Timestamp], pkt.Clone())

	if !pkt.Marker {
		return nil, nil
	}

	packets := a.pendingPackets[pkt.Timestamp]
	delete(a.pendingPackets, pkt.Timestamp)

	sort.Slice(packets, func(i, j int) bool {
		return packets[i].SequenceNumber < packets[j].SequenceNumber
	})

	var dep codecs.AV1Depacketizer
	var out []byte
	for _, p := range packets {
		b, err := dep.Unmarshal(p.Payload)
		if err != nil {
			return nil, fmt.Errorf("[%s] AV1 depacketize failed (seq=%d): %w", a.logPrefix, p.SequenceNumber, err)
		}
		out = append(out, b...)
	}

	return &AV1AssembledFrame{
		Data:      out,
		Timestamp: pkt.Timestamp,
	}, nil
}

// Flush drops all pending packets currently buffered by timestamp.
func (a *AV1FrameAssembler) Flush() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingPackets = make(map[uint32][]*rtp.Packet)
}
