package workspace

import "testing"

func TestNewPVCNameIsStableAndOpaque(t *testing.T) {
	first := NewPVCName("generator-session-a")
	if first != NewPVCName("generator-session-a") {
		t.Fatal("pvc name must be stable")
	}
	if first == NewPVCName("generator-session-b") {
		t.Fatal("different sessions must not share a pvc name")
	}
	if len(first) > 63 {
		t.Fatalf("pvc name length = %d, want Kubernetes-safe name", len(first))
	}
}
