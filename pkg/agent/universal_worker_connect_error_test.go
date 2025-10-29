package agent

import (
	"context"
	"testing"
)

func TestUniversalWorker_Connect_Error(t *testing.T) {
	w := &UniversalWorker{serverURL: "http://localhost:7880", apiKey: "", apiSecret: "", logger: NewDefaultLogger()}
	if err := w.connect(context.Background()); err == nil {
		t.Fatalf("expected error due to missing credentials")
	}
}
