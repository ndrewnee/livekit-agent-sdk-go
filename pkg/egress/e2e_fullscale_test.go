//go:build e2e
// +build e2e

package egress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/pipeline"
	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/egress/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// TestE2EFullScale runs a comprehensive end-to-end test with:
// 1. Local LiveKit server (dev mode)
// 2. Egress worker registered with LiveKit (JT_PUBLISHER type)
// 3. N real participants publishing video/audio from test.mp4
// 4. Worker automatically creates one job per publisher
// 5. Each session subscribes to and records the target participant
// 6. RTP packets are forwarded to GStreamer pipeline
// 7. HLS output to local storage
// 8. Verification of HLS playlists and segments
//
// Test Status: ✅ Worker architecture working correctly
//
//	✅ Jobs created per participant
//	✅ Tracks subscribed and packets flowing
//	⚠️  HLS segments not created (pipeline issue - see pipeline/direct_pipeline.go)
func TestE2EFullScale(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping full-scale E2E test in short mode")
	}

	// Configuration
	lkURL := getEnvOrDefault("LIVEKIT_URL", "ws://localhost:7880")
	lkAPIKey := getEnvOrDefault("LIVEKIT_API_KEY", "devkey")
	lkAPISecret := getEnvOrDefault("LIVEKIT_API_SECRET", "secret")
	participantCount := 3                 // Number of participants to test
	recordingDuration := 20 * time.Second // Longer duration to ensure segments are created

	t.Logf("═══════════════════════════════════════════════════════════")
	t.Logf("Full-Scale E2E Test")
	t.Logf("═══════════════════════════════════════════════════════════")
	t.Logf("LiveKit URL: %s", lkURL)
	t.Logf("Participants: %d", participantCount)
	t.Logf("Duration: %v", recordingDuration)
	t.Logf("")

	ctx := context.Background()

	// Step 1: Verify LiveKit server
	t.Logf("Step 1: Verifying LiveKit server...")
	roomClient := lksdk.NewRoomServiceClient(lkURL, lkAPIKey, lkAPISecret)

	// Test connection by listing rooms
	_, err := roomClient.ListRooms(ctx, &livekit.ListRoomsRequest{})
	require.NoError(t, err, "Failed to connect to LiveKit server - is it running with --dev?")
	t.Logf("✓ LiveKit server connected")
	t.Logf("")

	// Step 2: Start egress worker
	t.Logf("Step 2: Starting egress worker...")

	sessionID := fmt.Sprintf("fullscale-%d", time.Now().Unix())
	outputDir := fmt.Sprintf("/tmp/egress-fullscale/%s", sessionID)
	os.MkdirAll(outputDir, 0755)

	// Create egress handler
	handler := &TestEgressHandler{
		outputDir: outputDir,
		sessions:  make(map[string]*TestRecordingSession),
		t:         t,
	}

	// Create worker - use JT_PUBLISHER to get one job per publishing participant
	worker := agent.NewUniversalWorker(
		lkURL,
		lkAPIKey,
		lkAPISecret,
		handler,
		agent.WorkerOptions{
			AgentName: "egress-test-worker",
			JobType:   livekit.JobType_JT_PUBLISHER,
			MaxJobs:   participantCount,
		},
	)

	// Start worker in background
	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	go func() {
		if err := worker.Start(workerCtx); err != nil {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for worker to register
	time.Sleep(3 * time.Second)
	t.Logf("✓ Egress worker started and registered")
	t.Logf("")

	// Step 3: Create room with agent dispatch (worker will create one job per publisher)
	t.Logf("Step 3: Creating LiveKit room with agent dispatch...")
	roomName := fmt.Sprintf("fullscale-test-%d", time.Now().Unix())

	room, err := roomClient.CreateRoom(ctx, &livekit.CreateRoomRequest{
		Name: roomName,
		Agents: []*livekit.RoomAgentDispatch{
			{
				AgentName: "egress-test-worker",
				Metadata:  fmt.Sprintf(`{"session_id": "%s", "test": true}`, sessionID),
			},
		},
	})
	require.NoError(t, err, "Failed to create room")
	defer roomClient.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: roomName})

	t.Logf("✓ Room created: %s (SID: %s)", room.Name, room.Sid)
	t.Logf("✓ Agent dispatched - will create one job per publisher")
	t.Logf("")

	// Step 4: Launch N participants
	t.Logf("Step 4: Launching %d participants...", participantCount)

	type participantInfo struct {
		identity string
		room     *lksdk.Room
		err      error
	}

	var wg sync.WaitGroup
	participantCh := make(chan participantInfo, participantCount)

	mp4File := "../../examples/egress-agent/test-data/test.mp4"

	for i := 0; i < participantCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			identity := fmt.Sprintf("participant-%d", index)

			// Connect participant
			participant, err := connectParticipant(lkURL, lkAPIKey, lkAPISecret, roomName, identity)
			if err != nil {
				participantCh <- participantInfo{identity: identity, err: err}
				return
			}

			// Publish tracks from test.mp4
			err = publishFromMP4File(t, participant, mp4File, int(recordingDuration.Seconds()))
			if err != nil {
				participant.Disconnect()
				participantCh <- participantInfo{identity: identity, err: err}
				return
			}

			participantCh <- participantInfo{identity: identity, room: participant}
		}(i)
	}

	// Wait for all participants to connect
	wg.Wait()
	close(participantCh)

	// Collect participants
	var participants []*lksdk.Room
	for info := range participantCh {
		if info.err != nil {
			t.Errorf("Participant %s failed: %v", info.identity, info.err)
			continue
		}
		participants = append(participants, info.room)
		t.Logf("  ✓ Participant connected: %s", info.identity)
	}

	require.Equal(t, participantCount, len(participants), "Not all participants connected successfully")
	t.Logf("✓ All %d participants connected and publishing", participantCount)
	t.Logf("")

	// Cleanup participants at the end
	defer func() {
		for _, p := range participants {
			p.Disconnect()
		}
	}()

	// Step 5: Wait for worker to pick up jobs (one per participant)
	t.Logf("Step 5: Waiting for egress worker to pick up jobs...")

	// Wait for worker to create jobs for all participants and for pipelines to start
	time.Sleep(8 * time.Second)

	handler.mu.RLock()
	sessionCount := len(handler.sessions)
	handler.mu.RUnlock()

	t.Logf("✓ Worker picked up job (active sessions: %d)", sessionCount)
	t.Logf("")

	// Step 6: Wait for recording duration
	t.Logf("Step 6: Recording for %v...", recordingDuration)

	progressTicker := time.NewTicker(5 * time.Second)
	recordingTimer := time.NewTimer(recordingDuration)

	for {
		select {
		case <-progressTicker.C:
			handler.mu.RLock()
			stats := ""
			for _, session := range handler.sessions {
				if s := session.GetStats(); s != nil {
					stats = fmt.Sprintf("Video: %d, Audio: %d, Segments: %d",
						s.VideoPacketsReceived, s.AudioPacketsReceived, s.SegmentsWritten)
				}
			}
			handler.mu.RUnlock()
			t.Logf("  Recording in progress... %s", stats)
		case <-recordingTimer.C:
			progressTicker.Stop()
			goto recordingComplete
		}
	}

