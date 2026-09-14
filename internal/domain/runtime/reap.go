package runtime

import (
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/execution"
)

// ReapKind identifies a provider resource that is safe to remove after its
// aggregate no longer needs it. Reaping never changes business state.
type ReapKind string

const (
	ReapVerificationEnvironment ReapKind = "verification-environment"
	ReapNodeBuildImage          ReapKind = "node-build-image"
	ReapCandidateArtifact       ReapKind = "candidate-artifact"
)

func (k ReapKind) Valid() bool {
	switch k {
	case ReapVerificationEnvironment, ReapNodeBuildImage, ReapCandidateArtifact:
		return true
	default:
		return false
	}
}

// Reap is the fixed provider input for one cleanup action. ResourceID is the
// CandidateRevision or Catalog Entry that owns the staging resources; final
// artifacts are supplied separately because they can be scoped by a commit.
type Reap struct {
	Scope                       Scope                              `json:"scope"`
	ResourceID                  string                             `json:"resource_id"`
	Kind                        ReapKind                           `json:"kind"`
	DeleteFinalArtifact         bool                               `json:"delete_final_artifact,omitempty"`
	Snapshot                    execution.Snapshot                 `json:"snapshot"`
	Build                       *execution.BuildOutput             `json:"build,omitempty"`
	Artifact                    *execution.ArtifactReference       `json:"artifact,omitempty"`
	FinalArtifact               *execution.ArtifactReference       `json:"final_artifact,omitempty"`
	VerificationEnvironment     *execution.VerificationEnvironment `json:"verification_environment,omitempty"`
	FinalArtifactTargetID       string                             `json:"final_artifact_target_id,omitempty"`
	FinalArtifactTargetRevision string                             `json:"final_artifact_target_revision,omitempty"`
}

func (r Reap) Valid() error {
	if !r.Scope.Valid() || strings.TrimSpace(r.ResourceID) == "" || !r.Kind.Valid() || r.Snapshot.Validate() != nil {
		return errors.New("runtime resource reap is incomplete")
	}
	if r.Build != nil && r.Build.Validate(r.Snapshot.Runtime) != nil {
		return errors.New("runtime resource reap build output is invalid")
	}
	if r.Artifact != nil && r.Artifact.Validate(r.Snapshot.Runtime) != nil {
		return errors.New("runtime resource reap artifact is invalid")
	}
	if r.FinalArtifact != nil && r.FinalArtifact.Validate(r.Snapshot.Runtime) != nil {
		return errors.New("runtime resource reap final artifact is invalid")
	}
	if r.VerificationEnvironment != nil && r.VerificationEnvironment.Validate(r.Snapshot.Runtime) != nil {
		return errors.New("runtime resource reap verification environment is invalid")
	}
	if r.DeleteFinalArtifact && (r.FinalArtifact == nil || strings.TrimSpace(r.FinalArtifactTargetID) == "" || strings.TrimSpace(r.FinalArtifactTargetRevision) == "") {
		return errors.New("runtime resource reap final artifact is incomplete")
	}
	return nil
}

type ReapCredential struct {
	Attempt    int    `json:"attempt"`
	LeaseOwner string `json:"lease_owner"`
}

func (c ReapCredential) Valid() bool {
	return c.Attempt >= 0 && strings.TrimSpace(c.LeaseOwner) != ""
}

type ReapClaim struct {
	Reap
	ReapCredential
}

func (c ReapClaim) Valid() error {
	if err := c.Reap.Valid(); err != nil {
		return err
	}
	if !c.ReapCredential.Valid() {
		return errors.New("runtime resource reap credentials are invalid")
	}
	return nil
}
