//go:build gocv

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/pion/webrtc/v4"
)

func TestPublisherHLSAgentUploadsAV1ToS3_WithFaces(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end integration test in short mode")
	}

	ms := startMinIOServer(t)
	if !ms.KeepAlive {
		defer ms.Shutdown(t)
	} else {
		t.Logf("PUBLISHER_HLS_KEEP_MINIO=1 detected; MinIO will remain running at http://%s", ms.Endpoint)
	}

	repoRoot := findRepoRoot(t)
	testVideo := filepath.Join(repoRoot, "examples", "publisher-hls-agent", "test", "test_faces.mp4")
	if _, err := os.Stat(testVideo); err != nil {
		t.Skipf("missing test video %s: %v", testVideo, err)
	}

	uniqueAgentName := fmt.Sprintf("publisher-hls-av1-faces-s3-agent-%d", time.Now().UnixNano())
	uniqueRoomName := fmt.Sprintf("publisher-hls-av1-faces-s3-room-%d", time.Now().UnixNano())
	uniqueParticipant := fmt.Sprintf("publisher-hls-av1-faces-s3-participant-%d", time.Now().UnixNano())

	e2eePassphrase := "test-e2ee-secret-123"

	scenario := e2eScenario{
		name:             "av1-s3-upload-faces",
		agentName:        uniqueAgentName,
		roomName:         uniqueRoomName,
		participant:      uniqueParticipant,
		testVideo:        testVideo,
		videoCodec:       webrtc.MimeTypeAV1,
		skipS3Validation: true, // Custom validation for AV1/CMAF
		e2eePassphrase:   e2eePassphrase,
		agentEnv: map[string]string{
			"S3_ENDPOINT":             ms.Endpoint,
			"S3_BUCKET":               ms.Bucket,
			"S3_REGION":               "us-east-1",
			"S3_ACCESS_KEY":           ms.AccessKey,
			"S3_SECRET_KEY":           ms.SecretKey,
			"S3_FORCE_PATH_STYLE":     "true",
			"S3_USE_SSL":              "false",
			"S3_PREFIX":               "publisher-av1-faces-tests",
			"S3_OBJECT_ACL":           "public-read",
			"AUTO_ACTIVATE_RECORDING": "true",
			"E2EE_PASSPHRASE":         e2eePassphrase,
			"THUMBNAILS_ENABLED":      "true",
			"THUMBNAIL_INTERVAL_SECS": "5",
			"THUMBNAIL_WIDTH":         "640",
			"THUMBNAIL_HEIGHT":        "320",
			"THUMBNAIL_FORMAT":        "jpg",
			"FACES_ENABLED":           "true",
			"FACE_FORMAT":             "jpg",
			"FACE_NORMALIZED_WIDTH":   "160",
			"FACE_NORMALIZED_HEIGHT":  "160",
			"FACE_MAX_UNIQUE":         "50",
		},
	}

	_ = runE2EScenario(t, scenario)

	client := ms.NewClient(t)
	prefix := path.Join(strings.Trim(scenario.agentEnv["S3_PREFIX"], "/"), scenario.roomName, scenario.participant)

	if err := validateS3AV1RecordingExact(t, client, ms.Bucket, prefix, testVideo); err != nil {
		t.Fatalf("AV1 S3 validation failed: %v", err)
	}

	if err := validateS3Thumbnails(t, client, ms.Bucket, prefix, 640, 320); err != nil {
		t.Fatalf("thumbnail validation failed: %v", err)
	}

	if err := validateS3Faces(t, client, ms.Bucket, prefix, 160, 160); err != nil {
		t.Fatalf("face validation failed: %v", err)
	}
}

func validateS3Faces(t *testing.T, client *minio.Client, bucket, prefix string, expectedWidth, expectedHeight int) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	facesPrefix := path.Join(prefix, facesDirName) + "/"
	groupsKey := path.Join(prefix, facesDirName, facesGroupsManifestName)
	var keys []string
	for obj := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: facesPrefix, Recursive: true}) {
		if obj.Err != nil {
			return fmt.Errorf("list faces in S3 (%s): %w", facesPrefix, obj.Err)
		}
		if obj.Key == "" || obj.Size == 0 {
			continue
		}
		if !strings.HasPrefix(obj.Key, facesPrefix) {
			continue
		}
		rel := strings.TrimPrefix(obj.Key, facesPrefix)
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(rel))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp":
			keys = append(keys, obj.Key)
		}
	}

	if len(keys) == 0 {
		return fmt.Errorf("no extracted faces uploaded under s3://%s/%s", bucket, facesPrefix)
	}

	// groups.json must exist and reference uploaded face images.
	if _, err := client.StatObject(ctx, bucket, groupsKey, minio.StatObjectOptions{}); err != nil {
		return fmt.Errorf("missing face groups manifest s3://%s/%s: %w", bucket, groupsKey, err)
	}

	tempDir := t.TempDir()
	localGroupsPath := filepath.Join(tempDir, "groups.json")
	if err := client.FGetObject(ctx, bucket, groupsKey, localGroupsPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download face groups manifest %s: %w", groupsKey, err)
	}
	data, err := os.ReadFile(localGroupsPath)
	if err != nil {
		return fmt.Errorf("read face groups manifest: %w", err)
	}

	var manifest facesGroupsManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse face groups manifest: %w", err)
	}
	if len(manifest.Groups) == 0 {
		return fmt.Errorf("face groups manifest has no groups")
	}

	// Verify each referenced face file exists in S3.
	for _, g := range manifest.Groups {
		for _, rel := range g.Faces {
			if rel == "" {
				continue
			}
			objKey := path.Join(prefix, rel)
			if _, err := client.StatObject(ctx, bucket, objKey, minio.StatObjectOptions{}); err != nil {
				return fmt.Errorf("face groups manifest references missing object s3://%s/%s: %w", bucket, objKey, err)
			}
		}
	}

	// Download and decode one face to validate image integrity and normalization size.
	firstKey := keys[0]
	localPath := filepath.Join(tempDir, filepath.Base(firstKey))
	if err := client.FGetObject(ctx, bucket, firstKey, localPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("download face %s: %w", firstKey, err)
	}

	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open face %s: %w", localPath, err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return fmt.Errorf("decode face %s: %w", firstKey, err)
	}

	gotW := img.Bounds().Dx()
	gotH := img.Bounds().Dy()
	if gotW != expectedWidth || gotH != expectedHeight {
		return fmt.Errorf("unexpected face resolution %dx%d, expected %dx%d", gotW, gotH, expectedWidth, expectedHeight)
	}

	t.Logf("validated %d extracted face images under s3://%s/%s (sample=%s)", len(keys), bucket, facesPrefix, firstKey)
	return nil
}
