package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// RealtimeS3Uploader manages real-time S3 upload of HLS segments with automatic cleanup.
//
// This uploader monitors the HLS playlist file and uploads segments immediately after
// they're written by hlssink. By watching playlist updates (rather than file creation),
// we ensure segments are fully written before uploading, avoiding interference.
//
// The uploader:
//   - Polls playlist.m3u8 for updates every 500ms
//   - Uploads newly referenced segments immediately
//   - Deletes local segments after successful upload to minimize storage
//   - Keeps playlist.m3u8 until recording completes (uploaded in final sweep)
type RealtimeS3Uploader struct {
	cfg         S3Config
	room        string
	participant string
	watchDir    string

	client        *minio.Client
	uploadedMu    sync.Mutex
	uploadedFiles map[string]struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	lastPlaylistContent string
}

// NewRealtimeS3Uploader creates a new S3 uploader with automatic file cleanup.
//
// Parameters:
//   - cfg: S3 configuration (must have cfg.Enabled() == true)
//   - room: Room name for S3 path construction
//   - participant: Participant identity for S3 path construction
//   - watchDir: Local directory containing HLS files
//
// Returns an error if S3 client creation fails.
func NewRealtimeS3Uploader(cfg S3Config, room, participant, watchDir string) (*RealtimeS3Uploader, error) {
	if !cfg.Enabled() {
		return nil, fmt.Errorf("s3 configuration not enabled")
	}

	// Create MinIO client (works with any S3-compatible storage)
	creds := credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken)
	opts := &minio.Options{
		Creds:  creds,
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	}
	if cfg.ForcePathStyle {
		opts.BucketLookup = minio.BucketLookupPath
	}

	client, err := minio.New(cfg.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	uploader := &RealtimeS3Uploader{
		cfg:           cfg,
		room:          room,
		participant:   participant,
		watchDir:      watchDir,
		client:        client,
		uploadedFiles: make(map[string]struct{}),
		ctx:           ctx,
		cancel:        cancel,
	}

	// Start monitoring playlist for new segments
	uploader.wg.Add(1)
	go uploader.monitorPlaylist()

	log.Printf("[%s/%s] S3 real-time upload enabled (monitoring playlist): s3://%s/%s/%s/%s",
		room, participant, cfg.Bucket, cfg.Prefix, room, participant)

	return uploader, nil
}

// monitorPlaylist polls the HLS playlist and uploads newly added segments in real-time.
func (u *RealtimeS3Uploader) monitorPlaylist() {
	defer u.wg.Done()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	playlistPath := filepath.Join(u.watchDir, "playlist.m3u8")

	for {
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
			u.checkPlaylistUpdates(playlistPath)
		}
	}
}

// checkPlaylistUpdates reads the playlist and uploads any new segments.
func (u *RealtimeS3Uploader) checkPlaylistUpdates(playlistPath string) {
	// Read playlist content
	content, err := os.ReadFile(playlistPath)
	if err != nil {
		// Playlist might not exist yet at the start of recording
		if !os.IsNotExist(err) {
			log.Printf("[%s/%s] error reading playlist: %v", u.room, u.participant, err)
		}
		return
	}

	contentStr := string(content)

	// Skip if playlist hasn't changed
	if contentStr == u.lastPlaylistContent {
		return
	}

	// Parse playlist to find segment files
	newSegments := u.parseSegments(contentStr)

	// Upload new segments asynchronously
	for _, segment := range newSegments {
		u.uploadedMu.Lock()
		_, already := u.uploadedFiles[segment]
		u.uploadedMu.Unlock()

		if !already {
			// Upload and delete segment file asynchronously
			u.wg.Add(1)
			go u.uploadAndDeleteSegment(segment)
		}
	}

	u.lastPlaylistContent = contentStr
}

// parseSegments extracts segment filenames from the playlist content.
func (u *RealtimeS3Uploader) parseSegments(content string) []string {
	var segments []string
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip comments, empty lines, and playlist directives
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// This is a segment filename (e.g., "segment00000.ts")
		if strings.HasSuffix(line, ".ts") {
			segments = append(segments, line)
		}
	}

	return segments
}

