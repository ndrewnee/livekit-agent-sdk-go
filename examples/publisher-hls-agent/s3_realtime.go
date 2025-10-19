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
//
// This goroutine runs continuously until the uploader is closed, checking for
// playlist updates every 500ms. By monitoring the playlist file rather than
// individual segment files, we ensure segments are fully written before upload.
//
// Lifecycle:
//  1. Started automatically in NewRealtimeS3Uploader
//  2. Runs until ctx is cancelled via Close()
//  3. Signals completion via wg.Done()
//
// The 500ms polling interval balances responsiveness with resource usage:
//   - Fast enough to upload segments promptly (HLS target duration is ~2s)
//   - Slow enough to avoid excessive file I/O and CPU usage
//
// Thread-safety: This goroutine is the only reader of playlist.m3u8,
// while hlssink is the only writer, avoiding read/write conflicts.
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
//
// This function is called periodically by monitorPlaylist. It:
//  1. Reads the current playlist.m3u8 content
//  2. Compares it to the previously seen content (cached in lastPlaylistContent)
//  3. If changed, parses segment filenames from the playlist
//  4. Uploads any segments not yet uploaded (tracked in uploadedFiles map)
//
// Design decisions:
//   - String comparison of entire playlist: Simple and correct, since hlssink
//     rewrites the entire file on each update. No need for incremental parsing.
//   - Async upload: Each segment is uploaded in a separate goroutine to avoid
//     blocking the monitoring loop.
//   - Deduplication: uploadedFiles map prevents duplicate uploads if hlssink
//     keeps a segment in the playlist across multiple updates.
//
// Error handling:
//   - File not found: Ignored (playlist doesn't exist at start of recording)
//   - Other read errors: Logged but not fatal (retry on next poll)
//
// Thread-safety: uploadedFiles map is protected by uploadedMu.
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
//
// HLS playlist format (M3U8):
//
//	#EXTM3U
//	#EXT-X-VERSION:3
//	#EXT-X-TARGETDURATION:2
//	#EXTINF:2.000000,
//	segment00000.ts
//	#EXTINF:2.000000,
//	segment00001.ts
//	...
//
// The function identifies segment lines by these criteria:
//   - Non-empty lines
//   - Not starting with # (comments/directives)
//   - Ending with .ts extension
//
// This simple parsing is sufficient for HLS playlists generated by hlssink.
// More complex HLS features (e.g., variant playlists, byte ranges) are not
// used in this recording scenario.
//
// Parameters:
//   - content: Full text content of playlist.m3u8
//
// Returns:
//   - List of segment filenames (e.g., ["segment00000.ts", "segment00001.ts"])
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
//
// This function runs in a separate goroutine for each segment to avoid blocking
// the monitoring loop. It performs three steps:
//  1. Upload segment to S3
//  2. Mark segment as uploaded in the uploadedFiles map
//  3. Delete local file to minimize disk usage
//
// The deletion is critical for long recordings: without it, local storage would
// fill up as segments accumulate. By deleting immediately after upload, we only
// keep segments temporarily while they're being written and uploaded.
//
// Error handling:
//   - Upload failure: Logged, segment kept locally for potential retry in finalUploadSweep
//   - Delete failure: Logged as warning (file may not exist if already deleted)
//
// Thread-safety:
//   - Called as a goroutine, multiple segments can be uploaded concurrently
//   - uploadedFiles map access is protected by uploadedMu
//   - Each segment operates on its own file, no conflicts
//
// Parameters:
//   - segmentName: Filename only (e.g., "segment00000.ts"), not full path
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

// uploadFile uploads a single file to S3 with proper content type and metadata.
//
// The function handles all file types used in HLS recordings:
//   - .m3u8 files: "application/vnd.apple.mpegurl" (HLS playlist MIME type)
//   - .ts files: "video/MP2T" (MPEG-TS container MIME type)
//   - Other files: "application/octet-stream" (generic binary)
//
// S3 configuration:
//   - Timeout: 30 seconds per file (sufficient for typical segments <2MB)
//   - ACL: Configured via S3Config.ACL (e.g., "public-read" for public access)
//   - Bucket and region: From S3Config
//
// MinIO compatibility:
// This code uses the MinIO Go client which is fully compatible with:
//   - Amazon S3
//   - MinIO Server
//   - Any S3-compatible storage (DigitalOcean Spaces, Backblaze B2, etc.)
//
// Parameters:
//   - localPath: Absolute path to the local file
//   - fileName: Filename only, used to construct S3 key and determine content type
//
// Returns:
//   - nil on success
//   - error if upload fails (network error, auth error, timeout, etc.)
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

// GetS3URL returns the S3 URL for the uploaded recordings directory.
//
// The URL format is: s3://bucket/prefix/room/participant
//
// This URL points to the directory containing all files for this participant:
//   - playlist.m3u8 (HLS playlist)
//   - segment00000.ts, segment00001.ts, ... (HLS segments)
//
// To access the HLS stream, append /playlist.m3u8 to this URL:
//
//	s3://bucket/prefix/room/participant/playlist.m3u8
//
// Returns:
//   - S3 URL string (not an HTTP URL, use GetHTTPURL if you need browser-accessible URL)
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
//
// This function performs a graceful shutdown of the uploader:
//  1. Cancel the monitoring context to stop monitorPlaylist goroutine
//  2. Wait for all in-flight uploads to complete (via wg.Wait())
//  3. Perform final sweep to upload any remaining files
//  4. Log upload statistics
//
// The final sweep is necessary because:
//   - playlist.m3u8 is not uploaded during monitoring (only at the end)
//   - Some segments might have been created just before monitoring stopped
//   - Ensures all files are uploaded before returning
//
// After Close() returns:
//   - All goroutines have exited
//   - All files have been uploaded (or errors logged)
//   - Local directory has been cleaned up (files deleted after upload)
//
// Returns:
//   - nil on success
//   - error if final sweep fails (individual upload errors are logged but not returned)
//
// This function should be called after recording stops, typically in a defer statement
// to ensure cleanup happens even if recording encounters an error.
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
