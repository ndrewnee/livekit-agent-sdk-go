package screenshot

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/storage"
	"github.com/go-gst/go-gst/gst"
)

// Config holds screenshot extraction configuration
type Config struct {
	Enabled       bool
	Interval      int    // Seconds between screenshots
	OutputFormat  string // "jpeg" or "png"
	Quality       int    // JPEG quality (1-100)
	Width         int    // Resize width (0 = keep original)
	Height        int    // Resize height (0 = keep original)
	MaxScreenshots int   // Maximum number of screenshots (0 = unlimited)
}

// Extractor handles screenshot extraction from video pipeline
type Extractor struct {
	config     *Config
	pipeline   *gst.Pipeline
	branch     *gst.Element // Tee branch for screenshots
	sessionID  string
	uploader   storage.Storage
	extractor  *SampleExtractor // Real CGo sample extractor

	mu              sync.Mutex
	screenshotCount int
	lastCapture     time.Time
	active          bool
}

// NewExtractor creates a new screenshot extractor
func NewExtractor(config *Config, sessionID string, uploader storage.Storage) *Extractor {
	if !config.Enabled {
		return nil
	}

	// Set defaults
	if config.OutputFormat == "" {
		config.OutputFormat = "jpeg"
	}
	if config.Quality == 0 {
		config.Quality = 85
	}
	if config.Interval == 0 {
		config.Interval = 10 // Default 10 seconds
	}

	return &Extractor{
		config:    config,
		sessionID: sessionID,
		uploader:  uploader,
		active:    true,
	}
}

// CreateBranch creates a GStreamer branch for screenshot extraction
// This should be added to the main pipeline after the video decoder
func (e *Extractor) CreateBranch() ([]*gst.Element, error) {
	if e == nil || !e.config.Enabled {
		return nil, nil
	}

	elements := []*gst.Element{}

	// Create queue for screenshot branch
	queue, err := gst.NewElement("queue")
	if err != nil {
		return nil, fmt.Errorf("failed to create screenshot queue: %w", err)
	}
	queue.SetProperty("max-size-buffers", uint(1))
	queue.SetProperty("leaky", 2) // Drop old buffers
	elements = append(elements, queue)

	// Videorate to control screenshot frequency
	videorate, err := gst.NewElement("videorate")
	if err != nil {
		return nil, fmt.Errorf("failed to create videorate: %w", err)
	}
	videorate.SetProperty("skip-to-first", true)
	elements = append(elements, videorate)

	// Caps to set framerate (1 frame per interval)
	capsStr := fmt.Sprintf("video/x-raw,framerate=1/%d", e.config.Interval)
	caps, err := gst.NewElement("capsfilter")
	if err != nil {
		return nil, fmt.Errorf("failed to create capsfilter: %w", err)
	}
	caps.SetProperty("caps", gst.NewCapsFromString(capsStr))
	elements = append(elements, caps)

	// Video scale if resizing is needed
	if e.config.Width > 0 || e.config.Height > 0 {
		videoscale, err := gst.NewElement("videoscale")
		if err != nil {
			return nil, fmt.Errorf("failed to create videoscale: %w", err)
		}
		elements = append(elements, videoscale)

		// Add caps for scaled resolution
		scaleCapsStr := fmt.Sprintf("video/x-raw")
		if e.config.Width > 0 {
			scaleCapsStr += fmt.Sprintf(",width=%d", e.config.Width)
		}
		if e.config.Height > 0 {
			scaleCapsStr += fmt.Sprintf(",height=%d", e.config.Height)
		}

		scaleCaps, err := gst.NewElement("capsfilter")
		if err != nil {
			return nil, fmt.Errorf("failed to create scale capsfilter: %w", err)
		}
		scaleCaps.SetProperty("caps", gst.NewCapsFromString(scaleCapsStr))
		elements = append(elements, scaleCaps)
	}

	// Video convert for format conversion
	videoconvert, err := gst.NewElement("videoconvert")
	if err != nil {
		return nil, fmt.Errorf("failed to create videoconvert: %w", err)
	}
	elements = append(elements, videoconvert)

	// Encoder based on format
	var encoder *gst.Element
	if e.config.OutputFormat == "png" {
		encoder, err = gst.NewElement("pngenc")
		if err != nil {
			return nil, fmt.Errorf("failed to create pngenc: %w", err)
		}
	} else {
		encoder, err = gst.NewElement("jpegenc")
		if err != nil {
			return nil, fmt.Errorf("failed to create jpegenc: %w", err)
		}
		encoder.SetProperty("quality", e.config.Quality)
	}
	elements = append(elements, encoder)

	// AppSink to capture encoded frames
	appsink, err := gst.NewElement("appsink")
	if err != nil {
		return nil, fmt.Errorf("failed to create appsink: %w", err)
	}
	appsink.SetProperty("emit-signals", true)
	appsink.SetProperty("sync", false)

	// Create real CGo sample extractor
	e.extractor = NewSampleExtractor(appsink)

	// Connect to new-sample signal
	appsink.Connect("new-sample", func(sink *gst.Element) gst.FlowReturn {
		e.handleNewSample(sink)
		return gst.FlowOK
	})

	elements = append(elements, appsink)

	return elements, nil
}

