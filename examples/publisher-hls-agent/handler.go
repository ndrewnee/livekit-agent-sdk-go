package main

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/am-sokolov/livekit-agent-sdk-go/pkg/agent"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

type PublisherHLSHandler struct {
	agent.BaseHandler
	cfg *Config

	mu                  sync.Mutex
	sessions            map[string]*recordingSession
	summaries           []RecordingSummary
	participantSessions map[string]*recordingSession
	readyOnce           sync.Once
	readyCh             chan struct{}
}

func (h *PublisherHLSHandler) WaitReady(ctx context.Context) error {
	select {
	case <-h.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type recordingSession struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	recorder    *ParticipantRecorder
	participant string
	tracksReady map[webrtc.RTPCodecType]bool
	activated   bool
}

func NewPublisherHLSHandler(cfg *Config) *PublisherHLSHandler {
	return &PublisherHLSHandler{
		cfg:                 cfg,
		sessions:            make(map[string]*recordingSession),
		participantSessions: make(map[string]*recordingSession),
		readyCh:             make(chan struct{}),
	}
}

func (h *PublisherHLSHandler) OnJobRequest(ctx context.Context, job *livekit.Job) (bool, *agent.JobMetadata) {
	if job.Type != livekit.JobType_JT_PUBLISHER {
		log.Printf("rejecting job %s: unsupported type %s", job.Id, job.Type.String())
		return false, nil
	}

	if job.Participant == nil || job.Participant.Identity == "" {
		log.Printf("rejecting job %s: missing participant information", job.Id)
		return false, nil
	}

	log.Printf("accepting JT_PUBLISHER job %s for participant %s in room %s", job.Id, job.Participant.Identity, job.Room.Name)

	return true, &agent.JobMetadata{
		ParticipantIdentity: "",
		ParticipantName:     "publisher-hls-recorder",
		ParticipantMetadata: `{"agent":"publisher-hls-recorder"}`,
	}
}

func (h *PublisherHLSHandler) OnJobAssigned(ctx context.Context, jobCtx *agent.JobContext) error {
	if jobCtx.Job.Participant == nil || jobCtx.Job.Participant.Identity == "" {
		return fmt.Errorf("job %s missing participant identity", jobCtx.Job.Id)
	}

	participantIdentity := jobCtx.Job.Participant.Identity
	roomName := jobCtx.Job.Room.Name

	recorder, err := NewParticipantRecorder(h.cfg, roomName, participantIdentity)
	if err != nil {
		return fmt.Errorf("failed to create recorder: %w", err)
	}

	if state := jobCtx.Job.GetState(); state != nil {
		log.Printf("[debug] job state: participantIdentity=%s status=%s", state.GetParticipantIdentity(), state.GetStatus().String())
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	session := &recordingSession{
		cancel:      cancel,
		recorder:    recorder,
		participant: participantIdentity,
		tracksReady: make(map[webrtc.RTPCodecType]bool),
	}

	recorder.SetOnVideoReady(func() {
		h.notifyReady()
		h.tryAutoActivate(session)
	})

	h.storeSession(jobCtx.Job.Id, participantIdentity, session)

	started := false
	defer func() {
		cancel()
		recorder.Stop()
		h.removeSession(jobCtx.Job.Id)
		if started {
			h.addSummary(recorder.Summary())
		}
	}()

	if err := recorder.Start(); err != nil {
		return fmt.Errorf("failed to start recorder: %w", err)
	}
	started = true

	trackSet := &trackRegistry{seen: make(map[string]struct{})}
	targetIdentity := participantIdentity

	roomCallback := lksdk.NewRoomCallback()
	roomCallback.OnParticipantDisconnected = func(rp *lksdk.RemoteParticipant) {
		if rp.Identity() == targetIdentity {
			log.Printf("[%s/%s] participant disconnected, stopping recording", roomName, targetIdentity)
			cancel()
		}
	}
	roomCallback.OnDisconnected = func() {
		log.Printf("[%s/%s] recorder connection disconnected by server", roomName, targetIdentity)
		cancel()
	}
	roomCallback.ParticipantCallback.OnTrackPublished = func(publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		if rp.Identity() != targetIdentity {
			return
		}
		if err := publication.SetSubscribed(true); err != nil {
			log.Printf("[%s/%s] failed to subscribe to track %s: %v", roomName, targetIdentity, publication.SID(), err)
		} else {
			log.Printf("[%s/%s] requested subscription to track %s", roomName, targetIdentity, publication.SID())
		}
		if publication.Kind() == lksdk.TrackKindVideo {
			publication.SetEnabled(true)
			if err := publication.SetVideoQuality(livekit.VideoQuality_HIGH); err != nil {
				log.Printf("[%s/%s] failed to set video quality for %s: %v", roomName, targetIdentity, publication.SID(), err)
			} else {
				log.Printf("[%s/%s] requested HIGH quality for track %s", roomName, targetIdentity, publication.SID())
			}
		}
	}
	roomCallback.ParticipantCallback.OnTrackSubscribed = func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		if rp.Identity() != targetIdentity {
			return
		}
		if !trackSet.mark(publication.SID()) {
			return
		}
		if err := publication.SetSubscribed(true); err != nil {
			log.Printf("[%s/%s] failed to confirm subscription for track %s: %v", roomName, targetIdentity, publication.SID(), err)
		}
		if publication.Kind() == lksdk.TrackKindVideo {
			if info := publication.TrackInfo(); info != nil {
				log.Printf("[%s/%s] track info: %s", roomName, targetIdentity, info.String())
			}
			if receiver := publication.Receiver(); receiver != nil {
				if params := receiver.GetParameters(); params.Codecs != nil {
					for _, codec := range params.Codecs {
						log.Printf("[%s/%s] receiver codec: mime=%s fmtp=%s", roomName, targetIdentity, codec.MimeType, codec.SDPFmtpLine)
					}
				}
			}
		}
		log.Printf("[%s/%s] track subscribed: sid=%s kind=%s codec=%s payloadType=%d", roomName, targetIdentity, publication.SID(), publication.Kind(), track.Codec().MimeType, track.PayloadType())
		switch track.Kind() {
		case webrtc.RTPCodecTypeVideo:
			recorder.AttachVideoTrack(sessionCtx, track, rp.WritePLI)
			if session, ok := h.getSessionByParticipant(targetIdentity); ok {
				session.setTrackReady(webrtc.RTPCodecTypeVideo)
				h.tryAutoActivate(session)
			}
		case webrtc.RTPCodecTypeAudio:
			recorder.AttachAudioTrack(sessionCtx, track)
			if session, ok := h.getSessionByParticipant(targetIdentity); ok {
				session.setTrackReady(webrtc.RTPCodecTypeAudio)
				h.tryAutoActivate(session)
			}
		default:
			log.Printf("[%s/%s] unsupported track kind %s", roomName, targetIdentity, track.Kind().String())
		}
	}
	roomCallback.ParticipantCallback.OnTrackUnsubscribed = func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		if rp.Identity() != targetIdentity {
			return
		}
		trackSet.unmark(publication.SID())
		switch track.Kind() {
		case webrtc.RTPCodecTypeVideo:
			recorder.VideoStreamEnded()
		case webrtc.RTPCodecTypeAudio:
			recorder.AudioStreamEnded()
		}
	}

	recorderIdentity := jobCtx.Job.State.GetParticipantIdentity()
	if recorderIdentity == "" {
		recorderIdentity = fmt.Sprintf("recorder-%s", jobCtx.Job.Id)
	}

	connectInfo := lksdk.ConnectInfo{
		APIKey:              h.cfg.APIKey,
		APISecret:           h.cfg.APISecret,
		RoomName:            roomName,
		ParticipantIdentity: recorderIdentity,
		ParticipantName:     "HLS Recorder",
		ParticipantMetadata: fmt.Sprintf(`{"agent":"%s"}`, h.cfg.AgentName),
	}

	directRoom, err := lksdk.ConnectToRoom(h.cfg.LiveKitURL, connectInfo, roomCallback, lksdk.WithAutoSubscribe(false))
	if err != nil {
		return fmt.Errorf("failed to connect to room %s: %w", roomName, err)
	}
	defer directRoom.Disconnect()

	if rp := directRoom.GetParticipantByIdentity(targetIdentity); rp != nil {
		for _, pub := range rp.TrackPublications() {
			if remotePub, ok := pub.(*lksdk.RemoteTrackPublication); ok {
				if err := remotePub.SetSubscribed(true); err != nil {
					log.Printf("[%s/%s] failed to subscribe to existing track %s: %v", roomName, targetIdentity, remotePub.SID(), err)
				}
				if remotePub.Kind() == lksdk.TrackKindVideo {
					remotePub.SetEnabled(true)
					if err := remotePub.SetVideoQuality(livekit.VideoQuality_HIGH); err != nil {
						log.Printf("[%s/%s] failed to set video quality for existing track %s: %v", roomName, targetIdentity, remotePub.SID(), err)
					} else {
						log.Printf("[%s/%s] requested HIGH quality for existing track %s", roomName, targetIdentity, remotePub.SID())
					}
				}
			}
		}
	} else {
		log.Printf("[%s/%s] waiting for participant tracks", roomName, targetIdentity)
	}

	select {
	case <-sessionCtx.Done():
	case <-ctx.Done():
	}

	return nil
}

