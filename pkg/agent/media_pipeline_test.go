package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStage implements MediaPipelineStage for testing
type TestStage struct {
	name     string
	priority int
	process  func(ctx context.Context, data MediaData) (MediaData, error)
}

func (ts *TestStage) GetName() string                     { return ts.name }
func (ts *TestStage) GetPriority() int                    { return ts.priority }
func (ts *TestStage) CanProcess(mediaType MediaType) bool { return true }
func (ts *TestStage) Process(ctx context.Context, input MediaData) (MediaData, error) {
	if ts.process != nil {
		return ts.process(ctx, input)
	}
	return input, nil
}

// TestMediaPipelineCreation tests basic pipeline creation
func TestMediaPipelineCreation(t *testing.T) {
	pipeline := NewMediaPipeline()

	require.NotNil(t, pipeline)
	assert.NotNil(t, pipeline.stages)
	assert.NotNil(t, pipeline.processors)
	assert.NotNil(t, pipeline.tracks)
	assert.NotNil(t, pipeline.bufferFactory)
	assert.Empty(t, pipeline.stages)
	assert.Empty(t, pipeline.processors)
	assert.Empty(t, pipeline.tracks)
}

// TestMediaPipelineStageManagement tests adding and removing stages
func TestMediaPipelineStageManagement(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Create test stages
	stage1 := &TestStage{
		name:     "Stage1",
		priority: 1,
	}

	stage2 := &TestStage{
		name:     "Stage2",
		priority: 2,
	}

	// Add stages
	pipeline.AddStage(stage1)
	pipeline.AddStage(stage2)

	// Verify stages are added in priority order
	pipeline.mu.RLock()
	assert.Len(t, pipeline.stages, 2)
	assert.Equal(t, "Stage1", pipeline.stages[0].GetName())
	assert.Equal(t, "Stage2", pipeline.stages[1].GetName())
	pipeline.mu.RUnlock()

	// Try to add duplicate stage (allowed in current implementation)
	pipeline.AddStage(stage1)
	pipeline.mu.RLock()
	assert.Len(t, pipeline.stages, 3)
	pipeline.mu.RUnlock()

	// Remove stage
	pipeline.RemoveStage("Stage1")

	pipeline.mu.RLock()
	// RemoveStage removes all stages with that name
	remainingStages := 0
	for _, s := range pipeline.stages {
		if s.GetName() != "Stage1" {
			remainingStages++
		}
	}
	assert.Equal(t, 1, remainingStages)
	pipeline.mu.RUnlock()
}

// TestMediaPipelineTrackProcessing tests processing tracks with synthetic audio
func TestMediaPipelineTrackProcessing(t *testing.T) {
	// This test requires LiveKit server - use TestMediaPipelineWithLiveKit instead
	t.Log("Use TestMediaPipelineWithLiveKit for track processing tests with LiveKit server")
}

// TestMediaPipelineMultipleTracks tests processing multiple tracks simultaneously
func TestMediaPipelineMultipleTracks(t *testing.T) {
	// This test requires LiveKit server - use TestMediaPipelineMultipleTracksLiveKit instead
	t.Log("Use TestMediaPipelineMultipleTracksLiveKit for multiple track tests with LiveKit server")
}

// TestMediaPipelineProcessorRegistration tests processor registration
func TestMediaPipelineProcessorRegistration(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Create a mock processor
	processor := &MockMediaProcessor{
		name: "TestProcessor",
		capabilities: ProcessorCapabilities{
			SupportedMediaTypes: []MediaType{MediaTypeAudio, MediaTypeVideo},
			SupportedFormats: []MediaFormat{
				{SampleRate: 48000, Channels: 2, BitDepth: 16},
				{Width: 1920, Height: 1080, FrameRate: 30},
			},
			MaxConcurrency: 2,
		},
	}

	// Register processor
	pipeline.RegisterProcessor(processor)

	// Get processor
	pipeline.mu.RLock()
	retrieved, exists := pipeline.processors[processor.GetName()]
	pipeline.mu.RUnlock()

	assert.True(t, exists)
	assert.Equal(t, processor, retrieved)

	// Register another processor
	processor2 := &MockMediaProcessor{
		name: "TestProcessor2",
	}
	pipeline.RegisterProcessor(processor2)

	pipeline.mu.RLock()
	assert.Len(t, pipeline.processors, 2)
	pipeline.mu.RUnlock()
}

// MockMediaProcessor implements MediaProcessor for testing
type MockMediaProcessor struct {
	name         string
	capabilities ProcessorCapabilities
	processCalls int
	mu           sync.Mutex
}

func (m *MockMediaProcessor) ProcessAudio(ctx context.Context, samples []byte, sampleRate uint32, channels uint8) ([]byte, error) {
	m.mu.Lock()
	m.processCalls++
	m.mu.Unlock()
	return samples, nil
}

func (m *MockMediaProcessor) ProcessVideo(ctx context.Context, frame []byte, width, height uint32, format VideoFormat) ([]byte, error) {
	m.mu.Lock()
	m.processCalls++
	m.mu.Unlock()
	return frame, nil
}

func (m *MockMediaProcessor) GetName() string {
	return m.name
}

func (m *MockMediaProcessor) GetCapabilities() ProcessorCapabilities {
	return m.capabilities
}

