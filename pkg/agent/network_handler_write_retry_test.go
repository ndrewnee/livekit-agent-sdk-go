package agent

import (
	"testing"
	"time"
)

type tempNetErr struct{}

func (tempNetErr) Error() string   { return "temporary error" }
func (tempNetErr) Timeout() bool   { return false }
func (tempNetErr) Temporary() bool { return true }

type fakeWS struct{ err error }

func (f *fakeWS) WriteMessage(messageType int, data []byte) error { return f.err }
func (f *fakeWS) SetWriteDeadline(t time.Time) error              { return nil }

func TestNetworkHandler_WriteMessageWithRetry_Temporary(t *testing.T) {
	nh := NewNetworkHandler()
	ws := &fakeWS{err: tempNetErr{}}
	if err := nh.WriteMessageWithRetry(ws, 1, []byte("hello")); err == nil {
		t.Fatalf("expected error on temporary write")
	}
	if !nh.HasPartialWrite() {
		t.Fatalf("expected partial write to be saved")
	}
}

type fakeNetErr2 struct{}

func (fakeNetErr2) Error() string { return "connection reset" }

func TestNetworkHandler_WriteMessageWithRetry_NetworkError(t *testing.T) {
	nh := NewNetworkHandler()
	ws := &fakeWS{err: fakeNetErr2{}}
	if err := nh.WriteMessageWithRetry(ws, 1, []byte("hello")); err == nil {
		t.Fatalf("expected error")
	}
	if !nh.DetectNetworkPartition() {
		t.Fatalf("expected network partition detected")
	}
}