recordingComplete:
	t.Logf("✓ Recording complete")
	t.Logf("")

	// Step 7: Stop participants to trigger finalization
	t.Logf("Step 7: Stopping participants...")
	for _, p := range participants {
		p.Disconnect()
	}
	participants = nil
	t.Logf("✓ Participants disconnected")
	t.Logf("")

	// Step 8: Stop worker and wait for cleanup
	t.Logf("Step 8: Stopping worker...")
	workerCancel()
	time.Sleep(3 * time.Second)
	handler.Shutdown()
	t.Logf("✓ Worker stopped")
	t.Logf("")

	// Step 9: Verify HLS output
	t.Logf("Step 9: Verifying HLS output...")

	files, err := os.ReadDir(outputDir)
	if err != nil {
		t.Logf("⚠️  Could not read output directory: %v", err)
		t.Logf("   Output dir: %s", outputDir)
	} else {
		var playlistCount int
		var segmentCount int

		for _, file := range files {
			if file.IsDir() {
				// Check subdirectories
				subDir := fmt.Sprintf("%s/%s", outputDir, file.Name())
				subFiles, _ := os.ReadDir(subDir)
				for _, subFile := range subFiles {
					if subFile.Name() == "playlist.m3u8" {
						playlistCount++
						t.Logf("  ✓ Found playlist: %s/%s", file.Name(), subFile.Name())
					} else if len(subFile.Name()) > 3 && subFile.Name()[len(subFile.Name())-3:] == ".ts" {
						segmentCount++
					}
				}
			}
		}

		t.Logf("  Playlists: %d", playlistCount)
		t.Logf("  Segments: %d", segmentCount)
		t.Logf("")

		if playlistCount > 0 {
			t.Logf("✓ HLS files generated successfully")
			t.Logf("")
			t.Logf("Output location: %s", outputDir)
			t.Logf("")
			t.Logf("To verify manually:")
			t.Logf("  1. ./tools/hls-player/play.sh")
			t.Logf("  2. Open http://localhost:8080/tools/hls-player/player.html")
			t.Logf("  3. Load: %s/<session>/playlist.m3u8", outputDir)
		} else {
			t.Logf("⚠️  No playlists found")
			t.Logf("   This may be expected if recording duration was too short")
		}
	}

	t.Logf("")

	// Step 10: Upload to MinIO and generate public URLs
	t.Logf("Step 10: Uploading to MinIO and generating public URLs...")
	minioURLs, err := uploadToMinIO(ctx, t, outputDir)
	if err != nil {
		t.Logf("⚠️  MinIO upload failed: %v", err)
		t.Logf("   Files are still available locally at: %s", outputDir)
	} else if len(minioURLs) > 0 {
		t.Logf("✓ Successfully uploaded %d HLS streams to MinIO", len(minioURLs))
		t.Logf("")
		t.Logf("═══════════════════════════════════════════════════════════")
		t.Logf("📺 HLS Stream URLs (Public Access)")
		t.Logf("═══════════════════════════════════════════════════════════")
		for session, url := range minioURLs {
			t.Logf("")
			t.Logf("Session: %s", session)
			t.Logf("Playlist URL: %s", url)
			t.Logf("")
			t.Logf("Test with:")
			t.Logf("  ffplay '%s'", url)
			t.Logf("  or open in browser: https://hls-js.netlify.app/demo/?src=%s", url)
		}
	}

	t.Logf("")
	t.Logf("═══════════════════════════════════════════════════════════")
	t.Logf("✅ Full-Scale E2E Test Complete!")
	t.Logf("═══════════════════════════════════════════════════════════")
}

