package documentpractice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
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
	Generate(context.Context, domain.LearningUnitPlan, runnable.RuntimeProfile, runnable.LifecyclePolicy) (CandidateBlueprint, error)
}

type RuntimeProfileResolver interface {
	DocumentationRuntimeConstraints() []domain.RuntimeConstraint
	DocumentationLifecyclePolicy() runnable.LifecyclePolicy
	ResolveDocumentationRuntimeProfile(domain.RuntimeConstraint) (runnable.RuntimeProfile, error)
}

// ConstrainedPlanningAgent receives only Server-resolved runtime choices. The
// compatibility-free fallback is used by focused tests and cannot be used by
// the production DocumentPlanner adapter.
type ConstrainedPlanningAgent interface {
	ProposeConstrained(context.Context, domain.Page, domain.Metadata, []domain.EvidenceReference, []domain.RuntimeConstraint) (domain.LearningUnitPlan, error)
}

// GateFeedback is the trusted, server-stamped rejection record of a previous
// attempt, replayed into the next planner and generator prompts. It mirrors
// the operations side's last_error contract: only gate reasons travel - never
// document text - so the untrusted-data principle is untouched.
type GateFeedback struct {
	// Gate names the rejecting ledger gate kind: plan-gate or artifact-gate.
	Gate string `json:"gate"`
	// Attempt is the workflow revision that was rejected.
	Attempt int64    `json:"attempt"`
	Reasons []string `json:"reasons"`
}

// FeedbackPlanningAgent is the feedback-aware planner capability. The pipeline
// discovers it by type assertion, so an adapter receives rejection feedback
// only by implementing the interface explicitly - and when it does not, the
// retry loop still restarts, just without prompt feedback.
type FeedbackPlanningAgent interface {
	ProposeWithFeedback(context.Context, domain.Page, domain.Metadata, []domain.EvidenceReference, []domain.RuntimeConstraint, []GateFeedback) (domain.LearningUnitPlan, error)
}

// FeedbackBlueprintGenerator is the feedback-aware generator capability, with
// the same explicit-capability semantics as FeedbackPlanningAgent.
type FeedbackBlueprintGenerator interface {
	GenerateWithFeedback(context.Context, domain.LearningUnitPlan, runnable.RuntimeProfile, runnable.LifecyclePolicy, []GateFeedback) (CandidateBlueprint, error)
}

type AgentPipelineConfig struct {
	Model         string
	PromptVersion string
	ToolVersion   string
	PolicyVersion string
}

