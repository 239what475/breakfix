package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/generation"
	taxonomydomain "github.com/breakfix/breakfix/internal/domain/taxonomy"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

var errReleaseCommitSemantic = errors.New("catalog release commit has invalid content")

// ReleaseStore owns release lifecycle and durable commit identities. Its
// implementation provides the transaction that makes a prepared filesystem
// tree visible to the rest of the platform.
type ReleaseStore interface {
	GetRelease(context.Context, string) (*catalogdomain.Release, error)
	ListEntries(context.Context, string) ([]catalogdomain.Entry, error)
	GetEntryCommit(context.Context, string) (*catalogdomain.Commit, error)
	PrepareCommit(context.Context, string, catalogdomain.RuntimeIdentity, time.Time) (*catalogdomain.Commit, error)
	MarkCommitMaterialized(context.Context, string, time.Time) (*catalogdomain.Commit, error)
	BeginReleaseCommit(context.Context, string, time.Time) (*catalogdomain.Release, bool, error)
	CompleteReleaseCommit(context.Context, string, time.Time) (*catalogdomain.Release, error)
	ListRecoverableReleases(context.Context) ([]catalogdomain.Release, error)
	StartReleaseCleanup(context.Context, string, string, time.Time) error
	ReleaseCleanupReady(context.Context, string) (bool, error)
	CompleteReleaseCleanup(context.Context, string, time.Time) (*catalogdomain.Release, error)
}

type CandidateReader interface {
	GetCandidateRevision(context.Context, string) (*generation.Revision, error)
}

type ReleaseCoordinatorConfig struct {
	DataDir       string
	ChallengesDir string
	Taxonomy      *taxonomy.Store
	Releases      ReleaseStore
	Candidates    CandidateReader
}

// ReleaseCoordinator finalizes a verified release. It is Server-owned: the
// Worker can only report build and verification results under its lease and
// never receives a target challenge ID or direct catalog filesystem access.
type ReleaseCoordinator struct {
	dataDir       string
	challengesDir string
	taxonomy      *taxonomy.Store
	releases      ReleaseStore
	candidates    CandidateReader
	now           func() time.Time
}

