package egress

import (
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
)

// MockTrackRemote implements a minimal webrtc.TrackRemote for testing
type MockTrackRemote struct {
	id    string
	kind  webrtc.RTPCodecType
	codec webrtc.RTPCodecParameters
}

func (m *MockTrackRemote) ID() string {
	return m.id
}

func (m *MockTrackRemote) Kind() webrtc.RTPCodecType {
	return m.kind
}

func (m *MockTrackRemote) Codec() webrtc.RTPCodecParameters {
	return m.codec
}

func (m *MockTrackRemote) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	// Mock implementation - returns nil to simulate end of stream
	return nil, nil, io.EOF
}

func TestTrackManager(t *testing.T) {
	codecTracker := NewCodecTracker("test-session")

	t.Run("creates track manager", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		assert.NotNil(t, tm)
		assert.Equal(t, "test-session", tm.sessionID)
		assert.Equal(t, 100, tm.maxTracks)
	})

	t.Run("adds and removes tracks", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)

		// Create mock track
		track := &MockTrackRemote{
			id:   "track-1",
			kind: webrtc.RTPCodecTypeVideo,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "video/H264",
				},
				PayloadType: 96,
			},
		}

		// Add track
		err := tm.AddTrack(track, "participant-1", "User 1")
		assert.NoError(t, err)
		assert.Equal(t, 1, tm.GetActiveTrackCount())

		// Get track info
		info, err := tm.GetTrack("track-1")
		assert.NoError(t, err)
		assert.NotNil(t, info)
		assert.Equal(t, "track-1", info.TrackID)
		assert.Equal(t, "participant-1", info.ParticipantID)
		assert.Equal(t, "User 1", info.ParticipantName)
		assert.True(t, info.Active)

		// Remove track
		err = tm.RemoveTrack("track-1")
		assert.NoError(t, err)
		assert.Equal(t, 0, tm.GetActiveTrackCount())
	})

	t.Run("prevents duplicate tracks", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		codecTracker.Reset() // Reset codec tracker for new test

		track := &MockTrackRemote{
			id:   "track-1",
			kind: webrtc.RTPCodecTypeVideo,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "video/H264",
				},
			},
		}

		// Add track
		err := tm.AddTrack(track, "participant-1", "User 1")
		assert.NoError(t, err)

		// Try to add same track again
		err = tm.AddTrack(track, "participant-1", "User 1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("enforces max tracks limit", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		tm.SetMaxTracks(2)
		codecTracker.Reset()

		// Add tracks up to limit
		for i := 0; i < 2; i++ {
			track := &MockTrackRemote{
				id:   fmt.Sprintf("track-%d", i),
				kind: webrtc.RTPCodecTypeAudio,
				codec: webrtc.RTPCodecParameters{
					RTPCodecCapability: webrtc.RTPCodecCapability{
						MimeType: "audio/opus",
					},
				},
			}
			err := tm.AddTrack(track, "participant-1", "User 1")
			assert.NoError(t, err)
		}

		// Try to add one more
		track := &MockTrackRemote{
			id:   "track-overflow",
			kind: webrtc.RTPCodecTypeAudio,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "audio/opus",
				},
			},
		}
		err := tm.AddTrack(track, "participant-1", "User 1")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "maximum track limit")
	})

	t.Run("validates codecs", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		codecTracker.Reset()

		// Add track with unsupported codec (AV1 is not supported)
		track := &MockTrackRemote{
			id:   "track-av1",
			kind: webrtc.RTPCodecTypeVideo,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "video/AV1",
				},
			},
		}

		err := tm.AddTrack(track, "participant-1", "User 1")
		assert.Error(t, err)
		if err != nil {
			assert.Contains(t, err.Error(), "codec validation failed")
		}
	})

	t.Run("removes participant tracks", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		codecTracker.Reset()

		// Add tracks for multiple participants
		for i := 0; i < 3; i++ {
			track := &MockTrackRemote{
				id:   fmt.Sprintf("track-%d", i),
				kind: webrtc.RTPCodecTypeAudio,
				codec: webrtc.RTPCodecParameters{
					RTPCodecCapability: webrtc.RTPCodecCapability{
						MimeType: "audio/opus",
					},
				},
			}
			participantID := "participant-1"
			if i > 1 {
				participantID = "participant-2"
			}
			err := tm.AddTrack(track, participantID, fmt.Sprintf("User %s", participantID))
			assert.NoError(t, err)
		}

		assert.Equal(t, 3, tm.GetActiveTrackCount())

		// Remove all tracks for participant-1
		removed := tm.RemoveParticipantTracks("participant-1")
		assert.Equal(t, 2, removed)
		assert.Equal(t, 1, tm.GetActiveTrackCount())
	})

	t.Run("gets all tracks", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		codecTracker.Reset()

		// Add multiple tracks
		for i := 0; i < 3; i++ {
			codec := webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "audio/opus",
				},
			}
			if i == 0 {
				codec.MimeType = "video/H264"
			}

			track := &MockTrackRemote{
				id:    fmt.Sprintf("track-%d", i),
				kind:  webrtc.RTPCodecTypeAudio,
				codec: codec,
			}
			if i == 0 {
				track.kind = webrtc.RTPCodecTypeVideo
			}

			err := tm.AddTrack(track, fmt.Sprintf("participant-%d", i), fmt.Sprintf("User %d", i))
			assert.NoError(t, err)
		}

		tracks := tm.GetAllTracks()
		assert.Len(t, tracks, 3)
	})

	t.Run("gets statistics", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		codecTracker.Reset()

		// Add video and audio tracks
		videoTrack := &MockTrackRemote{
			id:   "video-track",
			kind: webrtc.RTPCodecTypeVideo,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "video/H264",
				},
			},
		}
		err := tm.AddTrack(videoTrack, "participant-1", "User 1")
		assert.NoError(t, err)

		audioTrack := &MockTrackRemote{
			id:   "audio-track",
			kind: webrtc.RTPCodecTypeAudio,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "audio/opus",
				},
			},
		}
		err = tm.AddTrack(audioTrack, "participant-1", "User 1")
		assert.NoError(t, err)

		stats := tm.GetStatistics()
		assert.Equal(t, 2, stats.TotalTracks)
		assert.Equal(t, 2, stats.ActiveTracks)
		assert.Equal(t, 1, stats.VideoTracks)
		assert.Equal(t, 1, stats.AudioTracks)
		assert.Equal(t, 1, stats.UniqueParticipants)
	})

	t.Run("handles non-existent track", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)

		// Try to get non-existent track
		_, err := tm.GetTrack("non-existent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")

		// Try to remove non-existent track
		err = tm.RemoveTrack("non-existent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("closes track manager", func(t *testing.T) {
		tm := NewTrackManager("test-session", nil, codecTracker)
		codecTracker.Reset()

		// Add a track
		track := &MockTrackRemote{
			id:   "track-1",
			kind: webrtc.RTPCodecTypeVideo,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "video/H264",
				},
			},
		}
		tm.AddTrack(track, "participant-1", "User 1")

		// Close manager
		tm.Close()

		// Verify all tracks are cleared
		assert.Equal(t, 0, tm.GetActiveTrackCount())
		assert.Len(t, tm.GetAllTracks(), 0)
	})
}

