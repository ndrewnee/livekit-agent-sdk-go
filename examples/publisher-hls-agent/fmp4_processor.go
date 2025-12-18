package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
)

// fMP4 box types for parsing segment structure
const (
	boxTypeFtyp = "ftyp" // File type box
	boxTypeMoov = "moov" // Movie box (initialization)
	boxTypeMoof = "moof" // Movie fragment box (media)
	boxTypeStyp = "styp" // Segment type box
	boxTypeMdat = "mdat" // Media data box
)

// boxHeader represents an fMP4 box header
type boxHeader struct {
	Size uint32
	Type string
}

// readBoxHeader reads a box header from the reader
func readBoxHeader(r io.Reader) (*boxHeader, error) {
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		return nil, err
	}

	typeBytes := make([]byte, 4)
	if _, err := io.ReadFull(r, typeBytes); err != nil {
		return nil, err
	}

	return &boxHeader{
		Size: size,
		Type: string(typeBytes),
	}, nil
}

// findInitSegmentBoundary finds the byte offset where init data ends and media data begins.
// In a self-contained fMP4 segment, the structure is:
//
//	[ftyp][moov] - initialization data
//	[moof][mdat] - media data (or [styp][moof][mdat] for CMAF segments)
//
// Returns the offset where media data starts (right after moov ends).
func findInitSegmentBoundary(data []byte) (int, error) {
	r := bytes.NewReader(data)
	offset := 0

	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read box header at offset %d: %w", offset, err)
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(data) {
			return 0, fmt.Errorf("box %s at offset %d extends beyond data (size %d, data len %d)",
				header.Type, offset, header.Size, len(data))
		}

		// Init segment consists of ftyp and moov boxes
		// Media data starts with moof or styp
		if header.Type == boxTypeMoof || header.Type == boxTypeStyp {
			return offset, nil
		}

		// Skip to next box
		skipBytes := int(header.Size) - 8 // We already read 8 bytes (size + type)
		if skipBytes > 0 {
			if _, err := r.Seek(int64(skipBytes), io.SeekCurrent); err != nil {
				return 0, fmt.Errorf("skip box %s content: %w", header.Type, err)
			}
		}
		offset = boxEnd
	}

	// No media data found, entire file is init data
	return len(data), nil
}

// extractInitSegment extracts the initialization segment (ftyp+moov) from an fMP4 file.
// Returns the init segment data.
func extractInitSegment(data []byte) ([]byte, error) {
	boundary, err := findInitSegmentBoundary(data)
	if err != nil {
		return nil, err
	}
	if boundary == 0 {
		return nil, fmt.Errorf("no init segment found")
	}
	return data[:boundary], nil
}

// extractMediaSegment extracts the media segment (moof+mdat or styp+moof+mdat) from an fMP4 file.
// Returns the media segment data.
func extractMediaSegment(data []byte) ([]byte, error) {
	boundary, err := findInitSegmentBoundary(data)
	if err != nil {
		return nil, err
	}
	if boundary >= len(data) {
		return nil, fmt.Errorf("no media segment found")
	}
	return data[boundary:], nil
}

// processFMP4Segment processes a CMAF/fMP4 segment file for HLS compatibility.
// For the first segment (index 0):
//   - Extracts and writes the init segment to initPath
//   - Returns the media-only data
//
// For subsequent segments:
//   - Returns the media-only data (strips init if present)
//
// This handles the case where splitmuxsink+cmafmux creates self-contained segments
// with duplicate moov atoms, converting them to proper CMAF format.
func processFMP4Segment(segmentPath string, segmentIndex int, initPath string) ([]byte, error) {
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		return nil, fmt.Errorf("read segment: %w", err)
	}

	if len(data) < 8 {
		return nil, fmt.Errorf("segment too small: %d bytes", len(data))
	}

	// Find init/media boundary.
	boundary, err := findInitSegmentBoundary(data)
	if err != nil {
		return nil, fmt.Errorf("find boundary: %w", err)
	}

	log.Printf("[fmp4] segment %d: total %d bytes, init boundary at %d", segmentIndex, len(data), boundary)

	// For first segment, extract and save init data.
	if segmentIndex == 0 && boundary > 0 {
		initData := data[:boundary]
		log.Printf("[fmp4] writing init segment to %s (%d bytes)", initPath, len(initData))
		if err := os.WriteFile(initPath, initData, 0644); err != nil {
			return nil, fmt.Errorf("write init segment: %w", err)
		}
		log.Printf("[fmp4] init segment written successfully")
	}

	// Return media-only data.
	if boundary >= len(data) {
		// No media data, just return the whole thing (shouldn't happen for valid segments).
		return data, nil
	}

	return data[boundary:], nil
}

