package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MPEG-TS timestamp normalization constants.
//
// These constants define the structure and constraints of MPEG Transport Stream
// packets and timestamp encoding as specified in ISO/IEC 13818-1.
const (
	// tsPacketSize is the fixed size of an MPEG-TS packet in bytes.
	// All MPEG-TS packets are exactly 188 bytes (not to be confused with
	// 204-byte packets used in some broadcast systems with FEC).
	tsPacketSize = 188

	// ptsModulo is the modulo value for PTS (Presentation Time Stamp) and DTS (Decoding Time Stamp).
	// PTS/DTS are 33-bit values, so they wrap around at 2^33 = 8589934592.
	// At 90kHz clock, this is approximately 26.5 hours.
	ptsModulo = uint64(1) << 33

	// pcrModulo is the modulo value for PCR (Program Clock Reference).
	// PCR is 42 bits, wrapping around at 2^42 = 4398046511104.
	// PCR = base * 300 + extension, where base is 33 bits and extension is 9 bits.
	pcrModulo = uint64(1) << 42

	// ptsMarkerMask is the marker bit mask used in PTS/DTS encoding.
	// The MPEG-TS specification requires marker bits (set to 1) at specific
	// positions to help detect bit errors during transmission.
	ptsMarkerMask = 0x01

	// pcrReserved is the reserved bits value in PCR encoding.
	// Bits 1-6 of byte 4 in the PCR field are reserved and must be set to 0x7e (111111b).
	pcrReserved = byte(0x7e)

	// sanitizedDir is the directory name for sanitized playlists.
	// This directory is skipped during timestamp normalization to avoid
	// re-processing already normalized files.
	sanitizedDir = "sanitized-playlists"

	// tsFileExtLower is the lowercase file extension for MPEG-TS segment files.
	tsFileExtLower = ".ts"
)

// normalizeHLSTimestamps normalizes PTS and PCR timestamps in all HLS segments
// within a recording directory.
//
// Background:
// When GStreamer creates HLS segments, the PTS (Presentation Time Stamp) and
// PCR (Program Clock Reference) values start from the timestamps of the input
// RTP packets, which can be arbitrarily large values. This causes compatibility
// issues with some video players and HLS validators that expect timestamps to
// start near zero.
//
// Algorithm:
//  1. Collect all .ts files in the output directory (excluding sanitized subdirectories)
//  2. Select a reference file (preferably output.ts, or the first file)
//  3. Analyze the reference file to determine PTS and PCR offsets
//  4. Normalize all .ts files by subtracting these offsets from all timestamps
//
// The normalization ensures that:
//   - The first PTS in the recording is close to 0
//   - The first PCR in the recording is close to 0
//   - All timestamps maintain their relative relationships
//   - Wraparound at 33-bit (PTS) and 42-bit (PCR) boundaries is handled correctly
//
// Parameters:
//   - outputDir: Directory containing HLS playlist and segment files
//
// Returns:
//   - nil if normalization succeeds or no .ts files are found
//   - error if file operations fail or MPEG-TS parsing encounters issues
//
// This function is typically called after HLS recording completes and before
// uploading to S3, ensuring playback compatibility across different players.
func normalizeHLSTimestamps(outputDir string) error {
	tsFiles, err := collectTsFiles(outputDir)
	if err != nil {
		return err
	}
	if len(tsFiles) == 0 {
		return nil
	}

	reference := selectReferenceFile(tsFiles)
	ptsOffset, pcrOffset, err := analyzeTsOffsets(reference)
	if err != nil {
		return fmt.Errorf("analyze TS offsets for %s: %w", reference, err)
	}

	for _, file := range tsFiles {
		if err := normalizeTsFile(file, ptsOffset, pcrOffset); err != nil {
			return fmt.Errorf("normalize %s: %w", file, err)
		}
	}
	return nil
}

