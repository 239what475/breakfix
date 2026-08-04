package generation

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	execution "github.com/breakfix/breakfix/internal/domain/execution"
)

var (
	ErrCandidateNotFound     = errors.New("candidate revision not found")
	ErrCandidateInvalidState = errors.New("candidate revision is not in the required state")
)

// Runtime execution data is shared with Catalog Release installation. These
// aliases preserve Generation's persisted JSON contract while keeping Catalog
// independent from GenerationWorkflow state.
type CheckpointSnapshot = execution.CheckpointSnapshot
type NodeSnapshot = execution.NodeSnapshot
type NodeRuntimeSnapshot = execution.NodeRuntimeSnapshot
type NodeResources = execution.NodeResources
type K8sRuntimeSnapshot = execution.K8sRuntimeSnapshot
type K8sResources = execution.K8sResources
type ExecutionSnapshot = execution.Snapshot
type BuildOutput = execution.BuildOutput
type IncusBuildReference = execution.IncusBuildReference
type ArtifactReference = execution.ArtifactReference
type VerificationEnvironment = execution.VerificationEnvironment
type ExecutionResult = execution.ExecutionResult
type CheckpointResult = execution.CheckpointResult
type VerificationReport = execution.VerificationReport

type Publication struct {
	ChallengeID string             `json:"challenge_id"`
	SourceSlug  string             `json:"source_slug"`
	TargetPath  string             `json:"target_path"`
	RequestedAt time.Time          `json:"requested_at"`
	Artifact    *ArtifactReference `json:"artifact,omitempty"`
}

func (p Publication) ValidateIntent() error {
	if !challenge.ValidID(p.ChallengeID) || !challenge.ValidSourceSlug(p.SourceSlug) || p.TargetPath != p.SourceSlug || p.RequestedAt.IsZero() || p.Artifact != nil {
		return errors.New("challenge publication intent is incomplete")
	}
	return nil
}

type Revision struct {
	ID                 string                   `json:"id"`
	Source             Source                   `json:"source"`
	SourceRevision     string                   `json:"source_revision"`
	GeneratorSessionID string                   `json:"generator_session_id,omitempty"`
	GeneratorRunID     string                   `json:"generator_run_id,omitempty"`
	JudgeRunID         string                   `json:"judge_run_id,omitempty"`
	ArchivePath        string                   `json:"-"`
	ArchiveSHA256      string                   `json:"archive_sha256"`
	Snapshot           ExecutionSnapshot        `json:"snapshot"`
	Build              *BuildOutput             `json:"build,omitempty"`
	Artifact           *ArtifactReference       `json:"artifact,omitempty"`
	VerifyEnvironment  *VerificationEnvironment `json:"verify_environment,omitempty"`
	Verification       *VerificationReport      `json:"verification,omitempty"`
	Failure            *Failure                 `json:"failure,omitempty"`
	Publication        *Publication             `json:"publication,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	VerifiedAt         *time.Time               `json:"verified_at,omitempty"`
	PublishedAt        *time.Time               `json:"published_at,omitempty"`
}

// WorkerView deliberately excludes ArchivePath. Workers access candidate
// bytes only through attempt-fenced Server endpoints and never learn the
// Server volume layout.
type WorkerView struct {
	ID                string                   `json:"id"`
	SourceRevision    string                   `json:"source_revision"`
	ArchiveSHA256     string                   `json:"archive_sha256"`
	Snapshot          ExecutionSnapshot        `json:"snapshot"`
	Build             *BuildOutput             `json:"build,omitempty"`
	Artifact          *ArtifactReference       `json:"artifact,omitempty"`
	VerifyEnvironment *VerificationEnvironment `json:"verify_environment,omitempty"`
	Verification      *VerificationReport      `json:"verification,omitempty"`
	Failure           *Failure                 `json:"failure,omitempty"`
	Publication       *Publication             `json:"publication,omitempty"`
}

func (r Revision) WorkerView() WorkerView {
	var build *BuildOutput
	if r.Build != nil {
		copy := *r.Build
		copy.OCIArchivePath = ""
		build = &copy
	}
	var failure *Failure
	if r.Failure != nil {
		copy := *r.Failure
		failure = &copy
	}
	return WorkerView{
		ID: r.ID, SourceRevision: r.SourceRevision, ArchiveSHA256: r.ArchiveSHA256, Snapshot: r.Snapshot,
		Build: build, Artifact: r.Artifact, VerifyEnvironment: r.VerifyEnvironment,
		Verification: r.Verification, Failure: failure, Publication: r.Publication,
	}
}

// IDForGeneratorRun is stable across a lost HTTP response while remaining
// opaque to users and independent of challenge content.
func IDForGeneratorRun(runID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(runID)))
	return "candidate-" + fmt.Sprintf("%x", sum[:12])
}

func (r Revision) ValidateForCreate() error {
	if strings.TrimSpace(r.ID) == "" || !r.Source.Valid() || strings.TrimSpace(r.SourceRevision) == "" {
		return errors.New("candidate revision requires identity and source revision")
	}
	if r.Source.Kind == SourceAuthoring && (strings.TrimSpace(r.GeneratorSessionID) == "" || strings.TrimSpace(r.GeneratorRunID) == "") {
		return errors.New("authoring candidate revision requires generator lineage")
	}
	if strings.TrimSpace(r.ArchivePath) == "" || !ValidSHA256(r.ArchiveSHA256) {
		return errors.New("candidate revision requires an immutable archive")
	}
	if r.Build != nil || r.Artifact != nil || r.VerifyEnvironment != nil || r.Verification != nil || r.Failure != nil || r.Publication != nil {
		return errors.New("new candidate revision must not contain stage outputs")
	}
	return r.Snapshot.Validate()
}

func ValidSHA256(value string) bool { return execution.ValidSHA256(value) }