func NewReleaseCoordinator(config ReleaseCoordinatorConfig) (*ReleaseCoordinator, error) {
	if strings.TrimSpace(config.DataDir) == "" || strings.TrimSpace(config.ChallengesDir) == "" ||
		config.Taxonomy == nil || config.Releases == nil || config.Candidates == nil {
		return nil, errors.New("catalog release coordinator requires data paths, taxonomy, release store, and candidate reader")
	}
	return &ReleaseCoordinator{
		dataDir: filepath.Clean(config.DataDir), challengesDir: filepath.Clean(config.ChallengesDir), taxonomy: config.Taxonomy,
		releases: config.Releases, candidates: config.Candidates, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Advance drives one durable release state. It is safe to call after every
// release verification result and during Server startup recovery.
func (c *ReleaseCoordinator) Advance(ctx context.Context, releaseID string) error {
	if c == nil {
		return errors.New("catalog release coordinator is not configured")
	}
	release, err := c.releases.GetRelease(ctx, releaseID)
	if err != nil {
		return err
	}
	return c.advance(ctx, *release)
}

// Recover resumes interrupted installs, commits, cleanup, and post-ready
// staging removal. Worker-owned side effects remain lease-fenced and are
// never replayed by this loop.
func (c *ReleaseCoordinator) Recover(ctx context.Context) error {
	if c == nil {
		return errors.New("catalog release coordinator is not configured")
	}
	releases, err := c.releases.ListRecoverableReleases(ctx)
	if err != nil {
		return err
	}
	for _, release := range releases {
		if err := c.advance(ctx, release); err != nil {
			return err
		}
	}
	return nil
}

func (c *ReleaseCoordinator) advance(ctx context.Context, release catalogdomain.Release) error {
	now := c.now().UTC()
	if release.DeadlineAt != nil && !now.Before(release.DeadlineAt.UTC()) &&
		release.State != catalogdomain.ReleaseReady && release.State != catalogdomain.ReleaseCleaningUp && release.State != catalogdomain.ReleaseFailed {
		return c.beginCleanup(ctx, release, "catalog release deadline exceeded")
	}
	switch release.State {
	case catalogdomain.ReleaseInstalling:
		started, committing, err := c.releases.BeginReleaseCommit(ctx, release.ID, now)
		if err != nil || !committing {
			return err
		}
		return c.commit(ctx, *started)
	case catalogdomain.ReleaseCommitting:
		return c.commit(ctx, release)
	case catalogdomain.ReleaseCleaningUp:
		return c.cleanup(ctx, release)
	case catalogdomain.ReleaseReady:
		return c.removeReleaseResidue(ctx, release, false)
	case catalogdomain.ReleaseFailed:
		return nil
	default:
		return fmt.Errorf("catalog release %q has unsupported state %q", release.ID, release.State)
	}
}

func (c *ReleaseCoordinator) commit(ctx context.Context, release catalogdomain.Release) error {
	entries, err := c.releases.ListEntries(ctx, release.ID)
	if err != nil {
		return err
	}
	source, err := LoadPortableSource(c.sourceRoot(release.ID))
	if err != nil {
		return c.beginCleanup(ctx, release, fmt.Sprintf("load staged catalog source: %v", err))
	}
	byPath := make(map[string]SourceChallenge, len(source.Challenges))
	for _, value := range source.Challenges {
		byPath[value.Path] = value
	}
	prepared := make(map[string]*challenge.Entry, len(entries))
	commits := make(map[string]*catalogdomain.Commit, len(entries))
	for _, entry := range entries {
		if entry.State != catalogdomain.EntryReadyToCommit {
			return c.beginCleanup(ctx, release, fmt.Sprintf("catalog entry %q is not ready to commit", entry.ID))
		}
		sourceChallenge, exists := byPath[entry.SourcePath]
		if !exists || sourceChallenge.ContentRevision != entry.ContentRevision {
			return c.beginCleanup(ctx, release, fmt.Sprintf("catalog entry %q does not match staged source", entry.ID))
		}
		commit, err := c.ensureCommitIdentity(ctx, entry, sourceChallenge.Entry)
		if err != nil {
			return err
		}
		candidateRevision, err := c.candidates.GetCandidateRevision(ctx, entry.CandidateRevisionID)
		if err != nil {
			return c.beginCleanup(ctx, release, fmt.Sprintf("load catalog candidate %q: %v", entry.ID, err))
		}
		expected, err := c.prepareEntry(release, entry, *commit, *candidateRevision)
		if err != nil {
			return c.handleCommitError(ctx, release, err)
		}
		prepared[entry.SourcePath] = expected
		commits[entry.ID] = commit
	}
	runtimeTaxonomy, err := compilePortableTaxonomy(source.Taxonomy, prepared)
	if err != nil {
		return c.beginCleanup(ctx, release, err.Error())
	}
	for _, entry := range entries {
		expected := prepared[entry.SourcePath]
		if err := c.promotePreparedEntry(release, entry, *commits[entry.ID], expected); err != nil {
			return c.handleCommitError(ctx, release, err)
		}
		if _, err := c.releases.MarkCommitMaterialized(ctx, entry.ID, c.now().UTC()); err != nil {
			return err
		}
	}
	if _, err := c.taxonomy.Publish(runtimeTaxonomy); err != nil {
		return err
	}
	if _, err := c.releases.CompleteReleaseCommit(ctx, release.ID, c.now().UTC()); err != nil {
		return err
	}
	return c.removeReleaseResidue(ctx, release, false)
}

func (c *ReleaseCoordinator) handleCommitError(ctx context.Context, release catalogdomain.Release, err error) error {
	if errors.Is(err, errReleaseCommitSemantic) {
		return c.beginCleanup(ctx, release, err.Error())
	}
	return err
}

func (c *ReleaseCoordinator) beginCleanup(ctx context.Context, release catalogdomain.Release, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "catalog release cleanup requested"
	}
	return c.releases.StartReleaseCleanup(ctx, release.ID, reason, c.now().UTC())
}

func (c *ReleaseCoordinator) cleanup(ctx context.Context, release catalogdomain.Release) error {
	if err := c.beginCleanup(ctx, release, release.LastError); err != nil {
		return err
	}
	ready, err := c.releases.ReleaseCleanupReady(ctx, release.ID)
	if err != nil {
		return err
	}
	if !ready {
		return nil
	}
	if err := c.removeReleaseTaxonomyIfCurrent(ctx, release); err != nil {
		return err
	}
	if err := c.removeReleaseResidue(ctx, release, true); err != nil {
		return err
	}
	_, err = c.releases.CompleteReleaseCleanup(ctx, release.ID, c.now().UTC())
	return err
}

func (c *ReleaseCoordinator) ensureCommitIdentity(ctx context.Context, entry catalogdomain.Entry, source challenge.Entry) (*catalogdomain.Commit, error) {
	commit, err := c.releases.GetEntryCommit(ctx, entry.ID)
	if err != nil {
		return nil, err
	}
	if commit.RuntimeIdentity != nil {
		return commit, nil
	}
	challengeID := challenge.NewID()
	identity := catalogdomain.RuntimeIdentity{
		ChallengeID: challengeID,
		Slug:        challenge.SourceSlugFor(source.Title, challengeID),
	}
	return c.releases.PrepareCommit(ctx, entry.ID, identity, c.now().UTC())
}

// prepareEntry validates the portable source again and writes the target
// challenge directory under the release staging root. It never changes the
// public challenges directory.
func (c *ReleaseCoordinator) prepareEntry(release catalogdomain.Release, entry catalogdomain.Entry, commit catalogdomain.Commit, revision generation.Revision) (*challenge.Entry, error) {
	if commit.RuntimeIdentity == nil || !commit.RuntimeIdentity.Valid() {
		return nil, fmt.Errorf("%w: catalog entry has no runtime identity", errReleaseCommitSemantic)
	}
	if revision.Source.Kind != generation.SourceRelease || revision.Source.Ref != entry.ID ||
		revision.SourceRevision != string(entry.ContentRevision) || revision.Artifact == nil ||
		revision.Verification == nil || !revision.Verification.Passed {
		return nil, fmt.Errorf("%w: catalog candidate is not a verified release artifact", errReleaseCommitSemantic)
	}
	if err := revision.Artifact.Validate(revision.Snapshot.Runtime); err != nil {
		return nil, fmt.Errorf("%w: catalog candidate artifact is invalid: %v", errReleaseCommitSemantic, err)
	}
	source, err := sourcePath(c.sourceRoot(release.ID), entry.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("%w: catalog source path is invalid: %v", errReleaseCommitSemantic, err)
	}
	portable, err := challenge.ValidatePortableDir(source)
	if err != nil {
		return nil, fmt.Errorf("%w: validate catalog candidate: %v", errReleaseCommitSemantic, err)
	}
	contentRevision, err := ContentRevision(source)
	if err != nil {
		return nil, err
	}
	if contentRevision != entry.ContentRevision {
		return nil, fmt.Errorf("%w: catalog candidate content revision differs from its release entry", errReleaseCommitSemantic)
	}
	if challenge.SourceSlugFor(portable.Title, commit.RuntimeIdentity.ChallengeID) != commit.RuntimeIdentity.Slug {
		return nil, fmt.Errorf("%w: catalog runtime slug does not match immutable candidate", errReleaseCommitSemantic)
	}
	image := revision.Artifact.IncusFingerprint
	if revision.Artifact.Runtime == challenge.RuntimeK8s {
		image = revision.Artifact.OCIReference
	}
	root, err := os.MkdirTemp(c.releaseStageRoot(release.ID), ".prepare-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	expectedRoot := filepath.Join(root, "expected")
	expected, err := challenge.PromoteDirectoryAt(expectedRoot, source, commit.RuntimeIdentity.ChallengeID, image, string(entry.ContentRevision), release.CreatedAt)
	if err != nil {
		return nil, err
	}
	finalTarget := filepath.Join(c.challengesDir, commit.RuntimeIdentity.Slug)
	if existing, exists, err := existingReleaseMaterialization(finalTarget, expected); err != nil || exists {
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errReleaseCommitSemantic, err)
		}
		return existing, nil
	}
	preparedRoot := c.preparedChallengesRoot(release.ID)
	preparedTarget := filepath.Join(preparedRoot, commit.RuntimeIdentity.Slug)
	if existing, exists, err := existingReleaseMaterialization(preparedTarget, expected); err != nil || exists {
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errReleaseCommitSemantic, err)
		}
		return existing, nil
	}
	materialized, err := challenge.MaterializeWithSlug(preparedRoot, expected.ID, expected.SourceSlug, func(destination string) error {
		return challenge.CopyRegularFiles(expected.Dir, destination)
	})
	if err == nil {
		return materialized, nil
	}
	if existing, exists, validationErr := existingReleaseMaterialization(preparedTarget, expected); exists || validationErr != nil {
		if validationErr != nil {
			return nil, fmt.Errorf("%w: %v", errReleaseCommitSemantic, validationErr)
		}
		return existing, nil
	}
	return nil, err
}

