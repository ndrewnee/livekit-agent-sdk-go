package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Test MediaPipeline
func TestMediaPipelineOperations(t *testing.T) {
	pipeline := NewMediaPipeline()
	require.NotNil(t, pipeline)

	// Check initial state
	pipeline.mu.RLock()
	assert.NotNil(t, pipeline.stages)
	assert.NotNil(t, pipeline.tracks)
	assert.NotNil(t, pipeline.processors)
	pipeline.mu.RUnlock()
}

// Test MediaBufferFactory
func TestMediaBufferFactory(t *testing.T) {
	factory := NewMediaBufferFactory(1024, 4096)
	require.NotNil(t, factory)

	// Check factory is initialized
	assert.NotNil(t, factory)
}

// Test MediaMetricsCollector
func TestMediaMetricsCollector(t *testing.T) {
	collector := NewMediaMetricsCollector()
	require.NotNil(t, collector)

	// Test RecordProcessing
	collector.RecordProcessing("track-1", true, 10*time.Millisecond)
	collector.RecordProcessing("track-1", false, 5*time.Millisecond)

	// Test GetMetrics
	metrics, exists := collector.GetMetrics("track-1")
	assert.True(t, exists)
	assert.NotNil(t, metrics)

	// Test GetAllMetrics
	allMetrics := collector.GetAllMetrics()
	assert.NotNil(t, allMetrics)
	assert.Contains(t, allMetrics, "track-1")
}

// Test TimingManager
func TestTimingManagerOperations(t *testing.T) {
	logger := zap.NewNop()
	opts := TimingManagerOptions{
		MaxSkewSamples:     5,
		SkewThreshold:      1 * time.Second,
		BackpressureWindow: 100 * time.Millisecond,
		BackpressureLimit:  10,
	}

	tm := NewTimingManager(logger, opts)
	require.NotNil(t, tm)

	// Test UpdateServerTime
	now := time.Now()
	serverTime := now.Add(500 * time.Millisecond)
	tm.UpdateServerTime(serverTime, now)

	// Test ServerTimeNow
	adjustedTime := tm.ServerTimeNow()
	assert.NotNil(t, adjustedTime)

	// Test SetDeadline
	tm.SetDeadline("job-1", time.Now().Add(5*time.Second), "test")

	// Test GetDeadline
	deadline, exists := tm.GetDeadline("job-1")
	assert.True(t, exists)
	assert.NotNil(t, deadline)

	// Test CheckDeadline
	exceeded, remaining := tm.CheckDeadline("job-1")
	assert.False(t, exceeded)
	assert.Greater(t, remaining, time.Duration(0))

	// Test RemoveDeadline
	tm.RemoveDeadline("job-1")
	_, exists = tm.GetDeadline("job-1")
	assert.False(t, exists)

	// Test PropagateDeadline
	tm.SetDeadline("job-2", time.Now().Add(1*time.Second), "test")
	ctx := context.Background()
	ctxWithDeadline, cancel := tm.PropagateDeadline(ctx, "job-2")
	defer cancel()

	dl, ok := ctxWithDeadline.Deadline()
	assert.True(t, ok)
	assert.NotNil(t, dl)

	// Test RecordEvent
	for i := 0; i < 5; i++ {
		tm.RecordEvent()
	}

	// Test CheckBackpressure
	shouldApply := tm.CheckBackpressure()
	assert.False(t, shouldApply) // Below limit

	// Test GetBackpressureDelay
	delay := tm.GetBackpressureDelay()
	assert.Equal(t, time.Duration(0), delay)

	// Test GetMetrics
	metrics := tm.GetMetrics()
	assert.NotNil(t, metrics)
	assert.Contains(t, metrics, "clock_skew_offset_ms")
	assert.Contains(t, metrics, "active_deadlines")
}

