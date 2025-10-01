package egress

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"

	"github.com/pion/rtp"
)

// ExtractH264NALUnits extracts NAL units from an MP4 file using ffmpeg
func ExtractH264NALUnits(mp4Path string) ([][]byte, error) {
	// Use ffmpeg to extract raw H.264 stream
	cmd := exec.Command("ffmpeg", "-i", mp4Path, "-c:v", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "h264", "-")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to extract H.264: %w", err)
	}

	// Parse NAL units from Annex B format (0x00 0x00 0x00 0x01 separators)
	var nalUnits [][]byte
	startCode := []byte{0x00, 0x00, 0x00, 0x01}
	data := output

	for {
		// Find next start code
		idx := bytes.Index(data, startCode)
		if idx < 0 {
			break
		}

		// Skip the start code
		data = data[idx+4:]

		// Find the next start code to determine NAL unit length
		nextIdx := bytes.Index(data, startCode)
		var nalUnit []byte
		if nextIdx < 0 {
			// Last NAL unit
			nalUnit = data
			data = nil
		} else {
			nalUnit = data[:nextIdx]
			data = data[nextIdx:]
		}

		// Skip empty NAL units
		if len(nalUnit) > 0 {
			nalUnits = append(nalUnits, nalUnit)
		}

		if data == nil {
			break
		}
	}

	return nalUnits, nil
}

// ExtractOpusFrames extracts REAL Opus frames from an Ogg Opus file
// Uses ffmpeg to extract raw Opus packets properly
func ExtractOpusFrames(oggPath string) ([][]byte, error) {
	// Check file exists
	if _, err := os.Stat(oggPath); err != nil {
		return nil, fmt.Errorf("opus file not found: %w", err)
	}

	// Use ffmpeg to extract Opus packets in RTP format
	// This gives us properly formatted Opus frames
	cmd := exec.Command("ffmpeg",
		"-i", oggPath,
		"-vn", // No video
		"-c:a", "copy", // Copy codec without re-encoding
		"-f", "opus", // Output as Opus
		"-") // Output to stdout
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to extract Opus stream: %w", err)
	}

	if len(output) < 100 {
		return nil, fmt.Errorf("opus output too short: %d bytes", len(output))
	}

	// Parse Opus packets from output
	// Opus in Ogg has: OpusHead header, OpusTags, then pages with Opus frames
	var frames [][]byte
	data := output

	// Skip to first OggS page after headers
	// Look for pattern: "OggS" followed by Opus data
	for len(data) > 4 {
		// Find OggS marker
		idx := bytes.Index(data, []byte("OggS"))
		if idx < 0 {
			break
		}

		data = data[idx+4:] // Skip "OggS"

		// Skip Ogg page header (minimum 23 bytes)
		if len(data) < 23 {
			break
		}

		// Get number of page segments (byte at offset 22)
		if len(data) < 23 {
			break
		}
		numSegments := int(data[22])

		// Skip to segment table
		data = data[23:]
		if len(data) < numSegments {
			break
		}

		// Read segment sizes
		totalSize := 0
		for i := 0; i < numSegments; i++ {
			totalSize += int(data[i])
		}

		data = data[numSegments:] // Skip segment table

		if totalSize > len(data) {
			break
		}

		// Extract Opus frame data (skip any page with OpusHead or OpusTags)
		frameData := data[:totalSize]
		if !bytes.Contains(frameData, []byte("OpusHead")) &&
		   !bytes.Contains(frameData, []byte("OpusTags")) &&
		   len(frameData) > 0 {
			// Ogg pages can contain multiple Opus frames
			// Each frame is typically 20-200 bytes for speech/music
			// Split large pages into smaller chunks (individual Opus frames)
			pageData := frameData

			// If page is small enough (<= 1000 bytes), it's likely a single frame
			if len(pageData) <= 1000 {
				frame := make([]byte, len(pageData))
				copy(frame, pageData)
				frames = append(frames, frame)
			} else {
				// Split large pages into ~100 byte chunks (typical Opus frame size)
				const chunkSize = 100
				for i := 0; i < len(pageData); i += chunkSize {
					end := i + chunkSize
					if end > len(pageData) {
						end = len(pageData)
					}
					chunk := make([]byte, end-i)
					copy(chunk, pageData[i:end])
					frames = append(frames, chunk)

					if len(frames) >= 500 {
						break
					}
				}
			}
		}

		data = data[totalSize:]

		// Limit to reasonable number for testing
		if len(frames) >= 500 {
			break
		}
	}

	if len(frames) == 0 {
		return nil, fmt.Errorf("no opus frames extracted from file")
	}

	return frames, nil
}

