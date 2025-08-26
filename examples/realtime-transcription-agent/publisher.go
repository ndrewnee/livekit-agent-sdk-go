// publisher.go - Audio file publisher for testing transcription
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

// PublisherWorker publishes audio file to LiveKit room
type PublisherWorker struct {
	room       *lksdk.Room
	audioTrack *lksdk.LocalTrack
	audioFile  string
}

// NewPublisherWorker creates a new audio publisher
func NewPublisherWorker(audioFile string) *PublisherWorker {
	return &PublisherWorker{
		audioFile: audioFile,
	}
}

// Connect connects to LiveKit room and starts publishing
func (p *PublisherWorker) Connect(ctx context.Context, url, apiKey, apiSecret, roomName string) error {
	getLogger := logger.GetLogger()

	// Generate token for publisher
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         roomName,
		CanPublish:   true,
		CanSubscribe: false,
	}
	at.AddGrant(grant).
		SetIdentity("audiobook-publisher").
		SetValidFor(3600)

	token, err := at.ToJWT()
	if err != nil {
		return fmt.Errorf("failed to create token: %w", err)
	}

	// Connect to room
	room, err := lksdk.ConnectToRoom(url, token, &lksdk.RoomCallback{
		OnConnected: func() {
			getLogger.Infow("Publisher connected to room",
				"room", roomName,
				"identity", "audiobook-publisher")
		},
		OnDisconnected: func() {
			getLogger.Infow("Publisher disconnected from room")
		},
	}, lksdk.WithAutoSubscribe(false))
	if err != nil {
		return fmt.Errorf("failed to connect to room: %w", err)
	}

	p.room = room

	// Create and publish audio track
	if err := p.publishAudioTrack(ctx); err != nil {
		room.Disconnect()
		return fmt.Errorf("failed to publish audio track: %w", err)
	}

	return nil
}

// publishAudioTrack creates and publishes an audio track from file
func (p *PublisherWorker) publishAudioTrack(ctx context.Context) error {
	getLogger := logger.GetLogger()

	// Open audio file
	file, err := os.Open(p.audioFile)
	if err != nil {
		return fmt.Errorf("failed to open audio file: %w", err)
	}
	defer file.Close()

	// Get file info
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	getLogger.Infow("Publishing audio file",
		"file", p.audioFile,
		"size", fileInfo.Size())

	// Create audio track with Opus codec
	audioTrack, err := lksdk.NewLocalSampleTrack(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		})
	if err != nil {
		return fmt.Errorf("failed to create audio track: %w", err)
	}

	p.audioTrack = audioTrack

	// Publish the track
	publication, err := p.room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name:   "audiobook",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		return fmt.Errorf("failed to publish track: %w", err)
	}

	getLogger.Infow("Audio track published",
		"trackID", publication.SID(),
		"name", "audiobook")

	// Start streaming audio in background
	go p.streamAudioFile(ctx, file)

	return nil
}

// streamAudioFile streams audio file content to the track
func (p *PublisherWorker) streamAudioFile(ctx context.Context, file *os.File) {
	getLogger := logger.GetLogger()
	defer file.Close()

	// Read WAV file header (skip for now, assume PCM)
	// In production, you'd parse the WAV header properly
	file.Seek(44, 0) // Skip standard WAV header

	// Buffer for reading audio data
	bufferSize := 960 * 2 * 2 // 20ms of stereo 16-bit audio at 48kHz
	buffer := make([]byte, bufferSize)

	// Stream parameters
	sampleRate := uint32(48000)
	frameDuration := 20 * time.Millisecond
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	frameCount := 0
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			getLogger.Infow("Stopping audio streaming (context cancelled)")
			return
		case <-ticker.C:
			// Read audio data
			n, err := file.Read(buffer)
			if err != nil {
				if err == io.EOF {
					// Loop the file
					file.Seek(44, 0)
					continue
				}
				getLogger.Errorw("Error reading audio file", err)
				return
			}

			if n == 0 {
				continue
			}

			// Convert PCM to Opus and send
			// For simplicity, we're sending raw samples
			// In production, you'd encode to Opus first
			samples := make([]int16, n/2)
			for i := 0; i < len(samples); i++ {
				samples[i] = int16(buffer[i*2]) | int16(buffer[i*2+1])<<8
			}

			// Write samples to track
			err = p.audioTrack.WriteSample(media.Sample{
				Data:     buffer[:n],
				Duration: frameDuration,
			}, nil)
			if err != nil {
				getLogger.Errorw("Failed to write audio sample", err)
				continue
			}

			frameCount++
			if frameCount%500 == 0 { // Log every 10 seconds
				elapsed := time.Since(startTime)
				getLogger.Infow("Audio streaming progress",
					"frames", frameCount,
					"elapsed", elapsed,
					"rate", float64(frameCount)/elapsed.Seconds())
			}
		}
	}
}

// Disconnect disconnects from the room
func (p *PublisherWorker) Disconnect() {
	if p.room != nil {
		p.room.Disconnect()
	}
}

// RunPublisher runs the audio publisher
func RunPublisher(ctx context.Context, url, apiKey, apiSecret, roomName, audioFile string) error {
	publisher := NewPublisherWorker(audioFile)

	// Connect and start publishing
	if err := publisher.Connect(ctx, url, apiKey, apiSecret, roomName); err != nil {
		return err
	}
	defer publisher.Disconnect()

	fmt.Printf("Publishing audio from %s to room %s\n", audioFile, roomName)
	fmt.Println("Press Ctrl+C to stop...")

	// Wait for context cancellation
	<-ctx.Done()

	return nil
}
