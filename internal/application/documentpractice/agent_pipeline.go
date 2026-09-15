package documentpractice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// PlanReviewRole, CandidateReviewRole, and VerificationReviewRole intentionally
// have separate calls. A concrete Agent adapter cannot receive a capability for
// a later phase simply because it can implement several interfaces.
type PlanReviewRole interface {
	ReviewPlan(context.Context, string, domain.LearningUnitPlan) (domain.ReviewOpinion, error)
}

type CandidateReviewRole interface {
	ReviewCandidate(context.Context, string, domain.LearningUnitPlan, domain.PracticeCandidate, []GeneratedFile) (domain.ReviewOpinion, error)
}

type VerificationReviewRole interface {
	ReviewVerification(context.Context, string, domain.LearningUnitPlan, runnable.VerificationReport) (domain.ReviewOpinion, error)
}

type BlueprintGenerator interface {
	Generate(context.Context, domain.LearningUnitPlan, runnable.RuntimeProfile) (CandidateBlueprint, error)
}

type RuntimeProfileResolver interface {
	DocumentationRuntimeConstraints() []domain.RuntimeConstraint
	ResolveDocumentationRuntimeProfile(domain.RuntimeConstraint) (runnable.RuntimeProfile, error)
}

// ConstrainedPlanningAgent receives only Server-resolved runtime choices. The
// compatibility-free fallback is used by focused tests and cannot be used by
// the production DocumentPlanner adapter.
type ConstrainedPlanningAgent interface {
	ProposeConstrained(context.Context, domain.Page, domain.Metadata, []domain.EvidenceReference, []domain.RuntimeConstraint) (domain.LearningUnitPlan, error)
}

type AgentPipelineConfig struct {
	Model         string
	PromptVersion string
	ToolVersion   string
	PolicyVersion string
}

// AgentPipeline starts the Agent-only portion of a workflow and reconciles
// completed public runtime actions. It owns no provider, queue, filesystem, or
// database access beyond Service and Reader ports.
type AgentPipeline struct {
	service               *Service
	reader                Reader
	planner               PlanningAgent
	planReviewers         []PlanReviewRole
	generator             BlueprintGenerator
	artifactReviewers     []CandidateReviewRole
	verificationReviewers []VerificationReviewRole
	profiles              RuntimeProfileResolver
	config                AgentPipelineConfig
	now                   func() time.Time
	newRunID              func(string) (string, error)
}

func NewAgentPipeline(service *Service, reader Reader, planner PlanningAgent, planReviewers []PlanReviewRole, generator BlueprintGenerator, artifactReviewers []CandidateReviewRole, verificationReviewers []VerificationReviewRole, profiles RuntimeProfileResolver, cfg AgentPipelineConfig) (*AgentPipeline, error) {
	if service == nil || reader == nil || planner == nil || generator == nil || profiles == nil || len(planReviewers) == 0 || len(artifactReviewers) == 0 || len(verificationReviewers) == 0 || strings.TrimSpace(cfg.Model) == "" || strings.TrimSpace(cfg.PromptVersion) == "" || strings.TrimSpace(cfg.ToolVersion) == "" || strings.TrimSpace(cfg.PolicyVersion) == "" {
		return nil, errors.New("documentation Agent pipeline requires all roles, profile resolver, and audit versions")
	}
	return &AgentPipeline{service: service, reader: reader, planner: planner, planReviewers: append([]PlanReviewRole(nil), planReviewers...), generator: generator, artifactReviewers: append([]CandidateReviewRole(nil), artifactReviewers...), verificationReviewers: append([]VerificationReviewRole(nil), verificationReviewers...), profiles: profiles, config: cfg, now: func() time.Time { return time.Now().UTC() }, newRunID: randomRunID}, nil
}

type PipelineStartResult struct {
	Workflow              domain.Workflow
	MaterializationAction runnable.ActionIdentity
}