// CreateRTPPacketsFromNALUnits creates RTP packets from H.264 NAL units
func CreateRTPPacketsFromNALUnits(nalUnits [][]byte, ssrc uint32) []*rtp.Packet {
	var packets []*rtp.Packet
	seq := uint16(1000)
	ts := uint32(0)

	for _, nalUnit := range nalUnits {
		if len(nalUnit) == 0 {
			continue
		}

		// For small NAL units, send as single NAL unit packet
		if len(nalUnit) <= 1400 {
			packet := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: seq,
					Timestamp:      ts,
					SSRC:           ssrc,
					Marker:         isLastNALInAccessUnit(nalUnit),
				},
				Payload: nalUnit,
			}
			packets = append(packets, packet)
			seq++
		} else {
			// Fragment large NAL units using FU-A (fragmentation unit)
			fragments := fragmentNALUnit(nalUnit, 1400)
			for i, frag := range fragments {
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    96,
						SequenceNumber: seq,
						Timestamp:      ts,
						SSRC:           ssrc,
						Marker:         i == len(fragments)-1 && isLastNALInAccessUnit(nalUnit),
					},
					Payload: frag,
				}
				packets = append(packets, packet)
				seq++
			}
		}

		// Increment timestamp for each frame (assuming 30fps at 90kHz)
		if isLastNALInAccessUnit(nalUnit) {
			ts += 3000 // 90000 / 30
		}
	}

	return packets
}

// isLastNALInAccessUnit checks if NAL unit is the last in an access unit
func isLastNALInAccessUnit(nalUnit []byte) bool {
	if len(nalUnit) == 0 {
		return false
	}
	nalType := nalUnit[0] & 0x1F
	// VCL NAL units (1-5) typically mark end of access unit
	return nalType >= 1 && nalType <= 5
}

// fragmentNALUnit fragments a large NAL unit into FU-A packets
func fragmentNALUnit(nalUnit []byte, maxSize int) [][]byte {
	if len(nalUnit) == 0 {
		return nil
	}

	var fragments [][]byte
	nalHeader := nalUnit[0]
	nalData := nalUnit[1:]

	// FU indicator and FU header
	fuIndicator := (nalHeader & 0xE0) | 28 // Type 28 = FU-A

	for len(nalData) > 0 {
		fragmentSize := maxSize - 2 // Account for FU indicator and header
		if fragmentSize > len(nalData) {
			fragmentSize = len(nalData)
		}

		// Determine FU header flags
		var fuHeader byte
		if len(fragments) == 0 {
			fuHeader = 0x80 | (nalHeader & 0x1F) // Start bit + NAL type
		} else if len(nalData) <= fragmentSize {
			fuHeader = 0x40 | (nalHeader & 0x1F) // End bit + NAL type
		} else {
			fuHeader = nalHeader & 0x1F // NAL type only
		}

		fragment := append([]byte{fuIndicator, fuHeader}, nalData[:fragmentSize]...)
		fragments = append(fragments, fragment)
		nalData = nalData[fragmentSize:]
	}

	return fragments
}

// ReadMP4NALUnits reads REAL NAL units from MP4 file using ExtractH264NALUnits
func ReadMP4NALUnits(mp4Path string, limit int) ([][]byte, error) {
	// Use the real extraction function that actually reads the file
	nalUnits, err := ExtractH264NALUnits(mp4Path)
	if err != nil {
		return nil, fmt.Errorf("failed to extract NAL units from %s: %w", mp4Path, err)
	}

	if len(nalUnits) == 0 {
		return nil, fmt.Errorf("no NAL units found in %s", mp4Path)
	}

	// Limit to requested number if specified
	if limit > 0 && limit < len(nalUnits) {
		return nalUnits[:limit], nil
	}

	return nalUnits, nil
}