package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type thumbnailConfig struct {
	Interval time.Duration
	Width    int
	Height   int
	Ext      string
}

func generateThumbnailsWithOptionalFaceExtraction(
	ctx context.Context,
	inputPath,
	outputDir string,
	thumbCfg thumbnailConfig,
	faceCfg *faceExtractionConfig,
) (thumbnails []string, faceSummary *faceExtractionSummary, warnings []error, err error) {
	if faceCfg == nil || !faceCfg.Enabled {
		thumbnails, err = generateThumbnailsFFmpeg(ctx, inputPath, outputDir, thumbCfg)
		return thumbnails, nil, nil, err
	}

	framesDir, err := os.MkdirTemp("", "publisher-hls-face-frames-*")
	if err != nil {
		warnings = append(warnings, fmt.Errorf("create temp frames dir: %w", err))
		thumbnails, err = generateThumbnailsFFmpeg(ctx, inputPath, outputDir, thumbCfg)
		return thumbnails, nil, warnings, err
	}
	defer os.RemoveAll(framesDir)

	frameFiles, err := extractThumbnailSourceFramesFFmpeg(ctx, inputPath, framesDir, thumbCfg.Interval)
	if err != nil {
		warnings = append(warnings, fmt.Errorf("extract thumbnail source frames: %w", err))
		thumbnails, err = generateThumbnailsFFmpeg(ctx, inputPath, outputDir, thumbCfg)
		return thumbnails, nil, warnings, err
	}

	var framePaths []string
	for _, name := range frameFiles {
		framePaths = append(framePaths, filepath.Join(framesDir, name))
	}

	summary, faceErr := extractAndSaveUniqueFacesFromImages(ctx, framePaths, outputDir, *faceCfg)
	faceSummary = &summary
	if faceErr != nil {
		warnings = append(warnings, fmt.Errorf("extract faces: %w", faceErr))
	}

	startNumber := guessFrameSequenceStart(frameFiles)
	thumbnails, err = generateThumbnailsFromFramesFFmpeg(ctx, framesDir, outputDir, thumbCfg, startNumber)
	if err != nil {
		warnings = append(warnings, fmt.Errorf("generate thumbnails from extracted frames: %w", err))
		thumbnails, err = generateThumbnailsFFmpeg(ctx, inputPath, outputDir, thumbCfg)
		return thumbnails, faceSummary, warnings, err
	}

	return thumbnails, faceSummary, warnings, nil
}

func thumbnailConfigFromConfig(cfg *Config) (thumbnailConfig, error) {
	if cfg == nil {
		return thumbnailConfig{}, fmt.Errorf("config is nil")
	}

	intervalSecs := cfg.ThumbnailIntervalSecs
	if intervalSecs <= 0 {
		return thumbnailConfig{}, fmt.Errorf("THUMBNAIL_INTERVAL_SECS must be > 0")
	}
	if cfg.ThumbnailWidth <= 0 {
		return thumbnailConfig{}, fmt.Errorf("THUMBNAIL_WIDTH must be > 0")
	}
	if cfg.ThumbnailHeight <= 0 {
		return thumbnailConfig{}, fmt.Errorf("THUMBNAIL_HEIGHT must be > 0")
	}

	ext := strings.ToLower(strings.TrimSpace(cfg.ThumbnailFormat))
	switch ext {
	case "", "jpg", "jpeg":
		ext = "jpg"
	case "png", "webp":
	default:
		return thumbnailConfig{}, fmt.Errorf("unsupported THUMBNAIL_FORMAT %q (supported: jpg, png, webp)", cfg.ThumbnailFormat)
	}

	return thumbnailConfig{
		Interval: time.Duration(intervalSecs) * time.Second,
		Width:    cfg.ThumbnailWidth,
		Height:   cfg.ThumbnailHeight,
		Ext:      ext,
	}, nil
}

