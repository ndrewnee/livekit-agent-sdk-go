package agent

import (
	"context"
	"testing"
)

func TestUniversalWorker_Reconnect_Error(t *testing.T) {
	w := &UniversalWorker{logger: NewDefaultLogger()}
	if err := w.reconnect(context.Background()); err == nil {
		t.Fatalf("expected error when reconnecting without credentials")
	}
}
