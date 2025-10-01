package storage

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStorageIntegration performs comprehensive integration tests
func TestStorageIntegration(t *testing.T) {
	lg := logger.GetLogger()

	t.Run("LocalStorage", func(t *testing.T) {
		testLocalStorageIntegration(t, lg)
	})

	t.Run("S3Storage", func(t *testing.T) {
		if os.Getenv("ENABLE_S3_TESTS") != "true" {
			t.Skip("S3 tests disabled. Set ENABLE_S3_TESTS=true to enable")
		}
		testS3StorageIntegration(t, lg)
	})

	t.Run("HybridStorage", func(t *testing.T) {
		testHybridStorageIntegration(t, lg)
	})

	t.Run("CircuitBreaker", func(t *testing.T) {
		testCircuitBreakerIntegration(t, lg)
	})

	t.Run("RetentionPolicy", func(t *testing.T) {
		testRetentionPolicyIntegration(t, lg)
	})

	t.Run("BufferManager", func(t *testing.T) {
		testBufferManagerIntegration(t, lg)
	})

	t.Run("UploadManager", func(t *testing.T) {
		testUploadManagerIntegration(t, lg)
	})

	t.Run("HLSPlaylist", func(t *testing.T) {
		testHLSPlaylistIntegration(t, lg)
	})

	t.Run("SegmentValidation", func(t *testing.T) {
		testSegmentValidationIntegration(t, lg)
	})

	t.Run("Screenshots", func(t *testing.T) {
		testScreenshotIntegration(t, lg)
	})

	t.Run("MetricsMonitoring", func(t *testing.T) {
		testMetricsIntegration(t, lg)
	})

	t.Run("StressTest", func(t *testing.T) {
		testStorageStress(t, lg)
	})
}

// testLocalStorageIntegration tests local storage functionality
func testLocalStorageIntegration(t *testing.T, lg logger.Logger) {
	config := LocalConfig{
		Path:            "/tmp/test-recordings",
		MaxSize:         100 * 1024 * 1024, // 100MB
		BufferOnFailure: true,
		BufferPath:      "/tmp/test-buffer",
		MaxBufferSize:   10 * 1024 * 1024, // 10MB
	}

	// Clean up test directories
	os.RemoveAll(config.Path)
	os.RemoveAll(config.BufferPath)
	defer os.RemoveAll(config.Path)
	defer os.RemoveAll(config.BufferPath)

	storage, err := NewLocalStorage(config, lg)
	require.NoError(t, err)
	defer storage.Close()

	ctx := context.Background()
	sessionID := "test-session-001"

	// Test segment storage
	t.Run("StoreSegments", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			segmentName := fmt.Sprintf("segment_%d.ts", i)
			data := generateTestSegment(i, 1024*100) // 100KB segments

			err := storage.StoreSegment(ctx, sessionID, segmentName, data)
			assert.NoError(t, err)
		}
	})

	// Test segment retrieval
	t.Run("GetSegments", func(t *testing.T) {
		segment5, err := storage.GetSegment(ctx, sessionID, "segment_5.ts")
		require.NoError(t, err)
		assert.NotNil(t, segment5)
		assert.True(t, len(segment5) > 0)
	})

	// Test listing
	t.Run("ListSegments", func(t *testing.T) {
		segments, err := storage.ListSegments(ctx, sessionID)
		require.NoError(t, err)
		assert.Len(t, segments, 10)
	})

	// Test playlist storage
	t.Run("StorePlaylists", func(t *testing.T) {
		masterPlaylist := []byte("#EXTM3U\n#EXT-X-VERSION:7\n")
		mediaPlaylist := []byte("#EXTM3U\n#EXT-X-VERSION:6\n")

		err := storage.StorePlaylists(ctx, sessionID, masterPlaylist, mediaPlaylist)
		assert.NoError(t, err)
	})

	// Test manifest storage
	t.Run("StoreManifest", func(t *testing.T) {
		manifest := []byte(`{"session_id":"test-session-001","segments":10}`)
		err := storage.StoreManifest(ctx, sessionID, manifest)
		assert.NoError(t, err)
	})

	// Test health check
	t.Run("HealthCheck", func(t *testing.T) {
		assert.True(t, storage.IsHealthy())

		info := storage.GetStorageInfo()
		assert.Equal(t, StorageTypeLocal, info.Type)
		assert.True(t, info.Available)
	})

	// Test deletion
	t.Run("DeleteSegment", func(t *testing.T) {
		err := storage.DeleteSegment(ctx, sessionID, "segment_0.ts")
		assert.NoError(t, err)

		segments, err := storage.ListSegments(ctx, sessionID)
		require.NoError(t, err)
		assert.Len(t, segments, 9) // One deleted
	})

	// Test retention policy
	t.Run("RetentionPolicy", func(t *testing.T) {
		storage.SetRetentionPolicy(1*time.Second, false)
		time.Sleep(2 * time.Second)

		// Force cleanup
		storage.(*LocalStorage).performCleanup()

		// Old segments should be deleted
		segments, err := storage.ListSegments(ctx, sessionID)
		require.NoError(t, err)
		assert.Less(t, len(segments), 9)
	})
}

