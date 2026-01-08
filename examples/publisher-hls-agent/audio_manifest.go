package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AudioManifest describes the audio segments for a participant's recording.
// This manifest is used by the web player to fetch and decode Opus audio
// segments for mixing with other participants' audio streams.
type AudioManifest struct {
	// Version is the manifest format version (currently 1)
	Version int `json:"version"`

	// Codec is the audio codec used (always "opus")
	Codec string `json:"codec"`

	// SampleRate is the audio sample rate in Hz (typically 48000)
	SampleRate int `json:"sampleRate"`

	// Channels is the number of audio channels (typically 2 for stereo)
	Channels int `json:"channels"`

	// SegmentDuration is the target duration of each segment in seconds
	SegmentDuration float64 `json:"segmentDuration"`

	// MaxMixerParticipants is the recommended max participants for mixing
	MaxMixerParticipants int `json:"maxMixerParticipants"`

	// StartTime is the recording start time in ISO 8601 format
	// Used for synchronization across multiple participants
	StartTime string `json:"startTime"`

	// Init is the filename of the fMP4 initialization segment.
	// Empty string indicates segments are self-contained (each has init data).
	Init string `json:"init,omitempty"`

	// Segments is the list of audio segments in order
	Segments []AudioSegment `json:"segments"`
}

// AudioSegment describes a single audio segment in the manifest.
type AudioSegment struct {
	// Index is the segment index (0-based)
	Index int `json:"index"`

	// File is the filename of the segment (e.g., "audio00000.m4s")
	File string `json:"file"`

	// Duration is the actual duration of this segment in seconds
	Duration float64 `json:"duration"`

	// StartTime is the start time of this segment relative to recording start (seconds)
	StartTime float64 `json:"startTime"`

	// Size is the file size in bytes (populated after segment is written)
	Size int64 `json:"size,omitempty"`
}

// AudioManifestWriter creates and updates the audio manifest file.
type AudioManifestWriter struct {
	mu           sync.Mutex
	manifest     *AudioManifest
	outputDir    string
	manifestPath string
}

// NewAudioManifestWriter creates a new manifest writer.
func NewAudioManifestWriter(outputDir string, cfg *Config, startTime time.Time) *AudioManifestWriter {
	return &AudioManifestWriter{
		outputDir:    outputDir,
		manifestPath: filepath.Join(outputDir, "audio.json"),
		manifest: &AudioManifest{
			Version:              1,
			Codec:                "opus",
			SampleRate:           48000,
			Channels:             2,
			SegmentDuration:      float64(cfg.SegmentDurationSecs),
			MaxMixerParticipants: cfg.MaxMixerParticipants,
			StartTime:            startTime.UTC().Format(time.RFC3339Nano),
			Init:                 "audio_init.mp4", // Separate init segment (extracted from first segment)
			Segments:             []AudioSegment{},
		},
	}
}

// AddSegment adds a new segment to the manifest.
func (w *AudioManifestWriter) AddSegment(index int, filename string, duration float64, startTime float64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	segment := AudioSegment{
		Index:     index,
		File:      filename,
		Duration:  duration,
		StartTime: startTime,
	}

	// Check file size
	filePath := filepath.Join(w.outputDir, filename)
	if info, err := os.Stat(filePath); err == nil {
		segment.Size = info.Size()
	}

	w.manifest.Segments = append(w.manifest.Segments, segment)
}

// Write writes the manifest to disk.
func (w *AudioManifestWriter) Write() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.MarshalIndent(w.manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal audio manifest: %w", err)
	}

	if err := os.WriteFile(w.manifestPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write audio manifest: %w", err)
	}

	return nil
}

// GetManifest returns a copy of the current manifest.
func (w *AudioManifestWriter) GetManifest() AudioManifest {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Return a copy
	manifest := *w.manifest
	manifest.Segments = make([]AudioSegment, len(w.manifest.Segments))
	copy(manifest.Segments, w.manifest.Segments)
	return manifest
}

// SegmentCount returns the number of segments in the manifest.
func (w *AudioManifestWriter) SegmentCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.manifest.Segments)
}

// WriteHLSPlaylist writes an HLS playlist (audio.m3u8) for the audio segments.
// This allows standard HLS players to play the audio stream directly.
func (w *AudioManifestWriter) WriteHLSPlaylist() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.manifest.Segments) == 0 {
		return fmt.Errorf("no segments to write")
	}

	playlistPath := filepath.Join(w.outputDir, "audio.m3u8")

	// Calculate max segment duration for EXT-X-TARGETDURATION
	maxDuration := 0.0
	for _, seg := range w.manifest.Segments {
		if seg.Duration > maxDuration {
			maxDuration = seg.Duration
		}
	}
	targetDuration := int(maxDuration) + 1

	// Build HLS playlist content
	var content string
	content += "#EXTM3U\n"
	content += "#EXT-X-VERSION:7\n" // Version 7 for fMP4 support
	content += "#EXT-X-MEDIA-SEQUENCE:0\n"
	content += fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", targetDuration)
	content += "#EXT-X-INDEPENDENT-SEGMENTS\n" // Each segment is independently decodable
	content += "\n"

	// Add init segment map (required for fMP4/CMAF segments)
	if w.manifest.Init != "" {
		content += fmt.Sprintf("#EXT-X-MAP:URI=\"%s\"\n", w.manifest.Init)
		content += "\n"
	}

	// Add each segment
	for _, seg := range w.manifest.Segments {
		content += fmt.Sprintf("#EXTINF:%.3f,\n", seg.Duration)
		content += seg.File + "\n"
	}

	// Mark as VOD (complete playlist)
	content += "#EXT-X-ENDLIST\n"

	if err := os.WriteFile(playlistPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write audio HLS playlist: %w", err)
	}

	return nil
}
