package agent

import (
	lksdk "github.com/livekit/server-sdk-go/v2"
	"testing"
)

func TestUniversalWorker_GetPublisherInfo_None(t *testing.T) {
	w := &UniversalWorker{activeJobs: make(map[string]*JobContext)}
	w.activeJobs["j1"] = &JobContext{Room: &lksdk.Room{}}
	if w.GetPublisherInfo() != nil {
		t.Fatalf("expected nil publisher info with no participants")
	}
}
