package agent

import (
	"context"
	"errors"
	"testing"
)

func TestShutdownHandler_Basic(t *testing.T) {
	sh := NewShutdownHandler(NewDefaultLogger())
	// Add hooks
	_ = sh.AddHook(ShutdownPhasePreStop, ShutdownHook{Name: "h1", Handler: func(context.Context) error { return nil }})
	_ = sh.AddHook(ShutdownPhasePreStop, ShutdownHook{Name: "h2", Handler: func(context.Context) error { return errors.New("x") }})
	// Execute
	_ = sh.ExecutePhase(context.Background(), ShutdownPhasePreStop)
	// Remove
	if !sh.RemoveHook(ShutdownPhasePreStop, "h1") {
		t.Fatalf("expected remove true")
	}
	_ = sh.GetHooks(ShutdownPhasePreStop)
}
