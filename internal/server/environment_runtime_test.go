package server

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewCommonEnvironmentSpecCreatesImmutableExecutionSnapshot(t *testing.T) {
	handler := &Handler{cooldownMin: 1}
	spec, err := handler.newCommonEnvironmentSpec("u-demo", &challenge.Entry{
		ID:       "cleanup-logs",
		Revision: "sha256:abc",
		Runtime:  challenge.RuntimeContainer,
		Image:    "breakfix-cleanup-logs:latest",
		Checkpoints: []challenge.Checkpoint{
			{ID: "archive-old-logs"},
			{ID: "keep-protected-logs"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.ChallengeRef != "cleanup-logs" || spec.ChallengeRevision != "sha256:abc" || spec.UserRef != "u-demo" {
		t.Fatalf("unexpected challenge snapshot: %#v", spec)
	}
	if spec.Runtime != challenge.RuntimeContainer || spec.Image != "breakfix-cleanup-logs:latest" {
		t.Fatalf("unexpected runtime snapshot: %#v", spec)
	}
	if len(spec.CheckpointIDs) != 2 || spec.CheckpointIDs[0] != "archive-old-logs" || spec.CheckpointIDs[1] != "keep-protected-logs" {
		t.Fatalf("unexpected checkpoint snapshot: %#v", spec.CheckpointIDs)
	}
	if spec.ActivityAt == nil || spec.Timeouts.IdleTTLSeconds == nil || *spec.Timeouts.IdleTTLSeconds != 60 {
		t.Fatalf("expected initial activity and 60-second TTL, got %#v", spec)
	}
}

func TestNewCommonEnvironmentSpecRejectsIncompleteSnapshot(t *testing.T) {
	handler := &Handler{cooldownMin: 1}
	_, err := handler.newCommonEnvironmentSpec("u-demo", &challenge.Entry{
		ID:      "cleanup-logs",
		Runtime: challenge.RuntimeContainer,
		Image:   "breakfix-cleanup-logs:latest",
	})
	if err == nil {
		t.Fatal("expected incomplete challenge snapshot to fail")
	}
}

func TestRenewActivityOnlyUpdatesEnvironmentSpec(t *testing.T) {
	before := metav1.NewTime(time.Now().Add(-time.Minute))
	spec := breakfixv1.CommonEnvironmentSpec{ActivityAt: &before}
	adapter := &environmentRuntimeAdapter{
		runtime: "container",
		updateSpec: func(_ context.Context, _ string, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
			mutate(&spec)
			return nil
		},
	}
	next := metav1.NewTime(time.Now().UTC())
	if err := adapter.renewActivity(context.Background(), "demo", next); err != nil {
		t.Fatal(err)
	}
	if spec.ActivityAt == nil || !spec.ActivityAt.Equal(&next) {
		t.Fatalf("expected spec activity update, got %#v", spec.ActivityAt)
	}
}

func TestMutateEnvironmentSpecDoesNotRequireStatusMutation(t *testing.T) {
	env := &breakfixv1.ContainerEnvironment{}
	env.Generation = 11
	updated := false
	if err := mutateEnvironmentSpec(context.Background(), "demo",
		func(_ context.Context, _ string) (*breakfixv1.ContainerEnvironment, error) {
			return env, nil
		},
		func(_ context.Context, next *breakfixv1.ContainerEnvironment) error {
			updated = true
			env = next
			return nil
		},
		func(spec *breakfixv1.CommonEnvironmentSpec) {
			activity := metav1.Now()
			spec.ActivityAt = &activity
		},
	); err != nil {
		t.Fatal(err)
	}
	if !updated || env.Spec.ActivityAt == nil {
		t.Fatalf("expected spec-only update, got %#v", env)
	}
	if env.Status.ObservedGeneration != 0 {
		t.Fatalf("Server must not write status, got %#v", env.Status)
	}
}
