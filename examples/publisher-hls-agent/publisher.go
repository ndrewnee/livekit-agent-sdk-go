package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type sampleTrackWriter interface {
	WriteSample(media.Sample, *lksdk.SampleWriteOptions) error
}

// GStreamerPublisher streams an MP4 file into LiveKit local tracks.
//
// This publisher reads an MP4 file containing H.264 or AV1 video and Opus audio,
// demuxes it with qtdemux, parses the streams, and writes samples to LiveKit
// local tracks for transmission. It's primarily used for testing the HLS recorder
// with known media content.
//
// The GStreamer pipeline:
//
//	filesrc → qtdemux → video: h264parse → appsink (H.264 byte-stream, Annex B)
//	                  → audio: opusparse → appsink (Opus)
//
// For AV1:
//
//	filesrc → qtdemux → video: av1parse → appsink (AV1 OBU stream)
//
// Video samples are tagged with keyframe metadata to help downstream recorders
// identify I-frames for proper HLS segmentation.
//
// E2EE Support:
// When an encryption key is provided, samples are encrypted using AES-GCM before
// being written to the tracks. This matches the LiveKit E2EE frame encryption format.
type GStreamerPublisher struct {
	pipeline    *gst.Pipeline
	videoTrack  sampleTrackWriter
	audioTrack  sampleTrackWriter
	videoCodec  string
	mu          sync.Mutex
	stopped     bool
	videoTotal  time.Duration
	audioTotal  time.Duration
	cipherBlock cipher.Block // AES cipher for E2EE encryption (nil = no encryption)
	// GStreamer doesn't always set per-buffer durations, especially for video. Since
	// LiveKit's sample-based tracks derive RTP timestamps from Sample.Duration, a zero
	// duration can collapse multiple frames into the same RTP timestamp and cause
	// downstream frame assembly to merge/drop frames. Track last PTS to derive a
	// stable per-sample duration when buffer.Duration() is missing.
	videoPTSSaw  bool
	audioPTSSaw  bool
	lastVideoPTS gst.ClockTime
	lastAudioPTS gst.ClockTime
	lastVideoDur time.Duration
	lastAudioDur time.Duration
	// E2EE encryption stats (for verification)
	videoEncryptedFrames int
	audioEncryptedFrames int
	videoEncryptedBytes  int64
	audioEncryptedBytes  int64
}

