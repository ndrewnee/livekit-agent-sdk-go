package screenshot

import (
	"context"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockUploader implements storage.Uploader for testing
type MockUploader struct {
	ScreenshotCount int
	SegmentCount    int
	PlaylistCount   int
}

func (m *MockUploader) UploadSegment(ctx context.Context, data []byte, filename string) error {
	m.SegmentCount++
	return nil
}

func (m *MockUploader) UploadPlaylist(ctx context.Context, data []byte, filename string) error {
	m.PlaylistCount++
	return nil
}

func (m *MockUploader) UploadScreenshot(ctx context.Context, data []byte, filename string) error {
	m.ScreenshotCount++
	return nil
}

func (m *MockUploader) GetUploadURL(key string, expiration time.Duration) (string, error) {
	return "http://test.url/" + key, nil
}

func (m *MockUploader) Close() error {
	return nil
}

func TestExtractorCreation(t *testing.T) {
	t.Run("disabled extractor returns nil", func(t *testing.T) {
		config := &Config{
			Enabled: false,
		}
		extractor := NewExtractor(config, "test-session", &MockUploader{})
		assert.Nil(t, extractor)
	})

	t.Run("enabled extractor with defaults", func(t *testing.T) {
		config := &Config{
			Enabled: true,
		}
		extractor := NewExtractor(config, "test-session", &MockUploader{})
		require.NotNil(t, extractor)

		assert.Equal(t, "jpeg", config.OutputFormat)
		assert.Equal(t, 85, config.Quality)
		assert.Equal(t, 10, config.Interval)
	})

	t.Run("custom configuration", func(t *testing.T) {
		config := &Config{
			Enabled:        true,
			Interval:       5,
			OutputFormat:   "png",
			Quality:        95,
			Width:          1280,
			Height:         720,
			MaxScreenshots: 100,
		}
		extractor := NewExtractor(config, "test-session", &MockUploader{})
		require.NotNil(t, extractor)

		assert.Equal(t, "png", config.OutputFormat)
		assert.Equal(t, 95, config.Quality)
		assert.Equal(t, 5, config.Interval)
		assert.Equal(t, 1280, config.Width)
		assert.Equal(t, 720, config.Height)
		assert.Equal(t, 100, config.MaxScreenshots)
	})
}

func TestExtractorBranch(t *testing.T) {
	// Initialize GStreamer for testing
	gst.Init(nil)

	config := &Config{
		Enabled:      true,
		Interval:     5,
		OutputFormat: "jpeg",
		Quality:      90,
	}
	extractor := NewExtractor(config, "test-session", &MockUploader{})
	require.NotNil(t, extractor)

	t.Run("creates branch elements", func(t *testing.T) {
		elements, err := extractor.CreateBranch()
		assert.NoError(t, err)
		assert.NotNil(t, elements)
		assert.Greater(t, len(elements), 0)
		// Should have queue, videorate, caps, videoconvert, encoder, appsink
		assert.GreaterOrEqual(t, len(elements), 6)
	})

	t.Run("nil extractor returns nil", func(t *testing.T) {
		var nilExtractor *Extractor
		elements, err := nilExtractor.CreateBranch()
		assert.NoError(t, err)
		assert.Nil(t, elements)
	})

	t.Run("disabled extractor returns nil", func(t *testing.T) {
		disabledConfig := &Config{
			Enabled: false,
		}
		disabledExtractor := NewExtractor(disabledConfig, "test", &MockUploader{})
		assert.Nil(t, disabledExtractor)
	})
}

func TestExtractorWithScaling(t *testing.T) {
	// Initialize GStreamer for testing
	gst.Init(nil)

	config := &Config{
		Enabled:      true,
		Width:        1920,
		Height:       1080,
		OutputFormat: "jpeg",
	}
	extractor := NewExtractor(config, "test-session", &MockUploader{})
	require.NotNil(t, extractor)

	elements, err := extractor.CreateBranch()
	assert.NoError(t, err)
	assert.NotNil(t, elements)
	// Should have additional videoscale and caps elements
	assert.GreaterOrEqual(t, len(elements), 8)
}

func TestExtractorStartStop(t *testing.T) {
	config := &Config{
		Enabled: true,
	}
	extractor := NewExtractor(config, "test-session", &MockUploader{})
	require.NotNil(t, extractor)

	// Initial state
	stats := extractor.GetStats()
	assert.True(t, stats.Active) // Active by default on creation
	assert.Equal(t, 0, stats.CaptureCount)

	// Stop extractor
	extractor.Stop()
	stats = extractor.GetStats()
	assert.False(t, stats.Active)

	// Start extractor
	extractor.Start()
	stats = extractor.GetStats()
	assert.True(t, stats.Active)

	// Test nil safety
	var nilExtractor *Extractor
	nilExtractor.Start() // Should not panic
	nilExtractor.Stop()  // Should not panic
	stats = nilExtractor.GetStats()
	assert.Equal(t, 0, stats.CaptureCount)
}

func TestExtractorPNGFormat(t *testing.T) {
	// Initialize GStreamer for testing
	gst.Init(nil)

	config := &Config{
		Enabled:      true,
		OutputFormat: "png",
	}
	extractor := NewExtractor(config, "test-session", &MockUploader{})
	require.NotNil(t, extractor)

	elements, err := extractor.CreateBranch()
	assert.NoError(t, err)
	assert.NotNil(t, elements)
	// Should create pngenc instead of jpegenc
}

func TestExtractorMaxScreenshots(t *testing.T) {
	config := &Config{
		Enabled:        true,
		MaxScreenshots: 10,
	}
	extractor := NewExtractor(config, "test-session", &MockUploader{})
	require.NotNil(t, extractor)

	// Simulate capturing screenshots up to the limit
	for i := 0; i < 15; i++ {
		extractor.handleNewSample(nil)
		stats := extractor.GetStats()
		if i < config.MaxScreenshots {
			assert.Equal(t, i+1, stats.CaptureCount)
		} else {
			// Should stop at max
			assert.Equal(t, config.MaxScreenshots, stats.CaptureCount)
		}
	}
}

func BenchmarkExtractorCreation(b *testing.B) {
	config := &Config{
		Enabled:      true,
		Interval:     5,
		OutputFormat: "jpeg",
		Quality:      85,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		extractor := NewExtractor(config, "bench-session", &MockUploader{})
		_ = extractor
	}
}

func BenchmarkBranchCreation(b *testing.B) {
	// Initialize GStreamer for testing
	gst.Init(nil)

	config := &Config{
		Enabled:      true,
		Width:        1280,
		Height:       720,
		OutputFormat: "jpeg",
	}
	extractor := NewExtractor(config, "bench-session", &MockUploader{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		elements, _ := extractor.CreateBranch()
		_ = elements
	}
}