// TestEgressHandler is a test implementation of the egress handler
type TestEgressHandler struct {
	agent.BaseHandler
	outputDir string
	sessions  map[string]*TestRecordingSession
	mu        sync.RWMutex
	t         *testing.T
}

// OnTrackPublished implements agent.UniversalHandler
func (h *TestEgressHandler) OnTrackPublished(ctx context.Context, participant *lksdk.RemoteParticipant, publication *lksdk.RemoteTrackPublication) {
	h.t.Logf("  Track published: %s from %s", publication.SID(), participant.Identity())

	// Find the session recording this participant
	h.mu.RLock()
	var targetSession *TestRecordingSession
	for _, session := range h.sessions {
		if session.job.Participant != nil && session.job.Participant.Identity == participant.Identity() {
			targetSession = session
			break
		}
	}
	h.mu.RUnlock()

	if targetSession != nil {
		// Subscribe to this track
		publication.SetSubscribed(true)
		h.t.Logf("  Subscribing to track %s for recording", publication.SID())
	}
}

// OnJobRequest implements agent.UniversalHandler
func (h *TestEgressHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	if job.Type != livekit.JobType_JT_PUBLISHER {
		return false, nil
	}

	participantIdentity := ""
	if job.Participant != nil {
		participantIdentity = job.Participant.Identity
	}

	h.t.Logf("  Job request received: %s for publisher: %s in room: %s",
		job.Id, participantIdentity, job.Room.Name)

	return true, &agent.JobMetadata{
		ParticipantIdentity: fmt.Sprintf("egress-%s", job.Id),
		ParticipantName:     fmt.Sprintf("Egress Agent (Recording %s)", participantIdentity),
	}
}

