// +build e2e

package egress

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2ERealParticipantsToMinIO tests the complete workflow:
// 1. Create LiveKit room
// 2. Create participants that publish real video/audio from test files
// 3. Start egress worker to capture the room
// 4. Verify HLS output in MinIO
func TestE2ERealParticipantsToMinIO(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real E2E test in short mode")
	}

	// Get configuration
	lkURL := getEnvOrDefault("LIVEKIT_URL", "ws://localhost:7880")
	lkAPIKey := getEnvOrDefault("LIVEKIT_API_KEY", "devkey")
	lkAPISecret := getEnvOrDefault("LIVEKIT_API_SECRET", "secret")

	minioEndpoint := getEnvOrDefault("MINIO_ENDPOINT", "localhost:9000")
	minioAccessKey := getEnvOrDefault("MINIO_ACCESS_KEY", "minioadmin")
	minioSecretKey := getEnvOrDefault("MINIO_SECRET_KEY", "minioadmin")
	minioBucket := getEnvOrDefault("MINIO_BUCKET", "egress-test")

	t.Logf("LiveKit URL: %s", lkURL)
	t.Logf("MinIO Endpoint: %s", minioEndpoint)

	// Create unique room name
	roomName := fmt.Sprintf("e2e-real-test-%d", time.Now().Unix())
	sessionID := fmt.Sprintf("session-%d", time.Now().Unix())

	t.Logf("Creating room: %s", roomName)
	t.Logf("Session ID: %s", sessionID)

	ctx := context.Background()

	// Initialize MinIO client
	minioClient, err := minio.New(minioEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(minioAccessKey, minioSecretKey, ""),
		Secure: false,
	})
	require.NoError(t, err, "Failed to create MinIO client")

	// Ensure bucket exists
	exists, err := minioClient.BucketExists(ctx, minioBucket)
	require.NoError(t, err)
	if !exists {
		err = minioClient.MakeBucket(ctx, minioBucket, minio.MakeBucketOptions{})
		require.NoError(t, err)
		t.Logf("Created bucket: %s", minioBucket)
	}

	// Create LiveKit room
	roomClient := lksdk.NewRoomServiceClient(lkURL, lkAPIKey, lkAPISecret)
	room, err := roomClient.CreateRoom(ctx, &livekit.CreateRoomRequest{
		Name: roomName,
	})
	require.NoError(t, err, "Failed to create room")
	defer roomClient.DeleteRoom(ctx, &livekit.DeleteRoomRequest{Room: roomName})

	t.Logf("Created room: %s (SID: %s)", room.Name, room.Sid)

	// Step 1: Create participant and publish tracks from test files
	// IMPORTANT: Keep participant alive throughout the test
	var participant *lksdk.Room
	t.Run("publish_real_tracks", func(t *testing.T) {
		var err error
		participant, err = connectParticipant(lkURL, lkAPIKey, lkAPISecret, roomName, "participant-1")
		require.NoError(t, err, "Failed to connect participant")

		t.Logf("Participant connected: %s", participant.LocalParticipant.Identity())

		// Publish video track from test file
		videoFile := "../../examples/egress-agent/test-data/test-video-h264.mp4"
		videoTrack, err := publishVideoFromFile(t, participant, videoFile)
		require.NoError(t, err, "Failed to publish video")
		t.Logf("Video track published: %s", videoTrack.SID())

		// Publish audio track from test file
		audioFile := "../../examples/egress-agent/test-data/test-audio-opus.ogg"
		audioTrack, err := publishAudioFromFile(t, participant, audioFile)
		require.NoError(t, err, "Failed to publish audio")
		t.Logf("Audio track published: %s", audioTrack.SID())

		// Wait for tracks to be fully published
		time.Sleep(2 * time.Second)

		// Verify tracks are published
		localTracks := participant.LocalParticipant.TrackPublications()
		assert.GreaterOrEqual(t, len(localTracks), 2, "Should have at least 2 tracks published")
		t.Logf("Published %d tracks total", len(localTracks))
	})

	// Ensure participant disconnects at the end of the test
	defer func() {
		if participant != nil {
			t.Logf("Disconnecting participant...")
			participant.Disconnect()
		}
	}()

	// Wait for tracks to be fully published and ready
	time.Sleep(2 * time.Second)

	// Step 2: Start egress agent to capture the room
	var recordingSession *RecordingSession
	t.Run("start_egress_capture", func(t *testing.T) {
		t.Logf("Starting egress capture for room: %s", roomName)
		t.Logf("Output will go to MinIO: %s/%s/", minioBucket, sessionID)

		// Create egress configuration
		egressConfig := DefaultConfig()
		egressConfig.PipelineConfig.OutputDir = "/tmp/egress-test"
		egressConfig.PipelineConfig.SegmentDuration = 2
		egressConfig.PipelineConfig.AllowAsyncStart = false // Wait for pipeline to reach PAUSED before accepting packets
		egressConfig.RecordingConfig.AutoStart = true
		egressConfig.RecordingConfig.RecordVideo = true
		egressConfig.RecordingConfig.RecordAudio = true
		egressConfig.StorageConfig.Type = "s3"
		egressConfig.StorageConfig.S3Config.Endpoint = minioEndpoint
		egressConfig.StorageConfig.S3Config.AccessKeyID = minioAccessKey
		egressConfig.StorageConfig.S3Config.SecretAccessKey = minioSecretKey
		egressConfig.StorageConfig.S3Config.Bucket = minioBucket
		egressConfig.StorageConfig.S3Config.Region = "us-east-1"
		egressConfig.StorageConfig.S3Config.PathPrefix = sessionID

		// Create a temporary recording session to get callback functions
		// We need to do this in two steps because callbacks must be set at connection time
		tempJobCtx := &agent.JobContext{
			Job: &livekit.Job{
				Id: sessionID,
				Room: &livekit.Room{
					Name: roomName,
					Sid:  room.Sid,
				},
				Type: livekit.JobType_JT_ROOM,
			},
			Room: nil, // Will be set after connection
		}

		// Create recording session (without room yet)
		recordingSession = NewRecordingSession(tempJobCtx, egressConfig)
		require.NotNil(t, recordingSession, "Failed to create recording session")

		// Connect egress agent with RecordingSession callbacks
		egressRoom, err := lksdk.ConnectToRoom(lkURL, lksdk.ConnectInfo{
			APIKey:              lkAPIKey,
			APISecret:           lkAPISecret,
			RoomName:            roomName,
			ParticipantIdentity: fmt.Sprintf("egress-agent-%s", sessionID),
		}, &lksdk.RoomCallback{
			ParticipantCallback: lksdk.ParticipantCallback{
				OnTrackPublished: func(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
					t.Logf("Egress agent sees track published: %s from %s", publication.SID(), rp.Identity())
				},
				OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
					t.Logf("Egress agent subscribed to track: %s from %s", publication.SID(), rp.Identity())
					// Forward to recording session
					if recordingSession == nil {
						t.Logf("ERROR: recordingSession is NIL in callback!")
					} else {
						t.Logf("DEBUG: Calling recordingSession.OnTrackSubscribed for %s", publication.SID())
						recordingSession.OnTrackSubscribed(track, publication, rp)
						t.Logf("DEBUG: OnTrackSubscribed returned")
					}
				},
			},
		})
		require.NoError(t, err, "Failed to connect egress agent to room")

		t.Logf("Egress agent connected as participant: %s", egressRoom.LocalParticipant.Identity())
		t.Logf("Remote participants visible to egress: %d", len(egressRoom.GetRemoteParticipants()))

		// Update the recording session's room reference
		recordingSession.SetRoom(egressRoom)

		// Start the recording session
		err = recordingSession.Start(ctx)
		require.NoError(t, err, "Failed to start recording session")

		t.Logf("Recording session started successfully")

		// Wait for session to subscribe to tracks
		time.Sleep(3 * time.Second)

		// Clean up session after test
		defer func() {
			if recordingSession != nil {
				t.Logf("Stopping recording session...")
				recordingSession.Stop()
			}
			if egressRoom != nil {
				egressRoom.Disconnect()
			}
		}()
	})

	// Step 3: Wait for recording and verify HLS output in MinIO
	t.Run("verify_hls_in_minio", func(t *testing.T) {
		// Wait for recording to happen (need at least 2-3 segments for proper HLS)
		// With 2-second segments, wait at least 10 seconds
		t.Logf("Waiting 15 seconds for recording to generate HLS segments...")
		time.Sleep(15 * time.Second)

		// List objects in MinIO under session path
		objects := listMinIOObjects(t, minioClient, minioBucket, sessionID)
		t.Logf("Found %d objects in MinIO under prefix %s", len(objects), sessionID)

		// Log all found objects for debugging
		for _, obj := range objects {
			t.Logf("  - %s", obj)
		}

		// REAL VERIFICATION - NO SHORTCUTS!
		// Count playlists and segments
		playlistCount := 0
		segmentCount := 0

		for _, obj := range objects {
			ext := filepath.Ext(obj)
			if ext == ".m3u8" {
				playlistCount++
			} else if ext == ".ts" {
				segmentCount++
			}
		}

		// STRICT ASSERTIONS - Test must produce HLS output
		require.Greater(t, playlistCount, 0, "FAILED: No HLS playlist found in MinIO. Recording session did not produce output.")
		require.Greater(t, segmentCount, 0, "FAILED: No HLS segments found in MinIO. Recording session did not produce segments.")

		// Verify we have reasonable amount of segments for 15 seconds of recording
		// With 2-second segments, expect at least 5-6 segments
		assert.GreaterOrEqual(t, segmentCount, 5, "Expected at least 5 segments for 15 seconds of recording")

		// Verify playlist content
		playlistPath := ""
		for _, obj := range objects {
			if filepath.Ext(obj) == ".m3u8" {
				playlistPath = obj
				break
			}
		}

		// Download and verify playlist
		playlistObj, err := minioClient.GetObject(ctx, minioBucket, playlistPath, minio.GetObjectOptions{})
		require.NoError(t, err, "Failed to download playlist from MinIO")
		defer playlistObj.Close()

		playlistData, err := io.ReadAll(playlistObj)
		require.NoError(t, err, "Failed to read playlist content")

		playlistContent := string(playlistData)
		assert.Contains(t, playlistContent, "#EXTM3U", "Playlist must have HLS header")
		assert.Contains(t, playlistContent, "#EXT-X-VERSION", "Playlist must have version")
		assert.Contains(t, playlistContent, ".ts", "Playlist must reference segment files")

		t.Logf("✓ HLS output VERIFIED successfully:")
		t.Logf("  - Playlists: %d", playlistCount)
		t.Logf("  - Segments: %d", segmentCount)
		t.Logf("  - View at: http://%s/%s/%s/playlist.m3u8", minioEndpoint, minioBucket, sessionID)

		// Get recording session stats
		if recordingSession != nil {
			stats := recordingSession.GetStats()
			t.Logf("✓ Recording session stats:")
			t.Logf("  - Duration: %d seconds", stats.Duration)
			t.Logf("  - Packets received: %d", stats.PacketsReceived)
			t.Logf("  - Tracks subscribed: %d", stats.TracksSubscribed)
			t.Logf("  - Bytes received: %d", stats.BytesReceived)

			require.Greater(t, stats.PacketsReceived, uint64(0), "Session must have received packets")
			require.Greater(t, stats.TracksSubscribed, int32(0), "Session must have subscribed to tracks")
		}
	})
}