// AgentPipeline starts the Agent-only portion of a workflow and reconciles
// completed public runnable actions. It owns no provider, queue, filesystem, or
// database access beyond Service and Reader ports.
type AgentPipeline struct {
	service *Service
	// OnTick optionally reports each reconciliation pass to the bootstrap's
	// in-memory service registry.
	OnTick                func(error)
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

const pipelineRecoveryInterval = 5 * time.Second

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

// Prepare validates the requested page/anchor and creates the workflow with
// its page identity and the administrative ignition audit durably - without
// running any Agent work. The chain itself is a separate Start invocation:
// asynchronous ignitions (the HTTP endpoint) Prepare durably, acknowledge with
// the workflow's state, and drive Start on a background goroutine, exactly
// like the batch scheduler's ignite.
func (p *AgentPipeline) Prepare(ctx context.Context, workflowID, pagePath, anchor string, ignition *audit.HumanAction) (domain.Workflow, error) {
	metadata, err := p.reader.ReadMetadata(pagePath)
	if err != nil {
		return domain.Workflow{}, err
	}
	if !metadataContainsAnchor(metadata, anchor) {
		return domain.Workflow{}, errors.New("documentation anchor is absent from the pinned page")
	}
	identity := domain.WorkflowPageIdentity{SourceID: metadata.Context.SourceID, Commit: metadata.Context.Commit, Language: metadata.Context.Language, PagePath: pagePath, Anchor: anchor}
	return p.service.Start(ctx, workflowID, identity, ignition)
}

// Start reads exactly one requested page/anchor and runs planning, independent
// review, generation, independent artifact review, and deterministic gates.
// It stops before Provider work and returns the immutable materialization key.
// The administrative ignition action, when supplied, is recorded together
// with the workflow creation. The library metadata supplies the page identity
// persisted alongside the workflow for corpus aggregation.
//
// A gate rejection is not terminal while the automatic retry budget lasts:
// the pipeline internally restarts the workflow (the same RestartAt machinery
// an administrator triggers, recorded as a system decision) and re-runs the
// chain in this same invocation. Hard rejections and exhausted budgets keep
// the Rejected terminal for the administrator's rescue verbs.
func (p *AgentPipeline) Start(ctx context.Context, workflowID, pagePath, anchor string, ignition *audit.HumanAction) (PipelineStartResult, error) {
	metadata, err := p.reader.ReadMetadata(pagePath)
	if err != nil {
		return PipelineStartResult{}, err
	}
	identity := domain.WorkflowPageIdentity{SourceID: metadata.Context.SourceID, Commit: metadata.Context.Commit, Language: metadata.Context.Language, PagePath: pagePath, Anchor: anchor}
	workflow, err := p.service.Start(ctx, workflowID, identity, ignition)
	if err != nil {
		return PipelineStartResult{}, err
	}
	// The workflow ID is deployment-owned and identifies one exact pinned page.
	// A repeated request observes its durable state; it must not re-run any Agent
	// role or create a second public runnable action.
	if workflow.State != domain.Planning {
		return PipelineStartResult{Workflow: workflow}, nil
	}
	page, err := p.reader.ReadPage(pagePath, anchor)
	if err != nil {
		return PipelineStartResult{}, err
	}
	if !metadataContainsAnchor(metadata, anchor) {
		return PipelineStartResult{}, errors.New("documentation anchor is absent from the pinned page")
	}
	evidence := []domain.EvidenceReference{{ID: "page", Kind: domain.EvidencePage, Path: pagePath, Digest: page.Digest, Anchor: anchor, Quote: page.Content}}
	input, err := NewAgentInput("pinned-document-planner", page.Content, evidence)
	if err != nil {
		return PipelineStartResult{}, err
	}
	// A re-ignited restarted workflow resumes with the feedback context the
	// interrupted loop would have carried: the rejection reasons are rebuilt
	// from the immutable ledger, never from process memory.
	feedback := gateFeedbackFromLedger(workflow)
	for {
		result, rejection, err := p.attempt(ctx, workflow, page, metadata, evidence, input, pagePath, anchor, feedback)
		if err != nil || rejection == nil {
			return result, err
		}
		restarted, retry, err := p.service.AutoRestart(ctx, rejection.workflow.ID, rejection.gateKind, rejection.gate, rejection.workflow)
		if err != nil {
			return PipelineStartResult{}, err
		}
		if !retry && restarted.State != domain.Planning {
			// A hard rejection, an exhausted budget, or an administrator who
			// won the race and resolved the workflow differently: the durable
			// state is the answer.
			return PipelineStartResult{Workflow: restarted}, nil
		}
		// The restarted attempt re-plans into fresh, attempt-namespaced ledger
		// entries; revision strictly advances per restart, so the loop is
		// bounded by max_revisions. The rejection reasons ride along as the
		// next attempt's prompt feedback - the equivalent of the operations
		// side's last_error re-entry.
		feedback = []GateFeedback{{Gate: rejection.gateKind, Attempt: rejection.workflow.Revision, Reasons: rejection.gate.Reasons}}
		workflow = restarted
	}
}

// gateRejection is a gate refusal that may still re-enter the retry budget.
type gateRejection struct {
	gateKind string
	gate     domain.GateResult
	workflow domain.Workflow
}

// attempt runs one full Agent chain from Planning. It returns either the
// chain's outcome or the gate rejection that ended it, never both. The
// feedback parameter carries the previous attempt's gate rejection into the
// planner and generator prompts when those roles have the capability.
func (p *AgentPipeline) attempt(ctx context.Context, workflow domain.Workflow, page domain.Page, metadata domain.Metadata, evidence []domain.EvidenceReference, input AgentInput, pagePath, anchor string, feedback []GateFeedback) (PipelineStartResult, *gateRejection, error) {
	plannerRun, err := p.newRunID("planner")
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	plan, err := p.proposePlan(ctx, page, metadata, evidence, feedback)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	if err := validatePlannedPage(plan, page, pagePath, anchor); err != nil {
		return PipelineStartResult{}, nil, err
	}
	plannerAudit, err := p.audit(plannerRun, "planner", input, plan)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	workflow, planArtifact, err := p.service.SubmitPlan(ctx, workflow.ID, plan, plannerAudit)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	planOpinions, planAudits, err := p.reviewPlan(ctx, plan)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	bundle := ReviewBundle{ArtifactID: planArtifact.ID, ArtifactDigest: planArtifact.Digest, Opinions: planOpinions, CreatedAt: p.now()}
	workflow, planGate, err := p.service.GatePlan(ctx, workflow.ID, plannerRun, planArtifact, bundle, planAudits)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	if workflow.State == domain.NoPractice {
		return PipelineStartResult{Workflow: workflow}, nil, nil
	}
	if workflow.State == domain.Rejected {
		return PipelineStartResult{}, &gateRejection{gateKind: "plan-gate", gate: planGate, workflow: workflow}, nil
	}
	if !planGate.Approved() || workflow.State != domain.Generating {
		return PipelineStartResult{}, nil, errors.New("documentation plan did not reach generation")
	}
	profile, err := p.profiles.ResolveDocumentationRuntimeProfile(plan.Runtime)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	generatorRun, err := p.newRunID("generator")
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	lifecycle := p.profiles.DocumentationLifecyclePolicy()
	blueprint, err := p.generateBlueprint(ctx, plan, profile, lifecycle, feedback)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	candidate, archive, err := CompileCandidate(plan, profile, lifecycle, blueprint, p.now())
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	generatorAudit, err := p.audit(generatorRun, "generator", struct {
		Plan      domain.LearningUnitPlan `json:"plan"`
		Profile   runnable.RuntimeProfile `json:"profile"`
		Blueprint CandidateBlueprint      `json:"blueprint"`
	}{Plan: plan, Profile: profile, Blueprint: blueprint}, candidate)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	workflow, candidateArtifact, err := p.service.SubmitCandidate(ctx, workflow.ID, plan, candidate, archive, generatorAudit)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	opinions, audits, err := p.reviewCandidate(ctx, plan, candidate, blueprint.Files)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	specDigest, err := candidate.Spec.Digest()
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	candidateBundle := ArtifactReviewBundle{CandidateID: candidate.ID, CandidateDigest: candidate.Source.Digest, SpecDigest: specDigest, PlanID: plan.ID, PlanRevision: plan.Revision, Opinions: opinions, CreatedAt: p.now()}
	workflow, artifactGate, err := p.service.GateCandidate(ctx, workflow.ID, generatorRun, plan, candidate, candidateArtifact, candidateBundle, audits)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	if workflow.State == domain.Rejected {
		return PipelineStartResult{}, &gateRejection{gateKind: "artifact-gate", gate: artifactGate, workflow: workflow}, nil
	}
	if !artifactGate.Approved() || workflow.State != domain.MaterializingArtifact {
		return PipelineStartResult{Workflow: workflow}, nil, nil
	}
	action, err := p.service.ScheduleMaterialization(ctx, workflow.ID, candidate, archive)
	if err != nil {
		return PipelineStartResult{}, nil, err
	}
	return PipelineStartResult{Workflow: workflow, MaterializationAction: action}, nil, nil
}

func (p *AgentPipeline) proposePlan(ctx context.Context, page domain.Page, metadata domain.Metadata, evidence []domain.EvidenceReference, feedback []GateFeedback) (domain.LearningUnitPlan, error) {
	constraints := p.profiles.DocumentationRuntimeConstraints()
	if planner, ok := p.planner.(FeedbackPlanningAgent); ok && len(feedback) > 0 {
		return planner.ProposeWithFeedback(ctx, page, metadata, evidence, constraints, feedback)
	}
	if planner, ok := p.planner.(ConstrainedPlanningAgent); ok {
		return planner.ProposeConstrained(ctx, page, metadata, evidence, constraints)
	}
	return p.planner.Propose(ctx, page, metadata, evidence)
}

// generateBlueprint routes generation through the feedback-aware capability
// when the generator implements it and a previous attempt left feedback. The
// Server-resolved profile and lifecycle are identical on both paths.
func (p *AgentPipeline) generateBlueprint(ctx context.Context, plan domain.LearningUnitPlan, profile runnable.RuntimeProfile, lifecycle runnable.LifecyclePolicy, feedback []GateFeedback) (CandidateBlueprint, error) {
	if generator, ok := p.generator.(FeedbackBlueprintGenerator); ok && len(feedback) > 0 {
		return generator.GenerateWithFeedback(ctx, plan, profile, lifecycle, feedback)
	}
	return p.generator.Generate(ctx, plan, profile, lifecycle)
}

// gateFeedbackFromLedger rebuilds the rejection feedback of the immediately
// previous attempt from the immutable ledger - the recovery path for a retry
// loop interrupted between the restart commit and the next attempt's ignition.
// An approved or absent gate in that attempt means no gate rejected it (a
// fresh workflow, a verification failure, or an administrative restart), and
// no feedback applies.
func gateFeedbackFromLedger(workflow domain.Workflow) []GateFeedback {
	if workflow.Revision < 2 {
		return nil
	}
	suffix := fmt.Sprintf("-a%d", workflow.Revision-1)
	var newest domain.ArtifactRecord
	var newestGate domain.GateResult
	for _, artifact := range workflow.Artifacts {
		if (artifact.Kind != "plan-gate" && artifact.Kind != "artifact-gate") || !strings.HasSuffix(artifact.ID, suffix) {
			continue
		}
		var gate domain.GateResult
		if err := json.Unmarshal(artifact.Payload, &gate); err != nil || gate.Approved() {
			continue
		}
		if newest.ID == "" || !artifact.CreatedAt.Before(newest.CreatedAt) {
			newest, newestGate = artifact, gate
		}
	}
	if newest.ID == "" {
		return nil
	}
	return []GateFeedback{{Gate: newest.Kind, Attempt: workflow.Revision - 1, Reasons: newestGate.Reasons}}
}

type PipelineReconcileResult struct {
	Workflow           domain.Workflow
	VerificationAction runnable.ActionIdentity
}

// ForceFail and Restart expose the administrative verbs through the pipeline
// without granting access to the underlying service or store.
func (p *AgentPipeline) ForceFail(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (domain.Workflow, error) {
	return p.service.ForceFail(ctx, workflowID, reason, action)
}

func (p *AgentPipeline) Restart(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (domain.Workflow, error) {
	return p.service.Restart(ctx, workflowID, reason, action)
}

// Reconcile completes exactly one public runnable action for an already known
// workflow. Materialization schedules verification; a passed verification runs
// independent review and atomically publishes. A failed verification remains a
// terminal machine result without an Agent override.
func (p *AgentPipeline) Reconcile(ctx context.Context, workflowID string, action runnable.ActionIdentity) (PipelineReconcileResult, error) {
	workflow, err := p.service.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return PipelineReconcileResult{}, err
	}
	if workflow.State.Terminal() {
		return PipelineReconcileResult{Workflow: workflow}, nil
	}
	switch action.Phase {
	case runnable.ActionMaterializeArtifact:
		return p.reconcileMaterialization(ctx, workflowID, workflow, action)
	case runnable.ActionVerify:
		return p.reconcileVerification(ctx, workflowID, workflow, action)
	default:
		return PipelineReconcileResult{}, errors.New("unsupported documentation runnable action")
	}
}

// reconcileMaterialization resumes at the durable state reached before an
// interruption. The Worker result is immutable, so the only possible follow-up
// is the same verification action bound to the same workflow state version.
func (p *AgentPipeline) reconcileMaterialization(ctx context.Context, workflowID string, workflow domain.Workflow, action runnable.ActionIdentity) (PipelineReconcileResult, error) {
	var reference runnable.RevisionReference
	var err error
	switch workflow.State {
	case domain.MaterializingArtifact:
		workflow, reference, err = p.service.Materialized(ctx, workflowID, action)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
	case domain.Verifying, domain.VerificationReviewing, domain.Publishing:
		reference, err = p.service.runnable.ResolveMaterializedRunnableRevision(ctx, action)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
	default:
		return PipelineReconcileResult{}, fmt.Errorf("cannot reconcile materialization while documentation workflow is %s", workflow.State)
	}
	if workflow.State != domain.Verifying {
		return PipelineReconcileResult{Workflow: workflow}, nil
	}
	verification, err := p.service.ScheduleVerification(ctx, workflowID, reference)
	if err != nil {
		return PipelineReconcileResult{}, err
	}
	return PipelineReconcileResult{Workflow: workflow, VerificationAction: verification}, nil
}

// reconcileVerification never re-executes the Worker result. It resumes
// review or publication from the append-only ledger after a Server restart.
func (p *AgentPipeline) reconcileVerification(ctx context.Context, workflowID string, workflow domain.Workflow, action runnable.ActionIdentity) (PipelineReconcileResult, error) {
	stored, err := p.service.runnable.ResolveVerificationForAction(ctx, action)
	if err != nil {
		return PipelineReconcileResult{}, err
	}
	if workflow.State == domain.Verifying {
		workflow, stored, err = p.service.Verified(ctx, workflowID, action)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
	}
	if workflow.State == domain.Failed {
		return PipelineReconcileResult{Workflow: workflow}, nil
	}
	if workflow.State != domain.VerificationReviewing && workflow.State != domain.Publishing {
		return PipelineReconcileResult{}, fmt.Errorf("cannot reconcile verification while documentation workflow is %s", workflow.State)
	}
	plan, candidate, planGate, artifactGate, err := workflowPublicationInputs(workflow)
	if err != nil {
		return PipelineReconcileResult{}, err
	}
	var review domain.VerificationReviewBundle
	if workflow.State == domain.VerificationReviewing {
		opinions, audits, err := p.reviewVerification(ctx, plan, stored.Report)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		reportArtifact, ok := artifactFor(workflow, "verification-report-"+stored.Reference.ID)
		if !ok {
			return PipelineReconcileResult{}, errors.New("verification report is missing from the workflow ledger")
		}
		review = domain.VerificationReviewBundle{ArtifactID: reportArtifact.ID, ArtifactDigest: reportArtifact.Digest, ReportDigest: stored.Reference.Digest, Opinions: opinions, CreatedAt: p.now()}
		workflow, err = p.service.GateVerification(ctx, workflowID, "runtime-worker", stored, review, audits)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
		if workflow.State == domain.Rejected {
			return PipelineReconcileResult{Workflow: workflow}, nil
		}
	} else {
		review, err = verificationReviewFromLedger(workflow, stored)
		if err != nil {
			return PipelineReconcileResult{}, err
		}
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
}

// ReconcileCompletedAction routes one completed public runnable action through
// its durable documentation binding. An unrelated content kind is a no-op.
//
// A completion is acknowledged only (binding marked reconciled, no state
// change, logged) when the workflow can never act on it: it reached a terminal
// state such as an administrative force-fail, it sits in Planning — the state
// a restart resets to and a state that never binds runnable actions — or the
// action's phase belongs to an earlier lineage than the workflow's current
// state. Otherwise the pipeline resumes: the workflow may legitimately have
// advanced past earlier steps of this same action before an interruption, and
// every state advance stays fenced by the store's state-version check.
func (p *AgentPipeline) ReconcileCompletedAction(ctx context.Context, action runnable.ActionIdentity) error {
	workflowID, found, err := p.service.WorkflowForRunnableAction(ctx, action)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	workflow, err := p.service.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return err
	}
	if stale, reason := staleRunnableCompletion(workflow, action); stale {
		slog.Warn("late or stale documentation runnable action completion is acknowledged without advancing",
			"reason", reason,
			"workflow_id", workflowID, "workflow_state", string(workflow.State),
			"workflow_state_version", workflow.StateVersion, "action_state_version", action.StateVersion,
			"action_key", action.Key())
		return p.service.MarkRunnableActionReconciled(ctx, action)
	}
	if _, err := p.Reconcile(ctx, workflowID, action); err != nil {
		return err
	}
	return p.service.MarkRunnableActionReconciled(ctx, action)
}

// staleRunnableCompletion decides, read-only, whether a completed action can
// no longer advance its workflow.
func staleRunnableCompletion(workflow domain.Workflow, action runnable.ActionIdentity) (bool, string) {
	if workflow.State.Terminal() {
		return true, "workflow_terminal"
	}
	if workflow.State == domain.Planning {
		return true, "workflow_restarted"
	}
	if action.Phase == runnable.ActionMaterializeArtifact && workflow.State == domain.MaterializingArtifact && action.StateVersion != workflow.StateVersion {
		return true, "materialization_superseded"
	}
	earlyStates := []domain.WorkflowState{domain.PlanReviewing, domain.Generating, domain.ArtifactReviewing, domain.MaterializingArtifact}
	if action.Phase == runnable.ActionVerify {
		for _, state := range earlyStates {
			if workflow.State == state {
				return true, "verification_premature"
			}
		}
	}
	return false, ""
}

// Recover reconciles completed actions left unacknowledged after a Server
// interruption. It never claims or executes Provider work.
func (p *AgentPipeline) Recover(ctx context.Context) error {
	actions, err := p.service.CompletedUnreconciledRunnableActions(ctx)
	if err != nil {
		return err
	}
	for _, action := range actions {
		if err := p.ReconcileCompletedAction(ctx, action); err != nil {
			return err
		}
	}
	return nil
}

// Run retries post-completion product reconciliation and runs the watchdog
// on every tick. It never executes a runnable action: Worker ownership and
// public result persistence remain outside this loop.
func (p *AgentPipeline) Run(ctx context.Context) error {
	if p == nil {
		return errors.New("documentation Agent pipeline is not configured")
	}
	ticker := time.NewTicker(pipelineRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := p.tick(ctx)
			if p.OnTick != nil {
				p.OnTick(err)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("reconcile and watchdog documentation workflows", "err", err)
			}
		}
	}
}

func (p *AgentPipeline) tick(ctx context.Context) error {
	if err := p.Recover(ctx); err != nil {
		return err
	}
	return p.Watchdog(ctx)
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

func metadataContainsAnchor(metadata domain.Metadata, anchor string) bool {
	if strings.TrimSpace(anchor) == "" {
		return true
	}
	for _, value := range metadata.Anchors {
		if value == anchor {
			return true
		}
	}
	return false
}

func workflowPublicationInputs(workflow domain.Workflow) (domain.LearningUnitPlan, domain.PracticeCandidate, domain.GateResult, domain.GateResult, error) {
	var plan domain.LearningUnitPlan
	var candidate domain.PracticeCandidate
	var planGate domain.GateResult
	var artifactGate domain.GateResult
	// Every chain artifact is namespaced by its workflow attempt; a restarted
	// workflow carries several attempts, and publication must bind to the
	// current one.
	attempt := fmt.Sprintf("-a%d", workflow.Revision)
	for _, artifact := range workflow.Artifacts {
		if !strings.HasSuffix(artifact.ID, attempt) {
			continue
		}
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

func verificationReviewFromLedger(workflow domain.Workflow, report runnable.StoredVerificationReport) (domain.VerificationReviewBundle, error) {
	artifact, found := artifactFor(workflow, "verification-review-"+report.Reference.ID)
	if !found || artifact.Kind != "verification-review" {
		return domain.VerificationReviewBundle{}, errors.New("verification review is missing from the workflow ledger")
	}
	var review domain.VerificationReviewBundle
	if err := json.Unmarshal(artifact.Payload, &review); err != nil {
		return domain.VerificationReviewBundle{}, errors.New("verification review ledger artifact is invalid")
	}
	if err := review.Validate(report.Report); err != nil {
		return domain.VerificationReviewBundle{}, errors.New("verification review ledger binding is invalid")
	}
	return review, nil
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
