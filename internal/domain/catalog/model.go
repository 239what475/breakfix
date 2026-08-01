// Package catalog owns the portable, administrator-installed catalog release
// contract. It contains no OCI, filesystem, database, or runtime artifact
// implementation details.
package catalog

import (
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
)

var contentRevisionPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ContentRevision identifies a canonical portable candidate file tree. It is
// intentionally distinct from a runtime OCI digest or Incus fingerprint.
type ContentRevision string

func (r ContentRevision) Valid() bool {
	return contentRevisionPattern.MatchString(string(r))
}

func (r ContentRevision) Validate() error {
	if !r.Valid() {
		return errors.New("content revision must be a lowercase sha256 digest")
	}
	return nil
}

// BundleDigest identifies the immutable OCI source bundle selected by an
// administrator. It has the same digest syntax as ContentRevision but a
// different meaning and must not be used as a candidate content identity.
type BundleDigest string

func (d BundleDigest) Valid() bool {
	return contentRevisionPattern.MatchString(string(d))
}

type ReleaseState string

const (
	ReleasePending    ReleaseState = "Pending"
	ReleaseInstalling ReleaseState = "Installing"
	ReleaseCommitting ReleaseState = "Committing"
	ReleaseReady      ReleaseState = "Ready"
	ReleaseCleaningUp ReleaseState = "CleaningUp"
	ReleaseFailed     ReleaseState = "Failed"
)

func (s ReleaseState) Valid() bool {
	switch s {
	case ReleasePending, ReleaseInstalling, ReleaseCommitting, ReleaseReady, ReleaseCleaningUp, ReleaseFailed:
		return true
	default:
		return false
	}
}

func (s ReleaseState) Terminal() bool {
	return s == ReleaseReady || s == ReleaseFailed
}

type EntryState string

const (
	EntryPending       EntryState = "Pending"
	EntryBuilding      EntryState = "Building"
	EntryVerifying     EntryState = "Verifying"
	EntryReadyToCommit EntryState = "ReadyToCommit"
	EntryCleaningUp    EntryState = "CleaningUp"
	EntryCleaned       EntryState = "Cleaned"
	EntryFailed        EntryState = "Failed"
)

func (s EntryState) Valid() bool {
	switch s {
	case EntryPending, EntryBuilding, EntryVerifying, EntryReadyToCommit, EntryCleaningUp, EntryCleaned, EntryFailed:
		return true
	default:
		return false
	}
}

func (s EntryState) Terminal() bool {
	return s == EntryCleaned || s == EntryFailed
}

// CommitState tracks target-platform publication after every entry has passed
// build and verification. It is separate from EntryState because source
// validation and filesystem/database commitment have different recovery
// boundaries.
type CommitState string

const (
	CommitPending      CommitState = "Pending"
	CommitPrepared     CommitState = "Prepared"
	CommitMaterialized CommitState = "Materialized"
	CommitCommitted    CommitState = "Committed"
)

func (s CommitState) Valid() bool {
	switch s {
	case CommitPending, CommitPrepared, CommitMaterialized, CommitCommitted:
		return true
	default:
		return false
	}
}

// RuntimeIdentity is allocated exactly once during release-level committing.
// It belongs to the target platform and is intentionally absent from the
// portable catalog source bundle.
type RuntimeIdentity struct {
	ChallengeID string `json:"challenge_id"`
	Slug        string `json:"slug"`
}

func (i RuntimeIdentity) Valid() bool {
	return strings.TrimSpace(i.ChallengeID) != "" && strings.TrimSpace(i.Slug) != ""
}

// Release is an administrator-owned installation attempt of an immutable
// portable catalog source bundle. BundleDigest is pinned by its caller and
// does not identify any target-platform artifact.
type Release struct {
	ID                      string          `json:"id"`
	Name                    string          `json:"name"`
	Version                 string          `json:"version"`
	BundleDigest            BundleDigest    `json:"bundle_digest"`
	TaxonomyContentRevision ContentRevision `json:"taxonomy_content_revision"`
	State                   ReleaseState    `json:"state"`
	DeadlineAt              *time.Time      `json:"deadline_at,omitempty"`
	LastError               string          `json:"last_error,omitempty"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

func (r Release) Valid() bool {
	return strings.TrimSpace(r.ID) != "" && strings.TrimSpace(r.Name) != "" && strings.TrimSpace(r.Version) != "" &&
		r.BundleDigest.Valid() && r.TaxonomyContentRevision.Valid() && r.State.Valid()
}

// Entry is one portable challenge source in a Release. Runtime challenge ID,
// slug, and artifact references are deliberately absent until a release-level
// commit has created them on the target platform.
type Entry struct {
	ID                  string          `json:"id"`
	ReleaseID           string          `json:"release_id"`
	SourcePath          string          `json:"source_path"`
	ContentRevision     ContentRevision `json:"content_revision"`
	CandidateRevisionID string          `json:"-"`
	State               EntryState      `json:"state"`
	LastError           string          `json:"last_error,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// Commit stores the target-platform side of an entry installation. The
// identity is reserved before filesystem materialization so recovery reuses
// the same challenge ID and slug rather than producing duplicates.
type Commit struct {
	EntryID         string           `json:"entry_id"`
	ContentRevision ContentRevision  `json:"content_revision"`
	State           CommitState      `json:"state"`
	RuntimeIdentity *RuntimeIdentity `json:"runtime_identity,omitempty"`
	MaterializedAt  *time.Time       `json:"materialized_at,omitempty"`
	CommittedAt     *time.Time       `json:"committed_at,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

func (c Commit) Valid() bool {
	if strings.TrimSpace(c.EntryID) == "" || !c.ContentRevision.Valid() || !c.State.Valid() {
		return false
	}
	if c.State == CommitPending {
		return c.RuntimeIdentity == nil && c.MaterializedAt == nil && c.CommittedAt == nil
	}
	if c.RuntimeIdentity == nil || !c.RuntimeIdentity.Valid() {
		return false
	}
	if c.State == CommitPrepared {
		return c.MaterializedAt == nil && c.CommittedAt == nil
	}
	if c.State == CommitMaterialized {
		return c.MaterializedAt != nil && c.CommittedAt == nil
	}
	return c.MaterializedAt != nil && c.CommittedAt != nil
}

func (e Entry) Valid() bool {
	return strings.TrimSpace(e.ID) != "" && strings.TrimSpace(e.ReleaseID) != "" && validSourcePath(e.SourcePath) &&
		e.ContentRevision.Valid() && e.State.Valid()
}

func validSourcePath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || path.IsAbs(value) {
		return false
	}
	clean := path.Clean(value)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && clean == value
}
