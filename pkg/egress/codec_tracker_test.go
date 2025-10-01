package egress

import (
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodecTracker(t *testing.T) {
	t.Run("locks on first codec", func(t *testing.T) {
		tracker := NewCodecTracker("test-session")

		// First H.264 codec should lock
		h264Codec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/H264",
			},
			PayloadType: 96,
		}

		err := tracker.ValidateVideoCodec(h264Codec)
		require.NoError(t, err)

		// Check it's locked
		locked := tracker.GetVideoCodec()
		assert.NotNil(t, locked)
		assert.Equal(t, "video/H264", locked.MimeType)
	})

	t.Run("rejects codec changes", func(t *testing.T) {
		tracker := NewCodecTracker("test-session")

		// Lock with H.264
		h264Codec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/H264",
			},
			PayloadType: 96,
		}
		err := tracker.ValidateVideoCodec(h264Codec)
		require.NoError(t, err)

		// Try to change to VP8 (should be rejected)
		vp8Codec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/VP8",
			},
			PayloadType: 97,
		}
		err = tracker.ValidateVideoCodec(vp8Codec)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "codec change detected")

		// Check reject count
		assert.Equal(t, int64(1), tracker.GetRejectCount())
	})

	t.Run("accepts same codec", func(t *testing.T) {
		tracker := NewCodecTracker("test-session")

		// Lock with Opus
		opusCodec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "audio/opus",
			},
			PayloadType: 111,
		}
		err := tracker.ValidateAudioCodec(opusCodec)
		require.NoError(t, err)

		// Same codec should be accepted
		err = tracker.ValidateAudioCodec(opusCodec)
		assert.NoError(t, err)

		// No rejects
		assert.Equal(t, int64(0), tracker.GetRejectCount())
	})

	t.Run("rejects unsupported codecs", func(t *testing.T) {
		tracker := NewCodecTracker("test-session")

		// Test unsupported codec (AV1) first, before locking any codec
		unsupportedCodec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/AV1",
			},
			PayloadType: 99,
		}
		err := tracker.ValidateVideoCodec(unsupportedCodec)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported video codec")

		// Test VP9 codec support after testing unsupported
		vp9Codec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/VP9",
			},
			PayloadType: 98,
		}
		err = tracker.ValidateVideoCodec(vp9Codec)
		assert.NoError(t, err, "VP9 codec should be supported")
	})

	t.Run("tracks audio and video independently", func(t *testing.T) {
		tracker := NewCodecTracker("test-session")

		// Lock video with H.264
		h264Codec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/H264",
			},
			PayloadType: 96,
		}
		err := tracker.ValidateVideoCodec(h264Codec)
		require.NoError(t, err)

		// Lock audio with Opus
		opusCodec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "audio/opus",
			},
			PayloadType: 111,
		}
		err = tracker.ValidateAudioCodec(opusCodec)
		require.NoError(t, err)

		// Get codec info
		info := tracker.GetCodecInfo()
		assert.Equal(t, "video/H264", info.VideoCodec)
		assert.Equal(t, "audio/opus", info.AudioCodec)
		assert.Equal(t, int64(0), info.RejectCount)
	})

	t.Run("reset clears locks", func(t *testing.T) {
		tracker := NewCodecTracker("test-session")

		// Lock codecs
		h264Codec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/H264",
			},
			PayloadType: 96,
		}
		tracker.ValidateVideoCodec(h264Codec)

		// Reset
		tracker.Reset()

		// Should be able to lock different codec now
		// Test that we can lock with a different payload type after reset
		h264NewCodec := webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: "video/H264",
			},
			PayloadType: 100, // Different payload type
		}
		err := tracker.ValidateVideoCodec(h264NewCodec)
		assert.NoError(t, err)

		assert.Equal(t, int64(0), tracker.GetRejectCount())
	})
}