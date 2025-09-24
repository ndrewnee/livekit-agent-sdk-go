package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestDefaultAndMetadataPriorityCalculators(t *testing.T) {
	d := &DefaultPriorityCalculator{}

	// Metadata hints
	assert.Equal(t, JobPriorityUrgent, d.CalculatePriority(&livekit.Job{Metadata: "urgent"}))
	assert.Equal(t, JobPriorityHigh, d.CalculatePriority(&livekit.Job{Metadata: "high"}))
	assert.Equal(t, JobPriorityLow, d.CalculatePriority(&livekit.Job{Metadata: "low"}))

	// Type-based defaults
	assert.Equal(t, JobPriorityHigh, d.CalculatePriority(&livekit.Job{Type: livekit.JobType_JT_ROOM}))
	assert.Equal(t, JobPriorityNormal, d.CalculatePriority(&livekit.Job{Type: livekit.JobType_JT_PARTICIPANT}))
	assert.Equal(t, JobPriorityNormal, d.CalculatePriority(&livekit.Job{Type: livekit.JobType_JT_PUBLISHER}))

	// MetadataPriorityCalculator with structured field
	m := &MetadataPriorityCalculator{PriorityField: "priority"}
	assert.Equal(t, JobPriorityUrgent, m.CalculatePriority(&livekit.Job{Metadata: "priority:urgent"}))
	assert.Equal(t, JobPriorityHigh, m.CalculatePriority(&livekit.Job{Metadata: "priority:high"}))
	assert.Equal(t, JobPriorityLow, m.CalculatePriority(&livekit.Job{Metadata: "priority:low"}))
	assert.Equal(t, JobPriorityNormal, m.CalculatePriority(&livekit.Job{Metadata: "priority:other"}))
	assert.Equal(t, JobPriorityNormal, m.CalculatePriority(&livekit.Job{}))
}
