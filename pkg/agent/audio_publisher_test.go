//go:build integration

package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// AudioPublisher publishes WAV audio files to LiveKit rooms for testing.
type AudioPublisher struct {
	room       *lksdk.Room
	audioTrack *lksdk.LocalTrack
	audioFile  string
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewAudioPublisher creates a new audio publisher.
func NewAudioPublisher(audioFile string) *AudioPublisher {
	ctx, cancel := context.WithCancel(context.Background())
	return &AudioPublisher{
		audioFile: audioFile,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Connect connects to a LiveKit room as an audio publisher.
func (p *AudioPublisher) Connect(ctx context.Context, url, apiKey, apiSecret, roomName string) error {
	// Generate publisher token
	at := auth.NewAccessToken(apiKey, apiSecret)
	canPublish := true
	canSubscribe := false
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanPublish:   &canPublish,
		CanSubscribe: &canSubscribe,
	}
	at.SetVideoGrant(grant).
		SetIdentity("audio-publisher").
		SetValidFor(time.Hour)

	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to create token: %w", err)
	}

	// Connect to room
	room, err := lksdk.ConnectToRoomWithToken(url, token, &lksdk.RoomCallback{})
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	p.room = room

	// Create audio track with Opus codec (LiveKit standard)
	audioTrack, err := lksdk.NewLocalSampleTrack(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2, // Stereo
		})
	if err != nil {
		room.Disconnect()
		return fmt.Errorf("failed to create track: %w", err)
	}
	p.audioTrack = audioTrack

	// Publish track
	_, err = p.room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name:   "test-audio",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		room.Disconnect()
		return fmt.Errorf("failed to publish track: %w", err)
	}

	return nil
}

// StartStreaming starts streaming audio from the file.
func (p *AudioPublisher) StartStreaming() error {
	file, err := os.Open(p.audioFile)
	if err != nil {
		return fmt.Errorf("failed to open audio: %w", err)
	}

	go p.streamAudio(file)
	return nil
}

// streamAudio reads WAV file and streams it to the track.
func (p *AudioPublisher) streamAudio(file *os.File) {
	defer file.Close()

	// Skip standard WAV header (44 bytes)
	file.Seek(44, 0)

	const (
		frameDuration = 10 * time.Millisecond // 10ms frames
		maxPacketSize = 960                   // Safe packet size
	)

	buffer := make([]byte, maxPacketSize)
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			n, err := file.Read(buffer)
			if err != nil {
				if err == io.EOF {
					// Loop audio file
					file.Seek(44, 0)
					continue
				}
				return
			}

			if n > 0 {
				p.audioTrack.WriteSample(media.Sample{
					Data:     buffer[:n],
					Duration: frameDuration,
				}, nil)
			}
		}
	}
}

// Stop stops streaming and disconnects from the room.
func (p *AudioPublisher) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.room != nil {
		p.room.Disconnect()
	}
}
