package agent

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

func TestDefaultShutdownHooks_LogFlushHook(t *testing.T) {
	hooks := DefaultShutdownHooks{}
	logger, _ := zap.NewDevelopment()
	hook := hooks.NewLogFlushHook(logger)

	// Execute the hook; it should ignore /dev/std* sync errors in tests
	if err := hook.Handler(context.Background()); err != nil {
		t.Fatalf("log flush hook returned error: %v", err)
	}
}
