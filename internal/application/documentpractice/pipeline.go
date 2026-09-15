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
// or network capabilities.
type Reader interface {
	ReadPage(string, string) (domain.Page, error)
	ReadMetadata(string) (domain.Metadata, error)
	ReadSource(string, int, int) (domain.SourceFragment, error)
	ReadInclude(string, int, int) (domain.SourceFragment, error)
}
type Page = domain.Page
type Metadata = domain.Metadata
type SourceFragment = domain.SourceFragment

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
	if plan.Context.Commit != page.Context.Commit || plan.Context.MirrorDigest != page.Context.MirrorDigest || plan.Context.PagePath != path {
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
	return domain.GateResult{ArtifactID: bundle.ArtifactID, ArtifactDigest: bundle.ArtifactDigest, Decision: decision, Reasons: reasons, PolicyVersion: "document-gate-v1", CreatedAt: time.Now().UTC()}, nil
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
	if candidate.Context.Commit != plan.Context.Commit || candidate.Context.MirrorDigest != plan.Context.MirrorDigest || candidate.Context.PagePath != plan.Context.PagePath {
		return errors.New("candidate changed document evidence context")
	}
	return nil
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