// processAudioSegment processes an audio segment file for HLS/CMAF compatibility.
// For the first segment (index 0):
//   - Extracts and writes the init segment to initPath
//   - Returns the media-only data
//
// For subsequent segments:
//   - Returns the media-only data (strips init if present)
//
// This handles the case where splitmuxsink+cmafmux creates self-contained segments
// with duplicate moov atoms, converting them to proper CMAF format.
func processAudioSegment(segmentPath string, segmentIndex int, initPath string) ([]byte, error) {
	return processFMP4Segment(segmentPath, segmentIndex, initPath)
}

// createStypBox creates a CMAF segment type box (styp)
// This is required at the start of CMAF media segments.
func createStypBox(brand string) []byte {
	if len(brand) != 4 {
		brand = "iso6"
	}
	// styp box structure:
	// - 4 bytes: size (28)
	// - 4 bytes: 'styp'
	// - 4 bytes: major brand 'cmf2'
	// - 4 bytes: minor version (0)
	// - 4 bytes: compatible brand 'iso6'
	// - 4 bytes: compatible brand 'cmfc'
	// - 4 bytes: compatible brand (codec-specific, e.g. 'opus', 'av01')
	buf := make([]byte, 28)
	binary.BigEndian.PutUint32(buf[0:4], 28)
	copy(buf[4:8], "styp")
	copy(buf[8:12], "cmf2")
	binary.BigEndian.PutUint32(buf[12:16], 0)
	copy(buf[16:20], "iso6")
	copy(buf[20:24], "cmfc")
	copy(buf[24:28], brand)
	return buf
}

// ensureStypPrefix ensures the media segment starts with a styp box.
// If it already has styp, returns as-is. Otherwise, prepends a styp box.
func ensureStypPrefix(mediaData []byte, brand string) []byte {
	if len(mediaData) < 8 {
		return mediaData
	}

	// Check if already has styp
	if string(mediaData[4:8]) == boxTypeStyp {
		return mediaData
	}

	// Prepend styp box
	styp := createStypBox(brand)
	result := make([]byte, len(styp)+len(mediaData))
	copy(result, styp)
	copy(result[len(styp):], mediaData)
	return result
}

func getMP4TrackTimescale(initSegmentPath string) (uint32, error) {
	data, err := os.ReadFile(initSegmentPath)
	if err != nil {
		return 0, fmt.Errorf("read init segment: %w", err)
	}
	if len(data) < 8 {
		return 0, fmt.Errorf("init segment too small: %d bytes", len(data))
	}

	moov, err := findFirstBoxContent(data, boxTypeMoov)
	if err != nil {
		return 0, err
	}
	trak, err := findFirstBoxContent(moov, "trak")
	if err != nil {
		return 0, err
	}
	mdia, err := findFirstBoxContent(trak, "mdia")
	if err != nil {
		return 0, err
	}
	mdhd, err := findFirstBoxContent(mdia, "mdhd")
	if err != nil {
		return 0, err
	}

	if len(mdhd) < 16 {
		return 0, fmt.Errorf("mdhd too small: %d bytes", len(mdhd))
	}

	version := mdhd[0]
	switch version {
	case 1:
		if len(mdhd) < 24 {
			return 0, fmt.Errorf("mdhd v1 too small: %d bytes", len(mdhd))
		}
		// version(1) + flags(3) + creation_time(8) + modification_time(8) + timescale(4)
		timescale := binary.BigEndian.Uint32(mdhd[20:24])
		if timescale == 0 {
			return 0, fmt.Errorf("invalid timescale 0")
		}
		return timescale, nil
	default:
		// version(1) + flags(3) + creation_time(4) + modification_time(4) + timescale(4)
		timescale := binary.BigEndian.Uint32(mdhd[12:16])
		if timescale == 0 {
			return 0, fmt.Errorf("invalid timescale 0")
		}
		return timescale, nil
	}
}

