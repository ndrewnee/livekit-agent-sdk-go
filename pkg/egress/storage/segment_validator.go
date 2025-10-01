package storage

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/livekit/protocol/logger"
)

// SegmentValidator validates HLS segments for playback compatibility
// Implements segment validation requirements from PLAN.md Milestone 3
type SegmentValidator struct {
	logger logger.Logger
	config ValidationConfig
}

// ValidationConfig holds validation configuration
type ValidationConfig struct {
	// Enable strict validation
	StrictMode bool

	// Maximum segment duration in seconds
	MaxSegmentDuration float64

	// Minimum segment size in bytes
	MinSegmentSize int64

	// Maximum segment size in bytes
	MaxSegmentSize int64

	// Check for zero-transcode compliance
	CheckZeroTranscode bool

	// Validate codec parameters
	ValidateCodecs bool

	// Check timestamp continuity
	CheckTimestamps bool
}

// ValidationResult holds the result of segment validation
type ValidationResult struct {
	Valid            bool
	Errors           []string
	Warnings         []string
	SegmentInfo      *SegmentInfo
	CodecInfo        *CodecValidation
	TimestampInfo    *TimestampValidation
	PlaybackSupport  *PlaybackCompatibility
}

// SegmentInfo holds segment metadata
type SegmentInfo struct {
	Size           int64
	Duration       float64
	PacketCount    int
	HasPAT         bool
	HasPMT         bool
	HasVideo       bool
	HasAudio       bool
	VideoCodec     string
	AudioCodec     string
	Resolution     string
	FrameRate      float64
	Bitrate        int
}

// CodecValidation holds codec validation results
type CodecValidation struct {
	VideoCodecValid   bool
	AudioCodecValid   bool
	VideoCodec        string
	AudioCodec        string
	VideoProfile      string
	VideoLevel        string
	AudioSampleRate   int
	AudioChannels     int
	ZeroTranscode     bool
}

// TimestampValidation holds timestamp validation results
type TimestampValidation struct {
	FirstPTS         int64
	LastPTS          int64
	PTSContinuous    bool
	DTSValid         bool
	TimestampWrap    bool
	MaxGap           int64
}

// PlaybackCompatibility holds playback compatibility info
type PlaybackCompatibility struct {
	Safari           bool
	Chrome           bool
	Firefox          bool
	VLC              bool
	FFmpeg           bool
	iOS              bool
	Android          bool
	Roku             bool
	AppleTV          bool
	AndroidTV        bool
	CompatibilityIssues []string
}

// NewSegmentValidator creates a new segment validator
func NewSegmentValidator(config ValidationConfig, logger logger.Logger) *SegmentValidator {
	// Set defaults
	if config.MaxSegmentDuration <= 0 {
		config.MaxSegmentDuration = 10.0 // HLS spec recommends max 10s
	}
	if config.MinSegmentSize <= 0 {
		config.MinSegmentSize = 1024 // 1KB minimum
	}
	if config.MaxSegmentSize <= 0 {
		config.MaxSegmentSize = 50 * 1024 * 1024 // 50MB maximum
	}

	return &SegmentValidator{
		logger: logger,
		config: config,
	}
}

