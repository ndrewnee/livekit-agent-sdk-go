package agent

import "testing"

func TestWorkerStatus_String(t *testing.T) {
	if WorkerStatusAvailable.String() != "available" {
		t.Fatalf("expected available, got %s", WorkerStatusAvailable.String())
	}
	if WorkerStatusFull.String() != "full" {
		t.Fatalf("expected full, got %s", WorkerStatusFull.String())
	}
	if WorkerStatus(99).String() != "unknown" {
		t.Fatalf("expected unknown for invalid status")
	}
}
