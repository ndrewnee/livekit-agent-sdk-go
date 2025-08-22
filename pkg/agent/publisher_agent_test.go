package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// MockPublisherAgentHandler implements PublisherAgentHandler for testing
type MockPublisherAgentHandler struct {
	SimpleJobHandler
	trackPublished       int
	trackUnpublished     int
	trackSubscribed      int
	trackUnsubscribed    int
	qualityChanged       int
	connectionQualityChanged int
	
	lastParticipant    *lksdk.RemoteParticipant
	lastPublication    *lksdk.RemoteTrackPublication
	lastTrack          *webrtc.TrackRemote
	lastOldQuality     livekit.VideoQuality
	lastNewQuality     livekit.VideoQuality
	lastConnQuality    livekit.ConnectionQuality
}

func NewMockPublisherAgentHandler() *MockPublisherAgentHandler {
	h := &MockPublisherAgentHandler{}
	// Set up default behavior
	h.OnJob = func(ctx context.Context, job *livekit.Job, room *lksdk.Room) error {
		return nil
	}
	return h
}

func (h *MockPublisherAgentHandler) OnPublisherTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	h.trackPublished++
	h.lastParticipant = participant
	h.lastPublication = publication
}

func (h *MockPublisherAgentHandler) OnPublisherTrackUnpublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	h.trackUnpublished++
	h.lastParticipant = participant
	h.lastPublication = publication
}

func (h *MockPublisherAgentHandler) OnPublisherTrackSubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication, track *webrtc.TrackRemote) {
	h.trackSubscribed++
	h.lastParticipant = participant
	h.lastPublication = publication
	h.lastTrack = track
}

func (h *MockPublisherAgentHandler) OnPublisherTrackUnsubscribed(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote) {
	h.trackUnsubscribed++
	h.lastParticipant = participant
	h.lastTrack = track
}

func (h *MockPublisherAgentHandler) OnPublisherQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, track *webrtc.TrackRemote, oldQuality, newQuality livekit.VideoQuality) {
	h.qualityChanged++
	h.lastParticipant = participant
	h.lastTrack = track
	h.lastOldQuality = oldQuality
	h.lastNewQuality = newQuality
}

func (h *MockPublisherAgentHandler) OnPublisherConnectionQualityChanged(ctx context.Context, participant *lksdk.RemoteParticipant, quality livekit.ConnectionQuality) {
	h.connectionQualityChanged++
	h.lastParticipant = participant
	h.lastConnQuality = quality
}

func TestPublisherAgent_Creation(t *testing.T) {
	handler := NewMockPublisherAgentHandler()
	
	opts := WorkerOptions{}
	
	agent, err := NewPublisherAgent("ws://localhost:7880", "test-key", "test-secret", handler, opts)
	require.NoError(t, err)
	require.NotNil(t, agent)
	
	// Verify job type is set correctly
	require.Equal(t, livekit.JobType_JT_PUBLISHER, agent.Worker.opts.JobType)
	
	// Verify components are initialized
	require.NotNil(t, agent.qualityController)
	require.NotNil(t, agent.connectionMonitor)
	require.NotNil(t, agent.subscriptionManager)
	require.NotNil(t, agent.subscribedTracks)
}

func TestPublisherAgent_TrackSubscriptionManager(t *testing.T) {
	manager := NewTrackSubscriptionManager()
	
	// Test default settings
	require.True(t, manager.autoSubscribe)
	require.True(t, manager.subscribeAudio)
	require.True(t, manager.subscribeVideo)
	
	// Test disabling auto-subscribe
	manager.SetAutoSubscribe(false)
	mockPub := &lksdk.RemoteTrackPublication{}
	require.False(t, manager.ShouldAutoSubscribe(mockPub))
	
	// Test source priorities
	require.Equal(t, 100, manager.GetSourcePriority(livekit.TrackSource_CAMERA))
	require.Equal(t, 90, manager.GetSourcePriority(livekit.TrackSource_SCREEN_SHARE))
	
	// Test custom priority
	manager.SetSourcePriority(livekit.TrackSource_UNKNOWN, 25)
	require.Equal(t, 25, manager.GetSourcePriority(livekit.TrackSource_UNKNOWN))
	
	// Test filters
	filterCalled := false
	manager.AddFilter(func(publication *lksdk.RemoteTrackPublication) bool {
		filterCalled = true
		return false
	})
	
	manager.SetAutoSubscribe(true)
	require.False(t, manager.ShouldAutoSubscribe(mockPub))
	require.True(t, filterCalled)
	
	// Test clear filters
	manager.ClearFilters()
	require.True(t, manager.ShouldAutoSubscribe(mockPub))
}