// Start reads exactly one requested page/anchor and runs planning, independent
// review, generation, independent artifact review, and deterministic gates.
// It stops before Provider work and returns the immutable materialization key.
func (p *AgentPipeline) Start(ctx context.Context, workflowID, pagePath, anchor string) (PipelineStartResult, error) {
	workflow, err := p.service.Start(ctx, workflowID)
	if err != nil {
		return PipelineStartResult{}, err
	}
	page, err := p.reader.ReadPage(pagePath, anchor)
	if err != nil {
		return PipelineStartResult{}, err
	}
	metadata, err := p.reader.ReadMetadata(pagePath)
	if err != nil {
		return PipelineStartResult{}, err
	}
	evidence := []domain.EvidenceReference{{ID: "page", Kind: domain.EvidencePage, Path: pagePath, Digest: page.Digest, Anchor: anchor, Quote: page.Content}}
	input, err := NewAgentInput("pinned-document-planner", page.Content, evidence)
	if err != nil {
		return PipelineStartResult{}, err
	}
	plannerRun, err := p.newRunID("planner")
	if err != nil {
		return PipelineStartResult{}, err
	}
	plan, err := p.proposePlan(ctx, page, metadata, evidence)
	if err != nil {
		return PipelineStartResult{}, err
	}
	if err := validatePlannedPage(plan, page, pagePath, anchor); err != nil {
		return PipelineStartResult{}, err
	}
	plannerAudit, err := p.audit(plannerRun, "planner", input, plan)
	if err != nil {
		return PipelineStartResult{}, err
	}
	workflow, planArtifact, err := p.service.SubmitPlan(ctx, workflow.ID, plan, plannerAudit)
	if err != nil {
		return PipelineStartResult{}, err
	}
	planOpinions, planAudits, err := p.reviewPlan(ctx, plan)
	if err != nil {
		return PipelineStartResult{}, err
	}
	bundle := ReviewBundle{ArtifactID: planArtifact.ID, ArtifactDigest: planArtifact.Digest, Opinions: planOpinions, CreatedAt: p.now()}
	workflow, planGate, err := p.service.GatePlan(ctx, workflow.ID, plannerRun, planArtifact, bundle, planAudits)
	if err != nil {
		return PipelineStartResult{}, err
	}
	if workflow.State == domain.NoPractice || workflow.State == domain.Rejected {
		return PipelineStartResult{Workflow: workflow}, nil
	}
	if !planGate.Approved() || workflow.State != domain.Generating {
		return PipelineStartResult{}, errors.New("documentation plan did not reach generation")
	}
	profile, err := p.profiles.ResolveDocumentationRuntimeProfile(plan.Runtime)
	if err != nil {
		return PipelineStartResult{}, err
	}
	generatorRun, err := p.newRunID("generator")
	if err != nil {
		return PipelineStartResult{}, err
	}
	blueprint, err := p.generator.Generate(ctx, plan, profile)
	if err != nil {
		return PipelineStartResult{}, err
	}
	candidate, archive, err := CompileCandidate(plan, profile, blueprint, p.now())
	if err != nil {
		return PipelineStartResult{}, err
	}
	generatorAudit, err := p.audit(generatorRun, "generator", struct {
		Plan      domain.LearningUnitPlan `json:"plan"`
		Profile   runnable.RuntimeProfile `json:"profile"`
		Blueprint CandidateBlueprint      `json:"blueprint"`
	}{Plan: plan, Profile: profile, Blueprint: blueprint}, candidate)
	if err != nil {
		return PipelineStartResult{}, err
	}
	workflow, candidateArtifact, err := p.service.SubmitCandidate(ctx, workflow.ID, plan, candidate, archive, generatorAudit)
	if err != nil {
		return PipelineStartResult{}, err
	}
	opinions, audits, err := p.reviewCandidate(ctx, plan, candidate, blueprint.Files)
	if err != nil {
		return PipelineStartResult{}, err
	}
	specDigest, err := candidate.Spec.Digest()
	if err != nil {
		return PipelineStartResult{}, err
	}
	candidateBundle := ArtifactReviewBundle{CandidateID: candidate.ID, CandidateDigest: candidate.Source.Digest, SpecDigest: specDigest, PlanID: plan.ID, PlanRevision: plan.Revision, Opinions: opinions, CreatedAt: p.now()}
	workflow, artifactGate, err := p.service.GateCandidate(ctx, workflow.ID, generatorRun, plan, candidate, candidateArtifact, candidateBundle, audits)
	if err != nil {
		return PipelineStartResult{}, err
	}
	if !artifactGate.Approved() || workflow.State != domain.MaterializingArtifact {
		return PipelineStartResult{Workflow: workflow}, nil
	}
	action, err := p.service.ScheduleMaterialization(ctx, workflow.ID, candidate, archive)
	if err != nil {
		return PipelineStartResult{}, err
	}
	return PipelineStartResult{Workflow: workflow, MaterializationAction: action}, nil
}