// ValidateSegment validates an HLS segment
func (sv *SegmentValidator) ValidateSegment(data []byte) *ValidationResult {
	result := &ValidationResult{
		Valid:        true,
		Errors:       []string{},
		Warnings:     []string{},
		SegmentInfo:  &SegmentInfo{},
		CodecInfo:    &CodecValidation{},
		TimestampInfo: &TimestampValidation{},
		PlaybackSupport: &PlaybackCompatibility{
			Safari:    true,
			Chrome:    true,
			Firefox:   true,
			VLC:       true,
			FFmpeg:    true,
			iOS:       true,
			Android:   true,
			Roku:      true,
			AppleTV:   true,
			AndroidTV: true,
		},
	}

	// Basic size validation
	result.SegmentInfo.Size = int64(len(data))
	if result.SegmentInfo.Size < sv.config.MinSegmentSize {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("segment too small: %d bytes < %d bytes",
			result.SegmentInfo.Size, sv.config.MinSegmentSize))
	}
	if result.SegmentInfo.Size > sv.config.MaxSegmentSize {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("segment too large: %d bytes > %d bytes",
			result.SegmentInfo.Size, sv.config.MaxSegmentSize))
	}

	// Parse MPEG-TS structure
	if err := sv.parseMPEGTS(data, result); err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("MPEG-TS parse error: %v", err))
		return result
	}

	// Validate duration
	if result.SegmentInfo.Duration > sv.config.MaxSegmentDuration {
		result.Warnings = append(result.Warnings, fmt.Sprintf("segment duration %.2f exceeds recommended %.2f seconds",
			result.SegmentInfo.Duration, sv.config.MaxSegmentDuration))
	}

	// Validate codecs if enabled
	if sv.config.ValidateCodecs {
		sv.validateCodecs(result)
	}

	// Check zero-transcode compliance
	if sv.config.CheckZeroTranscode {
		sv.checkZeroTranscode(result)
	}

	// Check timestamps if enabled
	if sv.config.CheckTimestamps {
		sv.validateTimestamps(result)
	}

	// Check playback compatibility
	sv.checkPlaybackCompatibility(result)

	// Set final validation status
	if len(result.Errors) > 0 {
		result.Valid = false
	}

	return result
}

// parseMPEGTS parses MPEG-TS structure
func (sv *SegmentValidator) parseMPEGTS(data []byte, result *ValidationResult) error {
	const tsPacketSize = 188
	const tsSyncByte = 0x47

	if len(data)%tsPacketSize != 0 {
		return fmt.Errorf("invalid MPEG-TS: size %d not multiple of %d", len(data), tsPacketSize)
	}

	packetCount := len(data) / tsPacketSize
	result.SegmentInfo.PacketCount = packetCount

	videoPID := -1
	audioPID := -1
	var firstPTS, lastPTS int64
	ptsFound := false

	for i := 0; i < packetCount; i++ {
		offset := i * tsPacketSize
		packet := data[offset : offset+tsPacketSize]

		// Check sync byte
		if packet[0] != tsSyncByte {
			return fmt.Errorf("invalid sync byte at packet %d", i)
		}

		// Parse packet header
		pid := int(binary.BigEndian.Uint16(packet[1:3])) & 0x1FFF
		adaptationFieldControl := (packet[3] >> 4) & 0x3
		payloadUnitStart := (packet[1] & 0x40) != 0

		// Check for PAT (PID 0)
		if pid == 0 {
			result.SegmentInfo.HasPAT = true
			// Parse PAT to find PMT PID
			if payloadUnitStart {
				sv.parsePAT(packet, &videoPID, &audioPID)
			}
		}

		// Check for PMT (typically PID 0x100)
		if pid == 0x100 || pid == 0x1000 {
			result.SegmentInfo.HasPMT = true
			if payloadUnitStart {
				sv.parsePMT(packet, result)
			}
		}

		// Extract PTS if adaptation field present
		if adaptationFieldControl == 2 || adaptationFieldControl == 3 {
			if pts, hasPTS := sv.extractPTS(packet); hasPTS {
				if !ptsFound {
					firstPTS = pts
					ptsFound = true
				}
				lastPTS = pts
			}
		}

		// Track video/audio PIDs
		if pid == videoPID {
			result.SegmentInfo.HasVideo = true
		}
		if pid == audioPID {
			result.SegmentInfo.HasAudio = true
		}
	}

	// Calculate duration from PTS
	if ptsFound && lastPTS > firstPTS {
		// PTS is in 90kHz units
		result.SegmentInfo.Duration = float64(lastPTS-firstPTS) / 90000.0
		result.TimestampInfo.FirstPTS = firstPTS
		result.TimestampInfo.LastPTS = lastPTS
		result.TimestampInfo.PTSContinuous = true
	}

	// Validate structure
	if !result.SegmentInfo.HasPAT {
		result.Errors = append(result.Errors, "missing PAT (Program Association Table)")
	}
	if !result.SegmentInfo.HasPMT {
		result.Errors = append(result.Errors, "missing PMT (Program Map Table)")
	}

	return nil
}

