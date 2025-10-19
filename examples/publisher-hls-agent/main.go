// Package main implements a LiveKit agent that records publisher tracks to HLS format.
//
// The Publisher HLS Agent subscribes to a target participant's audio and video tracks,
// processes them through a GStreamer pipeline, and generates HLS (HTTP Live Streaming)
// playlists and segments. Optionally, recordings can be uploaded to S3-compatible storage.
//
// # Architecture
//
// The agent implements a delayed pipeline start mechanism to ensure all HLS segments
// are immediately playable:
//  1. Connects to LiveKit room as a recorder participant
//  2. Subscribes to target participant's video and audio tracks
//  3. Waits for first keyframe to establish H.264 parameters (SPS/PPS)
//  4. Activates recording (auto or manual)
//  5. Starts GStreamer pipeline on first recording keyframe
//  6. Generates HLS segments with hlssink element
//  7. Uploads to S3 on completion (if configured)
//
// # GStreamer Pipeline
//
// Video path: appsrc → rtpjitterbuffer → rtph264depay → h264parse → capsfilter → queue
// Audio path: appsrc → rtpjitterbuffer → rtpopusdepay → opusdec → audioconvert → avenc_aac → aacparse → queue
// Mux path: mpegtsmux → tee → [filesink (output.ts), hlssink (segments)]
//
// # Configuration
//
// All configuration is via environment variables. See Config type for details.
//
// Required:
//   - LIVEKIT_API_KEY: LiveKit API key
//   - LIVEKIT_API_SECRET: LiveKit API secret
//
// Optional:
//   - LIVEKIT_URL: LiveKit server URL (default: ws://localhost:7880)
//   - OUTPUT_DIR: Local output directory (default: publisher-hls-output)
//   - AUTO_ACTIVATE_RECORDING: Auto-start recording (default: false)
//   - S3_*: S3 upload configuration (see S3Config)
//
// # Usage
//
//	export LIVEKIT_API_KEY="your-key"
//	export LIVEKIT_API_SECRET="your-secret"
//	export AUTO_ACTIVATE_RECORDING="true"
//	./publisher-hls-agent
//
// Then dispatch a JT_PUBLISHER job to the agent specifying the target participant.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg := loadConfig()

	handler := NewPublisherHLSHandler(cfg)

	worker := agent.NewUniversalWorker(
		cfg.LiveKitURL,
		cfg.APIKey,
		cfg.APISecret,
		handler,
		agent.WorkerOptions{
			AgentName: cfg.AgentName,
			JobType:   livekit.JobType_JT_PUBLISHER,
			MaxJobs:   4,
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("starting publisher HLS agent %q as JT_PUBLISHER worker", cfg.AgentName)
		errCh <- worker.Start(ctx)
	}()

	select {
	case sig := <-sigCh:
		log.Printf("received signal %v, stopping worker", sig)
		cancel()
		_ = worker.Stop()
	case err := <-errCh:
		if err != nil {
			log.Fatalf("worker exited with error: %v", err)
		}
	}

	handler.PrintSummary()
	log.Println("publisher HLS agent stopped")
}