// collectTsFiles recursively collects all .ts (MPEG-TS segment) file paths
// in a directory, excluding sanitized subdirectories.
//
// The function walks the directory tree and identifies files with the .ts extension.
// Subdirectories named "sanitized-playlists" are skipped to avoid re-processing
// already normalized files.
//
// Parameters:
//   - root: Root directory to search for .ts files
//
// Returns:
//   - files: Sorted slice of absolute paths to .ts files
//   - error: nil on success, error if directory walking fails
//
// The returned file list is sorted lexicographically, ensuring consistent
// processing order (e.g., segment00000.ts before segment00001.ts).
func collectTsFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if strings.Contains(path, sanitizedDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), tsFileExtLower) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// selectReferenceFile chooses which .ts file to use as the reference for
// calculating timestamp offsets.
//
// Selection priority:
//  1. If a file named "output.ts" exists, it is preferred (common for single-file recordings)
//  2. Otherwise, the first file in the sorted list is used (typically segment00000.ts)
//
// The reference file determines the PTS and PCR offsets that will be subtracted
// from all files during normalization. Using the first segment ensures timestamps
// start close to zero after normalization.
//
// Parameters:
//   - files: Sorted slice of .ts file paths
//
// Returns:
//   - path to the selected reference file
func selectReferenceFile(files []string) string {
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(filepath.Base(f)), "output.ts") {
			return f
		}
	}
	return files[0]
}

// analyzeTsOffsets analyzes an MPEG-TS file to extract the first PTS and PCR values.
//
// The function scans through MPEG-TS packets sequentially until it finds:
//   - The first PTS value in a PES (Packetized Elementary Stream) packet
//   - The first PCR value in an adaptation field
//
// MPEG-TS packet structure analyzed:
//  1. Sync byte (0x47) validation
//  2. Adaptation field control bits determine presence of adaptation field and payload
//  3. Adaptation field may contain PCR (6 bytes starting at offset+1)
//  4. Payload may contain PES packets with PTS/DTS timestamps
//
// PES packet structure:
//   - Start code: 0x000001
//   - PTS/DTS flags at offset+7 indicate timestamp presence
//   - PTS: 33-bit timestamp encoded in 5 bytes at offset+9
//   - DTS: Optional 33-bit timestamp encoded in 5 bytes at offset+14
//
// If PTS is found but PCR is not found, PCR is estimated as PTS * 300
// (since PCR = base * 300 + extension, and for estimation we set extension = 0).
//
// Parameters:
//   - path: Absolute path to the MPEG-TS file
//
// Returns:
//   - ptsOffset: First PTS value found (modulo 2^33)
//   - pcrOffset: First PCR value found (modulo 2^42), or estimated from PTS
//   - error: nil on success, error if file cannot be read or no PTS is found
func analyzeTsOffsets(path string) (uint64, uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}

	var ptsOffset uint64
	var pcrOffset uint64
	var ptsFound, pcrFound bool

	for i := 0; i+tsPacketSize <= len(data) && (!ptsFound || !pcrFound); i += tsPacketSize {
		packet := data[i : i+tsPacketSize]
		if packet[0] != 0x47 {
			return 0, 0, fmt.Errorf("sync byte mismatch at packet %d", i/tsPacketSize)
		}

		adaptationControl := (packet[3] >> 4) & 0x3
		hasAdaptation := adaptationControl == 2 || adaptationControl == 3
		hasPayload := adaptationControl == 1 || adaptationControl == 3
		offset := 4

		if hasAdaptation {
			if offset >= len(packet) {
				continue
			}
			adaptLen := int(packet[offset])
			offset++
			if offset+adaptLen > len(packet) {
				continue
			}
			if adaptLen >= 6 {
				flags := packet[offset]
				if (flags & 0x10) != 0 {
					pcrBytes := packet[offset+1 : offset+7]
					pcrVal := decodePCR(pcrBytes)
					if !pcrFound {
						pcrOffset = pcrVal
						pcrFound = true
					}
				}
			}
			offset += adaptLen
		}

		if hasPayload && offset < len(packet) {
			payloadStart := (packet[1] & 0x40) != 0
			if payloadStart && offset+6 <= len(packet) &&
				packet[offset] == 0x00 && packet[offset+1] == 0x00 && packet[offset+2] == 0x01 {

				if offset+9 > len(packet) {
					continue
				}
				ptsDtsFlags := (packet[offset+7] >> 6) & 0x3
				fieldsStart := offset + 9
				if ptsDtsFlags >= 0x2 && fieldsStart+5 <= len(packet) {
					ptsVal, _ := decodePTS(packet[fieldsStart : fieldsStart+5])
					if !ptsFound {
						ptsOffset = ptsVal
						ptsFound = true
					}
				}
				if ptsDtsFlags == 0x3 {
					dtsStart := fieldsStart + 5
					if dtsStart+5 <= len(packet) {
						if !ptsFound {
							dtsVal, _ := decodePTS(packet[dtsStart : dtsStart+5])
							ptsOffset = dtsVal
							ptsFound = true
						}
					}
				}
			}
		}
	}

	if !ptsFound {
		return 0, 0, errors.New("PTS not found")
	}
	if !pcrFound {
		pcrOffset = ptsOffset * 300
	}
	return ptsOffset % ptsModulo, pcrOffset % pcrModulo, nil
}

