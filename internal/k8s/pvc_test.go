package k8s

import (
	"context"
	"testing"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureWorkspacePVCCreatesAndChecksOwnership(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset()}
	ctx := context.Background()
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "generator-one", "1Gi"); err != nil {
		t.Fatalf("ensure workspace pvc: %v", err)
	}
	claim, err := client.clientset.CoreV1().PersistentVolumeClaims("opensandbox").Get(ctx, "workspace-one", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created pvc: %v", err)
	}
	if claim.Labels["breakfix.dev/workspace"] != "generator-one" {
		t.Fatalf("workspace label = %q", claim.Labels["breakfix.dev/workspace"])
	}
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "generator-one", "1Gi"); err != nil {
		t.Fatalf("ensure same workspace pvc: %v", err)
	}
	if err := client.EnsureWorkspacePVC(ctx, "opensandbox", "workspace-one", "generator-two", "1Gi"); err == nil {
		t.Fatal("different generator run unexpectedly adopted pvc")
	}
	if err := client.DeleteWorkspacePVC(ctx, "opensandbox", "workspace-one"); err != nil {
		t.Fatalf("delete workspace pvc: %v", err)
	}
	if _, err := client.clientset.CoreV1().PersistentVolumeClaims("opensandbox").Get(ctx, "workspace-one", metav1.GetOptions{}); !k8sErrors.IsNotFound(err) {
		t.Fatalf("pvc after delete error = %v, want not found", err)
	}
}