func TestTrackStatistics(t *testing.T) {
	codecTracker := NewCodecTracker("test-session")
	tm := NewTrackManager("test-session", nil, codecTracker)

	// Start with empty stats
	stats := tm.GetStatistics()
	assert.Equal(t, 0, stats.TotalTracks)
	assert.Equal(t, 0, stats.ActiveTracks)
	assert.Equal(t, uint64(0), stats.TotalPackets)
	assert.Equal(t, uint64(0), stats.TotalBytes)

	// Add tracks and verify stats update
	track := &MockTrackRemote{
		id:   "track-1",
		kind: webrtc.RTPCodecTypeVideo,
		codec: webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/H264",
			},
		},
	}
	tm.AddTrack(track, "participant-1", "User 1")

	// Simulate packet reception by directly updating track info
	tm.mu.Lock()
	if info, exists := tm.tracks["track-1"]; exists {
		info.PacketsReceived = 100
		info.BytesReceived = 150000
		info.LastPacketTime = time.Now()
	}
	tm.mu.Unlock()

	stats = tm.GetStatistics()
	assert.Equal(t, 1, stats.TotalTracks)
	assert.Equal(t, 1, stats.ActiveTracks)
	assert.Equal(t, uint64(100), stats.TotalPackets)
	assert.Equal(t, uint64(150000), stats.TotalBytes)
}

func BenchmarkTrackManagerAdd(b *testing.B) {
	codecTracker := NewCodecTracker("bench-session")
	tm := NewTrackManager("bench-session", nil, codecTracker)
	tm.SetMaxTracks(b.N + 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		track := &MockTrackRemote{
			id:   fmt.Sprintf("track-%d", i),
			kind: webrtc.RTPCodecTypeAudio,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "audio/opus",
				},
			},
		}
		tm.AddTrack(track, "participant-1", "User 1")
	}
}

func BenchmarkTrackManagerGetStats(b *testing.B) {
	codecTracker := NewCodecTracker("bench-session")
	tm := NewTrackManager("bench-session", nil, codecTracker)

	// Add some tracks
	for i := 0; i < 10; i++ {
		track := &MockTrackRemote{
			id:   fmt.Sprintf("track-%d", i),
			kind: webrtc.RTPCodecTypeAudio,
			codec: webrtc.RTPCodecParameters{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType: "audio/opus",
				},
			},
		}
		tm.AddTrack(track, fmt.Sprintf("participant-%d", i%3), fmt.Sprintf("User %d", i%3))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats := tm.GetStatistics()
		_ = stats
	}
}