// normalizeTsFile rewrites an MPEG-TS file, subtracting offsets from all PTS, DTS,
// and PCR timestamps.
//
// The function performs in-place modification of the file:
//  1. Reads the entire file into memory
//  2. Iterates through each 188-byte MPEG-TS packet
//  3. For packets with PCR in adaptation field: subtract pcrOffset with modulo wraparound
//  4. For packets with PTS/DTS in PES payload: subtract ptsOffset with modulo wraparound
//  5. Writes the modified data back to the same file path
//
// Timestamp normalization details:
//   - PTS/DTS: 33-bit values, wraparound at 2^33
//   - PCR: 42-bit values, wraparound at 2^42
//   - Subtraction handles modulo wraparound correctly (see subMod function)
//   - Marker bits in PTS/DTS encoding are preserved
//   - Reserved bits in PCR encoding are preserved
//
// Parameters:
//   - path: Absolute path to the MPEG-TS file to normalize
//   - ptsOffset: Value to subtract from all PTS and DTS timestamps
//   - pcrOffset: Value to subtract from all PCR timestamps
//
// Returns:
//   - nil if normalization succeeds or offsets are both zero (no-op)
//   - error if file cannot be read, MPEG-TS sync bytes are invalid, or write fails
//
// The file permissions are preserved during rewriting.
func normalizeTsFile(path string, ptsOffset, pcrOffset uint64) error {
	if ptsOffset == 0 && pcrOffset == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	for i := 0; i+tsPacketSize <= len(data); i += tsPacketSize {
		packet := data[i : i+tsPacketSize]
		if packet[0] != 0x47 {
			return fmt.Errorf("sync byte mismatch at packet %d", i/tsPacketSize)
		}

		adaptationControl := (packet[3] >> 4) & 0x3
		hasAdaptation := adaptationControl == 2 || adaptationControl == 3
		hasPayload := adaptationControl == 1 || adaptationControl == 3
		offset := 4

		if hasAdaptation {
			if offset >= len(packet) {
				continue
			}
			adaptLen := int(packet[offset])
			offset++
			if offset+adaptLen > len(packet) {
				continue
			}
			if adaptLen >= 6 {
				flags := packet[offset]
				if (flags & 0x10) != 0 {
					pcrBytes := packet[offset+1 : offset+7]
					pcrVal := decodePCR(pcrBytes)
					pcrVal = subMod(pcrVal, pcrOffset, pcrModulo)
					encodePCR(pcrBytes, pcrVal)
				}
			}
			offset += adaptLen
		}

		if hasPayload && offset < len(packet) {
			payloadStart := (packet[1] & 0x40) != 0
			if !payloadStart || offset+6 > len(packet) {
				continue
			}
			if packet[offset] != 0x00 || packet[offset+1] != 0x00 || packet[offset+2] != 0x01 {
				continue
			}

			if offset+9 > len(packet) {
				continue
			}
			ptsDtsFlags := (packet[offset+7] >> 6) & 0x3
			fieldsStart := offset + 9

			if ptsDtsFlags >= 0x2 && fieldsStart+5 <= len(packet) {
				ptsVal, prefix := decodePTS(packet[fieldsStart : fieldsStart+5])
				ptsVal = subMod(ptsVal, ptsOffset, ptsModulo)
				encodePTS(packet[fieldsStart:fieldsStart+5], ptsVal, prefix)
			}
			if ptsDtsFlags == 0x3 {
				dtsStart := fieldsStart + 5
				if dtsStart+5 <= len(packet) {
					dtsVal, prefix := decodePTS(packet[dtsStart : dtsStart+5])
					dtsVal = subMod(dtsVal, ptsOffset, ptsModulo)
					encodePTS(packet[dtsStart:dtsStart+5], dtsVal, prefix)
				}
			}
		}
	}

	return os.WriteFile(path, data, info.Mode())
}

