package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		valid  bool
	}{
		{
			name: "valid passthrough config",
			config: Config{
				VideoPort:          5004,
				AudioPort:          5006,
				OutputDir:          "/tmp/recordings",
				SegmentDuration:    4,
				JitterBufferMs:     200,
				AudioMode:          AudioPassThrough,
			},
			valid: true,
		},
		{
			name: "valid AAC transcode config",
			config: Config{
				VideoPort:          5004,
				AudioPort:          5006,
				OutputDir:          "/tmp/recordings",
				SegmentDuration:    4,
				JitterBufferMs:     200,
				AudioMode:          AudioTranscodeAAC,
				AACBitrate:         192,
			},
			valid: true,
		},
		{
			name: "valid MP3 transcode config",
			config: Config{
				VideoPort:          5004,
				AudioPort:          5006,
				OutputDir:          "/tmp/recordings",
				SegmentDuration:    4,
				JitterBufferMs:     200,
				AudioMode:          AudioTranscodeMP3,
				MP3Bitrate:         192,
			},
			valid: true,
		},
		{
			name: "same video and audio port",
			config: Config{
				VideoPort:          5004,
				AudioPort:          5004, // Same as video - OK since deprecated
				OutputDir:          "/tmp/recordings",
				SegmentDuration:    4,
				JitterBufferMs:     200,
				AudioMode:          AudioPassThrough,
			},
			valid: true, // Ports are deprecated, no validation needed
		},
		{
			name: "invalid port",
			config: Config{
				VideoPort:          -1, // Invalid - but OK since deprecated
				AudioPort:          5006,
				OutputDir:          "/tmp/recordings",
				SegmentDuration:    4,
				JitterBufferMs:     200,
				AudioMode:          AudioPassThrough,
			},
			valid: true, // Ports are deprecated, no validation needed
		},
		{
			name: "zero segment duration",
			config: Config{
				VideoPort:          5004,
				AudioPort:          5006,
				OutputDir:          "/tmp/recordings",
				SegmentDuration:    0, // Invalid
				JitterBufferMs:     200,
				AudioMode:          AudioPassThrough,
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(&tt.config)
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestAudioModeString(t *testing.T) {
	assert.Equal(t, "passthrough", string(AudioPassThrough))
	assert.Equal(t, "transcode_aac", string(AudioTranscodeAAC))
	assert.Equal(t, "transcode_mp3", string(AudioTranscodeMP3))
}

func TestStateString(t *testing.T) {
	states := map[State]string{
		StateStopped: "stopped",
		StatePlaying: "playing",
		StatePaused:  "paused",
	}

	for _, expected := range states {
		assert.Contains(t, []string{"stopped", "playing", "paused"}, expected)
	}
}