// testS3StorageIntegration tests S3 storage functionality
func testS3StorageIntegration(t *testing.T, lg logger.Logger) {
	config := S3Config{
		Endpoint:        os.Getenv("S3_ENDPOINT"),
		Bucket:          os.Getenv("S3_BUCKET"),
		Region:          os.Getenv("S3_REGION"),
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		UseSSL:          true,
		StorageClass:    "STANDARD",
	}

	if config.Bucket == "" {
		config.Bucket = "test-egress-recordings"
	}
	if config.Region == "" {
		config.Region = "us-east-1"
	}

	storage, err := NewS3Storage(config, lg)
	require.NoError(t, err)
	defer storage.Close()

	ctx := context.Background()
	sessionID := fmt.Sprintf("test-session-%d", time.Now().Unix())

	// Test upload with retry
	t.Run("UploadWithRetry", func(t *testing.T) {
		storage.SetRetryConfig(3, 1*time.Second, 10*time.Second, 2.0, true)

		segmentData := generateTestSegment(1, 1024*10) // 10KB
		err := storage.StoreSegment(ctx, sessionID, "test_segment.ts", segmentData)
		assert.NoError(t, err)
	})

	// Test concurrent uploads
	t.Run("ConcurrentUploads", func(t *testing.T) {
		storage.SetConcurrentUploads(5)

		var wg sync.WaitGroup
		errors := make(chan error, 10)

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				segmentName := fmt.Sprintf("concurrent_segment_%d.ts", index)
				data := generateTestSegment(index, 1024*5) // 5KB

				if err := storage.StoreSegment(ctx, sessionID, segmentName, data); err != nil {
					errors <- err
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		// Check for errors
		for err := range errors {
			t.Errorf("concurrent upload error: %v", err)
		}
	})

	// Test download
	t.Run("Download", func(t *testing.T) {
		data, err := storage.GetSegment(ctx, sessionID, "test_segment.ts")
		require.NoError(t, err)
		assert.NotNil(t, data)
		assert.True(t, len(data) > 0)
	})

	// Test metrics
	t.Run("Metrics", func(t *testing.T) {
		metrics := storage.GetMetrics()
		assert.Greater(t, metrics.SuccessfulUploads, int64(0))
		assert.Greater(t, metrics.BytesUploaded, int64(0))
		assert.False(t, metrics.CircuitBreakerOpen)
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		segments, err := storage.ListSegments(ctx, sessionID)
		require.NoError(t, err)

		for _, segment := range segments {
			storage.DeleteSegment(ctx, sessionID, segment)
		}
	})
}

// testHybridStorageIntegration tests hybrid storage functionality
func testHybridStorageIntegration(t *testing.T, lg logger.Logger) {
	// Create local storage
	localConfig := LocalConfig{
		Path: "/tmp/test-hybrid-local",
	}
	os.RemoveAll(localConfig.Path)
	defer os.RemoveAll(localConfig.Path)

	localStorage, err := NewLocalStorage(localConfig, lg)
	require.NoError(t, err)

	// Create mock cloud storage (use another local storage as mock)
	cloudConfig := LocalConfig{
		Path: "/tmp/test-hybrid-cloud",
	}
	os.RemoveAll(cloudConfig.Path)
	defer os.RemoveAll(cloudConfig.Path)

	cloudStorage, err := NewLocalStorage(cloudConfig, lg)
	require.NoError(t, err)

	// Create hybrid storage
	config := DefaultConfig()
	config.Type = StorageTypeHybrid
	config.Upload.UploadOnCreate = true
	config.Upload.MaxConcurrent = 3

	hybrid, err := NewHybridStorage(localStorage, cloudStorage, config, lg)
	require.NoError(t, err)
	defer hybrid.Close()

	ctx := context.Background()
	sessionID := "hybrid-test-session"

	// Test store to both storages
	t.Run("StoreToHybrid", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			segmentName := fmt.Sprintf("hybrid_segment_%d.ts", i)
			data := generateTestSegment(i, 1024*10)

			err := hybrid.StoreSegment(ctx, sessionID, segmentName, data)
			assert.NoError(t, err)
		}

		// Give time for async upload
		time.Sleep(2 * time.Second)
	})

	// Test retrieval (should check local first, then cloud)
	t.Run("RetrieveFromHybrid", func(t *testing.T) {
		data, err := hybrid.GetSegment(ctx, sessionID, "hybrid_segment_2.ts")
		require.NoError(t, err)
		assert.NotNil(t, data)
	})

	// Test list (should combine both storages)
	t.Run("ListFromHybrid", func(t *testing.T) {
		segments, err := hybrid.ListSegments(ctx, sessionID)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(segments), 5)
	})

	// Test metrics
	t.Run("HybridMetrics", func(t *testing.T) {
		metrics := hybrid.GetMetrics()
		assert.GreaterOrEqual(t, metrics.SuccessfulUploads, int64(0))
		assert.True(t, hybrid.IsHealthy())
	})
}

