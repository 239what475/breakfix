package generation

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	execution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
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

type ClassificationResult string

const (
	ClassificationProposed       ClassificationResult = "proposed"
	ClassificationUnclassifiable ClassificationResult = "unclassifiable"
)

type NewTopic struct {
	Domain            roadmap.Ref `json:"domain"`
	Title             string      `json:"title"`
	Definition        string      `json:"definition"`
	Scope             string      `json:"scope"`
	NonGoals          string      `json:"non_goals"`
	ChallengeGuidance string      `json:"challenge_guidance"`
}

type TopicProposal struct {
	Existing *roadmap.Ref `json:"existing,omitempty"`
	New      *NewTopic    `json:"new,omitempty"`
	Reason   string       `json:"reason"`
}

type NewTag struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

type TagProposal struct {
	Existing *roadmap.Ref `json:"existing,omitempty"`
	New      *NewTag      `json:"new,omitempty"`
	Reason   string       `json:"reason"`
}

// ClassificationOutput is the closed typed result emitted by the Classifying
// role. The Server, not the model, supplies proposal identity and revisions
// when it persists this output for a frozen CandidateRevision.
type ClassificationOutput struct {
	Result               ClassificationResult `json:"result"`
	Topic                *TopicProposal       `json:"topic,omitempty"`
	Tags                 []TagProposal        `json:"tags,omitempty"`
	UnclassifiableReason string               `json:"unclassifiable_reason,omitempty"`
	AdjustmentSuggestion string               `json:"adjustment_suggestion,omitempty"`
}

func (o ClassificationOutput) Validate() error {
	return validateClassificationFields(o.Result, o.Topic, o.Tags, o.UnclassifiableReason, o.AdjustmentSuggestion)
}

