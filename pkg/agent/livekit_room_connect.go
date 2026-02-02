package agent

import (
	lksdk "github.com/livekit/server-sdk-go/v2"
)

// Wrappers around LiveKit SDK connection helpers.
// These are package-level vars to allow unit tests to stub out network calls.
var (
	connectToRoom          = lksdk.ConnectToRoom
	connectToRoomWithToken = lksdk.ConnectToRoomWithToken
)