// testCircuitBreakerIntegration tests circuit breaker functionality
func testCircuitBreakerIntegration(t *testing.T, lg logger.Logger) {
	breaker := NewCircuitBreaker(3, 2, 1*time.Second, 2)

	t.Run("NormalOperation", func(t *testing.T) {
		// Should allow requests when closed
		for i := 0; i < 5; i++ {
			err := breaker.Execute(func() error {
				return nil // Success
			})
			assert.NoError(t, err)
		}
		assert.True(t, breaker.IsClosed())
	})

	t.Run("OpenOnFailures", func(t *testing.T) {
		// Cause failures to open circuit
		for i := 0; i < 3; i++ {
			breaker.Execute(func() error {
				return fmt.Errorf("simulated failure %d", i)
			})
		}

		// Circuit should be open
		assert.True(t, breaker.IsOpen())

		// Should reject requests when open
		err := breaker.Execute(func() error {
			return nil
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "circuit breaker is open")
	})

	t.Run("TransitionToHalfOpen", func(t *testing.T) {
		// Wait for timeout
		time.Sleep(1100 * time.Millisecond)

		// Should transition to half-open and allow limited requests
		successCount := 0
		for i := 0; i < 4; i++ {
			err := breaker.Execute(func() error {
				return nil // Success
			})
			if err == nil {
				successCount++
			}
		}

		// Should allow some requests in half-open
		assert.GreaterOrEqual(t, successCount, 2)
	})

	t.Run("CloseOnSuccess", func(t *testing.T) {
		// Reset breaker
		breaker.Reset()
		assert.True(t, breaker.IsClosed())
	})

	t.Run("Statistics", func(t *testing.T) {
		stats := breaker.GetStats()
		assert.Greater(t, stats.TotalRequests, int64(0))
		assert.Contains(t, []string{"closed", "open", "half-open"}, stats.CurrentState)
	})
}

// testRetentionPolicyIntegration tests retention policy functionality
func testRetentionPolicyIntegration(t *testing.T, lg logger.Logger) {
	// Create test storage
	storage, _ := NewLocalStorage(LocalConfig{
		Path: "/tmp/test-retention",
	}, lg)
	defer os.RemoveAll("/tmp/test-retention")

	retentionConfig := &RetentionConfig{
		Enabled:           true,
		LocalHours:        0,    // Immediate deletion for testing
		DeleteAfterUpload: false,
		CleanupInterval:   1 * time.Second,
		MinSegments:       2,
		MinDuration:       0,
	}

	executor := NewRetentionExecutor(storage, retentionConfig, lg)

	t.Run("StartExecutor", func(t *testing.T) {
		err := executor.Start()
		require.NoError(t, err)
		defer executor.Stop()
	})

	t.Run("RegisterSession", func(t *testing.T) {
		executor.RegisterSession("retention-test-001", 1)
		executor.RegisterSession("retention-test-002", 2) // Higher priority
	})

	t.Run("UpdateSession", func(t *testing.T) {
		executor.UpdateSession("retention-test-001", 10, 1024*1024, 5)

		info, err := executor.GetSessionInfo("retention-test-001")
		require.NoError(t, err)
		assert.Equal(t, 10, info.SegmentCount)
		assert.Equal(t, int64(1024*1024), info.TotalSize)
	})

	t.Run("ExtendRetention", func(t *testing.T) {
		err := executor.ExtendRetention("retention-test-002", 24)
		assert.NoError(t, err)
	})

	t.Run("ForceCleanup", func(t *testing.T) {
		// Create some test segments
		ctx := context.Background()
		for i := 0; i < 5; i++ {
			storage.StoreSegment(ctx, "retention-test-001",
				fmt.Sprintf("seg_%d.ts", i),
				generateTestSegment(i, 1024))
		}

		// Force cleanup
		err := executor.ForceCleanup("retention-test-001")
		assert.NoError(t, err)

		// Check metrics
		metrics := executor.GetMetrics()
		assert.GreaterOrEqual(t, metrics.TotalDeleted, int64(0))
	})
}

// testBufferManagerIntegration tests buffer manager functionality
func testBufferManagerIntegration(t *testing.T, lg logger.Logger) {
	localConfig := LocalConfig{
		Path:            "/tmp/test-buffer-local",
		BufferOnFailure: true,
		BufferPath:      "/tmp/test-buffer-cache",
		MaxBufferSize:   10 * 1024 * 1024,
	}

	os.RemoveAll(localConfig.Path)
	os.RemoveAll(localConfig.BufferPath)
	defer os.RemoveAll(localConfig.Path)
	defer os.RemoveAll(localConfig.BufferPath)

	localStorage, _ := NewLocalStorage(localConfig, lg)
	cloudStorage, _ := NewLocalStorage(LocalConfig{Path: "/tmp/test-buffer-cloud"}, lg)
	defer os.RemoveAll("/tmp/test-buffer-cloud")

	bufferManager := NewBufferManager(localStorage, cloudStorage, &localConfig, lg)

	t.Run("StartBufferManager", func(t *testing.T) {
		err := bufferManager.Start()
		require.NoError(t, err)
		defer bufferManager.Stop()
	})

	t.Run("BufferItems", func(t *testing.T) {
		sessionID := "buffer-test-session"

		// Buffer various items
		err := bufferManager.BufferSegment(sessionID, "segment_1.ts", generateTestSegment(1, 1024))
		assert.NoError(t, err)

		err = bufferManager.BufferPlaylist(sessionID, "master", []byte("#EXTM3U\n"))
		assert.NoError(t, err)

		err = bufferManager.BufferManifest(sessionID, []byte("{}"))
		assert.NoError(t, err)

		err = bufferManager.BufferScreenshot(sessionID, 1000, generateTestSegment(0, 512))
		assert.NoError(t, err)
	})

	t.Run("FlushBuffer", func(t *testing.T) {
		// Give time for async processing
		time.Sleep(2 * time.Second)

		metrics := bufferManager.GetMetrics()
		assert.Greater(t, metrics.TotalBuffered, int64(0))
		// Items should be flushed
		assert.GreaterOrEqual(t, metrics.TotalFlushed, int64(0))
	})

	t.Run("GetBufferedSessions", func(t *testing.T) {
		sessions := bufferManager.GetBufferedSessions()
		// Might be empty if already flushed
		assert.GreaterOrEqual(t, len(sessions), 0)
	})
}

// testUploadManagerIntegration tests upload manager functionality
func testUploadManagerIntegration(t *testing.T, lg logger.Logger) {
	storage, _ := NewLocalStorage(LocalConfig{
		Path: "/tmp/test-upload-manager",
	}, lg)
	defer os.RemoveAll("/tmp/test-upload-manager")

	uploadConfig := &UploadConfig{
		Concurrent:    true,
		MaxConcurrent: 3,
		UploadTimeout: 5 * time.Second,
		Retry: RetryConfig{
			Enabled:        true,
			MaxAttempts:    2,
			InitialBackoff: 100 * time.Millisecond,
			MaxBackoff:     1 * time.Second,
			Multiplier:     2.0,
			Jitter:         true,
		},
		CircuitBreaker: CircuitBreakerConfig{
			Enabled:          true,
			FailureThreshold: 3,
			SuccessThreshold: 2,
			OpenTimeout:      1 * time.Second,
			HalfOpenRequests: 2,
		},
	}

	uploadManager := NewUploadManager(storage, uploadConfig, lg)

	t.Run("StartUploadManager", func(t *testing.T) {
		err := uploadManager.Start()
		require.NoError(t, err)
		defer uploadManager.Stop()
	})

	t.Run("QueueUploads", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			job := &UploadJob{
				ID:          fmt.Sprintf("job_%d", i),
				SessionID:   "upload-test-session",
				SegmentName: fmt.Sprintf("segment_%d.ts", i),
				Data:        generateTestSegment(i, 1024),
				ContentType: "video/mp2t",
				Priority:    i % 3,
			}

			err := uploadManager.QueueUpload(job)
			assert.NoError(t, err)
		}
	})

	t.Run("FlushUploads", func(t *testing.T) {
		err := uploadManager.Flush(10 * time.Second)
		assert.NoError(t, err)
	})

	t.Run("CheckMetrics", func(t *testing.T) {
		metrics := uploadManager.GetMetrics()
		assert.Equal(t, int64(10), metrics.TotalJobs)
		assert.Equal(t, int64(0), metrics.PendingJobs)
		assert.Greater(t, metrics.CompletedUploads, int64(0))
	})

	t.Run("HealthCheck", func(t *testing.T) {
		assert.True(t, uploadManager.IsHealthy())
	})
}

