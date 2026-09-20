package httpapi

import (
	"context"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/content/scenario"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEnvironmentObjectMetaCarriesGenericContentIdentity(t *testing.T) {
	operations := environmentObjectMeta("learning-demo", "breakfix-system", "u-demo", environmentContentOperations, "demo", "chrev-aaaaaaaaaaaaaaaa", runtimev2.PurposeLearning)
	if operations.Labels["breakfix.dev/content-kind"] != "operations" || operations.Labels["breakfix.dev/content-id"] != "demo" || operations.Labels["breakfix.dev/content-revision"] != "chrev-aaaaaaaaaaaaaaaa" {
		t.Fatalf("environment labels = %#v", operations.Labels)
	}
	practice := environmentObjectMeta("learning-practice", "breakfix-system", "u-demo", environmentContentDocumentationPractice, "practice-01", "runnable-revision-01", runtimev2.PurposeLearning)
	if practice.Labels["breakfix.dev/content-kind"] != "documentation-practice" || practice.Labels["breakfix.dev/content-id"] != "practice-01" {
		t.Fatalf("practice environment labels = %#v", practice.Labels)
	}
	if _, found := operations.Labels["breakfix.dev/scenario"]; found {
		t.Fatalf("environment metadata retained a scenario-specific label: %#v", operations.Labels)
	}
}

func TestRenewActivityUpdatesLease(t *testing.T) {
	before := metav1.NewTime(time.Now().UTC().Add(-time.Minute).Truncate(time.Second))
	updated := metav1.Time{}
	calls := 0
	adapter := &environmentRuntimeAdapter{
		runtime: scenario.RuntimeNode,
		updateLease: func(_ context.Context, _ string, value metav1.Time) error {
			calls++
			updated = value
			return nil
		},
	}
	next := metav1.NewTime(before.Add(750 * time.Millisecond))
	if err := adapter.renewActivity(context.Background(), "demo", next); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !updated.Time.Equal(next.Time) {
		t.Fatalf("lease update = calls:%d value:%s, want %s", calls, updated.Time, next.Time)
	}
}

func TestEnvironmentFromRuntimeMapsGenericProviderState(t *testing.T) {
	environment := &runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "learning-demo", Labels: map[string]string{
				"breakfix.dev/user": "u-demo", "breakfix.dev/content-kind": "operations",
				"breakfix.dev/content-id": "demo", "breakfix.dev/content-revision": "chrev-aaaaaaaaaaaaaaaa",
			},
		},
		Spec: runtimev2.RuntimeEnvironmentSpec{RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: "rrev-demo", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Purpose: runtimev2.PurposeLearning},
		Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{
			Provider: "k8s", ResourceRefs: []runtimev2.ResourceReference{{Provider: "k8s", Kind: "namespace", ID: "runtime-demo"}, {Provider: "k8s", Kind: "pod", ID: "runtime-demo/terminal"}},
			EndpointRefs: []runtimev2.EndpointReference{{Name: "terminal", Ref: "runtime-demo/terminal"}},
		}},
	}
	active := environmentFromRuntime(environment)
	if active.Runtime != scenario.RuntimeK8s || active.Namespace != "runtime-demo" || active.WorkspacePod != "terminal" {
		t.Fatalf("runtime projection = %#v", active)
	}
	if active.ScenarioRef != "demo" || active.SourceRevision != "chrev-aaaaaaaaaaaaaaaa" || active.RunnableRevisionDigest != environment.Spec.RunnableRevisionRef.Digest {
		t.Fatalf("identity projection = %#v", active)
	}
}