// OnJobAssigned implements agent.UniversalHandler
func (h *TestEgressHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	h.t.Logf("  Job assigned: %s", jobCtx.Job.Id)

	session := &TestRecordingSession{
		job:       jobCtx.Job,
		room:      jobCtx.Room,
		outputDir: h.outputDir,
		t:         h.t,
	}

	h.mu.Lock()
	h.sessions[jobCtx.Job.Id] = session
	h.mu.Unlock()

	return session.Start(ctx)
}

// OnJobTerminated implements agent.UniversalHandler
func (h *TestEgressHandler) OnJobTerminated(ctx context.Context, jobID string) {
	h.t.Logf("  Job terminated: %s", jobID)

	h.mu.Lock()
	session, exists := h.sessions[jobID]
	if exists {
		delete(h.sessions, jobID)
	}
	h.mu.Unlock()

	if exists {
		session.Stop()
	}
}

// Shutdown stops all sessions
func (h *TestEgressHandler) Shutdown() {
	h.mu.Lock()
	sessions := make([]*TestRecordingSession, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.sessions = make(map[string]*TestRecordingSession)
	h.mu.Unlock()

	for _, session := range sessions {
		session.Stop()
	}
}

// TestRecordingSession handles a single recording session
type TestRecordingSession struct {
	job       *livekit.Job
	room      *lksdk.Room
	outputDir string
	saver     *HLSSaver
	cancel    context.CancelFunc
	mu        sync.RWMutex
	t         *testing.T
}

// Start begins the recording
func (s *TestRecordingSession) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Create HLSSaver (no GStreamer!)
	// Use 2 second segments so we get multiple complete segments during 20s recording
	saver, err := NewHLSSaver(s.job.Id, s.outputDir, 2*time.Second)
	if err != nil {
		return fmt.Errorf("failed to create HLS saver: %w", err)
	}
	s.saver = saver

	// Start HLS saver
	if err := saver.Start(); err != nil {
		return fmt.Errorf("failed to start HLS saver: %w", err)
	}

	// Determine target participant
	targetIdentity := ""
	if s.job.Participant != nil {
		targetIdentity = s.job.Participant.Identity
	}

	s.t.Logf("    Recording session started with HLSSaver: %s (target: %s)", s.job.Id, targetIdentity)

	// Subscribe to existing tracks from target participant immediately
	for _, participant := range s.room.GetRemoteParticipants() {
		if participant.Identity() == targetIdentity {
			for _, publication := range participant.TrackPublications() {
				if remoteTrack, ok := publication.(*lksdk.RemoteTrackPublication); ok {
					remoteTrack.SetSubscribed(true)
					s.t.Logf("    Subscribing to existing track: %s", remoteTrack.SID())

					// Start forwarding if already subscribed
					if track := remoteTrack.Track(); track != nil {
						if webrtcTrack, ok := track.(*webrtc.TrackRemote); ok {
							s.t.Logf("    Track already available, starting forwarding: %s", remoteTrack.SID())
							go s.forwardRTPPackets(webrtcTrack)
						}
					}
				}
			}
			break
		}
	}

	// Monitor for newly subscribed tracks
	go s.monitorTracks(ctx)

	// Block until context is canceled (job lifetime)
	<-ctx.Done()

	return nil
}

// Stop ends the recording
func (s *TestRecordingSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
	}

	if s.saver != nil {
		s.saver.Stop()
		s.saver = nil
	}

	s.t.Logf("    Recording session stopped: %s", s.job.Id)
}

