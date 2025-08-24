package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// MediaPublisherHandler handles publisher jobs
type MediaPublisherHandler struct {
	agent.BaseHandler // Embed base handler for default implementations
	publisher         *MediaPublisher
	config            *Config
}

// OnJobRequest implements agent.UniversalHandler
func (h *MediaPublisherHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	log.Printf("Job request received: %s for publisher job", job.Id)

	// Validate job type
	if job.Type != livekit.JobType_JT_PUBLISHER {
		log.Printf("Rejecting non-publisher job type: %v", job.Type)
		return false, nil
	}

	// Accept the job
	return true, &agent.JobMetadata{
		ParticipantIdentity: "media-publisher",
		ParticipantName:     "Media Publisher",
		ParticipantMetadata: `{"agent_type": "publisher"}`,
	}
}

// OnJobAssigned implements agent.UniversalHandler
func (h *MediaPublisherHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	// Parse job metadata
	var jobMeta PublisherJobMetadata
	if err := json.Unmarshal([]byte(jobCtx.Job.Metadata), &jobMeta); err != nil {
		// Use default values if metadata parsing fails
		jobMeta = PublisherJobMetadata{
			Mode:   "audio_tone",
			Volume: 1.0,
		}
		log.Printf("Using default job metadata: %+v", jobMeta)
	}

	log.Printf("Starting media publishing for room: %s (Job ID: %s, Mode: %s)",
		jobCtx.Room.Name(), jobCtx.Job.Id, jobMeta.Mode)

	// Create publishing session
	session := h.publisher.CreateSession(jobCtx.Room.Name(), jobCtx.Job.Id, jobMeta)
	defer func() {
		session.End()
		log.Printf("Publishing ended for room: %s", jobCtx.Room.Name())
	}()

	// Start polling for data messages and participants
	go h.pollForEvents(ctx, jobCtx.Room, session, jobMeta)

	// Start publishing based on mode
	switch jobMeta.Mode {
	case "audio_tone":
		return h.runAudioToneMode(ctx, session, jobCtx.Room, jobMeta)
	case "video_pattern":
		return h.runVideoPatternMode(ctx, session, jobCtx.Room, jobMeta)
	case "both":
		return h.runBothMode(ctx, session, jobCtx.Room, jobMeta)
	case "interactive":
		return h.runInteractiveMode(ctx, session, jobCtx.Room, jobMeta)
	default:
		log.Printf("Unknown mode '%s', defaulting to audio_tone", jobMeta.Mode)
		return h.runAudioToneMode(ctx, session, jobCtx.Room, jobMeta)
	}
}

// OnJobTerminated implements agent.UniversalHandler
func (h *MediaPublisherHandler) OnJobTerminated(ctx context.Context, jobID string) {
	log.Printf("Job terminated: %s", jobID)
	h.publisher.EndSessionByJobID(jobID)
}

// pollForEvents polls for room events since we can't modify callbacks after connection
func (h *MediaPublisherHandler) pollForEvents(
	ctx context.Context,
	room *lksdk.Room,
	session *PublishingSession,
	jobMeta PublisherJobMetadata,
) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	knownParticipants := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check for new participants
			for _, p := range room.GetRemoteParticipants() {
				if !knownParticipants[p.Identity()] {
					knownParticipants[p.Identity()] = true
					if jobMeta.WelcomeMessage != "" {
						h.sendWelcomeMessage(p, jobMeta.WelcomeMessage, room)
					}
				}
			}
		}
	}
}

func (h *MediaPublisherHandler) runAudioToneMode(
	ctx context.Context,
	session *PublishingSession,
	room *lksdk.Room,
	jobMeta PublisherJobMetadata,
) error {
	log.Printf("Starting audio tone mode with frequency: %.1f Hz", jobMeta.ToneFrequency)

	// Create audio track
	audioTrack, err := h.createAudioTrack("tone-audio")
	if err != nil {
		return fmt.Errorf("failed to create audio track: %w", err)
	}

	// Publish audio track
	audioPublication, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name:   "Audio Tone",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		return fmt.Errorf("failed to publish audio track: %w", err)
	}

	session.AddTrack("audio", audioPublication.SID())
	log.Printf("Published audio track: %s", audioPublication.SID())

	// Start audio generation
	go h.generateAudioTone(ctx, audioTrack, session, jobMeta)

	// Wait for context cancellation
	<-ctx.Done()
	return nil
}

