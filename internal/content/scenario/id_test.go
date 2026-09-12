package scenario

import (
	"testing"
)

func TestNewID(t *testing.T) {
	got := NewID()
	if len(got) != len("chal-")+12 {
		t.Fatalf("unexpected id length %q", got)
	}
	if got[:5] != "chal-" {
		t.Fatalf("unexpected id %q", got)
	}
	if !ValidID(got) {
		t.Fatalf("id should be valid: %q", got)
	}
}