func TestConnectionQualityMonitor(t *testing.T) {
	monitor := NewConnectionQualityMonitor()
	
	// Test initial state
	require.Equal(t, livekit.ConnectionQuality_GOOD, monitor.GetCurrentQuality())
	
	// Test quality updates
	monitor.UpdateQuality(livekit.ConnectionQuality_EXCELLENT)
	require.Equal(t, livekit.ConnectionQuality_EXCELLENT, monitor.GetCurrentQuality())
	
	monitor.UpdateQuality(livekit.ConnectionQuality_POOR)
	require.Equal(t, livekit.ConnectionQuality_POOR, monitor.GetCurrentQuality())
	
	// Test quality history
	history := monitor.GetQualityHistory()
	require.Len(t, history, 2)
	require.Equal(t, livekit.ConnectionQuality_EXCELLENT, history[0].Quality)
	require.Equal(t, livekit.ConnectionQuality_POOR, history[1].Quality)
	
	// Test average quality
	time.Sleep(10 * time.Millisecond)
	monitor.UpdateQuality(livekit.ConnectionQuality_GOOD)
	avgQuality := monitor.GetAverageQuality(1 * time.Second)
	// Should average to GOOD (2+0+1)/3 = 1
	require.Equal(t, livekit.ConnectionQuality_GOOD, avgQuality)
	
	// Test stability - with mixed quality in recent history
	// Since we just added EXCELLENT, POOR, and GOOD within a short time,
	// it should be unstable when looking at recent history
	require.False(t, monitor.IsStable(100*time.Millisecond))
	
	// Make it stable - wait a bit to ensure old measurements are outside the window
	time.Sleep(110 * time.Millisecond)
	monitor.UpdateQuality(livekit.ConnectionQuality_GOOD)
	monitor.UpdateQuality(livekit.ConnectionQuality_GOOD)
	require.True(t, monitor.IsStable(100*time.Millisecond))
	
	// Test callback
	callbackCalled := false
	var oldQ, newQ livekit.ConnectionQuality
	monitor.SetQualityChangeCallback(func(old, new livekit.ConnectionQuality) {
		callbackCalled = true
		oldQ = old
		newQ = new
	})
	
	monitor.UpdateQuality(livekit.ConnectionQuality_EXCELLENT)
	require.True(t, callbackCalled)
	require.Equal(t, livekit.ConnectionQuality_GOOD, oldQ)
	require.Equal(t, livekit.ConnectionQuality_EXCELLENT, newQ)
}

func TestQualityController(t *testing.T) {
	controller := NewQualityController()
	
	// Test default policy
	policy := controller.adaptationPolicy
	require.Equal(t, 0.02, policy.LossThresholdUp)
	require.Equal(t, 0.01, policy.LossThresholdDown)
	require.Equal(t, livekit.VideoQuality_HIGH, policy.MaxQuality)
	require.Equal(t, livekit.VideoQuality_LOW, policy.MinQuality)
	
	// Test custom policy
	customPolicy := QualityAdaptationPolicy{
		LossThresholdUp:   0.05,
		LossThresholdDown: 0.02,
		MaxQuality:        livekit.VideoQuality_MEDIUM,
		MinQuality:        livekit.VideoQuality_LOW,
	}
	controller.SetAdaptationPolicy(customPolicy)
	
	// Test quality calculations
	subscription := &PublisherTrackSubscription{
		CurrentQuality: livekit.VideoQuality_HIGH,
	}
	
	// Excellent connection should give max quality
	quality := controller.CalculateOptimalQuality(livekit.ConnectionQuality_EXCELLENT, subscription)
	require.Equal(t, livekit.VideoQuality_MEDIUM, quality) // Limited by MaxQuality
	
	// Poor connection should give min quality
	quality = controller.CalculateOptimalQuality(livekit.ConnectionQuality_POOR, subscription)
	require.Equal(t, livekit.VideoQuality_LOW, quality)
	
	// Test quality level changes
	require.Equal(t, livekit.VideoQuality_MEDIUM, controller.decreaseQuality(livekit.VideoQuality_HIGH))
	require.Equal(t, livekit.VideoQuality_LOW, controller.decreaseQuality(livekit.VideoQuality_MEDIUM))
	require.Equal(t, livekit.VideoQuality_LOW, controller.decreaseQuality(livekit.VideoQuality_LOW))
	
	require.Equal(t, livekit.VideoQuality_MEDIUM, controller.increaseQuality(livekit.VideoQuality_LOW))
	require.Equal(t, livekit.VideoQuality_HIGH, controller.increaseQuality(livekit.VideoQuality_MEDIUM))
	require.Equal(t, livekit.VideoQuality_HIGH, controller.increaseQuality(livekit.VideoQuality_HIGH))
}

func TestMediaPipeline(t *testing.T) {
	pipeline := NewMediaPipeline()
	
	// Test adding stages
	stage1 := NewPassthroughStage("stage1", 10)
	stage2 := NewPassthroughStage("stage2", 5)
	stage3 := NewPassthroughStage("stage3", 15)
	
	pipeline.AddStage(stage1)
	pipeline.AddStage(stage2)
	pipeline.AddStage(stage3)
	
	// Verify stages are sorted by priority
	require.Len(t, pipeline.stages, 3)
	require.Equal(t, "stage2", pipeline.stages[0].GetName()) // Priority 5
	require.Equal(t, "stage1", pipeline.stages[1].GetName()) // Priority 10
	require.Equal(t, "stage3", pipeline.stages[2].GetName()) // Priority 15
	
	// Test removing stage
	pipeline.RemoveStage("stage1")
	require.Len(t, pipeline.stages, 2)
	
	// Test processor registration
	mockProcessor := &MockMediaProcessor{name: "test-processor"}
	pipeline.RegisterProcessor(mockProcessor)
	require.Contains(t, pipeline.processors, "test-processor")
}

