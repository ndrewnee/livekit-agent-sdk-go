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

const (
	tsPacketSize   = 188
	ptsModulo      = uint64(1) << 33
	pcrModulo      = uint64(1) << 42
	ptsMarkerMask  = 0x01
	pcrReserved    = byte(0x7e)
	sanitizedDir   = "sanitized-playlists"
	tsFileExtLower = ".ts"
)

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

func selectReferenceFile(files []string) string {
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(filepath.Base(f)), "output.ts") {
			return f
		}
	}
	return files[0]
}

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

func decodePTS(data []byte) (uint64, byte) {
	prefix := data[0] & 0xF0
	value := (uint64(data[0]&0x0E) << 29) |
		(uint64(data[1]) << 22) |
		(uint64(data[2]&0xFE) << 14) |
		(uint64(data[3]) << 7) |
		(uint64(data[4]) >> 1)
	return value, prefix
}

func encodePTS(buf []byte, value uint64, prefix byte) {
	v := value % ptsModulo
	buf[0] = (prefix & 0xF0) | byte((v>>29)&0x0E) | ptsMarkerMask
	buf[1] = byte((v >> 22) & 0xFF)
	buf[2] = byte(((v >> 14) & 0xFE) | ptsMarkerMask)
	buf[3] = byte((v >> 7) & 0xFF)
	buf[4] = byte(((v << 1) & 0xFE) | ptsMarkerMask)
}

func decodePCR(data []byte) uint64 {
	base := (uint64(data[0]) << 25) |
		(uint64(data[1]) << 17) |
		(uint64(data[2]) << 9) |
		(uint64(data[3]) << 1) |
		(uint64(data[4]) >> 7)
	ext := ((uint64(data[4]) & 0x01) << 8) | uint64(data[5])
	return base*300 + ext
}

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

// normalizeSegmentTimestamps normalizes timestamps in a single MPEG-TS segment.
// This is used by real-time S3 uploader to normalize segments before upload.
func normalizeSegmentTimestamps(segmentPath string) error {
	// Analyze the segment to find PTS/PCR offsets
	ptsOffset, pcrOffset, err := analyzeTsOffsets(segmentPath)
	if err != nil {
		return fmt.Errorf("analyze segment offsets: %w", err)
	}

	// If offsets are already zero, no normalization needed
	if ptsOffset == 0 && pcrOffset == 0 {
		return nil
	}

	// Normalize the segment file
	if err := normalizeTsFile(segmentPath, ptsOffset, pcrOffset); err != nil {
		return fmt.Errorf("normalize segment: %w", err)
	}

	return nil
}
