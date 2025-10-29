package agent

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestJobUtils_PublishData_ErrorWhenNoRoom(t *testing.T) {
	jc := NewJobUtils(nil, nil, nil)
	err := jc.PublishData([]byte("x"), true, nil)
	assert.Error(t, err)
}

func TestJobUtils_PublishData_WithDestinations_Error(t *testing.T) {
	jc := NewJobUtils(nil, nil, nil)
	err := jc.PublishData([]byte("y"), false, []string{"p1"})
	assert.Error(t, err)
}
