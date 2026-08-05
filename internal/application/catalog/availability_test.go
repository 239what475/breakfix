package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
)

func TestConfiguredAvailabilityRequiresTheSelectedReleaseToBeReady(t *testing.T) {
	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("a", 64))
	store := &availabilityStore{release: &catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: catalogdomain.ReleaseInstalling,
		SourceAttempt: 1, NextRunAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}
	availability, err := NewAvailability("registry.example.com/breakfix/catalog@"+string(digest), store)
	if err != nil {
		t.Fatalf("create catalog availability: %v", err)
	}
	if err := availability.Ready(context.Background()); !errors.Is(err, ErrReleaseNotReady) {
		t.Fatalf("installing release readiness = %v, want catalog not ready", err)
	}
	store.release.State = catalogdomain.ReleaseReady
	store.release.CommitID = catalogdomain.CommitIDForRelease(store.release.ID)
	if err := availability.Ready(context.Background()); err != nil {
		t.Fatalf("ready release availability = %v", err)
	}
}

type availabilityStore struct {
	release *catalogdomain.Release
	err     error
}

func (s *availabilityStore) ReleaseByDigest(_ context.Context, digest catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.release == nil || s.release.BundleDigest != digest {
		return nil, catalogdomain.ErrReleaseNotFound
	}
	return s.release, nil
}