// Test MultiParticipantCoordinator
func TestMultiParticipantCoordinatorOperations(t *testing.T) {
	coordinator := NewMultiParticipantCoordinator()
	require.NotNil(t, coordinator)

	// Test RegisterParticipant
	participant := &lksdk.RemoteParticipant{}
	coordinator.RegisterParticipant("participant-1", participant)

	// Test UpdateParticipantActivity
	coordinator.UpdateParticipantActivity("participant-1", ActivityTypeJoined)

	// Test RecordInteraction
	coordinator.RecordInteraction("participant-1", "participant-2", "chat", nil)

	// Test CreateGroup
	group, err := coordinator.CreateGroup("group-1", "Test Group", map[string]interface{}{"key": "value"})
	assert.NoError(t, err)
	assert.NotNil(t, group)
	assert.Equal(t, "group-1", group.ID)
	assert.Equal(t, "Test Group", group.Name)

	// Test AddParticipantToGroup
	err = coordinator.AddParticipantToGroup("participant-1", "group-1")
	assert.NoError(t, err)

	// Test GetActiveParticipants
	active := coordinator.GetActiveParticipants()
	assert.NotNil(t, active)
	assert.Len(t, active, 1)

	// Test GetParticipantGroups
	groups := coordinator.GetParticipantGroups("participant-1")
	assert.Len(t, groups, 1)
	assert.Equal(t, "group-1", groups[0].ID)

	// Test GetGroupMembers
	members := coordinator.GetGroupMembers("group-1")
	assert.Contains(t, members, "participant-1")

	// Test RemoveParticipantFromGroup
	err = coordinator.RemoveParticipantFromGroup("participant-1", "group-1")
	assert.NoError(t, err)

	// Test GetInteractionGraph
	graph := coordinator.GetInteractionGraph()
	assert.NotNil(t, graph)

	// Test GetParticipantInteractions
	interactions := coordinator.GetParticipantInteractions("participant-1")
	assert.NotNil(t, interactions)

	// Test GetActivityMetrics
	metrics := coordinator.GetActivityMetrics()
	assert.NotNil(t, metrics)

	// Test AddCoordinationRule (would need a proper implementation)
	// CoordinationRule is an interface, so we can't test it directly

	// Test RegisterEventHandler
	coordinator.RegisterEventHandler("test", func(event CoordinationEvent) {
		// Handler registered
	})

	// Test UnregisterParticipant
	coordinator.UnregisterParticipant("participant-1")

	// Test Stop
	coordinator.Stop()
}

// Test ParticipantPermissionManager
func TestParticipantPermissionManagerOperations(t *testing.T) {
	manager := NewParticipantPermissionManager()
	require.NotNil(t, manager)

	// Test UpdateParticipantPermissions
	perms := &livekit.ParticipantPermission{
		CanPublish:   true,
		CanSubscribe: true,
	}
	manager.UpdateParticipantPermissions("participant-1", perms)

	// Test GetParticipantPermissions
	retrieved := manager.GetParticipantPermissions("participant-1")
	assert.NotNil(t, retrieved)
	assert.True(t, retrieved.CanPublish)

	// Test SetDefaultPermissions
	defaultPerms := &livekit.ParticipantPermission{
		CanPublish:   false,
		CanSubscribe: true,
	}
	manager.SetDefaultPermissions(defaultPerms)

	// Test ValidatePermissions
	err := manager.ValidatePermissions(perms)
	assert.NoError(t, err)

	// Test RequestPermissionChange
	approved, err := manager.RequestPermissionChange("participant-1", perms)
	assert.NoError(t, err)
	assert.True(t, approved)

	// Test CanSendDataTo
	canSend := manager.CanSendDataTo("participant-1")
	assert.True(t, canSend)

	// Test CanManagePermissions
	canManage := manager.CanManagePermissions()
	assert.False(t, canManage)

	// Test SetCustomRestriction
	manager.SetCustomRestriction("participant-1", "screen_share", false)

	// Test GetPermissionHistory
	history := manager.GetPermissionHistory("participant-1")
	assert.NotNil(t, history)

	// Test AddPolicy (would need a proper implementation)
	// PermissionPolicy is an interface, so we can't test it directly

	// Test SetAgentCapabilities
	caps := &AgentCapabilities{
		CanSendData:          true,
		CanPublishTracks:     true,
		CanSubscribeToTracks: true,
	}
	manager.SetAgentCapabilities(caps)

	// Test RemoveParticipant
	manager.RemoveParticipant("participant-1")
}

