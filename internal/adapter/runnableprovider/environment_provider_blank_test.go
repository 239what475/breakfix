package runnableprovider

import (
	"context"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/controller/runtimeenvironment"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func testBlankPlan(t *testing.T) runnable.BlankRuntimePlan {
	t.Helper()
	image := "registry.example/breakfix/terminal@sha256:" + strings.Repeat("d", 64)
	plan := runnable.BlankRuntimePlan{
		Profile: runnable.RuntimeProfile{
			Runtime: runnable.RuntimeK8s, ProfileRevision: "profile-1",
			BaseImage:        image,
			SoftwareVersions: map[string]string{"kubernetes": "v1.30.0"},
			Resources:        runnable.ResourceLimits{CPU: "1", MemoryBytes: 1 << 30, EphemeralBytes: 1 << 30, MaxProcesses: 64, MaxConcurrentTasks: 1},
			Network:          runnable.NetworkPrivate, Topology: "single-kubernetes-cluster",
			ExecutionBoundaries: []runnable.ExecutionBoundary{
				{ID: "management-write", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 900},
				{ID: "management-read", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 900},
			},
		},
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 600, MaxLifetimeSeconds: 1800},
		Image:     image,
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestEnvironmentProviderProvisionsResetsAndReleasesBlankK8s(t *testing.T) {
	plan := testBlankPlan(t)
	node := &fakeNodeEnvironmentProvider{}
	k8s := &fakeK8sEnvironmentProvider{}
	provider := testEnvironmentProvider(t, node, k8s)
	binding := runtimeenvironment.Binding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeLearning, BlankRuntime: &plan}

	observation, err := provider.Provision(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Ready || k8s.provisioned != 1 || !k8s.request.Blank || k8s.request.Revision != planDigest || k8s.request.Runtime.ImageDigest != plan.Image || k8s.request.Purpose != environment.PurposeLearning {
		t.Fatalf("blank K8s provision = %#v, calls = %d", k8s.request, k8s.provisioned)
	}

	if _, err := provider.Reset(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if k8s.deleted != 1 || k8s.provisioned != 2 {
		t.Fatalf("blank reset calls delete=%d provision=%d", k8s.deleted, k8s.provisioned)
	}

	done, err := provider.Stop(context.Background(), binding)
	if err != nil || !done {
		t.Fatalf("blank stop done=%v err=%v", done, err)
	}
	releaseDeleted := k8s.deleted
	done, err = provider.Release(context.Background(), binding)
	if err != nil || !done || k8s.deleted != releaseDeleted+1 {
		t.Fatalf("blank release done=%v deletes=%d err=%v", done, k8s.deleted, err)
	}
}

// A plan digest drift between provisioning and release (for example a
// controller upgrade while the environment stays live) must not block
// cleanup: blank release fences on the environment identity.
func TestEnvironmentProviderReleasesBlankAfterPlanDigestChange(t *testing.T) {
	plan := testBlankPlan(t)
	k8s := &fakeK8sEnvironmentProvider{}
	provider := testEnvironmentProvider(t, &fakeNodeEnvironmentProvider{}, k8s)
	changed := plan
	changed.Lifecycle.IdleTTLSeconds = 1200
	if digest, err := changed.Digest(); err != nil || digest == "" {
		t.Fatalf("changed plan digest=%q err=%v", digest, err)
	}
	binding := runtimeenvironment.Binding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeLearning, BlankRuntime: &changed}
	done, err := provider.Release(context.Background(), binding)
	if err != nil || !done || k8s.deleted != 1 {
		t.Fatalf("blank release after drift done=%v deletes=%d err=%v", done, k8s.deleted, err)
	}
	if !k8s.deleteRequest.Blank || k8s.deleteRequest.Revision != "" || k8s.deleteRequest.EnvironmentUID != binding.UID {
		t.Fatalf("blank release request = %#v", k8s.deleteRequest)
	}
}

func TestEnvironmentProviderRejectsBlankPlanMismatch(t *testing.T) {
	plan := testBlankPlan(t)
	plan.Profile.ProfileRevision = "another-profile"
	provider := testEnvironmentProvider(t, &fakeNodeEnvironmentProvider{}, &fakeK8sEnvironmentProvider{})
	binding := runtimeenvironment.Binding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeLearning, BlankRuntime: &plan}
	if _, err := provider.Provision(context.Background(), binding); err == nil || !strings.Contains(err.Error(), "K8s blank profile") {
		t.Fatalf("expected installed blank profile error, got %v", err)
	}
}
