package documentpractice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// Planner reads only through a fixed-revision Reader and delegates semantic
// suggestions to an Agent. The Agent never receives unrestricted filesystem
// or network capabilities. The Reader serves offline-parsed library output
// only; there is no runtime source-file or rendered-HTML access.
type Reader interface {
	ReadPage(string, string) (domain.Page, error)
	ReadMetadata(string) (domain.Metadata, error)
}
type Page = domain.Page
type Metadata = domain.Metadata

type PlanningAgent interface {
	Propose(context.Context, Page, Metadata, []domain.EvidenceReference) (domain.LearningUnitPlan, error)
}

type Planner struct {
	Reader Reader
	Agent  PlanningAgent
}

func (p Planner) Plan(ctx context.Context, path, anchor string) (domain.LearningUnitPlan, error) {
	if p.Reader == nil || p.Agent == nil {
		return domain.LearningUnitPlan{}, errors.New("planner requires reader and agent")
	}
	page, err := p.Reader.ReadPage(path, anchor)
	if err != nil {
		return domain.LearningUnitPlan{}, err
	}
	metadata, err := p.Reader.ReadMetadata(path)
	if err != nil {
		return domain.LearningUnitPlan{}, err
	}
	evidence := []domain.EvidenceReference{{ID: "page", Kind: domain.EvidencePage, Path: path, Digest: page.Digest, Anchor: anchor, Quote: page.Content}}
	plan, err := p.Agent.Propose(ctx, page, metadata, evidence)
	if err != nil {
		return domain.LearningUnitPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return domain.LearningUnitPlan{}, fmt.Errorf("planner returned invalid plan: %w", err)
	}
	if plan.Context.SourceID != page.Context.SourceID || plan.Context.Repository != page.Context.Repository || plan.Context.Commit != page.Context.Commit || plan.Context.Version != page.Context.Version || plan.Context.Language != page.Context.Language || plan.Context.License != page.Context.License || plan.Context.PagePath != path {
		return domain.LearningUnitPlan{}, errors.New("planner changed pinned document context")
	}
	return plan, nil
}

type ReviewBundle struct {
	ArtifactID     string                 `json:"artifact_id"`
	ArtifactDigest string                 `json:"artifact_digest"`
	Opinions       []domain.ReviewOpinion `json:"opinions"`
	CreatedAt      time.Time              `json:"created_at"`
}

func (b ReviewBundle) Validate() error {
	if strings.TrimSpace(b.ArtifactID) == "" || !runnable.ValidDigest(b.ArtifactDigest) || len(b.Opinions) == 0 || b.CreatedAt.IsZero() {
		return errors.New("review bundle is incomplete")
	}
	seen := map[string]bool{}
	for _, o := range b.Opinions {
		if strings.TrimSpace(o.ReviewerID) == "" || strings.TrimSpace(o.Role) == "" || (o.Decision != domain.ReviewApprove && o.Decision != domain.ReviewReject) || strings.TrimSpace(o.PolicyVersion) == "" || seen[o.ReviewerID] {
			return errors.New("review opinion is invalid or duplicated")
		}
		seen[o.ReviewerID] = true
	}
	return nil
}

// Gate applies fixed rules. Agent opinions are evidence, never authority.
func Gate(bundle ReviewBundle, requiredRoles ...string) (domain.GateResult, error) {
	if err := bundle.Validate(); err != nil {
		return domain.GateResult{}, err
	}
	roles := map[string]bool{}
	hardReject := false
	reasons := []string{}
	for _, o := range bundle.Opinions {
		roles[o.Role] = true
		if o.HardReject || o.Decision == domain.ReviewReject {
			hardReject = true
			reasons = append(reasons, o.Role+": rejected")
		}
		reasons = append(reasons, o.Reasons...)
	}
	for _, role := range requiredRoles {
		if !roles[role] {
			hardReject = true
			reasons = append(reasons, "missing reviewer role: "+role)
		}
	}
	decision := domain.ReviewApprove
	if hardReject {
		decision = domain.ReviewReject
	}
	// The gate result is itself an immutable ledger artifact. Its timestamp is
	// derived from the reviewed bundle so an exact retry has the same digest.
	return domain.GateResult{ArtifactID: bundle.ArtifactID, ArtifactDigest: bundle.ArtifactDigest, Decision: decision, Reasons: reasons, PolicyVersion: "document-gate-v1", CreatedAt: bundle.CreatedAt.UTC()}, nil
}