func TestMediaBuffer(t *testing.T) {
	factory := NewMediaBufferFactory(10, 100)
	buffer := factory.CreateBuffer()
	
	// Test empty buffer
	require.Equal(t, 0, buffer.Size())
	require.Nil(t, buffer.Dequeue())
	
	// Test enqueue
	data1 := MediaData{Type: MediaTypeAudio, TrackID: "track1"}
	data2 := MediaData{Type: MediaTypeVideo, TrackID: "track2"}
	
	require.True(t, buffer.Enqueue(data1))
	require.True(t, buffer.Enqueue(data2))
	require.Equal(t, 2, buffer.Size())
	
	// Test dequeue
	dequeued := buffer.Dequeue()
	require.NotNil(t, dequeued)
	require.Equal(t, "track1", dequeued.TrackID)
	require.Equal(t, 1, buffer.Size())
	
	// Test buffer overflow (dropOldest = true)
	for i := 0; i < 110; i++ {
		data := MediaData{Type: MediaTypeAudio, TrackID: string(rune(i))}
		buffer.Enqueue(data)
	}
	require.Equal(t, 100, buffer.Size()) // Max size enforced
	
	// Test clear
	buffer.Clear()
	require.Equal(t, 0, buffer.Size())
}

func TestMediaMetricsCollector(t *testing.T) {
	collector := NewMediaMetricsCollector()
	
	// Test recording metrics
	collector.RecordProcessing("track1", true, 10*time.Millisecond)
	collector.RecordProcessing("track1", true, 15*time.Millisecond)
	collector.RecordProcessing("track1", false, 20*time.Millisecond)
	
	// Test getting metrics
	stats, exists := collector.GetMetrics("track1")
	require.True(t, exists)
	require.Equal(t, uint64(2), stats.FramesProcessed)
	require.Equal(t, uint64(1), stats.FramesDropped)
	require.Equal(t, uint64(1), stats.Errors)
	require.InDelta(t, 20.0, stats.ProcessingTimeMs, 1.0)
	
	// Test getting all metrics
	collector.RecordProcessing("track2", true, 5*time.Millisecond)
	allMetrics := collector.GetAllMetrics()
	require.Len(t, allMetrics, 2)
	require.Contains(t, allMetrics, "track1")
	require.Contains(t, allMetrics, "track2")
}

func TestPublisherAgent_TrackOperations(t *testing.T) {
	handler := NewMockPublisherAgentHandler()
	
	opts := WorkerOptions{}
	
	agent, err := NewPublisherAgent("ws://localhost:7880", "test-key", "test-secret", handler, opts)
	require.NoError(t, err)
	
	// Test manual subscription (would fail without real track)
	err = agent.SubscribeToTrack(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "publication cannot be nil")
	
	// Test track quality setting without subscription
	err = agent.SetTrackQuality("non-existent", livekit.VideoQuality_HIGH)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found in subscriptions")
	
	// Test dimension setting
	err = agent.SetTrackDimensions("non-existent", 1920, 1080)
	require.Error(t, err)
	
	// Test frame rate setting
	err = agent.SetTrackFrameRate("non-existent", 30.0)
	require.Error(t, err)
	
	// Test enable/disable track
	err = agent.EnableTrack("non-existent", false)
	require.Error(t, err)
}

// MockMediaProcessor implements MediaProcessor for testing
type MockMediaProcessor struct {
	name         string
	audioCalled  int
	videoCalled  int
	lastAudioLen int
	lastVideoLen int
}

func (m *MockMediaProcessor) ProcessAudio(ctx context.Context, samples []byte, sampleRate uint32, channels uint8) ([]byte, error) {
	m.audioCalled++
	m.lastAudioLen = len(samples)
	return samples, nil
}

func (m *MockMediaProcessor) ProcessVideo(ctx context.Context, frame []byte, width, height uint32, format VideoFormat) ([]byte, error) {
	m.videoCalled++
	m.lastVideoLen = len(frame)
	return frame, nil
}

func (m *MockMediaProcessor) GetName() string {
	return m.name
}

func (m *MockMediaProcessor) GetCapabilities() ProcessorCapabilities {
	return ProcessorCapabilities{
		SupportedMediaTypes: []MediaType{MediaTypeAudio, MediaTypeVideo},
		MaxConcurrency:      4,
		RequiresGPU:         false,
	}
}

func TestPublisherAgent_Cleanup(t *testing.T) {
	handler := NewMockPublisherAgentHandler()
	
	opts := WorkerOptions{}
	
	agent, err := NewPublisherAgent("ws://localhost:7880", "test-key", "test-secret", handler, opts)
	require.NoError(t, err)
	
	// Test cleanup
	agent.cleanupAllSubscriptions()
	require.Empty(t, agent.subscribedTracks)
	
	// Test stop
	err = agent.Stop()
	require.NoError(t, err)
}