// decodePTS decodes a PTS (Presentation Time Stamp) or DTS (Decoding Time Stamp)
// from its 5-byte MPEG-TS encoding.
//
// PTS/DTS encoding format (5 bytes):
//
//	Byte 0: [prefix(4 bits)][pts32-30(3 bits)][marker(1 bit)]
//	Byte 1: [pts29-22(8 bits)]
//	Byte 2: [pts21-15(7 bits)][marker(1 bit)]
//	Byte 3: [pts14-7(8 bits)]
//	Byte 4: [pts6-0(7 bits)][marker(1 bit)]
//
// The prefix (upper 4 bits of byte 0) indicates the timestamp type:
//   - 0x20: PTS only
//   - 0x30: PTS with DTS following
//   - 0x10: DTS (when following PTS)
//
// Marker bits (always 1) are used for error detection and are stripped during decoding.
//
// Parameters:
//   - data: 5-byte slice containing the encoded PTS/DTS value
//
// Returns:
//   - value: Decoded 33-bit PTS/DTS value (0 to 8589934591)
//   - prefix: Upper 4 bits of byte 0, indicating timestamp type
//
// The prefix is returned separately so it can be preserved when re-encoding
// after timestamp normalization.
func decodePTS(data []byte) (uint64, byte) {
	prefix := data[0] & 0xF0
	value := (uint64(data[0]&0x0E) << 29) |
		(uint64(data[1]) << 22) |
		(uint64(data[2]&0xFE) << 14) |
		(uint64(data[3]) << 7) |
		(uint64(data[4]) >> 1)
	return value, prefix
}

// encodePTS encodes a PTS (Presentation Time Stamp) or DTS (Decoding Time Stamp)
// into its 5-byte MPEG-TS format.
//
// This is the inverse of decodePTS. The function:
//  1. Reduces value modulo 2^33 to ensure it fits in 33 bits
//  2. Splits the 33-bit value across 5 bytes according to MPEG-TS format
//  3. Preserves the prefix (timestamp type indicator) in the upper 4 bits of byte 0
//  4. Sets marker bits to 1 at required positions (bytes 0, 2, 4)
//
// Encoding format (5 bytes):
//
//	Byte 0: [prefix(4 bits)][pts32-30(3 bits)][marker=1(1 bit)]
//	Byte 1: [pts29-22(8 bits)]
//	Byte 2: [pts21-15(7 bits)][marker=1(1 bit)]
//	Byte 3: [pts14-7(8 bits)]
//	Byte 4: [pts6-0(7 bits)][marker=1(1 bit)]
//
// Parameters:
//   - buf: 5-byte buffer to write the encoded PTS/DTS (modified in place)
//   - value: 33-bit timestamp value to encode
//   - prefix: Upper 4 bits indicating timestamp type (0x20, 0x30, or 0x10)
//
// The prefix is typically obtained from decodePTS to ensure the timestamp type
// indicator is preserved during normalization.
func encodePTS(buf []byte, value uint64, prefix byte) {
	v := value % ptsModulo
	buf[0] = (prefix & 0xF0) | byte((v>>29)&0x0E) | ptsMarkerMask
	buf[1] = byte((v >> 22) & 0xFF)
	buf[2] = byte(((v >> 14) & 0xFE) | ptsMarkerMask)
	buf[3] = byte((v >> 7) & 0xFF)
	buf[4] = byte(((v << 1) & 0xFE) | ptsMarkerMask)
}

