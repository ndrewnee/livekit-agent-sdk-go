package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// fixHLSPlaylist corrects the duration of the final segment in an HLS playlist.
//
// Background:
// GStreamer's hlssink element has a known bug where it writes an invalid duration
// (typically a very large number like 18446743552) for the final segment when it
// receives EOS (end-of-stream). This occurs because hlssink doesn't know the exact
// end time of the last segment until after it has already written the playlist.
//
// Algorithm:
//  1. Read the playlist.m3u8 file from outputDir
//  2. Scan backwards to find the last #EXTINF directive and its segment filename
//  3. Parse the current duration from the #EXTINF line
//  4. If duration is already valid (≤10 seconds), skip correction
//  5. Calculate actual duration by reading PTS timestamps from the .ts file
//  6. Update the #EXTINF line with the correct duration
//  7. Write the corrected playlist back to disk
//
// Parameters:
//   - outputDir: Directory containing playlist.m3u8 and segment files
//
// Returns:
//   - nil if correction succeeds or is not needed
//   - error if playlist cannot be read, parsed, or written
//
// Special cases:
//   - Returns nil for empty or malformed playlists
//   - Returns nil if no segments are found
//   - Returns nil if final segment was already uploaded to S3 (logs warning)
//   - Skips correction if duration is already valid (>0 and ≤10 seconds)
//
// This function is called after recording stops to ensure the HLS playlist
// is playback-compatible. Without this fix, video players may fail to play
// the final segment or calculate incorrect total duration.
func fixHLSPlaylist(outputDir string) error {
	playlistPath := filepath.Join(outputDir, "playlist.m3u8")
	data, err := os.ReadFile(playlistPath)
	if err != nil {
		return fmt.Errorf("read playlist: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 3 {
		return nil // Empty or malformed playlist
	}

	// Find the last EXTINF line and its corresponding segment
	var lastExtinfIndex int
	var lastExtinfLine string
	var lastSegmentFile string

	extinfRe := regexp.MustCompile(`^#EXTINF:([0-9.]+),`)

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || line == "#EXT-X-ENDLIST" {
			continue
		}
		if extinfRe.MatchString(line) {
			lastExtinfLine = line
			lastExtinfIndex = i
			// Next non-empty line should be the segment filename
			for j := i + 1; j < len(lines); j++ {
				segLine := strings.TrimSpace(lines[j])
				if segLine != "" && !strings.HasPrefix(segLine, "#") {
					lastSegmentFile = segLine
					break
				}
			}
			break
		}
	}

	if lastExtinfLine == "" || lastSegmentFile == "" {
		return nil // No segments found
	}

	// Parse the current duration
	matches := extinfRe.FindStringSubmatch(lastExtinfLine)
	if len(matches) != 2 {
		return fmt.Errorf("invalid EXTINF format: %s", lastExtinfLine)
	}

	currentDuration, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return fmt.Errorf("parse duration: %w", err)
	}

	// If duration looks valid (< 10 seconds for 2s target), don't modify
	if currentDuration > 0 && currentDuration <= 10 {
		return nil
	}

	// Calculate actual duration from the TS file
	segmentPath := filepath.Join(outputDir, lastSegmentFile)

	// Check if segment file exists (may have been uploaded and deleted with S3_REALTIME_UPLOAD)
	if _, err := os.Stat(segmentPath); os.IsNotExist(err) {
		// Log warning but don't fail - segment was already uploaded to S3
		// The playlist on S3 will have the invalid duration, but this is a known GStreamer bug
		log.Printf("segment file %s not found (uploaded to S3): cannot fix final segment duration (GStreamer bug workaround skipped)", lastSegmentFile)
		return nil
	}

	actualDuration, err := calculateSegmentDuration(segmentPath)
	if err != nil {
		return fmt.Errorf("calculate segment duration for %s: %w", lastSegmentFile, err)
	}

	// Update the EXTINF line with the correct duration
	lines[lastExtinfIndex] = fmt.Sprintf("#EXTINF:%.10f,", actualDuration)

	// Write the corrected playlist
	corrected := strings.Join(lines, "\n")
	if err := os.WriteFile(playlistPath, []byte(corrected), 0644); err != nil {
		return fmt.Errorf("write corrected playlist: %w", err)
	}

	return nil
}