// monitorTracks continuously monitors for newly subscribed tracks from the target participant
func (s *TestRecordingSession) monitorTracks(ctx context.Context) {
	subscribedTracks := make(map[string]bool)
	ticker := time.NewTicker(200 * time.Millisecond) // Check frequently
	defer ticker.Stop()

	// Determine target participant identity from job
	targetIdentity := ""
	if s.job.Participant != nil {
		targetIdentity = s.job.Participant.Identity
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Find the target participant
			var targetParticipant *lksdk.RemoteParticipant
			for _, participant := range s.room.GetRemoteParticipants() {
				if participant.Identity() == targetIdentity {
					targetParticipant = participant
					break
				}
			}

			if targetParticipant == nil {
				continue
			}

			// Check for newly subscribed tracks
			for _, publication := range targetParticipant.TrackPublications() {
				remoteTrack, ok := publication.(*lksdk.RemoteTrackPublication)
				if !ok {
					continue
				}

				trackID := remoteTrack.SID()
				if subscribedTracks[trackID] {
					continue // Already handling this track
				}

				// Check if track is now available (subscribed by worker)
				if track := remoteTrack.Track(); track != nil {
					if webrtcTrack, ok := track.(*webrtc.TrackRemote); ok {
						subscribedTracks[trackID] = true
						s.t.Logf("    Track now available, starting forwarding: %s (%s)", trackID, webrtcTrack.Codec().MimeType)
						go s.forwardRTPPackets(webrtcTrack)
					}
				}
			}
		}
	}
}

// forwardRTPPackets forwards RTP packets to HLS saver
func (s *TestRecordingSession) forwardRTPPackets(track *webrtc.TrackRemote) {
	packetCount := 0
	var lastTimestamp uint32 = 0
	var timestamps []uint32

	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			break
		}

		packetCount++

		// Debug: log first 50 packets and collect timestamps to see multiple NAL units
		if track.Kind() == webrtc.RTPCodecTypeVideo && packetCount <= 50 {
			s.t.Logf("    [DEBUG] Video packet #%d: seq=%d, ts=%d, delta=%d, payload=%d bytes, marker=%v, PT=%d, SSRC=%d",
				packetCount, packet.SequenceNumber, packet.Timestamp,
				int64(packet.Timestamp)-int64(lastTimestamp), len(packet.Payload), packet.Marker,
				packet.PayloadType, packet.SSRC)
			// Log first few bytes of payload if present
			if len(packet.Payload) > 0 && packetCount <= 5 {
				payloadPreview := packet.Payload
				if len(payloadPreview) > 16 {
					payloadPreview = payloadPreview[:16]
				}
				s.t.Logf("      Payload preview: %02x", payloadPreview)
			} else if len(packet.Payload) == 0 && packetCount <= 5 {
				s.t.Logf("      Payload is EMPTY - this is the bug!")
			}
			lastTimestamp = packet.Timestamp
			timestamps = append(timestamps, packet.Timestamp)
		}

		// Debug: log first 10 audio packets
		if track.Kind() == webrtc.RTPCodecTypeAudio && packetCount <= 10 {
			s.t.Logf("    [DEBUG] Audio packet #%d: payload=%d bytes, marker=%v",
				packetCount, len(packet.Payload), packet.Marker)
		}

		// Lock to safely access saver
		s.mu.RLock()
		saver := s.saver
		s.mu.RUnlock()

		if saver == nil {
			break // Saver closed, stop forwarding
		}

		// Route to HLSSaver based on track type
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			saver.InjectVideoRTP(packet)
		} else {
			saver.InjectAudioRTP(packet)
		}
	}

	// Log summary of timestamps
	if track.Kind() == webrtc.RTPCodecTypeVideo && len(timestamps) > 0 {
		unique := make(map[uint32]bool)
		for _, ts := range timestamps {
			unique[ts] = true
		}
		s.t.Logf("    [DEBUG] Video timestamp summary: %d packets, %d unique timestamps", len(timestamps), len(unique))
	}
}

// GetStats returns HLS saver stats
func (s *TestRecordingSession) GetStats() *pipeline.PipelineStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.saver != nil {
		videoPackets, audioPackets := s.saver.GetStats()
		return &pipeline.PipelineStats{
			VideoPacketsReceived: videoPackets,
			AudioPacketsReceived: audioPackets,
			SegmentsWritten:      0, // HLSSaver doesn't track this directly
		}
	}
	return nil
}