func (p *AgentPipeline) proposePlan(ctx context.Context, page domain.Page, metadata domain.Metadata, evidence []domain.EvidenceReference) (domain.LearningUnitPlan, error) {
	if planner, ok := p.planner.(ConstrainedPlanningAgent); ok {
		return planner.ProposeConstrained(ctx, page, metadata, evidence, p.profiles.DocumentationRuntimeConstraints())
	}
	return p.planner.Propose(ctx, page, metadata, evidence)
}

type PipelineReconcileResult struct {
	Workflow           domain.Workflow
	VerificationAction runnable.ActionIdentity
}

// Reconcile completes exactly one public runtime action for an already known
// workflow. Materialization schedules verification; a passed verification runs
// independent review and atomically publishes. A failed verification remains a
// terminal machine result without an Agent override.
func (p *AgentPipeline) Reconcile(ctx context.Context, workflowID string, action runnable.ActionIdentity) (PipelineReconcileResult, error) {
	workflow, err := p.service.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return PipelineReconcileResult{}, err
	}
	plan, candidate, planGate, artifactGate, err := workflowPublicationInputs(workflow)
	if err != nil {
		return PipelineReconcileResult{}, err
	}
	switch action.Phase {
	case runnable.ActionMaterializeArtifact:
		workflow, reference, err := p.service.Materialized(ctx, workflowID, action)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		verification, err := p.service.ScheduleVerification(ctx, workflowID, reference)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		return PipelineReconcileResult{Workflow: workflow, VerificationAction: verification}, nil
	case runnable.ActionVerify:
		workflow, stored, err := p.service.Verified(ctx, workflowID, action)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		if workflow.State == domain.Failed {
			return PipelineReconcileResult{Workflow: workflow}, nil
		}
		opinions, audits, err := p.reviewVerification(ctx, plan, stored.Report)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		reportArtifact, ok := artifactFor(workflow, "verification-report-"+stored.Reference.ID)
		if !ok {
			return PipelineReconcileResult{}, errors.New("verification report is missing from the workflow ledger")
		}
		review := domain.VerificationReviewBundle{ArtifactID: reportArtifact.ID, ArtifactDigest: reportArtifact.Digest, ReportDigest: stored.Reference.Digest, Opinions: opinions, CreatedAt: p.now()}
		workflow, err = p.service.GateVerification(ctx, workflowID, "runtime-worker", stored, review, audits)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		if workflow.State == domain.Rejected {
			return PipelineReconcileResult{Workflow: workflow}, nil
		}
		revisionRef, err := p.service.runnable.ResolveMaterializedRunnableRevision(ctx, actionForMaterialization(candidate, action))
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		workflow, _, err = p.service.Publish(ctx, workflowID, candidate, plan, planGate, artifactGate, revisionRef, stored, review)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		return PipelineReconcileResult{Workflow: workflow}, nil
	default:
		return PipelineReconcileResult{}, errors.New("unsupported documentation runnable action")
	}
}

func (p *AgentPipeline) reviewPlan(ctx context.Context, plan domain.LearningUnitPlan) ([]domain.ReviewOpinion, []domain.AgentAudit, error) {
	opinions := make([]domain.ReviewOpinion, 0, len(p.planReviewers))
	audits := make([]domain.AgentAudit, 0, len(p.planReviewers))
	for _, reviewer := range p.planReviewers {
		runID, err := p.newRunID("plan-review")
		if err != nil {
			return nil, nil, err
		}
		opinion, err := reviewer.ReviewPlan(ctx, runID, plan)
		if err != nil {
			return nil, nil, err
		}
		audit, err := p.audit(runID, "plan-review", plan, opinion)
		if err != nil {
			return nil, nil, err
		}
		opinions, audits = append(opinions, opinion), append(audits, audit)
	}
	return opinions, audits, nil
}

func (p *AgentPipeline) reviewCandidate(ctx context.Context, plan domain.LearningUnitPlan, candidate domain.PracticeCandidate, files []GeneratedFile) ([]domain.ReviewOpinion, []domain.AgentAudit, error) {
	opinions := make([]domain.ReviewOpinion, 0, len(p.artifactReviewers))
	audits := make([]domain.AgentAudit, 0, len(p.artifactReviewers))
	for _, reviewer := range p.artifactReviewers {
		runID, err := p.newRunID("artifact-review")
		if err != nil {
			return nil, nil, err
		}
		opinion, err := reviewer.ReviewCandidate(ctx, runID, plan, candidate, files)
		if err != nil {
			return nil, nil, err
		}
		audit, err := p.audit(runID, "artifact-review", struct {
			Plan      domain.LearningUnitPlan  `json:"plan"`
			Candidate domain.PracticeCandidate `json:"candidate"`
			Files     []GeneratedFile          `json:"files"`
		}{plan, candidate, files}, opinion)
		if err != nil {
			return nil, nil, err
		}
		opinions, audits = append(opinions, opinion), append(audits, audit)
	}
	return opinions, audits, nil
}

