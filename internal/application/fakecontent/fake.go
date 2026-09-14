// Package fakecontent is a deliberately small content adapter used to prove
// that new content kinds can exercise the public runnable worker without
// adding product branches to that worker.
package fakecontent

import (
	"context"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const (
	contentKind = "fake"
	contentID   = "smoke"
	contentRev  = "rev-01"
)

// Spec returns a deterministic, self-contained runnable contract. It has one
// initialization action and one read-only assertion so both public execution
// paths are exercised by the compatibility test.
func Spec() runnable.RunnableSpec {
	return runnable.RunnableSpec{
		FormatVersion: runnable.FormatVersion,
		Identity:      runnable.ContentIdentity{Kind: contentKind, ID: contentID, Revision: contentRev},
		RuntimeProfile: runnable.RuntimeProfile{
			Runtime: runnable.RuntimeNode, ProfileRevision: "fake-profile-01", BaseImage: "incus://fake/base@sha256:" + strings.Repeat("a", 64),
			SoftwareVersions: map[string]string{"fake": "v1"},
			Resources:        runnable.ResourceLimits{CPU: "1", MemoryBytes: 256 << 20, EphemeralBytes: 512 << 20, MaxProcesses: 32, MaxConcurrentTasks: 1},
			Network:          runnable.NetworkNone, Topology: "single-host",
			ExecutionBoundaries: []runnable.ExecutionBoundary{
				{ID: "fake-write", Target: runnable.TargetLocation{Kind: "node", ID: "fake"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkNone, MaxTimeout: 30},
				{ID: "fake-read", Target: runnable.TargetLocation{Kind: "node", ID: "fake"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkNone, MaxTimeout: 30},
			},
		},
		Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "fake://source", Digest: "sha256:" + strings.Repeat("b", 64)},
		Initialization: []runnable.ActionSpec{{
			ID: "initialize", Entrypoint: "init.sh", Target: runnable.TargetLocation{Kind: "node", ID: "fake"}, BoundaryID: "fake-write", TimeoutSeconds: 5, ExpectedExitCodes: []int{0},
		}},
		ValidationPlan: runnable.ValidationPlan{
			FormatVersion: runnable.FormatVersion,
			Phases: []runnable.ValidationPhase{{
				ID: "observe", TimeoutSeconds: 10, Execution: runnable.PhaseSequential,
				Assertions: []runnable.AssertionSpec{{ID: "ready", Entrypoint: "check.sh", Target: runnable.TargetLocation{Kind: "node", ID: "fake"}, BoundaryID: "fake-read", TimeoutSeconds: 5}},
			}},
		},
		LifecyclePolicy: runnable.LifecyclePolicy{CreateTimeoutSeconds: 10, ResetTimeoutSeconds: 10, StopTimeoutSeconds: 10, ReapTimeoutSeconds: 10, IdleTTLSeconds: 10, MaxLifetimeSeconds: 30},
	}
}

// Materializer is the fake content's provider-neutral materialization adapter.
// It does not access a provider; the returned reference is enough to exercise
// the worker's spec/artifact binding checks.
type Materializer struct{}

func (Materializer) Materialize(_ context.Context, request runnable.MaterializeRequest) (runnable.ArtifactReference, error) {
	digest, err := request.Spec.Digest()
	if err != nil {
		return runnable.ArtifactReference{}, err
	}
	artifactDigest := "sha256:" + strings.Repeat("c", 64)
	return runnable.ArtifactReference{
		FormatVersion: runnable.FormatVersion, Runtime: request.Spec.RuntimeProfile.Runtime,
		ProviderReference: "fake://artifact@" + artifactDigest, ArtifactDigest: artifactDigest,
		BuiltFromSpecDigest: digest, BuilderVersion: "fake-builder-01",
	}, nil
}

// Verifier is the fake content's deterministic verification adapter.
type Verifier struct{}

func (Verifier) Verify(_ context.Context, request runnable.VerifyRequest) (runnable.VerificationReport, error) {
	profileDigest, err := request.RunnableRevision.Spec.RuntimeProfile.Digest()
	if err != nil {
		return runnable.VerificationReport{}, err
	}
	return runnable.VerificationReport{
		FormatVersion: runnable.FormatVersion, RunnableRevisionDigest: request.RunnableRevisionDigest,
		Environment: runnable.EnvironmentIdentity{ID: "fake-environment", Provider: "fake", ProfileDigest: profileDigest},
		Attempt:     request.Attempt, Passed: true, CreatedAt: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		Phases: []runnable.PhaseResult{
			{ID: "initialization", Actions: []runnable.ActionResult{{ID: "initialize", ExitCode: 0, Summary: "initialized"}}},
			{ID: "observe", Assertions: []runnable.AssertionResult{{ID: "ready", Satisfied: true, Summary: "ready"}}},
		},
	}, nil
}
