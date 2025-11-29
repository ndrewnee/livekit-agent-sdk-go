package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4/pkg/media"
)

// GStreamerPublisher streams an MP4 file into LiveKit local tracks.
//
// This publisher reads an MP4 file containing H.264 video and Opus audio,
// demuxes it with qtdemux, parses the streams, and writes samples to LiveKit
// local tracks for transmission. It's primarily used for testing the HLS recorder
// with known media content.
//
// The GStreamer pipeline:
//
//	filesrc → qtdemux → video: h264parse → appsink (H.264 byte-stream)
//	                  → audio: opusparse → appsink (Opus)
//
// Video samples are tagged with keyframe metadata to help downstream recorders
// identify I-frames for proper HLS segmentation.
//
// E2EE Support:
// When an encryption key is provided, samples are encrypted using AES-GCM before
// being written to the tracks. This matches the LiveKit E2EE frame encryption format.
type GStreamerPublisher struct {
	pipeline    *gst.Pipeline
	videoTrack  *lksdk.LocalTrack
	audioTrack  *lksdk.LocalTrack
	mu          sync.Mutex
	stopped     bool
	videoTotal  time.Duration
	audioTotal  time.Duration
	cipherBlock cipher.Block // AES cipher for E2EE encryption (nil = no encryption)
	// E2EE encryption stats (for verification)
	videoEncryptedFrames int
	audioEncryptedFrames int
	videoEncryptedBytes  int64
	audioEncryptedBytes  int64
}

// NewGStreamerPublisher creates a new GStreamer-based publisher for the given MP4 file.
//
// The publisher demuxes the MP4 file and configures appsinks to pull H.264 video
// and Opus audio samples, which are then written to the provided LiveKit local tracks.
//
// Parameters:
//   - filePath: Path to MP4 file containing H.264 video and Opus audio
//   - videoTrack: LiveKit local video track to publish H.264 samples
//   - audioTrack: LiveKit local audio track to publish Opus samples
//
// Returns a configured GStreamerPublisher ready to Start(), or an error if
// pipeline creation fails.
func NewGStreamerPublisher(filePath string, videoTrack, audioTrack *lksdk.LocalTrack) (*GStreamerPublisher, error) {
	gst.Init(nil)

	p := &GStreamerPublisher{
		videoTrack: videoTrack,
		audioTrack: audioTrack,
	}

	pipelineStr := fmt.Sprintf(`
		filesrc location="%s" ! qtdemux name=demux
	demux.video_0 ! queue ! h264parse config-interval=1 ! video/x-h264,stream-format=byte-stream,alignment=au ! appsink name=videosink emit-signals=true
		demux.audio_0 ! queue ! opusparse ! audio/x-opus ! appsink name=audiosink emit-signals=true
	`, filePath)

	pipeline, err := gst.NewPipelineFromString(pipelineStr)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher pipeline: %w", err)
	}
	p.pipeline = pipeline

	videoSink, err := pipeline.GetElementByName("videosink")
	if err != nil {
		return nil, fmt.Errorf("failed to get video sink: %w", err)
	}
	app.SinkFromElement(videoSink).SetCallbacks(&app.SinkCallbacks{
		NewSampleFunc: func(sink *app.Sink) gst.FlowReturn {
			sample := sink.PullSample()
			if sample == nil {
				return gst.FlowEOS
			}

			buffer := sample.GetBuffer()
			if buffer == nil {
				return gst.FlowError
			}

			data := buffer.Map(gst.MapRead).Bytes()
			defer buffer.Unmap()

			duration := time.Duration(buffer.Duration())

			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()

			if !stopped && p.videoTrack != nil {
				flags := buffer.GetFlags()
				isKeyFrame := (flags & gst.BufferFlagDeltaUnit) == 0

				// Copy data and optionally encrypt
				sampleData := append([]byte{}, data...)
				if p.cipherBlock != nil {
					originalSize := len(sampleData)
					var err error
					sampleData, err = p.encryptSample(sampleData, e2eeUnencryptedVideoH264)
					if err != nil {
						log.Printf("failed to encrypt video sample: %v", err)
						return gst.FlowError
					}
					// Track encryption stats
					p.mu.Lock()
					p.videoEncryptedFrames++
					p.videoEncryptedBytes += int64(len(sampleData) - originalSize) // overhead added
					p.mu.Unlock()
				}

				sample := media.Sample{
					Data:     sampleData,
					Duration: duration,
				}
				if isKeyFrame {
					sample.Metadata = map[string]any{"keyframe": true}
				}

				if err := p.videoTrack.WriteSample(sample, nil); err != nil {
					log.Printf("failed to write video sample: %v", err)
					return gst.FlowError
				}
				p.mu.Lock()
				p.videoTotal += duration
				p.mu.Unlock()
			}
			return gst.FlowOK
		},
	})

	audioSink, err := pipeline.GetElementByName("audiosink")
	if err != nil {
		return nil, fmt.Errorf("failed to get audio sink: %w", err)
	}
	app.SinkFromElement(audioSink).SetCallbacks(&app.SinkCallbacks{
		NewSampleFunc: func(sink *app.Sink) gst.FlowReturn {
			sample := sink.PullSample()
			if sample == nil {
				return gst.FlowEOS
			}

			buffer := sample.GetBuffer()
			if buffer == nil {
				return gst.FlowError
			}

			data := buffer.Map(gst.MapRead).Bytes()
			defer buffer.Unmap()

			duration := time.Duration(buffer.Duration())

			p.mu.Lock()
			stopped := p.stopped
			p.mu.Unlock()

			if !stopped && p.audioTrack != nil {
				// Copy data and optionally encrypt
				sampleData := append([]byte{}, data...)
				if p.cipherBlock != nil {
					originalSize := len(sampleData)
					var err error
					sampleData, err = p.encryptAudioSample(sampleData)
					if err != nil {
						log.Printf("failed to encrypt audio sample: %v", err)
						return gst.FlowError
					}
					// Track encryption stats
					p.mu.Lock()
					p.audioEncryptedFrames++
					p.audioEncryptedBytes += int64(len(sampleData) - originalSize) // overhead added
					p.mu.Unlock()
				}

				if err := p.audioTrack.WriteSample(media.Sample{
					Data:     sampleData,
					Duration: duration,
				}, nil); err != nil {
					log.Printf("failed to write audio sample: %v", err)
					return gst.FlowError
				}
				p.mu.Lock()
				p.audioTotal += duration
				p.mu.Unlock()
			}
			return gst.FlowOK
		},
	})

	return p, nil
}

