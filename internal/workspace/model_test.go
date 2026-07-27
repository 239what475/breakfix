package workspace

import "testing"

func TestNewPVCNameIsStableAndOpaque(t *testing.T) {
	first := NewPVCName("generator-run-a")
	if first != NewPVCName("generator-run-a") {
		t.Fatal("pvc name must be stable")
	}
	if first == NewPVCName("generator-run-b") {
		t.Fatal("different runs must not share a pvc name")
	}
	if len(first) > 63 {
		t.Fatalf("pvc name length = %d, want Kubernetes-safe name", len(first))
	}
}
