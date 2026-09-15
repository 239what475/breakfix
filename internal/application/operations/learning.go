package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const finalObservationPhaseID = "final-observation"

// AssertionExecutionOutput is the bounded provider response for one
// Operations learning observation. It deliberately has no mutable provider
// references: durable learning history records only the first passing fact.
type AssertionExecutionOutput struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// LearningAssertionExecutor is the Operations boundary for observing an
// existing learning environment. Callers receive only assertions selected
// from the immutable RunnableRevision; no action or arbitrary entrypoint can
// be requested through this port.
type LearningAssertionExecutor interface {
	ExecuteReadOnly(context.Context, runnable.AssertionSpec, runnable.ExecutionBoundary) (AssertionExecutionOutput, error)
}

// EvaluateLearningCheckpoints executes only final, read-only Operations
// assertions in an existing Ready environment. It is distinct from public
// Runnable verification: it never initializes, applies a repair, creates an
// environment, or produces a VerificationReport.
func EvaluateLearningCheckpoints(ctx context.Context, revision runnable.RunnableRevision, environment runnable.EnvironmentIdentity, executor LearningAssertionExecutor) ([]runnable.AssertionResult, error) {
	if executor == nil {
		return nil, errors.New("operations learning assertion executor is required")
	}
	if err := revision.Validate(); err != nil {
		return nil, fmt.Errorf("validate Operations runnable revision: %w", err)
	}
	if revision.Spec.Identity.Kind != operationsContentKind {
		return nil, errors.New("learning checkpoints require an Operations runnable revision")
	}
	if err := environment.Validate(); err != nil {
		return nil, fmt.Errorf("validate learning environment: %w", err)
	}
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		return nil, fmt.Errorf("digest Operations runtime profile: %w", err)
	}
	if environment.Provider != string(revision.Spec.RuntimeProfile.Runtime) || environment.ProfileDigest != profileDigest {
		return nil, errors.New("learning environment is bound to another runtime profile")
	}

	phase, found := finalObservationPhase(revision.Spec.ValidationPlan)
	if !found {
		return []runnable.AssertionResult{}, nil
	}
	results := make([]runnable.AssertionResult, 0, len(phase.Assertions))
	for _, assertion := range phase.Assertions {
		boundary, found := executionBoundary(revision.Spec.RuntimeProfile, assertion.BoundaryID)
		if !found || boundary.Target != assertion.Target || boundary.Permission != runnable.PermissionReadOnly {
			return nil, fmt.Errorf("learning assertion %q does not have an approved read-only boundary", assertion.ID)
		}
		assertionCtx, cancel := context.WithTimeout(ctx, time.Duration(assertion.TimeoutSeconds)*time.Second)
		output, executeErr := executor.ExecuteReadOnly(assertionCtx, assertion, boundary)
		cancel()
		if executeErr != nil {
			return nil, fmt.Errorf("execute learning assertion %q: %w", assertion.ID, executeErr)
		}
		if output.ExitCode != 0 {
			return nil, fmt.Errorf("learning assertion %q exited with %d", assertion.ID, output.ExitCode)
		}
		result, err := runnable.ParseAssertionOutput(output.Stdout, assertion.ID, nil)
		if err != nil {
			return nil, fmt.Errorf("parse learning assertion %q: %w", assertion.ID, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func finalObservationPhase(plan runnable.ValidationPlan) (runnable.ValidationPhase, bool) {
	for _, phase := range plan.Phases {
		if phase.ID == finalObservationPhaseID {
			return phase, true
		}
	}
	return runnable.ValidationPhase{}, false
}

func executionBoundary(profile runnable.RuntimeProfile, id string) (runnable.ExecutionBoundary, bool) {
	for _, boundary := range profile.ExecutionBoundaries {
		if boundary.ID == id {
			return boundary, true
		}
	}
	return runnable.ExecutionBoundary{}, false
}
