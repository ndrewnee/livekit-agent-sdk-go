package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

func TestPublisherHLSAgentEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end integration test in short mode")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)

	const (
		agentName           = "publisher-hls-e2e-agent"
		roomName            = "publisher-hls-e2e-room"
		participantIdentity = "publisher-hls-e2e-participant"
	)

	repoRoot := findRepoRoot(t)
	serverBinary := filepath.Join(repoRoot, "livekit", "livekit-server")
	configPath := filepath.Join(repoRoot, "examples", "livekit-server-dev.yaml")
	outputDir := filepath.Join(repoRoot, "examples", "publisher-hls-agent", "hls-agent-recordings")
	testVideo := filepath.Join(repoRoot, "examples", "publisher-hls-agent", "test", "test.mp4")

	requireFileExists(t, serverBinary)
	requireFileExists(t, testVideo)

	if err := os.RemoveAll(outputDir); err != nil {
		t.Fatalf("failed to clean output dir: %v", err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("failed to create output dir: %v", err)
	}

	tempRoot := t.TempDir()
	serverLogPath := filepath.Join(tempRoot, "livekit-server.log")
	agentLogPath := filepath.Join(tempRoot, "publisher-hls-agent.log")
	t.Logf("livekit server log: %s", serverLogPath)
	t.Logf("publisher agent log: %s", agentLogPath)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		if data, err := os.ReadFile(agentLogPath); err == nil {
			t.Logf("publisher agent log contents:\n%s", string(data))
		} else {
			t.Logf("failed to read agent log: %v", err)
		}
		if data, err := os.ReadFile(serverLogPath); err == nil {
			t.Logf("livekit server log contents:\n%s", string(data))
		} else {
			t.Logf("failed to read server log: %v", err)
		}
	})

	serverLogFile, err := os.Create(serverLogPath)
	if err != nil {
		t.Fatalf("failed to create server log file: %v", err)
	}
	defer serverLogFile.Close()

	serverCmd := exec.Command(serverBinary, "--dev", "--config", configPath, "--node-ip", "127.0.0.1")
	serverCmd.Dir = repoRoot
	serverCmd.Stdout = serverLogFile
	serverCmd.Stderr = serverLogFile

	if err := serverCmd.Start(); err != nil {
		t.Fatalf("failed to start livekit server: %v", err)
	}
	t.Cleanup(func() {
		shutdownProcess(t, serverCmd, "livekit-server", 10*time.Second)
	})

	if err := waitForLiveKitServer("localhost:7880", 25*time.Second); err != nil {
		t.Fatalf("livekit server not ready: %v", err)
	}

	agentLogFile, err := os.Create(agentLogPath)
	if err != nil {
		t.Fatalf("failed to create agent log file: %v", err)
	}
	defer agentLogFile.Close()

	agentDir := filepath.Join(repoRoot, "examples", "publisher-hls-agent")
	agentCmd := exec.Command("go", "run", ".")
	agentCmd.Dir = agentDir
	agentCmd.Stdout = agentLogFile
	agentCmd.Stderr = agentLogFile
	agentCmd.Env = append(os.Environ(),
		fmt.Sprintf("LIVEKIT_URL=%s", testLiveKitURL),
		fmt.Sprintf("LIVEKIT_API_KEY=%s", testAPIKey),
		fmt.Sprintf("LIVEKIT_API_SECRET=%s", testAPISecret),
		fmt.Sprintf("OUTPUT_DIR=%s", outputDir),
		fmt.Sprintf("AGENT_NAME=%s", agentName),
		"AUTO_ACTIVATE_RECORDING=true",
		"HLS_SEGMENT_DURATION=2",
		"HLS_MAX_SEGMENTS=0",
	)

	if err := agentCmd.Start(); err != nil {
		t.Fatalf("failed to start publisher agent: %v", err)
	}
	t.Cleanup(func() {
		shutdownProcess(t, agentCmd, "publisher-hls-agent", 15*time.Second)
	})

	if err := waitForLogContains(agentLogPath, "Worker registered", 20*time.Second); err != nil {
		t.Fatalf("agent failed to register: %v", err)
	}

	roomClient := lksdk.NewRoomServiceClient("http://localhost:7880", testAPIKey, testAPISecret)
	_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: roomName})

	_, err = roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: agentName,
				Metadata:  `{"record_audio":true,"record_video":true}`,
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create test room: %v", err)
	}
	defer func() {
		_, _ = roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{Room: roomName})
	}()

	participantRoom, err := lksdk.ConnectToRoom(testLiveKitURL, lksdk.ConnectInfo{
		APIKey:              testAPIKey,
		APISecret:           testAPISecret,
		RoomName:            roomName,
		ParticipantIdentity: participantIdentity,
		ParticipantName:     "Publisher HLS E2E",
	}, &lksdk.RoomCallback{}, lksdk.WithAutoSubscribe(true))
	if err != nil {
		t.Fatalf("failed to connect participant: %v", err)
	}
	defer participantRoom.Disconnect()

	videoTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:    webrtc.MimeTypeH264,
		ClockRate:   90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	})
	if err != nil {
		t.Fatalf("failed to create video track: %v", err)
	}

	audioTrack, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	if err != nil {
		t.Fatalf("failed to create audio track: %v", err)
	}

	videoReady := make(chan struct{})
	audioReady := make(chan struct{})

	videoTrack.OnBind(func() { close(videoReady) })
	audioTrack.OnBind(func() { close(audioReady) })

	if _, err := participantRoom.LocalParticipant.PublishTrack(videoTrack, &lksdk.TrackPublicationOptions{
		Name:   "publisher-e2e-video",
		Source: livekit.TrackSource_CAMERA,
	}); err != nil {
		t.Fatalf("failed to publish video track: %v", err)
	}

	if _, err := participantRoom.LocalParticipant.PublishTrack(audioTrack, &lksdk.TrackPublicationOptions{
		Name: "publisher-e2e-audio",
	}); err != nil {
		t.Fatalf("failed to publish audio track: %v", err)
	}

	select {
	case <-videoReady:
	case <-time.After(10 * time.Second):
		t.Fatal("video track not bound within timeout")
	}

	select {
	case <-audioReady:
	case <-time.After(10 * time.Second):
		t.Fatal("audio track not bound within timeout")
	}

	publisher, err := NewGStreamerPublisher(testVideo, videoTrack, audioTrack)
	if err != nil {
		t.Fatalf("failed to create GStreamer publisher: %v", err)
	}

	if err := publisher.Start(); err != nil {
		t.Fatalf("failed to start GStreamer publisher: %v", err)
	}

	if err := waitForLogContains(agentLogPath, "auto-activating recording", 30*time.Second); err != nil {
		t.Fatalf("recording did not auto-activate: %v", err)
	}

	if err := publisher.Restart(); err != nil {
		t.Fatalf("failed to restart GStreamer publisher: %v", err)
	}

	if err := publisher.Wait(); err != nil {
		t.Fatalf("publisher error: %v", err)
	}

	publisher.Stop()
	participantOutputDir := filepath.Join(outputDir, roomName, participantIdentity)
	outputFile := filepath.Join(participantOutputDir, "output.ts")

	if err := waitForFile(outputFile, 45*time.Second); err != nil {
		t.Fatalf("recording not created: %v", err)
	}

	time.Sleep(3 * time.Second)

	if err := validateRecordingOutput(t, outputFile, testVideo); err != nil {
		t.Fatalf("recording validation failed: %v", err)
	}
}

func requireFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("required file missing %s: %v", path, err)
	}
}

func shutdownProcess(t *testing.T, cmd *exec.Cmd, name string, timeout time.Duration) {
	t.Helper()
	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Signal(syscall.SIGINT)

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Logf("%s exited with error: %v", name, err)
		} else {
			t.Logf("%s exited cleanly", name)
		}
	case <-time.After(timeout):
		t.Logf("%s did not exit after %v, killing", name, timeout)
		_ = cmd.Process.Kill()
		if err := <-done; err != nil {
			t.Logf("%s kill wait error: %v", name, err)
		}
	}
}

func waitForLogContains(path, needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), needle) {
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %q to appear in %s", needle, path)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
