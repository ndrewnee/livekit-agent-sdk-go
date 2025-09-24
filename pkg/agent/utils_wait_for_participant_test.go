package agent

import (
	"context"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"testing"
	"time"
)

func TestJobUtils_WaitForParticipant_Timeout(t *testing.T) {
	jc := NewJobUtils(context.Background(), &livekit.Job{}, &lksdk.Room{})
	_, err := jc.WaitForParticipant("nobody", 20*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
}
