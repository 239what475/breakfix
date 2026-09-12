package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	execution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/publication"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

var ErrBaselineEstablished = errors.New("catalog baseline is already established")

type ReleaseState string

const (
	ReleasePending    ReleaseState = "Pending"
	ReleaseInstalling ReleaseState = "Installing"
	ReleaseCommitting ReleaseState = "Committing"
	ReleaseReady      ReleaseState = "Ready"
	ReleaseFailed     ReleaseState = "Failed"
)

func (s ReleaseState) Valid() bool {
	switch s {
	case ReleasePending, ReleaseInstalling, ReleaseCommitting, ReleaseReady, ReleaseFailed:
		return true
	default:
		return false
	}
}

func (s ReleaseState) Terminal() bool { return s == ReleaseReady || s == ReleaseFailed }

type EntryState string

const (
	EntryBuilding           EntryState = "Building"
	EntryArtifactPublishing EntryState = "ArtifactPublishing"
	EntryVerifying          EntryState = "Verifying"
	EntryReadyToCommit      EntryState = "ReadyToCommit"
	EntryFailed             EntryState = "Failed"
)

func (s EntryState) Valid() bool {
	switch s {
	case EntryBuilding, EntryArtifactPublishing, EntryVerifying, EntryReadyToCommit, EntryFailed:
		return true
	default:
		return false
	}
}

func (s EntryState) Terminal() bool { return s == EntryReadyToCommit || s == EntryFailed }

func (s EntryState) Leaseable() bool {
	return s == EntryBuilding || s == EntryArtifactPublishing || s == EntryVerifying
}

type CommitState string

const (
	CommitPending           CommitState = "Pending"
	CommitPrepared          CommitState = "Prepared"
	CommitArtifactPublished CommitState = "ArtifactPublished"
	CommitMaterialized      CommitState = "Materialized"
	CommitCommitted         CommitState = "Committed"
	CommitFailed            CommitState = "Failed"
)

func (s CommitState) Valid() bool {
	switch s {
	case CommitPending, CommitPrepared, CommitArtifactPublished, CommitMaterialized, CommitCommitted, CommitFailed:
		return true
	default:
		return false
	}
}

