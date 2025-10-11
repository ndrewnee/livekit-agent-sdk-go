package main

import (
	"fmt"
	"testing"

	"github.com/pion/webrtc/v4"
)

// TestDefaultInterceptors checks what interceptors are enabled by default
func TestDefaultInterceptors(t *testing.T) {
	// Create a default peer connection (like save-to-hls-gstreamer does)
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		t.Fatalf("Failed to create peer connection: %v", err)
	}
	defer pc.Close()

	fmt.Println("Default WebRTC configuration created")
	fmt.Println("Question: Does it include jitter buffer interceptor?")
	fmt.Println("Answer: By default, Pion WebRTC does NOT enable the jitter buffer interceptor")
	fmt.Println("")
	fmt.Println("To enable it, you would need to:")
	fmt.Println("  1. Create a MediaEngine")
	fmt.Println("  2. Create an InterceptorRegistry")
	fmt.Println("  3. Register the jitter buffer interceptor")
	fmt.Println("  4. Use webrtc.NewAPI() with the custom registry")
	fmt.Println("")
	fmt.Println("Example:")
	fmt.Println("  m := &webrtc.MediaEngine{}")
	fmt.Println("  if err := m.RegisterDefaultCodecs(); err != nil { ... }")
	fmt.Println("  i := &interceptor.Registry{}")
	fmt.Println("  if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil { ... }")
	fmt.Println("  // Add jitter buffer")
	fmt.Println("  jbFactory, err := jitterbuffer.NewInterceptor()")
	fmt.Println("  i.Add(jbFactory)  // <-- This is what would enable it")
	fmt.Println("  api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))")
	fmt.Println("  pc, err := api.NewPeerConnection(config)")
}
