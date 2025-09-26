package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/livekit/agent-sdk-go/pkg/egress/config"
)

// State represents the state of the GStreamer pipeline
type State int

const (
	StateStopped State = iota
	StatePlaying
	StatePaused
)

// Manager manages the GStreamer pipeline
type Manager struct {
	config    *config.Config
	process   *exec.Cmd
	state     State
	stateMu   sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	sessionID string
}

// New creates a new pipeline manager
func New(cfg *config.Config, sessionID string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		config:    cfg,
		state:     StateStopped,
		ctx:       ctx,
		cancel:    cancel,
		sessionID: sessionID,
	}
}

// Start starts the GStreamer pipeline
func (m *Manager) Start() error {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	if m.state != StateStopped {
		return fmt.Errorf("pipeline already running")
	}

	// Create output directory if it doesn't exist
	outputDir := fmt.Sprintf("%s/%s", m.config.Output.Dir, m.sessionID)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Build pipeline string
	builder := &Builder{config: m.config, outputDir: outputDir}
	pipelineStr := builder.Build()

	log.Printf("Starting GStreamer pipeline for session %s", m.sessionID)

	// Start GStreamer process
	m.process = exec.CommandContext(m.ctx, "gst-launch-1.0", "-e", pipelineStr)

	// Set environment for debugging
	m.process.Env = append(os.Environ(),
		"GST_DEBUG=2",
		fmt.Sprintf("GST_DEBUG_FILE=%s/gstreamer.log", outputDir),
	)

	// Capture stderr for debugging
	m.process.Stderr = os.Stderr

	// Start process
	if err := m.process.Start(); err != nil {
		return fmt.Errorf("failed to start GStreamer: %w", err)
	}

	m.state = StatePlaying

	// Monitor process
	go m.monitorProcess()

	return nil
}

// Stop stops the GStreamer pipeline
func (m *Manager) Stop() error {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	if m.state == StateStopped {
		return nil
	}

	log.Printf("Stopping GStreamer pipeline for session %s", m.sessionID)

	m.cancel() // Cancel context

	if m.process != nil && m.process.Process != nil {
		// Send SIGTERM for graceful shutdown
		if err := m.process.Process.Signal(syscall.SIGTERM); err != nil {
			// Force kill if SIGTERM fails
			m.process.Process.Kill()
		}

		// Wait for process to exit with timeout
		done := make(chan error, 1)
		go func() {
			done <- m.process.Wait()
		}()

		select {
		case <-done:
			// Process exited
		case <-time.After(5 * time.Second):
			// Force kill after timeout
			m.process.Process.Kill()
		}
	}

	m.state = StateStopped
	return nil
}

// GetState returns the current pipeline state
func (m *Manager) GetState() State {
	m.stateMu.RLock()
	defer m.stateMu.RUnlock()
	return m.state
}

// monitorProcess monitors the GStreamer process
func (m *Manager) monitorProcess() {
	err := m.process.Wait()

	m.stateMu.Lock()
	m.state = StateStopped
	m.stateMu.Unlock()

	if err != nil && m.ctx.Err() == nil {
		// Process crashed, not intentional stop
		log.Printf("GStreamer process crashed for session %s: %v", m.sessionID, err)
		// Implement restart logic here
		m.handleCrash()
	}
}

// handleCrash handles pipeline crashes with exponential backoff restart
func (m *Manager) handleCrash() {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for attempt := 1; attempt <= 3; attempt++ {
		log.Printf("Attempting to restart GStreamer for session %s (attempt %d/3)", m.sessionID, attempt)

		time.Sleep(backoff)

		if err := m.Start(); err == nil {
			log.Printf("GStreamer restarted successfully for session %s", m.sessionID)
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	log.Printf("Failed to restart GStreamer for session %s after 3 attempts", m.sessionID)
}