package v2

import (
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
