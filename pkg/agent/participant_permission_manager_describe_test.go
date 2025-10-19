package agent

import (
	"testing"

	"github.com/livekit/protocol/livekit"
	"github.com/stretchr/testify/assert"
)

func TestDescribePermissionChanges(t *testing.T) {
	from := &livekit.ParticipantPermission{CanSubscribe: true, CanPublish: false, CanPublishData: true}
	to := &livekit.ParticipantPermission{CanSubscribe: false, CanPublish: true, CanPublishData: false, Hidden: true}
	s := describePermissionChanges(from, to)
	assert.NotEmpty(t, s)
	// Contains several fields
	assert.Contains(t, s, "subscribe:")
	assert.Contains(t, s, "publish:")
	assert.Contains(t, s, "publish_data:")
	assert.Contains(t, s, "hidden:")
}
