package generation

import "testing"

func TestNewWorkspacePVCNameIsStableAndOpaque(t *testing.T) {
	first := NewWorkspacePVCName("generator-run-a")
	if first != NewWorkspacePVCName("generator-run-a") {
		t.Fatal("pvc name must be stable")
	}
	if first == NewWorkspacePVCName("generator-run-b") {
		t.Fatal("different runs must not share a pvc name")
	}
	if len(first) > 63 {
		t.Fatalf("pvc name length = %d, want Kubernetes-safe name", len(first))
	}
}