func (h *MediaPublisherHandler) runVideoPatternMode(
	ctx context.Context,
	session *PublishingSession,
	room *lksdk.Room,
	jobMeta PublisherJobMetadata,
) error {
	log.Printf("Starting video pattern mode: %s", jobMeta.VideoPattern)

	// Create video track
	videoTrack, err := h.createVideoTrack("pattern-video")
	if err != nil {
		return fmt.Errorf("failed to create video track: %w", err)
	}

	// Publish video track
	videoPublication, err := room.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "Video Pattern",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		return fmt.Errorf("failed to publish video track: %w", err)
	}

	session.AddTrack("video", videoPublication.SID())
	log.Printf("Published video track: %s", videoPublication.SID())

	// Start video generation
	go h.generateVideoPattern(ctx, videoTrack, session, jobMeta)

	// Wait for context cancellation
	<-ctx.Done()
	return nil
}

func (h *MediaPublisherHandler) runBothMode(
	ctx context.Context,
	session *PublishingSession,
	room *lksdk.Room,
	jobMeta PublisherJobMetadata,
) error {
	log.Printf("Starting both audio and video mode")

	// Create and publish audio track
	audioTrack, err := h.createAudioTrack("tone-audio")
	if err != nil {
		return fmt.Errorf("failed to create audio track: %w", err)
	}

	audioPublication, err := room.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name:   "Audio Tone",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		return fmt.Errorf("failed to publish audio track: %w", err)
	}
	session.AddTrack("audio", audioPublication.SID())

	// Create and publish video track
	videoTrack, err := h.createVideoTrack("pattern-video")
	if err != nil {
		return fmt.Errorf("failed to create video track: %w", err)
	}

	videoPublication, err := room.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "Video Pattern",
		Source: livekit.TrackSource_CAMERA,
	})
	if err != nil {
		return fmt.Errorf("failed to publish video track: %w", err)
	}
	session.AddTrack("video", videoPublication.SID())

	// Start generation
	go h.generateAudioTone(ctx, audioTrack, session, jobMeta)
	go h.generateVideoPattern(ctx, videoTrack, session, jobMeta)

	// Wait for context cancellation
	<-ctx.Done()
	return nil
}

func (h *MediaPublisherHandler) runInteractiveMode(
	ctx context.Context,
	session *PublishingSession,
	room *lksdk.Room,
	jobMeta PublisherJobMetadata,
) error {
	log.Printf("Starting interactive mode - responding to data messages")

	// Send initial message
	msg := PublisherMessage{
		Type:      "status",
		Message:   "Interactive media publisher ready. Send commands to control.",
		Timestamp: time.Now(),
	}
	if data, err := json.Marshal(msg); err == nil {
		room.LocalParticipant.PublishData(data, lksdk.WithDataPublishReliable(true))
	}

	// Create tracks but don't publish yet
	audioTrack, _ := h.createAudioTrack("interactive-audio")
	videoTrack, _ := h.createVideoTrack("interactive-video")

	// Store tracks in session for interactive control
	session.SetInteractiveTrack(audioTrack)
	// Note: We can only store one track in the current implementation

	// Poll for data messages
	go h.pollForDataMessages(ctx, room, session, audioTrack, videoTrack)

	// Wait for context cancellation
	<-ctx.Done()
	return nil
}

// pollForDataMessages handles data messages in interactive mode
func (h *MediaPublisherHandler) pollForDataMessages(
	ctx context.Context,
	room *lksdk.Room,
	session *PublishingSession,
	audioTrack *webrtc.TrackLocalStaticSample,
	videoTrack *webrtc.TrackLocalStaticSample,
) {
	// In a real implementation, you would set up a data channel receiver
	// For now, we'll simulate with periodic status updates
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Send status update
			stats := session.GetStats()
			msg := PublisherMessage{
				Type:      "stats",
				Timestamp: time.Now(),
				Data: map[string]interface{}{
					"audio_frames": stats.AudioFrameCount,
					"video_frames": stats.VideoFrameCount,
					"duration":     stats.Duration.Seconds(),
					"track_count":  stats.TrackCount,
				},
			}
			if data, err := json.Marshal(msg); err == nil {
				room.LocalParticipant.PublishData(data, lksdk.WithDataPublishReliable(true))
			}
		}
	}
}

func (h *MediaPublisherHandler) createAudioTrack(id string) (*webrtc.TrackLocalStaticSample, error) {
	return webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		id,
		"audio",
	)
}

