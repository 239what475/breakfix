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
	ID              string          `json:"id"`
	ReleaseID       string          `json:"release_id"`
	SourcePath      string          `json:"source_path"`
	ContentRevision ContentRevision `json:"content_revision"`
	State           EntryState      `json:"state"`
	LastError       string          `json:"last_error,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
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
