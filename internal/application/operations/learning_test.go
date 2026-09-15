package operations

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestEvaluateLearningCheckpointsExecutesOnlyFinalReadOnlyAssertions(t *testing.T) {
	entry := operationsNodeEntry()
	entry.HasReferenceRepair = true
	spec, err := Compile(Input{ContentID: "operations-node", ContentRevision: "revision-01", Entry: entry, Source: source()}, config())
	if err != nil {
		t.Fatal(err)
	}
	revision := revisionForSpec(t, spec)
	profileDigest, err := spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	executor := &learningAssertionExecutor{}
	results, err := EvaluateLearningCheckpoints(context.Background(), revision, runnable.EnvironmentIdentity{ID: "environment-01", Provider: "node", ProfileDigest: profileDigest}, executor)
	if err != nil {
		t.Fatalf("evaluate learning checkpoints: %v", err)
	}
	if len(results) != 2 || !results[0].Satisfied || !results[1].Satisfied {
		t.Fatalf("results = %#v", results)
	}
	if len(executor.assertions) != 2 {
		t.Fatalf("executed assertions = %#v", executor.assertions)
	}
	for _, assertion := range executor.assertions {
		if !strings.Contains(assertion.Entrypoint, "/assertions/final-") {
			t.Fatalf("non-final assertion executed: %#v", assertion)
		}
	}
	for _, boundary := range executor.boundaries {
		if boundary.Permission != runnable.PermissionReadOnly {
			t.Fatalf("non-read-only boundary executed: %#v", boundary)
		}
	}
}

func TestEvaluateLearningCheckpointsSkipsObservationOnlyRevision(t *testing.T) {
	entry := operationsNodeEntry()
	spec, err := Compile(Input{ContentID: "operations-node", ContentRevision: "revision-01", Entry: entry, Source: source()}, config())
	if err != nil {
		t.Fatal(err)
	}
	revision := revisionForSpec(t, spec)
	profileDigest, err := spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	executor := &learningAssertionExecutor{}
	results, err := EvaluateLearningCheckpoints(context.Background(), revision, runnable.EnvironmentIdentity{ID: "environment-01", Provider: "node", ProfileDigest: profileDigest}, executor)
	if err != nil || len(results) != 0 || len(executor.assertions) != 0 {
		t.Fatalf("observation-only learning evaluation = %#v, executor=%#v, err=%v", results, executor, err)
	}
}

func TestEvaluateLearningCheckpointsRejectsNonOperationsRevision(t *testing.T) {
	entry := operationsNodeEntry()
	entry.HasReferenceRepair = true
	spec, err := Compile(Input{ContentID: "operations-node", ContentRevision: "revision-01", Entry: entry, Source: source()}, config())
	if err != nil {
		t.Fatal(err)
	}
	spec.Identity.Kind = "documentation-practice"
	revision := revisionForSpec(t, spec)
	profileDigest, err := spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EvaluateLearningCheckpoints(context.Background(), revision, runnable.EnvironmentIdentity{ID: "environment-01", Provider: "node", ProfileDigest: profileDigest}, &learningAssertionExecutor{}); err == nil || !strings.Contains(err.Error(), "Operations runnable") {
		t.Fatalf("non-Operations revision was accepted: %v", err)
	}
}

type learningAssertionExecutor struct {
	assertions []runnable.AssertionSpec
	boundaries []runnable.ExecutionBoundary
}

func (e *learningAssertionExecutor) ExecuteReadOnly(_ context.Context, assertion runnable.AssertionSpec, boundary runnable.ExecutionBoundary) (AssertionExecutionOutput, error) {
	e.assertions = append(e.assertions, assertion)
	e.boundaries = append(e.boundaries, boundary)
	return AssertionExecutionOutput{Stdout: []byte(fmt.Sprintf(`{"assertions":[{"id":%q,"satisfied":true,"summary":"ready"}]}`, assertion.ID))}, nil
}

func revisionForSpec(t *testing.T, spec runnable.RunnableSpec) runnable.RunnableRevision {
	t.Helper()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{
		FormatVersion: runnable.FormatVersion, Runtime: spec.RuntimeProfile.Runtime,
		ProviderReference: "registry.example/operations@sha256:" + strings.Repeat("a", 64), ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
		BuiltFromSpecDigest: digest, BuilderVersion: "builder-01",
	}}
}
