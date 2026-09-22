package runtimeenvironment

import (
	"strings"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDecideUsesLeaseAndLifecycleWithoutContentFields(t *testing.T) {
	revision := validRevision(t)
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	environment := runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(createdAt)},
		Spec: runtimev2.RuntimeEnvironmentSpec{
			RunnableRevisionRef: &runtimev2.RunnableRevisionReference{ID: "revision-01", Digest: digest}, Purpose: runtimev2.PurposeLearning,
			Lease: runtimev2.LeaseSpec{RenewedAt: metav1.NewTime(createdAt.Add(5 * time.Minute))},
		},
		Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady},
	}
	plan := Plan{Revision: revision}
	decision, err := Decide(environment, plan, createdAt.Add(14*time.Minute), 0)
	if err != nil || decision != DecisionNone {
		t.Fatalf("decision before idle expiry = %q, %v", decision, err)
	}
	decision, err = Decide(environment, plan, createdAt.Add(16*time.Minute), 0)
	if err != nil || decision != DecisionDrain {
		t.Fatalf("decision after idle expiry = %q, %v", decision, err)
	}
}

func TestDecideFencesResetAndReap(t *testing.T) {
	revision := validRevision(t)
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	environment := runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now)},
		Spec:       runtimev2.RuntimeEnvironmentSpec{RunnableRevisionRef: &runtimev2.RunnableRevisionReference{ID: "revision-01", Digest: digest}, Purpose: runtimev2.PurposeLearning, Lease: runtimev2.LeaseSpec{RenewedAt: metav1.NewTime(now)}, ResetNonce: 2},
		Status:     runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady},
	}
	plan := Plan{Revision: revision}
	decision, err := Decide(environment, plan, now.Add(time.Minute), 1)
	if err != nil || decision != DecisionReset {
		t.Fatalf("reset decision = %q, %v", decision, err)
	}
	environment.Status.Phase = runtimev2.PhaseDraining
	decision, err = Decide(environment, plan, now.Add(time.Minute), 1)
	if err != nil || decision != DecisionReap {
		t.Fatalf("reap decision = %q, %v", decision, err)
	}
}

// A reset in flight keeps deciding Reset even after the nonce is observed:
// only the rebuilt terminal's Ready observation ends the operation, so the
// adoptive Provision path can never clear it on stale resources.
func TestDecideSustainsResetUntilRebuilt(t *testing.T) {
	revision := validRevision(t)
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	plan := Plan{Revision: revision}
	environment := runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now)},
		Spec:       runtimev2.RuntimeEnvironmentSpec{RunnableRevisionRef: &runtimev2.RunnableRevisionReference{ID: "revision-01", Digest: digest}, Purpose: runtimev2.PurposeLearning, Lease: runtimev2.LeaseSpec{RenewedAt: metav1.NewTime(now)}, ResetNonce: 1},
		Status:     runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Operation: runtimev2.OperationResetting},
	}
	decision, err := Decide(environment, plan, now.Add(time.Minute), 1)
	if err != nil || decision != DecisionReset {
		t.Fatalf("in-flight reset decision = %q, %v", decision, err)
	}
	// The phase may legitimately still be Ready (the pre-wipe terminal) or
	// Provisioning (a reset adopted mid-provision); both keep resetting.
	environment.Status.Phase = runtimev2.PhaseProvisioning
	decision, err = Decide(environment, plan, now.Add(time.Minute), 1)
	if err != nil || decision != DecisionReset {
		t.Fatalf("provisioning reset decision = %q, %v", decision, err)
	}
	// Lifecycle reclamation still wins over an in-flight reset: an expired
	// lease drains instead of wiping.
	environment.Status.Phase = runtimev2.PhaseReady
	environment.Spec.Lease.RenewedAt = metav1.NewTime(now.Add(-2 * time.Hour))
	decision, err = Decide(environment, plan, now.Add(time.Minute), 1)
	if err != nil || decision != DecisionDrain {
		t.Fatalf("expired reset decision = %q, %v", decision, err)
	}
	// Any other phase during a reset is a lifecycle inconsistency.
	environment.Spec.Lease.RenewedAt = metav1.NewTime(now)
	environment.Status.Phase = runtimev2.PhaseFailed
	decision, err = Decide(environment, plan, now.Add(time.Minute), 1)
	if err == nil || decision != DecisionNone || !strings.Contains(err.Error(), "invalid in phase") {
		t.Fatalf("invalid reset phase decision = %q, %v", decision, err)
	}
}

func TestValidateSpecRejectsRevisionOrProfileMismatch(t *testing.T) {
	revision := validRevision(t)
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	environment := runtimev2.RuntimeEnvironment{Spec: runtimev2.RuntimeEnvironmentSpec{RunnableRevisionRef: &runtimev2.RunnableRevisionReference{ID: "revision-01", Digest: digest}, Purpose: runtimev2.PurposeVerification, Lease: runtimev2.LeaseSpec{RenewedAt: metav1.NewTime(time.Now())}}}
	environment.Spec.RunnableRevisionRef.Digest = "sha256:" + strings.Repeat("f", 64)
	if err := ValidateSpec(environment, Plan{Revision: revision}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected revision mismatch, got %v", err)
	}
}

func TestCanTransitionIsLifecycleMonotonic(t *testing.T) {
	if !CanTransition(runtimev2.PhaseProvisioning, runtimev2.PhaseReady) || CanTransition(runtimev2.PhaseReleased, runtimev2.PhaseReady) || CanTransition(runtimev2.PhaseDraining, runtimev2.PhaseReady) {
		t.Fatal("unexpected lifecycle transition rules")
	}
}

func validRevision(t *testing.T) runnable.RunnableRevision {
	t.Helper()
	spec := runnable.RunnableSpec{
		FormatVersion: runnable.FormatVersion, Identity: runnable.ContentIdentity{Kind: "operations", ID: "service-startup", Revision: "rev-01"},
		RuntimeProfile:  runnable.RuntimeProfile{Runtime: runnable.RuntimeNode, ProfileRevision: "profile-01", BaseImage: "registry.example/base@sha256:" + strings.Repeat("a", 64), SoftwareVersions: map[string]string{"runtime": "v1"}, Resources: runnable.ResourceLimits{CPU: "2", MemoryBytes: 1 << 30, EphemeralBytes: 1 << 30, MaxProcesses: 64, MaxConcurrentTasks: 1}, Network: runnable.NetworkPrivate, Topology: "single-host", ExecutionBoundaries: []runnable.ExecutionBoundary{{ID: "host-write", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 60}, {ID: "host-read", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 60}}},
		Source:          runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/source.tar.gz", Digest: "sha256:" + strings.Repeat("b", 64)},
		Initialization:  []runnable.ActionSpec{{ID: "initialize", Entrypoint: "scripts/init.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-write", TimeoutSeconds: 60, ExpectedExitCodes: []int{0}}},
		ValidationPlan:  runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{{ID: "observe", TimeoutSeconds: 60, Execution: runnable.PhaseSequential, Assertions: []runnable.AssertionSpec{{ID: "ready", Entrypoint: "scripts/assert.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-read", TimeoutSeconds: 60}}}}},
		LifecyclePolicy: runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 600, MaxLifetimeSeconds: 1800},
	}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest := "sha256:" + strings.Repeat("c", 64)
	return runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: runnable.RuntimeNode, ProviderReference: "incus://breakfix/image@" + artifactDigest, ArtifactDigest: artifactDigest, BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01"}}
}
