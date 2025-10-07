// save-to-mp4 is a simple application that shows how to receive
// video using Pion and then save to H.264 file (which can be played in most players).
// Based on save-to-webm.go but outputs H.264 instead.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

type h264Saver struct {
	file          *os.File
	h264Builder   *samplebuilder.SampleBuilder
	h264Packet    codecs.H264Packet
	sampleCount   int
	sps           []byte // Cached SPS (NAL type 7)
	pps           []byte // Cached PPS (NAL type 8)
	wroteHeader   bool
}

func newH264Saver(filename string) (*h264Saver, error) {
	f, err := os.Create(filename)
	if err != nil {
		return nil, err
	}

	return &h264Saver{
		file:        f,
		h264Builder: samplebuilder.New(100, &codecs.H264Packet{}, 90000),
	}, nil
}

func (s *h264Saver) WriteRTP(pkt *rtp.Packet) error {
	s.h264Builder.Push(pkt)

	for {
		sample := s.h264Builder.Pop()
		if sample == nil {
			return nil
		}

		// SampleBuilder returns data which may contain multiple NAL units with Annex B start codes
		data := sample.Data
		if len(data) == 0 {
			continue
		}

		// Extract all NAL units from this sample
		nalUnits := s.parseAnnexBNALs(data)

		// Process each NAL unit
		for _, nalData := range nalUnits {
			if len(nalData) == 0 {
				continue
			}

			nalType := nalData[0] & 0x1F

			// Cache SPS and PPS when we encounter them
			if nalType == 7 { // SPS
				s.sps = make([]byte, len(nalData))
				copy(s.sps, nalData)
			} else if nalType == 8 { // PPS
				s.pps = make([]byte, len(nalData))
				copy(s.pps, nalData)
			}
		}

		// Write header once we have both SPS and PPS (before writing any frames)
		if !s.wroteHeader && len(s.sps) > 0 && len(s.pps) > 0 {
			// Write SPS first
			if _, err := s.file.Write([]byte{0x00, 0x00, 0x00, 0x01}); err != nil {
				return err
			}
			if _, err := s.file.Write(s.sps); err != nil {
				return err
			}

			// Write PPS
			if _, err := s.file.Write([]byte{0x00, 0x00, 0x00, 0x01}); err != nil {
				return err
			}
			if _, err := s.file.Write(s.pps); err != nil {
				return err
			}

			s.wroteHeader = true
		}

		// Write all NAL units from this sample
		for _, nalData := range nalUnits {
			s.sampleCount++
			if _, err := s.file.Write([]byte{0x00, 0x00, 0x00, 0x01}); err != nil {
				return err
			}
			if _, err := s.file.Write(nalData); err != nil {
				return err
			}
		}
	}
}

// parseAnnexBNALs parses Annex B formatted data and returns NAL units (without start codes)
func (s *h264Saver) parseAnnexBNALs(data []byte) [][]byte {
	var nalUnits [][]byte

	offset := 0
	for offset < len(data) {
		// Look for start code (0x000001 or 0x00000001)
		startCodeLen := 0
		if offset+4 <= len(data) && data[offset] == 0x00 && data[offset+1] == 0x00 && data[offset+2] == 0x00 && data[offset+3] == 0x01 {
			startCodeLen = 4
		} else if offset+3 <= len(data) && data[offset] == 0x00 && data[offset+1] == 0x00 && data[offset+2] == 0x01 {
			startCodeLen = 3
		} else {
			offset++
			continue
		}

		nalStart := offset + startCodeLen
		if nalStart >= len(data) {
			break
		}

		// Find next start code
		nalEnd := len(data)
		for i := nalStart + 1; i < len(data)-2; i++ {
			if data[i] == 0x00 && data[i+1] == 0x00 {
				if i+2 < len(data) && data[i+2] == 0x01 {
					nalEnd = i
					break
				} else if i+3 < len(data) && data[i+2] == 0x00 && data[i+3] == 0x01 {
					nalEnd = i
					break
				}
			}
		}

		if nalEnd > nalStart {
			nalUnit := make([]byte, nalEnd-nalStart)
			copy(nalUnit, data[nalStart:nalEnd])
			nalUnits = append(nalUnits, nalUnit)
		}

		offset = nalEnd
	}

	return nalUnits
}