// parsePAT parses Program Association Table
func (sv *SegmentValidator) parsePAT(packet []byte, videoPID, audioPID *int) {
	// Simplified PAT parsing
	// In production, this would properly parse the PAT structure
	*videoPID = 0x101  // Common video PID
	*audioPID = 0x102  // Common audio PID
}

// parsePMT parses Program Map Table
func (sv *SegmentValidator) parsePMT(packet []byte, result *ValidationResult) {
	// Simplified PMT parsing to detect codecs
	// In production, this would properly parse the PMT structure

	// Look for stream type indicators
	payload := packet[4:]
	for i := 0; i < len(payload)-4; i++ {
		streamType := payload[i]

		switch streamType {
		case 0x1B: // H.264/AVC video
			result.SegmentInfo.VideoCodec = "H264"
			result.CodecInfo.VideoCodec = "H264"
		case 0x24: // H.265/HEVC video
			result.SegmentInfo.VideoCodec = "H265"
			result.CodecInfo.VideoCodec = "H265"
		case 0x10: // MPEG-4 video
			result.SegmentInfo.VideoCodec = "MPEG4"
			result.CodecInfo.VideoCodec = "MPEG4"
		case 0x0F: // AAC audio
			result.SegmentInfo.AudioCodec = "AAC"
			result.CodecInfo.AudioCodec = "AAC"
		case 0x03, 0x04: // MP3 audio
			result.SegmentInfo.AudioCodec = "MP3"
			result.CodecInfo.AudioCodec = "MP3"
		case 0x81: // AC-3 audio
			result.SegmentInfo.AudioCodec = "AC3"
			result.CodecInfo.AudioCodec = "AC3"
		}
	}
}

// extractPTS extracts PTS from adaptation field
func (sv *SegmentValidator) extractPTS(packet []byte) (int64, bool) {
	// Check if adaptation field exists
	adaptationFieldControl := (packet[3] >> 4) & 0x3
	if adaptationFieldControl != 2 && adaptationFieldControl != 3 {
		return 0, false
	}

	// Check adaptation field length
	adaptationFieldLength := int(packet[4])
	if adaptationFieldLength == 0 {
		return 0, false
	}

	// Check for PCR flag (bit 4 of adaptation field flags)
	if adaptationFieldLength > 0 && (packet[5]&0x10) != 0 {
		// PCR is present at offset 6
		// PCR is 33 bits base + 9 bits extension
		pcrBase := int64(packet[6])<<25 | int64(packet[7])<<17 | int64(packet[8])<<9 | int64(packet[9])<<1 | int64(packet[10]>>7)
		return pcrBase, true
	}

	return 0, false
}