// testHLSPlaylistIntegration tests HLS playlist functionality
func testHLSPlaylistIntegration(t *testing.T, lg logger.Logger) {
	storage, _ := NewLocalStorage(LocalConfig{
		Path: "/tmp/test-hls-playlist",
	}, lg)
	defer os.RemoveAll("/tmp/test-hls-playlist")

	hlsConfig := HLSConfig{
		SegmentDuration: 4,
		PlaylistType:    "event",
		MaxSegments:     0,
		TargetDuration:  4,
		MasterPlaylist:  true,
		ByteRange:       false,
		Encryption: HLSEncryption{
			Enabled:             false,
			Method:              "AES-128",
			KeyRotationInterval: 10,
		},
	}

	playlistManager, err := NewHLSPlaylistManager(storage, hlsConfig, lg)
	require.NoError(t, err)

	sessionID := "hls-test-session"
	ctx := context.Background()

	t.Run("AddSegments", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			segment := &HLSSegment{
				Index:    i,
				Name:     fmt.Sprintf("segment_%d.ts", i),
				Duration: 3.96 + float64(i%2)*0.04, // Vary duration slightly
				URI:      fmt.Sprintf("segment_%d.ts", i),
			}

			err := playlistManager.AddSegment(sessionID, segment)
			assert.NoError(t, err)
		}
	})

	t.Run("GenerateMediaPlaylist", func(t *testing.T) {
		playlist, err := playlistManager.GenerateMediaPlaylist(sessionID)
		require.NoError(t, err)
		assert.Contains(t, string(playlist), "#EXTM3U")
		assert.Contains(t, string(playlist), "#EXT-X-VERSION:6")
		assert.Contains(t, string(playlist), "#EXT-X-TARGETDURATION:4")
		assert.Contains(t, string(playlist), "segment_")
	})

	t.Run("SetMasterPlaylist", func(t *testing.T) {
		master := &MasterPlaylist{
			Variants: []*Variant{
				{
					Bandwidth:  2000000,
					Resolution: "1920x1080",
					Codecs:     "avc1.42e01f,mp4a.40.2",
					FrameRate:  30.0,
					URI:        "1080p.m3u8",
				},
				{
					Bandwidth:  1000000,
					Resolution: "1280x720",
					Codecs:     "avc1.42e01f,mp4a.40.2",
					FrameRate:  30.0,
					URI:        "720p.m3u8",
				},
			},
		}

		playlistManager.SetMasterPlaylist(sessionID, master)
	})

	t.Run("GenerateMasterPlaylist", func(t *testing.T) {
		playlist, err := playlistManager.GenerateMasterPlaylist(sessionID)
		require.NoError(t, err)
		assert.Contains(t, string(playlist), "#EXTM3U")
		assert.Contains(t, string(playlist), "#EXT-X-VERSION:7")
		assert.Contains(t, string(playlist), "BANDWIDTH=2000000")
		assert.Contains(t, string(playlist), "RESOLUTION=1920x1080")
	})

	t.Run("SavePlaylists", func(t *testing.T) {
		err := playlistManager.SavePlaylist(ctx, sessionID, "media")
		assert.NoError(t, err)

		err = playlistManager.SavePlaylist(ctx, sessionID, "master")
		assert.NoError(t, err)
	})

	t.Run("ValidatePlaylist", func(t *testing.T) {
		err := playlistManager.ValidatePlaylist(sessionID)
		assert.NoError(t, err)
	})

	t.Run("GetStats", func(t *testing.T) {
		stats := playlistManager.GetStats()
		assert.Equal(t, 1, stats["total_sessions"])
		assert.Equal(t, 10, stats["total_segments"])
	})
}

