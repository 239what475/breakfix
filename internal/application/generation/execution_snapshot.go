package generation

import (
	"github.com/breakfix/breakfix/internal/content/challenge"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

// ExecutionSnapshotter freezes the runtime facts that an accepted candidate
// will use later. Server assembly supplies the provider-backed implementation,
// keeping generator clients independent from registry, Incus, and Kubernetes.
type ExecutionSnapshotter func(challenge.Entry) (domain.ExecutionSnapshot, error)
