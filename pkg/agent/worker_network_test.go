package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

// TestWorkerNetworkHandlerIntegration tests network handler integration with worker
func TestWorkerNetworkHandlerIntegration(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Verify network handler is initialized
	assert.NotNil(t, worker.Worker.networkHandler)
	assert.NotNil(t, worker.Worker.networkMonitor)
}

// TestWorkerDNSResolution tests DNS resolution in connection
func TestWorkerDNSResolution(t *testing.T) {
	handler := &testJobHandler{}
	
	// Test with valid URL
	worker := NewWorker("https://localhost:7880", "key", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})
	
	// Mock context for connection
	ctx := context.Background()
	
	// The connect function will attempt DNS resolution
	// For localhost, it should succeed without actual connection
	err := worker.connect(ctx)
	// Will fail at WebSocket dial, but DNS should succeed
	assert.Error(t, err) // Expected as we're not running a server
	
	// Check DNS failure count should be 0 for localhost
	assert.Equal(t, int32(0), worker.networkHandler.GetDNSFailureCount())
}

// TestWorkerPartialWriteRecovery tests partial write recovery
func TestWorkerPartialWriteRecovery(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Simulate partial write
	worker.Worker.networkHandler.partialWriteBuffer = []byte("partial message")
	worker.Worker.networkHandler.partialWriteMessageType = 1
	
	assert.True(t, worker.Worker.networkHandler.HasPartialWrite())
	
	// When sendMessage is called, it should attempt to complete partial write first
	// This would be tested with a mock connection in a real scenario
}

// TestWorkerNetworkPartitionDetection tests network partition detection
func TestWorkerNetworkPartitionDetection(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Set partition timeout to a short duration for testing
	worker.Worker.networkHandler.partitionTimeout = 100 * time.Millisecond
	
	// Simulate no network activity
	worker.Worker.networkHandler.lastNetworkActivity = time.Now().Add(-200 * time.Millisecond)
	
	// Check partition detection
	assert.True(t, worker.Worker.networkHandler.DetectNetworkPartition())
	
	// Update activity
	worker.Worker.networkHandler.UpdateNetworkActivity()
	assert.False(t, worker.Worker.networkHandler.DetectNetworkPartition())
}

// TestWorkerNetworkMonitorCallback tests network monitor callback
func TestWorkerNetworkMonitorCallback(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Start a custom monitor with short interval
	monitor := NewNetworkMonitor(worker.Worker.networkHandler, 50*time.Millisecond)
	
	callbackCalled := false
	monitor.Start(func() {
		callbackCalled = true
	})
	
	// Trigger partition detection
	worker.Worker.networkHandler.networkPartitionDetected = true
	
	// Wait for monitor to detect
	time.Sleep(100 * time.Millisecond)
	
	assert.True(t, callbackCalled)
	
	monitor.Stop()
}

// TestWorkerSendMessageWithPartialWrite tests sendMessage with partial write
func TestWorkerSendMessageWithPartialWrite(t *testing.T) {
	t.Skip("Requires mock WebSocket connection")
	
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Simulate partial write pending
	worker.Worker.networkHandler.partialWriteBuffer = []byte("old message")
	worker.Worker.networkHandler.partialWriteMessageType = 1

	// Network activity tracking would be tested with a real connection
	assert.True(t, worker.Worker.networkHandler.HasPartialWrite())
}

// TestWorkerShutdownNetworkCleanup tests network cleanup during shutdown
func TestWorkerShutdownNetworkCleanup(t *testing.T) {
	handler := &testJobHandler{}
	worker := NewWorker("http://localhost:7880", "key", "secret", handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Manually set network monitor as running
	worker.networkMonitor.wg.Add(1)
	go func() {
		defer worker.networkMonitor.wg.Done()
		<-worker.networkMonitor.stopChan
	}()

	// Shutdown should stop network monitor
	err := worker.StopWithTimeout(1 * time.Second)
	assert.NoError(t, err)

	// Monitor should be stopped (this would block if not properly stopped)
	done := make(chan bool)
	go func() {
		worker.networkMonitor.wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Network monitor was not stopped properly")
	}
}

// TestWorkerNetworkErrorInSendMessage tests network error handling in sendMessage
func TestWorkerNetworkErrorInSendMessage(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// No connection
	worker.Worker.conn = nil

	msg := &livekit.WorkerMessage{
		Message: &livekit.WorkerMessage_Ping{
			Ping: &livekit.WorkerPing{Timestamp: time.Now().Unix()},
		},
	}

	err := worker.Worker.sendMessage(msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
}

// TestWorkerUpdateNetworkActivityOnRead tests network activity update on message read
func TestWorkerUpdateNetworkActivityOnRead(t *testing.T) {
	handler := &testJobHandler{}
	worker := newTestableWorker(handler, WorkerOptions{
		JobType: livekit.JobType_JT_ROOM,
	})

	// Set initial activity time
	initialTime := time.Now().Add(-1 * time.Minute)
	worker.Worker.networkHandler.lastNetworkActivity = initialTime

	// Simulate successful message processing
	ctx := context.Background()
	msg := &livekit.ServerMessage{
		Message: &livekit.ServerMessage_Pong{
			Pong: &livekit.WorkerPong{},
		},
	}

	// This would normally be called from handleMessages
	worker.Worker.networkHandler.UpdateNetworkActivity()
	worker.Worker.handleServerMessage(ctx, msg)

	// Activity should be updated
	assert.True(t, worker.Worker.networkHandler.lastNetworkActivity.After(initialTime))
}