package documentpractice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// Store is the complete durable boundary for the documentation product. It is
// intentionally independent of Operations repositories and never exposes an
// update operation for ledger artifacts or practice revisions.
type Store interface {
	// CreateWorkflow durably creates the workflow and, when an administrative
	// action is supplied, its human audit row in the same transaction.
	CreateWorkflow(context.Context, domain.Workflow, *audit.HumanAction) error
	RecordHumanAction(context.Context, audit.HumanAction) error
	GetWorkflow(context.Context, string) (domain.Workflow, error)
	AppendArtifact(context.Context, string, domain.ArtifactRecord) error
	AdvanceWorkflow(context.Context, string, int64, domain.WorkflowState, time.Time, ...string) (domain.Workflow, error)
	SaveAgentAudit(context.Context, string, domain.AgentAudit) error
	BindRunnableAction(context.Context, string, runnable.ActionIdentity, time.Time) error
	WorkflowForRunnableAction(context.Context, runnable.ActionIdentity) (string, bool, error)
	ListCompletedUnreconciledRunnableActions(context.Context) ([]runnable.ActionIdentity, error)
	MarkRunnableActionReconciled(context.Context, runnable.ActionIdentity, time.Time) error
	PublishPracticeRevision(context.Context, string, int64, domain.PracticeRevision, domain.PublicationManifest, time.Time) (domain.Workflow, error)
	// ForceFailWorkflow and RestartWorkflow are the administrative escape
	// hatches. Each commits the state change, its ledger entry, and the human
	// action audit in one transaction.
	ForceFailWorkflow(context.Context, string, string, *audit.HumanAction, time.Time) (domain.Workflow, error)
	RestartWorkflow(context.Context, string, string, *audit.HumanAction, time.Time) (domain.Workflow, error)
	// ListWorkflowWatchdogCandidates returns every non-terminal workflow whose
	// bound public runnable action already failed or exhausted its attempts.
	ListWorkflowWatchdogCandidates(context.Context, time.Time) ([]WatchdogCandidate, error)
	// WatchdogFailWorkflow maps one stranded workflow onto Failed as a system
	// decision: the state fence and the watchdog ledger entry commit together,
	// and no human action audit row is written.
	WatchdogFailWorkflow(context.Context, string, string, int64, time.Time) (domain.Workflow, error)
}

type RunnableStore interface {
	StoreRunnableSource(context.Context, runnable.SourceArchive, []byte, time.Time) error
	ScheduleMaterialization(context.Context, runnable.RunnableSpec, int64, time.Time) (runnable.ActionIdentity, error)
	ResolveMaterializedRunnableRevision(context.Context, runnable.ActionIdentity) (runnable.RevisionReference, error)
	ScheduleVerification(context.Context, runnable.RevisionReference, int64, time.Time) (runnable.ActionIdentity, error)
	ResolveVerificationForAction(context.Context, runnable.ActionIdentity) (runnable.StoredVerificationReport, error)
	ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error)
}

type Service struct {
	store    Store
	runnable RunnableStore
	now      func() time.Time
}

