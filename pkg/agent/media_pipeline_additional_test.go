package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Additional unit tests to raise media pipeline coverage

// FuncStage is a test helper implementing MediaPipelineStage with pluggable behavior.
type FuncStage struct {
	name     string
	priority int
	can      func(MediaType) bool
	fn       func(context.Context, MediaData) (MediaData, error)
}

func (fs *FuncStage) GetName() string  { return fs.name }
func (fs *FuncStage) GetPriority() int { return fs.priority }
func (fs *FuncStage) CanProcess(mt MediaType) bool {
	if fs.can != nil {
		return fs.can(mt)
	}
	return true
}
func (fs *FuncStage) Process(ctx context.Context, d MediaData) (MediaData, error) {
	if fs.fn != nil {
		return fs.fn(ctx, d)
	}
	return d, nil
}

// TestProcessThroughStages_SuccessAndSkip covers successful stage processing and CanProcess skip.
func TestProcessThroughStages_SuccessAndSkip(t *testing.T) {
	mp := NewMediaPipeline()

	// Stage that skips audio
	skipStage := &FuncStage{
		name:     "SkipAudio",
		priority: 1,
		can:      func(mt MediaType) bool { return mt != MediaTypeAudio },
		fn:       func(ctx context.Context, data MediaData) (MediaData, error) { return data, nil },
	}

	// Stage that marks data as processed
	markStage := &FuncStage{
		name:     "Mark",
		priority: 2,
		fn: func(ctx context.Context, data MediaData) (MediaData, error) {
			if data.Metadata == nil {
				data.Metadata = map[string]interface{}{}
			}
			data.Metadata["marked"] = true
			return data, nil
		},
	}

	mp.AddStage(skipStage)
	mp.AddStage(markStage)

	tp := &MediaTrackPipeline{Pipeline: mp}

	input := MediaData{Type: MediaTypeAudio, Data: []byte("x")}
	out, err := tp.processThroughStages(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, out)
	// Skip stage should not run for audio, but markStage should
	assert.Equal(t, true, out.Metadata["marked"]) // processed by markStage
}

// TestProcessThroughStages_Error covers error propagation with stage name context.
func TestProcessThroughStages_Error(t *testing.T) {
	mp := NewMediaPipeline()
	errStage := &TestStage{
		name:     "ErrorStage",
		priority: 1,
		process: func(ctx context.Context, data MediaData) (MediaData, error) {
			return MediaData{}, fmt.Errorf("boom")
		},
	}
	mp.AddStage(errStage)

	tp := &MediaTrackPipeline{Pipeline: mp}
	_, err := tp.processThroughStages(context.Background(), MediaData{Type: MediaTypeAudio})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ErrorStage")
}

// TestUpdateStats covers success and error cases of stats update.
func TestUpdateStats(t *testing.T) {
	tp := &MediaTrackPipeline{ProcessingStats: &MediaProcessingStats{}}

	tp.updateStats(false, 5*time.Millisecond)
	tp.ProcessingStats.mu.RLock()
	processed := tp.ProcessingStats.FramesProcessed
	last := tp.ProcessingStats.LastProcessedAt
	pt := tp.ProcessingStats.ProcessingTimeMs
	tp.ProcessingStats.mu.RUnlock()
	assert.Equal(t, uint64(1), processed)
	assert.True(t, pt >= 0)
	assert.False(t, last.IsZero())

	tp.updateStats(true, 7*time.Millisecond)
	tp.ProcessingStats.mu.RLock()
	dropped := tp.ProcessingStats.FramesDropped
	errors := tp.ProcessingStats.Errors
	tp.ProcessingStats.mu.RUnlock()
	assert.Equal(t, uint64(1), dropped)
	assert.Equal(t, uint64(1), errors)
}

// TestGetOutputBuffer covers both present and missing track cases.
func TestGetOutputBuffer(t *testing.T) {
	mp := NewMediaPipeline()
	// Manually insert a track pipeline
	buf := mp.bufferFactory.CreateBuffer()
	tp := &MediaTrackPipeline{TrackID: "t1", OutputBuffer: buf}
	mp.mu.Lock()
	mp.tracks["t1"] = tp
	mp.mu.Unlock()

	got, ok := mp.GetOutputBuffer("t1")
	assert.True(t, ok)
	assert.Equal(t, buf, got)

	got, ok = mp.GetOutputBuffer("missing")
	assert.False(t, ok)
	assert.Nil(t, got)
}

