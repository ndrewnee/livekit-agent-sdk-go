package agent

import "testing"

func TestResourceHealthLevel_String(t *testing.T) {
	if ResourceHealthGood.String() != "good" {
		t.Fatalf("bad")
	}
	if ResourceHealthWarning.String() != "warning" {
		t.Fatalf("bad")
	}
	if ResourceHealthCritical.String() != "critical" {
		t.Fatalf("bad")
	}
	if ResourceHealthLevel(99).String() != "unknown" {
		t.Fatalf("bad")
	}
}
