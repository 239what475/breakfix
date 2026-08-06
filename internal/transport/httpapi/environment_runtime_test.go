package httpapi

import (
	"context"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/challenge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewEnvironmentSpecCreatesPublishedLearningSnapshot(t *testing.T) {
	handler := &Handler{cooldownMin: 1}
	spec, err := handler.newEnvironmentSpec("u-demo", &challenge.Entry{
		ID: "chal-r7m4x2q9v6kp", RevisionID: "chrev-aaaaaaaaaaaaaaaa", Revision: "sha256:abc", Runtime: challenge.RuntimeNode,
		Checkpoints: []challenge.Checkpoint{
			{ID: "proxy-listens", Node: "proxy"},
			{ID: "client-reaches-app", Node: "client"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Purpose != breakfixv1.EnvironmentPurposeLearning || spec.Source.Kind != breakfixv1.EnvironmentSourcePublished {
		t.Fatalf("unexpected environment purpose/source: %#v", spec)
	}
	if spec.Source.Ref != "chal-r7m4x2q9v6kp" || spec.Source.Revision != "chrev-aaaaaaaaaaaaaaaa" || spec.UserRef != "u-demo" {
		t.Fatalf("unexpected published source snapshot: %#v", spec)
	}
	if len(spec.Checkpoints) != 2 || spec.Checkpoints[0].Node != "proxy" || spec.Checkpoints[1].Node != "client" {
		t.Fatalf("unexpected checkpoint snapshot: %#v", spec.Checkpoints)
	}
	if spec.Lifecycle.ActivityAt == nil || spec.Lifecycle.IdleTTLSeconds == nil || *spec.Lifecycle.IdleTTLSeconds != 60 {
		t.Fatalf("expected initial activity and 60-second TTL, got %#v", spec.Lifecycle)
	}
}

func TestNewEnvironmentSpecRejectsIncompleteSnapshot(t *testing.T) {
	handler := &Handler{cooldownMin: 1}
	_, err := handler.newEnvironmentSpec("u-demo", &challenge.Entry{
		ID: "chal-r7m4x2q9v6kp", Runtime: challenge.RuntimeNode,
	})
	if err == nil {
		t.Fatal("expected incomplete challenge snapshot to fail")
	}
}

func TestRenewActivityOnlyUpdatesEnvironmentSpec(t *testing.T) {
	before := metav1.NewTime(time.Now().Add(-time.Minute))
	spec := breakfixv1.EnvironmentSpec{Lifecycle: breakfixv1.EnvironmentLifecycleSpec{ActivityAt: &before}}
	adapter := &environmentRuntimeAdapter{
		runtime: challenge.RuntimeNode,
		updateSpec: func(_ context.Context, _ string, mutate func(*breakfixv1.EnvironmentSpec)) error {
			mutate(&spec)
			return nil
		},
	}
	next := metav1.NewTime(time.Now().UTC().Truncate(time.Second).Add(750 * time.Millisecond))
	if err := adapter.renewActivity(context.Background(), "demo", next); err != nil {
		t.Fatal(err)
	}
	want := next.UTC().Truncate(time.Second)
	if spec.Lifecycle.ActivityAt == nil || !spec.Lifecycle.ActivityAt.Time.Equal(want) {
		t.Fatalf("expected normalized activity %s, got %#v", want, spec.Lifecycle.ActivityAt)
	}
}

func TestMutateNodeEnvironmentSpecDoesNotWriteStatus(t *testing.T) {
	environment := &breakfixv1.NodeEnvironment{}
	environment.Generation = 11
	updated := false
	if err := mutateEnvironmentSpec(context.Background(), "demo",
		func(context.Context, string) (*breakfixv1.NodeEnvironment, error) { return environment, nil },
		func(_ context.Context, next *breakfixv1.NodeEnvironment) error {
			updated = true
			environment = next
			return nil
		},
		func(spec *breakfixv1.EnvironmentSpec) {
			activity := metav1.Now()
			spec.Lifecycle.ActivityAt = &activity
		},
	); err != nil {
		t.Fatal(err)
	}
	if !updated || environment.Spec.Environment.Lifecycle.ActivityAt == nil {
		t.Fatalf("expected spec-only update, got %#v", environment)
	}
	if environment.Status.Environment.ObservedGeneration != 0 {
		t.Fatalf("Server must not write status, got %#v", environment.Status)
	}
}

func TestLearningNodeRuntimeSnapshotUsesPlatformConfiguration(t *testing.T) {
	handler := newHandlerForTest(t, nil, nil, config.Config{
		Runtime: config.RuntimeConfig{Node: config.NodeRuntimeConfig{
			ProfileRevision: "node-profile-v1", NetworkPolicyRevision: "node-network-v1",
		}},
		Incus: incus.Config{NodeCPU: "1", NodeMemory: "512MiB", NodeProcesses: 512, NodeRootDisk: "5GiB"},
	})
	if handler.runtimeConfig.Node.ProfileRevision != "node-profile-v1" || handler.incusConfig.NodeProcesses != 512 {
		t.Fatalf("handler lost fixed runtime configuration: %#v %#v", handler.runtimeConfig, handler.incusConfig)
	}
}