// Test QualityController
func TestQualityControllerOperations(t *testing.T) {
	controller := NewQualityController()
	require.NotNil(t, controller)

	// Test SetAdaptationPolicy
	policy := QualityAdaptationPolicy{
		LossThresholdUp:      5.0,
		LossThresholdDown:    2.0,
		BitrateThresholdUp:   80.0,
		BitrateThresholdDown: 50.0,
	}
	controller.SetAdaptationPolicy(policy)

	// Test CalculateOptimalQuality
	optimal := controller.CalculateOptimalQuality(livekit.ConnectionQuality_GOOD, nil)
	assert.NotEqual(t, livekit.VideoQuality_OFF, optimal)

	// Test EnableAdaptation
	controller.EnableAdaptation("track-1", true)

	// Test GetTrackStats
	stats, exists := controller.GetTrackStats("track-1")
	assert.False(t, exists) // No track being monitored
	assert.Nil(t, stats)

	// Test SetUpdateInterval
	controller.SetUpdateInterval(1 * time.Second)
}

// Test ParticipantEventProcessor
func TestParticipantEventProcessorOperations(t *testing.T) {
	processor := NewParticipantEventProcessor()
	require.NotNil(t, processor)

	// Test RegisterHandler
	processor.RegisterHandler(EventTypeParticipantJoined, func(event ParticipantEvent) error {
		return nil
	})

	// Test QueueEvent
	event := ParticipantEvent{
		Type:        EventTypeParticipantJoined,
		Participant: &lksdk.RemoteParticipant{},
		Timestamp:   time.Now(),
	}
	processor.QueueEvent(event)

	// Test ProcessPendingEvents
	processor.ProcessPendingEvents()
	time.Sleep(10 * time.Millisecond) // Give time for async processing

	// Test AddFilter
	processor.AddFilter(func(event ParticipantEvent) bool {
		return event.Type == EventTypeParticipantJoined
	})

	// Test GetEventHistory
	history := processor.GetEventHistory(10)
	assert.NotNil(t, history)

	// Test GetMetrics
	metrics := processor.GetMetrics()
	assert.NotNil(t, metrics)
	assert.Contains(t, metrics, "total_events")
	assert.Contains(t, metrics, "processed_events")

	// Test Stop
	processor.Stop()
}

// Test TrackSubscriptionManager
func TestTrackSubscriptionManagerOperations(t *testing.T) {
	manager := NewTrackSubscriptionManager()
	require.NotNil(t, manager)

	// Test SetAutoSubscribe
	manager.SetAutoSubscribe(true)

	// Test SetSubscribeAudio
	manager.SetSubscribeAudio(true)

	// Test SetSubscribeVideo
	manager.SetSubscribeVideo(false)

	// Test SetSourcePriority
	manager.SetSourcePriority(livekit.TrackSource_CAMERA, 100)
	manager.SetSourcePriority(livekit.TrackSource_SCREEN_SHARE, 90)

	// Test GetSourcePriority
	priority := manager.GetSourcePriority(livekit.TrackSource_CAMERA)
	assert.Equal(t, 100, priority)

	// Test AddFilter
	manager.AddFilter(func(publication *lksdk.RemoteTrackPublication) bool {
		return true
	})

	// Test ShouldAutoSubscribe (would need actual RemoteTrackPublication)
	// Can't test with mock as it expects actual SDK type

	// Test ClearFilters
	manager.ClearFilters()
}

// Test ConnectionQualityMonitor
func TestConnectionQualityMonitorOperations(t *testing.T) {
	monitor := NewConnectionQualityMonitor()
	require.NotNil(t, monitor)

	// Test StartMonitoring
	participant := &lksdk.RemoteParticipant{}
	monitor.StartMonitoring(participant)

	// Test UpdateQuality
	monitor.UpdateQuality(livekit.ConnectionQuality_GOOD)
	monitor.UpdateQuality(livekit.ConnectionQuality_POOR)

	// Test GetCurrentQuality
	quality := monitor.GetCurrentQuality()
	assert.Equal(t, livekit.ConnectionQuality_POOR, quality)

	// Test SetQualityChangeCallback
	callbackCalled := false
	monitor.SetQualityChangeCallback(func(old, new livekit.ConnectionQuality) {
		callbackCalled = true
	})

	monitor.UpdateQuality(livekit.ConnectionQuality_EXCELLENT)
	assert.True(t, callbackCalled)

	// Test GetQualityHistory
	history := monitor.GetQualityHistory()
	assert.GreaterOrEqual(t, len(history), 3)

	// Test IsStable
	isStable := monitor.IsStable(1 * time.Minute)
	assert.False(t, isStable) // Quality changed recently

	// Test GetAverageQuality
	avg := monitor.GetAverageQuality(1 * time.Minute)
	assert.NotNil(t, avg)

	// Test StopMonitoring
	monitor.StopMonitoring()
}