// handleNewSample processes a new screenshot sample
func (e *Extractor) handleNewSample(sink *gst.Element) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.active {
		return
	}

	// Check max screenshots limit
	if e.config.MaxScreenshots > 0 && e.screenshotCount >= e.config.MaxScreenshots {
		return
	}

	// Use real CGo extractor to get buffer data
	if e.extractor == nil {
		log.Printf("Error: sample extractor not initialized")
		return
	}

	// Extract actual buffer data using CGo
	data, err := e.extractor.ExtractBuffer()
	if err != nil {
		log.Printf("Failed to extract screenshot buffer: %v", err)
		return
	}

	// Verify we got real data
	if len(data) == 0 {
		log.Printf("Error: extracted empty screenshot buffer")
		return
	}

	e.screenshotCount++
	e.lastCapture = time.Now()

	// Generate filename
	ext := e.config.OutputFormat
	if ext == "jpeg" {
		ext = "jpg"
	}
	filename := fmt.Sprintf("screenshot_%s_%06d.%s",
		e.sessionID, e.screenshotCount, ext)

	log.Printf("Captured real screenshot %d: %s (size: %d bytes)", e.screenshotCount, filename, len(data))

	// Upload the real screenshot data if uploader is configured
	if e.uploader != nil {
		go func(data []byte, name string) {
			// Convert filename to timestamp for StoreScreenshot
			// Use the current timestamp as we don't have frame timestamp
			timestamp := time.Now().UnixNano()
			if err := e.uploader.StoreScreenshot(context.Background(), e.sessionID, timestamp, data); err != nil {
				log.Printf("Failed to upload screenshot %s: %v", name, err)
			} else {
				log.Printf("Successfully uploaded screenshot %s", name)
			}
		}(data, filename)
	}
}

// Start begins screenshot extraction
func (e *Extractor) Start() {
	if e == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.active = true
	e.lastCapture = time.Now()
	log.Printf("Started screenshot extraction for session %s", e.sessionID)
}

// Stop ends screenshot extraction
func (e *Extractor) Stop() {
	if e == nil {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.active = false
	log.Printf("Stopped screenshot extraction for session %s, captured %d screenshots",
		e.sessionID, e.screenshotCount)
}

// GetStats returns screenshot statistics
func (e *Extractor) GetStats() ScreenshotStats {
	if e == nil {
		return ScreenshotStats{}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	return ScreenshotStats{
		CaptureCount: e.screenshotCount,
		LastCapture:  e.lastCapture,
		Active:       e.active,
	}
}

// ScreenshotStats holds screenshot statistics
type ScreenshotStats struct {
	CaptureCount int
	LastCapture  time.Time
	Active       bool
}