// uploadAndDeleteSegment uploads a segment to S3 and deletes the local file.
func (u *RealtimeS3Uploader) uploadAndDeleteSegment(segmentName string) {
	defer u.wg.Done()

	segmentPath := filepath.Join(u.watchDir, segmentName)

	// Upload to S3
	if err := u.uploadFile(segmentPath, segmentName); err != nil {
		log.Printf("[%s/%s] failed to upload %s: %v", u.room, u.participant, segmentName, err)
		return
	}

	// Mark as uploaded
	u.uploadedMu.Lock()
	u.uploadedFiles[segmentName] = struct{}{}
	u.uploadedMu.Unlock()

	// Delete local file to save storage
	if err := os.Remove(segmentPath); err != nil && !os.IsNotExist(err) {
		log.Printf("[%s/%s] warning: failed to delete %s: %v", u.room, u.participant, segmentName, err)
	} else {
		log.Printf("[%s/%s] uploaded and deleted: %s", u.room, u.participant, segmentName)
	}
}

// uploadFile uploads a single file to S3.
func (u *RealtimeS3Uploader) uploadFile(localPath, fileName string) error {
	// Construct S3 object key
	s3Key := u.getS3Key(fileName)

	// Determine content type
	contentType := "application/octet-stream"
	if strings.HasSuffix(fileName, ".m3u8") {
		contentType = "application/vnd.apple.mpegurl"
	} else if strings.HasSuffix(fileName, ".ts") {
		contentType = "video/MP2T"
	}

	// Upload with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opts := minio.PutObjectOptions{
		ContentType: contentType,
	}
	if u.cfg.ACL != "" {
		opts.UserMetadata = map[string]string{
			"x-amz-acl": u.cfg.ACL,
		}
	}

	_, err := u.client.FPutObject(ctx, u.cfg.Bucket, s3Key, localPath, opts)
	if err != nil {
		return fmt.Errorf("upload to s3://%s/%s: %w", u.cfg.Bucket, s3Key, err)
	}

	return nil
}

// getS3Key constructs the S3 object key for a file.
// Format: prefix/room/participant/filename
func (u *RealtimeS3Uploader) getS3Key(fileName string) string {
	parts := []string{}
	if u.cfg.Prefix != "" {
		parts = append(parts, strings.Trim(u.cfg.Prefix, "/"))
	}
	parts = append(parts, u.room, u.participant, fileName)
	return strings.Join(parts, "/")
}

// GetS3URL returns the S3 URL for the uploaded recordings.
func (u *RealtimeS3Uploader) GetS3URL() string {
	parts := []string{}
	if u.cfg.Prefix != "" {
		parts = append(parts, strings.Trim(u.cfg.Prefix, "/"))
	}
	parts = append(parts, u.room, u.participant)
	path := strings.Join(parts, "/")
	return fmt.Sprintf("s3://%s/%s", u.cfg.Bucket, path)
}

// Close stops monitoring, uploads remaining files, and cleans up.
func (u *RealtimeS3Uploader) Close() error {
	// Stop the monitoring goroutine
	u.cancel()
	u.wg.Wait()

	// Upload any remaining files (playlist.m3u8 and any missed segments)
	// Note: output.ts is intentionally skipped as it's redundant
	if err := u.finalUploadSweep(); err != nil {
		log.Printf("[%s/%s] final sweep error: %v", u.room, u.participant, err)
		return err
	}

	log.Printf("[%s/%s] S3 upload complete: uploaded %d files to %s",
		u.room, u.participant, len(u.uploadedFiles), u.GetS3URL())

	return nil
}

// finalUploadSweep uploads all HLS files to S3 and deletes them from local storage.
// This is called after recording completes to minimize local storage costs.
// Note: output.ts is NOT uploaded as it's redundant (HLS segments contain all data).
func (u *RealtimeS3Uploader) finalUploadSweep() error {
	entries, err := filepath.Glob(filepath.Join(u.watchDir, "*"))
	if err != nil {
		return fmt.Errorf("glob directory: %w", err)
	}

	for _, entry := range entries {
		fileName := filepath.Base(entry)

		// Skip output.ts - it's redundant when we have HLS segments
		if fileName == "output.ts" {
			log.Printf("[%s/%s] skipping output.ts (redundant)", u.room, u.participant)
			continue
		}

		// Only upload HLS files (playlist and segments)
		if !strings.HasSuffix(fileName, ".ts") && !strings.HasSuffix(fileName, ".m3u8") {
			continue
		}

		// Upload file to S3
		if err := u.uploadFile(entry, fileName); err != nil {
			log.Printf("[%s/%s] failed to upload %s: %v", u.room, u.participant, fileName, err)
		} else {
			u.uploadedMu.Lock()
			u.uploadedFiles[fileName] = struct{}{}
			u.uploadedMu.Unlock()

			// Delete local file after successful upload to minimize disk usage
			if err := os.Remove(entry); err != nil {
				log.Printf("[%s/%s] warning: failed to delete %s: %v", u.room, u.participant, fileName, err)
			}
		}
	}

	return nil
}
