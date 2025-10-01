package egress

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAVSyncMonitor(t *testing.T) {
	t.Run("detects sync within limits", func(t *testing.T) {
		monitor := NewAVSyncMonitor()

		// Simulate synchronized A/V (within 50ms)
		videoPTS := uint64(1000000000) // 1 second in nanoseconds
		audioPTS := uint64(1020000000) // 1.02 seconds (20ms drift)

		monitor.UpdateVideoPTS(videoPTS)
		monitor.UpdateAudioPTS(audioPTS)

		// Check sync status
		assert.True(t, monitor.IsInSync(), "Should be in sync with 20ms drift")
		assert.LessOrEqual(t, monitor.GetCurrentDrift(), int64(50), "Drift should be <= 50ms")

		status := monitor.GetSyncStatus()
		assert.Equal(t, "OK", status.Status)
		assert.True(t, status.IsInSync)
	})

	t.Run("detects sync beyond limits", func(t *testing.T) {
		monitor := NewAVSyncMonitor()

		// Simulate out-of-sync A/V (>50ms)
		videoPTS := uint64(1000000000) // 1 second
		audioPTS := uint64(1100000000) // 1.1 seconds (100ms drift)

		monitor.UpdateVideoPTS(videoPTS)
		monitor.UpdateAudioPTS(audioPTS)

		// Check sync status
		assert.False(t, monitor.IsInSync(), "Should be out of sync with 100ms drift")
		assert.Greater(t, monitor.GetCurrentDrift(), int64(50), "Drift should be > 50ms")

		status := monitor.GetSyncStatus()
		// 100ms is exactly 2x the limit, which triggers WARNING, not CRITICAL
		assert.Equal(t, "WARNING", status.Status)
		assert.False(t, status.IsInSync)
		assert.Contains(t, status.Message, "slightly high")
	})

	t.Run("tracks maximum drift", func(t *testing.T) {
		monitor := NewAVSyncMonitor()

		// First update - 20ms drift
		monitor.UpdateVideoPTS(uint64(1000000000))       // 1s
		monitor.UpdateAudioPTS(uint64(1020000000))       // 1.02s = 20ms drift
		assert.Equal(t, int64(20), monitor.GetCurrentDrift())

		// Second update - 80ms drift (should become max)
		monitor.UpdateVideoPTS(uint64(2000000000))       // 2s
		monitor.UpdateAudioPTS(uint64(2080000000))       // 2.08s = 80ms drift
		assert.Equal(t, int64(80), monitor.GetCurrentDrift())
		assert.Equal(t, int64(80), monitor.GetMaxDrift())

		// Third update - 30ms drift (max should remain 80)
		monitor.UpdateVideoPTS(uint64(3000000000))       // 3s
		monitor.UpdateAudioPTS(uint64(3030000000))       // 3.03s = 30ms drift
		assert.Equal(t, int64(30), monitor.GetCurrentDrift())
		assert.Equal(t, int64(80), monitor.GetMaxDrift(), "Max drift should remain 80ms")
	})

	t.Run("calculates average drift", func(t *testing.T) {
		monitor := NewAVSyncMonitor()

		// Add multiple samples with different drifts
		drifts := []uint64{10000000, 20000000, 30000000, 40000000} // 10, 20, 30, 40ms in nanoseconds
		baseTime := uint64(1000000000) // 1 second base

		for i, drift := range drifts {
			videoTime := baseTime + uint64(i)*1000000000 // Advance by 1 second each time
			audioTime := videoTime + drift
			monitor.UpdateVideoPTS(videoTime)
			monitor.UpdateAudioPTS(audioTime)
		}

		// Average should be 25ms (10+20+30+40)/4
		avg := monitor.GetAverageDrift()
		assert.InDelta(t, 25.0, avg, 1.0, "Average drift should be ~25ms")
	})

	t.Run("handles reset", func(t *testing.T) {
		monitor := NewAVSyncMonitor()

		// Add some data
		monitor.UpdateVideoPTS(uint64(1000000000))
		monitor.UpdateAudioPTS(uint64(1050000000))
		assert.Greater(t, monitor.GetCurrentDrift(), int64(0))

		// Reset
		monitor.Reset()

		// Should be zeroed
		assert.Equal(t, int64(0), monitor.GetCurrentDrift())
		assert.Equal(t, int64(0), monitor.GetMaxDrift())
		assert.Equal(t, 0.0, monitor.GetAverageDrift())
	})

	t.Run("warning status for moderate drift", func(t *testing.T) {
		monitor := NewAVSyncMonitor()

		// Simulate 75ms drift (between 50-100ms)
		monitor.UpdateVideoPTS(uint64(1000000000))
		monitor.UpdateAudioPTS(uint64(1075000000))

		status := monitor.GetSyncStatus()
		assert.Equal(t, "WARNING", status.Status)
		assert.False(t, status.IsInSync)
		assert.Contains(t, status.Message, "slightly high")
	})
}

func TestAVSyncWithRealTimestamps(t *testing.T) {
	monitor := NewAVSyncMonitor()

	// Simulate real RTP timestamps converted to nanoseconds
	// Video: 90kHz clock, Audio: 48kHz clock

	// Start at t=0
	videoRTPBase := uint32(0)
	audioRTPBase := uint32(0)

	// Simulate 1 second of synchronized playback
	for i := 0; i < 30; i++ { // 30 video frames at 30fps
		// Video frame every 33.33ms (30fps)
		videoRTP := videoRTPBase + uint32(i*3000) // 3000 ticks at 90kHz = 33.33ms
		videoPTS := uint64(videoRTP) * 1000000000 / 90000

		// Audio packet every 20ms (typical Opus)
		audioPacketNum := i * 33 / 20 // Which audio packet corresponds to this time
		audioRTP := audioRTPBase + uint32(audioPacketNum*960) // 960 samples at 48kHz = 20ms
		audioPTS := uint64(audioRTP) * 1000000000 / 48000

		monitor.UpdateVideoPTS(videoPTS)
		monitor.UpdateAudioPTS(audioPTS)

		// Should remain in sync throughout
		if i > 0 { // Skip first update (need both timestamps)
			assert.True(t, monitor.IsInSync(), "Should remain in sync at frame %d", i)
		}
	}

	// Verify final sync status
	status := monitor.GetSyncStatus()
	assert.Equal(t, "OK", status.Status)
	assert.True(t, status.IsInSync)
	t.Logf("Final drift: %dms (max allowed: 50ms)", status.CurrentDriftMs)
}

func BenchmarkAVSyncMonitor(b *testing.B) {
	monitor := NewAVSyncMonitor()
	videoPTS := uint64(1000000000)
	audioPTS := uint64(1020000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		monitor.UpdateVideoPTS(videoPTS + uint64(i))
		monitor.UpdateAudioPTS(audioPTS + uint64(i))
		_ = monitor.GetCurrentDrift()
	}
}