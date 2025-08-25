package agent

import (
	"context"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// SyntheticAudioTrack creates a synthetic audio track for testing
type SyntheticAudioTrack struct {
	*webrtc.TrackLocalStaticRTP
	mu         sync.RWMutex
	sampleRate uint32
	channels   uint16
	running    bool
	cancelFunc context.CancelFunc
}

// NewSyntheticAudioTrack creates a new synthetic audio track that generates a sine wave
func NewSyntheticAudioTrack(id string, sampleRate uint32, channels uint16) (*SyntheticAudioTrack, error) {
	// Create an Opus track
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		id,
		"synthetic-stream",
	)
	if err != nil {
		return nil, err
	}

	return &SyntheticAudioTrack{
		TrackLocalStaticRTP: track,
		sampleRate:          sampleRate,
		channels:            channels,
	}, nil
}

// StartGenerating starts generating synthetic audio data
func (s *SyntheticAudioTrack) StartGenerating(frequency float64) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFunc = cancel
	s.mu.Unlock()

	go s.generateAudio(ctx, frequency)
}

// StopGenerating stops generating audio data
func (s *SyntheticAudioTrack) StopGenerating() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancelFunc != nil {
		s.cancelFunc()
		s.running = false
	}
}

// generateAudio generates a continuous sine wave
func (s *SyntheticAudioTrack) generateAudio(ctx context.Context, frequency float64) {
	// Opus typically uses 20ms frames at 48kHz
	frameDuration := 20 * time.Millisecond
	frameSize := 960 // 48000 Hz * 0.02 seconds

	// Create a simple sine wave generator
	phase := 0.0
	phaseIncrement := 2.0 * math.Pi * frequency / 48000.0

	// Pre-allocate buffer for samples
	samples := make([]int16, frameSize*2) // stereo

	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	sequenceNumber := uint16(0)
	timestamp := uint32(0)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Generate sine wave samples
			for i := 0; i < frameSize; i++ {
				sample := int16(math.Sin(phase) * 32767 * 0.5) // 50% volume
				samples[i*2] = sample                          // left channel
				samples[i*2+1] = sample                        // right channel
				phase += phaseIncrement
				if phase > 2*math.Pi {
					phase -= 2 * math.Pi
				}
			}

			// Convert to bytes (little-endian)
			payload := make([]byte, len(samples)*2)
			for i, sample := range samples {
				payload[i*2] = byte(sample & 0xFF)
				payload[i*2+1] = byte(sample >> 8)
			}

			// Create RTP packet
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    111, // Opus
					SequenceNumber: sequenceNumber,
					Timestamp:      timestamp,
					SSRC:           12345,
				},
				Payload: payload,
			}

			// Write the packet
			if err := s.WriteRTP(packet); err != nil && err != io.ErrClosedPipe {
				// Log error but continue
				continue
			}

			sequenceNumber++
			timestamp += 960 // Opus uses 48kHz clock rate, 20ms = 960 samples
		}
	}
}

// TestPeerConnection creates a pair of connected peer connections for testing
type TestPeerConnection struct {
	Local  *webrtc.PeerConnection
	Remote *webrtc.PeerConnection
	Tracks map[string]*webrtc.TrackRemote
	mu     sync.RWMutex
}

// NewTestPeerConnection creates a new test peer connection pair
func NewTestPeerConnection() (*TestPeerConnection, error) {
	// Create a MediaEngine with Opus support
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}

	// Create the API with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m))

	// Create peer connections
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	}

	localPC, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	remotePC, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	tc := &TestPeerConnection{
		Local:  localPC,
		Remote: remotePC,
		Tracks: make(map[string]*webrtc.TrackRemote),
	}

	// Set up track handler on remote peer
	remotePC.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		tc.mu.Lock()
		tc.Tracks[track.ID()] = track
		tc.mu.Unlock()
	})

	// Handle ICE candidates
	localPC.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			remotePC.AddICECandidate(c.ToJSON())
		}
	})

	remotePC.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			localPC.AddICECandidate(c.ToJSON())
		}
	})

	return tc, nil
}

