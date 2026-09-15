package v2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRuntimeEnvironmentDeepCopyKeepsMutableFieldsIndependent(t *testing.T) {
	releaseAt := metav1.NewTime(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	value := &RuntimeEnvironment{
		Spec: RuntimeEnvironmentSpec{
			RunnableRevisionRef: RunnableRevisionReference{ID: "revision-01", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Purpose:             PurposeLearning, Lease: LeaseSpec{RenewedAt: releaseAt, ReleaseAt: &releaseAt},
		},
		Status: RuntimeEnvironmentStatus{Runtime: RuntimeStatus{Provider: "node", ProfileDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ResourceRefs: []ResourceReference{{Provider: "incus", Kind: "instance", ID: "node-01"}}}},
	}
	copy := value.DeepCopy()
	copy.Spec.Lease.ReleaseAt.Time = copy.Spec.Lease.ReleaseAt.Add(time.Hour)
	copy.Status.Runtime.ResourceRefs[0].ID = "node-02"
	if value.Spec.Lease.ReleaseAt.Equal(copy.Spec.Lease.ReleaseAt) || value.Status.Runtime.ResourceRefs[0].ID != "node-01" {
		t.Fatalf("deep copy shared mutable fields: original=%#v copy=%#v", value, copy)
	}
}

func TestRuntimeEnvironmentCRDAllowsStatusUpdatesWithoutResetNonce(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	crd, err := os.ReadFile(filepath.Join(root, "deploy", "crds", "breakfix.dev_runtimeenvironments.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	value := string(crd)
	if !strings.Contains(value, "!has(oldSelf.resetNonce)") || !strings.Contains(value, "self.resetNonce >= oldSelf.resetNonce") {
		t.Fatalf("RuntimeEnvironment reset nonce CEL rule does not safely handle omitted values")
	}
}
