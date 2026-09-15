// Package documentpractice contains the product contract for executable
// documentation examples. It is intentionally separate from Operations and
// the runtime worker; the latter only receives the compiled runnable spec.
package documentpractice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const FormatVersion = "v1"

type EvidenceKind string

const (
	EvidencePage     EvidenceKind = "page"
	EvidenceRendered EvidenceKind = "rendered"
	EvidenceSource   EvidenceKind = "source"
	EvidenceInclude  EvidenceKind = "include"
	EvidenceResource EvidenceKind = "resource"
)

func (k EvidenceKind) Valid() bool {
	return k == EvidencePage || k == EvidenceRendered || k == EvidenceSource || k == EvidenceInclude || k == EvidenceResource
}

// DocumentContext identifies one immutable document scope. The upstream
// revision and fixed page coordinates are the identity; rendered HTML is
// presentation, not an identity input.
type DocumentContext struct {
	FormatVersion string `json:"format_version"`
	SourceID      string `json:"source_id"`
	Repository    string `json:"repository"`
	Commit        string `json:"commit"`
	Version       string `json:"version"`
	Language      string `json:"language"`
	License       string `json:"license"`
	PagePath      string `json:"page_path"`
	Anchor        string `json:"anchor,omitempty"`
}

func (c DocumentContext) Validate() error {
	if c.FormatVersion != FormatVersion || strings.TrimSpace(c.SourceID) == "" || strings.TrimSpace(c.Repository) == "" ||
		strings.TrimSpace(c.Commit) == "" || strings.TrimSpace(c.Version) == "" || strings.TrimSpace(c.Language) == "" ||
		strings.TrimSpace(c.License) == "" {
		return errors.New("document context requires a complete pinned source")
	}
	if err := ValidateRelativePath(c.PagePath); err != nil {
		return fmt.Errorf("document context page path: %w", err)
	}
	return nil
}

func (c DocumentContext) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

type EvidenceReference struct {
	ID        string       `json:"id"`
	Kind      EvidenceKind `json:"kind"`
	Path      string       `json:"path"`
	Digest    string       `json:"digest"`
	StartLine int          `json:"start_line,omitempty"`
	EndLine   int          `json:"end_line,omitempty"`
	Anchor    string       `json:"anchor,omitempty"`
	ParentID  string       `json:"parent_id,omitempty"`
	Quote     string       `json:"quote,omitempty"`
}

type Page struct {
	Context DocumentContext `json:"context"`
	Path    string          `json:"path"`
	Anchor  string          `json:"anchor,omitempty"`
	Content string          `json:"content"`
	Digest  string          `json:"digest"`
}
type Metadata struct {
	Context DocumentContext `json:"context"`
	Path    string          `json:"path"`
	Title   string          `json:"title"`
	Anchors []string        `json:"anchors"`
	Digest  string          `json:"digest"`
}
type SourceFragment struct {
	Context  DocumentContext   `json:"context"`
	Evidence EvidenceReference `json:"evidence"`
	Content  string            `json:"content"`
}

func (e EvidenceReference) Validate() error {
	if err := stableID(e.ID, "evidence.id"); err != nil {
		return err
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("evidence kind %q is unsupported", e.Kind)
	}
	if err := ValidateRelativePath(e.Path); err != nil {
		return err
	}
	if !runnable.ValidDigest(e.Digest) {
		return errors.New("evidence digest must be sha256")
	}
	if e.StartLine < 0 || e.EndLine < 0 || (e.StartLine > 0 && e.EndLine > 0 && e.EndLine < e.StartLine) {
		return errors.New("evidence line range is invalid")
	}
	if len(e.Quote) > 16*1024 {
		return errors.New("evidence quote exceeds limit")
	}
	return nil
}

type RuntimeConstraint struct {
	Runtime   runnable.Runtime        `json:"runtime"`
	BaseImage string                  `json:"base_image"`
	Resources runnable.ResourceLimits `json:"resources"`
	Network   runnable.NetworkScope   `json:"network"`
	Topology  string                  `json:"topology"`
}

func (r RuntimeConstraint) Validate() error {
	if !r.Runtime.Valid() || strings.TrimSpace(r.BaseImage) == "" || !r.Network.Valid() || strings.TrimSpace(r.Topology) == "" {
		return errors.New("runtime constraint is incomplete")
	}
	return r.Resources.Validate()
}

type UserStep struct {
	ID          string   `json:"id"`
	Instruction string   `json:"instruction"`
	EvidenceIDs []string `json:"evidence_ids"`
}
type ObservationPoint struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// LearningUnitPlan is the only planner output accepted by the deterministic
// plan gate. no_practice is explicit and still retains the source evidence.
type LearningUnitPlan struct {
	FormatVersion string              `json:"format_version"`
	ID            string              `json:"id"`
	Revision      int64               `json:"revision"`
	Context       DocumentContext     `json:"context"`
	Title         string              `json:"title"`
	Objective     string              `json:"objective"`
	Boundary      string              `json:"boundary"`
	Runtime       RuntimeConstraint   `json:"runtime"`
	UserSteps     []UserStep          `json:"user_steps,omitempty"`
	Observations  []ObservationPoint  `json:"observations,omitempty"`
	Evidence      []EvidenceReference `json:"evidence"`
	NoPractice    bool                `json:"no_practice"`
	CreatedAt     time.Time           `json:"created_at"`
}