func findFirstBoxContent(data []byte, boxType string) ([]byte, error) {
	r := bytes.NewReader(data)
	offset := 0

	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read box header at offset %d: %w", offset, err)
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(data) {
			return nil, fmt.Errorf("box %s at offset %d extends beyond data (size %d, data len %d)",
				header.Type, offset, header.Size, len(data))
		}

		if header.Type == boxType {
			return data[offset+8 : boxEnd], nil
		}

		skipBytes := int(header.Size) - 8
		if skipBytes > 0 {
			if _, err := r.Seek(int64(skipBytes), io.SeekCurrent); err != nil {
				return nil, fmt.Errorf("skip box %s content: %w", header.Type, err)
			}
		}
		offset = boxEnd
	}

	return nil, fmt.Errorf("no %s box found", boxType)
}

// getAudioSegmentDuration parses an fMP4 audio segment and returns its actual duration in seconds.
// It reads the trun box from the moof to get sample count and durations, then divides by timescale (48kHz for Opus).
// This is used to compute accurate manifest timing instead of using the configured target duration.
func getAudioSegmentDuration(segmentPath string, timescale uint32) (float64, error) {
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		return 0, fmt.Errorf("read segment: %w", err)
	}

	if len(data) < 8 {
		return 0, fmt.Errorf("segment too small: %d bytes", len(data))
	}

	// Find the moof box
	r := bytes.NewReader(data)
	var moofData []byte
	offset := 0

	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read box header at offset %d: %w", offset, err)
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(data) {
			break
		}

		if header.Type == boxTypeMoof {
			moofData = data[offset+8 : boxEnd] // Skip size+type
			break
		}

		// Skip to next box
		skipBytes := int(header.Size) - 8
		if skipBytes > 0 {
			if _, err := r.Seek(int64(skipBytes), io.SeekCurrent); err != nil {
				return 0, fmt.Errorf("skip box %s: %w", header.Type, err)
			}
		}
		offset = boxEnd
	}

	if moofData == nil {
		return 0, fmt.Errorf("no moof box found")
	}

	// Parse moof to find traf → trun
	duration, err := parseMoofForDuration(moofData, timescale)
	if err != nil {
		return 0, fmt.Errorf("parse moof: %w", err)
	}

	return duration, nil
}

// parseMoofForDuration extracts the total duration from a moof box.
// It parses: moof → traf → (tfhd for defaults) + trun (for sample durations)
func parseMoofForDuration(moofData []byte, timescale uint32) (float64, error) {
	r := bytes.NewReader(moofData)
	offset := 0

	// Find traf box inside moof
	var trafData []byte
	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(moofData) {
			break
		}

		if header.Type == "traf" {
			trafData = moofData[offset+8 : boxEnd]
			break
		}

		skipBytes := int(header.Size) - 8
		if skipBytes > 0 {
			r.Seek(int64(skipBytes), io.SeekCurrent)
		}
		offset = boxEnd
	}

	if trafData == nil {
		return 0, fmt.Errorf("no traf box found")
	}

	// Parse traf to find tfhd and trun
	return parseTrafForDuration(trafData, timescale)
}