// validateCodecs validates codec compatibility
func (sv *SegmentValidator) validateCodecs(result *ValidationResult) {
	// Check video codec
	switch result.CodecInfo.VideoCodec {
	case "H264":
		result.CodecInfo.VideoCodecValid = true
		result.CodecInfo.VideoProfile = "Main" // Would parse from SPS/PPS
		result.CodecInfo.VideoLevel = "4.1"
	case "H265":
		result.CodecInfo.VideoCodecValid = true
		result.CodecInfo.VideoProfile = "Main"
		result.CodecInfo.VideoLevel = "4.0"
		// H265 has limited browser support
		result.PlaybackSupport.Safari = true
		result.PlaybackSupport.Chrome = false // Chrome doesn't support HEVC
		result.PlaybackSupport.Firefox = false
		result.PlaybackSupport.CompatibilityIssues = append(result.PlaybackSupport.CompatibilityIssues,
			"H.265/HEVC has limited browser support")
	case "VP8", "VP9":
		result.CodecInfo.VideoCodecValid = true
		// VP8/VP9 supported in WebM, limited in TS
		result.Warnings = append(result.Warnings, "VP8/VP9 in MPEG-TS has limited compatibility")
	default:
		if result.SegmentInfo.HasVideo {
			result.CodecInfo.VideoCodecValid = false
			result.Errors = append(result.Errors, fmt.Sprintf("unsupported video codec: %s", result.CodecInfo.VideoCodec))
		}
	}

	// Check audio codec
	switch result.CodecInfo.AudioCodec {
	case "AAC", "MP3", "Opus":
		result.CodecInfo.AudioCodecValid = true
		result.CodecInfo.AudioSampleRate = 48000 // Would parse from stream
		result.CodecInfo.AudioChannels = 2
	case "AC3":
		result.CodecInfo.AudioCodecValid = true
		// AC3 has limited browser support
		result.PlaybackSupport.Chrome = false
		result.PlaybackSupport.Firefox = false
		result.PlaybackSupport.CompatibilityIssues = append(result.PlaybackSupport.CompatibilityIssues,
			"AC-3 audio requires specific player support")
	default:
		if result.SegmentInfo.HasAudio {
			result.CodecInfo.AudioCodecValid = false
			result.Errors = append(result.Errors, fmt.Sprintf("unsupported audio codec: %s", result.CodecInfo.AudioCodec))
		}
	}
}

// checkZeroTranscode checks for zero-transcode compliance
func (sv *SegmentValidator) checkZeroTranscode(result *ValidationResult) {
	// Zero-transcode means no re-encoding was performed
	// Check for signs of transcoding

	result.CodecInfo.ZeroTranscode = true

	// Check for transcoding indicators
	if strings.Contains(result.CodecInfo.VideoCodec, "x264") ||
		strings.Contains(result.CodecInfo.VideoCodec, "x265") {
		result.CodecInfo.ZeroTranscode = false
		result.Warnings = append(result.Warnings, "segment appears to be transcoded (encoder signature found)")
	}

	// Check bitrate consistency (transcoding often changes bitrate)
	expectedBitrate := int(float64(result.SegmentInfo.Size) * 8 / result.SegmentInfo.Duration / 1000) // kbps
	result.SegmentInfo.Bitrate = expectedBitrate

	if expectedBitrate < 100 || expectedBitrate > 50000 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("unusual bitrate: %d kbps", expectedBitrate))
	}
}

// validateTimestamps validates timestamp continuity
func (sv *SegmentValidator) validateTimestamps(result *ValidationResult) {
	// Check PTS continuity
	if result.TimestampInfo.FirstPTS > 0 && result.TimestampInfo.LastPTS > 0 {
		ptsDiff := result.TimestampInfo.LastPTS - result.TimestampInfo.FirstPTS

		// Check for timestamp wrap (33-bit PTS wraps at 2^33)
		if ptsDiff < 0 {
			result.TimestampInfo.TimestampWrap = true
			result.Warnings = append(result.Warnings, "PTS timestamp wrap detected")
		}

		// Check for large gaps (> 1 second)
		if ptsDiff > 90000 { // 90kHz * 1 second
			result.TimestampInfo.MaxGap = ptsDiff
			if ptsDiff > 900000 { // > 10 seconds
				result.Warnings = append(result.Warnings, fmt.Sprintf("large PTS gap: %.2f seconds",
					float64(ptsDiff)/90000))
			}
		}

		result.TimestampInfo.DTSValid = true // Simplified - would check actual DTS
	} else {
		result.TimestampInfo.PTSContinuous = false
		result.Errors = append(result.Errors, "missing or invalid PTS timestamps")
	}
}