// connectParticipant connects a participant to a LiveKit room
func connectParticipant(lkURL, apiKey, apiSecret, roomName, identity string) (*lksdk.Room, error) {
	room, err := lksdk.ConnectToRoom(lkURL, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: identity,
	}, &lksdk.RoomCallback{})
	return room, err
}

// publishVideoFromFile publishes a video track from an H.264 MP4 file
func publishVideoFromFile(t *testing.T, room *lksdk.Room, videoFile string) (*lksdk.LocalTrackPublication, error) {
	// Check file exists
	if _, err := os.Stat(videoFile); err != nil {
		return nil, fmt.Errorf("video file not found: %w", err)
	}

	// Extract H.264 stream to temporary file
	h264File := filepath.Join(os.TempDir(), fmt.Sprintf("video-%d.h264", time.Now().Unix()))
	cmd := exec.Command("ffmpeg", "-i", videoFile, "-c:v", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "h264", h264File, "-y")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg extraction failed: %w", err)
	}

	t.Logf("Extracted H.264 to: %s", h264File)

	// Use LiveKit's NewLocalFileTrack - it handles everything automatically!
	// Note: Do NOT use OnWriteComplete callback - it can cause premature disconnection
	track, err := lksdk.NewLocalFileTrack(h264File,
		lksdk.ReaderTrackWithFrameDuration(33*time.Millisecond), // ~30fps
	)
	if err != nil {
		os.Remove(h264File)
		return nil, fmt.Errorf("failed to create video track: %w", err)
	}

	// Publish track with proper options
	publication, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:        "camera",
		Source:      livekit.TrackSource_CAMERA,
		VideoWidth:  1280,
		VideoHeight: 720,
	})
	if err != nil {
		os.Remove(h264File)
		return nil, fmt.Errorf("failed to publish video track: %w", err)
	}

	return publication, nil
}