// promotePreparedEntry moves an already validated staged directory into the
// final catalog only after every entry has been prepared. A crash between
// moves is recoverable because the persisted identity validates either path.
func (c *ReleaseCoordinator) promotePreparedEntry(release catalogdomain.Release, entry catalogdomain.Entry, commit catalogdomain.Commit, expected *challenge.Entry) error {
	if expected == nil || commit.RuntimeIdentity == nil || !commit.RuntimeIdentity.Valid() {
		return fmt.Errorf("%w: catalog entry has no prepared runtime identity", errReleaseCommitSemantic)
	}
	finalTarget := filepath.Join(c.challengesDir, commit.RuntimeIdentity.Slug)
	if _, exists, err := existingReleaseMaterialization(finalTarget, expected); err != nil || exists {
		if err != nil {
			return fmt.Errorf("%w: %v", errReleaseCommitSemantic, err)
		}
		return nil
	}
	preparedTarget := filepath.Join(c.preparedChallengesRoot(release.ID), commit.RuntimeIdentity.Slug)
	if _, exists, err := existingReleaseMaterialization(preparedTarget, expected); err != nil {
		return fmt.Errorf("%w: %v", errReleaseCommitSemantic, err)
	} else if !exists {
		return fmt.Errorf("%w: catalog entry %q has no staged materialization", errReleaseCommitSemantic, entry.ID)
	}
	if err := os.MkdirAll(c.challengesDir, 0o755); err != nil {
		return err
	}
	moveErr := os.Rename(preparedTarget, finalTarget)
	if moveErr == nil {
		return nil
	}
	if _, exists, validationErr := existingReleaseMaterialization(finalTarget, expected); exists || validationErr != nil {
		if validationErr != nil {
			return fmt.Errorf("%w: %v", errReleaseCommitSemantic, validationErr)
		}
		return nil
	}
	return fmt.Errorf("move staged catalog entry %q: %w", entry.ID, moveErr)
}

