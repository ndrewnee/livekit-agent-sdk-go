package agent

import (
	"testing"

	"go.uber.org/zap"
)

func TestFileDescriptorTracker_LogOpenFiles(t *testing.T) {
	tr := NewFileDescriptorTracker()
	logger := zap.NewExample()
	// Should not panic on systems without /proc; exercises error/info paths
	tr.LogOpenFiles(logger)
}
