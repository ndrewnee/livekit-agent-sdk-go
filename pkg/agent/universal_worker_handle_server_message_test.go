package agent

import (
	"context"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"testing"
	"time"
)

func TestUniversalWorker_HandleServerMessage_Paths(t *testing.T) {
	w := &UniversalWorker{
		opts:                WorkerOptions{StrictProtocolMode: false, MaxJobs: 1},
		logger:              NewDefaultLogger(),
		handler:             &SimpleUniversalHandler{JobRequestFunc: func(_ context.Context, _ *livekit.Job) (bool, *JobMetadata) { return false, nil }},
		statusQueueChan:     make(chan struct{}, 10),
		reconnectChan:       make(chan struct{}, 1),
		raceProtector:       NewStatusUpdateRaceProtector(),
		loadCalculator:      &DefaultLoadCalculator{},
		activeJobs:          make(map[string]*JobContext),
		participantTrackers: make(map[string]*ParticipantTracker),
		rooms:               make(map[string]*lksdk.Room),
		jobStartTimes:       make(map[string]time.Time),
	}

	// Register -> no-op
	_ = w.handleServerMessage(&livekit.ServerMessage{Message: &livekit.ServerMessage_Register{Register: &livekit.RegisterWorkerResponse{}}})
	// Availability -> calls sendMessage (ErrNotConnected ok)
	_ = w.handleServerMessage(&livekit.ServerMessage{Message: &livekit.ServerMessage_Availability{Availability: &livekit.AvailabilityRequest{Job: &livekit.Job{Id: "j"}}}})
	// Assignment -> spawns handler
	_ = w.handleServerMessage(&livekit.ServerMessage{Message: &livekit.ServerMessage_Assignment{Assignment: &livekit.JobAssignment{Job: &livekit.Job{Id: "j", Room: &livekit.Room{Name: "r"}}}}})
	// Termination -> calls handler
	_ = w.handleServerMessage(&livekit.ServerMessage{Message: &livekit.ServerMessage_Termination{Termination: &livekit.JobTermination{JobId: "j"}}})
	// Pong -> updates health
	_ = w.handleServerMessage(&livekit.ServerMessage{Message: &livekit.ServerMessage_Pong{Pong: &livekit.WorkerPong{}}})
	// Unknown -> ignored in non-strict mode
	_ = w.handleServerMessage(&livekit.ServerMessage{})
}