func (h *PublisherHLSHandler) OnJobTerminated(ctx context.Context, jobID string) {
	h.mu.Lock()
	session, ok := h.sessions[jobID]
	h.mu.Unlock()

	if !ok {
		return
	}

	log.Printf("job %s terminated by server", jobID)
	session.cancel()
}

func (h *PublisherHLSHandler) PrintSummary() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.summaries) == 0 {
		log.Println("no recordings completed in this session")
		return
	}

	log.Println("=== Publisher HLS Recording Summary ===")
	for _, summary := range h.summaries {
		if summary.Err != nil {
			log.Printf("• %s in room %s → error: %v", summary.Participant, summary.Room, summary.Err)
			continue
		}
		log.Printf("• %s in room %s → file %s (%.2f MB), captured for %.1fs, packets video=%d audio=%d",
			summary.Participant,
			summary.Room,
			summary.OutputFile,
			float64(summary.SizeBytes)/1_000_000,
			summary.Duration.Seconds(),
			summary.VideoPackets,
			summary.AudioPackets)
	}
}

func (h *PublisherHLSHandler) storeSession(jobID, participant string, session *recordingSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[jobID] = session
	h.participantSessions[participant] = session
}

func (h *PublisherHLSHandler) removeSession(jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[jobID]
	if ok {
		delete(h.participantSessions, session.participant)
	}
	delete(h.sessions, jobID)
}

