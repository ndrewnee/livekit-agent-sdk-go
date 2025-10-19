package agent

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests that don't require WebRTC connectivity

// TestMediaPipelineBasicOperations tests basic pipeline operations without WebRTC
func TestMediaPipelineBasicOperations(t *testing.T) {
	pipeline := NewMediaPipeline()
	require.NotNil(t, pipeline)

	// Test stage management
	stage1 := &TestStage{name: "stage1", priority: 1}
	stage2 := &TestStage{name: "stage2", priority: 2}

	pipeline.AddStage(stage1)
	pipeline.AddStage(stage2)

	pipeline.mu.RLock()
	assert.Len(t, pipeline.stages, 2)
	assert.Equal(t, "stage1", pipeline.stages[0].GetName())
	assert.Equal(t, "stage2", pipeline.stages[1].GetName())
	pipeline.mu.RUnlock()

	// Test processor registration
	processor := &MockMediaProcessor{name: "processor1"}
	pipeline.RegisterProcessor(processor)

	pipeline.mu.RLock()
	assert.Len(t, pipeline.processors, 1)
	pipeline.mu.RUnlock()

	// Test stage removal
	pipeline.RemoveStage("stage1")

	pipeline.mu.RLock()
	foundStage1 := false
	for _, s := range pipeline.stages {
		if s.GetName() == "stage1" {
			foundStage1 = true
			break
		}
	}
	assert.False(t, foundStage1)
	pipeline.mu.RUnlock()
}

// TestMediaBufferOperations tests buffer operations
func TestMediaBufferOperations(t *testing.T) {
	factory := NewMediaBufferFactory(10, 100)
	buffer := factory.CreateBuffer()

	// Test enqueue/dequeue
	data1 := MediaData{Type: MediaTypeAudio, Data: []byte("data1")}
	data2 := MediaData{Type: MediaTypeVideo, Data: []byte("data2")}

	assert.True(t, buffer.Enqueue(data1))
	assert.True(t, buffer.Enqueue(data2))
	assert.Equal(t, 2, buffer.Size())

	dequeued := buffer.Dequeue()
	assert.NotNil(t, dequeued)
	assert.Equal(t, MediaTypeAudio, dequeued.Type)
	assert.Equal(t, []byte("data1"), dequeued.Data)

	assert.Equal(t, 1, buffer.Size())

	// Test clear
	buffer.Clear()
	assert.Equal(t, 0, buffer.Size())
	assert.Nil(t, buffer.Dequeue())
}

// TestProcessorCapabilities tests processor capabilities
func TestProcessorCapabilities(t *testing.T) {
	processor := &MockMediaProcessor{
		name: "test-processor",
		capabilities: ProcessorCapabilities{
			SupportedMediaTypes: []MediaType{MediaTypeAudio, MediaTypeVideo},
			SupportedFormats: []MediaFormat{
				{SampleRate: 48000, Channels: 2},
				{Width: 1920, Height: 1080, FrameRate: 30},
			},
			MaxConcurrency: 4,
		},
	}

	assert.Equal(t, "test-processor", processor.GetName())
	caps := processor.GetCapabilities()
	assert.Len(t, caps.SupportedMediaTypes, 2)
	assert.Len(t, caps.SupportedFormats, 2)
	assert.Equal(t, 4, caps.MaxConcurrency)

	// Test processing
	ctx := context.Background()
	audioData := []byte("audio")
	processedAudio, err := processor.ProcessAudio(ctx, audioData, 48000, 2)
	assert.NoError(t, err)
	assert.Equal(t, audioData, processedAudio)

	videoData := []byte("video")
	processedVideo, err := processor.ProcessVideo(ctx, videoData, 1920, 1080, VideoFormatI420)
	assert.NoError(t, err)
	assert.Equal(t, videoData, processedVideo)
}

