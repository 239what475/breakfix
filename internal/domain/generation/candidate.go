package generation

import (
	"errors"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
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
type ReproductionEvidenceSnapshot = execution.ReproductionEvidenceSnapshot
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
type ReproductionEvidenceResult = execution.ReproductionEvidenceResult
type VerificationReport = execution.VerificationReport

type Publication struct {
	CandidateRevisionID  string             `json:"candidate_revision_id"`
	IntentRevision       int                `json:"intent_revision"`
	ScenarioID           string             `json:"scenario_id,omitempty"`
	ScenarioRevisionID   string             `json:"scenario_revision_id,omitempty"`
	BaseActiveRevisionID string             `json:"base_active_revision_id,omitempty"`
	SourceSlug           string             `json:"source_slug,omitempty"`
	TargetPath           string             `json:"target_path,omitempty"`
	ScenarioTitle        string             `json:"scenario_title"`
	Runtime              string             `json:"runtime"`
	ContentRevision      string             `json:"content_revision,omitempty"`
	RequestedAt          time.Time          `json:"requested_at"`
	StagingArtifact      *ArtifactReference `json:"staging_artifact"`
	Artifact             *ArtifactReference `json:"artifact,omitempty"`
}

// PublicationMetadata is read from the verified portable manifest immediately
// before an author confirms publication. The immutable source is read again by
// the finalizer, which remains the authority for materialized type and tags.
type PublicationMetadata struct {
	Title   string
	Runtime string
}

func (m PublicationMetadata) Validate() error {
	if strings.TrimSpace(m.Title) == "" || (m.Runtime != scenario.RuntimeNode && m.Runtime != scenario.RuntimeK8s) {
		return errors.New("publication metadata is invalid")
	}
	return nil
}

func (p Publication) ValidateIntent() error {
	if err := p.validateCommon(); err != nil {
		return err
	}
	if p.Artifact != nil || p.ContentRevision != "" {
		return errors.New("scenario publication intent contains final publication data")
	}
	return nil
}

func (p Publication) ValidateFinal() error {
	if err := p.validateCommon(); err != nil {
		return err
	}
	if p.Artifact == nil || p.Artifact.Validate(p.Runtime) != nil || !scenario.ValidRevision(p.ContentRevision) {
		return errors.New("final scenario publication is incomplete")
	}
	return nil
}

// ValidatePromotionResult accepts the durable result reported by a Runtime
// Worker after external promotion has succeeded. Server materialization has not
// happened yet, so ContentRevision is intentionally still absent.
func (p Publication) ValidatePromotionResult() error {
	if err := p.validateCommon(); err != nil {
		return err
	}
	if p.Artifact == nil || p.Artifact.Validate(p.Runtime) != nil || p.ContentRevision != "" {
		return errors.New("scenario publication promotion result is incomplete")
	}
	return nil
}

func (p Publication) validateCommon() error {
	if strings.TrimSpace(p.CandidateRevisionID) == "" || strings.TrimSpace(p.ScenarioTitle) == "" ||
		(p.Runtime != scenario.RuntimeNode && p.Runtime != scenario.RuntimeK8s) || p.IntentRevision < 1 ||
		p.RequestedAt.IsZero() || p.StagingArtifact == nil || p.StagingArtifact.Validate(p.Runtime) != nil {
		return errors.New("scenario publication intent is incomplete")
	}
	if !scenario.ValidID(p.ScenarioID) || !scenario.ValidRevisionID(p.ScenarioRevisionID) || !scenario.ValidSourceSlug(p.SourceSlug) ||
		scenario.ValidateMaterializedPath(p.TargetPath, p.SourceSlug, p.ScenarioRevisionID) != nil {
		return errors.New("allocated scenario publication intent is incomplete")
	}
	if p.BaseActiveRevisionID != "" && !scenario.ValidRevisionID(p.BaseActiveRevisionID) {
		return errors.New("scenario publication base active revision is invalid")
	}
	return nil
}

type Revision struct {
	ID                string                   `json:"id"`
	Source            Source                   `json:"source"`
	SourceRevision    string                   `json:"source_revision"`
	JudgeRunID        string                   `json:"judge_run_id,omitempty"`
	ParentCandidateID string                   `json:"parent_candidate_id,omitempty"`
	RepairReason      string                   `json:"repair_reason,omitempty"`
	ArchivePath       string                   `json:"-"`
	ArchiveSHA256     string                   `json:"archive_sha256"`
	Snapshot          ExecutionSnapshot        `json:"snapshot"`
	Build             *BuildOutput             `json:"build,omitempty"`
	Artifact          *ArtifactReference       `json:"artifact,omitempty"`
	VerifyEnvironment *VerificationEnvironment `json:"verify_environment,omitempty"`
	Verification      *VerificationReport      `json:"verification,omitempty"`
	Failure           *Failure                 `json:"failure,omitempty"`
	Publication       *Publication             `json:"publication,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	VerifiedAt        *time.Time               `json:"verified_at,omitempty"`
	PublishedAt       *time.Time               `json:"published_at,omitempty"`
}

// CandidateSubmission is the idempotent boundary that freezes the current
// Generator workspace into one immutable CandidateRevision. The Server assigns
// the revision ID while processing this request; clients never derive it from
// an AgentRun or local session identifier.
type CandidateSubmission struct {
	WorkflowID     string `json:"workflow_id"`
	TurnID         string `json:"turn_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s CandidateSubmission) Valid() bool {
	return strings.TrimSpace(s.WorkflowID) != "" && strings.TrimSpace(s.TurnID) != "" && validIdempotencyKey(s.IdempotencyKey)
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
	VerifyEnvironment *VerificationEnvironment `json:"verification_environment,omitempty"`
	Verification      *VerificationReport      `json:"verification,omitempty"`
	Failure           *Failure                 `json:"failure,omitempty"`
	Publication       *Publication             `json:"publication,omitempty"`
}

func (r Revision) WorkerView() WorkerView {
	var build *BuildOutput
	if r.Build != nil {
		copy := *r.Build
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

func (r Revision) ValidateForCreate() error {
	if strings.TrimSpace(r.ID) == "" || !r.Source.Valid() || strings.TrimSpace(r.SourceRevision) == "" {
		return errors.New("candidate revision requires identity and source revision")
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