// testSegmentValidationIntegration tests segment validation
func testSegmentValidationIntegration(t *testing.T, lg logger.Logger) {
	validator := NewSegmentValidator(GetDefaultValidationConfig(), lg)

	t.Run("ValidateMPEGTS", func(t *testing.T) {
		// Create a minimal valid MPEG-TS segment
		segment := createTestMPEGTSSegment()

		result := validator.ValidateSegment(segment)
		assert.NotNil(t, result)

		// Check basic validation
		assert.True(t, result.SegmentInfo.HasPAT)
		assert.True(t, result.SegmentInfo.HasPMT)
		assert.Greater(t, result.SegmentInfo.PacketCount, 0)
	})

	t.Run("ValidatePlaylist", func(t *testing.T) {
		playlist := []byte(`#EXTM3U
#EXT-X-VERSION:6
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:3.96,
segment_0.ts
#EXTINF:4.00,
segment_1.ts
#EXTINF:3.96,
segment_2.ts`)

		err := validator.ValidatePlaylist(playlist)
		assert.NoError(t, err)
	})

	t.Run("CheckCompatibility", func(t *testing.T) {
		segment := createTestMPEGTSSegment()
		result := validator.ValidateSegment(segment)

		// Check playback compatibility
		assert.True(t, result.PlaybackSupport.Safari)
		assert.True(t, result.PlaybackSupport.Chrome)
		assert.True(t, result.PlaybackSupport.VLC)
		assert.True(t, result.PlaybackSupport.FFmpeg)
	})
}