func (s *h264Saver) Close() error {
	fmt.Printf("Wrote %d H.264 samples\n", s.sampleCount)
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

func main() {
	// For video-only recording to H.264 format
	h264File, err := newH264Saver("output.h264")
	if err != nil {
		panic(err)
	}

	peerConnection := createWebRTCConn(h264File)

	fmt.Printf("\nH.264 saver started\n")
	fmt.Printf("Output file: output.h264\n")
	fmt.Printf("Press Ctrl+C to stop\n\n")

	closed := make(chan os.Signal, 1)
	signal.Notify(closed, os.Interrupt)
	<-closed

	fmt.Printf("\nClosing H.264 file...\n")
	if err := peerConnection.Close(); err != nil {
		panic(err)
	}

	if err := h264File.Close(); err != nil {
		panic(err)
	}

	fmt.Printf("\n✅ Video saved to output.h264\n")
	fmt.Printf("\nTo play: ffplay output.h264\n")
}

func createWebRTCConn(h264File *h264Saver) *webrtc.PeerConnection {
	// Everything below is the Pion WebRTC API! Thanks for using it ❤️.

	// Prepare the configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	// Create a MediaEngine object to configure the supported codec
	mediaEngine := &webrtc.MediaEngine{}

	// Setup the codecs you want to use.
	// We support H264 for video
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		PayloadType:        102,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}

	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	// Set a handler for when a new remote track starts
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			// Send a PLI on an interval so that the publisher is pushing a keyframe every 3 seconds
			go func() {
				ticker := time.NewTicker(time.Second * 3)
				defer ticker.Stop()
				for range ticker.C {
					if rtcpSendErr := peerConnection.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}}); rtcpSendErr != nil {
						fmt.Println(rtcpSendErr)
					}
				}
			}()
		}

		fmt.Printf("Track has started, of type %d: %s \n", track.PayloadType(), track.Codec().RTPCodecCapability.MimeType)
		for {
			// Read RTP packets being sent to Pion
			rtpPkt, _, readErr := track.ReadRTP()
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return
				}
				panic(readErr)
			}

			if track.Codec().MimeType == webrtc.MimeTypeH264 {
				if err := h264File.WriteRTP(rtpPkt); err != nil {
					fmt.Printf("Error writing H.264: %v\n", err)
				}
			}
		}
	})

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
	})

	// Wait for the offer to be pasted
	offer := webrtc.SessionDescription{}
	decode(readUntilNewline(), &offer)

	// Set the remote SessionDescription
	err = peerConnection.SetRemoteDescription(offer)
	if err != nil {
		panic(err)
	}

	// Create an answer
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and starts our UDP listeners
	err = peerConnection.SetLocalDescription(answer)
	if err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	// Output the answer in base64 so we can paste it in browser
	fmt.Println(encode(peerConnection.LocalDescription()))

	return peerConnection
}

// Read from stdin until we get a newline.
func readUntilNewline() (in string) {
	var err error

	r := bufio.NewReader(os.Stdin)
	for {
		in, err = r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			panic(err)
		}

		if in = strings.TrimSpace(in); len(in) > 0 {
			break
		}
	}

	fmt.Println("")

	return
}

// JSON encode + base64 a SessionDescription.
func encode(obj *webrtc.SessionDescription) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return base64.StdEncoding.EncodeToString(b)
}

// Decode a base64 and unmarshal JSON into a SessionDescription.
func decode(in string, obj *webrtc.SessionDescription) {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}

	if err = json.Unmarshal(b, obj); err != nil {
		panic(err)
	}
}
