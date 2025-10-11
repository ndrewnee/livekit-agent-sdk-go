package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"
	"github.com/pion/webrtc/v4"
)

type ParticipantRecorder struct {
	participant string
	room        string
	outputDir   string

	pipeline    *gst.Pipeline
	videoAppSrc *app.Source
	audioAppSrc *app.Source

	videoInitialized bool
	audioInitialized bool
	videoEnded       bool
	audioEnded       bool

	videoPacketCount      int
	videoEmptyPacketCount int
	videoKeyframeCount    int
	audioPacketCount      int
	audioEmptyPacketCount int
	videoBytesReceived    int64
	audioBytesReceived    int64

	mu        sync.Mutex
	wg        sync.WaitGroup
	stopOnce  sync.Once
	startTime time.Time
}

type RecordingSummary struct {
	Participant  string
	Room         string
	OutputFile   string
	SizeBytes    int64
	Duration     time.Duration
	Err          error
	VideoPackets int
	AudioPackets int
}

func NewParticipantRecorder(cfg *Config, roomName, participant string) (*ParticipantRecorder, error) {
	baseDir := filepath.Join(cfg.OutputDir, roomName, participant)
	absDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve output directory: %w", err)
	}

	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory %s: %w", absDir, err)
	}

	gst.Init(nil)

	pipelineStr := fmt.Sprintf(`
		filesink location=%s/output.ts name=sink

		mpegtsmux name=mux alignment=7 ! sink.

		appsrc name=videosrc format=time is-live=true do-timestamp=true
		! rtpjitterbuffer latency=200
		! rtph264depay
		! h264parse
		! video/x-h264,stream-format=byte-stream,alignment=au
		! queue max-size-buffers=0 max-size-time=0 max-size-bytes=0
		! mux.

		appsrc name=audiosrc format=time is-live=true do-timestamp=true
		! rtpjitterbuffer latency=200
		! rtpopusdepay
		! opusdec
		! audioconvert
		! audioresample
		! audio/x-raw,rate=48000,channels=2
		! avenc_aac bitrate=128000
		! aacparse
		! queue max-size-buffers=0 max-size-time=0 max-size-bytes=0
		! mux.
	`, absDir)

	pipeline, err := gst.NewPipelineFromString(pipelineStr)
	if err != nil {
		return nil, fmt.Errorf("failed to create GStreamer pipeline: %w", err)
	}

	videoSrcElement, err := pipeline.GetElementByName("videosrc")
	if err != nil {
		return nil, fmt.Errorf("failed to get videosrc: %w", err)
	}
	audioSrcElement, err := pipeline.GetElementByName("audiosrc")
	if err != nil {
		return nil, fmt.Errorf("failed to get audiosrc: %w", err)
	}

	_ = os.Setenv("GST_DEBUG_DUMP_DOT_DIR", absDir)

	recorder := &ParticipantRecorder{
		participant: participant,
		room:        roomName,
		outputDir:   absDir,
		pipeline:    pipeline,
		videoAppSrc: app.SrcFromElement(videoSrcElement),
		audioAppSrc: app.SrcFromElement(audioSrcElement),
		startTime:   time.Now(),
	}

	bus := pipeline.GetPipelineBus()
	bus.AddWatch(func(msg *gst.Message) bool {
		switch msg.Type() {
		case gst.MessageError:
			gerr := msg.ParseError()
			log.Printf("[%s] GStreamer error: %v (debug: %s)", recorder.logPrefix(), gerr.Error(), gerr.DebugString())
			return false
		case gst.MessageWarning:
			gw := msg.ParseWarning()
			log.Printf("[%s] GStreamer warning: %v", recorder.logPrefix(), gw.Error())
		case gst.MessageEOS:
			log.Printf("[%s] GStreamer EOS received", recorder.logPrefix())
			return false
		}
		return true
	})

	return recorder, nil
}

func (r *ParticipantRecorder) logPrefix() string {
	return fmt.Sprintf("%s/%s", r.room, r.participant)
}

func (r *ParticipantRecorder) Start() error {
	log.Printf("[%s] starting GStreamer pipeline", r.logPrefix())
	if err := r.pipeline.SetState(gst.StatePlaying); err != nil {
		return fmt.Errorf("failed to start pipeline: %w", err)
	}
	r.pipeline.DebugBinToDotFileWithTs(gst.DebugGraphShowAll, "publisher_recorder")
	return nil
}