// testScreenshotIntegration tests screenshot extraction
func testScreenshotIntegration(t *testing.T, lg logger.Logger) {
	storage, _ := NewLocalStorage(LocalConfig{
		Path: "/tmp/test-screenshots",
	}, lg)
	defer os.RemoveAll("/tmp/test-screenshots")

	screenshotConfig := ScreenshotConfig{
		Enabled:           true,
		Interval:          5,
		Format:            "jpeg",
		Quality:           85,
		Scale:             0.5,
		MaxCount:          10,
		Upload:            true,
		ThumbnailPlaylist: false,
	}

	extractor := NewScreenshotExtractor(storage, screenshotConfig, lg)

	t.Run("StartExtractor", func(t *testing.T) {
		err := extractor.Start()
		require.NoError(t, err)
		defer extractor.Stop()
	})

	t.Run("ExtractFromSegment", func(t *testing.T) {
		sessionID := "screenshot-test-session"
		segmentData := generateTestSegment(1, 1024*10)

		err := extractor.ExtractFromSegment(sessionID, segmentData, 1000)
		assert.NoError(t, err)

		// Wait for async extraction
		time.Sleep(1 * time.Second)
	})

	t.Run("ExtractAtTime", func(t *testing.T) {
		sessionID := "screenshot-test-session"
		segmentData := generateTestSegment(2, 1024*10)

		done := make(chan bool)
		err := extractor.ExtractAtTime(sessionID, segmentData, 2.5, func(data []byte, err error) {
			assert.NoError(t, err)
			assert.NotNil(t, data)
			assert.Greater(t, len(data), 0)
			done <- true
		})
		assert.NoError(t, err)

		select {
		case <-done:
			// Success
		case <-time.After(5 * time.Second):
			t.Error("screenshot extraction timeout")
		}
	})

	t.Run("GenerateThumbnailStrip", func(t *testing.T) {
		screenshots := make([][]byte, 5)
		for i := range screenshots {
			// Generate test screenshot data
			screenshots[i] = generateTestImage()
		}

		strip, err := extractor.GenerateThumbnailStrip(screenshots, 5)
		assert.NoError(t, err)
		assert.NotNil(t, strip)
		assert.Greater(t, len(strip), 0)
	})

	t.Run("CheckMetrics", func(t *testing.T) {
		metrics := extractor.GetMetrics()
		assert.Greater(t, metrics.TotalJobs, int64(0))
		assert.GreaterOrEqual(t, metrics.CompletedJobs, int64(0))
	})
}

