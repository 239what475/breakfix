package runnable

import (
	"errors"
	"fmt"
	"strings"
)

// ActionPhase is the closed set of provider-side work exposed by the public
// runnable contract. Product publication is intentionally absent.
type ActionPhase string

var (
	ErrActionLeaseLost = errors.New("runnable action lease lost")
	ErrSourceNotFound  = errors.New("runnable source archive not found")
)

const (
	ActionMaterializeArtifact ActionPhase = "materialize-artifact"
	ActionVerify              ActionPhase = "verify"
)

func (p ActionPhase) Valid() bool {
	return p == ActionMaterializeArtifact || p == ActionVerify
}

// ActionIdentity is stable across infrastructure retries. It identifies a
// content revision, exact spec, action phase, and owning state version without
// exposing a product aggregate or provider resource name.
type ActionIdentity struct {
	Content      ContentIdentity `json:"content"`
	SpecDigest   string          `json:"spec_digest"`
	Phase        ActionPhase     `json:"phase"`
	StateVersion int64           `json:"state_version"`
}

func (i ActionIdentity) Validate() error {
	if err := i.Content.Validate(); err != nil {
		return fmt.Errorf("runnable action content: %w", err)
	}
	if !ValidDigest(i.SpecDigest) {
		return invalid("action.spec_digest", "must be a sha256 digest")
	}
	if !i.Phase.Valid() || i.StateVersion < 1 {
		return invalid("action", "has an invalid phase or state version")
	}
	return nil
}

// Key is suitable for idempotent provider resource lookup. Attempt and lease
// owner are deliberately absent, so a takeover uses the same resource.
func (i ActionIdentity) Key() string {
	return strings.Join([]string{
		i.Content.Kind, i.Content.ID, i.Content.Revision, i.SpecDigest, string(i.Phase), fmt.Sprintf("%d", i.StateVersion),
	}, "/")
}

type LeaseCredential struct {
	Identity   ActionIdentity `json:"identity"`
	LeaseOwner string         `json:"lease_owner"`
}

func (c LeaseCredential) Validate() error {
	if err := c.Identity.Validate(); err != nil {
		return err
	}
	return requiredString(c.LeaseOwner, "action.lease_owner", MaxIDLength)
}

type MaterializeRequest struct {
	Credential LeaseCredential `json:"credential"`
	Spec       RunnableSpec    `json:"spec"`
}

func (r MaterializeRequest) Validate() error {
	if err := r.Credential.Validate(); err != nil {
		return err
	}
	if r.Credential.Identity.Phase != ActionMaterializeArtifact {
		return errors.New("runnable materialize request has the wrong action phase")
	}
	if err := r.Spec.Validate(); err != nil {
		return err
	}
	digest, err := r.Spec.Digest()
	if err != nil {
		return err
	}
	if r.Credential.Identity.SpecDigest != digest || r.Credential.Identity.Content != r.Spec.Identity {
		return errors.New("runnable materialize request identity does not match spec")
	}
	return nil
}

type VerifyRequest struct {
	Credential             LeaseCredential   `json:"credential"`
	RunnableRevision       RunnableRevision  `json:"runnable_revision"`
	RunnableRevisionRef    RevisionReference `json:"runnable_revision_ref"`
	RunnableRevisionDigest string            `json:"runnable_revision_digest"`
	Attempt                int64             `json:"attempt"`
}

func (r VerifyRequest) Validate() error {
	if err := r.Credential.Validate(); err != nil {
		return err
	}
	if r.Credential.Identity.Phase != ActionVerify {
		return errors.New("runnable verify request has the wrong action phase")
	}
	if err := r.RunnableRevision.Validate(); err != nil {
		return err
	}
	specDigest, err := r.RunnableRevision.Spec.Digest()
	if err != nil {
		return err
	}
	revisionDigest, err := r.RunnableRevision.Digest()
	if err != nil {
		return err
	}
	if err := r.RunnableRevisionRef.Validate(); err != nil || r.Credential.Identity.SpecDigest != specDigest || r.Credential.Identity.Content != r.RunnableRevision.Spec.Identity || r.RunnableRevisionDigest != revisionDigest || r.RunnableRevisionRef.Digest != revisionDigest || r.Attempt < 1 {
		return errors.New("runnable verify request identity does not match revision")
	}
	return nil
}
