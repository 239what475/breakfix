package kubernetes

import (
	"context"
	"testing"

	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func testWorkspaceOwner() appgeneration.WorkspaceOwnerReference {
	return appgeneration.WorkspaceOwnerReference{
		APIVersion: "breakfix.dev/v2", Kind: "GeneratorWorkspace",
		Name: "generator-workspace-one", UID: "uid-generator-workspace-one",
	}
}

func TestEnsureWorkspacePVCCreatesAndChecksOwnership(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset()}
	ctx := context.Background()
	owner := testWorkspaceOwner()
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "workflow-one", "1Gi", owner); err != nil {
		t.Fatalf("ensure workspace pvc: %v", err)
	}
	claim, err := client.clientset.CoreV1().PersistentVolumeClaims("opensandbox").Get(ctx, "workspace-one", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created pvc: %v", err)
	}
	if claim.Labels["breakfix.dev/workflow"] != "workflow-one" {
		t.Fatalf("workflow label = %q", claim.Labels["breakfix.dev/workflow"])
	}
	if len(claim.OwnerReferences) != 1 {
		t.Fatalf("owner references = %#v", claim.OwnerReferences)
	}
	reference := claim.OwnerReferences[0]
	if reference.APIVersion != owner.APIVersion || reference.Kind != owner.Kind || reference.Name != owner.Name || string(reference.UID) != owner.UID {
		t.Fatalf("owner reference = %#v", reference)
	}
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "workflow-one", "1Gi", owner); err != nil {
		t.Fatalf("ensure same workspace pvc: %v", err)
	}
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "workflow-two", "1Gi", owner); err == nil {
		t.Fatal("different workflow unexpectedly adopted pvc")
	}
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "workflow-one", "1Gi", appgeneration.WorkspaceOwnerReference{}); err == nil {
		t.Fatal("missing owner reference unexpectedly accepted")
	}
	if err := client.DeleteWorkspacePVC(ctx, "opensandbox", "workspace-one"); err != nil {
		t.Fatalf("delete workspace pvc: %v", err)
	}
	if _, err := client.clientset.CoreV1().PersistentVolumeClaims("opensandbox").Get(ctx, "workspace-one", metav1.GetOptions{}); !k8sErrors.IsNotFound(err) {
		t.Fatalf("pvc after delete error = %v, want not found", err)
	}
}

// Claims provisioned before the ownership transfer gain the owner reference on
// the next ensure, so a database reset can still reclaim them through the CR.
func TestEnsureWorkspacePVCPatchesOwnerOntoExistingClaim(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workspace-one",
			Namespace: "opensandbox",
			Labels:    map[string]string{"breakfix.dev/workflow": "workflow-one"},
		},
	})}
	ctx := context.Background()
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "workflow-one", "1Gi", testWorkspaceOwner()); err != nil {
		t.Fatalf("ensure existing workspace pvc: %v", err)
	}
	claim, err := client.clientset.CoreV1().PersistentVolumeClaims("opensandbox").Get(ctx, "workspace-one", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched pvc: %v", err)
	}
	if len(claim.OwnerReferences) != 1 || string(claim.OwnerReferences[0].UID) != "uid-generator-workspace-one" {
		t.Fatalf("patched owner references = %#v", claim.OwnerReferences)
	}
}