// Release is a durable installation attempt for one immutable portable OCI
// bundle. It has no user, Generator AgentRun, or authoring-session identity.
type Release struct {
	ID                       string               `json:"id"`
	Name                     string               `json:"name"`
	Version                  string               `json:"version"`
	BundleDigest             BundleDigest         `json:"bundle_digest"`
	SourceDigest             ContentRevision      `json:"source_digest"`
	State                    ReleaseState         `json:"state"`
	SourceAttempt            int                  `json:"source_attempt"`
	NextRunAt                time.Time            `json:"next_run_at"`
	CommitID                 string               `json:"commit_id,omitempty"`
	LastError                string               `json:"last_error,omitempty"`
	FinalizerErrorCategory   publication.Category `json:"finalizer_error_category,omitempty"`
	FinalizerLastError       string               `json:"finalizer_last_error,omitempty"`
	FinalizerLastAttemptedAt *time.Time           `json:"finalizer_last_attempted_at,omitempty"`
	FinalizerNextRetryAt     *time.Time           `json:"finalizer_next_retry_at,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
	UpdatedAt                time.Time            `json:"updated_at"`
}

// BootstrapState is the complete durable view needed to decide whether one
// configured release may establish the platform's initial Catalog baseline.
// Failed releases remain as diagnostics; they do not become additional
// baselines.
type BootstrapState struct {
	Releases                []Release
	PublishedChallengeCount int
	FailedCleanupPending    bool
}

func (r Release) Valid() bool {
	if strings.TrimSpace(r.ID) == "" || !r.BundleDigest.Valid() || !r.State.Valid() || r.SourceAttempt < 0 || r.NextRunAt.IsZero() || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return false
	}
	initialized := strings.TrimSpace(r.Name) != "" || strings.TrimSpace(r.Version) != "" || r.SourceDigest != ""
	if initialized && (strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Version) == "" || !r.SourceDigest.Valid()) {
		return false
	}
	if !r.validFinalizerDiagnostic() {
		return false
	}
	if !initialized && r.State != ReleasePending && r.State != ReleaseFailed {
		return false
	}
	if r.State == ReleaseCommitting || r.State == ReleaseReady {
		return initialized && strings.TrimSpace(r.CommitID) != ""
	}
	if r.State == ReleaseFailed {
		return r.CommitID == "" || strings.TrimSpace(r.CommitID) == CommitIDForRelease(r.ID)
	}
	return r.CommitID == ""
}

func (r Release) validFinalizerDiagnostic() bool {
	if r.FinalizerErrorCategory == publication.CategoryUnknown {
		return r.FinalizerLastError == "" && r.FinalizerLastAttemptedAt == nil && r.FinalizerNextRetryAt == nil
	}
	lastAttempted := time.Time{}
	if r.FinalizerLastAttemptedAt != nil {
		lastAttempted = *r.FinalizerLastAttemptedAt
	}
	return (publication.Diagnostic{
		Category:        r.FinalizerErrorCategory,
		LastError:       r.FinalizerLastError,
		LastAttemptedAt: lastAttempted,
		NextRetryAt:     r.FinalizerNextRetryAt,
	}).Validate() == nil
}

// Entry records every durable output needed to resume a single source path.
// A successful stage remains durable while a later stage is retried.
type Entry struct {
	ID                string                             `json:"id"`
	ReleaseID         string                             `json:"release_id"`
	SourcePath        string                             `json:"source_path"`
	SourceRef         string                             `json:"source_ref"`
	Title             string                             `json:"title"`
	Type              challenge.ScenarioType             `json:"type"`
	Tags              []string                           `json:"tags"`
	ContentRevision   ContentRevision                    `json:"content_revision"`
	ArchiveSHA256     string                             `json:"archive_sha256"`
	Snapshot          execution.Snapshot                 `json:"snapshot"`
	State             EntryState                         `json:"state"`
	StateVersion      int64                              `json:"state_version"`
	RuntimeAttempt    int                                `json:"runtime_attempt"`
	LeaseOwner        string                             `json:"-"`
	LeaseExpires      *time.Time                         `json:"lease_expires_at,omitempty"`
	NextRunAt         time.Time                          `json:"next_run_at"`
	Build             *execution.BuildOutput             `json:"build,omitempty"`
	Artifact          *execution.ArtifactReference       `json:"artifact,omitempty"`
	VerifyEnvironment *execution.VerificationEnvironment `json:"verify_environment,omitempty"`
	Verification      *execution.VerificationReport      `json:"verification,omitempty"`
	LastError         string                             `json:"last_error,omitempty"`
	CreatedAt         time.Time                          `json:"created_at"`
	UpdatedAt         time.Time                          `json:"updated_at"`
}

func (e Entry) Valid() bool {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.ReleaseID) == "" || !validSourcePath(e.SourcePath) || strings.TrimSpace(e.SourceRef) == "" || strings.TrimSpace(e.Title) == "" || !e.Type.Valid() ||
		!e.ContentRevision.Valid() || !execution.ValidSHA256(e.ArchiveSHA256) || !e.State.Valid() || e.StateVersion < 1 || e.RuntimeAttempt < 0 || e.NextRunAt.IsZero() || e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
		return false
	}
	if (e.LeaseOwner == "") != (e.LeaseExpires == nil) {
		return false
	}
	if err := e.Snapshot.Validate(); err != nil {
		return false
	}
	canonicalTags, err := challenge.NormalizeTags(e.Tags)
	if err != nil || !slices.Equal(canonicalTags, e.Tags) || (e.Type == challenge.ScenarioDocumentationExample && len(e.Tags) != 0) {
		return false
	}
	if e.Build != nil && !validBuild(*e.Build, e.Snapshot.Runtime) {
		return false
	}
	if e.Artifact != nil && e.Artifact.Validate(e.Snapshot.Runtime) != nil {
		return false
	}
	if e.VerifyEnvironment != nil && e.VerifyEnvironment.Validate(e.Snapshot.Runtime) != nil {
		return false
	}
	if e.Verification != nil && e.Verification.Validate(e.Snapshot) != nil {
		return false
	}
	if e.State.Leaseable() {
		if e.RuntimeAttempt < 1 || e.RuntimeAttempt > runtime.MaxAttempts {
			return false
		}
	} else if e.RuntimeAttempt != 0 {
		return false
	}
	if e.State == EntryReadyToCommit {
		return e.Build != nil && e.Artifact != nil && e.Verification != nil && e.Verification.Passed
	}
	return true
}

// Commit reserves the final opaque Challenge identity before external final
// artifact publication and filesystem materialization. The identity is never
// regenerated after a restart.
type Commit struct {
	ID                  string                       `json:"id"`
	ReleaseID           string                       `json:"release_id"`
	EntryID             string                       `json:"entry_id"`
	ChallengeID         string                       `json:"challenge_id,omitempty"`
	ChallengeRevisionID string                       `json:"challenge_revision_id,omitempty"`
	SourceSlug          string                       `json:"source_slug,omitempty"`
	State               CommitState                  `json:"state"`
	StateVersion        int64                        `json:"state_version"`
	RuntimeAttempt      int                          `json:"runtime_attempt"`
	LeaseOwner          string                       `json:"-"`
	LeaseExpires        *time.Time                   `json:"lease_expires_at,omitempty"`
	NextRunAt           time.Time                    `json:"next_run_at"`
	LastError           string                       `json:"last_error,omitempty"`
	Artifact            *execution.ArtifactReference `json:"artifact,omitempty"`
	MaterializedAt      *time.Time                   `json:"materialized_at,omitempty"`
	CommittedAt         *time.Time                   `json:"committed_at,omitempty"`
	CreatedAt           time.Time                    `json:"created_at"`
	UpdatedAt           time.Time                    `json:"updated_at"`
}

func (c Commit) Valid() bool {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.ReleaseID) == "" || strings.TrimSpace(c.EntryID) == "" || !c.State.Valid() || c.StateVersion < 1 || c.RuntimeAttempt < 0 || c.NextRunAt.IsZero() || c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		return false
	}
	if (c.LeaseOwner == "") != (c.LeaseExpires == nil) {
		return false
	}
	if c.State == CommitPending {
		return c.ChallengeID == "" && c.ChallengeRevisionID == "" && c.SourceSlug == "" && c.RuntimeAttempt == 0 && c.Artifact == nil && c.MaterializedAt == nil && c.CommittedAt == nil
	}
	if !challenge.ValidID(c.ChallengeID) || !challenge.ValidRevisionID(c.ChallengeRevisionID) || !challenge.ValidSourceSlug(c.SourceSlug) {
		return false
	}
	if c.State == CommitPrepared {
		return c.RuntimeAttempt >= 1 && c.RuntimeAttempt <= runtime.MaxAttempts && c.Artifact == nil && c.MaterializedAt == nil && c.CommittedAt == nil
	}
	if c.RuntimeAttempt != 0 {
		return false
	}
	if c.State == CommitFailed {
		return c.Artifact == nil || c.Artifact.Validate(c.Artifact.Runtime) == nil
	}
	if c.Artifact == nil || c.Artifact.Validate(c.Artifact.Runtime) != nil {
		return false
	}
	if c.State == CommitArtifactPublished {
		return c.MaterializedAt == nil && c.CommittedAt == nil
	}
	if c.MaterializedAt == nil {
		return false
	}
	if c.State == CommitMaterialized {
		return c.CommittedAt == nil
	}
	return c.CommittedAt != nil
}

type EntryClaim struct {
	Release Release `json:"release"`
	Entry   Entry   `json:"entry"`
}

func (c EntryClaim) Valid() bool {
	return c.Release.Valid() && c.Entry.Valid() && c.Entry.ReleaseID == c.Release.ID && c.Entry.State.Leaseable() && c.Entry.LeaseOwner != ""
}

type CommitClaim struct {
	Release Release `json:"release"`
	Entry   Entry   `json:"entry"`
	Commit  Commit  `json:"commit"`
}

func (c CommitClaim) Valid() bool {
	return c.Release.Valid() && c.Entry.Valid() && c.Commit.Valid() && c.Entry.ReleaseID == c.Release.ID && c.Commit.ReleaseID == c.Release.ID && c.Commit.EntryID == c.Entry.ID && c.Commit.State == CommitPrepared && c.Commit.LeaseOwner != ""
}

func ReleaseIDForBundle(digest BundleDigest) string {
	sum := sha256.Sum256([]byte(digest))
	return "catalog-release-" + hex.EncodeToString(sum[:16])
}

func EntryIDFor(releaseID, sourcePath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(releaseID) + "\x00" + strings.TrimSpace(sourcePath)))
	return "catalog-entry-" + hex.EncodeToString(sum[:16])
}

func CommitIDForRelease(releaseID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(releaseID)))
	return "catalog-commit-" + hex.EncodeToString(sum[:16])
}

func EntryCommitIDFor(releaseID, entryID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(releaseID) + "\x00" + strings.TrimSpace(entryID)))
	return "catalog-entry-commit-" + hex.EncodeToString(sum[:16])
}

func validSourcePath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || path.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := path.Clean(value)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && clean == value
}

func validBuild(output execution.BuildOutput, runtime string) bool {
	return output.Validate(runtime) == nil
}

var ErrReleaseNotFound = errors.New("catalog release not found")
