package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/publication"
	"github.com/breakfix/breakfix/internal/domain/runnable"
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
	EntryMaterializing EntryState = "MaterializingArtifact"
	EntryVerifying     EntryState = "Verifying"
	EntryReadyToCommit EntryState = "ReadyToCommit"
	EntryFailed        EntryState = "Failed"
)

func (s EntryState) Valid() bool {
	switch s {
	case EntryMaterializing, EntryVerifying, EntryReadyToCommit, EntryFailed:
		return true
	default:
		return false
	}
}

func (s EntryState) Terminal() bool { return s == EntryReadyToCommit || s == EntryFailed }

type CommitState string

const (
	CommitPrepared     CommitState = "Prepared"
	CommitMaterialized CommitState = "Materialized"
	CommitCommitted    CommitState = "Committed"
	CommitFailed       CommitState = "Failed"
)

func (s CommitState) Valid() bool {
	switch s {
	case CommitPrepared, CommitMaterialized, CommitCommitted, CommitFailed:
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

type BootstrapState struct {
	Releases               []Release
	PublishedScenarioCount int
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
	return (publication.Diagnostic{Category: r.FinalizerErrorCategory, LastError: r.FinalizerLastError, LastAttemptedAt: lastAttempted, NextRetryAt: r.FinalizerNextRetryAt}).Validate() == nil
}

// Entry is a content-layer projection of public runnable progress. Leasing,
// retry budgets, provider artifacts, and cleanup records belong only to the
// runnable store and are never copied here.
type Entry struct {
	ID                    string                                `json:"id"`
	ReleaseID             string                                `json:"release_id"`
	SourcePath            string                                `json:"source_path"`
	SourceRef             string                                `json:"source_ref"`
	Title                 string                                `json:"title"`
	Type                  scenario.ScenarioType                 `json:"type"`
	Tags                  []string                              `json:"tags"`
	ContentRevision       ContentRevision                       `json:"content_revision"`
	Source                runnable.SourceArchive                `json:"source"`
	State                 EntryState                            `json:"state"`
	StateVersion          int64                                 `json:"state_version"`
	RunnableRevisionRef   *runnable.RevisionReference           `json:"runnable_revision_ref,omitempty"`
	VerificationReportRef *runnable.VerificationReportReference `json:"verification_report_ref,omitempty"`
	LastError             string                                `json:"last_error,omitempty"`
	CreatedAt             time.Time                             `json:"created_at"`
	UpdatedAt             time.Time                             `json:"updated_at"`
}

func (e Entry) Valid() bool {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.ReleaseID) == "" || !validSourcePath(e.SourcePath) || strings.TrimSpace(e.SourceRef) == "" || strings.TrimSpace(e.Title) == "" || !e.Type.Valid() || !e.ContentRevision.Valid() || e.Source.Validate() != nil || !e.State.Valid() || e.StateVersion < 1 || e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
		return false
	}
	canonicalTags, err := scenario.NormalizeTags(e.Tags)
	if err != nil || !slices.Equal(canonicalTags, e.Tags) {
		return false
	}
	if e.RunnableRevisionRef != nil && e.RunnableRevisionRef.Validate() != nil {
		return false
	}
	if e.VerificationReportRef != nil && e.VerificationReportRef.Validate() != nil {
		return false
	}
	switch e.State {
	case EntryMaterializing:
		return e.RunnableRevisionRef == nil && e.VerificationReportRef == nil
	case EntryVerifying:
		return e.RunnableRevisionRef != nil && e.VerificationReportRef == nil
	case EntryReadyToCommit:
		return e.RunnableRevisionRef != nil && e.VerificationReportRef != nil
	case EntryFailed:
		return strings.TrimSpace(e.LastError) != ""
	default:
		return false
	}
}

// Commit reserves content identities only. Materialization consumes the
// immutable RunnableRevision already bound by its Entry; it never requests a
// second provider artifact or promotion action.
type Commit struct {
	ID                   string      `json:"id"`
	ReleaseID            string      `json:"release_id"`
	EntryID              string      `json:"entry_id"`
	ScenarioID           string      `json:"scenario_id"`
	ScenarioRevisionID   string      `json:"scenario_revision_id"`
	SourceSlug           string      `json:"source_slug"`
	State                CommitState `json:"state"`
	MaterializedRevision string      `json:"materialized_revision,omitempty"`
	MaterializedAt       *time.Time  `json:"materialized_at,omitempty"`
	CommittedAt          *time.Time  `json:"committed_at,omitempty"`
	LastError            string      `json:"last_error,omitempty"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
}

func (c Commit) Valid() bool {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.ReleaseID) == "" || strings.TrimSpace(c.EntryID) == "" || !scenario.ValidID(c.ScenarioID) || !scenario.ValidRevisionID(c.ScenarioRevisionID) || !scenario.ValidSourceSlug(c.SourceSlug) || !c.State.Valid() || c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		return false
	}
	switch c.State {
	case CommitPrepared:
		return c.MaterializedRevision == "" && c.MaterializedAt == nil && c.CommittedAt == nil
	case CommitMaterialized:
		return scenario.ValidRevision(c.MaterializedRevision) && c.MaterializedAt != nil && c.CommittedAt == nil
	case CommitCommitted:
		return scenario.ValidRevision(c.MaterializedRevision) && c.MaterializedAt != nil && c.CommittedAt != nil
	case CommitFailed:
		return strings.TrimSpace(c.LastError) != ""
	default:
		return false
	}
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

var ErrReleaseNotFound = errors.New("catalog release not found")
