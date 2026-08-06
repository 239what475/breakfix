package httpapi

import (
	"errors"
	"fmt"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/publication"
)

func TestCandidatePublicationInvariantIsDeterministic(t *testing.T) {
	err := candidatePublicationFailure(fmt.Errorf("%w: target conflicts", errCandidatePublicationInvariant))
	if !publication.IsDeterministic(err) {
		t.Fatalf("candidate publication error category = %q, want deterministic", publication.CategoryOf(err))
	}
	if !errors.Is(err, errCandidatePublicationInvariant) {
		t.Fatalf("candidate publication error lost invariant cause: %v", err)
	}
}