// decodePCR decodes a PCR (Program Clock Reference) from its 6-byte MPEG-TS encoding.
//
// PCR encoding format (6 bytes, 48 bits total):
//   - Base: 33 bits (bits 47-15)
//   - Reserved: 6 bits (bits 14-9), must be 0x3F (111111b)
//   - Extension: 9 bits (bits 8-0)
//
// PCR value = base * 300 + extension
//
// The PCR uses a 27MHz clock (base at 90kHz * 300), providing higher precision
// than PTS/DTS (which use 90kHz). This precision is necessary for accurate
// synchronization and jitter control.
//
// Byte structure:
//
//	Bytes 0-3: base[32:1]
//	Byte 4: [base0][reserved(6 bits)][ext8]
//	Byte 5: ext[7:0]
//
// Parameters:
//   - data: 6-byte slice containing the encoded PCR value
//
// Returns:
//   - PCR value as a 42-bit integer (base * 300 + extension)
//
// The function extracts base and extension separately, then combines them
// using the formula: PCR = base * 300 + extension.
func decodePCR(data []byte) uint64 {
	base := (uint64(data[0]) << 25) |
		(uint64(data[1]) << 17) |
		(uint64(data[2]) << 9) |
		(uint64(data[3]) << 1) |
		(uint64(data[4]) >> 7)
	ext := ((uint64(data[4]) & 0x01) << 8) | uint64(data[5])
	return base*300 + ext
}

// encodePCR encodes a PCR (Program Clock Reference) into its 6-byte MPEG-TS format.
//
// This is the inverse of decodePCR. The function:
//  1. Reduces value modulo 2^42 to ensure it fits in 42 bits
//  2. Splits PCR into base (33 bits) and extension (9 bits) using: base = PCR / 300, ext = PCR % 300
//  3. Distributes base and extension across 6 bytes according to MPEG-TS format
//  4. Sets reserved bits (bits 14-9 of the 48-bit field) to 0x7e (111111b)
//
// Encoding format (6 bytes):
//
//	Bytes 0-3: base[32:1]
//	Byte 4: [base0][reserved=0x7e(6 bits)][ext8]
//	Byte 5: ext[7:0]
//
// The reserved field (6 bits set to 1) is required by the MPEG-TS specification
// and helps decoders validate the PCR structure.
//
// Parameters:
//   - buf: 6-byte buffer to write the encoded PCR (modified in place)
//   - value: 42-bit PCR value to encode
//
// The encoding preserves the relationship: PCR = base * 300 + extension,
// ensuring accurate clock reference after normalization.
func encodePCR(buf []byte, value uint64) {
	total := value % pcrModulo
	base := total / 300
	ext := total % 300

	buf[0] = byte(base >> 25)
	buf[1] = byte(base >> 17)
	buf[2] = byte(base >> 9)
	buf[3] = byte(base >> 1)
	buf[4] = byte((base<<7)&0x80) | pcrReserved | byte(ext>>8)
	buf[5] = byte(ext & 0xFF)
}

// subMod performs modular subtraction: (value - offset) mod m.
//
// This function correctly handles timestamp wraparound when subtracting offsets
// from PTS/PCR values. Since timestamps are stored in limited bit widths
// (33 bits for PTS, 42 bits for PCR), they wrap around to 0 after reaching
// their maximum value.
//
// Cases handled:
//  1. offset == 0: Simply reduce value modulo m (no-op case)
//  2. value >= offset: Normal subtraction, then modulo
//  3. value < offset: Wraparound case, compute (m - (offset - value)) mod m
//
// Example (PTS with 33-bit wraparound):
//   - mod = 2^33 = 8589934592
//   - value = 100, offset = 200
//   - Since value < offset, we wrapped around
//   - Result = (8589934592 - 100) mod 8589934592 = 8589934492
//
// This ensures normalized timestamps are always in the range [0, mod-1].
//
// Parameters:
//   - value: Current timestamp value
//   - offset: Offset to subtract
//   - mod: Modulo value (ptsModulo or pcrModulo)
//
// Returns:
//   - (value - offset) mod m, handling wraparound correctly
func subMod(value, offset, mod uint64) uint64 {
	if offset == 0 {
		return value % mod
	}
	if value >= offset {
		return (value - offset) % mod
	}
	diff := offset - value
	rem := diff % mod
	if rem == 0 {
		return 0
	}
	return (mod - rem) % mod
}
