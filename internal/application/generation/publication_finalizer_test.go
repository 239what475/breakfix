package generation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/publication"
)

func TestPublicationInvariantIsDeterministic(t *testing.T) {
	err := publicationFailure(fmt.Errorf("%w: target conflicts", errPublicationInvariant))
	if !publication.IsDeterministic(err) {
		t.Fatalf("publication error category = %q, want deterministic", publication.CategoryOf(err))
	}
	if !errors.Is(err, errPublicationInvariant) {
		t.Fatalf("publication error lost invariant cause: %v", err)
	}
}