func generateThumbnailsFFmpeg(ctx context.Context, inputPath, outputDir string, cfg thumbnailConfig) ([]string, error) {
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("thumbnail interval must be > 0")
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	fpsExpr := "1/" + strconv.FormatFloat(cfg.Interval.Seconds(), 'f', -1, 64)
	filter := fmt.Sprintf("fps=%s,scale=%d:%d", fpsExpr, cfg.Width, cfg.Height)

	outputPattern := filepath.Join(outputDir, fmt.Sprintf("thumb%%05d.%s", cfg.Ext))

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-i", inputPath,
		"-an",
		"-vf", filter,
	}
	if cfg.Ext == "jpg" {
		args = append(args, "-q:v", "2")
	}
	args = append(args, outputPattern)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg thumbnail extraction failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	files, err := filepath.Glob(filepath.Join(outputDir, fmt.Sprintf("thumb*.%s", cfg.Ext)))
	if err != nil {
		return nil, fmt.Errorf("glob thumbnails: %w", err)
	}
	sort.Strings(files)

	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no thumbnails")
	}
	return names, nil
}

func guessFrameSequenceStart(frameFiles []string) int {
	min := -1
	for _, name := range frameFiles {
		if n, ok := parseFrameSequenceNumber(name); ok {
			if min == -1 || n < min {
				min = n
			}
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

func parseFrameSequenceNumber(name string) (int, bool) {
	if !strings.HasPrefix(name, "frame") || !strings.HasSuffix(name, ".png") {
		return 0, false
	}
	numStr := strings.TrimSuffix(strings.TrimPrefix(name, "frame"), ".png")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, false
	}
	return n, true
}

func extractThumbnailSourceFramesFFmpeg(ctx context.Context, inputPath, outputDir string, interval time.Duration) ([]string, error) {
	if interval <= 0 {
		return nil, fmt.Errorf("thumbnail interval must be > 0")
	}

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	fpsExpr := "1/" + strconv.FormatFloat(interval.Seconds(), 'f', -1, 64)
	filter := fmt.Sprintf("fps=%s", fpsExpr)

	outputPattern := filepath.Join(outputDir, "frame%05d.png")
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-i", inputPath,
		"-an",
		"-vf", filter,
		"-start_number", "0",
		outputPattern,
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg frame extraction failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	files, err := filepath.Glob(filepath.Join(outputDir, "frame*.png"))
	if err != nil {
		return nil, fmt.Errorf("glob frames: %w", err)
	}
	sort.Strings(files)

	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no frames")
	}
	return names, nil
}

func generateThumbnailsFromFramesFFmpeg(ctx context.Context, framesDir, outputDir string, cfg thumbnailConfig, startNumber int) ([]string, error) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}

	inputPattern := filepath.Join(framesDir, "frame%05d.png")
	filter := fmt.Sprintf("scale=%d:%d", cfg.Width, cfg.Height)
	outputPattern := filepath.Join(outputDir, fmt.Sprintf("thumb%%05d.%s", cfg.Ext))

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-nostdin",
		"-y",
		"-start_number", strconv.Itoa(startNumber),
		"-i", inputPattern,
		"-vf", filter,
	}
	if cfg.Ext == "jpg" {
		args = append(args, "-q:v", "2")
	}
	args = append(args, outputPattern)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg thumbnail scaling failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	files, err := filepath.Glob(filepath.Join(outputDir, fmt.Sprintf("thumb*.%s", cfg.Ext)))
	if err != nil {
		return nil, fmt.Errorf("glob thumbnails: %w", err)
	}
	sort.Strings(files)

	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("ffmpeg produced no thumbnails from frames")
	}
	return names, nil
}

func writeThumbnailPlaylist(playlistPath string, thumbnails []string, interval time.Duration) error {
	if len(thumbnails) == 0 {
		return fmt.Errorf("no thumbnails to write")
	}
	if interval <= 0 {
		return fmt.Errorf("thumbnail interval must be > 0")
	}

	targetDuration := int(math.Ceil(interval.Seconds()))
	if targetDuration < 1 {
		targetDuration = 1
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", targetDuration))
	b.WriteString("#EXT-X-IMAGES-ONLY\n")
	b.WriteString("\n")

	for _, name := range thumbnails {
		b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", interval.Seconds()))
		b.WriteString(name + "\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")

	if err := os.WriteFile(playlistPath, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("write thumbnails playlist: %w", err)
	}
	return nil
}