func ValidateReviewIndependence(producerRunID string, bundle ReviewBundle) error {
	if strings.TrimSpace(producerRunID) == "" {
		return errors.New("producer AgentRun id is required")
	}
	if err := bundle.Validate(); err != nil {
		return err
	}
	for _, opinion := range bundle.Opinions {
		if opinion.ReviewerID == producerRunID {
			return errors.New("an AgentRun cannot review its own artifact")
		}
	}
	return nil
}

type CandidateGenerator interface {
	Generate(context.Context, domain.LearningUnitPlan) (domain.PracticeCandidate, error)
}

func ValidateCandidateAgainstPlan(candidate domain.PracticeCandidate, plan domain.LearningUnitPlan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.NoPractice {
		return errors.New("no_practice plan cannot generate a candidate")
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if candidate.PlanID != plan.ID || candidate.PlanRevision != plan.Revision {
		return errors.New("candidate is bound to another plan revision")
	}
	if candidate.Context.Commit != plan.Context.Commit || candidate.Context.PagePath != plan.Context.PagePath {
		return errors.New("candidate changed document evidence context")
	}
	profile := candidate.Spec.RuntimeProfile
	if profile.Runtime != plan.Runtime.Runtime || profile.BaseImage != plan.Runtime.BaseImage || profile.Network != plan.Runtime.Network || profile.Topology != plan.Runtime.Topology || profile.Resources != plan.Runtime.Resources {
		return errors.New("candidate expanded the approved runtime profile")
	}
	return nil
}

type FrozenCandidate struct {
	Candidate     domain.PracticeCandidate
	ArchiveDigest string
}

func FreezeCandidate(candidate domain.PracticeCandidate, archive []byte) (FrozenCandidate, error) {
	if len(archive) == 0 {
		return FrozenCandidate{}, errors.New("candidate archive is empty")
	}
	digestBytes := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(digestBytes[:])
	candidate.Source.Digest = digest
	// The Server, not an Agent, derives both source digest fields from exact
	// archive bytes before accepting a candidate. All other source fields remain
	// part of the generated contract and are validated below.
	candidate.Spec.Source.Digest = digest
	if err := candidate.Validate(); err != nil {
		return FrozenCandidate{}, err
	}
	return FrozenCandidate{Candidate: candidate, ArchiveDigest: digest}, nil
}

type ArtifactReviewBundle struct {
	CandidateID     string                 `json:"candidate_id"`
	CandidateDigest string                 `json:"candidate_digest"`
	SpecDigest      string                 `json:"spec_digest"`
	PlanID          string                 `json:"plan_id"`
	PlanRevision    int64                  `json:"plan_revision"`
	Opinions        []domain.ReviewOpinion `json:"opinions"`
	CreatedAt       time.Time              `json:"created_at"`
}

func (b ArtifactReviewBundle) Validate(plan domain.LearningUnitPlan, candidate domain.PracticeCandidate) error {
	if err := ValidateCandidateAgainstPlan(candidate, plan); err != nil {
		return err
	}
	if b.CandidateID != candidate.ID || b.PlanID != plan.ID || b.PlanRevision != plan.Revision || !runnable.ValidDigest(b.CandidateDigest) || !runnable.ValidDigest(b.SpecDigest) || len(b.Opinions) == 0 || b.CreatedAt.IsZero() {
		return errors.New("artifact review binding is invalid")
	}
	gotSpec, err := candidate.Spec.Digest()
	if err != nil || gotSpec != b.SpecDigest {
		return errors.New("artifact review spec digest mismatch")
	}
	archiveDigest := candidate.Source.Digest
	if archiveDigest != b.CandidateDigest {
		return errors.New("artifact review candidate digest mismatch")
	}
	return ReviewBundle{ArtifactID: b.CandidateID, ArtifactDigest: b.CandidateDigest, Opinions: b.Opinions, CreatedAt: b.CreatedAt}.Validate()
}

func ArtifactGate(bundle ArtifactReviewBundle, plan domain.LearningUnitPlan, candidate domain.PracticeCandidate, requiredRoles ...string) (domain.GateResult, error) {
	if err := bundle.Validate(plan, candidate); err != nil {
		return domain.GateResult{}, err
	}
	return Gate(ReviewBundle{ArtifactID: bundle.CandidateID, ArtifactDigest: bundle.CandidateDigest, Opinions: bundle.Opinions, CreatedAt: bundle.CreatedAt}, requiredRoles...)
}

// Publish validates every immutable prerequisite in one place. Callers can
// persist the returned manifest atomically with their product index.
func Publish(candidate domain.PracticeCandidate, planGate, artifactGate domain.GateResult, revision runnable.RunnableRevision, report runnable.VerificationReport, review VerificationReviewBundle, now time.Time) (domain.PublicationManifest, error) {
	if !planGate.Approved() || !artifactGate.Approved() {
		return domain.PublicationManifest{}, errors.New("publication requires approved plan and artifact gates")
	}
	if err := candidate.Validate(); err != nil {
		return domain.PublicationManifest{}, err
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		return domain.PublicationManifest{}, err
	}
	reportDigest, err := DigestJSON(report)
	if err != nil {
		return domain.PublicationManifest{}, err
	}
	if err := review.Validate(report); err != nil {
		return domain.PublicationManifest{}, err
	}
	if report.RunnableRevisionDigest != revisionDigest {
		return domain.PublicationManifest{}, errors.New("verification report is bound to another runnable revision")
	}
	verificationGate, err := Gate(ReviewBundle{ArtifactID: review.ArtifactID, ArtifactDigest: review.ArtifactDigest, Opinions: review.Opinions, CreatedAt: review.CreatedAt}, "verification")
	if err != nil || !verificationGate.Approved() {
		return domain.PublicationManifest{}, errors.New("verification review gate is not approved")
	}
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		return domain.PublicationManifest{}, err
	}
	manifest := domain.PublicationManifest{FormatVersion: domain.FormatVersion, ID: "publication-" + candidate.ID, Context: candidate.Context, PracticeCandidateID: candidate.ID, RunnableRevisionDigest: revisionDigest, EnvironmentProfileDigest: profileDigest, VerificationReportDigest: reportDigest, PlanGate: planGate, ArtifactGate: artifactGate, VerificationReview: review, CreatedAt: now.UTC()}
	if err := manifest.Validate(report); err != nil {
		return domain.PublicationManifest{}, err
	}
	return manifest, nil
}

