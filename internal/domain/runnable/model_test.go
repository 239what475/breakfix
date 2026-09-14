package runnable

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunnableSpecDigestIsStableAcrossMapConstruction(t *testing.T) {
	first := validSpec()
	second := validSpec()
	second.RuntimeProfile.SoftwareVersions = map[string]string{}
	second.RuntimeProfile.SoftwareVersions["kubernetes"] = "v1.34.0"
	second.RuntimeProfile.SoftwareVersions["runtime"] = "v0.1.0"

	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("digest first spec: %v", err)
	}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatalf("digest second spec: %v", err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("equivalent specs produced different digests: %s != %s", firstDigest, secondDigest)
	}

	second.Initialization.Entrypoint = "scripts/other-init.sh"
	changedDigest, err := second.Digest()
	if err != nil {
		t.Fatalf("digest changed spec: %v", err)
	}
	if changedDigest == firstDigest {
		t.Fatal("changed spec retained its digest")
	}
}

func TestRunnableRevisionRejectsArtifactFromAnotherSpec(t *testing.T) {
	spec := validSpec()
	revision := validRevision(t, spec)
	revision.Artifact.BuiltFromSpecDigest = testDigest("b")
	if err := revision.Validate(); err == nil || !strings.Contains(err.Error(), "another spec") {
		t.Fatalf("expected cross-spec artifact rejection, got %v", err)
	}
}

func TestParseRunnableSpecRejectsUnknownAndTrailingJSON(t *testing.T) {
	raw, err := CanonicalJSON(validSpec())
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	unknown := bytes.Replace(raw, []byte(`"base_image":"registry.example/base@sha256:`), []byte(`"untrusted":true,"base_image":"registry.example/base@sha256:`), 1)
	if _, err := ParseRunnableSpec(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field rejection, got %v", err)
	}
	if _, err := ParseRunnableSpec(append(raw, []byte(` {}`)...)); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("expected trailing JSON rejection, got %v", err)
	}
}

func TestVerificationReportComputesAssertionFailure(t *testing.T) {
	revision := validRevision(t, validSpec())
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatalf("digest revision: %v", err)
	}
	report := validReport(revisionDigest)
	report.Phases[0].Assertions[0].Satisfied = false
	report.Passed = false
	if err := report.Validate(revision); err != nil {
		t.Fatalf("validate business assertion failure: %v", err)
	}

	report.Passed = true
	if err := report.Validate(revision); err == nil || !strings.Contains(err.Error(), "does not match machine result") {
		t.Fatalf("expected machine result mismatch, got %v", err)
	}
}

func TestVerificationReportClassifiesActionContractViolationAsArtifactFailure(t *testing.T) {
	revision := validRevision(t, validSpec())
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatalf("digest revision: %v", err)
	}
	report := validReport(revisionDigest)
	report.Phases[0].Actions[0].ExitCode = 23
	report.Passed = false
	err = report.Validate(revision)
	var artifactFailure *ArtifactFailure
	if !errors.As(err, &artifactFailure) || artifactFailure.Code != "action-exit" {
		t.Fatalf("expected action contract artifact failure, got %#v", err)
	}
}

func TestAssertionCannotUseWritableBoundary(t *testing.T) {
	spec := validSpec()
	spec.ValidationPlan.Phases[0].Assertions[0].BoundaryID = "host-write"
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected writable assertion boundary rejection, got %v", err)
	}
}

func validSpec() RunnableSpec {
	return RunnableSpec{
		FormatVersion: FormatVersion,
		Identity:      ContentIdentity{Kind: "operations", ID: "service-startup", Revision: "rev-01"},
		RuntimeProfile: RuntimeProfile{
			Runtime:         RuntimeNode,
			ProfileRevision: "node-profile-01",
			BaseImage:       "registry.example/base@sha256:" + strings.Repeat("a", 64),
			SoftwareVersions: map[string]string{
				"runtime":    "v0.1.0",
				"kubernetes": "v1.34.0",
			},
			Resources: ResourceLimits{CPU: "2", MemoryBytes: 2 << 30, EphemeralBytes: 4 << 30, MaxProcesses: 512, MaxConcurrentTasks: 4},
			Network:   NetworkPrivate,
			Topology:  "single-host",
			ExecutionBoundaries: []ExecutionBoundary{
				{ID: "host-write", Target: TargetLocation{Kind: "node", ID: "host"}, Permission: PermissionReadWrite, Network: NetworkPrivate, MaxTimeout: 1800},
				{ID: "host-read", Target: TargetLocation{Kind: "node", ID: "host"}, Permission: PermissionReadOnly, Network: NetworkPrivate, MaxTimeout: 1800},
			},
		},
		Source: SourceArchive{FormatVersion: FormatVersion, Reference: "archives/service-startup.tar.gz", Digest: testDigest("b")},
		Initialization: ActionSpec{
			ID: "initialize", Entrypoint: "scripts/init.sh", Target: TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-write", TimeoutSeconds: 300, ExpectedExitCodes: []int{0},
		},
		ValidationPlan: ValidationPlan{FormatVersion: FormatVersion, Phases: []ValidationPhase{{
			ID: "observe", TimeoutSeconds: 900, Execution: PhaseSequential,
			Actions: []ActionSpec{{
				ID: "exercise", Entrypoint: "scripts/exercise.sh", Target: TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-write", TimeoutSeconds: 300, ExpectedExitCodes: []int{0},
			}},
			Assertions: []AssertionSpec{{
				ID: "service-ready", Entrypoint: "scripts/assert-ready.sh", Target: TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-read", TimeoutSeconds: 300,
			}},
		}}},
		LifecyclePolicy: LifecyclePolicy{CreateTimeoutSeconds: 600, ResetTimeoutSeconds: 600, StopTimeoutSeconds: 300, ReapTimeoutSeconds: 300, IdleTTLSeconds: 3600, MaxLifetimeSeconds: 7200},
	}
}

func validRevision(t *testing.T, spec RunnableSpec) RunnableRevision {
	t.Helper()
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatalf("digest spec: %v", err)
	}
	return RunnableRevision{
		FormatVersion: FormatVersion,
		Spec:          spec,
		Artifact: ArtifactReference{
			FormatVersion: FormatVersion, Runtime: spec.RuntimeProfile.Runtime,
			ProviderReference: "incus://breakfix/images/service-startup@" + testDigest("c"),
			ArtifactDigest:    testDigest("c"), BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01",
		},
	}
}

func validReport(revisionDigest string) VerificationReport {
	return VerificationReport{
		FormatVersion: FormatVersion, RunnableRevisionDigest: revisionDigest,
		Environment: EnvironmentIdentity{ID: "environment-01", Provider: "incus", ProfileDigest: testDigest("d")},
		Attempt:     1, Passed: true, CreatedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Phases: []PhaseResult{{
			ID:         "observe",
			Actions:    []ActionResult{{ID: "exercise", ExitCode: 0, Summary: "exercise completed", Outputs: []ImmutableReference{{Reference: "logs/exercise", Digest: testDigest("e"), SizeBytes: 23}}}},
			Assertions: []AssertionResult{{ID: "service-ready", Satisfied: true, Summary: "service reached ready state", Outputs: []ImmutableReference{{Reference: "logs/assertion", Digest: testDigest("f"), SizeBytes: 42}}}},
		}},
	}
}

func testDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