// NewGStreamerPublisher creates a new GStreamer-based publisher for the given MP4 file.
//
// The publisher demuxes the MP4 file and configures appsinks to pull video
// and Opus audio samples, which are then written to the provided LiveKit local tracks.
//
// Parameters:
//   - filePath: Path to MP4 file containing H.264/AV1 video and Opus audio
//   - videoCodec: Video codec MIME type (e.g. webrtc.MimeTypeH264, webrtc.MimeTypeAV1)
//   - videoTrack: LiveKit local video track to publish video samples
//   - audioTrack: LiveKit local audio track to publish Opus samples
//
// Returns a configured GStreamerPublisher ready to Start(), or an error if
// pipeline creation fails.
func NewGStreamerPublisher(filePath string, videoCodec string, videoTrack, audioTrack sampleTrackWriter) (*GStreamerPublisher, error) {
	gst.Init(nil)

	p := &GStreamerPublisher{
		videoTrack: videoTrack,
		audioTrack: audioTrack,
		videoCodec: videoCodec,
	}

	var pipelineStr string
	switch strings.ToLower(videoCodec) {
	case strings.ToLower(webrtc.MimeTypeH264):
		pipelineStr = fmt.Sprintf(`
			filesrc location="%s" ! qtdemux name=demux
		demux.video_0 ! queue ! h264parse config-interval=1 ! video/x-h264,stream-format=byte-stream,alignment=au ! appsink name=videosink emit-signals=true
			demux.audio_0 ! queue ! opusparse ! audio/x-opus ! appsink name=audiosink emit-signals=true
		`, filePath)
	case strings.ToLower(webrtc.MimeTypeAV1):
		pipelineStr = fmt.Sprintf(`
			filesrc location="%s" ! qtdemux name=demux
		demux.video_0 ! queue ! av1parse ! video/x-av1,stream-format=obu-stream,alignment=tu ! appsink name=videosink emit-signals=true
			demux.audio_0 ! queue ! opusparse ! audio/x-opus ! appsink name=audiosink emit-signals=true
		`, filePath)
	default:
		return nil, fmt.Errorf("unsupported video codec: %s", videoCodec)
	}

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

			p.mu.Lock()
			stopped := p.stopped
			duration := p.durationFromGstBufferLocked(buffer, true)
			cipherBlock := p.cipherBlock
			videoCodec := p.videoCodec
			p.mu.Unlock()

			if !stopped && p.videoTrack != nil {
				flags := buffer.GetFlags()
				isKeyFrame := (flags & gst.BufferFlagDeltaUnit) == 0

				// Copy data and optionally encrypt
				sampleData := append([]byte{}, data...)
				if cipherBlock != nil {
					originalSize := len(sampleData)
					var err error
					switch strings.ToLower(videoCodec) {
					case strings.ToLower(webrtc.MimeTypeH264):
						// LiveKit E2EE operates on frame-level Annex B and assumes 4-byte start codes.
						// Normalize to 4-byte start codes so the receiver can reconstruct and decrypt
						// frame bytes deterministically from RTP.
						sampleData = normalizeH264AnnexBStartCodes(sampleData)
						sampleData, err = p.encryptSample(sampleData, e2eeUnencryptedVideoH264)
					case strings.ToLower(webrtc.MimeTypeAV1):
						sampleData, err = encryptAV1E2EEOBUStream(sampleData, cipherBlock, 0)
					default:
						err = fmt.Errorf("unsupported video codec for encryption: %s", videoCodec)
					}
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

			p.mu.Lock()
			stopped := p.stopped
			duration := p.durationFromGstBufferLocked(buffer, false)
			cipherBlock := p.cipherBlock
			p.mu.Unlock()

			if !stopped && p.audioTrack != nil {
				// Copy data and optionally encrypt
				sampleData := append([]byte{}, data...)
				if cipherBlock != nil {
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
	p.videoPTSSaw = false
	p.audioPTSSaw = false
	p.lastVideoPTS = 0
	p.lastAudioPTS = 0
	p.lastVideoDur = 0
	p.lastAudioDur = 0
	p.mu.Unlock()

	return nil
}

func (p *GStreamerPublisher) durationFromGstBufferLocked(buffer *gst.Buffer, isVideo bool) time.Duration {
	if buffer == nil {
		return 0
	}

	// Prefer buffer duration when available.
	if d := buffer.Duration(); d != gst.ClockTimeNone && d > 0 {
		return time.Duration(d)
	}

	pts := buffer.PresentationTimestamp()
	if pts == gst.ClockTimeNone {
		if isVideo {
			return p.lastVideoDur
		}
		return p.lastAudioDur
	}

	if isVideo {
		if p.videoPTSSaw && pts > p.lastVideoPTS {
			delta := time.Duration(pts - p.lastVideoPTS)
			// Sanity clamp: drop obviously broken deltas.
			if delta > 0 && delta < time.Second {
				p.lastVideoDur = delta
			}
		}
		p.lastVideoPTS = pts
		p.videoPTSSaw = true
		return p.lastVideoDur
	}

	if p.audioPTSSaw && pts > p.lastAudioPTS {
		delta := time.Duration(pts - p.lastAudioPTS)
		if delta > 0 && delta < time.Second {
			p.lastAudioDur = delta
		}
	}
	p.lastAudioPTS = pts
	p.audioPTSSaw = true
	return p.lastAudioDur
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

	unencryptedBytes = computeH264UnencryptedBytes(sample, unencryptedBytes)
	if len(sample) <= unencryptedBytes {
		return sample, nil // Too short to encrypt
	}

	// Create GCM cipher
	aesGCM, err := cipher.NewGCMWithNonceSize(block, e2eeIVLength)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random IV for this frame
	iv := make([]byte, e2eeIVLength)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("failed to generate IV: %w", err)
	}

	// LiveKit E2EE format:
	// [unencrypted header (N bytes AAD)] + [ciphertext + 16-byte tag] + [IV] + [ivLen (1)] + [keyID (1)]
	frameHeader := sample[:unencryptedBytes]
	plainText := sample[unencryptedBytes:]

	cipherText := aesGCM.Seal(nil, iv, plainText, frameHeader)

	frameTrailer := []byte{e2eeIVLength, 0} // ivLength=12, keyID=0
	payload := make([]byte, 0, len(cipherText)+len(iv)+len(frameTrailer))
	payload = append(payload, cipherText...)
	payload = append(payload, iv...)
	payload = append(payload, frameTrailer...)
	// Prevent accidental start-code patterns inside the encrypted payload by applying
	// RBSP escaping (emulation prevention bytes). The receiver must reverse this
	// before AES-GCM authentication/decryption.
	payload = addRBSPEscaping(payload)

	result := make([]byte, 0, len(frameHeader)+len(payload))
	result = append(result, frameHeader...)
	result = append(result, payload...)
	return result, nil
}

type h264NALUnit struct {
	data        []byte
	startOffset int
}

func computeH264UnencryptedBytes(annexB []byte, defaultSize int) int {
	nalUnits := findH264NALUnits(annexB)
	if len(nalUnits) == 0 {
		if defaultSize <= 0 {
			defaultSize = 10
		}
		if defaultSize > len(annexB) {
			defaultSize = len(annexB)
		}
		return defaultSize
	}

	firstSliceIdx := 0
	for i, nal := range nalUnits {
		if len(nal.data) == 0 {
			continue
		}
		nalType := nal.data[0] & 0x1F
		if nalType >= 1 && nalType <= 5 {
			firstSliceIdx = i
			break
		}
	}

	// Unencrypted bytes = everything up to first slice NAL + 2 bytes of slice header.
	unencryptedBytes := nalUnits[firstSliceIdx].startOffset + 2
	if unencryptedBytes > len(annexB) {
		unencryptedBytes = len(annexB)
	}
	return unencryptedBytes
}

func findH264NALUnits(data []byte) []h264NALUnit {
	var units []h264NALUnit
	i := 0

	for i < len(data) {
		startCodeLen := 0
		if i+4 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			startCodeLen = 4
		} else if i+3 <= len(data) && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			startCodeLen = 3
		}

		if startCodeLen == 0 {
			i++
			continue
		}

		nalStart := i + startCodeLen

		nalEnd := len(data)
		for j := nalStart; j < len(data)-3; j++ {
			if data[j] == 0 && data[j+1] == 0 {
				if (j+2 < len(data) && data[j+2] == 1) ||
					(j+3 < len(data) && data[j+2] == 0 && data[j+3] == 1) {
					nalEnd = j
					break
				}
			}
		}

		if nalStart < nalEnd {
			units = append(units, h264NALUnit{
				data:        data[nalStart:nalEnd],
				startOffset: nalStart,
			})
		}

		i = nalEnd
	}

	return units
}

func normalizeH264AnnexBStartCodes(data []byte) []byte {
	nalUnits := findH264NALUnits(data)
	if len(nalUnits) == 0 {
		return data
	}

	startCode := []byte{0x00, 0x00, 0x00, 0x01}
	normalized := make([]byte, 0, len(data)+len(nalUnits))
	for _, nal := range nalUnits {
		// Match Pion's H264Payloader behavior: it drops AUD (9) and filler (12) NALs.
		// If we keep them in the bytes we encrypt, they will disappear during RTP
		// packetization and AES-GCM authentication will fail on the receiver.
		if len(nal.data) > 0 {
			switch nal.data[0] & 0x1F {
			case 9, 12:
				continue
			}
		}
		normalized = append(normalized, startCode...)
		normalized = append(normalized, nal.data...)
	}
	return normalized
}

// addRBSPEscaping inserts H.264 emulation prevention bytes into a byte stream.
//
// It implements the same logic as LiveKit JS SDK `writeRbsp`:
// if there are two consecutive zeros and the next byte is <= 0x03, insert 0x03.
func addRBSPEscaping(data []byte) []byte {
	if len(data) < 3 {
		return data
	}

	const (
		zerosInStartSequence = 2
		emulationByte        = 0x03
	)

	out := make([]byte, 0, len(data))
	numConsecutiveZeros := 0
	for _, b := range data {
		if b <= emulationByte && numConsecutiveZeros >= zerosInStartSequence {
			out = append(out, emulationByte)
			numConsecutiveZeros = 0
		}
		out = append(out, b)
		if b == 0x00 {
			numConsecutiveZeros++
		} else {
			numConsecutiveZeros = 0
		}
	}
	return out
}