// Helper test types

type TestMediaStage struct {
	name     string
	priority int
}

func (s *TestMediaStage) GetName() string  { return s.name }
func (s *TestMediaStage) GetPriority() int { return s.priority }
func (s *TestMediaStage) Process(ctx context.Context, data []byte) ([]byte, error) {
	return data, nil
}
func (s *TestMediaStage) Initialize() error                   { return nil }
func (s *TestMediaStage) Close() error                        { return nil }
func (s *TestMediaStage) CanProcess(mediaType MediaType) bool { return true }

type TestRemoteTrack struct {
	id string
}

func (t *TestRemoteTrack) ID() string                       { return t.id }
func (t *TestRemoteTrack) Kind() lksdk.TrackKind            { return lksdk.TrackKindVideo }
func (t *TestRemoteTrack) StreamID() string                 { return "stream-1" }
func (t *TestRemoteTrack) Codec() webrtc.RTPCodecParameters { return webrtc.RTPCodecParameters{} }

type TestRemoteTrackPublication struct {
	kind lksdk.TrackKind
}

func (p *TestRemoteTrackPublication) Kind() lksdk.TrackKind                 { return p.kind }
func (p *TestRemoteTrackPublication) Source() livekit.TrackSource           { return livekit.TrackSource_CAMERA }
func (p *TestRemoteTrackPublication) SID() string                           { return "pub-1" }
func (p *TestRemoteTrackPublication) Name() string                          { return "test-pub" }
func (p *TestRemoteTrackPublication) IsMuted() bool                         { return false }
func (p *TestRemoteTrackPublication) IsSubscribed() bool                    { return false }
func (p *TestRemoteTrackPublication) Track() lksdk.Track                    { return nil }
func (p *TestRemoteTrackPublication) SetSubscribed(subscribed bool) error   { return nil }
func (p *TestRemoteTrackPublication) SetEnabled(enabled bool) error         { return nil }
func (p *TestRemoteTrackPublication) Participant() *lksdk.RemoteParticipant { return nil }

// Test error cases
func TestErrorHandling(t *testing.T) {
	// Test TimingGuard with exceeded deadline
	logger := zap.NewNop()
	tm := NewTimingManager(logger, TimingManagerOptions{})

	// Set already exceeded deadline
	tm.SetDeadline("job-1", time.Now().Add(-1*time.Second), "test")

	guard := tm.NewGuard("job-1", "test-operation")
	err := guard.Execute(context.Background(), func(ctx context.Context) error {
		return nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deadline exceeded")

	// Test MultiParticipantCoordinator error cases
	coordinator := NewMultiParticipantCoordinator()

	// Try to add participant to non-existent group
	err = coordinator.AddParticipantToGroup("participant-1", "non-existent")
	assert.Error(t, err)

	// Try to remove participant from non-existent group
	err = coordinator.RemoveParticipantFromGroup("participant-1", "non-existent")
	assert.Error(t, err)

	// Test duplicate group creation
	_, err = coordinator.CreateGroup("group-1", "Group 1", nil)
	assert.NoError(t, err)
	_, err = coordinator.CreateGroup("group-1", "Group 1", nil)
	assert.Error(t, err)
}

// Test concurrent operations
func TestConcurrentOperations(t *testing.T) {
	// Test MultiParticipantCoordinator concurrent access
	coordinator := NewMultiParticipantCoordinator()

	done := make(chan bool, 3)

	// Concurrent participant registration
	go func() {
		for i := 0; i < 100; i++ {
			identity := fmt.Sprintf("participant-%d", i)
			coordinator.RegisterParticipant(identity, nil)
		}
		done <- true
	}()

	// Concurrent activity updates
	go func() {
		for i := 0; i < 100; i++ {
			identity := fmt.Sprintf("participant-%d", i%50)
			coordinator.UpdateParticipantActivity(identity, ActivityTypeJoined)
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < 100; i++ {
			coordinator.GetActiveParticipants()
			coordinator.GetActivityMetrics()
		}
		done <- true
	}()

	// Wait for completion
	for i := 0; i < 3; i++ {
		<-done
	}

	// Verify no panic occurred
	metrics := coordinator.GetActivityMetrics()
	assert.NotNil(t, metrics)
}
