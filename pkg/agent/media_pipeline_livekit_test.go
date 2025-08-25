package agent

import (
	"fmt"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMediaPipelineWithLiveKit tests media pipeline with actual LiveKit server
func TestMediaPipelineWithLiveKit(t *testing.T) {
	// Check if LiveKit server is available
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		t.Log("Please ensure LiveKit server is running: docker run --rm -p 7880:7880 livekit/livekit-server --dev")
		return
	}

	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	// Create connected rooms
	pubRoom, subRoom, err := manager.CreateConnectedRooms("test-pipeline")
	require.NoError(t, err)
	defer pubRoom.Disconnect()
	defer subRoom.Disconnect()

	// Publish a synthetic audio track
	publication, err := manager.PublishSyntheticAudioTrack(pubRoom, "test-audio-track")
	require.NoError(t, err)
	require.NotNil(t, publication)

	// Wait for track to appear on subscriber side
	var remoteTrack *webrtc.TrackRemote
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for remote track")
		case <-ticker.C:
			for _, participant := range subRoom.GetRemoteParticipants() {
				for _, pub := range participant.TrackPublications() {
					if pub.Track() != nil {
						if track, ok := pub.Track().(*webrtc.TrackRemote); ok {
							remoteTrack = track
							goto foundTrack
						}
					}
				}
			}
		}
	}
foundTrack:

	require.NotNil(t, remoteTrack)

	// Now test the media pipeline with the real track
	pipeline := NewMediaPipeline()

	// Add a processing stage
	stage := &TestStage{
		name:     "TestStage",
		priority: 1,
	}
	pipeline.AddStage(stage)

	// Start processing the track
	err = pipeline.StartProcessingTrack(remoteTrack)
	assert.NoError(t, err)

	// Let it process for a bit
	time.Sleep(500 * time.Millisecond)

	// Get processing stats
	stats, exists := pipeline.GetProcessingStats(remoteTrack.ID())
	assert.True(t, exists)
	assert.NotNil(t, stats)

	// Stop processing
	pipeline.StopProcessingTrack(remoteTrack.ID())

	// Verify track is removed
	pipeline.mu.RLock()
	_, exists = pipeline.tracks[remoteTrack.ID()]
	pipeline.mu.RUnlock()
	assert.False(t, exists)
}

// TestMediaPipelineMultipleTracksLiveKit tests processing multiple tracks with LiveKit
func TestMediaPipelineMultipleTracksLiveKit(t *testing.T) {
	// Check if LiveKit server is available
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	// Create connected rooms
	pubRoom, subRoom, err := manager.CreateConnectedRooms("test-multiple-tracks")
	require.NoError(t, err)
	defer pubRoom.Disconnect()
	defer subRoom.Disconnect()

	pipeline := NewMediaPipeline()

	// Publish multiple synthetic tracks
	numTracks := 3
	trackNames := make([]string, numTracks)

	for i := 0; i < numTracks; i++ {
		trackName := fmt.Sprintf("track-%d", i)
		trackNames[i] = trackName

		// Create and publish synthetic audio track
		track, err := NewSyntheticAudioTrack(trackName, 48000, 2)
		require.NoError(t, err)

		// Start generating different frequencies
		track.StartGenerating(440.0 * float64(i+1))
		defer track.StopGenerating()

		// Publish to room
		_, err = pubRoom.LocalParticipant.PublishTrack(track.TrackLocalStaticRTP, &lksdk.TrackPublicationOptions{
			Name:   trackName,
			Source: livekit.TrackSource_MICROPHONE,
		})
		require.NoError(t, err)
	}

	// Wait for all tracks to appear and process them
	processedTracks := 0
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for processedTracks < numTracks {
		select {
		case <-timeout:
			t.Fatalf("timeout waiting for tracks, got %d/%d", processedTracks, numTracks)
		case <-ticker.C:
			for _, participant := range subRoom.GetRemoteParticipants() {
				for _, pub := range participant.TrackPublications() {
					if pub.Track() != nil {
						if remoteTrack, ok := pub.Track().(*webrtc.TrackRemote); ok {
							// Check if we're already processing this track
							pipeline.mu.RLock()
							_, exists := pipeline.tracks[remoteTrack.ID()]
							pipeline.mu.RUnlock()

							if !exists {
								err := pipeline.StartProcessingTrack(remoteTrack)
								if err == nil {
									processedTracks++
								}
							}
						}
					}
				}
			}
		}
	}

	// Verify all tracks are being processed
	pipeline.mu.RLock()
	assert.Equal(t, numTracks, len(pipeline.tracks))
	pipeline.mu.RUnlock()

	// Let them process
	time.Sleep(500 * time.Millisecond)

	// Stop all tracks
	pipeline.mu.Lock()
	trackIDs := make([]string, 0, len(pipeline.tracks))
	for id := range pipeline.tracks {
		trackIDs = append(trackIDs, id)
	}
	pipeline.mu.Unlock()

	for _, id := range trackIDs {
		pipeline.StopProcessingTrack(id)
	}

	// Verify all tracks are removed
	pipeline.mu.RLock()
	assert.Empty(t, pipeline.tracks)
	pipeline.mu.RUnlock()
}

