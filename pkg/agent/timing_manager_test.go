package agent

import (
	"context"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestTimingManager_ClockSkewAndDeadlines(t *testing.T) {
	tm := NewTimingManager(zap.NewNop(), TimingManagerOptions{})

	// Set and get deadline without skew first
	jobID := "job-1"
	deadline := time.Now().Add(200 * time.Millisecond)
	tm.SetDeadline(jobID, deadline, "unit-test")
	dctx, ok := tm.GetDeadline(jobID)
	assert.True(t, ok)
	assert.WithinDuration(t, deadline, dctx.OriginalDeadline, time.Second)

	// CheckDeadline before expiry
	exceeded, rem := tm.CheckDeadline(jobID)
	assert.False(t, exceeded)
	assert.True(t, rem > 0)

	// After expiry
	time.Sleep(220 * time.Millisecond)
	exceeded, overBy := tm.CheckDeadline(jobID)
	assert.True(t, exceeded)
	assert.True(t, overBy >= 0)

	// Propagate deadline creates a context
	ctx := context.Background()
	ctx2, cancel := tm.PropagateDeadline(ctx, jobID)
	cancel()
	assert.NotNil(t, ctx2)

	// Remove
	tm.RemoveDeadline(jobID)
	_, ok = tm.GetDeadline(jobID)
	assert.False(t, ok)

	// Now simulate clock skew and verify ServerTimeNow offset
	recv := time.Now()
	server := recv.Add(2 * time.Second)
	tm.UpdateServerTime(server, recv)
	delta := tm.ServerTimeNow().Sub(time.Now())
	assert.True(t, delta > 0)
}

func TestBackpressureController_Flow(t *testing.T) {
	b := NewBackpressureController(200*time.Millisecond, 5)
	// Record many events within window
	for i := 0; i < 7; i++ {
		b.RecordEvent()
		time.Sleep(5 * time.Millisecond)
	}
	assert.True(t, b.ShouldApplyBackpressure())
	assert.True(t, b.GetDelay() >= 0)
	assert.True(t, b.GetCurrentRate() > 0)
	assert.True(t, b.IsActive())
}

func TestTimingGuard_Execute(t *testing.T) {
	tm := NewTimingManager(zap.NewNop(), TimingManagerOptions{})
	jobID := "gjob"
	// Deadline in the past to trigger error
	tm.SetDeadline(jobID, time.Now().Add(-10*time.Millisecond), "test")
	guard := tm.NewGuard(jobID, "op")
	err := guard.Execute(context.Background(), func(ctx context.Context) error { return nil })
	assert.Error(t, err)

	// Now set deadline in the future; function should run
	tm.SetDeadline(jobID, time.Now().Add(50*time.Millisecond), "test")
	err = guard.Execute(context.Background(), func(ctx context.Context) error { return nil })
	assert.NoError(t, err)
}

func TestClockSkewDetector(t *testing.T) {
	d := NewClockSkewDetector(3)
	now := time.Now()
	// Add a few samples
	_ = d.AddSample(now, now.Add(10*time.Millisecond))
	_ = d.AddSample(now, now.Add(20*time.Millisecond))
	avg := d.AddSample(now, now.Add(30*time.Millisecond))
	assert.True(t, avg > 0)
	// Average should be in the range
	assert.True(t, avg <= 30*time.Millisecond)
}

func TestDeadlineManager(t *testing.T) {
	tm := NewTimingManager(zap.NewNop(), TimingManagerOptions{})
	dm := NewDeadlineManager(tm, zap.NewNop())
	dm.SetJobDeadline(&livekit.Job{Id: "jid-1"})
	ctx := context.Background()
	ctx2, cancel := dm.CreateContextWithDeadline(ctx, "jid-1")
	cancel()
	assert.NotNil(t, ctx2)
}