// calculateSegmentDuration calculates the duration of an MPEG-TS segment by finding
// the first and last PTS timestamps.
//
// MPEG-TS background:
// MPEG Transport Stream packets contain PTS (Presentation Time Stamp) values that
// indicate when each frame should be presented to the user. These timestamps use
// a 90kHz clock and are encoded in 33 bits, allowing them to wrap around after
// approximately 26.5 hours.
//
// Algorithm:
//  1. Read the entire .ts file into memory
//  2. Scan from the beginning to find the first packet with a PTS value
//  3. Scan from the end backwards to find the last packet with a PTS value
//  4. Calculate duration = lastPTS - firstPTS
//  5. Handle PTS wraparound if lastPTS < firstPTS (crossed 33-bit boundary)
//  6. Convert from 90kHz clock ticks to seconds
//
// Parameters:
//   - tsPath: Absolute path to the MPEG-TS segment file
//
// Returns:
//   - duration in seconds (float64)
//   - error if file cannot be read or no PTS timestamps are found
//
// PTS wraparound handling:
// If lastPTS < firstPTS, we assume the timestamp wrapped around the 33-bit
// boundary (2^33 = 8589934592 at 90kHz = ~26.5 hours). The calculation becomes:
//
//	duration = (ptsModulo - firstPTS) + lastPTS
//
// Time conversion:
// PTS values use a 90kHz clock, so we divide by 90000.0 to get seconds.
// For example, 180000 PTS ticks = 2.0 seconds.
//
// This function is used by fixHLSPlaylist to determine the actual duration
// of the final segment when GStreamer's hlssink writes an invalid value.
func calculateSegmentDuration(tsPath string) (float64, error) {
	data, err := os.ReadFile(tsPath)
	if err != nil {
		return 0, fmt.Errorf("read TS file: %w", err)
	}

	var firstPTS, lastPTS uint64
	var foundFirst, foundLast bool

	// Scan from the beginning to find first PTS
	for i := 0; i+tsPacketSize <= len(data) && !foundFirst; i += tsPacketSize {
		if pts, ok := extractPTSFromPacket(data[i : i+tsPacketSize]); ok {
			firstPTS = pts
			foundFirst = true
		}
	}

	// Scan from the end to find last PTS
	for i := len(data) - tsPacketSize; i >= 0 && !foundLast; i -= tsPacketSize {
		if pts, ok := extractPTSFromPacket(data[i : i+tsPacketSize]); ok {
			lastPTS = pts
			foundLast = true
		}
	}

	if !foundFirst || !foundLast {
		return 0, fmt.Errorf("could not find PTS timestamps in segment")
	}

	// Handle PTS wraparound
	var duration uint64
	if lastPTS >= firstPTS {
		duration = lastPTS - firstPTS
	} else {
		// PTS wrapped around the 33-bit boundary
		duration = (ptsModulo - firstPTS) + lastPTS
	}

	// Convert from 90kHz clock to seconds
	durationSeconds := float64(duration) / 90000.0
	return durationSeconds, nil
}

// extractPTSFromPacket extracts the PTS value from an MPEG-TS packet if present.
//
// MPEG-TS packet structure:
// An MPEG-TS packet is always 188 bytes and has the following structure:
//   - Byte 0: Sync byte (0x47)
//   - Bytes 1-3: Header (contains payload unit start indicator, PID, etc.)
//   - Byte 3 bits 4-5: Adaptation field control (determines presence of adaptation field and payload)
//   - Optional: Adaptation field (variable length)
//   - Optional: Payload (contains PES packets for audio/video data)
//
// PES packet structure (inside payload):
// PES (Packetized Elementary Stream) packets carry audio/video data and may contain PTS:
//   - Bytes 0-2: Start code (0x000001)
//   - Byte 6: Stream ID
//   - Byte 7 bits 6-7: PTS/DTS flags (0x2 = PTS only, 0x3 = PTS and DTS)
//   - Bytes 9-13: PTS value (33 bits encoded in 5 bytes)
//
// Algorithm:
//  1. Validate packet: check sync byte (0x47) and length (188 bytes)
//  2. Parse adaptation field control to determine payload presence
//  3. Skip adaptation field if present
//  4. Check payload unit start indicator (must be set for PES packet start)
//  5. Verify PES start code (0x000001)
//  6. Check PTS/DTS flags (must be >= 0x2 for PTS presence)
//  7. Decode PTS value from 5 bytes at offset 9 of PES header
//
// Parameters:
//   - packet: 188-byte MPEG-TS packet
//
// Returns:
//   - pts: PTS value in 90kHz clock ticks (valid only if ok is true)
//   - ok: true if PTS was successfully extracted, false otherwise
//
// Returns false if:
//   - Packet length is not 188 bytes
//   - Sync byte is not 0x47
//   - Packet has no payload
//   - Payload unit start indicator is not set
//   - PES start code is not found
//   - PTS/DTS flags indicate no PTS present
//   - Packet is too short to contain PTS data
//
// This function is used by calculateSegmentDuration to find timestamp values
// in MPEG-TS segments for duration calculation.
func extractPTSFromPacket(packet []byte) (uint64, bool) {
	if len(packet) != tsPacketSize || packet[0] != 0x47 {
		return 0, false
	}

	adaptationControl := (packet[3] >> 4) & 0x3
	hasPayload := adaptationControl == 1 || adaptationControl == 3
	offset := 4

	// Skip adaptation field if present
	if adaptationControl == 2 || adaptationControl == 3 {
		if offset >= len(packet) {
			return 0, false
		}
		adaptLen := int(packet[offset])
		offset++
		if offset+adaptLen > len(packet) {
			return 0, false
		}
		offset += adaptLen
	}

	if !hasPayload || offset >= len(packet) {
		return 0, false
	}

	// Check for payload unit start indicator
	payloadStart := (packet[1] & 0x40) != 0
	if !payloadStart {
		return 0, false
	}

	// Look for PES packet start code
	if offset+6 > len(packet) {
		return 0, false
	}
	if packet[offset] != 0x00 || packet[offset+1] != 0x00 || packet[offset+2] != 0x01 {
		return 0, false
	}

	// Check PTS/DTS flags
	if offset+9 > len(packet) {
		return 0, false
	}
	ptsDtsFlags := (packet[offset+7] >> 6) & 0x3
	if ptsDtsFlags < 0x2 {
		return 0, false // No PTS present
	}

	// Decode PTS
	fieldsStart := offset + 9
	if fieldsStart+5 > len(packet) {
		return 0, false
	}
	pts, _ := decodePTS(packet[fieldsStart : fieldsStart+5])
	return pts, true
}