func existingReleaseMaterialization(target string, expected *challenge.Entry) (*challenge.Entry, bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, true, errors.New("catalog materialization target is not a directory")
	}
	existing, err := challenge.ValidateDir(target)
	if err != nil {
		return nil, true, fmt.Errorf("validate existing catalog materialization: %w", err)
	}
	if expected == nil || existing.ID != expected.ID || existing.SourceSlug != expected.SourceSlug ||
		existing.Image != expected.Image || existing.ContentRevision != expected.ContentRevision || existing.Revision != expected.Revision {
		return nil, true, errors.New("catalog materialization target conflicts with persisted identity")
	}
	return existing, true, nil
}

func compilePortableTaxonomy(source taxonomydomain.PortableSnapshot, materialized map[string]*challenge.Entry) (taxonomydomain.Snapshot, error) {
	result := taxonomydomain.Snapshot{
		Skills: append([]taxonomydomain.Skill(nil), source.Skills...), Tags: append([]taxonomydomain.Tag(nil), source.Tags...),
		SkillMappings:     append([]taxonomydomain.SkillMapping(nil), source.SkillMappings...),
		ChallengeMappings: make([]taxonomydomain.ChallengeMapping, 0, len(source.ChallengeMappings)),
	}
	for _, mapping := range source.ChallengeMappings {
		entry, exists := materialized[mapping.Challenge.Path]
		if !exists || entry == nil || entry.Title != mapping.Challenge.Title || entry.ContentRevision != mapping.Challenge.ContentRevision {
			return taxonomydomain.Snapshot{}, fmt.Errorf("portable taxonomy mapping %q has no matching materialized challenge", mapping.Challenge.Path)
		}
		result.ChallengeMappings = append(result.ChallengeMappings, taxonomydomain.ChallengeMapping{
			Challenge: taxonomydomain.ChallengeRef{ID: entry.ID, Title: entry.Title, ContentRevision: entry.ContentRevision},
			Tags:      append([]taxonomydomain.Ref(nil), mapping.Tags...), EntrySkills: append([]taxonomydomain.Ref(nil), mapping.EntrySkills...),
			Outcomes: append([]taxonomydomain.OutcomeRef(nil), mapping.Outcomes...), File: mapping.File,
		})
	}
	if err := taxonomydomain.Validate(result); err != nil {
		return taxonomydomain.Snapshot{}, fmt.Errorf("compile catalog taxonomy: %w", err)
	}
	return result, nil
}

