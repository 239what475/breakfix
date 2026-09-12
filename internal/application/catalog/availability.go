package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"

	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
)

var ErrReleaseNotReady = errors.New("configured catalog release is not ready")

// ReleaseReadinessStore exposes only the configured immutable release state.
// Catalog reads must not infer readiness from a partially materialized
// filesystem or a previously published revision.
type ReleaseReadinessStore interface {
	ReleaseByDigest(context.Context, catalogdomain.BundleDigest) (*catalogdomain.Release, error)
}

// Availability fences catalog-dependent application operations while a
// configured immutable release is being installed. A nil Availability means
// the deployment intentionally starts with no configured release.
type Availability struct {
	digest catalogdomain.BundleDigest
	store  ReleaseReadinessStore
}

func NewAvailability(reference string, store ReleaseReadinessStore) (*Availability, error) {
	if strings.TrimSpace(reference) == "" {
		return nil, nil
	}
	if store == nil {
		return nil, errors.New("catalog availability requires a release store")
	}
	digest, err := catalogdomain.BundleDigestFromReference(reference)
	if err != nil {
		return nil, err
	}
	return &Availability{digest: digest, store: store}, nil
}

func (a *Availability) Ready(ctx context.Context) error {
	if a == nil {
		return nil
	}
	release, err := a.store.ReleaseByDigest(ctx, a.digest)
	if errors.Is(err, catalogdomain.ErrReleaseNotFound) {
		return fmt.Errorf("%w: release %s has not been staged", ErrReleaseNotReady, a.digest)
	}
	if err != nil {
		return fmt.Errorf("read configured catalog release: %w", err)
	}
	if release == nil || release.State != catalogdomain.ReleaseReady {
		state := "unknown"
		if release != nil {
			state = string(release.State)
		}
		return fmt.Errorf("%w: release %s is %s", ErrReleaseNotReady, a.digest, state)
	}
	return nil
}
