package runnableworker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestExecutorRunsInitializationActionsAndReadOnlyAssertions(t *testing.T) {
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: validSpec(), Artifact: artifactFor(t, validSpec())}
	provider := &fakeEnvironmentProvider{results: map[string]ExecutionOutput{
		"scripts/init.sh":     output(0, "initialized", nil),
		"scripts/exercise.sh": output(0, "exercised", nil),
		"scripts/assert.sh":   output(0, "asserted", []byte(`{"assertions":[{"id":"ready","satisfied":true,"summary":"ready"}]}`)),
	}}
	executor, err := NewExecutor(provider)
	if err != nil {
		t.Fatal(err)
	}
	executor.now = func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	report, err := executor.Verify(context.Background(), revision, 2)
	if err != nil {
		t.Fatalf("execute verification: %v", err)
	}
	if !report.Passed || len(report.Phases) != 2 || len(provider.requests) != 3 || provider.requests[0].ReadOnly || provider.requests[1].ReadOnly || !provider.requests[2].ReadOnly {
		t.Fatalf("unexpected report or execution requests: report=%#v requests=%#v", report, provider.requests)
	}
	if err := report.Validate(revision); err != nil {
		t.Fatalf("validate complete report: %v", err)
	}
}

func TestExecutorKeepsUnsatisfiedAssertionAsBusinessResult(t *testing.T) {
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: validSpec(), Artifact: artifactFor(t, validSpec())}
	provider := &fakeEnvironmentProvider{results: map[string]ExecutionOutput{
		"scripts/init.sh":     output(0, "initialized", nil),
		"scripts/exercise.sh": output(0, "exercised", nil),
		"scripts/assert.sh":   output(0, "asserted", []byte(`{"assertions":[{"id":"ready","satisfied":false,"summary":"not ready"}]}`)),
	}}
	executor, err := NewExecutor(provider)
	if err != nil {
		t.Fatal(err)
	}
	report, err := executor.Verify(context.Background(), revision, 1)
	if err != nil {
		t.Fatalf("execute verification: %v", err)
	}
	if report.Passed || report.Failure != nil {
		t.Fatalf("unsatisfied assertion must remain a normal machine result: %#v", report)
	}
	if err := report.Validate(revision); err != nil {
		t.Fatalf("validate false assertion report: %v", err)
	}
}

func TestExecutorClassifiesInvalidAssertionProtocolAsArtifactFailure(t *testing.T) {
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: validSpec(), Artifact: artifactFor(t, validSpec())}
	provider := &fakeEnvironmentProvider{results: map[string]ExecutionOutput{
		"scripts/init.sh":     output(0, "initialized", nil),
		"scripts/exercise.sh": output(0, "exercised", nil),
		"scripts/assert.sh":   output(0, "asserted", []byte(`{"assertions":[{"id":"ready","satisfied":true,"summary":"ready"}],"extra":true}`)),
	}}
	executor, err := NewExecutor(provider)
	if err != nil {
		t.Fatal(err)
	}
	report, err := executor.Verify(context.Background(), revision, 1)
	if err != nil {
		t.Fatalf("execute verification: %v", err)
	}
	if report.Failure == nil || report.Failure.Class != runnable.FailureArtifact || report.Failure.Reason != "assertion-protocol" {
		t.Fatalf("invalid protocol was not classified as an artifact failure: %#v", report)
	}
	if err := report.Validate(revision); err != nil {
		t.Fatalf("validate artifact failure report: %v", err)
	}
}

func TestExecutorClassifiesProviderExecutionErrorAsInfrastructureFailure(t *testing.T) {
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: validSpec(), Artifact: artifactFor(t, validSpec())}
	provider := &fakeEnvironmentProvider{results: map[string]ExecutionOutput{}, errForEntrypoint: map[string]error{"scripts/exercise.sh": errors.New("provider temporarily unavailable")}}
	provider.results["scripts/init.sh"] = output(0, "initialized", nil)
	executor, err := NewExecutor(provider)
	if err != nil {
		t.Fatal(err)
	}
	report, err := executor.Verify(context.Background(), revision, 1)
	if err != nil {
		t.Fatalf("execute verification: %v", err)
	}
	if report.Failure == nil || report.Failure.Class != runnable.FailureInfrastructure || report.Passed {
		t.Fatalf("provider failure was not classified as infrastructure: %#v", report)
	}
	if err := report.Validate(revision); err != nil {
		t.Fatalf("validate infrastructure failure report: %v", err)
	}
}

type fakeEnvironmentProvider struct {
	results          map[string]ExecutionOutput
	errForEntrypoint map[string]error
	requests         []ExecutionRequest
}

func (p *fakeEnvironmentProvider) CreateVerificationEnvironment(_ context.Context, revision runnable.RunnableRevision, _ int64) (runnable.EnvironmentIdentity, error) {
	digest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		return runnable.EnvironmentIdentity{}, err
	}
	return runnable.EnvironmentIdentity{ID: "environment-01", Provider: "incus", ProfileDigest: digest}, nil
}

func (p *fakeEnvironmentProvider) Execute(_ context.Context, _ runnable.EnvironmentIdentity, request ExecutionRequest) (ExecutionOutput, error) {
	p.requests = append(p.requests, request)
	if err := p.errForEntrypoint[request.Entrypoint]; err != nil {
		return ExecutionOutput{}, err
	}
	result, exists := p.results[request.Entrypoint]
	if !exists {
		return ExecutionOutput{}, errors.New("missing fake execution output")
	}
	return result, nil
}

func output(exitCode int, summary string, raw []byte) ExecutionOutput {
	return ExecutionOutput{ExitCode: exitCode, Summary: summary, Raw: raw, Outputs: []runnable.ImmutableReference{{Reference: "logs/" + strings.ReplaceAll(summary, " ", "-"), Digest: testDigest("f"), SizeBytes: int64(len(raw))}}}
}
