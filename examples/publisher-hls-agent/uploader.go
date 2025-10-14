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