func (h *MediaPublisherHandler) createVideoTrack(id string) (*webrtc.TrackLocalStaticSample, error) {
	return webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		id,
		"video",
	)
}

func (h *MediaPublisherHandler) generateAudioTone(
	ctx context.Context,
	track *webrtc.TrackLocalStaticSample,
	session *PublishingSession,
	jobMeta PublisherJobMetadata,
) {
	// Audio parameters
	sampleRate := h.config.AudioConfig.SampleRate
	channels := h.config.AudioConfig.Channels
	frequency := jobMeta.ToneFrequency
	if frequency == 0 {
		frequency = 440 // Default A4 note
	}

	// Generate samples at regular intervals
	ticker := time.NewTicker(20 * time.Millisecond) // 50Hz update rate
	defer ticker.Stop()

	samplesPerFrame := sampleRate * 20 / 1000 // 20ms of samples
	samples := make([]int16, samplesPerFrame*channels)
	frameCount := uint32(0)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Generate tone samples
			volume := session.GetVolume()
			for i := 0; i < samplesPerFrame; i++ {
				sample := h.publisher.audioService.GenerateToneSample(
					frequency,
					float64(frameCount*uint32(samplesPerFrame)+uint32(i))/float64(sampleRate),
					volume,
				)
				for ch := 0; ch < channels; ch++ {
					samples[i*channels+ch] = sample
				}
			}

			// Convert to bytes
			buf := make([]byte, len(samples)*2)
			for i, s := range samples {
				buf[i*2] = byte(s)
				buf[i*2+1] = byte(s >> 8)
			}

			// Write to track
			if err := track.WriteSample(media.Sample{
				Data:     buf,
				Duration: 20 * time.Millisecond,
			}); err != nil {
				log.Printf("Failed to write audio sample: %v", err)
			}

			frameCount++
			session.UpdateAudioFrameCount(frameCount)
		}
	}
}

func (h *MediaPublisherHandler) generateVideoPattern(
	ctx context.Context,
	track *webrtc.TrackLocalStaticSample,
	session *PublishingSession,
	jobMeta PublisherJobMetadata,
) {
	// Video parameters
	width := h.config.VideoConfig.Width
	height := h.config.VideoConfig.Height
	frameRate := h.config.VideoConfig.FrameRate

	ticker := time.NewTicker(time.Second / time.Duration(frameRate))
	defer ticker.Stop()

	frameCount := uint32(0)
	pattern := jobMeta.VideoPattern
	if pattern == "" {
		pattern = "color_bars"
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Generate pattern frame
			frame := h.publisher.videoService.GenerateFrame(pattern, width, height, frameCount)

			// Encode frame (simplified - in production use proper VP8 encoder)
			// For demo purposes, we'll send raw frame data
			if err := track.WriteSample(media.Sample{
				Data:     frame,
				Duration: time.Second / time.Duration(frameRate),
			}); err != nil {
				log.Printf("Failed to write video sample: %v", err)
			}

			frameCount++
			session.UpdateVideoFrameCount(frameCount)
		}
	}
}

func (h *MediaPublisherHandler) sendWelcomeMessage(
	participant *lksdk.RemoteParticipant,
	message string,
	room *lksdk.Room,
) {
	msg := PublisherMessage{
		Type:      "welcome",
		Message:   message,
		Timestamp: time.Now(),
		Data: map[string]interface{}{
			"participant": participant.Identity(),
		},
	}

	if data, err := json.Marshal(msg); err == nil {
		if err := room.LocalParticipant.PublishData(data, lksdk.WithDataPublishReliable(true)); err != nil {
			log.Printf("Failed to send welcome message: %v", err)
		} else {
			log.Printf("Sent welcome message to %s", participant.Identity())
		}
	}
}

// PublisherJobMetadata contains configuration for the publishing job
type PublisherJobMetadata struct {
	Mode           string  `json:"mode"`            // audio_tone, video_pattern, both, interactive
	ToneFrequency  float64 `json:"tone_frequency"`  // Hz for audio tone
	VideoPattern   string  `json:"video_pattern"`   // color_bars, moving_circle, etc
	Volume         float32 `json:"volume"`          // 0.0 to 1.0
	WelcomeMessage string  `json:"welcome_message"` // Message to send to new participants
}

// PublisherMessage represents messages sent by the publisher
type PublisherMessage struct {
	Type      string                 `json:"type"`
	Message   string                 `json:"message,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
}
