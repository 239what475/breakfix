package runnableworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const maxAssertionOutputBytes = 256 * 1024

// EnvironmentProvider is the provider-neutral execution boundary. Provider
// implementations create a fresh environment for the complete revision and
// must enforce the supplied target, permission, network, and timeout limits.
type EnvironmentProvider interface {
	CreateVerificationEnvironment(context.Context, runnable.RunnableRevision, int64) (runnable.EnvironmentIdentity, error)
	Execute(context.Context, runnable.EnvironmentIdentity, ExecutionRequest) (ExecutionOutput, error)
}

type ExecutionRequest struct {
	Entrypoint string
	Target     runnable.TargetLocation
	Boundary   runnable.ExecutionBoundary
	Timeout    time.Duration
	ReadOnly   bool
}

// ExecutionOutput contains bounded raw assertion output and references to the
// immutable provider logs/raw streams. The provider must not turn output into
// mutable in-memory state after returning it.
type ExecutionOutput struct {
	ExitCode int
	Summary  string
	Raw      []byte
	Outputs  []runnable.ImmutableReference
}

type Executor struct {
	provider EnvironmentProvider
	now      func() time.Time
}

func NewExecutor(provider EnvironmentProvider) (*Executor, error) {
	if provider == nil {
		return nil, errors.New("runnable verifier requires an environment provider")
	}
	return &Executor{provider: provider, now: time.Now}, nil
}

// Verify executes initialization then each declared phase in order. It never
// interprets the purpose of an action or assertion; false assertions are valid
// machine results, while contract and output protocol defects are artifact
// failures.
func (e *Executor) Verify(ctx context.Context, revision runnable.RunnableRevision, attempt int64) (runnable.VerificationReport, error) {
	if err := revision.Validate(); err != nil {
		return runnable.VerificationReport{}, err
	}
	if attempt <= 0 {
		return runnable.VerificationReport{}, errors.New("runnable verification attempt must be positive")
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		return runnable.VerificationReport{}, err
	}
	environment, err := e.provider.CreateVerificationEnvironment(ctx, revision, attempt)
	if err != nil {
		return runnable.VerificationReport{}, fmt.Errorf("create verification environment: %w", err)
	}
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		return runnable.VerificationReport{}, err
	}
	if err := environment.Validate(); err != nil {
		return runnable.VerificationReport{}, fmt.Errorf("provider returned invalid environment: %w", err)
	}
	if environment.ProfileDigest != profileDigest {
		return runnable.VerificationReport{}, errors.New("provider returned an environment for another runtime profile")
	}
	report := runnable.VerificationReport{
		FormatVersion: runnable.FormatVersion, RunnableRevisionDigest: revisionDigest,
		Environment: environment, Attempt: attempt, CreatedAt: e.now().UTC(),
	}

	initialization := runnable.ValidationPhase{ID: "initialization", Actions: revision.Spec.Initialization}
	result, failure := e.executePhase(ctx, environment, revision.Spec.RuntimeProfile, initialization)
	report.Phases = append(report.Phases, result)
	if failure != nil {
		return finalizeFailure(revision, report, failure), nil
	}
	for _, phase := range revision.Spec.ValidationPlan.Phases {
		result, failure = e.executePhase(ctx, environment, revision.Spec.RuntimeProfile, phase)
		report.Phases = append(report.Phases, result)
		if failure != nil {
			return finalizeFailure(revision, report, failure), nil
		}
	}
	passed, err := runnable.ComputeSpecPassed(revision.Spec, report.Phases)
	if err != nil {
		return runnable.VerificationReport{}, fmt.Errorf("compute verification result: %w", err)
	}
	report.Passed = passed
	return report, nil
}

func (e *Executor) executePhase(ctx context.Context, environment runnable.EnvironmentIdentity, profile runnable.RuntimeProfile, phase runnable.ValidationPhase) (runnable.PhaseResult, *runnable.VerificationFailure) {
	result := runnable.PhaseResult{ID: phase.ID, Actions: make([]runnable.ActionResult, 0, len(phase.Actions)), Assertions: make([]runnable.AssertionResult, 0, len(phase.Assertions))}
	for _, action := range phase.Actions {
		output, failure := e.executeAction(ctx, environment, profile, action)
		if failure != nil {
			return result, failure
		}
		result.Actions = append(result.Actions, output)
		if !containsExitCode(action.ExpectedExitCodes, output.ExitCode) {
			return result, artifactFailure("action-exit", "action exit result does not satisfy its contract")
		}
	}
	for _, assertion := range phase.Assertions {
		output, failure := e.executeAssertion(ctx, environment, profile, assertion)
		if failure != nil {
			return result, failure
		}
		result.Assertions = append(result.Assertions, output)
	}
	return result, nil
}

func (e *Executor) executeAction(ctx context.Context, environment runnable.EnvironmentIdentity, profile runnable.RuntimeProfile, action runnable.ActionSpec) (runnable.ActionResult, *runnable.VerificationFailure) {
	boundary, ok := lookupBoundary(profile, action.BoundaryID)
	if !ok || boundary.Target != action.Target || boundary.Permission != runnable.PermissionReadWrite {
		return runnable.ActionResult{}, artifactFailure("action-boundary", "action does not have an approved write boundary")
	}
	output, err := e.execute(ctx, environment, ExecutionRequest{Entrypoint: action.Entrypoint, Target: action.Target, Boundary: boundary, Timeout: time.Duration(action.TimeoutSeconds) * time.Second})
	if err != nil {
		return runnable.ActionResult{}, infrastructureFailure("action-execution", err)
	}
	if err := validateOutputReferences(output.Outputs); err != nil {
		return runnable.ActionResult{}, artifactFailure("action-output", err.Error())
	}
	return runnable.ActionResult{ID: action.ID, ExitCode: output.ExitCode, Summary: summary(output.Summary, "action completed"), Outputs: output.Outputs}, nil
}