// H.264 NAL unit types used to detect keyframes.
const (
	nalUnitTypeSPS   = 7
	nalUnitTypePPS   = 8
	nalUnitTypeIDR   = 5
	nalUnitTypeSTAPA = 24
	nalUnitTypeFUA   = 28
)

func isH264Keyframe(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	nalType := payload[0] & 0x1F

	switch nalType {
	case nalUnitTypeSPS, nalUnitTypePPS, nalUnitTypeIDR:
		return true
	case nalUnitTypeSTAPA:
		offset := 1
		for offset < len(payload) {
			if offset+2 > len(payload) {
				break
			}
			nalSize := int(payload[offset])<<8 | int(payload[offset+1])
			offset += 2

			if offset+nalSize > len(payload) {
				break
			}
			if nalSize > 0 {
				innerNalType := payload[offset] & 0x1F
				if innerNalType == nalUnitTypeSPS || innerNalType == nalUnitTypePPS || innerNalType == nalUnitTypeIDR {
					return true
				}
			}
			offset += nalSize
		}
		return false
	case nalUnitTypeFUA:
		if len(payload) < 2 {
			return false
		}
		fuHeader := payload[1]
		startBit := (fuHeader & 0x80) != 0
		nalType := fuHeader & 0x1F
		return startBit && nalType == nalUnitTypeIDR
	default:
		return false
	}
}

func (r *ParticipantRecorder) AttachVideoTrack(ctx context.Context, track *webrtc.TrackRemote, pliWriter func(webrtc.SSRC)) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		log.Printf("[%s] video track subscribed (sid=%s, codec=%s, payloadType=%d, ssrc=%d)",
			r.logPrefix(), track.ID(), track.Codec().MimeType, track.PayloadType(), track.SSRC())

		if pliWriter == nil {
			pliWriter = func(webrtc.SSRC) {}
		}

		videoReady := false
		firstPacket := true

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			rtpPacket, _, err := track.ReadRTP()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("[%s] video track read error: %v", r.logPrefix(), err)
				}
				return
			}

			r.mu.Lock()
			r.videoPacketCount++
			r.mu.Unlock()
			if firstPacket {
				firstPacket = false
				log.Printf("[%s] requesting initial keyframe via PLI (seq=%d)", r.logPrefix(), rtpPacket.SequenceNumber)
				pliWriter(track.SSRC())
			}

			if len(rtpPacket.Payload) == 0 {
				r.mu.Lock()
				r.videoEmptyPacketCount++
				r.mu.Unlock()
				continue
			}

			r.mu.Lock()
			r.videoBytesReceived += int64(len(rtpPacket.Payload))
			r.mu.Unlock()

			if !videoReady {
				if isH264Keyframe(rtpPacket.Payload) {
					videoReady = true
					r.mu.Lock()
					r.videoKeyframeCount++
					r.mu.Unlock()
					log.Printf("[%s] received first video keyframe (seq=%d)", r.logPrefix(), rtpPacket.SequenceNumber)
				} else {
					if r.videoBytesReceived == 0 {
						pliWriter(track.SSRC())
					}
					continue
				}
			}

			r.mu.Lock()
			if !r.videoInitialized {
				capsStr := fmt.Sprintf("application/x-rtp,media=video,encoding-name=H264,clock-rate=90000,payload=%d", rtpPacket.PayloadType)
				caps := gst.NewCapsFromString(capsStr)
				r.videoAppSrc.SetProperty("caps", caps)
				r.videoInitialized = true
				log.Printf("[%s] video caps initialized: %s", r.logPrefix(), capsStr)
			}
			r.mu.Unlock()

			data, err := rtpPacket.Marshal()
			if err != nil {
				log.Printf("[%s] marshal video RTP failed: %v", r.logPrefix(), err)
				continue
			}

			buffer := gst.NewBufferFromBytes(data)
			ptsNs := uint64(rtpPacket.Timestamp) * 1_000_000_000 / 90000
			buffer.SetPresentationTimestamp(gst.ClockTime(ptsNs))

			if flow := r.videoAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
				if flow != gst.FlowFlushing {
					log.Printf("[%s] video appsrc push failed: %s", r.logPrefix(), flow.String())
				}
				return
			}
		}
	}()
}

