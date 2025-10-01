// +build e2e

package egress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/go-gst/go-gst/gst"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2EPipelineHLSGeneration tests HLS generation with real media data
func TestE2EPipelineHLSGeneration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Initialize GStreamer
	gst.Init(nil)

	// Create output directory
	outputDir := t.TempDir()
	sessionID := fmt.Sprintf("e2e-session-%d", time.Now().Unix())

	t.Logf("Output directory: %s", outputDir)
	t.Logf("Session ID: %s", sessionID)

	// Create pipeline configuration
	config := &pipeline.Config{
		OutputDir:          outputDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	// Create pipeline
	p, err := pipeline.NewDirectPipeline(config, sessionID)
	require.NoError(t, err, "Failed to create pipeline")
	defer p.Stop()

	// Start pipeline
	err = p.Start()
	require.NoError(t, err, "Failed to start pipeline")

	// Wait for pipeline to be ready
	time.Sleep(1 * time.Second)

	// Load real H.264 NAL units from test data
	nalUnits, err := ReadMP4NALUnits("../../examples/egress-agent/test-data/test-video-h264.mp4", 100)
	require.NoError(t, err, "Failed to read NAL units")
	require.NotEmpty(t, nalUnits, "No NAL units extracted")

	t.Logf("Loaded %d NAL units from test data", len(nalUnits))

	// Inject video and audio packets from REAL media files
	t.Run("inject_media_packets", func(t *testing.T) {
		// Create RTP packets from REAL NAL units extracted from file
		videoPackets := CreateRTPPacketsFromNALUnits(nalUnits, 12345)
		require.Greater(t, len(videoPackets), 0, "Must have video packets from real file")

		// Extract REAL audio frames
		audioFrames, err := ExtractOpusFrames("../../examples/egress-agent/test-data/test-audio-opus.ogg")
		require.NoError(t, err, "Must extract real Opus frames")
		require.Greater(t, len(audioFrames), 0, "Must have audio frames from real file")

		t.Logf("Injecting %d video packets and %d audio frames from real media files", len(videoPackets), len(audioFrames))

		// Send packets - must be successful for REAL test
		videoSuccessCount := 0
		audioSuccessCount := 0
		packetCount := len(videoPackets)
		if len(audioFrames) < packetCount {
			packetCount = len(audioFrames)
		}

		// Inject both video and audio packets together
		// Pipeline requires BOTH to work correctly - mpegtsmux needs both streams
		for i := 0; i < len(videoPackets); i++ {
			// Inject real video packet
			err := p.InjectVideoRTP(videoPackets[i])
			if err == nil {
				videoSuccessCount++
			} else {
				t.Logf("Warning: Failed to inject video packet %d: %v", i, err)
			}

			// Inject audio packet (synchronized with video)
			if i < len(audioFrames) {
				// Create RTP packet for Opus frame
				audioPacket := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						PayloadType:    111, // Opus
						SequenceNumber: uint16(2000 + i),
						Timestamp:      uint32(i * 960),  // 20ms at 48kHz = 960 samples
						SSRC:           54321,
						Marker:         false,
					},
					Payload: audioFrames[i],
				}
				err = p.InjectAudioRTP(audioPacket)
				if err == nil {
					audioSuccessCount++
				} else {
					t.Logf("Warning: Failed to inject audio packet %d: %v", i, err)
				}
			}

			time.Sleep(20 * time.Millisecond)
		}

		t.Logf("Successfully injected %d/%d video packets and %d/%d audio packets",
			videoSuccessCount, len(videoPackets), audioSuccessCount, len(videoPackets))

		// STRICT REQUIREMENT - Must inject 100% of packets (no artificial packet loss)
		require.Equal(t, videoSuccessCount, len(videoPackets),
			"Must successfully inject ALL video packets (got %d/%d)", videoSuccessCount, len(videoPackets))
		require.Equal(t, audioSuccessCount, len(videoPackets),
			"Must successfully inject ALL audio packets (got %d/%d)", audioSuccessCount, len(videoPackets))
	})

	// Wait for HLS generation
	time.Sleep(3 * time.Second)

	// Verify HLS output - STRICT REQUIREMENTS, NO SKIPS!
	t.Run("verify_hls_output", func(t *testing.T) {
		hlsDir := filepath.Join(outputDir, sessionID)
		files, err := os.ReadDir(hlsDir)
		require.NoError(t, err, "HLS directory must be created - pipeline failed to output HLS")

		require.Greater(t, len(files), 0, "HLS directory is empty - no output generated")

		var playlistFound bool
		segmentCount := 0
		var playlistName string

		for _, file := range files {
			ext := filepath.Ext(file.Name())
			if ext == ".m3u8" {
				playlistFound = true
				playlistName = file.Name()
				t.Logf("Found playlist: %s", playlistName)
			} else if ext == ".ts" {
				segmentCount++
			}
		}

		// STRICT ASSERTIONS - NO EXCUSES
		require.True(t, playlistFound, "HLS playlist (.m3u8) must be generated")
		require.Greater(t, segmentCount, 0, "HLS segments (.ts) must be generated")

		// Verify we have reasonable number of segments
		// With 2-second segments and ~3 seconds of media, expect at least 1 segment
		require.GreaterOrEqual(t, segmentCount, 1, "Must have at least 1 HLS segment")

		t.Logf("Generated %d HLS segments", segmentCount)

		// Verify playlist structure
		playlistPath := filepath.Join(hlsDir, playlistName)
		data, err := os.ReadFile(playlistPath)
		require.NoError(t, err, "Must be able to read playlist file")

		playlist := string(data)
		require.Contains(t, playlist, "#EXTM3U", "Playlist must have HLS header")
		require.Contains(t, playlist, "#EXT-X-VERSION", "Playlist must have version tag")
		require.Contains(t, playlist, ".ts", "Playlist must reference segment files")

		t.Logf("✓ HLS output VERIFIED successfully:")
		t.Logf("  - Playlist: %s", playlistName)
		t.Logf("  - Segments: %d", segmentCount)
		t.Logf("  - Output dir: %s", hlsDir)
	})

	// Check statistics
	stats := p.GetStats()
	t.Logf("Pipeline stats: Video=%d, Audio=%d", stats.VideoPacketsReceived, stats.AudioPacketsReceived)
	assert.Greater(t, stats.VideoPacketsReceived, uint64(0), "Should receive video packets")
	assert.Greater(t, stats.AudioPacketsReceived, uint64(0), "Should receive audio packets")
}