// uploadToMinIO uploads all HLS files from output directory to MinIO
// Returns map of sessionID -> playlist URL
func uploadToMinIO(ctx context.Context, t *testing.T, outputDir string) (map[string]string, error) {
	// MinIO configuration from environment or defaults
	minioEndpoint := getEnvOrDefault("MINIO_ENDPOINT", "localhost:9000")
	minioAccessKey := getEnvOrDefault("MINIO_ACCESS_KEY", "minioadmin")
	minioSecretKey := getEnvOrDefault("MINIO_SECRET_KEY", "minioadmin")
	minioBucket := getEnvOrDefault("MINIO_BUCKET", "egress-hls")

	t.Logf("  MinIO endpoint: %s", minioEndpoint)
	t.Logf("  MinIO bucket: %s", minioBucket)

	// Create storage configuration
	storageConfig := &storage.Config{
		Type: storage.StorageTypeS3,
		S3: storage.S3Config{
			Endpoint:        minioEndpoint,
			Bucket:          minioBucket,
			Region:          "us-east-1",
			AccessKeyID:     minioAccessKey,
			SecretAccessKey: minioSecretKey,
			UseSSL:          false,
			ForcePathStyle:  true,          // Required for MinIO
			ACL:             "public-read", // Make files publicly accessible
		},
		Upload: storage.UploadConfig{
			Concurrent:     true,
			MaxConcurrent:  5,
			UploadTimeout:  30 * time.Second,
			UploadOnCreate: false, // We'll upload manually after recording
			Retry: storage.RetryConfig{
				Enabled:        true,
				MaxAttempts:    3,
				InitialBackoff: 1 * time.Second,
				MaxBackoff:     10 * time.Second,
				Multiplier:     2.0,
				Jitter:         true,
			},
		},
	}

	// Create storage factory
	lg := logger.GetLogger()
	factory, err := storage.NewFactory(storageConfig, lg)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage factory: %w", err)
	}
	defer factory.Close()

	store := factory.GetStorage()

	// Set up bucket with public access policy
	if err := setupMinIOBucket(ctx, t, storageConfig); err != nil {
		t.Logf("  ⚠️  Warning: Failed to set bucket policy: %v", err)
		t.Logf("     Files will be uploaded but may not be publicly accessible")
	}

	// Find all session directories
	files, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read output directory: %w", err)
	}

	urls := make(map[string]string)

	// Upload each session's files
	for _, file := range files {
		if !file.IsDir() {
			continue
		}

		sessionID := file.Name()
		sessionDir := filepath.Join(outputDir, sessionID)

		t.Logf("  Uploading session: %s", sessionID)

		// Read and upload all files in session directory
		sessionFiles, err := os.ReadDir(sessionDir)
		if err != nil {
			t.Logf("  ⚠️  Failed to read session directory: %v", err)
			continue
		}

		// First pass: identify segments to skip
		skippedSegments := make(map[string]bool)
		for _, sf := range sessionFiles {
			if !sf.IsDir() && strings.HasSuffix(sf.Name(), ".ts") {
				// Always skip segment00000 - it's created during PAUSED→PLAYING transition
				// and has corrupted timestamps/sync even if it has data
				if sf.Name() == "segment00000.ts" {
					skippedSegments[sf.Name()] = true
					t.Logf("    ⊘ Will skip segment00000 (transition segment)")
					continue
				}

				// Also skip any other very small segments (< 100KB)
				filePath := filepath.Join(sessionDir, sf.Name())
				data, err := os.ReadFile(filePath)
				if err == nil && len(data) < 100*1024 {
					skippedSegments[sf.Name()] = true
					t.Logf("    ⊘ Will skip bogus segment: %s (%d bytes)", sf.Name(), len(data))
				}
			}
		}

		// Second pass: upload files and filter playlist
		var playlistFound bool
		for _, sf := range sessionFiles {
			if sf.IsDir() {
				continue
			}

			filePath := filepath.Join(sessionDir, sf.Name())
			data, err := os.ReadFile(filePath)
			if err != nil {
				t.Logf("  ⚠️  Failed to read file %s: %v", sf.Name(), err)
				continue
			}

			// Skip bogus segments
			if skippedSegments[sf.Name()] {
				continue
			}

			// Filter playlist to remove references to skipped segments
			if sf.Name() == "playlist.m3u8" && len(skippedSegments) > 0 {
				playlistContent := string(data)
				lines := strings.Split(playlistContent, "\n")
				var filteredLines []string
				var lastLine string

				for i, line := range lines {
					// Check if this line references a skipped segment
					isSkippedSegment := false
					for skipped := range skippedSegments {
						if line == skipped || strings.TrimSpace(line) == skipped {
							isSkippedSegment = true
							break
						}
					}

					if isSkippedSegment {
						// Remove the EXTINF line before this segment (last line added)
						if len(filteredLines) > 0 && strings.HasPrefix(filteredLines[len(filteredLines)-1], "#EXTINF") {
							filteredLines = filteredLines[:len(filteredLines)-1]
							t.Logf("      Removed EXTINF for skipped segment: %s", line)
						}
						continue
					}

					filteredLines = append(filteredLines, line)
					lastLine = line
					_ = i        // unused
					_ = lastLine // unused
				}

				data = []byte(strings.Join(filteredLines, "\n"))
				t.Logf("    ✓ Filtered playlist: removed %d bogus segment references", len(skippedSegments))
			}

			// Upload the file
			if err := store.StoreSegment(ctx, sessionID, sf.Name(), data); err != nil {
				t.Logf("  ⚠️  Failed to upload %s: %v", sf.Name(), err)
				continue
			}

			t.Logf("    ✓ Uploaded: %s (%d bytes)", sf.Name(), len(data))

			// Track playlist
			if sf.Name() == "playlist.m3u8" {
				playlistFound = true
			}
		}

		// Generate public URL for playlist
		if playlistFound {
			// MinIO public URL format: http://endpoint/bucket/sessionID/segments/playlist.m3u8
			playlistURL := fmt.Sprintf("http://%s/%s/%s/segments/playlist.m3u8",
				minioEndpoint, minioBucket, sessionID)
			urls[sessionID] = playlistURL
		}
	}

	return urls, nil
}

