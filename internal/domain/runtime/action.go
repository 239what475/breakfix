// Package runtime defines the closed, durable external actions executed by a
// Runtime Worker. It is not a workflow engine: every action belongs to one
// of the fixed GenerationWorkflow or Catalog aggregates.
package runtime

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/execution"
)

var (
	ErrLeaseLost      = errors.New("runtime action lease was lost")
	ErrActionNotFound = errors.New("runtime action was not found")
)

// MaxAttempts is the bounded infrastructure retry budget for one durable
// Runtime Action state. It intentionally excludes provider-local retries and
// never participates in external resource identity.
const MaxAttempts = 5

// Scope identifies the owning aggregate of a Runtime Action. This is a
// closed protocol boundary, not a user-extensible task kind.
type Scope string

const (
	ScopeGenerationWorkflow Scope = "generation-workflow"
	ScopeCatalogEntry       Scope = "catalog-entry"
	ScopeCatalogCommit      Scope = "catalog-commit"
)

func (s Scope) Valid() bool {
	switch s {
	case ScopeGenerationWorkflow, ScopeCatalogEntry, ScopeCatalogCommit:
		return true
	default:
		return false
	}
}

// State is the external runtime state represented by an action. Catalog
// commit preparation maps to ChallengePublishing; the other values map
// directly to the corresponding persisted aggregate state.
type State string

const (
	StateBuilding            State = "Building"
	StateArtifactPublishing  State = "ArtifactPublishing"
	StateVerifying           State = "Verifying"
	StateChallengePublishing State = "ChallengePublishing"
)

func (s State) Valid() bool {
	switch s {
	case StateBuilding, StateArtifactPublishing, StateVerifying, StateChallengePublishing:
		return true
	default:
		return false
	}
}

// Identity is stable over all infrastructure retries of one aggregate state.
// Attempt is intentionally absent: takeover must create or get the same
// provider resource instead of allocating a second one.
type Identity struct {
	Scope        Scope  `json:"scope"`
	OwnerID      string `json:"owner_id"`
	ParentID     string `json:"parent_id,omitempty"`
	CandidateID  string `json:"candidate_id"`
	State        State  `json:"state"`
	StateVersion int64  `json:"state_version"`
}

func (i Identity) Valid() bool {
	if !i.Scope.Valid() || strings.TrimSpace(i.OwnerID) == "" || strings.TrimSpace(i.CandidateID) == "" || !i.State.Valid() || i.StateVersion < 1 {
		return false
	}
	switch i.Scope {
	case ScopeGenerationWorkflow:
		return strings.TrimSpace(i.ParentID) == ""
	case ScopeCatalogEntry, ScopeCatalogCommit:
		return strings.TrimSpace(i.ParentID) != ""
	default:
		return false
	}
}

func (i Identity) String() string {
	return fmt.Sprintf("%s/%s/%s/%s/%d", i.Scope, i.ParentID, i.OwnerID, i.State, i.StateVersion)
}

// Credential fences every Runtime Worker request against the currently held
// aggregate lease.
type Credential struct {
	Identity   Identity `json:"identity"`
	LeaseOwner string   `json:"lease_owner"`
}

func (c Credential) Valid() bool {
	return c.Identity.Valid() && strings.TrimSpace(c.LeaseOwner) != ""
}

// Context is the complete immutable input Server exposes for one claimed
// action. It intentionally has no authoring plan, agent state, workspace
// binding, database credential, or Server filesystem path.
type Context struct {
	Identity                Identity                           `json:"identity"`
	LeaseOwner              string                             `json:"lease_owner"`
	ArchiveSHA256           string                             `json:"archive_sha256"`
	Snapshot                execution.Snapshot                 `json:"snapshot"`
	Build                   *execution.BuildOutput             `json:"build,omitempty"`
	Artifact                *execution.ArtifactReference       `json:"artifact,omitempty"`
	VerificationEnvironment *execution.VerificationEnvironment `json:"verification_environment,omitempty"`
	ChallengeID             string                             `json:"challenge_id,omitempty"`
	ChallengeRevisionID     string                             `json:"challenge_revision_id,omitempty"`
}

func (c Context) Credential() Credential {
	return Credential{Identity: c.Identity, LeaseOwner: c.LeaseOwner}
}

func (c Context) Valid() error {
	if !c.Credential().Valid() || !execution.ValidSHA256(c.ArchiveSHA256) || c.Snapshot.Validate() != nil {
		return errors.New("runtime action context is incomplete")
	}
	if c.Build != nil && c.Build.Validate(c.Snapshot.Runtime) != nil {
		return errors.New("runtime action build output is invalid")
	}
	if c.Artifact != nil && c.Artifact.Validate(c.Snapshot.Runtime) != nil {
		return errors.New("runtime action artifact is invalid")
	}
	if c.VerificationEnvironment != nil && c.VerificationEnvironment.Validate(c.Snapshot.Runtime) != nil {
		return errors.New("runtime action verification environment is invalid")
	}
	switch c.Identity.State {
	case StateBuilding:
		if c.Build != nil || c.Artifact != nil || c.ChallengeID != "" || c.ChallengeRevisionID != "" {
			return errors.New("build action has unexpected runtime output")
		}
	case StateArtifactPublishing:
		if c.Build == nil || c.Artifact != nil || c.ChallengeID != "" || c.ChallengeRevisionID != "" {
			return errors.New("artifact publication action has invalid build input")
		}
	case StateVerifying:
		if c.Artifact == nil || c.ChallengeID != "" || c.ChallengeRevisionID != "" {
			return errors.New("verification action has invalid staging input")
		}
	case StateChallengePublishing:
		if c.Artifact == nil || !challenge.ValidID(c.ChallengeID) || !challenge.ValidRevisionID(c.ChallengeRevisionID) {
			return errors.New("challenge publication action has invalid input")
		}
	default:
		return errors.New("runtime action state is unsupported")
	}
	return nil
}

// Work creates the provider-neutral execution input. The deadline is local to
// this one worker action and is neither stored on an aggregate nor inherited
// by an infrastructure retry.
func (c Context) Work(deadlineAt time.Time) (execution.Work, error) {
	if err := c.Valid(); err != nil {
		return execution.Work{}, err
	}
	work := execution.Work{
		OwnerID:                 c.Identity.OwnerID,
		CandidateID:             c.Identity.CandidateID,
		ArchiveSHA256:           c.ArchiveSHA256,
		Snapshot:                c.Snapshot,
		Attempt:                 c.Identity.StateVersion,
		DeadlineAt:              deadlineAt.UTC(),
		Build:                   c.Build,
		Artifact:                c.Artifact,
		VerificationEnvironment: c.VerificationEnvironment,
	}
	if err := work.Validate(); err != nil {
		return execution.Work{}, err
	}
	return work, nil
}