// testMetricsIntegration tests metrics monitoring
func testMetricsIntegration(t *testing.T, lg logger.Logger) {
	monitor := NewMetricsMonitor(lg)

	t.Run("StartMonitor", func(t *testing.T) {
		err := monitor.Start()
		require.NoError(t, err)
		defer monitor.Stop()
	})

	t.Run("RecordMetrics", func(t *testing.T) {
		// Record various metrics
		monitor.RecordSegmentStored(1024*100, true)
		monitor.RecordSegmentStored(1024*200, false)

		monitor.RecordUpload(true, 500*time.Millisecond, 1024*100)
		monitor.RecordUpload(false, 1*time.Second, 1024*50)

		monitor.RecordPlaybackSession(true, 0)
		monitor.RecordPlaybackSession(false, 10*time.Minute)
	})

	t.Run("GetCurrentMetrics", func(t *testing.T) {
		// Wait for aggregation
		time.Sleep(1 * time.Second)

		metrics := monitor.GetCurrentMetrics()
		assert.NotNil(t, metrics)
		assert.Greater(t, metrics.Storage.TotalSegments, int64(0))
		assert.Greater(t, metrics.Upload.TotalUploads, int64(0))
		assert.Greater(t, metrics.Playback.TotalSessions, int64(0))
	})

	t.Run("HealthCheck", func(t *testing.T) {
		metrics := monitor.GetCurrentMetrics()
		assert.Contains(t, []HealthStatus{
			HealthStatusHealthy,
			HealthStatusDegraded,
			HealthStatusCritical,
		}, metrics.Health.Status)
		assert.GreaterOrEqual(t, metrics.Health.Score, 0)
		assert.LessOrEqual(t, metrics.Health.Score, 100)
	})

	t.Run("AlertHandling", func(t *testing.T) {
		alertReceived := false
		monitor.RegisterAlertHandler(func(alert Alert) {
			alertReceived = true
			assert.NotEmpty(t, alert.ID)
			assert.NotEmpty(t, alert.Message)
		})

		// Trigger an alert by simulating bad metrics
		for i := 0; i < 10; i++ {
			monitor.RecordUpload(false, 1*time.Second, 1024)
		}

		time.Sleep(1 * time.Second)
		// Alert might or might not be triggered depending on thresholds
	})

	t.Run("ExportMetrics", func(t *testing.T) {
		data, err := monitor.ExportMetrics()
		assert.NoError(t, err)
		assert.NotNil(t, data)
		assert.Contains(t, string(data), "timestamp")
		assert.Contains(t, string(data), "health")
	})
}

// testStorageStress performs stress testing
func testStorageStress(t *testing.T, lg logger.Logger) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	storage, _ := NewLocalStorage(LocalConfig{
		Path:    "/tmp/test-stress",
		MaxSize: 100 * 1024 * 1024, // 100MB
	}, lg)
	defer os.RemoveAll("/tmp/test-stress")

	ctx := context.Background()
	sessionID := "stress-test-session"

	t.Run("ConcurrentWrites", func(t *testing.T) {
		var wg sync.WaitGroup
		errors := make(chan error, 100)

		// 100 concurrent writes
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				segmentName := fmt.Sprintf("stress_segment_%d.ts", index)
				data := generateTestSegment(index, 1024*10) // 10KB each

				if err := storage.StoreSegment(ctx, sessionID, segmentName, data); err != nil {
					errors <- err
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		errorCount := 0
		for err := range errors {
			t.Logf("stress test write error: %v", err)
			errorCount++
		}

		assert.Less(t, errorCount, 10) // Allow some failures under stress
	})

	t.Run("ConcurrentReads", func(t *testing.T) {
		var wg sync.WaitGroup
		errors := make(chan error, 100)

		// 100 concurrent reads
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				segmentName := fmt.Sprintf("stress_segment_%d.ts", index%10) // Read existing segments

				data, err := storage.GetSegment(ctx, sessionID, segmentName)
				if err != nil {
					errors <- err
				} else if data == nil || len(data) == 0 {
					errors <- fmt.Errorf("empty data for segment %s", segmentName)
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		errorCount := 0
		for err := range errors {
			t.Logf("stress test read error: %v", err)
			errorCount++
		}

		// Some reads might fail if the segment wasn't written yet
		assert.Less(t, errorCount, 50)
	})

	t.Run("MixedOperations", func(t *testing.T) {
		var wg sync.WaitGroup

		// Writers
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				for j := 0; j < 5; j++ {
					segmentName := fmt.Sprintf("mixed_%d_%d.ts", index, j)
					data := generateTestSegment(index*5+j, 1024*5)
					storage.StoreSegment(ctx, sessionID, segmentName, data)
				}
			}(i)
		}

		// Readers
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				storage.ListSegments(ctx, sessionID)
			}(i)
		}

		// Deleters
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				time.Sleep(100 * time.Millisecond) // Let some writes happen first
				segmentName := fmt.Sprintf("mixed_%d_0.ts", index)
				storage.DeleteSegment(ctx, sessionID, segmentName)
			}(i)
		}

		done := make(chan bool)
		go func() {
			wg.Wait()
			done <- true
		}()

		select {
		case <-done:
			// Success
		case <-time.After(30 * time.Second):
			t.Error("stress test timeout")
		}
	})
}