// publishAudioFromFile publishes an audio track from an Opus OGG file
func publishAudioFromFile(t *testing.T, room *lksdk.Room, audioFile string) (*lksdk.LocalTrackPublication, error) {
	// Check file exists
	if _, err := os.Stat(audioFile); err != nil {
		return nil, fmt.Errorf("audio file not found: %w", err)
	}

	t.Logf("Publishing audio from: %s", audioFile)

	// Use LiveKit's NewLocalFileTrack for Opus - it handles everything!
	// Note: Do NOT use OnWriteComplete callback - it can cause premature disconnection
	track, err := lksdk.NewLocalFileTrack(audioFile,
		lksdk.ReaderTrackWithFrameDuration(20*time.Millisecond), // 20ms Opus frames
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create audio track: %w", err)
	}

	// Publish track with proper options
	publication, err := room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
		Name:   "microphone",
		Source: livekit.TrackSource_MICROPHONE,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to publish audio track: %w", err)
	}

	return publication, nil
}

// listMinIOObjects lists all objects in a MinIO bucket with a prefix
func listMinIOObjects(t *testing.T, client *minio.Client, bucket, prefix string) []string {
	ctx := context.Background()
	var objects []string

	for obj := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			t.Logf("Error listing objects: %v", obj.Err)
			continue
		}
		objects = append(objects, obj.Key)
	}

	return objects
}