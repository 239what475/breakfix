package generation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
)

// RuntimeActionIdentity is the stable external identity of one durable action.
// RuntimeAttempt is deliberately absent: a lease takeover must create or get
// the same provider resource rather than allocating another one.
type RuntimeActionIdentity struct {
	WorkflowID          string        `json:"workflow_id"`
	CandidateRevisionID string        `json:"candidate_revision_id"`
	State               WorkflowState `json:"state"`
	StateVersion        int64         `json:"state_version"`
}

func RuntimeActionIdentityFor(claim Claim, candidateRevisionID string) RuntimeActionIdentity {
	return RuntimeActionIdentity{
		WorkflowID:          claim.Workflow.ID,
		CandidateRevisionID: strings.TrimSpace(candidateRevisionID),
		State:               claim.Workflow.State,
		StateVersion:        claim.Workflow.StateVersion,
	}
}

func (i RuntimeActionIdentity) Valid() bool {
	return strings.TrimSpace(i.WorkflowID) != "" && strings.TrimSpace(i.CandidateRevisionID) != "" &&
		i.State.RuntimeState() && i.StateVersion >= 1
}

func (i RuntimeActionIdentity) String() string {
	return fmt.Sprintf("%s/%s/%s/%d", i.WorkflowID, i.CandidateRevisionID, i.State, i.StateVersion)
}

// RuntimeActionCredential is the complete lease fence required for one
// Runtime Worker request. The Server verifies it against the currently held
// claim before exposing input or accepting a result.
type RuntimeActionCredential struct {
	LeaseCredential
	Identity RuntimeActionIdentity `json:"identity"`
}

func (c RuntimeActionCredential) Valid() bool {
	return c.LeaseCredential.Valid() && c.Identity.Valid() && c.Identity.StateVersion == c.StateVersion
}

// RuntimeActionContext is the complete immutable input that Server exposes to
// a Runtime Worker for one claimed state. It deliberately excludes Plan,
// authoring feedback, workspace details and every Server filesystem path.
// Candidate archive bytes remain available only through the action-fenced
// archive endpoint when Building needs them.
type RuntimeActionContext struct {
	Claim     Claim                 `json:"claim"`
	Identity  RuntimeActionIdentity `json:"identity"`
	Candidate WorkerView            `json:"candidate"`
}

func (c RuntimeActionContext) Credential() RuntimeActionCredential {
	return RuntimeActionCredential{LeaseCredential: c.Claim.LeaseCredential, Identity: c.Identity}
}

// PublicationFinalization is the Server-owned durable handoff after a Runtime
// Worker has promoted the final artifact. Its archive path is intentionally
// private to Server and never crosses the Runtime Worker API.
type PublicationFinalization struct {
	Workflow  Workflow
	Candidate Revision
}

func (c RuntimeActionContext) Valid() error {
	if !c.Claim.Valid() || !c.Identity.Valid() || c.Identity != RuntimeActionIdentityFor(c.Claim, c.Candidate.ID) {
		return errors.New("runtime action identity is invalid")
	}
	if c.Candidate.ID != c.Claim.Workflow.CandidateRevisionID || c.Candidate.Snapshot.Validate() != nil ||
		!ValidSHA256(c.Candidate.ArchiveSHA256) {
		return errors.New("runtime action candidate is invalid")
	}
	if c.Candidate.Snapshot.Runtime == challenge.RuntimeK8s && c.Identity.State == StateBuilding && c.Candidate.Snapshot.K8s == nil {
		return errors.New("runtime action K8s base artifact is invalid")
	}
	switch c.Identity.State {
	case StateBuilding:
		if c.Candidate.Build != nil || c.Candidate.Artifact != nil {
			return errors.New("build action has unexpected prior runtime output")
		}
	case StateArtifactPublishing:
		if c.Candidate.Build == nil || c.Candidate.Artifact != nil {
			return errors.New("artifact publication action has invalid build input")
		}
	case StateVerifying:
		if c.Candidate.Artifact == nil {
			return errors.New("verification action has no staging artifact")
		}
	case StateChallengePublishing:
		if c.Candidate.Artifact == nil || c.Candidate.Publication == nil || c.Candidate.Publication.Artifact != nil {
			return errors.New("challenge publication action has invalid publication input")
		}
	default:
		return errors.New("runtime action has unsupported state")
	}
	return nil
}

// Work creates the neutral runtime input consumed by build, publication and
// verification providers. DeadlineAt is deliberately local to this Worker
// action and never stored on the workflow or inherited by a retry.
func (c RuntimeActionContext) Work(deadlineAt time.Time) (domainexecution.Work, error) {
	if err := c.Valid(); err != nil {
		return domainexecution.Work{}, err
	}
	work := domainexecution.Work{
		OwnerID:                 c.Identity.WorkflowID,
		CandidateID:             c.Candidate.ID,
		ArchiveSHA256:           c.Candidate.ArchiveSHA256,
		Snapshot:                c.Candidate.Snapshot,
		Attempt:                 c.Identity.StateVersion,
		DeadlineAt:              deadlineAt.UTC(),
		Build:                   c.Candidate.Build,
		Artifact:                c.Candidate.Artifact,
		VerificationEnvironment: c.Candidate.VerifyEnvironment,
	}
	if err := work.Validate(); err != nil {
		return domainexecution.Work{}, err
	}
	return work, nil
}