// parseTrafForDuration extracts duration from a traf box by reading tfhd (defaults) and trun (samples).
func parseTrafForDuration(trafData []byte, timescale uint32) (float64, error) {
	r := bytes.NewReader(trafData)
	offset := 0

	var defaultSampleDuration uint32 = 960 // Default Opus frame at 48kHz (20ms)
	var trunData []byte

	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(trafData) {
			break
		}

		boxContent := trafData[offset+8 : boxEnd]

		switch header.Type {
		case "tfhd":
			// Parse tfhd for default sample duration
			if len(boxContent) >= 8 {
				flags := uint32(boxContent[1])<<16 | uint32(boxContent[2])<<8 | uint32(boxContent[3])
				tfhdOffset := 4 // Skip version(1) + flags(3)
				tfhdOffset += 4 // Skip track_id

				if flags&0x000001 != 0 {
					tfhdOffset += 8 // base_data_offset
				}
				if flags&0x000002 != 0 {
					tfhdOffset += 4 // sample_description_index
				}
				if flags&0x000008 != 0 && tfhdOffset+4 <= len(boxContent) {
					defaultSampleDuration = binary.BigEndian.Uint32(boxContent[tfhdOffset : tfhdOffset+4])
				}
			}
		case "trun":
			trunData = boxContent
		}

		skipBytes := int(header.Size) - 8
		if skipBytes > 0 {
			r.Seek(int64(skipBytes), io.SeekCurrent)
		}
		offset = boxEnd
	}

	if trunData == nil {
		return 0, fmt.Errorf("no trun box found")
	}

	// Parse trun to calculate total duration
	return parseTrunForDuration(trunData, defaultSampleDuration, timescale)
}

// parseTrunForDuration calculates total duration from a trun box.
func parseTrunForDuration(trunData []byte, defaultDuration uint32, timescale uint32) (float64, error) {
	if len(trunData) < 8 {
		return 0, fmt.Errorf("trun too small")
	}

	flags := uint32(trunData[1])<<16 | uint32(trunData[2])<<8 | uint32(trunData[3])
	sampleCount := binary.BigEndian.Uint32(trunData[4:8])

	trunOffset := 8

	// Data offset (if present)
	if flags&0x000001 != 0 {
		trunOffset += 4
	}

	// First sample flags (if present)
	if flags&0x000004 != 0 {
		trunOffset += 4
	}

	// Calculate total duration from all samples
	var totalDuration uint64 = 0

	for i := uint32(0); i < sampleCount; i++ {
		duration := defaultDuration

		// Read per-sample duration if present
		if flags&0x000100 != 0 {
			if trunOffset+4 > len(trunData) {
				break
			}
			duration = binary.BigEndian.Uint32(trunData[trunOffset : trunOffset+4])
			trunOffset += 4
		}

		// Skip sample size if present
		if flags&0x000200 != 0 {
			trunOffset += 4
		}

		// Skip sample flags if present
		if flags&0x000400 != 0 {
			trunOffset += 4
		}

		// Skip composition time offset if present
		if flags&0x000800 != 0 {
			trunOffset += 4
		}

		totalDuration += uint64(duration)
	}

	// Convert to seconds: duration / timescale
	durationSeconds := float64(totalDuration) / float64(timescale)
	return durationSeconds, nil
}

// AudioSegmentInfo contains timing information extracted from an fMP4 audio segment.
type AudioSegmentInfo struct {
	// BaseDecodeTime is the tfdt value in timescale units (when this segment's audio starts in the timeline)
	BaseDecodeTime uint64
	// Duration is the total duration in seconds
	Duration float64
	// StartTimeSeconds is BaseDecodeTime converted to seconds
	StartTimeSeconds float64
}

// getAudioSegmentInfo extracts complete timing information from an fMP4 audio segment.
// It reads both the tfdt (to get actual start time) and trun (to get duration).
// This is critical for accurate manifest timing - we can't just use cumulative durations
// because GStreamer may have startup delays or gaps between segments.
func getAudioSegmentInfo(segmentPath string, timescale uint32) (*AudioSegmentInfo, error) {
	data, err := os.ReadFile(segmentPath)
	if err != nil {
		return nil, fmt.Errorf("read segment: %w", err)
	}

	if len(data) < 8 {
		return nil, fmt.Errorf("segment too small: %d bytes", len(data))
	}

	// Find the moof box
	r := bytes.NewReader(data)
	var moofData []byte
	offset := 0

	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read box header at offset %d: %w", offset, err)
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(data) {
			break
		}

		if header.Type == boxTypeMoof {
			moofData = data[offset+8 : boxEnd] // Skip size+type
			break
		}

		// Skip to next box
		skipBytes := int(header.Size) - 8
		if skipBytes > 0 {
			if _, err := r.Seek(int64(skipBytes), io.SeekCurrent); err != nil {
				return nil, fmt.Errorf("skip box %s: %w", header.Type, err)
			}
		}
		offset = boxEnd
	}

	if moofData == nil {
		return nil, fmt.Errorf("no moof box found")
	}

	// Parse moof to extract tfdt and duration
	return parseMoofForSegmentInfo(moofData, timescale)
}