// TestConcurrentBufferOperations tests concurrent buffer operations
func TestConcurrentBufferOperations(t *testing.T) {
	factory := NewMediaBufferFactory(100, 1000)
	buffer := factory.CreateBuffer()

	var wg sync.WaitGroup
	numProducers := 10
	numConsumers := 5
	itemsPerProducer := 20

	// Producers
	for i := 0; i < numProducers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < itemsPerProducer; j++ {
				data := MediaData{
					Type: MediaTypeAudio,
					Data: []byte(fmt.Sprintf("producer-%d-item-%d", id, j)),
				}
				buffer.Enqueue(data)
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	// Consumers
	consumedCount := make([]int64, numConsumers)
	for i := 0; i < numConsumers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				data := buffer.Dequeue()
				if data != nil {
					atomic.AddInt64(&consumedCount[id], 1)
				}
				time.Sleep(time.Microsecond * 2)

				// Check if we've consumed enough
				var totalConsumed int64
				for i := 0; i < numConsumers; i++ {
					totalConsumed += atomic.LoadInt64(&consumedCount[i])
				}
				if totalConsumed >= int64(numProducers*itemsPerProducer/2) {
					break
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify some items were consumed
	var totalConsumed int64
	for i := 0; i < numConsumers; i++ {
		totalConsumed += atomic.LoadInt64(&consumedCount[i])
	}
	assert.Greater(t, totalConsumed, int64(0))
}

// TestPipelineStagePriority tests that stages execute in priority order
func TestPipelineStagePriority(t *testing.T) {
	pipeline := NewMediaPipeline()

	// Add stages in reverse priority order
	for i := 5; i > 0; i-- {
		stage := &TestStage{
			name:     fmt.Sprintf("stage-%d", i),
			priority: i,
		}
		pipeline.AddStage(stage)
	}

	// Check they're sorted by priority
	pipeline.mu.RLock()
	defer pipeline.mu.RUnlock()

	assert.Len(t, pipeline.stages, 5)
	for i := 0; i < 5; i++ {
		assert.Equal(t, fmt.Sprintf("stage-%d", i+1), pipeline.stages[i].GetName())
		assert.Equal(t, i+1, pipeline.stages[i].GetPriority())
	}
}

// TestMediaDataTypes tests media data type handling
func TestMediaDataTypes(t *testing.T) {
	audioData := MediaData{
		Type: MediaTypeAudio,
		Data: []byte("audio samples"),
		Metadata: map[string]interface{}{
			"sampleRate": 48000,
			"channels":   2,
		},
	}

	assert.Equal(t, MediaTypeAudio, audioData.Type)
	assert.NotNil(t, audioData.Metadata)
	assert.Equal(t, 48000, audioData.Metadata["sampleRate"])

	videoData := MediaData{
		Type: MediaTypeVideo,
		Data: []byte("video frame"),
		Format: MediaFormat{
			Width:     1920,
			Height:    1080,
			FrameRate: 30,
		},
	}

	assert.Equal(t, MediaTypeVideo, videoData.Type)
	assert.Equal(t, uint32(1920), videoData.Format.Width)
	assert.Equal(t, uint32(1080), videoData.Format.Height)
}

// TestPassthroughStage tests the built-in passthrough stage
func TestPassthroughStage(t *testing.T) {
	stage := NewPassthroughStage("passthrough", 1)

	assert.Equal(t, "passthrough", stage.GetName())
	assert.Equal(t, 1, stage.GetPriority())
	assert.True(t, stage.CanProcess(MediaTypeAudio))
	assert.True(t, stage.CanProcess(MediaTypeVideo))

	ctx := context.Background()
	inputData := MediaData{
		Type: MediaTypeAudio,
		Data: []byte("test"),
	}

	outputData, err := stage.Process(ctx, inputData)
	assert.NoError(t, err)
	assert.Equal(t, inputData, outputData)
}

// TestTranscodingStage tests the transcoding stage
func TestTranscodingStage(t *testing.T) {
	targetFormat := MediaFormat{
		SampleRate: 44100,
		Channels:   2,
	}
	stage := NewTranscodingStage("transcoder", 2, targetFormat)

	assert.Equal(t, "transcoder", stage.GetName())
	assert.Equal(t, 2, stage.GetPriority())
	assert.True(t, stage.CanProcess(MediaTypeAudio))
	assert.True(t, stage.CanProcess(MediaTypeVideo))

	ctx := context.Background()
	inputData := MediaData{
		Type: MediaTypeAudio,
		Data: []byte("audio"),
		Format: MediaFormat{
			SampleRate: 48000,
			Channels:   2,
		},
	}

	outputData, err := stage.Process(ctx, inputData)
	assert.NoError(t, err)
	assert.Equal(t, MediaTypeAudio, outputData.Type)
	// The transcoding stage sets the target format
	assert.Equal(t, uint32(44100), outputData.Format.SampleRate)
}