// Start begins playing the MP4 file and streaming samples to LiveKit tracks.
// The GStreamer pipeline transitions to the PLAYING state.
func (p *GStreamerPublisher) Start() error {
	return p.pipeline.SetState(gst.StatePlaying)
}

// Stop halts the publisher and stops writing samples to LiveKit tracks.
// The pipeline transitions to the NULL state. Total video and audio
// durations are logged.
func (p *GStreamerPublisher) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	_ = p.pipeline.SetState(gst.StateNull)
	p.mu.Lock()
	log.Printf("publisher totals: video=%.3fs audio=%.3fs", p.videoTotal.Seconds(), p.audioTotal.Seconds())
	if p.cipherBlock != nil {
		log.Printf("[publisher] E2EE stats: video=%d frames (+%d bytes overhead), audio=%d frames (+%d bytes overhead)",
			p.videoEncryptedFrames, p.videoEncryptedBytes, p.audioEncryptedFrames, p.audioEncryptedBytes)
	}
	p.mu.Unlock()
}

// Restart seeks the pipeline back to the beginning and resumes playback.
// This allows re-publishing the same MP4 file without recreating the pipeline.
// Video and audio duration counters are reset to zero.
//
// Returns an error if the pipeline cannot be paused, seeked, or resumed.
func (p *GStreamerPublisher) Restart() error {
	// Check pipeline exists (with lock)
	p.mu.Lock()
	pipeline := p.pipeline
	p.mu.Unlock()

	if pipeline == nil {
		return fmt.Errorf("pipeline not initialized")
	}

	// Pause, seek, and resume pipeline WITHOUT holding lock
	// (GStreamer callbacks may still be running and need to acquire the lock)
	if err := pipeline.SetState(gst.StatePaused); err != nil {
		return fmt.Errorf("failed to pause pipeline: %w", err)
	}

	// Seek to beginning
	flags := gst.SeekFlagFlush | gst.SeekFlagKeyUnit
	if ok := pipeline.SeekSimple(0, gst.FormatTime, flags); !ok {
		return fmt.Errorf("failed to seek pipeline to start")
	}

	if err := pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to resume pipeline: %w", err)
	}

	// Reset counters (with lock)
	p.mu.Lock()
	p.videoTotal = 0
	p.audioTotal = 0
	p.mu.Unlock()

	return nil
}

