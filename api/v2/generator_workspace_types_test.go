package v2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGeneratorWorkspaceDeepCopyKeepsStatusIndependent(t *testing.T) {
	value := &GeneratorWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "generator-workspace-one", Namespace: "opensandbox"},
		Status: GeneratorWorkspaceStatus{
			WorkflowID: "workflow-one", Namespace: "opensandbox", PVCName: "breakfix-workspace-01",
			SandboxID: "sandbox-one", Phase: WorkspacePhaseActive,
		},
	}
	copy := value.DeepCopy()
	copy.Status.SandboxID = "sandbox-two"
	if value.Status.SandboxID != "sandbox-one" {
		t.Fatalf("deep copy shared the status: original=%#v copy=%#v", value.Status, copy.Status)
	}
	list := &GeneratorWorkspaceList{Items: []GeneratorWorkspace{*value}}
	listCopy := list.DeepCopy()
	listCopy.Items[0].Status.Phase = WorkspacePhaseDeleting
	if list.Items[0].Status.Phase != WorkspacePhaseActive {
		t.Fatalf("deep copy shared the list items: %#v", list.Items[0].Status)
	}
}

// The generated CRD is the deployed contract: controller-gen derives the
// required list from the missing omitempty tags and the phase enum from the
// kubebuilder marker, and make verify-generated keeps the file in sync.
func TestGeneratorWorkspaceCRDRequiresOwnershipFactsAndEnumeratesPhase(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	crd, err := os.ReadFile(filepath.Join(root, "deploy", "crds", "breakfix.dev_generatorworkspaces.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(crd)
	if !strings.Contains(value, "name: generatorworkspaces.breakfix.dev") {
		t.Fatal("CRD does not define the generatorworkspaces resource")
	}
	if !strings.Contains(value, "scope: Namespaced") {
		t.Fatal("CRD must be namespaced so a PVC can reference it as an owner")
	}
	if !strings.Contains(value, "subresources:\n      status: {}") {
		t.Fatal("CRD does not enable the status subresource")
	}
	const requiredStatusFields = "required:\n            - namespace\n            - phase\n            - pvcName\n            - workflowID"
	if !strings.Contains(value, requiredStatusFields) {
		t.Fatal("CRD status schema does not require the ownership facts namespace, phase, pvcName, and workflowID")
	}
	const phaseEnum = "phase:\n                enum:\n                - Pending\n                - Active\n                - Deleting"
	if !strings.Contains(value, phaseEnum) {
		t.Fatal("CRD phase field does not enumerate Pending, Active, and Deleting")
	}
}