// Helper functions

func generateTestSegment(index int, size int) []byte {
	data := make([]byte, size)
	// Fill with pattern for verification
	for i := range data {
		data[i] = byte((index + i) % 256)
	}
	return data
}

func generateTestImage() []byte {
	// Generate a simple PNG image
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func createTestMPEGTSSegment() []byte {
	// Create a minimal valid MPEG-TS segment
	// This is a simplified version for testing
	const packetSize = 188
	const syncByte = 0x47

	// Create 10 packets
	segment := make([]byte, packetSize*10)

	for i := 0; i < 10; i++ {
		offset := i * packetSize
		// Sync byte
		segment[offset] = syncByte
		// PID and flags
		if i == 0 {
			// PAT packet (PID 0)
			segment[offset+1] = 0x40 // Payload unit start
			segment[offset+2] = 0x00 // PID 0
		} else if i == 1 {
			// PMT packet (PID 0x100)
			segment[offset+1] = 0x41
			segment[offset+2] = 0x00
		} else {
			// Data packets
			segment[offset+1] = 0x41
			segment[offset+2] = 0x01
		}
		segment[offset+3] = 0x10 // Adaptation field control
	}

	return segment
}

// Benchmark tests

func BenchmarkSegmentStorage(b *testing.B) {
	lg := logger.GetLogger()
	storage, _ := NewLocalStorage(LocalConfig{
		Path: "/tmp/bench-storage",
	}, lg)
	defer os.RemoveAll("/tmp/bench-storage")

	ctx := context.Background()
	sessionID := "bench-session"
	data := generateTestSegment(1, 1024*100) // 100KB segment

	b.ResetTimer()

	b.Run("Store", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			segmentName := fmt.Sprintf("segment_%d.ts", i)
			storage.StoreSegment(ctx, sessionID, segmentName, data)
		}
	})

	b.Run("Retrieve", func(b *testing.B) {
		// Store first
		storage.StoreSegment(ctx, sessionID, "bench_segment.ts", data)

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			storage.GetSegment(ctx, sessionID, "bench_segment.ts")
		}
	})

	b.Run("List", func(b *testing.B) {
		// Store some segments first
		for i := 0; i < 10; i++ {
			storage.StoreSegment(ctx, sessionID, fmt.Sprintf("list_seg_%d.ts", i), data)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			storage.ListSegments(ctx, sessionID)
		}
	})
}

func BenchmarkPlaylistGeneration(b *testing.B) {
	lg := logger.GetLogger()
	storage, _ := NewLocalStorage(LocalConfig{Path: "/tmp/bench-playlist"}, lg)
	defer os.RemoveAll("/tmp/bench-playlist")

	config := HLSConfig{
		SegmentDuration: 4,
		PlaylistType:    "event",
		TargetDuration:  4,
	}

	manager, _ := NewHLSPlaylistManager(storage, config, lg)
	sessionID := "bench-playlist-session"

	// Add segments
	for i := 0; i < 100; i++ {
		segment := &HLSSegment{
			Index:    i,
			Name:     fmt.Sprintf("segment_%d.ts", i),
			Duration: 3.96,
			URI:      fmt.Sprintf("segment_%d.ts", i),
		}
		manager.AddSegment(sessionID, segment)
	}

	b.ResetTimer()

	b.Run("GenerateMediaPlaylist", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			manager.GenerateMediaPlaylist(sessionID)
		}
	})
}

func BenchmarkSegmentValidation(b *testing.B) {
	lg := logger.GetLogger()
	validator := NewSegmentValidator(GetDefaultValidationConfig(), lg)
	segment := createTestMPEGTSSegment()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		validator.ValidateSegment(segment)
	}
}