func (p LearningUnitPlan) Validate() error {
	if p.FormatVersion != FormatVersion || p.Revision < 1 {
		return errors.New("learning unit plan format or revision is invalid")
	}
	if err := stableID(p.ID, "plan.id"); err != nil {
		return err
	}
	if err := p.Context.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Objective) == "" || strings.TrimSpace(p.Boundary) == "" {
		return errors.New("learning unit plan requires title, objective, and boundary")
	}
	if err := p.Runtime.Validate(); err != nil {
		return err
	}
	if len(p.Evidence) == 0 {
		return errors.New("learning unit plan requires evidence")
	}
	seen := map[string]bool{}
	for _, e := range p.Evidence {
		if err := e.Validate(); err != nil {
			return err
		}
		if seen[e.ID] {
			return fmt.Errorf("duplicate evidence %q", e.ID)
		}
		seen[e.ID] = true
	}
	if p.NoPractice {
		return nil
	}
	if len(p.Observations) == 0 {
		return errors.New("practice plan requires an observation")
	}
	for _, s := range p.UserSteps {
		if err := stableID(s.ID, "user_step.id"); err != nil {
			return err
		}
		if strings.TrimSpace(s.Instruction) == "" {
			return errors.New("user step instruction is required")
		}
		if !referencesExist(s.EvidenceIDs, seen) {
			return errors.New("user step references unknown evidence")
		}
	}
	for _, o := range p.Observations {
		if err := stableID(o.ID, "observation.id"); err != nil {
			return err
		}
		if strings.TrimSpace(o.Description) == "" || !referencesExist(o.EvidenceIDs, seen) {
			return errors.New("observation is incomplete or ungrounded")
		}
	}
	return nil
}

func referencesExist(ids []string, known map[string]bool) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !known[id] {
			return false
		}
	}
	return true
}