// TestE2EMinIOUpload is removed - it was testing MinIO SDK functionality with fake HLS files,
// not the actual egress pipeline output. MinIO upload verification is covered by
// TestE2ECompletePipeline and TestE2ERealParticipantsToMinIO which use REAL HLS output.

// TestE2ECompletePipeline tests complete pipeline with MinIO upload
func TestE2ECompletePipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Part 1: Generate HLS
	gst.Init(nil)

	outputDir := t.TempDir()
	sessionID := fmt.Sprintf("complete-e2e-%d", time.Now().Unix())

	config := &pipeline.Config{
		OutputDir:          outputDir,
		SegmentDuration:    2,
		JitterBufferMs:     200,
		AudioMode:          pipeline.AudioPassThrough,
		StateChangeTimeout: 10 * time.Second,
		IsLiveSource:       true,
	}

	p, err := pipeline.NewDirectPipeline(config, sessionID)
	require.NoError(t, err)
	defer p.Stop()

	err = p.Start()
	require.NoError(t, err)

	// Inject test data - MUST send both video AND audio for valid HLS
	nalUnits, err := ReadMP4NALUnits("../../examples/egress-agent/test-data/test-video-h264.mp4", 100)
	require.NoError(t, err)

	// Extract REAL audio frames
	audioFrames, err := ExtractOpusFrames("../../examples/egress-agent/test-data/test-audio-opus.ogg")
	require.NoError(t, err, "Must extract real Opus frames")
	require.Greater(t, len(audioFrames), 0, "Must have audio frames from real file")

	videoPackets := CreateRTPPacketsFromNALUnits(nalUnits, 12345)
	videoSuccessCount := 0
	audioSuccessCount := 0

	// Send first 50 video packets WITH corresponding audio packets
	for i := 0; i < 50 && i < len(videoPackets); i++ {
		// Inject video packet
		if err := p.InjectVideoRTP(videoPackets[i]); err != nil {
			t.Logf("Warning: video packet %d failed: %v", i, err)
		} else {
			videoSuccessCount++
		}

		// Inject corresponding audio packet (synchronized with video)
		if i < len(audioFrames) {
			audioPacket := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    111, // Opus
					SequenceNumber: uint16(2000 + i),
					Timestamp:      uint32(i * 960), // 20ms at 48kHz = 960 samples
					SSRC:           54321,
					Marker:         false,
				},
				Payload: audioFrames[i],
			}
			if err := p.InjectAudioRTP(audioPacket); err != nil {
				t.Logf("Warning: audio packet %d failed: %v", i, err)
			} else {
				audioSuccessCount++
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Logf("Injected %d video and %d audio packets", videoSuccessCount, audioSuccessCount)

	// STRICT REQUIREMENT - Must inject 100% of packets (no artificial packet loss)
	require.Equal(t, videoSuccessCount, 50,
		"Must successfully inject ALL video packets (got %d/50)", videoSuccessCount)
	require.Equal(t, audioSuccessCount, 50,
		"Must successfully inject ALL audio packets (got %d/50)", audioSuccessCount)

	time.Sleep(3 * time.Second)

	// Stop pipeline to flush EOS and finalize playlist
	p.Stop()
	time.Sleep(500 * time.Millisecond) // Give time for pipeline to flush

	// Part 2: Upload to MinIO
	minioEndpoint := getEnvOrDefault("MINIO_ENDPOINT", "localhost:9000")
	minioAccessKey := getEnvOrDefault("MINIO_ACCESS_KEY", "minioadmin")
	minioSecretKey := getEnvOrDefault("MINIO_SECRET_KEY", "minioadmin")
	minioBucket := getEnvOrDefault("MINIO_BUCKET", "egress-test")

	ctx := context.Background()
	minioClient, err := minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccessKey, minioSecretKey, ""),
		Secure: false,
	})
	require.NoError(t, err)

	// Upload all generated files - STRICT VERIFICATION
	hlsDir := filepath.Join(outputDir, sessionID)
	files, err := os.ReadDir(hlsDir)
	require.NoError(t, err, "Must be able to read HLS directory")
	require.Greater(t, len(files), 0, "HLS directory must contain files")

	uploadCount := 0
	failedUploads := 0
	playlistUploaded := false
	segmentsUploaded := 0

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		localPath := filepath.Join(hlsDir, file.Name())
		remotePath := fmt.Sprintf("%s/%s", sessionID, file.Name())

		_, err := minioClient.FPutObject(ctx, minioBucket, remotePath, localPath, minio.PutObjectOptions{
			ContentType: getContentType(file.Name()),
		})
		if err != nil {
			t.Logf("Failed to upload %s: %v", file.Name(), err)
			failedUploads++
		} else {
			uploadCount++
			t.Logf("Uploaded: %s", file.Name())

			// Track what was uploaded
			if filepath.Ext(file.Name()) == ".m3u8" {
				playlistUploaded = true
			} else if filepath.Ext(file.Name()) == ".ts" {
				segmentsUploaded++
			}
		}
	}

	// STRICT ASSERTIONS - Must upload complete HLS output
	require.Greater(t, uploadCount, 0, "Must upload at least some files to MinIO")
	require.Equal(t, 0, failedUploads, "All uploads must succeed, but %d failed", failedUploads)
	require.True(t, playlistUploaded, "Must upload HLS playlist (.m3u8)")
	require.Greater(t, segmentsUploaded, 0, "Must upload at least one HLS segment (.ts)")

	// Verify files exist in MinIO
	listCtx := context.Background()
	objectCount := 0
	for obj := range minioClient.ListObjects(listCtx, minioBucket, minio.ListObjectsOptions{
		Prefix:    sessionID,
		Recursive: true,
	}) {
		require.NoError(t, obj.Err, "Error listing MinIO objects")
		objectCount++
	}

	require.Equal(t, uploadCount, objectCount, "MinIO should have exactly %d objects, found %d", uploadCount, objectCount)

	t.Logf("✓ Successfully uploaded and verified %d files to MinIO", uploadCount)
	t.Logf("  - Playlist: %v", playlistUploaded)
	t.Logf("  - Segments: %d", segmentsUploaded)
	t.Logf("✓ View at: http://%s/%s/%s/playlist.m3u8", minioEndpoint, minioBucket, sessionID)
}

// Helper functions

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getContentType(filename string) string {
	ext := filepath.Ext(filename)
	switch ext {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/MP2T"
	default:
		return "application/octet-stream"
	}
}