// Wait blocks until the pipeline reaches EOS (end of stream) or encounters an error.
// This is useful for synchronizing with the completion of file playback.
//
// Returns nil on EOS, or the pipeline error if one occurs.
func (p *GStreamerPublisher) Wait() error {
	bus := p.pipeline.GetBus()
	for {
		msg := bus.TimedPop(gst.ClockTimeNone)
		if msg == nil {
			break
		}
		switch msg.Type() {
		case gst.MessageEOS:
			return nil
		case gst.MessageError:
			return msg.ParseError()
		}
	}
	return nil
}

// SetE2EEKey configures the publisher to encrypt samples with the given key.
// The key must be 16 bytes (AES-128).
func (p *GStreamerPublisher) SetE2EEKey(key []byte) error {
	if len(key) != 16 {
		return fmt.Errorf("E2EE key must be 16 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed to create AES cipher: %w", err)
	}
	p.mu.Lock()
	p.cipherBlock = block
	p.mu.Unlock()
	log.Printf("[publisher] E2EE encryption enabled")
	return nil
}

// E2EE encryption constants matching LiveKit client SDK
const (
	e2eeIVLength             = 12 // Standard GCM nonce size
	e2eeUnencryptedAudio     = 1  // First byte of Opus frames is unencrypted
	e2eeUnencryptedVideoH264 = 1  // First byte of H264 NAL unit is unencrypted
)

// encryptAudioSample encrypts an audio sample using AES-GCM.
// Format: [first byte (unencrypted)] + [encrypted payload + tag] + [IV] + [ivLength] + [keyID]
// Opus audio frames don't have start codes like H264, so encryption is simpler.
func (p *GStreamerPublisher) encryptAudioSample(sample []byte) ([]byte, error) {
	p.mu.Lock()
	block := p.cipherBlock
	p.mu.Unlock()

	if block == nil {
		return sample, nil // No encryption configured
	}

	if len(sample) <= e2eeUnencryptedAudio {
		return sample, nil // Too short to encrypt
	}

	// Create GCM cipher
	aesGCM, err := cipher.NewGCMWithNonceSize(block, e2eeIVLength)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random IV
	iv := make([]byte, e2eeIVLength)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("failed to generate IV: %w", err)
	}

	// Split: [first byte (unencrypted)] + [payload to encrypt]
	frameHeader := sample[:e2eeUnencryptedAudio]
	plainText := sample[e2eeUnencryptedAudio:]

	// Encrypt payload with header as additional authenticated data
	cipherText := aesGCM.Seal(nil, iv, plainText, frameHeader)

	// Build encrypted frame: header + ciphertext + iv + ivLength + keyID
	frameTrailer := []byte{e2eeIVLength, 0} // ivLength=12, keyID=0
	result := make([]byte, len(frameHeader)+len(cipherText)+len(iv)+len(frameTrailer))
	offset := 0
	copy(result[offset:], frameHeader)
	offset += len(frameHeader)
	copy(result[offset:], cipherText)
	offset += len(cipherText)
	copy(result[offset:], iv)
	offset += len(iv)
	copy(result[offset:], frameTrailer)

	return result, nil
}