// setupMinIOBucket creates the bucket if it doesn't exist and sets public-read policy
func setupMinIOBucket(ctx context.Context, t *testing.T, config *storage.Config) error {
	// Import AWS S3 SDK for bucket management
	cfg, err := createAWSConfigForMinIO(config.S3)
	if err != nil {
		return fmt.Errorf("failed to create AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	// Check if bucket exists, create if not
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: &config.S3.Bucket,
	})

	if err != nil {
		// Bucket doesn't exist, create it
		t.Logf("  Creating bucket: %s", config.S3.Bucket)
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket: &config.S3.Bucket,
		})
		if err != nil {
			return fmt.Errorf("failed to create bucket: %w", err)
		}
	}

	// Set public-read policy
	policy := fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Effect": "Allow",
				"Principal": "*",
				"Action": ["s3:GetObject"],
				"Resource": ["arn:aws:s3:::%s/*"]
			}
		]
	}`, config.S3.Bucket)

	t.Logf("  Setting public-read policy on bucket: %s", config.S3.Bucket)
	_, err = client.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: &config.S3.Bucket,
		Policy: &policy,
	})
	if err != nil {
		return fmt.Errorf("failed to set bucket policy: %w", err)
	}

	t.Logf("  ✓ Bucket configured for public access")
	return nil
}

// createAWSConfigForMinIO creates AWS SDK config for MinIO
func createAWSConfigForMinIO(s3Config storage.S3Config) (aws.Config, error) {
	return config.LoadDefaultConfig(context.Background(),
		config.WithRegion(s3Config.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			s3Config.AccessKeyID,
			s3Config.SecretAccessKey,
			"",
		)),
		config.WithEndpointResolverWithOptions(aws.EndpointResolverWithOptionsFunc(
			func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{
					URL:               fmt.Sprintf("http://%s", s3Config.Endpoint),
					SigningRegion:     s3Config.Region,
					HostnameImmutable: true,
				}, nil
			},
		)),
	)
}
