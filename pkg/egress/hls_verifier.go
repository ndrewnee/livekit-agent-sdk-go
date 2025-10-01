package egress

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HLSVerifier verifies HLS output is being generated correctly
type HLSVerifier struct {
	outputDir string
	sessionID string
}

// NewHLSVerifier creates a new HLS verifier
func NewHLSVerifier(outputDir, sessionID string) *HLSVerifier {
	return &HLSVerifier{
		outputDir: outputDir,
		sessionID: sessionID,
	}
}

// VerifyOutput checks if HLS output is being generated
func (v *HLSVerifier) VerifyOutput() (*HLSVerificationResult, error) {
	result := &HLSVerificationResult{
		Timestamp: time.Now(),
	}

	// Check if output directory exists
	sessionDir := filepath.Join(v.outputDir, v.sessionID)
	info, err := os.Stat(sessionDir)
	if err != nil {
		return result, fmt.Errorf("output directory not found: %w", err)
	}
	if !info.IsDir() {
		return result, fmt.Errorf("output path is not a directory")
	}
	result.OutputDirExists = true

	// Check for playlist file
	playlistPath := filepath.Join(sessionDir, "playlist.m3u8")
	if _, err := os.Stat(playlistPath); err == nil {
		result.PlaylistExists = true

		// Parse playlist
		if err := v.parsePlaylist(playlistPath, result); err != nil {
			return result, fmt.Errorf("failed to parse playlist: %w", err)
		}
	}

	// Check for segment files
	segments, err := filepath.Glob(filepath.Join(sessionDir, "*.ts"))
	if err == nil {
		result.SegmentCount = len(segments)
		result.Segments = make([]SegmentInfo, 0, len(segments))

		for _, segPath := range segments {
			info, err := os.Stat(segPath)
			if err == nil {
				result.Segments = append(result.Segments, SegmentInfo{
					Filename: filepath.Base(segPath),
					Size:     info.Size(),
					Modified: info.ModTime(),
				})

				result.TotalSize += info.Size()

				// Track latest segment
				if info.ModTime().After(result.LatestSegmentTime) {
					result.LatestSegmentTime = info.ModTime()
				}
			}
		}
	}

	// Calculate if actively generating
	if !result.LatestSegmentTime.IsZero() {
		timeSinceLastSegment := time.Since(result.LatestSegmentTime)
		result.IsActive = timeSinceLastSegment < 10*time.Second // Consider active if segment within 10s
	}

	// Verify segments are playable (basic check)
	if result.SegmentCount > 0 && len(result.Segments) > 0 {
		// Check first segment has valid MPEG-TS signature
		firstSegPath := filepath.Join(sessionDir, result.Segments[0].Filename)
		if isValidMPEGTS(firstSegPath) {
			result.ValidFormat = true
		}
	}

	return result, nil
}

// parsePlaylist parses the HLS playlist
func (v *HLSVerifier) parsePlaylist(path string, result *HLSVerificationResult) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()

		// Check for HLS tags
		if strings.HasPrefix(line, "#EXTM3U") {
			result.ValidPlaylist = true
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			fmt.Sscanf(line, "#EXT-X-TARGETDURATION:%d", &result.TargetDuration)
		}
		if strings.HasPrefix(line, "#EXT-X-VERSION:") {
			fmt.Sscanf(line, "#EXT-X-VERSION:%d", &result.Version)
		}
		if strings.HasPrefix(line, "#EXTINF:") {
			result.PlaylistSegments++
		}
	}

	return scanner.Err()
}

// isValidMPEGTS checks if file has valid MPEG-TS signature
func isValidMPEGTS(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	// MPEG-TS packets start with 0x47 sync byte
	buf := make([]byte, 188) // Standard TS packet size
	n, err := file.Read(buf)
	if err != nil || n < 1 {
		return false
	}

	return buf[0] == 0x47 // MPEG-TS sync byte
}

// WatchOutput monitors HLS output generation in real-time
func (v *HLSVerifier) WatchOutput(interval time.Duration, stopChan chan struct{}) chan *HLSVerificationResult {
	resultChan := make(chan *HLSVerificationResult)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(resultChan)

		for {
			select {
			case <-stopChan:
				return
			case <-ticker.C:
				result, _ := v.VerifyOutput()
				select {
				case resultChan <- result:
				default:
					// Skip if channel is full
				}
			}
		}
	}()

	return resultChan
}

// HLSVerificationResult contains HLS output verification results
type HLSVerificationResult struct {
	Timestamp         time.Time
	OutputDirExists   bool
	PlaylistExists    bool
	ValidPlaylist     bool
	SegmentCount      int
	PlaylistSegments  int
	Segments          []SegmentInfo
	TotalSize         int64
	LatestSegmentTime time.Time
	IsActive          bool
	ValidFormat       bool
	TargetDuration    int
	Version           int
}

// SegmentInfo contains information about an HLS segment
type SegmentInfo struct {
	Filename string
	Size     int64
	Modified time.Time
}

// IsHealthy returns true if HLS output is being generated correctly
func (r *HLSVerificationResult) IsHealthy() bool {
	return r.OutputDirExists &&
		r.PlaylistExists &&
		r.ValidPlaylist &&
		r.SegmentCount > 0 &&
		r.ValidFormat &&
		r.IsActive
}

// GetStatus returns a status message
func (r *HLSVerificationResult) GetStatus() string {
	if !r.OutputDirExists {
		return "Output directory not found"
	}
	if !r.PlaylistExists {
		return "No playlist file generated"
	}
	if !r.ValidPlaylist {
		return "Invalid playlist format"
	}
	if r.SegmentCount == 0 {
		return "No segments generated"
	}
	if !r.ValidFormat {
		return "Invalid segment format"
	}
	if !r.IsActive {
		return fmt.Sprintf("Inactive (last segment: %s ago)",
			time.Since(r.LatestSegmentTime).Round(time.Second))
	}

	return fmt.Sprintf("Healthy: %d segments, %.2f MB total",
		r.SegmentCount, float64(r.TotalSize)/(1024*1024))
}