func (r *ParticipantRecorder) AttachAudioTrack(ctx context.Context, track *webrtc.TrackRemote) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		log.Printf("[%s] audio track subscribed (sid=%s, codec=%s, payloadType=%d)",
			r.logPrefix(), track.ID(), track.Codec().MimeType, track.PayloadType())
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			rtpPacket, _, err := track.ReadRTP()
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("[%s] audio track read error: %v", r.logPrefix(), err)
				}
				return
			}
			r.mu.Lock()
			r.audioPacketCount++
			if len(rtpPacket.Payload) == 0 {
				r.audioEmptyPacketCount++
				r.mu.Unlock()
				continue
			}
			r.audioBytesReceived += int64(len(rtpPacket.Payload))
			if !r.audioInitialized {
				capsStr := fmt.Sprintf("application/x-rtp,media=audio,encoding-name=OPUS,clock-rate=48000,payload=%d", rtpPacket.PayloadType)
				caps := gst.NewCapsFromString(capsStr)
				r.audioAppSrc.SetProperty("caps", caps)
				r.audioInitialized = true
				log.Printf("[%s] audio caps initialized: %s", r.logPrefix(), capsStr)
			}
			r.mu.Unlock()

			data, err := rtpPacket.Marshal()
			if err != nil {
				log.Printf("[%s] marshal audio RTP failed: %v", r.logPrefix(), err)
				continue
			}

			buffer := gst.NewBufferFromBytes(data)
			ptsNs := uint64(rtpPacket.Timestamp) * 1_000_000_000 / 48000
			buffer.SetPresentationTimestamp(gst.ClockTime(ptsNs))

			if flow := r.audioAppSrc.PushBuffer(buffer); flow != gst.FlowOK {
				if flow != gst.FlowFlushing {
					log.Printf("[%s] audio appsrc push returned %s", r.logPrefix(), flow.String())
				}
				return
			}
		}
	}()
}

func (r *ParticipantRecorder) VideoStreamEnded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.videoEnded {
		return
	}
	r.videoEnded = true
	if r.videoAppSrc != nil {
		r.videoAppSrc.EndStream()
	}
}

func (r *ParticipantRecorder) AudioStreamEnded() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.audioEnded {
		return
	}
	r.audioEnded = true
	if r.audioAppSrc != nil {
		r.audioAppSrc.EndStream()
	}
}

func (r *ParticipantRecorder) Stop() {
	r.stopOnce.Do(func() {
		log.Printf("[%s] stopping recorder", r.logPrefix())
		r.VideoStreamEnded()
		r.AudioStreamEnded()

		if r.pipeline != nil {
			r.pipeline.SendEvent(gst.NewEOSEvent())
			if bus := r.pipeline.GetBus(); bus != nil {
				bus.TimedPopFiltered(gst.ClockTime(5*1_000_000_000), gst.MessageEOS|gst.MessageError)
			}
			r.pipeline.SetState(gst.StateNull)
		}

		log.Printf("[%s] recorder stats: videoPackets=%d emptyVideo=%d videoBytes=%d audioPackets=%d emptyAudio=%d audioBytes=%d",
			r.logPrefix(),
			r.videoPacketCount,
			r.videoEmptyPacketCount,
			r.videoBytesReceived,
			r.audioPacketCount,
			r.audioEmptyPacketCount,
			r.audioBytesReceived)
	})
	r.wg.Wait()
}

func (r *ParticipantRecorder) OutputDirectory() string {
	return r.outputDir
}

func (r *ParticipantRecorder) Summary() RecordingSummary {
	summary := RecordingSummary{
		Participant: r.participant,
		Room:        r.room,
	}

	outputFile := filepath.Join(r.outputDir, "output.ts")
	summary.OutputFile = outputFile

	stat, err := os.Stat(outputFile)
	if err != nil {
		summary.Err = fmt.Errorf("failed to stat recording: %w", err)
		return summary
	}

	summary.SizeBytes = stat.Size()
	summary.Duration = time.Since(r.startTime)
	summary.VideoPackets = r.videoPacketCount
	summary.AudioPackets = r.audioPacketCount

	return summary
}