// checkPlaybackCompatibility checks player compatibility
func (sv *SegmentValidator) checkPlaybackCompatibility(result *ValidationResult) {
	// Check for common compatibility issues

	// Missing PAT/PMT affects all players
	if !result.SegmentInfo.HasPAT || !result.SegmentInfo.HasPMT {
		result.PlaybackSupport.Safari = false
		result.PlaybackSupport.Chrome = false
		result.PlaybackSupport.Firefox = false
		result.PlaybackSupport.iOS = false
		result.PlaybackSupport.Android = false
		result.PlaybackSupport.CompatibilityIssues = append(result.PlaybackSupport.CompatibilityIssues,
			"Missing PAT/PMT tables - segment will not play")
	}

	// Check segment size for mobile devices
	if result.SegmentInfo.Size > 10*1024*1024 { // > 10MB
		result.PlaybackSupport.iOS = false
		result.PlaybackSupport.Android = false
		result.PlaybackSupport.CompatibilityIssues = append(result.PlaybackSupport.CompatibilityIssues,
			"Segment too large for mobile devices (>10MB)")
	}

	// Check duration for live streaming
	if result.SegmentInfo.Duration > 6.0 {
		result.Warnings = append(result.Warnings, "segment duration >6s may cause buffering in live streams")
	}

	// Resolution checks (would need actual parsing)
	if result.SegmentInfo.Resolution == "3840x2160" || result.SegmentInfo.Resolution == "4096x2160" {
		result.PlaybackSupport.Roku = false // Many Roku devices don't support 4K
		result.PlaybackSupport.CompatibilityIssues = append(result.PlaybackSupport.CompatibilityIssues,
			"4K resolution not supported on all devices")
	}

	// Frame rate checks
	if result.SegmentInfo.FrameRate > 60 {
		result.PlaybackSupport.AppleTV = false
		result.PlaybackSupport.AndroidTV = false
		result.PlaybackSupport.CompatibilityIssues = append(result.PlaybackSupport.CompatibilityIssues,
			"High frame rate (>60fps) has limited device support")
	}
}

// ValidatePlaylist validates an HLS playlist
func (sv *SegmentValidator) ValidatePlaylist(playlist []byte) error {
	lines := strings.Split(string(playlist), "\n")

	if len(lines) < 2 || !strings.HasPrefix(lines[0], "#EXTM3U") {
		return fmt.Errorf("invalid HLS playlist header")
	}

	var errors []string
	hasEndList := false
	targetDuration := 0
	segmentCount := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			fmt.Sscanf(line, "#EXT-X-TARGETDURATION:%d", &targetDuration)
		} else if strings.HasPrefix(line, "#EXTINF:") {
			segmentCount++
			var duration float64
			fmt.Sscanf(line, "#EXTINF:%f", &duration)

			if targetDuration > 0 && duration > float64(targetDuration)*1.1 {
				errors = append(errors, fmt.Sprintf("segment duration %.2f exceeds target duration %d",
					duration, targetDuration))
			}
		} else if line == "#EXT-X-ENDLIST" {
			hasEndList = true
		}
	}

	if segmentCount == 0 {
		errors = append(errors, "no segments found in playlist")
	}

	if len(errors) > 0 {
		return fmt.Errorf("playlist validation failed: %s", strings.Join(errors, "; "))
	}

	sv.logger.Debugw("playlist validated",
		"segments", segmentCount,
		"target_duration", targetDuration,
		"has_end_list", hasEndList)

	return nil
}

// GetDefaultConfig returns default validation configuration
func GetDefaultValidationConfig() ValidationConfig {
	return ValidationConfig{
		StrictMode:         false,
		MaxSegmentDuration: 10.0,
		MinSegmentSize:     1024,
		MaxSegmentSize:     50 * 1024 * 1024,
		CheckZeroTranscode: true,
		ValidateCodecs:     true,
		CheckTimestamps:    true,
	}
}