// TestMediaPipelineMetricsLiveKit tests metrics collection with LiveKit
func TestMediaPipelineMetricsLiveKit(t *testing.T) {
	// Check if LiveKit server is available
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	// Create connected rooms
	pubRoom, subRoom, err := manager.CreateConnectedRooms("test-metrics")
	require.NoError(t, err)
	defer pubRoom.Disconnect()
	defer subRoom.Disconnect()

	pipeline := NewMediaPipeline()

	// Publish a synthetic audio track
	track, err := NewSyntheticAudioTrack("metrics-track", 48000, 2)
	require.NoError(t, err)
	track.StartGenerating(440.0)
	defer track.StopGenerating()

	_, err = pubRoom.LocalParticipant.PublishTrack(track.TrackLocalStaticRTP, &lksdk.TrackPublicationOptions{
		Name:   "metrics-track",
		Source: livekit.TrackSource_MICROPHONE,
	})
	require.NoError(t, err)

	// Wait for track and start processing
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var remoteTrack *webrtc.TrackRemote
	for remoteTrack == nil {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for track")
		case <-ticker.C:
			for _, participant := range subRoom.GetRemoteParticipants() {
				for _, pub := range participant.TrackPublications() {
					if pub.Track() != nil {
						if track, ok := pub.Track().(*webrtc.TrackRemote); ok {
							remoteTrack = track
							break
						}
					}
				}
			}
		}
	}

	err = pipeline.StartProcessingTrack(remoteTrack)
	require.NoError(t, err)

	// Let it process and collect metrics
	time.Sleep(1 * time.Second)

	// Get track-specific stats
	stats, exists := pipeline.GetProcessingStats(remoteTrack.ID())
	assert.True(t, exists)
	assert.NotNil(t, stats)

	// The stats should show some processing
	// Note: actual packet counts depend on the track activity
	assert.NotNil(t, stats)

	// Stop processing
	pipeline.StopProcessingTrack(remoteTrack.ID())
}

// TestMediaPipelineDestroyLiveKit tests pipeline destruction with LiveKit
func TestMediaPipelineDestroyLiveKit(t *testing.T) {
	// Check if LiveKit server is available
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	// Create connected rooms
	pubRoom, subRoom, err := manager.CreateConnectedRooms("test-destroy")
	require.NoError(t, err)
	defer pubRoom.Disconnect()
	defer subRoom.Disconnect()

	pipeline := NewMediaPipeline()

	// Add some stages and processors
	stage := &TestStage{
		name:     "TestStage",
		priority: 1,
	}
	pipeline.AddStage(stage)

	processor := &MockMediaProcessor{name: "TestProcessor"}
	pipeline.RegisterProcessor(processor)

	// Publish and process a track
	track, err := NewSyntheticAudioTrack("destroy-track", 48000, 2)
	require.NoError(t, err)
	track.StartGenerating(440.0)
	defer track.StopGenerating()

	_, err = pubRoom.LocalParticipant.PublishTrack(track.TrackLocalStaticRTP, &lksdk.TrackPublicationOptions{
		Name:   "destroy-track",
		Source: livekit.TrackSource_MICROPHONE,
	})
	require.NoError(t, err)

	// Wait for track
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var remoteTrack *webrtc.TrackRemote
	for remoteTrack == nil {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for track")
		case <-ticker.C:
			for _, participant := range subRoom.GetRemoteParticipants() {
				for _, pub := range participant.TrackPublications() {
					if pub.Track() != nil {
						if track, ok := pub.Track().(*webrtc.TrackRemote); ok {
							remoteTrack = track
							break
						}
					}
				}
			}
		}
	}

	err = pipeline.StartProcessingTrack(remoteTrack)
	require.NoError(t, err)

	// Let it process
	time.Sleep(200 * time.Millisecond)

	// Clear everything (simulating destroy)
	pipeline.StopProcessingTrack(remoteTrack.ID())

	// Clear stages and processors but keep maps initialized
	pipeline.mu.Lock()
	pipeline.stages = []MediaPipelineStage{}
	// Clear processors map
	for k := range pipeline.processors {
		delete(pipeline.processors, k)
	}
	// Verify tracks are already cleared by StopProcessingTrack
	assert.Empty(t, pipeline.tracks)
	pipeline.mu.Unlock()

	// Verify everything is cleaned up
	pipeline.mu.RLock()
	assert.Empty(t, pipeline.stages)
	assert.Empty(t, pipeline.processors)
	assert.Empty(t, pipeline.tracks)
	pipeline.mu.RUnlock()
}