// TestMediaBufferOverflowBehavior covers dropOldest=true and false paths.
func TestMediaBufferOverflowBehavior(t *testing.T) {
	// dropOldest = true
	mb := &MediaBuffer{maxSize: 2, dropOldest: true}
	assert.True(t, mb.Enqueue(MediaData{Data: []byte("a")}))
	assert.True(t, mb.Enqueue(MediaData{Data: []byte("b")}))
	// This should drop "a"
	assert.True(t, mb.Enqueue(MediaData{Data: []byte("c")}))
	assert.Equal(t, 2, mb.Size())
	d1 := mb.Dequeue()
	require.NotNil(t, d1)
	assert.Equal(t, []byte("b"), d1.Data)
	d2 := mb.Dequeue()
	require.NotNil(t, d2)
	assert.Equal(t, []byte("c"), d2.Data)

	// dropOldest = false
	mb2 := &MediaBuffer{maxSize: 1, dropOldest: false}
	assert.True(t, mb2.Enqueue(MediaData{Data: []byte("x")}))
	assert.False(t, mb2.Enqueue(MediaData{Data: []byte("y")})) // should be rejected
	assert.Equal(t, 1, mb2.Size())
}

// TestProcessLoopSuccess ensures processed data is queued to output buffer.
func TestProcessLoopSuccess(t *testing.T) {
	mp := NewMediaPipeline()
	stage := &TestStage{
		name:     "Mark",
		priority: 1,
		process: func(ctx context.Context, data MediaData) (MediaData, error) {
			if data.Metadata == nil {
				data.Metadata = map[string]interface{}{}
			}
			data.Metadata["ok"] = true
			return data, nil
		},
	}
	mp.AddStage(stage)

	tp := &MediaTrackPipeline{
		TrackID:         "track-1",
		Pipeline:        mp,
		InputBuffer:     mp.bufferFactory.CreateBuffer(),
		OutputBuffer:    mp.bufferFactory.CreateBuffer(),
		ProcessingStats: &MediaProcessingStats{},
		Active:          true,
	}

	// Seed one item
	tp.InputBuffer.Enqueue(MediaData{Type: MediaTypeAudio, Data: []byte("in")})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	tp.wg.Add(1)
	go func() {
		tp.processLoop(ctx)
		close(done)
	}()

	// Wait for processing to occur
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tp.OutputBuffer.Size() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	out := tp.OutputBuffer.Dequeue()
	require.NotNil(t, out)
	assert.Equal(t, true, out.Metadata["ok"])
}

// TestProcessLoopError ensures error path updates stats and does not enqueue output.
func TestProcessLoopError(t *testing.T) {
	mp := NewMediaPipeline()
	stage := &TestStage{
		name:     "Err",
		priority: 1,
		process:  func(ctx context.Context, data MediaData) (MediaData, error) { return MediaData{}, fmt.Errorf("fail") },
	}
	mp.AddStage(stage)

	tp := &MediaTrackPipeline{
		TrackID:         "track-err",
		Pipeline:        mp,
		InputBuffer:     mp.bufferFactory.CreateBuffer(),
		OutputBuffer:    mp.bufferFactory.CreateBuffer(),
		ProcessingStats: &MediaProcessingStats{},
		Active:          true,
	}

	tp.InputBuffer.Enqueue(MediaData{Type: MediaTypeAudio, Data: []byte("in")})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	tp.wg.Add(1)
	go func() {
		tp.processLoop(ctx)
		close(done)
	}()

	// Let one iteration run
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	assert.Equal(t, 0, tp.OutputBuffer.Size())
	tp.ProcessingStats.mu.RLock()
	errs := tp.ProcessingStats.Errors
	dropped := tp.ProcessingStats.FramesDropped
	tp.ProcessingStats.mu.RUnlock()
	assert.GreaterOrEqual(t, errs, uint64(1))
	assert.GreaterOrEqual(t, dropped, uint64(1))
}

// TestMediaMetricsCollector_NoMetrics ensures GetMetrics false path is covered.
func TestMediaMetricsCollector_NoMetrics(t *testing.T) {
	mmc := NewMediaMetricsCollector()
	stats, ok := mmc.GetMetrics("unknown")
	assert.False(t, ok)
	assert.Nil(t, stats)

	// After recording, it should exist
	mmc.RecordProcessing("t1", true, time.Millisecond)
	stats, ok = mmc.GetMetrics("t1")
	assert.True(t, ok)
	assert.NotNil(t, stats)
}
