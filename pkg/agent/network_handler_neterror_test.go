package agent

import "testing"

type fakeNetErr struct{}

func (fakeNetErr) Error() string   { return "temp" }
func (fakeNetErr) Timeout() bool   { return true }
func (fakeNetErr) Temporary() bool { return true }

func TestIsNetworkError_NetErrorType(t *testing.T) {
	if !isNetworkError(fakeNetErr{}) {
		t.Fatalf("expected net.Error to be detected")
	}
}