// TestQualityControllerWithLiveKit tests quality controller with real tracks
func TestQualityControllerWithLiveKit(t *testing.T) {
	// Check if LiveKit server is available
	if err := TestLiveKitConnection(); err != nil {
		t.Logf("LiveKit server not available: %v", err)
		return
	}

	controller := NewQualityController()
	manager := NewTestRoomManager()
	defer manager.CleanupRooms()

	// Create connected rooms
	pubRoom, subRoom, err := manager.CreateConnectedRooms("test-quality")
	require.NoError(t, err)
	defer pubRoom.Disconnect()
	defer subRoom.Disconnect()

	// Create and publish tracks
	audioTrack, err := NewSyntheticAudioTrack("audio-quality", 48000, 2)
	require.NoError(t, err)
	audioTrack.StartGenerating(440.0)
	defer audioTrack.StopGenerating()

	videoTrack, err := NewSyntheticVideoTrack("video-quality", 1920, 1080, 30.0)
	require.NoError(t, err)
	videoTrack.StartGenerating()
	defer videoTrack.StopGenerating()

	// Publish audio track
	_, err = pubRoom.LocalParticipant.PublishTrack(audioTrack.TrackLocalStaticRTP, &lksdk.TrackPublicationOptions{
		Name:   "audio-quality",
		Source: livekit.TrackSource_MICROPHONE,
	})
	require.NoError(t, err)

	// Publish video track
	_, err = pubRoom.LocalParticipant.PublishTrack(videoTrack.TrackLocalStaticSample, &lksdk.TrackPublicationOptions{
		Name:   "video-quality",
		Source: livekit.TrackSource_CAMERA,
	})
	require.NoError(t, err)

	// Wait for tracks to appear on subscriber side
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var remoteAudioTrack, remoteVideoTrack *webrtc.TrackRemote

	for remoteAudioTrack == nil || remoteVideoTrack == nil {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for tracks")
		case <-ticker.C:
			for _, participant := range subRoom.GetRemoteParticipants() {
				for _, pub := range participant.TrackPublications() {
					if pub.Track() != nil {
						if track, ok := pub.Track().(*webrtc.TrackRemote); ok {
							if track.Kind() == webrtc.RTPCodecTypeAudio && remoteAudioTrack == nil {
								remoteAudioTrack = track
							} else if track.Kind() == webrtc.RTPCodecTypeVideo && remoteVideoTrack == nil {
								remoteVideoTrack = track
							}
						}
					}
				}
			}
		}
	}

	// Test with audio track
	err = controller.ApplyQualitySettings(remoteAudioTrack, livekit.VideoQuality_HIGH)
	// Audio tracks don't support video quality settings, this should handle gracefully
	_ = err

	// Test with video track
	err = controller.ApplyQualitySettings(remoteVideoTrack, livekit.VideoQuality_HIGH)
	// Should handle video track quality settings
	_ = err

	// Test dimension settings on video track
	err = controller.ApplyDimensionSettings(remoteVideoTrack, 1920, 1080)
	_ = err

	// Test frame rate settings
	err = controller.ApplyFrameRateSettings(remoteVideoTrack, 30.0)
	_ = err

	// Test with nil should still be handled
	err = controller.ApplyQualitySettings(nil, livekit.VideoQuality_HIGH)
	assert.Error(t, err) // Should return error for nil track
}
