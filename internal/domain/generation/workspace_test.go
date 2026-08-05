package generation

import "testing"

func TestNewWorkspacePVCNameIsStableAndOpaque(t *testing.T) {
	first := NewWorkspacePVCName("workspace-a")
	if first != NewWorkspacePVCName("workspace-a") {
		t.Fatal("pvc name must be stable")
	}
	if first == NewWorkspacePVCName("workspace-b") {
		t.Fatal("different workspaces must not share a pvc name")
	}
	if len(first) > 63 {
		t.Fatalf("pvc name length = %d, want Kubernetes-safe name", len(first))
	}
}