func (e *Executor) executeAssertion(ctx context.Context, environment runnable.EnvironmentIdentity, profile runnable.RuntimeProfile, assertion runnable.AssertionSpec) (runnable.AssertionResult, *runnable.VerificationFailure) {
	boundary, ok := lookupBoundary(profile, assertion.BoundaryID)
	if !ok || boundary.Target != assertion.Target || boundary.Permission != runnable.PermissionReadOnly {
		return runnable.AssertionResult{}, artifactFailure("assertion-boundary", "assertion does not have an approved read-only boundary")
	}
	output, err := e.execute(ctx, environment, ExecutionRequest{Entrypoint: assertion.Entrypoint, Target: assertion.Target, Boundary: boundary, Timeout: time.Duration(assertion.TimeoutSeconds) * time.Second, ReadOnly: true})
	if err != nil {
		return runnable.AssertionResult{}, infrastructureFailure("assertion-execution", err)
	}
	if output.ExitCode != 0 {
		return runnable.AssertionResult{}, artifactFailure("assertion-exit", "assertion command exited unsuccessfully")
	}
	if err := validateOutputReferences(output.Outputs); err != nil {
		return runnable.AssertionResult{}, artifactFailure("assertion-output", err.Error())
	}
	result, err := parseAssertionOutput(output.Raw, assertion.ID, output.Outputs)
	if err != nil {
		return runnable.AssertionResult{}, artifactFailure("assertion-protocol", err.Error())
	}
	return result, nil
}

func (e *Executor) execute(ctx context.Context, environment runnable.EnvironmentIdentity, request ExecutionRequest) (ExecutionOutput, error) {
	if request.Timeout <= 0 || request.Timeout > time.Duration(request.Boundary.MaxTimeout)*time.Second || request.ReadOnly != (request.Boundary.Permission == runnable.PermissionReadOnly) {
		return ExecutionOutput{}, errors.New("execution request exceeds its approved boundary")
	}
	deadline, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	return e.provider.Execute(deadline, environment, request)
}

func lookupBoundary(profile runnable.RuntimeProfile, id string) (runnable.ExecutionBoundary, bool) {
	for _, boundary := range profile.ExecutionBoundaries {
		if boundary.ID == id {
			return boundary, true
		}
	}
	return runnable.ExecutionBoundary{}, false
}

func containsExitCode(codes []int, value int) bool {
	for _, code := range codes {
		if code == value {
			return true
		}
	}
	return false
}

func finalizeFailure(revision runnable.RunnableRevision, report runnable.VerificationReport, failure *runnable.VerificationFailure) runnable.VerificationReport {
	report.Passed = false
	report.Failure = failure
	return report
}

func artifactFailure(reason, message string) *runnable.VerificationFailure {
	return &runnable.VerificationFailure{Class: runnable.FailureArtifact, Component: "verifier", Reason: reason, Message: message}
}

func infrastructureFailure(reason string, err error) *runnable.VerificationFailure {
	return &runnable.VerificationFailure{Class: runnable.FailureInfrastructure, Component: "provider", Reason: reason, Message: summary(err.Error(), "provider execution failed")}
}

func summary(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > runnable.MaxSummaryLength {
		return value[:runnable.MaxSummaryLength]
	}
	return value
}

func validateOutputReferences(outputs []runnable.ImmutableReference) error {
	if len(outputs) == 0 {
		return errors.New("execution did not retain immutable output references")
	}
	if len(outputs) > runnable.MaxOutputReferenceCount {
		return errors.New("execution returned too many output references")
	}
	for _, output := range outputs {
		if err := output.Validate("execution.outputs"); err != nil {
			return err
		}
	}
	return nil
}

func parseAssertionOutput(raw []byte, expectedID string, outputs []runnable.ImmutableReference) (runnable.AssertionResult, error) {
	if len(raw) == 0 || len(raw) > maxAssertionOutputBytes {
		return runnable.AssertionResult{}, errors.New("assertion output is empty or exceeds the protocol limit")
	}
	var document struct {
		Assertions []struct {
			ID        string `json:"id"`
			Satisfied *bool  `json:"satisfied"`
			Summary   string `json:"summary"`
			Details   string `json:"details,omitempty"`
		} `json:"assertions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return runnable.AssertionResult{}, fmt.Errorf("assertion output is not strict JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return runnable.AssertionResult{}, errors.New("assertion output contains trailing JSON")
	}
	if len(document.Assertions) != 1 || document.Assertions[0].ID != expectedID || document.Assertions[0].Satisfied == nil {
		return runnable.AssertionResult{}, errors.New("assertion output must report its declared id exactly once")
	}
	result := runnable.AssertionResult{ID: document.Assertions[0].ID, Satisfied: *document.Assertions[0].Satisfied, Summary: document.Assertions[0].Summary, Details: document.Assertions[0].Details, Outputs: outputs}
	if err := result.Validate(); err != nil {
		return runnable.AssertionResult{}, err
	}
	return result, nil
}