// findNALStart finds the position of the NAL header in H264 byte-stream format.
// Returns the offset past any Annex B start code (0x00 0x00 0x01 or 0x00 0x00 0x00 0x01).
func findNALStart(data []byte) int {
	if len(data) < 4 {
		return 0
	}
	// Check for 4-byte start code: 0x00 0x00 0x00 0x01
	if data[0] == 0 && data[1] == 0 && data[2] == 0 && data[3] == 1 {
		return 4
	}
	// Check for 3-byte start code: 0x00 0x00 0x01
	if data[0] == 0 && data[1] == 0 && data[2] == 1 {
		return 3
	}
	// No start code found
	return 0
}

// splitNALUnits splits H264 Annex B byte stream into individual NAL units.
// Returns a slice of (startCode, nalUnit) pairs.
func splitNALUnits(data []byte) []struct {
	startCode []byte
	nalUnit   []byte
} {
	var units []struct {
		startCode []byte
		nalUnit   []byte
	}

	// Find all start codes and split
	i := 0
	for i < len(data) {
		// Find start code at current position
		startCodeLen := 0
		if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			startCodeLen = 4
		} else if i+3 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			startCodeLen = 3
		}

		if startCodeLen == 0 {
			// No start code at current position - this shouldn't happen in valid Annex B
			i++
			continue
		}

		startCode := data[i : i+startCodeLen]
		nalStart := i + startCodeLen

		// Find next start code or end of data
		nalEnd := len(data)
		for j := nalStart; j < len(data)-2; j++ {
			if data[j] == 0 && data[j+1] == 0 {
				if (j+3 <= len(data) && data[j+2] == 0 && data[j+3] == 1) ||
					(data[j+2] == 1) {
					nalEnd = j
					break
				}
			}
		}

		if nalEnd > nalStart {
			units = append(units, struct {
				startCode []byte
				nalUnit   []byte
			}{startCode, data[nalStart:nalEnd]})
		}

		i = nalEnd
	}

	return units
}

// encryptSample encrypts a media sample using AES-GCM.
// For H264, this encrypts each NAL unit separately to preserve NAL boundaries.
// Format per NAL: [start code] + [NAL header (unencrypted)] + [encrypted payload + tag] + [IV] + [ivLength] + [keyID]
func (p *GStreamerPublisher) encryptSample(sample []byte, unencryptedBytes int) ([]byte, error) {
	p.mu.Lock()
	block := p.cipherBlock
	p.mu.Unlock()

	if block == nil {
		return sample, nil // No encryption configured
	}

	// Split the sample into individual NAL units
	nalUnits := splitNALUnits(sample)
	if len(nalUnits) == 0 {
		return sample, nil // No NAL units found
	}

	// Create GCM cipher
	aesGCM, err := cipher.NewGCMWithNonceSize(block, e2eeIVLength)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Encrypt each NAL unit separately
	var result []byte
	for _, unit := range nalUnits {
		if len(unit.nalUnit) <= unencryptedBytes {
			// NAL unit too short to encrypt, keep as-is
			result = append(result, unit.startCode...)
			result = append(result, unit.nalUnit...)
			continue
		}

		// SPS (7) and PPS (8) must NOT be encrypted - they contain codec config
		// (profile, level, dimensions) needed by SFU and decoders
		nalType := unit.nalUnit[0] & 0x1F
		if nalType == 7 || nalType == 8 {
			result = append(result, unit.startCode...)
			result = append(result, unit.nalUnit...)
			continue
		}

		// Generate random IV for this NAL unit
		iv := make([]byte, e2eeIVLength)
		if _, err := rand.Read(iv); err != nil {
			return nil, fmt.Errorf("failed to generate IV: %w", err)
		}

		// Split: [NAL header (unencrypted)] + [payload to encrypt]
		nalHeader := unit.nalUnit[:unencryptedBytes]
		plainText := unit.nalUnit[unencryptedBytes:]

		// Encrypt payload with NAL header as additional authenticated data
		cipherText := aesGCM.Seal(nil, iv, plainText, nalHeader)

		// Build encrypted NAL: start_code + nal_header + ciphertext + iv + ivLength + keyID
		frameTrailer := []byte{e2eeIVLength, 0} // ivLength=12, keyID=0

		result = append(result, unit.startCode...)
		result = append(result, nalHeader...)
		result = append(result, cipherText...)
		result = append(result, iv...)
		result = append(result, frameTrailer...)
	}

	return result, nil
}