func NewService(store Store, runnableStore RunnableStore) (*Service, error) {
	if store == nil || runnableStore == nil {
		return nil, errors.New("documentation practice service requires durable workflow and runnable stores")
	}
	return &Service{store: store, runnable: runnableStore, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Start creates the workflow and records the administrative ignition in one
// transaction. A repeated start request observes the durable workflow at
// every stage: the replay returns the stored state without a second audit
// row, because no state change took place.
func (s *Service) Start(ctx context.Context, workflowID string, action *audit.HumanAction) (domain.Workflow, error) {
	workflow, err := domain.NewWorkflow(workflowID, s.now())
	if err != nil {
		return domain.Workflow{}, err
	}
	if err := s.store.CreateWorkflow(ctx, workflow, action); err != nil {
		// Workflow identity is caller supplied. Repeating a start request must
		// return the durable workflow at every stage, never restart its Agent
		// work or replace immutable ledger entries.
		stored, getErr := s.store.GetWorkflow(ctx, workflowID)
		if getErr == nil {
			return stored, nil
		}
		return domain.Workflow{}, err
	}
	return workflow, nil
}

// SubmitPlan stores only the structured planner output and audit metadata.
// Page bytes are neither copied into the ledger nor forwarded to later roles.
func (s *Service) SubmitPlan(ctx context.Context, workflowID string, plan domain.LearningUnitPlan, audit domain.AgentAudit) (domain.Workflow, domain.ArtifactRecord, error) {
	if err := plan.Validate(); err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if audit.Role != "planner" {
		return domain.Workflow{}, domain.ArtifactRecord{}, errors.New("plan submission requires a planner AgentRun")
	}
	payload, digest, err := artifactPayload(plan)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if audit.OutputDigest != digest {
		return domain.Workflow{}, domain.ArtifactRecord{}, errors.New("planner audit does not bind the submitted plan")
	}
	// The plan artifact is namespaced by the workflow revision that produced
	// it: a restarted workflow re-plans into fresh ledger entries instead of
	// colliding with the immutable artifacts of its failed attempt.
	current, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	attempt := current.Revision
	artifact := domain.ArtifactRecord{ID: fmt.Sprintf("plan-%s-r%d-a%d", plan.ID, plan.Revision, attempt), Kind: "learning-unit-plan", ContentRevision: fmt.Sprintf("%d", plan.Revision), Digest: digest, SchemaVersion: domain.FormatVersion, OwnerRole: "planner", PolicyVersion: audit.PolicyVersion, CreatedAt: s.now(), Payload: payload}
	contextPayload, contextDigest, err := artifactPayload(plan.Context)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	contextArtifact := domain.ArtifactRecord{
		ID:              "document-context-" + domain.ContentID(plan.Context),
		Kind:            "document-context",
		ContentRevision: plan.Context.Commit,
		Digest:          contextDigest,
		SchemaVersion:   domain.FormatVersion,
		OwnerRole:       "server",
		PolicyVersion:   audit.PolicyVersion,
		CreatedAt:       s.now(),
		Payload:         contextPayload,
	}
	artifact.ParentID = contextArtifact.ID
	workflow, replay, err := s.readyFor(ctx, workflowID, domain.Planning, domain.PlanReviewing, artifact)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if replay {
		return workflow, artifact, nil
	}
	if err := s.store.AppendArtifact(ctx, workflowID, contextArtifact); err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if err := s.store.AppendArtifact(ctx, workflowID, artifact); err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if err := s.store.SaveAgentAudit(ctx, workflowID, audit); err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	workflow, err = s.advanceFrom(ctx, workflow, domain.PlanReviewing, artifact.Kind)
	return workflow, artifact, err
}

func (s *Service) GatePlan(ctx context.Context, workflowID, producerRunID string, planArtifact domain.ArtifactRecord, bundle ReviewBundle, audits []domain.AgentAudit) (domain.Workflow, domain.GateResult, error) {
	if planArtifact.Kind != "learning-unit-plan" || bundle.ArtifactID != planArtifact.ID || bundle.ArtifactDigest != planArtifact.Digest {
		return domain.Workflow{}, domain.GateResult{}, errors.New("plan review does not bind the current plan")
	}
	if err := ValidateReviewIndependence(producerRunID, bundle); err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	if err := s.saveReviewAudits(ctx, workflowID, audits, bundle.Opinions, "plan-review"); err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	gate, err := Gate(bundle, "evidence", "value")
	if err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	payload, digest, err := artifactPayload(gate)
	if err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	gateArtifact := domain.ArtifactRecord{ID: "plan-gate-" + planArtifact.ID, ParentID: planArtifact.ID, Kind: "plan-gate", ContentRevision: planArtifact.ContentRevision, Digest: digest, SchemaVersion: domain.FormatVersion, OwnerRole: "server", PolicyVersion: gate.PolicyVersion, CreatedAt: s.now(), Payload: payload}
	next := domain.Generating
	if !gate.Approved() {
		next = domain.Rejected
	} else {
		var plan domain.LearningUnitPlan
		if err := json.Unmarshal(planArtifact.Payload, &plan); err != nil || plan.Validate() != nil {
			return domain.Workflow{}, domain.GateResult{}, errors.New("plan ledger artifact is invalid")
		}
		if plan.NoPractice {
			next = domain.NoPractice
		}
	}
	workflow, replay, err := s.readyFor(ctx, workflowID, domain.PlanReviewing, next, gateArtifact)
	if err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	if replay {
		return workflow, gate, nil
	}
	if err := s.store.AppendArtifact(ctx, workflowID, gateArtifact); err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	workflow, err = s.advanceFrom(ctx, workflow, next, gateArtifact.Kind)
	return workflow, gate, err
}

// SubmitCandidate freezes exact archive bytes before it schedules no provider
// work. Candidate review remains a separate state and cannot be skipped.
func (s *Service) SubmitCandidate(ctx context.Context, workflowID string, plan domain.LearningUnitPlan, candidate domain.PracticeCandidate, archive []byte, audit domain.AgentAudit) (domain.Workflow, domain.ArtifactRecord, error) {
	if audit.Role != "generator" {
		return domain.Workflow{}, domain.ArtifactRecord{}, errors.New("candidate submission requires a generator AgentRun")
	}
	frozen, err := FreezeCandidate(candidate, archive)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if err := ValidateCandidateAgainstPlan(frozen.Candidate, plan); err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	payload, digest, err := artifactPayload(frozen.Candidate)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if audit.OutputDigest != digest {
		return domain.Workflow{}, domain.ArtifactRecord{}, errors.New("generator audit does not bind the submitted candidate")
	}
	current, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	attempt := current.Revision
	planArtifactID := fmt.Sprintf("plan-%s-r%d-a%d", plan.ID, plan.Revision, attempt)
	artifact := domain.ArtifactRecord{ID: fmt.Sprintf("candidate-%s-r%d-a%d", candidate.ID, candidate.Revision, attempt), ParentID: planArtifactID, Kind: "practice-candidate", ContentRevision: fmt.Sprintf("%d", candidate.Revision), Digest: frozen.ArchiveDigest, SchemaVersion: domain.FormatVersion, OwnerRole: "generator", PolicyVersion: audit.PolicyVersion, CreatedAt: s.now(), Payload: payload}
	workflow, replay, err := s.readyFor(ctx, workflowID, domain.Generating, domain.ArtifactReviewing, artifact)
	if err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if replay {
		return workflow, artifact, nil
	}
	if err := s.store.AppendArtifact(ctx, workflowID, artifact); err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	if err := s.store.SaveAgentAudit(ctx, workflowID, audit); err != nil {
		return domain.Workflow{}, domain.ArtifactRecord{}, err
	}
	workflow, err = s.advanceFrom(ctx, workflow, domain.ArtifactReviewing, artifact.Kind)
	return workflow, artifact, err
}

func (s *Service) GateCandidate(ctx context.Context, workflowID, producerRunID string, plan domain.LearningUnitPlan, candidate domain.PracticeCandidate, artifact domain.ArtifactRecord, bundle ArtifactReviewBundle, audits []domain.AgentAudit) (domain.Workflow, domain.GateResult, error) {
	if artifact.Kind != "practice-candidate" || artifact.Digest != candidate.Source.Digest {
		return domain.Workflow{}, domain.GateResult{}, errors.New("candidate ledger record does not bind the frozen archive")
	}
	base := ReviewBundle{ArtifactID: bundle.CandidateID, ArtifactDigest: bundle.CandidateDigest, Opinions: bundle.Opinions, CreatedAt: bundle.CreatedAt}
	if err := ValidateReviewIndependence(producerRunID, base); err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	if err := s.saveReviewAudits(ctx, workflowID, audits, bundle.Opinions, "artifact-review"); err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	gate, err := ArtifactGate(bundle, plan, candidate, "safety", "consistency")
	if err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	payload, digest, err := artifactPayload(gate)
	if err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	gateArtifact := domain.ArtifactRecord{ID: "artifact-gate-" + artifact.ID, ParentID: artifact.ID, Kind: "artifact-gate", ContentRevision: artifact.ContentRevision, Digest: digest, SchemaVersion: domain.FormatVersion, OwnerRole: "server", PolicyVersion: gate.PolicyVersion, CreatedAt: s.now(), Payload: payload}
	next := domain.MaterializingArtifact
	if !gate.Approved() {
		next = domain.Rejected
	}
	workflow, replay, err := s.readyFor(ctx, workflowID, domain.ArtifactReviewing, next, gateArtifact)
	if err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	if replay {
		return workflow, gate, nil
	}
	if err := s.store.AppendArtifact(ctx, workflowID, gateArtifact); err != nil {
		return domain.Workflow{}, domain.GateResult{}, err
	}
	workflow, err = s.advanceFrom(ctx, workflow, next, gateArtifact.Kind)
	return workflow, gate, err
}

func (s *Service) ScheduleMaterialization(ctx context.Context, workflowID string, candidate domain.PracticeCandidate, archive []byte) (runnable.ActionIdentity, error) {
	if err := candidate.Validate(); err != nil {
		return runnable.ActionIdentity{}, err
	}
	workflow, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	if workflow.State != domain.MaterializingArtifact {
		return runnable.ActionIdentity{}, errors.New("candidate is not approved for materialization")
	}
	if err := s.runnable.StoreRunnableSource(ctx, candidate.Source, archive, s.now()); err != nil {
		return runnable.ActionIdentity{}, err
	}
	specDigest, err := candidate.Spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	expected := runnable.ActionIdentity{Content: candidate.Spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionMaterializeArtifact, StateVersion: workflow.StateVersion}
	if err := s.store.BindRunnableAction(ctx, workflowID, expected, s.now()); err != nil {
		return runnable.ActionIdentity{}, err
	}
	action, err := s.runnable.ScheduleMaterialization(ctx, candidate.Spec, workflow.StateVersion, s.now())
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	if action != expected {
		return runnable.ActionIdentity{}, errors.New("materialization action does not match the bound workflow action")
	}
	return action, nil
}

func (s *Service) Materialized(ctx context.Context, workflowID string, action runnable.ActionIdentity) (domain.Workflow, runnable.RevisionReference, error) {
	if action.Phase != runnable.ActionMaterializeArtifact {
		return domain.Workflow{}, runnable.RevisionReference{}, errors.New("expected a materialization action")
	}
	reference, err := s.runnable.ResolveMaterializedRunnableRevision(ctx, action)
	if err != nil {
		return domain.Workflow{}, runnable.RevisionReference{}, err
	}
	payload, digest, err := artifactPayload(reference)
	if err != nil {
		return domain.Workflow{}, runnable.RevisionReference{}, err
	}
	artifact := domain.ArtifactRecord{ID: "runnable-revision-" + reference.ID, Kind: "runnable-revision", ContentRevision: action.Content.Revision, Digest: digest, SchemaVersion: domain.FormatVersion, OwnerRole: "runtime-worker", CreatedAt: s.now(), Payload: payload}
	workflow, replay, err := s.readyFor(ctx, workflowID, domain.MaterializingArtifact, domain.Verifying, artifact)
	if err != nil {
		return domain.Workflow{}, runnable.RevisionReference{}, err
	}
	if replay {
		return workflow, reference, nil
	}
	if action.StateVersion != workflow.StateVersion {
		return domain.Workflow{}, runnable.RevisionReference{}, errors.New("materialization action state version is stale")
	}
	if err := s.store.AppendArtifact(ctx, workflowID, artifact); err != nil {
		return domain.Workflow{}, runnable.RevisionReference{}, err
	}
	workflow, err = s.advanceFrom(ctx, workflow, domain.Verifying, artifact.Kind)
	return workflow, reference, err
}

func (s *Service) ScheduleVerification(ctx context.Context, workflowID string, reference runnable.RevisionReference) (runnable.ActionIdentity, error) {
	workflow, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	if workflow.State != domain.Verifying {
		return runnable.ActionIdentity{}, errors.New("runnable revision is not ready for verification")
	}
	revision, err := s.runnable.ResolveRunnableRevision(ctx, reference.ID, reference.Digest)
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	specDigest, err := revision.Spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	expected := runnable.ActionIdentity{Content: revision.Spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: workflow.StateVersion}
	if err := s.store.BindRunnableAction(ctx, workflowID, expected, s.now()); err != nil {
		return runnable.ActionIdentity{}, err
	}
	action, err := s.runnable.ScheduleVerification(ctx, reference, workflow.StateVersion, s.now())
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	if action != expected {
		return runnable.ActionIdentity{}, errors.New("verification action does not match the bound workflow action")
	}
	return action, nil
}

// WorkflowForRunnableAction resolves only workflow actions explicitly bound by
// this product. Other public runnable content kinds intentionally have no
// documentation workflow association.
func (s *Service) WorkflowForRunnableAction(ctx context.Context, action runnable.ActionIdentity) (string, bool, error) {
	if err := action.Validate(); err != nil {
		return "", false, err
	}
	return s.store.WorkflowForRunnableAction(ctx, action)
}

func (s *Service) CompletedUnreconciledRunnableActions(ctx context.Context) ([]runnable.ActionIdentity, error) {
	return s.store.ListCompletedUnreconciledRunnableActions(ctx)
}

func (s *Service) MarkRunnableActionReconciled(ctx context.Context, action runnable.ActionIdentity) error {
	if err := action.Validate(); err != nil {
		return err
	}
	return s.store.MarkRunnableActionReconciled(ctx, action, s.now())
}

func (s *Service) Verified(ctx context.Context, workflowID string, action runnable.ActionIdentity) (domain.Workflow, runnable.StoredVerificationReport, error) {
	if action.Phase != runnable.ActionVerify {
		return domain.Workflow{}, runnable.StoredVerificationReport{}, errors.New("expected a verification action")
	}
	report, err := s.runnable.ResolveVerificationForAction(ctx, action)
	if err != nil {
		return domain.Workflow{}, runnable.StoredVerificationReport{}, err
	}
	payload, digest, err := artifactPayload(report.Reference)
	if err != nil {
		return domain.Workflow{}, runnable.StoredVerificationReport{}, err
	}
	artifact := domain.ArtifactRecord{ID: "verification-report-" + report.Reference.ID, Kind: "verification-report", ContentRevision: action.Content.Revision, Digest: digest, SchemaVersion: domain.FormatVersion, OwnerRole: "runtime-worker", CreatedAt: s.now(), Payload: payload}
	next := domain.VerificationReviewing
	if !report.Report.Passed {
		next = domain.Failed
	}
	workflow, replay, err := s.readyFor(ctx, workflowID, domain.Verifying, next, artifact)
	if err != nil {
		return domain.Workflow{}, runnable.StoredVerificationReport{}, err
	}
	if replay {
		return workflow, report, nil
	}
	if action.StateVersion != workflow.StateVersion {
		return domain.Workflow{}, runnable.StoredVerificationReport{}, errors.New("verification action state version is stale")
	}
	if err := s.store.AppendArtifact(ctx, workflowID, artifact); err != nil {
		return domain.Workflow{}, runnable.StoredVerificationReport{}, err
	}
	workflow, err = s.advanceFrom(ctx, workflow, next, artifact.Kind)
	return workflow, report, err
}

func (s *Service) GateVerification(ctx context.Context, workflowID, producerRunID string, report runnable.StoredVerificationReport, bundle VerificationReviewBundle, audits []domain.AgentAudit) (domain.Workflow, error) {
	if bundle.ReportDigest != report.Reference.Digest {
		return domain.Workflow{}, errors.New("verification review does not bind the machine report")
	}
	base := ReviewBundle{ArtifactID: bundle.ArtifactID, ArtifactDigest: bundle.ArtifactDigest, Opinions: bundle.Opinions, CreatedAt: bundle.CreatedAt}
	if err := ValidateReviewIndependence(producerRunID, base); err != nil {
		return domain.Workflow{}, err
	}
	if err := s.saveReviewAudits(ctx, workflowID, audits, bundle.Opinions, "verification-review"); err != nil {
		return domain.Workflow{}, err
	}
	gate, err := Gate(base, "verification")
	if err != nil {
		return domain.Workflow{}, err
	}
	payload, digest, err := artifactPayload(bundle)
	if err != nil {
		return domain.Workflow{}, err
	}
	artifact := domain.ArtifactRecord{ID: "verification-review-" + report.Reference.ID, Kind: "verification-review", ContentRevision: report.Report.RunnableRevisionDigest, Digest: digest, SchemaVersion: domain.FormatVersion, OwnerRole: "verification-review", PolicyVersion: gate.PolicyVersion, CreatedAt: s.now(), Payload: payload}
	next := domain.Publishing
	if !gate.Approved() {
		next = domain.Rejected
	}
	workflow, replay, err := s.readyFor(ctx, workflowID, domain.VerificationReviewing, next, artifact)
	if err != nil {
		return domain.Workflow{}, err
	}
	if replay {
		return workflow, nil
	}
	if err := s.store.AppendArtifact(ctx, workflowID, artifact); err != nil {
		return domain.Workflow{}, err
	}
	return s.advanceFrom(ctx, workflow, next, artifact.Kind)
}

func (s *Service) Publish(ctx context.Context, workflowID string, candidate domain.PracticeCandidate, plan domain.LearningUnitPlan, planGate, artifactGate domain.GateResult, revisionRef runnable.RevisionReference, report runnable.StoredVerificationReport, review VerificationReviewBundle) (domain.Workflow, domain.PracticeRevision, error) {
	workflow, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return domain.Workflow{}, domain.PracticeRevision{}, err
	}
	if workflow.State != domain.Publishing {
		return domain.Workflow{}, domain.PracticeRevision{}, errors.New("practice is not ready to publish")
	}
	revision, err := s.runnable.ResolveRunnableRevision(ctx, revisionRef.ID, revisionRef.Digest)
	if err != nil {
		return domain.Workflow{}, domain.PracticeRevision{}, err
	}
	manifest, err := Publish(candidate, planGate, artifactGate, revision, report.Report, review, s.now())
	if err != nil {
		return domain.Workflow{}, domain.PracticeRevision{}, err
	}
	practice := domain.PracticeRevision{FormatVersion: domain.FormatVersion, ID: "practice-" + candidate.ID, WorkflowID: workflowID, Context: candidate.Context, PlanID: plan.ID, PlanRevision: plan.Revision, WorkflowRevision: workflow.Revision, CandidateID: candidate.ID, RunnableRevisionRef: revisionRef, VerificationReportRef: report.Reference, PublicationManifestID: manifest.ID, ReaderProjection: domain.ReaderProjectionFromPlan(plan), PublishedAt: s.now()}
	final, err := s.store.PublishPracticeRevision(ctx, workflowID, workflow.StateVersion, practice, manifest, s.now())
	return final, practice, err
}

func (s *Service) advance(ctx context.Context, workflowID string, next domain.WorkflowState, required ...string) (domain.Workflow, error) {
	workflow, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return domain.Workflow{}, err
	}
	return s.store.AdvanceWorkflow(ctx, workflowID, workflow.StateVersion, next, s.now(), required...)
}

