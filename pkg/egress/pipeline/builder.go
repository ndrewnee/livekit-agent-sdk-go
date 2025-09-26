package pipeline

import (
	"fmt"
	"os"
	"strings"

	"github.com/livekit/agent-sdk-go/pkg/egress/config"
)

// Builder builds GStreamer pipeline strings
type Builder struct {
	config    *config.Config
	outputDir string
}

// Build builds the GStreamer pipeline string
func (b *Builder) Build() string {
	var pipeline strings.Builder

	// RTP bin for jitter buffer and sync
	pipeline.WriteString(b.buildRTPBin())
	pipeline.WriteString(" ")

	// Video chain
	pipeline.WriteString(b.buildVideoChain())
	pipeline.WriteString(" ")

	// Audio chain
	pipeline.WriteString(b.buildAudioChain())

	// Optional screenshot branch
	if b.config.Screenshots.Enabled {
		pipeline.WriteString(" ")
		pipeline.WriteString(b.buildScreenshotBranch())
	}

	return pipeline.String()
}

// buildRTPBin builds the RTP bin configuration
func (b *Builder) buildRTPBin() string {
	return fmt.Sprintf("rtpbin name=rtpbin latency=%d do-lost=true drop-on-latency=false",
		b.config.Pipeline.JitterBufferMs)
}

// buildVideoChain builds the video processing chain
func (b *Builder) buildVideoChain() string {
	return fmt.Sprintf(`
		udpsrc port=%d caps="application/x-rtp,media=video,clock-rate=90000,encoding-name=H264"
		! rtpbin.recv_rtp_sink_0
		rtpbin. ! rtph264depay ! h264parse config-interval=-1
		! videorate drop-only=false duplicate-on-gap=true skip-to-first=true
		! video/x-h264,framerate=30/1
		! queue max-size-time=2000000000 leaky=downstream
		! mpegtsmux name=mux alignment=7
		! hlssink2
			location=%s/segment%%05d.ts
			playlist-location=%s/playlist.m3u8
			target-duration=%d
			max-files=0
			send-keyframe-requests=false`,
		b.config.Pipeline.VideoPort,
		b.outputDir,
		b.outputDir,
		b.config.Output.SegmentDuration)
}

// buildAudioChain builds the audio processing chain
func (b *Builder) buildAudioChain() string {
	// Base RTP reception
	base := fmt.Sprintf(`
		udpsrc port=%d caps="application/x-rtp,media=audio,clock-rate=48000,encoding-name=OPUS"
		! rtpbin.recv_rtp_sink_1
		rtpbin. ! rtpopusdepay ! opusparse`,
		b.config.Pipeline.AudioPort)

	// Choose audio processing based on mode
	switch b.config.Audio.Mode {
	case config.AudioTranscodeAAC:
		// Transcode Opus to AAC for maximum compatibility
		return base + fmt.Sprintf(`
		! opusdec
		! audioconvert ! audioresample
		! audio/x-raw,rate=48000,channels=2
		! avenc_aac bitrate=%d compliance=-2
		! aacparse
		! queue max-size-time=2000000000 leaky=downstream
		! mux.`, b.config.Audio.AACBitrate*1000) // Convert kbps to bps

	case config.AudioTranscodeMP3:
		// Transcode Opus to MP3 (lower CPU than AAC)
		return base + fmt.Sprintf(`
		! opusdec
		! audioconvert ! audioresample
		! audio/x-raw,rate=48000,channels=2
		! lamemp3enc target=bitrate bitrate=%d
		! mpegaudioparse
		! queue max-size-time=2000000000 leaky=downstream
		! mux.`, b.config.Audio.MP3Bitrate)

	default: // AudioPassThrough
		// Keep Opus as-is (zero-transcode)
		return base + `
		! audiorate tolerance=40000000 add=true silent=false
		! audio/x-opus,rate=48000,channels=2
		! queue max-size-time=2000000000 leaky=downstream
		! mux.`
	}
}

// buildScreenshotBranch builds the screenshot extraction branch
func (b *Builder) buildScreenshotBranch() string {
	// Create screenshots directory
	screenshotDir := fmt.Sprintf("%s/screenshots", b.outputDir)
	os.MkdirAll(screenshotDir, 0755)

	return fmt.Sprintf(`
		h264parse ! tee name=video_tee
		video_tee. ! queue ! videorate ! mux.
		video_tee. ! queue leaky=downstream
		! avdec_h264 ! videorate ! video/x-raw,framerate=1/%d
		! jpegenc quality=85
		! multifilesink location=%s/frame_%%05d.jpg`,
		b.config.Screenshots.Interval,
		screenshotDir)
}