func (p *AgentPipeline) reviewVerification(ctx context.Context, plan domain.LearningUnitPlan, report runnable.VerificationReport) ([]domain.ReviewOpinion, []domain.AgentAudit, error) {
	opinions := make([]domain.ReviewOpinion, 0, len(p.verificationReviewers))
	audits := make([]domain.AgentAudit, 0, len(p.verificationReviewers))
	for _, reviewer := range p.verificationReviewers {
		runID, err := p.newRunID("verification-review")
		if err != nil {
			return nil, nil, err
		}
		opinion, err := reviewer.ReviewVerification(ctx, runID, plan, report)
		if err != nil {
			return nil, nil, err
		}
		audit, err := p.audit(runID, "verification-review", struct {
			Plan   domain.LearningUnitPlan     `json:"plan"`
			Report runnable.VerificationReport `json:"report"`
		}{plan, report}, opinion)
		if err != nil {
			return nil, nil, err
		}
		opinions, audits = append(opinions, opinion), append(audits, audit)
	}
	return opinions, audits, nil
}

func (p *AgentPipeline) audit(runID, role string, input, output any) (domain.AgentAudit, error) {
	inputDigest, err := domain.DigestAgentInput(input)
	if err != nil {
		return domain.AgentAudit{}, err
	}
	outputDigest, err := DigestJSON(output)
	if err != nil {
		return domain.AgentAudit{}, err
	}
	return domain.AgentAudit{RunID: runID, Role: role, Model: p.config.Model, PromptVersion: p.config.PromptVersion, ToolVersion: p.config.ToolVersion, PolicyVersion: p.config.PolicyVersion, InputDigest: inputDigest, OutputDigest: outputDigest, CreatedAt: p.now()}, nil
}

func validatePlannedPage(plan domain.LearningUnitPlan, page domain.Page, pagePath, anchor string) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.Context != page.Context || plan.Context.PagePath != pagePath || plan.Context.Anchor != anchor {
		return errors.New("planner changed the pinned page context")
	}
	return nil
}

func workflowPublicationInputs(workflow domain.Workflow) (domain.LearningUnitPlan, domain.PracticeCandidate, domain.GateResult, domain.GateResult, error) {
	var plan domain.LearningUnitPlan
	var candidate domain.PracticeCandidate
	var planGate domain.GateResult
	var artifactGate domain.GateResult
	for _, artifact := range workflow.Artifacts {
		switch artifact.Kind {
		case "learning-unit-plan":
			_ = json.Unmarshal(artifact.Payload, &plan)
		case "practice-candidate":
			_ = json.Unmarshal(artifact.Payload, &candidate)
		case "plan-gate":
			_ = json.Unmarshal(artifact.Payload, &planGate)
		case "artifact-gate":
			_ = json.Unmarshal(artifact.Payload, &artifactGate)
		}
	}
	if err := plan.Validate(); err != nil || candidate.Validate() != nil || !planGate.Approved() || !artifactGate.Approved() {
		return domain.LearningUnitPlan{}, domain.PracticeCandidate{}, domain.GateResult{}, domain.GateResult{}, errors.New("documentation workflow has incomplete publication inputs")
	}
	return plan, candidate, planGate, artifactGate, nil
}

func artifactFor(workflow domain.Workflow, id string) (domain.ArtifactRecord, bool) {
	for _, artifact := range workflow.Artifacts {
		if artifact.ID == id {
			return artifact, true
		}
	}
	return domain.ArtifactRecord{}, false
}

func actionForMaterialization(candidate domain.PracticeCandidate, verify runnable.ActionIdentity) runnable.ActionIdentity {
	digest, _ := candidate.Spec.Digest()
	return runnable.ActionIdentity{Content: candidate.Spec.Identity, SpecDigest: digest, Phase: runnable.ActionMaterializeArtifact, StateVersion: verify.StateVersion - 1}
}

func randomRunID(prefix string) (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(bytes), nil
}
