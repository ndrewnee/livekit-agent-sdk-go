module github.com/am-sokolov/livekit-agent-sdk-go/examples/save-to-hls

go 1.24.6

replace github.com/am-sokolov/livekit-agent-sdk-go => ../..

replace github.com/bluenviron/gohlslib/v2 => ../../gohlslib

replace github.com/bluenviron/mediacommon/v2 => ../../mediacommon

require (
	github.com/bluenviron/gohlslib/v2 v2.2.3
	github.com/bluenviron/mediacommon/v2 v2.4.3
	github.com/pion/rtcp v1.2.15
	github.com/pion/rtp v1.8.21
	github.com/pion/webrtc/v4 v4.1.5-0.20250828044558-c376d0edf977
)

require (
	github.com/abema/go-mp4 v1.4.1 // indirect
	github.com/asticode/go-astikit v0.30.0 // indirect
	github.com/asticode/go-astits v1.13.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pion/datachannel v1.5.10 // indirect
	github.com/pion/dtls/v3 v3.0.7 // indirect
	github.com/pion/ice/v4 v4.0.10 // indirect
	github.com/pion/interceptor v0.1.40 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.0.7 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.8.39 // indirect
	github.com/pion/sdp/v3 v3.0.15 // indirect
	github.com/pion/srtp/v3 v3.0.7 // indirect
	github.com/pion/stun/v3 v3.0.0 // indirect
	github.com/pion/transport/v3 v3.0.7 // indirect
	github.com/pion/turn/v4 v4.1.1 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/crypto v0.41.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.35.0 // indirect
)