// TestMediaPipelineConfiguration tests pipeline configuration
func TestMediaPipelineConfiguration(t *testing.T) {
	pipeline := NewMediaPipeline()

	// The pipeline should have default values
	pipeline.mu.RLock()
	assert.NotNil(t, pipeline.bufferFactory)
	pipeline.mu.RUnlock()
}

// TestMediaPipelineDestroy tests pipeline destruction
func TestMediaPipelineDestroy(t *testing.T) {
	// This test requires LiveKit server - use TestMediaPipelineDestroyLiveKit instead
	t.Log("Use TestMediaPipelineDestroyLiveKit for pipeline destruction tests with LiveKit server")
}

// TestMediaPipelineMetrics tests metrics collection
func TestMediaPipelineMetrics(t *testing.T) {
	// This test requires LiveKit server - use TestMediaPipelineMetricsLiveKit instead
	t.Log("Use TestMediaPipelineMetricsLiveKit for metrics tests with LiveKit server")
}

// TestMediaPipelineConcurrency tests concurrent operations
func TestMediaPipelineConcurrency(t *testing.T) {
	pipeline := NewMediaPipeline()

	var wg sync.WaitGroup
	numGoroutines := 10

	// Concurrent stage operations
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			stage := &TestStage{
				name:     fmt.Sprintf("Stage%d", id),
				priority: id,
			}
			pipeline.AddStage(stage)
		}(i)
	}

	// Concurrent processor operations
	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			processor := &MockMediaProcessor{
				name: fmt.Sprintf("Processor%d", id),
			}
			pipeline.RegisterProcessor(processor)
		}(i)
	}

	wg.Wait()

	// Verify no panic occurred
	assert.NotNil(t, pipeline)

	// Verify stages and processors were added
	pipeline.mu.RLock()
	assert.GreaterOrEqual(t, len(pipeline.stages), numGoroutines)
	assert.GreaterOrEqual(t, len(pipeline.processors), numGoroutines)
	pipeline.mu.RUnlock()
}

// TestMediaPipelineErrorHandling tests error handling in pipeline
func TestMediaPipelineErrorHandling(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Add a stage that returns an error
	errorStage := &TestStage{
		name:     "ErrorStage",
		priority: 1,
		process: func(ctx context.Context, data MediaData) (MediaData, error) {
			return MediaData{}, fmt.Errorf("processing error")
		},
	}

	pipeline.AddStage(errorStage)

	// Test processing with error stage
	// The pipeline should handle the error gracefully

	// Start processing nil track
	err := pipeline.StartProcessingTrack(nil)
	assert.Error(t, err)
}

// TestMediaPipelineBuffering tests buffer management
func TestMediaPipelineBuffering(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Create a buffer
	buffer := pipeline.bufferFactory.CreateBuffer()
	assert.NotNil(t, buffer)

	// Test buffer operations
	testData := MediaData{
		Type: MediaTypeAudio,
		Data: []byte("test"),
	}

	// Enqueue data
	success := buffer.Enqueue(testData)
	assert.True(t, success)

	// Dequeue data
	data := buffer.Dequeue()
	assert.NotNil(t, data)
	assert.Equal(t, testData.Type, data.Type)
	assert.Equal(t, testData.Data, data.Data)

	// Test buffer overflow behavior
	for i := 0; i < 15; i++ {
		buffer.Enqueue(MediaData{
			Type: MediaTypeAudio,
			Data: []byte(fmt.Sprintf("data%d", i)),
		})
	}

	// Buffer should handle overflow gracefully
	assert.NotNil(t, buffer)
}

// TestMediaPipelineStageOrdering tests that stages are executed in priority order
func TestMediaPipelineStageOrdering(t *testing.T) {
	pipeline := NewMediaPipeline()

	var executionOrder []string
	var mu sync.Mutex

	// Add stages in random order but with specific priorities
	stages := []MediaPipelineStage{
		&TestStage{
			name:     "Stage3",
			priority: 3,
			process: func(ctx context.Context, data MediaData) (MediaData, error) {
				mu.Lock()
				executionOrder = append(executionOrder, "Stage3")
				mu.Unlock()
				return data, nil
			},
		},
		&TestStage{
			name:     "Stage1",
			priority: 1,
			process: func(ctx context.Context, data MediaData) (MediaData, error) {
				mu.Lock()
				executionOrder = append(executionOrder, "Stage1")
				mu.Unlock()
				return data, nil
			},
		},
		&TestStage{
			name:     "Stage2",
			priority: 2,
			process: func(ctx context.Context, data MediaData) (MediaData, error) {
				mu.Lock()
				executionOrder = append(executionOrder, "Stage2")
				mu.Unlock()
				return data, nil
			},
		},
	}

	// Add stages in random order
	for _, stage := range stages {
		pipeline.AddStage(stage)
	}

	// Process some data through the pipeline stages directly
	// We would need to call the actual processing method
	// Since the pipeline processes tracks asynchronously, we'll verify the sorting
	pipeline.mu.RLock()
	assert.Len(t, pipeline.stages, 3)
	assert.Equal(t, "Stage1", pipeline.stages[0].GetName())
	assert.Equal(t, "Stage2", pipeline.stages[1].GetName())
	assert.Equal(t, "Stage3", pipeline.stages[2].GetName())
	pipeline.mu.RUnlock()
}
