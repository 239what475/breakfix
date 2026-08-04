package generation

import (
	"errors"
	"strings"
)

// ResourceReapKind identifies infrastructure that is safe to remove without
// changing a GenerationWorkflow business state. Each kind has one owner.
type ResourceReapKind string

const (
	ResourceReapVerificationEnvironment ResourceReapKind = "verification-environment"
	ResourceReapBuildArchive            ResourceReapKind = "build-archive"
	ResourceReapNodeBuildImage          ResourceReapKind = "node-build-image"
	ResourceReapCandidateArtifact       ResourceReapKind = "candidate-artifact"
)

func (k ResourceReapKind) Valid() bool {
	switch k {
	case ResourceReapVerificationEnvironment, ResourceReapBuildArchive, ResourceReapNodeBuildImage, ResourceReapCandidateArtifact:
		return true
	default:
		return false
	}
}

func (k ResourceReapKind) Owner() string {
	switch k {
	case ResourceReapVerificationEnvironment, ResourceReapBuildArchive:
		return "server"
	case ResourceReapNodeBuildImage, ResourceReapCandidateArtifact:
		return "generate-worker"
	default:
		return ""
	}
}

// ResourceReap carries the immutable candidate resource identities needed by
// one owner. Candidate source archives are deliberately absent: they are
// durable audit inputs and never reaped.
type ResourceReap struct {
	CandidateRevisionID string           `json:"candidate_revision_id"`
	Kind                ResourceReapKind `json:"kind"`
	DeleteFinalArtifact bool             `json:"delete_final_artifact,omitempty"`
	Candidate           WorkerView       `json:"candidate"`
}

func (r ResourceReap) Valid() bool {
	return strings.TrimSpace(r.CandidateRevisionID) != "" && r.Kind.Valid() && r.Candidate.ID == r.CandidateRevisionID
}

// ResourceReapCredential fences a reaper report after another Server or
// Generate Worker takes over an expired infrastructure lease.
type ResourceReapCredential struct {
	Attempt    int    `json:"attempt"`
	LeaseOwner string `json:"lease_owner"`
}

func (c ResourceReapCredential) Valid() bool {
	return c.Attempt >= 0 && strings.TrimSpace(c.LeaseOwner) != ""
}

type ResourceReapClaim struct {
	ResourceReap
	ResourceReapCredential
}

func (c ResourceReapClaim) Valid() error {
	if !c.ResourceReap.Valid() || !c.ResourceReapCredential.Valid() {
		return errors.New("generation resource reap claim is invalid")
	}
	return nil
}
