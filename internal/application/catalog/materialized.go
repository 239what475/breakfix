package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/content/challenge"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

// ErrMaterializedIntegrity identifies a mismatch between the current Roadmap
// revision and the published challenge directories on the Server data volume.
var ErrMaterializedIntegrity = errors.New("catalog materialized integrity error")

// MaterializedIntegrityError names the binding that cannot be safely exposed.
// Its detail deliberately includes the failed invariant so readiness and
// operators can distinguish a missing directory from a changed artifact.
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

func materializedIntegrity(binding roadmapdomain.ChallengeRef, format string, args ...any) error {
	return &MaterializedIntegrityError{
		ChallengeID: binding.ID,
		SourceSlug:  binding.SourceSlug,
		Detail:      fmt.Sprintf(format, args...),
	}
}

// materializedChallengeIndex creates an ID index for valid published
// directories, then checks every current Roadmap binding against that index.
// Directories not referenced by the revision are intentionally ignored: they
// are valid during the materialize-before-publish window and are reclaimed by
// the existing lifecycle diagnostics instead of blocking catalog reads.
func materializedChallengeIndex(revision roadmapdomain.Revision, challengesDir string) (map[string]challenge.Entry, error) {
	if err := revision.Validate(); err != nil {
		return nil, materializedIntegrity(roadmapdomain.ChallengeRef{}, "current roadmap revision is invalid: %v", err)
	}
	expectedBySlug := make(map[string]roadmapdomain.ChallengeBinding, len(revision.ChallengeBindings))
	for _, binding := range revision.ChallengeBindings {
		expectedBySlug[binding.Challenge.SourceSlug] = binding
	}

	rootInfo, err := os.Lstat(challengesDir)
	if err != nil {
		if os.IsNotExist(err) && len(revision.ChallengeBindings) == 0 {
			return map[string]challenge.Entry{}, nil
		}
		if os.IsNotExist(err) && len(revision.ChallengeBindings) > 0 {
			return nil, materializedIntegrity(revision.ChallengeBindings[0].Challenge, "materialized challenges root is missing")
		}
		return nil, materializedIntegrity(roadmapdomain.ChallengeRef{}, "stat materialized challenges root: %v", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, materializedIntegrity(roadmapdomain.ChallengeRef{}, "materialized challenges root must be a directory, not a symlink")
	}
	directories, err := os.ReadDir(challengesDir)
	if err != nil {
		return nil, materializedIntegrity(roadmapdomain.ChallengeRef{}, "read materialized challenges root: %v", err)
	}

	byID := make(map[string][]challenge.Entry, len(revision.ChallengeBindings))
	for _, directory := range directories {
		name := directory.Name()
		if name == "base" || strings.HasPrefix(name, ".") {
			continue
		}
		binding, referenced := expectedBySlug[name]
		if !referenced {
			continue
		}
		if directory.Type()&os.ModeSymlink != 0 || !directory.IsDir() {
			return nil, materializedIntegrity(binding.Challenge, "materialized source path is not a directory")
		}
		entry, err := challenge.ValidateDir(filepath.Join(challengesDir, name))
		if err != nil {
			return nil, materializedIntegrity(binding.Challenge, "invalid materialized source: %v", err)
		}
		byID[entry.ID] = append(byID[entry.ID], *entry)
	}

	result := make(map[string]challenge.Entry, len(revision.ChallengeBindings))
	for _, binding := range revision.ChallengeBindings {
		candidates := byID[binding.Challenge.ID]
		switch len(candidates) {
		case 0:
			return nil, materializedIntegrity(binding.Challenge, "referenced materialized source is missing")
		case 1:
		default:
			return nil, materializedIntegrity(binding.Challenge, "multiple materialized directories use the same challenge id")
		}
		entry := candidates[0]
		if filepath.Base(entry.Dir) != binding.Challenge.SourceSlug {
			return nil, materializedIntegrity(binding.Challenge, "materialized source path is %q", filepath.Base(entry.Dir))
		}
		if entry.ID != binding.Challenge.ID {
			return nil, materializedIntegrity(binding.Challenge, "materialized id is %q", entry.ID)
		}
		if entry.Title != binding.Challenge.Title {
			return nil, materializedIntegrity(binding.Challenge, "materialized title is %q", entry.Title)
		}
		if entry.ContentRevision != binding.Challenge.ContentRevision {
			return nil, materializedIntegrity(binding.Challenge, "materialized content_revision is %q", entry.ContentRevision)
		}
		if entry.SourceSlug != binding.Challenge.SourceSlug {
			return nil, materializedIntegrity(binding.Challenge, "materialized source_slug is %q", entry.SourceSlug)
		}
		if entry.Revision != binding.Challenge.MaterializedRevision {
			return nil, materializedIntegrity(binding.Challenge, "materialized_revision is %q", entry.Revision)
		}
		result[binding.Challenge.ID] = entry
	}
	return result, nil
}
