package agent

import (
	"context"
	"testing"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Cover SimpleUniversalHandler methods where function fields are set.
func TestSimpleUniversalHandler_AllCallbacks(t *testing.T) {
	h := &SimpleUniversalHandler{}
	h.ParticipantSpeakingChangedFunc = func(ctx context.Context, p *lksdk.RemoteParticipant, speaking bool) {}
	h.TrackMutedFunc = func(ctx context.Context, pub lksdk.TrackPublication, participant lksdk.Participant) {}
	h.TrackUnmutedFunc = func(ctx context.Context, pub lksdk.TrackPublication, participant lksdk.Participant) {}

	// Calls should route to the function fields with no panic
	h.OnParticipantSpeakingChanged(context.Background(), nil, true)
	h.OnTrackMuted(context.Background(), nil, nil)
	h.OnTrackUnmuted(context.Background(), nil, nil)
}