// parseMoofForSegmentInfo extracts timing info from a moof box.
func parseMoofForSegmentInfo(moofData []byte, timescale uint32) (*AudioSegmentInfo, error) {
	r := bytes.NewReader(moofData)
	offset := 0

	// Find traf box inside moof
	var trafData []byte
	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(moofData) {
			break
		}

		if header.Type == "traf" {
			trafData = moofData[offset+8 : boxEnd]
			break
		}

		skipBytes := int(header.Size) - 8
		if skipBytes > 0 {
			r.Seek(int64(skipBytes), io.SeekCurrent)
		}
		offset = boxEnd
	}

	if trafData == nil {
		return nil, fmt.Errorf("no traf box found")
	}

	return parseTrafForSegmentInfo(trafData, timescale)
}

// parseTrafForSegmentInfo extracts timing info from a traf box.
func parseTrafForSegmentInfo(trafData []byte, timescale uint32) (*AudioSegmentInfo, error) {
	r := bytes.NewReader(trafData)
	offset := 0

	var defaultSampleDuration uint32 = 960 // Default Opus frame at 48kHz (20ms)
	var trunData []byte
	var baseDecodeTime uint64 = 0

	for {
		header, err := readBoxHeader(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		boxEnd := offset + int(header.Size)
		if boxEnd > len(trafData) {
			break
		}

		boxContent := trafData[offset+8 : boxEnd]

		switch header.Type {
		case "tfhd":
			// Parse tfhd for default sample duration
			if len(boxContent) >= 8 {
				flags := uint32(boxContent[1])<<16 | uint32(boxContent[2])<<8 | uint32(boxContent[3])
				tfhdOffset := 4 // Skip version(1) + flags(3)
				tfhdOffset += 4 // Skip track_id

				if flags&0x000001 != 0 {
					tfhdOffset += 8 // base_data_offset
				}
				if flags&0x000002 != 0 {
					tfhdOffset += 4 // sample_description_index
				}
				if flags&0x000008 != 0 && tfhdOffset+4 <= len(boxContent) {
					defaultSampleDuration = binary.BigEndian.Uint32(boxContent[tfhdOffset : tfhdOffset+4])
					// Some muxers (or corrupted segments) may emit a zero default duration.
					// For Opus in CMAF, 20ms (960 samples at 48kHz) is the common fixed frame size.
					// Treat 0 as invalid and fall back to 960 so duration calculations remain sane.
					if defaultSampleDuration == 0 {
						defaultSampleDuration = 960
					}
				}
			}
		case "tfdt":
			// Parse tfdt for base decode time
			if len(boxContent) >= 8 {
				version := boxContent[0]
				if version == 1 {
					// 64-bit base_media_decode_time
					if len(boxContent) >= 12 {
						baseDecodeTime = binary.BigEndian.Uint64(boxContent[4:12])
					}
				} else {
					// 32-bit base_media_decode_time
					baseDecodeTime = uint64(binary.BigEndian.Uint32(boxContent[4:8]))
				}
			}
		case "trun":
			trunData = boxContent
		}

		skipBytes := int(header.Size) - 8
		if skipBytes > 0 {
			r.Seek(int64(skipBytes), io.SeekCurrent)
		}
		offset = boxEnd
	}

	if trunData == nil {
		return nil, fmt.Errorf("no trun box found")
	}

	// Parse trun to calculate total duration
	duration, err := parseTrunForDuration(trunData, defaultSampleDuration, timescale)
	if err != nil {
		return nil, err
	}

	return &AudioSegmentInfo{
		BaseDecodeTime:   baseDecodeTime,
		Duration:         duration,
		StartTimeSeconds: float64(baseDecodeTime) / float64(timescale),
	}, nil
}
