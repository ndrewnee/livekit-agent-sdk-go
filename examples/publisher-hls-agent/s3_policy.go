package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
)

func ensurePublicReadBucketPolicy(ctx context.Context, client *minio.Client, bucket, prefix string) error {
	if client == nil {
		return fmt.Errorf("minio client is nil")
	}
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return fmt.Errorf("bucket is empty")
	}
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return fmt.Errorf("prefix is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	resource := fmt.Sprintf("arn:aws:s3:::%s/%s/*", bucket, prefix)

	// Fast-path: if policy already mentions the resource, don't rewrite.
	existing, err := client.GetBucketPolicy(ctx, bucket)
	if err != nil {
		return fmt.Errorf("get bucket policy: %w", err)
	}
	if existing != "" && strings.Contains(existing, resource) {
		return nil
	}

	var doc map[string]any
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &doc); err != nil {
			return fmt.Errorf("parse existing bucket policy: %w", err)
		}
	} else {
		doc = map[string]any{
			"Version": "2012-10-17",
		}
	}

	stmt, _ := doc["Statement"].([]any)
	stmt = append(stmt, map[string]any{
		"Sid":    "publisher-hls-public-read",
		"Effect": "Allow",
		"Principal": map[string]any{
			"AWS": []string{"*"},
		},
		"Action":   []string{"s3:GetObject"},
		"Resource": []string{resource},
	})
	doc["Statement"] = stmt

	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal bucket policy: %w", err)
	}
	if err := client.SetBucketPolicy(ctx, bucket, string(data)); err != nil {
		return fmt.Errorf("set bucket policy: %w", err)
	}
	return nil
}
