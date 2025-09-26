package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "config.yaml")

	configContent := `
output:
  dir: /tmp/test
  segment_duration: 6
pipeline:
  video_port: 5100
  audio_port: 5102
  jitter_buffer_ms: 300
audio:
  mode: transcode_aac
  aac_bitrate: 256
s3:
  enabled: true
  bucket: test-bucket
  access_key: test-key
  secret_key: test-secret
`

	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)

	// Load the config
	cfg, err := Load(configFile)
	require.NoError(t, err)

	// Verify values
	assert.Equal(t, "/tmp/test", cfg.Output.Dir)
	assert.Equal(t, 6, cfg.Output.SegmentDuration)
	assert.Equal(t, 5100, cfg.Pipeline.VideoPort)
	assert.Equal(t, 5102, cfg.Pipeline.AudioPort)
	assert.Equal(t, 300, cfg.Pipeline.JitterBufferMs)
	assert.Equal(t, AudioTranscodeAAC, cfg.Audio.Mode)
	assert.Equal(t, 256, cfg.Audio.AACBitrate)
	assert.True(t, cfg.S3.Enabled)
	assert.Equal(t, "test-bucket", cfg.S3.Bucket)
}

func TestDefaultConfig(t *testing.T) {
	cfg := Default()

	// Check defaults
	assert.Equal(t, "/tmp/recordings", cfg.Output.Dir)
	assert.Equal(t, 4, cfg.Output.SegmentDuration)
	assert.Equal(t, 5004, cfg.Pipeline.VideoPort)
	assert.Equal(t, 5006, cfg.Pipeline.AudioPort)
	assert.Equal(t, 200, cfg.Pipeline.JitterBufferMs)
	assert.Equal(t, AudioPassThrough, cfg.Audio.Mode)
	assert.Equal(t, 192, cfg.Audio.AACBitrate)
	assert.Equal(t, 192, cfg.Audio.MP3Bitrate)
	assert.False(t, cfg.S3.Enabled)
	assert.Equal(t, 10, cfg.Agent.MaxJobs)
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			modify: func(c *Config) {
				// No modifications - should be valid
			},
			wantErr: false,
		},
		{
			name: "invalid audio mode",
			modify: func(c *Config) {
				c.Audio.Mode = "invalid"
			},
			wantErr: true,
			errMsg:  "invalid audio mode",
		},
		{
			name: "invalid video port",
			modify: func(c *Config) {
				c.Pipeline.VideoPort = 0
			},
			wantErr: true,
			errMsg:  "invalid video port",
		},
		{
			name: "invalid audio port",
			modify: func(c *Config) {
				c.Pipeline.AudioPort = 70000
			},
			wantErr: true,
			errMsg:  "invalid audio port",
		},
		{
			name: "same video and audio ports",
			modify: func(c *Config) {
				c.Pipeline.VideoPort = 5000
				c.Pipeline.AudioPort = 5000
			},
			wantErr: true,
			errMsg:  "video and audio ports must be different",
		},
		{
			name: "S3 enabled without bucket",
			modify: func(c *Config) {
				c.S3.Enabled = true
				c.S3.Bucket = ""
				c.S3.AccessKey = "key"
				c.S3.SecretKey = "secret"
			},
			wantErr: true,
			errMsg:  "S3 bucket is required",
		},
		{
			name: "S3 enabled without credentials",
			modify: func(c *Config) {
				c.S3.Enabled = true
				c.S3.Bucket = "bucket"
				c.S3.AccessKey = ""
				c.S3.SecretKey = ""
			},
			wantErr: true,
			errMsg:  "S3 credentials are required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.modify(cfg)

			err := cfg.validate()
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}