// Package scenario contains the durable identity lifecycle of a published
// scenario. Portable scenario files live in content/scenario; this package
// owns the facts that only the platform can assign.
package scenario

import (
	"errors"
	"slices"
	"strings"
	"time"

	content "github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

type SourceKind string

const (
	SourceAuthoring SourceKind = "authoring"
	SourceRelease   SourceKind = "release"
)

func (k SourceKind) Valid() bool { return k == SourceAuthoring || k == SourceRelease }

type State string

const (
	StateActive     State = "active"
	StateDeprecated State = "deprecated"
)

func (s State) Valid() bool { return s == StateActive || s == StateDeprecated }

type RevisionState string

const (
	RevisionActive     RevisionState = "active"
	RevisionSuperseded RevisionState = "superseded"
)

func (s RevisionState) Valid() bool { return s == RevisionActive || s == RevisionSuperseded }

// Scenario is the stable identity. Its active revision is a pointer, not the
// content itself; changing the pointer never changes an existing revision.
type Scenario struct {
	ID               string
	SourceKind       SourceKind
	SourceRef        string
	OwnerUserID      string
	State            State
	ActiveRevisionID string
	SourceSlug       string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (c Scenario) Valid() bool {
	if !content.ValidID(c.ID) || !c.SourceKind.Valid() || strings.TrimSpace(c.SourceRef) == "" ||
		!c.State.Valid() || !content.ValidRevisionID(c.ActiveRevisionID) || !content.ValidSourceSlug(c.SourceSlug) ||
		c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		return false
	}
	if c.SourceKind == SourceAuthoring && strings.TrimSpace(c.OwnerUserID) == "" {
		return false
	}
	return true
}

// Revision is the immutable published result of one complete generation and
// verification lifecycle. Provider artifacts remain embedded exclusively in
// the referenced public RunnableRevision; Operations only retains immutable
// public references alongside its materialized content facts.
type Revision struct {
	ID                    string
	ScenarioID            string
	SourceKind            SourceKind
	SourceRef             string
	SourceRevisionID      string
	BaseActiveRevisionID  string
	Title                 string
	Type                  content.ScenarioType
	Tags                  []string
	ContentRevision       string
	SourceSlug            string
	MaterializedPath      string
	MaterializedRevision  string
	RunnableRevisionRef   runnable.RevisionReference
	VerificationReportRef runnable.VerificationReportReference
	State                 RevisionState
	PublishedAt           time.Time
	CreatedAt             time.Time
}

// ActiveRevision pairs a stable Scenario identity with the immutable
// revision currently exposed by the Catalog. Historical callers continue to
// resolve a specific revision ID rather than following this active pointer.
type ActiveRevision struct {
	Scenario Scenario
	Revision Revision
}

func (r ActiveRevision) Valid() bool {
	return r.Scenario.Valid() && r.Scenario.State == StateActive &&
		r.Revision.Valid() && r.Revision.State == RevisionActive &&
		r.Scenario.ID == r.Revision.ScenarioID &&
		r.Scenario.ActiveRevisionID == r.Revision.ID &&
		r.Scenario.SourceKind == r.Revision.SourceKind &&
		r.Scenario.SourceRef == r.Revision.SourceRef &&
		r.Scenario.SourceSlug == r.Revision.SourceSlug
}

func (r Revision) Valid() bool {
	if !content.ValidRevisionID(r.ID) || !content.ValidID(r.ScenarioID) || !r.SourceKind.Valid() ||
		strings.TrimSpace(r.SourceRef) == "" || strings.TrimSpace(r.SourceRevisionID) == "" ||
		strings.TrimSpace(r.Title) == "" || !r.Type.Valid() || !content.ValidRevision(r.ContentRevision) ||
		!content.ValidSourceSlug(r.SourceSlug) || strings.TrimSpace(r.MaterializedPath) == "" ||
		!content.ValidRevision(r.MaterializedRevision) || !r.State.Valid() || r.PublishedAt.IsZero() || r.CreatedAt.IsZero() ||
		r.RunnableRevisionRef.Validate() != nil || r.VerificationReportRef.Validate() != nil {
		return false
	}
	if r.BaseActiveRevisionID != "" && !content.ValidRevisionID(r.BaseActiveRevisionID) {
		return false
	}
	canonicalTags, err := content.NormalizeTags(r.Tags)
	if err != nil || !slices.Equal(canonicalTags, r.Tags) {
		return false
	}
	return true
}

func NewRevisionID() string {
	return content.NewRevisionID()
}

var (
	ErrNotFound         = errors.New("scenario not found")
	ErrRevisionConflict = errors.New("scenario active revision changed")
	ErrNotMutable       = errors.New("scenario cannot be modified")
)