func (h *PublisherHLSHandler) addSummary(summary RecordingSummary) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.summaries = append(h.summaries, summary)
}

func (h *PublisherHLSHandler) getSessionByParticipant(participant string) (*recordingSession, bool) {
	h.mu.Lock()
	session, ok := h.participantSessions[participant]
	h.mu.Unlock()
	return session, ok
}

func (h *PublisherHLSHandler) notifyReady() {
	h.readyOnce.Do(func() {
		log.Println("handler ready: video track subscription acknowledged")
		close(h.readyCh)
	})
}

func (h *PublisherHLSHandler) ActivateRecording(participant string) error {
	session, ok := h.getSessionByParticipant(participant)
	if !ok {
		return fmt.Errorf("no active session for participant %s", participant)
	}
	log.Printf("activating recording for participant %s", participant)
	session.recorder.ActivateRecording()
	return nil
}

func (h *PublisherHLSHandler) tryAutoActivate(session *recordingSession) {
	if !h.cfg.AutoActivate {
		return
	}

	if !session.recorder.HandshakeReady() {
		return
	}

	if !session.markActivatedIfReady() {
		return
	}

	log.Printf("[%s/%s] auto-activating recording", session.recorder.room, session.participant)
	session.recorder.ActivateRecording()
}

func (s *recordingSession) setTrackReady(kind webrtc.RTPCodecType) {
	s.mu.Lock()
	if s.tracksReady == nil {
		s.tracksReady = make(map[webrtc.RTPCodecType]bool)
	}
	s.tracksReady[kind] = true
	s.mu.Unlock()
}

func (s *recordingSession) markActivatedIfReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.activated {
		return false
	}
	if !s.tracksReady[webrtc.RTPCodecTypeVideo] || !s.tracksReady[webrtc.RTPCodecTypeAudio] {
		return false
	}
	if !s.recorder.HandshakeReady() {
		return false
	}
	s.activated = true
	return true
}

type trackRegistry struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (r *trackRegistry) mark(sid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[sid]; ok {
		return false
	}
	r.seen[sid] = struct{}{}
	return true
}

func (r *trackRegistry) unmark(sid string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.seen, sid)
}
