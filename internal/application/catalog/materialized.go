package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/breakfix/breakfix/internal/content/challenge"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
)

// ErrMaterializedIntegrity identifies a mismatch between a durable active
// revision and its published challenge directory on the Server data volume.
var ErrMaterializedIntegrity = errors.New("catalog materialized integrity error")

// MaterializedIntegrityError names the content that cannot be safely exposed.
type MaterializedIntegrityError struct {
	ChallengeID string
	SourceSlug  string
	Detail      string
}

func (e *MaterializedIntegrityError) Error() string {
	if e == nil {
		return ErrMaterializedIntegrity.Error()
	}
	identity := strings.TrimSpace(e.ChallengeID)
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

func materializedIntegrity(challengeID, sourceSlug, format string, args ...any) error {
	return &MaterializedIntegrityError{
		ChallengeID: challengeID,
		SourceSlug:  sourceSlug,
		Detail:      fmt.Sprintf(format, args...),
	}
}

// materializedChallengeIndex creates an ID index for the exact immutable
// active revisions selected by the durable lifecycle. Directories not selected
// by that query are historical or not-yet-published content and cannot alter
// current Catalog reads.
func materializedChallengeIndex(revisions []challengedomain.ActiveRevision, challengesDir string) (map[string]challenge.Entry, error) {
	rootInfo, err := os.Lstat(challengesDir)
	if err != nil {
		if os.IsNotExist(err) && len(revisions) == 0 {
			return map[string]challenge.Entry{}, nil
		}
		if os.IsNotExist(err) && len(revisions) > 0 {
			return nil, materializedIntegrity(revisions[0].Challenge.ID, revisions[0].Challenge.SourceSlug, "materialized challenges root is missing")
		}
		return nil, materializedIntegrity("", "", "stat materialized challenges root: %v", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, materializedIntegrity("", "", "materialized challenges root must be a directory, not a symlink")
	}
	result := make(map[string]challenge.Entry, len(revisions))
	for _, active := range revisions {
		if !active.Valid() {
			return nil, materializedIntegrity(active.Challenge.ID, active.Challenge.SourceSlug, "active lifecycle record is invalid")
		}
		if _, exists := result[active.Challenge.ID]; exists {
			return nil, materializedIntegrity(active.Challenge.ID, active.Challenge.SourceSlug, "active lifecycle returned the challenge more than once")
		}
		entry, err := validateMaterializedRevision(challengesDir, active.Challenge, active.Revision)
		if err != nil {
			return nil, err
		}
		result[active.Challenge.ID] = *entry
	}
	return result, nil
}

func validateMaterializedRevision(challengesDir string, stable challengedomain.Challenge, revision challengedomain.Revision) (*challenge.Entry, error) {
	if !stable.Valid() || !revision.Valid() || stable.ID != revision.ChallengeID || stable.SourceKind != revision.SourceKind ||
		stable.SourceRef != revision.SourceRef || stable.SourceSlug != revision.SourceSlug ||
		challenge.ValidateMaterializedPath(revision.MaterializedPath, revision.SourceSlug, revision.ID) != nil {
		return nil, materializedIntegrity(stable.ID, stable.SourceSlug, "durable revision identity conflicts with its lifecycle")
	}
	entry, err := challenge.ValidateDir(filepath.Join(challengesDir, filepath.FromSlash(revision.MaterializedPath)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, materializedIntegrity(stable.ID, revision.SourceSlug, "referenced materialized source is missing")
		}
		return nil, materializedIntegrity(stable.ID, revision.SourceSlug, "invalid materialized source: %v", err)
	}
	expectedImage := revision.Artifact.IncusFingerprint
	if revision.Runtime == challenge.RuntimeK8s {
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
	if entry.ID != revision.ChallengeID || entry.RevisionID != revision.ID || entry.SourceSlug != revision.SourceSlug ||
		entry.Title != revision.Title || entry.Runtime != revision.Runtime || entry.Type != revision.Type || !slices.Equal(entryTags, revisionTags) ||
		entry.ContentRevision != revision.ContentRevision || entry.Revision != revision.MaterializedRevision || entry.Image != expectedImage ||
		!entry.PublishedAt.UTC().Equal(revision.PublishedAt.UTC()) {
		return nil, materializedIntegrity(stable.ID, revision.SourceSlug, "materialized source does not match its durable revision")
	}
	return entry, nil
}