func (c *ReleaseCoordinator) removeReleaseTaxonomyIfCurrent(ctx context.Context, release catalogdomain.Release) error {
	source, err := LoadPortableSource(c.sourceRoot(release.ID))
	if err != nil {
		return fmt.Errorf("load staged catalog source for taxonomy cleanup: %w", err)
	}
	entries, err := c.releases.ListEntries(ctx, release.ID)
	if err != nil {
		return err
	}
	byPath := make(map[string]SourceChallenge, len(source.Challenges))
	for _, value := range source.Challenges {
		byPath[value.Path] = value
	}
	materialized := make(map[string]*challenge.Entry, len(entries))
	for _, entry := range entries {
		commit, err := c.releases.GetEntryCommit(ctx, entry.ID)
		if err != nil {
			return err
		}
		if commit.RuntimeIdentity == nil {
			return nil
		}
		sourceChallenge, exists := byPath[entry.SourcePath]
		if !exists || sourceChallenge.ContentRevision != entry.ContentRevision {
			return fmt.Errorf("catalog entry %q does not match staged source during taxonomy cleanup", entry.ID)
		}
		materialized[entry.SourcePath] = &challenge.Entry{
			ID:              commit.RuntimeIdentity.ChallengeID,
			Title:           sourceChallenge.Entry.Title,
			ContentRevision: string(entry.ContentRevision),
		}
	}
	runtimeTaxonomy, err := compilePortableTaxonomy(source.Taxonomy, materialized)
	if err != nil {
		return err
	}
	revision, err := c.taxonomy.PreviewRevision(runtimeTaxonomy)
	if err != nil {
		return err
	}
	_, err = c.taxonomy.RemoveCurrentRevision(revision)
	return err
}

// removeReleaseResidue removes source and candidate archives only after a
// terminal release state. Failed releases additionally remove any final
// directories that still match their immutable release identity.
func (c *ReleaseCoordinator) removeReleaseResidue(ctx context.Context, release catalogdomain.Release, removeMaterializations bool) error {
	entries, err := c.releases.ListEntries(ctx, release.ID)
	if err != nil {
		return err
	}
	if removeMaterializations {
		for _, entry := range entries {
			commit, err := c.releases.GetEntryCommit(ctx, entry.ID)
			if err != nil {
				return err
			}
			if commit.RuntimeIdentity == nil {
				continue
			}
			target := filepath.Join(c.challengesDir, commit.RuntimeIdentity.Slug)
			info, err := os.Lstat(target)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() {
				return fmt.Errorf("catalog cleanup target %q is not a directory", target)
			}
			existing, err := challenge.ValidateDir(target)
			if err != nil {
				return fmt.Errorf("validate catalog cleanup target %q: %w", target, err)
			}
			if existing.ID != commit.RuntimeIdentity.ChallengeID || existing.SourceSlug != commit.RuntimeIdentity.Slug || existing.ContentRevision != string(entry.ContentRevision) {
				return fmt.Errorf("catalog cleanup target %q conflicts with release identity", target)
			}
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("remove catalog cleanup target %q: %w", target, err)
			}
		}
	}
	for _, entry := range entries {
		candidateID := strings.TrimSpace(entry.CandidateRevisionID)
		if candidateID == "" {
			continue
		}
		if filepath.Base(candidateID) != candidateID {
			return fmt.Errorf("catalog candidate identity %q is invalid", candidateID)
		}
		if err := os.RemoveAll(filepath.Join(c.dataDir, "candidates", candidateID)); err != nil {
			return fmt.Errorf("remove catalog candidate %q: %w", candidateID, err)
		}
	}
	if err := os.RemoveAll(c.releaseStageRoot(release.ID)); err != nil {
		return fmt.Errorf("remove catalog release staging: %w", err)
	}
	return nil
}

func (c *ReleaseCoordinator) releaseStageRoot(releaseID string) string {
	return filepath.Join(c.dataDir, ".staging", "releases", releaseID)
}

func (c *ReleaseCoordinator) sourceRoot(releaseID string) string {
	return filepath.Join(c.releaseStageRoot(releaseID), "source")
}

func (c *ReleaseCoordinator) preparedChallengesRoot(releaseID string) string {
	return filepath.Join(c.releaseStageRoot(releaseID), "prepared", "challenges")
}
