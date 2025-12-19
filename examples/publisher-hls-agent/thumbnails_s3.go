package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

var hlsMapURIRegex = regexp.MustCompile(`(?i)^#EXT-X-MAP:.*URI="([^"]+)"`)

func parseHLSPlaylistAssets(playlist string) (mapURI string, segments []string) {
	lines := strings.Split(playlist, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-MAP:") {
			if match := hlsMapURIRegex.FindStringSubmatch(trimmed); len(match) == 2 {
				mapURI = match[1]
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		segments = append(segments, trimmed)
	}
	return mapURI, segments
}

func generateAndUploadThumbnailsFromS3(u *RealtimeS3Uploader, cfg thumbnailConfig) error {
	if u == nil || u.client == nil {
		return fmt.Errorf("S3 uploader not initialized")
	}

	tempDir, err := os.MkdirTemp("", "publisher-hls-thumbnails-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	videoPlaylistObj := u.getS3Key("video.m3u8")
	localPlaylist := filepath.Join(tempDir, "video.m3u8")
	if err := u.client.FGetObject(ctx, u.cfg.Bucket, videoPlaylistObj, localPlaylist, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download video.m3u8 from s3://%s/%s: %w", u.cfg.Bucket, videoPlaylistObj, err)
	}

	playlistBytes, err := os.ReadFile(localPlaylist)
	if err != nil {
		return fmt.Errorf("read downloaded video.m3u8: %w", err)
	}

	mapURI, segments := parseHLSPlaylistAssets(string(playlistBytes))
	if mapURI != "" {
		dst := filepath.Join(tempDir, filepath.FromSlash(mapURI))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create mapURI dir: %w", err)
		}
		obj := u.getS3Key(mapURI)
		if err := u.client.FGetObject(ctx, u.cfg.Bucket, obj, dst, minio.GetObjectOptions{}); err != nil {
			return fmt.Errorf("download EXT-X-MAP %s from s3://%s/%s: %w", mapURI, u.cfg.Bucket, obj, err)
		}
	}

	for _, seg := range segments {
		dst := filepath.Join(tempDir, filepath.FromSlash(seg))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create segment dir: %w", err)
		}
		obj := u.getS3Key(seg)
		if err := u.client.FGetObject(ctx, u.cfg.Bucket, obj, dst, minio.GetObjectOptions{}); err != nil {
			return fmt.Errorf("download segment %s from s3://%s/%s: %w", seg, u.cfg.Bucket, obj, err)
		}
	}

	thumbnails, err := generateThumbnailsFFmpeg(ctx, localPlaylist, tempDir, cfg)
	if err != nil {
		return fmt.Errorf("generate thumbnails from S3-downloaded HLS: %w", err)
	}

	thumbPlaylistPath := filepath.Join(tempDir, "thumbnails.m3u8")
	if err := writeThumbnailPlaylist(thumbPlaylistPath, thumbnails, cfg.Interval); err != nil {
		return fmt.Errorf("write thumbnails.m3u8: %w", err)
	}

	if err := u.uploadFile(thumbPlaylistPath, "thumbnails.m3u8"); err != nil {
		return fmt.Errorf("upload thumbnails.m3u8: %w", err)
	}
	for _, name := range thumbnails {
		if err := u.uploadFile(filepath.Join(tempDir, name), name); err != nil {
			return fmt.Errorf("upload thumbnail %s: %w", name, err)
		}
	}

	return nil
}