type ClassificationProposal struct {
	Revision             int                  `json:"revision"`
	CandidateRevisionID  string               `json:"candidate_revision_id"`
	RoadmapRevision      string               `json:"roadmap_revision"`
	Result               ClassificationResult `json:"result"`
	Topic                *TopicProposal       `json:"topic,omitempty"`
	Tags                 []TagProposal        `json:"tags,omitempty"`
	UnclassifiableReason string               `json:"unclassifiable_reason,omitempty"`
	AdjustmentSuggestion string               `json:"adjustment_suggestion,omitempty"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

func (p ClassificationProposal) Validate() error {
	if p.Revision < 1 || strings.TrimSpace(p.CandidateRevisionID) == "" || !roadmap.ValidRevision(p.RoadmapRevision) {
		return errors.New("classification proposal identity is incomplete")
	}
	return validateClassificationFields(p.Result, p.Topic, p.Tags, p.UnclassifiableReason, p.AdjustmentSuggestion)
}

func validateClassificationFields(result ClassificationResult, topic *TopicProposal, tags []TagProposal, unclassifiableReason, adjustmentSuggestion string) error {
	switch result {
	case ClassificationProposed:
		if topic == nil || !topic.valid() || strings.TrimSpace(unclassifiableReason) != "" || strings.TrimSpace(adjustmentSuggestion) != "" {
			return errors.New("proposed classification requires one valid topic and no unclassifiable result")
		}
		seen := make(map[string]struct{}, len(tags))
		for _, tag := range tags {
			if !tag.valid() {
				return errors.New("classification tag proposal is invalid")
			}
			key := tag.key()
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate classification tag %q", key)
			}
			seen[key] = struct{}{}
		}
	case ClassificationUnclassifiable:
		if topic != nil || len(tags) != 0 || strings.TrimSpace(unclassifiableReason) == "" || strings.TrimSpace(adjustmentSuggestion) == "" {
			return errors.New("unclassifiable result requires a reason and adjustment suggestion")
		}
	default:
		return errors.New("classification proposal result is invalid")
	}
	return nil
}

func (p TopicProposal) valid() bool {
	if strings.TrimSpace(p.Reason) == "" || (p.Existing == nil) == (p.New == nil) {
		return false
	}
	if p.Existing != nil {
		return strings.TrimSpace(p.Existing.ID) != "" && strings.TrimSpace(p.Existing.SourceRef) != "" && strings.TrimSpace(p.Existing.Title) != ""
	}
	return p.New.valid()
}

func (t NewTopic) valid() bool {
	return strings.TrimSpace(t.Domain.ID) != "" && strings.TrimSpace(t.Domain.SourceRef) != "" && strings.TrimSpace(t.Domain.Title) != "" &&
		strings.TrimSpace(t.Title) != "" && strings.TrimSpace(t.Definition) != "" && strings.TrimSpace(t.Scope) != "" &&
		strings.TrimSpace(t.NonGoals) != "" && strings.TrimSpace(t.ChallengeGuidance) != ""
}

func (p TagProposal) valid() bool {
	if strings.TrimSpace(p.Reason) == "" || (p.Existing == nil) == (p.New == nil) {
		return false
	}
	if p.Existing != nil {
		return strings.TrimSpace(p.Existing.ID) != "" && strings.TrimSpace(p.Existing.SourceRef) != "" && strings.TrimSpace(p.Existing.Title) != ""
	}
	return strings.TrimSpace(p.New.Title) != "" && strings.TrimSpace(p.New.Description) != ""
}

func (p TagProposal) key() string {
	if p.Existing != nil {
		return "existing:" + p.Existing.SourceRef
	}
	return "new:" + strings.ToLower(strings.Join(strings.Fields(p.New.Title), " "))
}

type Publication struct {
	CandidateRevisionID    string             `json:"candidate_revision_id"`
	IntentRevision         int                `json:"intent_revision"`
	ChallengeID            string             `json:"challenge_id,omitempty"`
	ChallengeRevisionID    string             `json:"challenge_revision_id,omitempty"`
	BaseActiveRevisionID   string             `json:"base_active_revision_id,omitempty"`
	ChallengeSourceRef     string             `json:"challenge_source_ref,omitempty"`
	SourceSlug             string             `json:"source_slug,omitempty"`
	TargetPath             string             `json:"target_path,omitempty"`
	ChallengeTitle         string             `json:"challenge_title"`
	Runtime                string             `json:"runtime"`
	TopicSourceRef         string             `json:"topic_source_ref,omitempty"`
	TagSourceRefs          []string           `json:"tag_source_refs,omitempty"`
	ClassificationRevision int                `json:"classification_revision"`
	ContentRevision        string             `json:"content_revision,omitempty"`
	RequestedAt            time.Time          `json:"requested_at"`
	StagingArtifact        *ArtifactReference `json:"staging_artifact"`
	Artifact               *ArtifactReference `json:"artifact,omitempty"`
}

func (p Publication) ValidateIntent() error {
	if err := p.validateCommon(); err != nil {
		return err
	}
	if p.Artifact != nil || p.ContentRevision != "" {
		return errors.New("challenge publication intent contains final publication data")
	}
	return nil
}

func (p Publication) ValidateFinal() error {
	if err := p.validateCommon(); err != nil {
		return err
	}
	if p.Artifact == nil || p.Artifact.Validate(p.Runtime) != nil || !roadmap.ValidRevision(p.ContentRevision) {
		return errors.New("final challenge publication is incomplete")
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
		return errors.New("challenge publication promotion result is incomplete")
	}
	return nil
}

func (p Publication) validateCommon() error {
	if strings.TrimSpace(p.CandidateRevisionID) == "" || strings.TrimSpace(p.ChallengeTitle) == "" ||
		(p.Runtime != challenge.RuntimeNode && p.Runtime != challenge.RuntimeK8s) || p.IntentRevision < 1 || p.ClassificationRevision < 1 ||
		p.RequestedAt.IsZero() || p.StagingArtifact == nil || p.StagingArtifact.Validate(p.Runtime) != nil {
		return errors.New("challenge publication intent is incomplete")
	}
	if !challenge.ValidID(p.ChallengeID) || !challenge.ValidRevisionID(p.ChallengeRevisionID) || !challenge.ValidSourceSlug(p.SourceSlug) ||
		challenge.ValidateMaterializedPath(p.TargetPath, p.SourceSlug, p.ChallengeRevisionID) != nil || strings.TrimSpace(p.ChallengeSourceRef) == "" {
		return errors.New("allocated challenge publication intent is incomplete")
	}
	if p.BaseActiveRevisionID != "" && !challenge.ValidRevisionID(p.BaseActiveRevisionID) {
		return errors.New("challenge publication base active revision is invalid")
	}
	seen := make(map[string]struct{}, len(p.TagSourceRefs))
	for _, sourceRef := range p.TagSourceRefs {
		sourceRef = strings.TrimSpace(sourceRef)
		if sourceRef == "" {
			return errors.New("challenge publication has an empty tag reference")
		}
		if _, exists := seen[sourceRef]; exists {
			return fmt.Errorf("duplicate challenge publication tag %q", sourceRef)
		}
		seen[sourceRef] = struct{}{}
	}
	return nil
}

type Revision struct {
	ID                string                   `json:"id"`
	Source            Source                   `json:"source"`
	SourceRevision    string                   `json:"source_revision"`
	GeneratorRunID    string                   `json:"generator_run_id,omitempty"`
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
	Classification    *ClassificationProposal  `json:"classification,omitempty"`
	Publication       *Publication             `json:"publication,omitempty"`
	CreatedAt         time.Time                `json:"created_at"`
	UpdatedAt         time.Time                `json:"updated_at"`
	VerifiedAt        *time.Time               `json:"verified_at,omitempty"`
	PublishedAt       *time.Time               `json:"published_at,omitempty"`
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
	Classification    *ClassificationProposal  `json:"classification,omitempty"`
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
		Verification: r.Verification, Failure: failure, Classification: r.Classification, Publication: r.Publication,
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
	if r.Source.Kind == SourceAuthoring && strings.TrimSpace(r.GeneratorRunID) == "" {
		return errors.New("authoring candidate revision requires generator lineage")
	}
	if strings.TrimSpace(r.ArchivePath) == "" || !ValidSHA256(r.ArchiveSHA256) {
		return errors.New("candidate revision requires an immutable archive")
	}
	if r.Build != nil || r.Artifact != nil || r.VerifyEnvironment != nil || r.Verification != nil || r.Failure != nil || r.Classification != nil || r.Publication != nil {
		return errors.New("new candidate revision must not contain stage outputs")
	}
	return r.Snapshot.Validate()
}

func ValidSHA256(value string) bool { return execution.ValidSHA256(value) }
