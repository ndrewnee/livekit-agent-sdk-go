package main

import (
	"context"
	"fmt"
	"io/fs"
	"mime"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// uploadRecordingToS3 uploads all files from the recording directory to S3-compatible storage.
//
// This function:
//  1. Creates a MinIO client with the provided credentials
//  2. Ensures the target bucket exists (creates if missing)
//  3. Walks the recording directory recursively
//  4. Uploads each file with appropriate Content-Type headers:
//     - .m3u8 files: application/vnd.apple.mpegurl
//     - .ts files: video/MP2T
//     - Other files: detected via MIME type or application/octet-stream
//  5. Preserves directory structure under s3://{bucket}/{prefix}/{room}/{participant}/
//  6. Skips output.ts as it's redundant (HLS segments contain all data)
//
// Parameters:
//   - ctx: Context for cancellation and timeout (2-minute overall timeout applied)
//   - cfg: S3 configuration including endpoint, bucket, credentials, and options
//   - room: LiveKit room name (used in S3 path)
//   - participant: Participant identity (used in S3 path)
//   - dir: Local directory containing recording files (playlist.m3u8, segments)
//
// Returns:
//   - string: S3 base URL (s3://{bucket}/{prefix}/{room}/{participant}) if upload succeeds
//   - error: Upload error, or nil if cfg.Enabled() is false
//
// The function respects cfg.ACL to set object ACLs (e.g., "public-read").
// Each individual file upload has a 30-second timeout.
func uploadRecordingToS3(ctx context.Context, cfg S3Config, room, participant, dir string) (string, error) {
	if !cfg.Enabled() {
		return "", nil
	}

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
		return "", fmt.Errorf("create minio client: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return "", fmt.Errorf("check bucket: %w", err)
	}
	if !exists {
		if err = client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return "", fmt.Errorf("create bucket: %w", err)
		}
	}

	prefixParts := []string{}
	if trimmed := strings.Trim(cfg.Prefix, "/"); trimmed != "" {
		prefixParts = append(prefixParts, trimmed)
	}
	prefixParts = append(prefixParts, room, participant)
	basePrefix := path.Join(prefixParts...)

	err = filepath.WalkDir(dir, func(fullPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		// Skip output.ts - it's redundant when we have HLS segments
		if d.Name() == "output.ts" {
			return nil
		}

		rel, relErr := filepath.Rel(dir, fullPath)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		objectName := path.Join(basePrefix, rel)

		ext := strings.ToLower(filepath.Ext(fullPath))
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			switch ext {
			case ".m3u8":
				contentType = "application/vnd.apple.mpegurl"
			case ".ts":
				contentType = "video/MP2T"
			default:
				contentType = "application/octet-stream"
			}
		}

		uploadCtx, cancelUpload := context.WithTimeout(ctx, 30*time.Second)
		defer cancelUpload()

		opts := minio.PutObjectOptions{
			ContentType: contentType,
		}
		if cfg.ACL != "" {
			opts.UserMetadata = map[string]string{
				"x-amz-acl": cfg.ACL,
			}
		}

		_, err = client.FPutObject(uploadCtx, cfg.Bucket, objectName, fullPath, opts)
		if err != nil {
			return fmt.Errorf("upload %s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("s3://%s/%s", cfg.Bucket, basePrefix), nil
}
