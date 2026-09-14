package runnableworker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestMaterializeReturnsOnlyArtifactBoundToClaimedSpec(t *testing.T) {
	spec := validSpec()
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := New(materializer{artifact: artifactFor(t, spec)}, verifier{})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := worker.Materialize(context.Background(), runnable.MaterializeRequest{
		Credential: runnable.LeaseCredential{Identity: runnable.ActionIdentity{Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionMaterializeArtifact, StateVersion: 1}, LeaseOwner: "worker-01"},
		Spec:       spec,
	})
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if revision.Artifact.BuiltFromSpecDigest != specDigest {
		t.Fatalf("artifact = %#v", revision.Artifact)
	}
}

func TestMaterializeRejectsUnboundProviderArtifact(t *testing.T) {
	spec := validSpec()
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	artifact := artifactFor(t, spec)
	artifact.BuiltFromSpecDigest = testDigest("e")
	worker, err := New(materializer{artifact: artifact}, verifier{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.Materialize(context.Background(), runnable.MaterializeRequest{
		Credential: runnable.LeaseCredential{Identity: runnable.ActionIdentity{Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionMaterializeArtifact, StateVersion: 1}, LeaseOwner: "worker-01"},
		Spec:       spec,
	})
	if err == nil || !strings.Contains(err.Error(), "unbound artifact") {
		t.Fatalf("expected artifact binding rejection, got %v", err)
	}
}

func TestVerifyRejectsReportWithCallerControlledPassedFlag(t *testing.T) {
	spec := validSpec()
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: artifactFor(t, spec)}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	report := reportFor(t, revision)
	report.Phases[0].Assertions[0].Satisfied = false
	report.Passed = true
	worker, err := New(materializer{}, verifier{report: report})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.Verify(context.Background(), runnable.VerifyRequest{
		Credential:       runnable.LeaseCredential{Identity: runnable.ActionIdentity{Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: 2}, LeaseOwner: "worker-01"},
		RunnableRevision: revision, RunnableRevisionDigest: revisionDigest,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid report") {
		t.Fatalf("expected machine result validation, got %v", err)
	}
}

type materializer struct {
	artifact runnable.ArtifactReference
}

func (m materializer) Materialize(context.Context, runnable.RunnableSpec) (runnable.ArtifactReference, error) {
	return m.artifact, nil
}

type verifier struct {
	report runnable.VerificationReport
}

func (v verifier) Verify(context.Context, runnable.RunnableRevision) (runnable.VerificationReport, error) {
	return v.report, nil
}

func validSpec() runnable.RunnableSpec {
	return runnable.RunnableSpec{
		FormatVersion: runnable.FormatVersion,
		Identity:      runnable.ContentIdentity{Kind: "operations", ID: "service-startup", Revision: "rev-01"},
		RuntimeProfile: runnable.RuntimeProfile{
			Runtime: runnable.RuntimeNode, ProfileRevision: "profile-01", BaseImage: "registry.example/base@sha256:" + strings.Repeat("a", 64),
			SoftwareVersions: map[string]string{"runtime": "v1"}, Resources: runnable.ResourceLimits{CPU: "2", MemoryBytes: 2 << 30, EphemeralBytes: 4 << 30, MaxProcesses: 512, MaxConcurrentTasks: 2},
			Network: runnable.NetworkPrivate, Topology: "single-host",
			ExecutionBoundaries: []runnable.ExecutionBoundary{
				{ID: "host-write", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 900},
				{ID: "host-read", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 900},
			},
		},
		Source:         runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/source.tar.gz", Digest: testDigest("b")},
		Initialization: []runnable.ActionSpec{{ID: "initialize", Entrypoint: "scripts/init.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-write", TimeoutSeconds: 60, ExpectedExitCodes: []int{0}}},
		ValidationPlan: runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{{
			ID: "observe", TimeoutSeconds: 300, Execution: runnable.PhaseSequential,
			Actions:    []runnable.ActionSpec{{ID: "exercise", Entrypoint: "scripts/exercise.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-write", TimeoutSeconds: 60, ExpectedExitCodes: []int{0}}},
			Assertions: []runnable.AssertionSpec{{ID: "ready", Entrypoint: "scripts/assert.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-read", TimeoutSeconds: 60}},
		}}},
		LifecyclePolicy: runnable.LifecyclePolicy{CreateTimeoutSeconds: 300, ResetTimeoutSeconds: 300, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 600, MaxLifetimeSeconds: 1200},
	}
}

func artifactFor(t *testing.T, spec runnable.RunnableSpec) runnable.ArtifactReference {
	t.Helper()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest := testDigest("c")
	return runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: spec.RuntimeProfile.Runtime, ProviderReference: "incus://breakfix/images/service@" + artifactDigest, ArtifactDigest: artifactDigest, BuiltFromSpecDigest: digest, BuilderVersion: "builder-01"}
}

func reportFor(t *testing.T, revision runnable.RunnableRevision) runnable.VerificationReport {
	t.Helper()
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return runnable.VerificationReport{
		FormatVersion: runnable.FormatVersion, RunnableRevisionDigest: digest, Environment: runnable.EnvironmentIdentity{ID: "environment-01", Provider: "incus", ProfileDigest: testDigest("d")}, Attempt: 1, Passed: true,
		CreatedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Phases:    []runnable.PhaseResult{{ID: "observe", Actions: []runnable.ActionResult{{ID: "exercise", ExitCode: 0, Summary: "exercise complete"}}, Assertions: []runnable.AssertionResult{{ID: "ready", Satisfied: true, Summary: "ready"}}}},
	}
}

func testDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
