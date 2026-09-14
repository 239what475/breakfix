package runnable

import (
	"errors"
	"fmt"
)

// ActionContext is the complete, lease-fenced input exposed to a Runtime
// Worker. It carries exactly one public action and never carries content
// aggregate, publication, or provider SDK state.
type ActionContext struct {
	Credential             LeaseCredential   `json:"credential"`
	Spec                   *RunnableSpec     `json:"spec,omitempty"`
	RunnableRevision       *RunnableRevision `json:"runnable_revision,omitempty"`
	RunnableRevisionDigest string            `json:"runnable_revision_digest,omitempty"`
}

func (c ActionContext) Validate() error {
	if err := c.Credential.Validate(); err != nil {
		return err
	}
	switch c.Credential.Identity.Phase {
	case ActionMaterializeArtifact:
		if c.Spec == nil || c.RunnableRevision != nil || c.RunnableRevisionDigest != "" {
			return errors.New("runnable materialization action has an invalid input")
		}
		return MaterializeRequest{Credential: c.Credential, Spec: *c.Spec}.Validate()
	case ActionVerify:
		if c.Spec != nil || c.RunnableRevision == nil || !ValidDigest(c.RunnableRevisionDigest) {
			return errors.New("runnable verification action has an invalid input")
		}
		return VerifyRequest{Credential: c.Credential, RunnableRevision: *c.RunnableRevision, RunnableRevisionDigest: c.RunnableRevisionDigest}.Validate()
	default:
		return fmt.Errorf("runnable action has unsupported phase %q", c.Credential.Identity.Phase)
	}
}
