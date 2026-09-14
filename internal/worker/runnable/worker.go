// Package runnableworker executes product-neutral materialization and
// verification actions. It has no content, publication, or provider SDK
// dependencies; provider adapters implement the two narrow interfaces.
package runnableworker

import (
	"context"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

type Materializer interface {
	Materialize(context.Context, runnable.RunnableSpec) (runnable.ArtifactReference, error)
}

type Verifier interface {
	Verify(context.Context, runnable.RunnableRevision, int64) (runnable.VerificationReport, error)
}

type Worker struct {
	materializer Materializer
	verifier     Verifier
}

func New(materializer Materializer, verifier Verifier) (*Worker, error) {
	if materializer == nil || verifier == nil {
		return nil, errors.New("runnable worker requires materializer and verifier")
	}
	return &Worker{materializer: materializer, verifier: verifier}, nil
}

// Materialize consumes only a frozen RunnableSpec and returns the immutable
// revision after checking that the provider result remains bound to that spec.
func (w *Worker) Materialize(ctx context.Context, request runnable.MaterializeRequest) (runnable.RunnableRevision, error) {
	if err := request.Validate(); err != nil {
		return runnable.RunnableRevision{}, err
	}
	artifact, err := w.materializer.Materialize(ctx, request.Spec)
	if err != nil {
		return runnable.RunnableRevision{}, err
	}
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: request.Spec, Artifact: artifact}
	if err := revision.Validate(); err != nil {
		return runnable.RunnableRevision{}, fmt.Errorf("materializer returned an unbound artifact: %w", err)
	}
	return revision, nil
}

// Verify accepts an already-bound RunnableRevision. The verifier cannot alter
// the machine result because the report is revalidated against the revision's
// declared action and assertion plan before it is returned.
func (w *Worker) Verify(ctx context.Context, request runnable.VerifyRequest) (runnable.VerificationReport, error) {
	if err := request.Validate(); err != nil {
		return runnable.VerificationReport{}, err
	}
	report, err := w.verifier.Verify(ctx, request.RunnableRevision, request.Attempt)
	if err != nil {
		return runnable.VerificationReport{}, err
	}
	if err := report.Validate(request.RunnableRevision); err != nil {
		return runnable.VerificationReport{}, fmt.Errorf("verifier returned an invalid report: %w", err)
	}
	return report, nil
}
