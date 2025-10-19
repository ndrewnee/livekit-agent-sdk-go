package agent

import (
	lksdk "github.com/livekit/server-sdk-go/v2"
	"testing"
	"time"
)

func TestWaitForRemoteTrack_Timeout(t *testing.T) {
	trm := NewTestRoomManager()
	room := &lksdk.Room{}
	if _, err := trm.WaitForRemoteTrack(room, "nonexistent", 10*time.Millisecond); err == nil {
		t.Fatalf("expected timeout error")
	}
}
