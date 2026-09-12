package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/breakfix/breakfix/internal/content/scenario"
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
func materializedScenarioIndex(revisions []scenariodomain.ActiveRevision, scenariosDir string) (map[string]scenario.Entry, error) {
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
		entry, err := validateMaterializedRevision(scenariosDir, active.Scenario, active.Revision)
		if err != nil {
			return nil, err
		}
		result[active.Scenario.ID] = *entry
	}
	return result, nil
}

func validateMaterializedRevision(scenariosDir string, stable scenariodomain.Scenario, revision scenariodomain.Revision) (*scenario.Entry, error) {
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
	expectedImage := revision.Artifact.IncusFingerprint
	if revision.Runtime == scenario.RuntimeK8s {
		expectedImage = revision.Artifact.OCIReference
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
		entry.Title != revision.Title || entry.Runtime != revision.Runtime || entry.Type != revision.Type || !slices.Equal(entryTags, revisionTags) ||
		entry.ContentRevision != revision.ContentRevision || entry.Revision != revision.MaterializedRevision || entry.Image != expectedImage ||
		!entry.PublishedAt.UTC().Equal(revision.PublishedAt.UTC()) {
		return nil, materializedIntegrity(stable.ID, revision.SourceSlug, "materialized source does not match its durable revision")
	}
	return entry, nil
}