type VerificationReviewBundle = domain.VerificationReviewBundle

type PublicationManifest = domain.PublicationManifest

func DigestJSON(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

// UntrustedDocument wraps page text as data. Callers pass SystemInstruction
// separately to their model adapter; the wrapper makes it impossible to
// accidentally concatenate document text into the instruction channel.
type AgentInput struct {
	SystemInstruction string                     `json:"system_instruction"`
	DocumentData      string                     `json:"document_data"`
	Evidence          []domain.EvidenceReference `json:"evidence"`
}

func NewAgentInput(systemInstruction, documentData string, evidence []domain.EvidenceReference) (AgentInput, error) {
	if strings.TrimSpace(systemInstruction) == "" || strings.TrimSpace(documentData) == "" || len(documentData) > 512*1024 || len(evidence) == 0 {
		return AgentInput{}, errors.New("agent input requires bounded instruction, document data, and evidence")
	}
	for _, ref := range evidence {
		if err := ref.Validate(); err != nil {
			return AgentInput{}, err
		}
	}
	return AgentInput{SystemInstruction: systemInstruction, DocumentData: documentData, Evidence: append([]domain.EvidenceReference(nil), evidence...)}, nil
}

// Ledger is a small in-memory implementation used by workflow tests and
// local development. Production persistence follows the same append-only API.
type Ledger struct {
	mu        sync.Mutex
	artifacts map[string]domain.ArtifactRecord
	workflows map[string]domain.Workflow
}

func NewLedger() *Ledger {
	return &Ledger{artifacts: map[string]domain.ArtifactRecord{}, workflows: map[string]domain.Workflow{}}
}
func (l *Ledger) PutWorkflow(w domain.Workflow) error {
	if err := w.Validate(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.workflows[w.ID]; ok {
		return errors.New("workflow already exists")
	}
	l.workflows[w.ID] = w
	return nil
}
func (l *Ledger) Append(workflowID string, artifact domain.ArtifactRecord, now time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.workflows[workflowID]
	if !ok {
		return errors.New("workflow not found")
	}
	if err := w.Append(artifact, now); err != nil {
		return err
	}
	l.workflows[workflowID] = w
	l.artifacts[artifact.ID] = artifact
	return nil
}
func (l *Ledger) Workflow(id string) (domain.Workflow, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.workflows[id]
	return w, ok
}