type PracticeCandidate struct {
	FormatVersion string                 `json:"format_version"`
	ID            string                 `json:"id"`
	Revision      int64                  `json:"revision"`
	PlanID        string                 `json:"plan_id"`
	PlanRevision  int64                  `json:"plan_revision"`
	Context       DocumentContext        `json:"context"`
	Source        runnable.SourceArchive `json:"source"`
	Spec          runnable.RunnableSpec  `json:"spec"`
	UserSteps     []UserStep             `json:"user_steps,omitempty"`
	Observations  []ObservationPoint     `json:"observations,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
}

func (c PracticeCandidate) Validate() error {
	if c.FormatVersion != FormatVersion || c.Revision < 1 || c.PlanRevision < 1 {
		return errors.New("practice candidate version is invalid")
	}
	if err := stableID(c.ID, "candidate.id"); err != nil {
		return err
	}
	if strings.TrimSpace(c.PlanID) == "" {
		return errors.New("candidate plan binding is required")
	}
	if err := c.Context.Validate(); err != nil {
		return errors.New("candidate context is invalid")
	}
	if err := c.Source.Validate(); err != nil {
		return err
	}
	if err := c.Spec.Validate(); err != nil {
		return err
	}
	if c.Spec.Identity.Kind != "documentation-practice" || c.Spec.Identity.ID != ContentID(c.Context) || c.Spec.Source != c.Source {
		return errors.New("candidate does not bind the documentation context and source to its spec")
	}
	return nil
}

// ContentID converts a source/page identity to the stable identifier required
// by the public Runnable contract without placing a path in its ID field.
func ContentID(ctx DocumentContext) string {
	sum := sha256.Sum256([]byte(ctx.SourceID + "\x00" + ctx.Commit + "\x00" + ctx.Language + "\x00" + ctx.PagePath + "\x00" + ctx.Anchor))
	return "practice-" + hex.EncodeToString(sum[:])[:16]
}

type ReviewDecision string

const (
	ReviewApprove ReviewDecision = "approve"
	ReviewReject  ReviewDecision = "reject"
)

type ReviewOpinion struct {
	ReviewerID    string         `json:"reviewer_id"`
	Role          string         `json:"role"`
	Decision      ReviewDecision `json:"decision"`
	HardReject    bool           `json:"hard_reject"`
	Reasons       []string       `json:"reasons,omitempty"`
	PolicyVersion string         `json:"policy_version"`
}

type GateResult struct {
	ArtifactID     string         `json:"artifact_id"`
	ArtifactDigest string         `json:"artifact_digest"`
	Decision       ReviewDecision `json:"decision"`
	Reasons        []string       `json:"reasons,omitempty"`
	PolicyVersion  string         `json:"policy_version"`
	CreatedAt      time.Time      `json:"created_at"`
}

func (g GateResult) Approved() bool { return g.Decision == ReviewApprove }

type VerificationReviewBundle struct {
	ArtifactID     string          `json:"artifact_id"`
	ArtifactDigest string          `json:"artifact_digest"`
	ReportDigest   string          `json:"report_digest"`
	Opinions       []ReviewOpinion `json:"opinions"`
	CreatedAt      time.Time       `json:"created_at"`
}

type PublicationManifest struct {
	FormatVersion            string                   `json:"format_version"`
	ID                       string                   `json:"id"`
	Context                  DocumentContext          `json:"document_context"`
	PracticeCandidateID      string                   `json:"practice_candidate_id"`
	RunnableRevisionDigest   string                   `json:"runnable_revision_digest"`
	EnvironmentProfileDigest string                   `json:"environment_profile_digest"`
	VerificationReportDigest string                   `json:"verification_report_digest"`
	PlanGate                 GateResult               `json:"plan_gate"`
	ArtifactGate             GateResult               `json:"artifact_gate"`
	VerificationReview       VerificationReviewBundle `json:"verification_review"`
	CreatedAt                time.Time                `json:"created_at"`
}

// PracticeRevision is the immutable product record made visible by the
// documentation publication finalizer. It references, rather than copies,
// the common runtime revision and report.
type PracticeRevision struct {
	FormatVersion         string                               `json:"format_version"`
	ID                    string                               `json:"id"`
	WorkflowID            string                               `json:"workflow_id"`
	Context               DocumentContext                      `json:"document_context"`
	PlanID                string                               `json:"plan_id"`
	PlanRevision          int64                                `json:"plan_revision"`
	CandidateID           string                               `json:"candidate_id"`
	RunnableRevisionRef   runnable.RevisionReference           `json:"runnable_revision_ref"`
	VerificationReportRef runnable.VerificationReportReference `json:"verification_report_ref"`
	PublicationManifestID string                               `json:"publication_manifest_id"`
	PublishedAt           time.Time                            `json:"published_at"`
}

func (r PracticeRevision) Validate() error {
	if r.FormatVersion != FormatVersion || strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.WorkflowID) == "" || strings.TrimSpace(r.PlanID) == "" || r.PlanRevision < 1 || strings.TrimSpace(r.CandidateID) == "" || strings.TrimSpace(r.PublicationManifestID) == "" || r.PublishedAt.IsZero() {
		return errors.New("practice revision is incomplete")
	}
	if err := r.Context.Validate(); err != nil {
		return err
	}
	if err := r.RunnableRevisionRef.Validate(); err != nil {
		return err
	}
	return r.VerificationReportRef.Validate()
}

func (b VerificationReviewBundle) Validate(report runnable.VerificationReport) error {
	if strings.TrimSpace(b.ArtifactID) == "" || !runnable.ValidDigest(b.ArtifactDigest) || len(b.Opinions) == 0 || b.CreatedAt.IsZero() {
		return errors.New("verification review bundle is incomplete")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(encoded)
	if "sha256:"+hex.EncodeToString(sum[:]) != b.ReportDigest {
		return errors.New("verification review is bound to another report")
	}
	return nil
}

func (m PublicationManifest) Validate(report runnable.VerificationReport) error {
	if m.FormatVersion != FormatVersion || strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.PracticeCandidateID) == "" || !runnable.ValidDigest(m.RunnableRevisionDigest) || !runnable.ValidDigest(m.EnvironmentProfileDigest) || !runnable.ValidDigest(m.VerificationReportDigest) || m.CreatedAt.IsZero() {
		return errors.New("publication manifest is incomplete")
	}
	if !m.PlanGate.Approved() || !m.ArtifactGate.Approved() || !report.Passed {
		return errors.New("publication prerequisites are not satisfied")
	}
	if err := m.VerificationReview.Validate(report); err != nil {
		return err
	}
	verified := false
	for _, opinion := range m.VerificationReview.Opinions {
		if strings.TrimSpace(opinion.ReviewerID) == "" || strings.TrimSpace(opinion.Role) == "" || strings.TrimSpace(opinion.PolicyVersion) == "" || (opinion.Decision != ReviewApprove && opinion.Decision != ReviewReject) || opinion.HardReject || opinion.Decision == ReviewReject {
			return errors.New("verification review includes a rejected or invalid opinion")
		}
		if opinion.Role == "verification" {
			verified = true
		}
	}
	if !verified {
		return errors.New("publication requires a verification review")
	}
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	if "sha256:"+hex.EncodeToString(sum[:]) != m.VerificationReportDigest {
		return errors.New("publication report digest mismatch")
	}
	return m.Context.Validate()
}

func stableID(value, field string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return fmt.Errorf("%s must be a bounded identifier", field)
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '/' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return fmt.Errorf("%s contains an invalid character", field)
		}
	}
	return nil
}

func ValidateRelativePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") || strings.Contains(value, "\\") {
		return errors.New("path must be a safe relative path")
	}
	return nil
}

func sameContextIdentity(a, b DocumentContext) bool {
	return a.SourceID == b.SourceID && a.Commit == b.Commit && a.Language == b.Language && a.PagePath == b.PagePath && a.Anchor == b.Anchor
}