func (s *Service) advanceFrom(ctx context.Context, workflow domain.Workflow, next domain.WorkflowState, required ...string) (domain.Workflow, error) {
	return s.store.AdvanceWorkflow(ctx, workflow.ID, workflow.StateVersion, next, s.now(), required...)
}

// ForceFail resolves an irrecoverable workflow: the machine result becomes
// Failed exactly as a failed verification would, with the administrator's
// reason carried in both the ledger entry and the human audit.
func (s *Service) ForceFail(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (domain.Workflow, error) {
	return s.store.ForceFailWorkflow(ctx, workflowID, reason, action, s.now())
}

// Restart re-drives a failed or rejected workflow through the ordinary
// ignition endpoint. It only resets the state; the next start invocation
// performs the Agent work, so restart and ignition stay separate verbs.
func (s *Service) Restart(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (domain.Workflow, error) {
	return s.store.RestartWorkflow(ctx, workflowID, reason, action, s.now())
}

// readyFor permits an exact replay only after the expected artifact is already
// present at the requested next state. It prevents a retry from appending new
// inputs to a workflow which has progressed past that phase.
func (s *Service) readyFor(ctx context.Context, workflowID string, expected, next domain.WorkflowState, artifact domain.ArtifactRecord) (domain.Workflow, bool, error) {
	workflow, err := s.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return domain.Workflow{}, false, err
	}
	if workflow.State == expected {
		return workflow, false, nil
	}
	if workflow.State == next {
		for _, existing := range workflow.Artifacts {
			if existing.ID == artifact.ID && existing.Digest == artifact.Digest {
				return workflow, true, nil
			}
		}
	}
	return domain.Workflow{}, false, fmt.Errorf("documentation workflow is %s, expected %s", workflow.State, expected)
}

func (s *Service) saveReviewAudits(ctx context.Context, workflowID string, audits []domain.AgentAudit, opinions []domain.ReviewOpinion, role string) error {
	if len(audits) != len(opinions) || len(audits) == 0 {
		return errors.New("review opinions must each have one AgentRun audit")
	}
	byRun := make(map[string]domain.AgentAudit, len(audits))
	for _, audit := range audits {
		if audit.Role != role || audit.Validate() != nil {
			return errors.New("unexpected or invalid review AgentRun audit")
		}
		if _, exists := byRun[audit.RunID]; exists {
			return errors.New("duplicate review AgentRun audit")
		}
		byRun[audit.RunID] = audit
	}
	for _, opinion := range opinions {
		audit, exists := byRun[opinion.ReviewerID]
		if !exists {
			return errors.New("review opinion is not bound to an AgentRun audit")
		}
		digest, err := DigestJSON(opinion)
		if err != nil || audit.OutputDigest != digest {
			return errors.New("review AgentRun audit does not bind its opinion")
		}
		if err := s.store.SaveAgentAudit(ctx, workflowID, audit); err != nil {
			return err
		}
	}
	return nil
}

func artifactPayload(value any) ([]byte, string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	digest, err := DigestJSON(value)
	return payload, digest, err
}
