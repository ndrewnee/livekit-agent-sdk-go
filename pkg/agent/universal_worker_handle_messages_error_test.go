package agent

import (
	"context"
	"testing"
)

func TestUniversalWorker_HandleMessages_ReadErrorPath(t *testing.T) {
	w := &UniversalWorker{logger: NewDefaultLogger(), reconnectChan: make(chan struct{}, 1)}
	// No connection set; handleMessages should call readMessage -> error -> handleConnectionError -> return
	w.handleMessages(context.Background())
}
