package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

// ErrMaterializedIntegrity identifies a mismatch between a durable active
// revision and its published scenario directory on the Server data volume.
var ErrMaterializedIntegrity = errors.New("catalog materialized integrity error")

// MaterializedIntegrityError names the content that cannot be safely exposed.
type MaterializedIntegrityError struct {
	ScenarioID string
	SourceSlug string
	Detail     string
}

func (e *MaterializedIntegrityError) Error() string {
	if e == nil {
		return ErrMaterializedIntegrity.Error()
	}
	identity := strings.TrimSpace(e.ScenarioID)
	if strings.TrimSpace(e.SourceSlug) != "" {
		if identity != "" {
			identity += " at "
		}
		identity += e.SourceSlug
	}
	if identity == "" {
		identity = "catalog"
	}
	return fmt.Sprintf("%s: %s: %s", ErrMaterializedIntegrity, identity, e.Detail)
}

func (e *MaterializedIntegrityError) Unwrap() error { return ErrMaterializedIntegrity }

func materializedIntegrity(scenarioID, sourceSlug, format string, args ...any) error {
	return &MaterializedIntegrityError{
		ScenarioID: scenarioID,
		SourceSlug: sourceSlug,
		Detail:     fmt.Sprintf(format, args...),
	}
}

// materializedScenarioIndex creates an ID index for the exact immutable
// active revisions selected by the durable lifecycle. Directories not selected
// by that query are historical or not-yet-published content and cannot alter
// current Catalog reads.
func materializedScenarioIndex(ctx context.Context, revisions []scenariodomain.ActiveRevision, scenariosDir string, resolver RunnableRevisionResolver) (map[string]scenario.Entry, error) {
	if resolver == nil {
		return nil, materializedIntegrity("", "", "runnable revision resolver is unavailable")
	}
	rootInfo, err := os.Lstat(scenariosDir)
	if err != nil {
		if os.IsNotExist(err) && len(revisions) == 0 {
			return map[string]scenario.Entry{}, nil
		}
		if os.IsNotExist(err) && len(revisions) > 0 {
			return nil, materializedIntegrity(revisions[0].Scenario.ID, revisions[0].Scenario.SourceSlug, "materialized scenarios root is missing")
		}
		return nil, materializedIntegrity("", "", "stat materialized scenarios root: %v", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, materializedIntegrity("", "", "materialized scenarios root must be a directory, not a symlink")
	}
	result := make(map[string]scenario.Entry, len(revisions))
	for _, active := range revisions {
		if !active.Valid() {
			return nil, materializedIntegrity(active.Scenario.ID, active.Scenario.SourceSlug, "active lifecycle record is invalid")
		}
		if _, exists := result[active.Scenario.ID]; exists {
			return nil, materializedIntegrity(active.Scenario.ID, active.Scenario.SourceSlug, "active lifecycle returned the scenario more than once")
		}
		entry, err := validateMaterializedRevision(ctx, scenariosDir, active.Scenario, active.Revision, resolver)
		if err != nil {
			return nil, err
		}
		result[active.Scenario.ID] = *entry
	}
	return result, nil
}

func validateMaterializedRevision(ctx context.Context, scenariosDir string, stable scenariodomain.Scenario, revision scenariodomain.Revision, resolver RunnableRevisionResolver) (*scenario.Entry, error) {
	if !stable.Valid() || !revision.Valid() || stable.ID != revision.ScenarioID || stable.SourceKind != revision.SourceKind ||
		stable.SourceRef != revision.SourceRef || stable.SourceSlug != revision.SourceSlug ||
		scenario.ValidateMaterializedPath(revision.MaterializedPath, revision.SourceSlug, revision.ID) != nil {
		return nil, materializedIntegrity(stable.ID, stable.SourceSlug, "durable revision identity conflicts with its lifecycle")
	}
	entry, err := scenario.ValidateDir(filepath.Join(scenariosDir, filepath.FromSlash(revision.MaterializedPath)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, materializedIntegrity(stable.ID, revision.SourceSlug, "referenced materialized source is missing")
		}
		return nil, materializedIntegrity(stable.ID, revision.SourceSlug, "invalid materialized source: %v", err)
	}
	if resolver == nil {
		return nil, materializedIntegrity(stable.ID, stable.SourceSlug, "runnable revision resolver is unavailable")
	}
	runnableRevision, err := resolver.ResolveRunnableRevision(ctx, revision.RunnableRevisionRef.ID, revision.RunnableRevisionRef.Digest)
	if err != nil {
		return nil, materializedIntegrity(stable.ID, stable.SourceSlug, "resolve runnable revision: %v", err)
	}
	if err := runnableRevision.Validate(); err != nil {
		return nil, materializedIntegrity(stable.ID, stable.SourceSlug, "stored runnable revision is invalid: %v", err)
	}
	expectedImage, err := materializedArtifactImage(runnableRevision.Artifact)
	if err != nil {
		return nil, materializedIntegrity(stable.ID, stable.SourceSlug, "resolve runnable artifact: %v", err)
	}
	revisionTags := revision.Tags
	if revisionTags == nil {
		revisionTags = []string{}
	}
	entryTags := entry.Tags
	if entryTags == nil {
		entryTags = []string{}
	}
	if entry.ID != revision.ScenarioID || entry.RevisionID != revision.ID || entry.SourceSlug != revision.SourceSlug ||
		entry.Title != revision.Title || entry.Runtime != string(runnableRevision.Spec.RuntimeProfile.Runtime) || entry.Type != revision.Type || !slices.Equal(entryTags, revisionTags) ||
		entry.ContentRevision != revision.ContentRevision || entry.Revision != revision.MaterializedRevision || entry.Image != expectedImage ||
		!entry.PublishedAt.UTC().Equal(revision.PublishedAt.UTC()) {
		return nil, materializedIntegrity(stable.ID, revision.SourceSlug, "materialized source does not match its durable revision")
	}
	return entry, nil
}

func materializedArtifactImage(artifact runnable.ArtifactReference) (string, error) {
	if err := artifact.Validate(); err != nil {
		return "", err
	}
	switch artifact.Runtime {
	case runnable.RuntimeNode:
		_, digest, found := strings.Cut(strings.TrimPrefix(artifact.ProviderReference, "incus://"), "@")
		if !found || digest != artifact.ArtifactDigest {
			return "", errors.New("node artifact reference is invalid")
		}
		return strings.TrimPrefix(digest, "sha256:"), nil
	case runnable.RuntimeK8s:
		return artifact.ProviderReference, nil
	default:
		return "", errors.New("unsupported runnable artifact runtime")
	}
}