// AddTrack adds a local track and returns the corresponding remote track
func (tc *TestPeerConnection) AddTrack(track webrtc.TrackLocal) (*webrtc.TrackRemote, error) {
	// Add track to local peer
	sender, err := tc.Local.AddTrack(track)
	if err != nil {
		return nil, err
	}

	// Read incoming RTCP packets (required for proper operation)
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(rtcpBuf); err != nil {
				return
			}
		}
	}()

	// Create and exchange offer/answer
	offer, err := tc.Local.CreateOffer(nil)
	if err != nil {
		return nil, err
	}

	if err := tc.Local.SetLocalDescription(offer); err != nil {
		return nil, err
	}

	if err := tc.Remote.SetRemoteDescription(offer); err != nil {
		return nil, err
	}

	answer, err := tc.Remote.CreateAnswer(nil)
	if err != nil {
		return nil, err
	}

	if err := tc.Remote.SetLocalDescription(answer); err != nil {
		return nil, err
	}

	if err := tc.Local.SetRemoteDescription(answer); err != nil {
		return nil, err
	}

	// Wait for the track to appear on the remote side
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return nil, fmt.Errorf("timeout waiting for track")
		case <-ticker.C:
			tc.mu.RLock()
			if remoteTrack, exists := tc.Tracks[track.ID()]; exists {
				tc.mu.RUnlock()
				return remoteTrack, nil
			}
			tc.mu.RUnlock()
		}
	}
}

// Close closes both peer connections
func (tc *TestPeerConnection) Close() error {
	if err := tc.Local.Close(); err != nil {
		return err
	}
	return tc.Remote.Close()
}

// SyntheticVideoTrack creates a synthetic video track for testing
type SyntheticVideoTrack struct {
	*webrtc.TrackLocalStaticSample
	mu         sync.RWMutex
	width      int
	height     int
	frameRate  float64
	running    bool
	cancelFunc context.CancelFunc
}

// NewSyntheticVideoTrack creates a new synthetic video track
func NewSyntheticVideoTrack(id string, width, height int, frameRate float64) (*SyntheticVideoTrack, error) {
	// Create a VP8 track
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP8,
		},
		id,
		"synthetic-video-stream",
	)
	if err != nil {
		return nil, err
	}

	return &SyntheticVideoTrack{
		TrackLocalStaticSample: track,
		width:                  width,
		height:                 height,
		frameRate:              frameRate,
	}, nil
}

// StartGenerating starts generating synthetic video frames
func (s *SyntheticVideoTrack) StartGenerating() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFunc = cancel
	s.mu.Unlock()

	go s.generateVideo(ctx)
}

// StopGenerating stops generating video frames
func (s *SyntheticVideoTrack) StopGenerating() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancelFunc != nil {
		s.cancelFunc()
		s.running = false
	}
}

// generateVideo generates synthetic video frames
func (s *SyntheticVideoTrack) generateVideo(ctx context.Context) {
	frameDuration := time.Duration(float64(time.Second) / s.frameRate)
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	frameCount := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Create a simple test pattern (alternating colors)
			frameSize := s.width * s.height * 3 / 2 // YUV420
			frame := make([]byte, frameSize)

			// Fill Y plane with gradient
			for i := 0; i < s.width*s.height; i++ {
				frame[i] = byte((frameCount + i) % 256)
			}

			// Fill U and V planes
			uvOffset := s.width * s.height
			for i := 0; i < s.width*s.height/4; i++ {
				frame[uvOffset+i] = 128                    // U plane
				frame[uvOffset+s.width*s.height/4+i] = 128 // V plane
			}

			// Write the frame
			sample := media.Sample{
				Data:     frame,
				Duration: frameDuration,
			}

			if err := s.WriteSample(sample); err != nil && err != io.ErrClosedPipe {
				// Log error but continue
				continue
			}

			frameCount++
		}
	}
}
