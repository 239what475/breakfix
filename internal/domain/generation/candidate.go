package generation

import (
	"errors"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

var (
	ErrCandidateNotFound     = errors.New("candidate revision not found")
	ErrCandidateInvalidState = errors.New("candidate revision is not in the required state")
)

// Publication reserves Operations content identities before Server-owned
// materialization. Provider artifacts are deliberately absent: both finalizer
// and persistence resolve them from Candidate.RunnableRevisionRef.
type Publication struct {
	CandidateRevisionID  string    `json:"candidate_revision_id"`
	IntentRevision       int       `json:"intent_revision"`
	ScenarioID           string    `json:"scenario_id,omitempty"`
	ScenarioRevisionID   string    `json:"scenario_revision_id,omitempty"`
	BaseActiveRevisionID string    `json:"base_active_revision_id,omitempty"`
	SourceSlug           string    `json:"source_slug,omitempty"`
	TargetPath           string    `json:"target_path,omitempty"`
	ScenarioTitle        string    `json:"scenario_title"`
	RequestedAt          time.Time `json:"requested_at"`
}

// PublicationMetadata is re-read from the immutable candidate archive just
// before an author confirms publication. Runtime belongs to RunnableSpec and
// is never copied into a Generation publication record.
type PublicationMetadata struct {
	Title string
}

func (m PublicationMetadata) Validate() error {
	if strings.TrimSpace(m.Title) == "" {
		return errors.New("publication metadata is invalid")
	}
	return nil
}

func (p Publication) ValidateIntent() error {
	if strings.TrimSpace(p.CandidateRevisionID) == "" || strings.TrimSpace(p.ScenarioTitle) == "" || p.IntentRevision < 1 || p.RequestedAt.IsZero() ||
		!scenario.ValidID(p.ScenarioID) || !scenario.ValidRevisionID(p.ScenarioRevisionID) || !scenario.ValidSourceSlug(p.SourceSlug) ||
		scenario.ValidateMaterializedPath(p.TargetPath, p.SourceSlug, p.ScenarioRevisionID) != nil {
		return errors.New("scenario publication intent is incomplete")
	}
	if p.BaseActiveRevisionID != "" && !scenario.ValidRevisionID(p.BaseActiveRevisionID) {
		return errors.New("scenario publication base active revision is invalid")
	}
	return nil
}

// Revision is the immutable authored source and its public runnable progress.
// It has no legacy Snapshot, BuildOutput, ArtifactReference, verification
// Environment, Worker lease, or provider retry state.
type Revision struct {
	ID                    string                                `json:"id"`
	Source                Source                                `json:"source"`
	SourceRevision        string                                `json:"source_revision"`
	JudgeRunID            string                                `json:"judge_run_id,omitempty"`
	ParentCandidateID     string                                `json:"parent_candidate_id,omitempty"`
	RepairReason          string                                `json:"repair_reason,omitempty"`
	ArchivePath           string                                `json:"-"`
	ArchiveDigest         string                                `json:"archive_digest"`
	ContentRevision       string                                `json:"content_revision"`
	SourceArchive         runnable.SourceArchive                `json:"source_archive"`
	RunnableRevisionRef   *runnable.RevisionReference           `json:"runnable_revision_ref,omitempty"`
	VerificationReportRef *runnable.VerificationReportReference `json:"verification_report_ref,omitempty"`
	Failure               *Failure                              `json:"failure,omitempty"`
	Publication           *Publication                          `json:"publication,omitempty"`
	CreatedAt             time.Time                             `json:"created_at"`
	UpdatedAt             time.Time                             `json:"updated_at"`
	VerifiedAt            *time.Time                            `json:"verified_at,omitempty"`
	PublishedAt           *time.Time                            `json:"published_at,omitempty"`
}

// CandidateSubmission is the idempotent boundary that freezes the current
// Generator workspace into one immutable CandidateRevision.
type CandidateSubmission struct {
	WorkflowID     string `json:"workflow_id"`
	TurnID         string `json:"turn_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s CandidateSubmission) Valid() bool {
	return strings.TrimSpace(s.WorkflowID) != "" && strings.TrimSpace(s.TurnID) != "" && validIdempotencyKey(s.IdempotencyKey)
}

func (r Revision) ValidateForCreate() error {
	if strings.TrimSpace(r.ID) == "" || !r.Source.Valid() || strings.TrimSpace(r.SourceRevision) == "" || strings.TrimSpace(r.ArchivePath) == "" || !ValidSHA256(r.ArchiveDigest) || !ValidSHA256(r.ContentRevision) || r.SourceArchive.Validate() != nil {
		return errors.New("candidate revision requires source identity and frozen archive")
	}
	if r.RunnableRevisionRef != nil || r.VerificationReportRef != nil || r.Failure != nil || r.Publication != nil || r.VerifiedAt != nil || r.PublishedAt != nil {
		return errors.New("new candidate revision must not contain lifecycle outputs")
	}
	return nil
}

func ValidSHA256(value string) bool { return runnable.ValidDigest(value) }
