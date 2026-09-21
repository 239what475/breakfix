package documentpractice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestServicePublishesMultiPhaseAutomatedDocumentationPracticeWithoutUserSteps(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 13, 0, 0, 0, time.UTC)
	store := newMemoryDocumentStore()
	plan := validPlan()
	plan.CreatedAt = now
	plan.UserSteps = nil
	archive := []byte("documentation practice archive")
	candidate := serviceCandidate(t, plan, archive, now)
	candidate.UserSteps = nil
	candidate.Spec.ValidationPlan.Phases = append(candidate.Spec.ValidationPlan.Phases, runnable.ValidationPhase{
		ID: "conclude", TimeoutSeconds: 60, Execution: runnable.PhaseSequential,
		Assertions: []runnable.AssertionSpec{{ID: "pod-conclusion", Entrypoint: "scripts/conclude.sh", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, BoundaryID: "management-read", TimeoutSeconds: 60}},
	})
	if err := candidate.Validate(); err != nil {
		t.Fatalf("multi-phase automated candidate: %v", err)
	}
	revision, report := serviceRevisionAndReport(t, candidate, now, true)
	runnableStore := &memoryRunnableStore{revision: revision, report: report}
	service, err := NewService(store, runnableStore)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }

	workflow, err := service.Start(ctx, "document-service-full", testPageIdentity(), nil)
	if err != nil || workflow.State != domain.Planning {
		t.Fatalf("start = %#v, %v", workflow, err)
	}
	planAudit := serviceAudit(t, "planner-run", "planner", plan)
	workflow, planArtifact, err := service.SubmitPlan(ctx, workflow.ID, plan, planAudit)
	if err != nil || workflow.State != domain.PlanReviewing {
		t.Fatalf("submit plan = %#v, %v", workflow, err)
	}
	// A network retry of the same planner result must not advance twice.
	replay, replayArtifact, err := service.SubmitPlan(ctx, workflow.ID, plan, planAudit)
	if err != nil || replay.StateVersion != workflow.StateVersion || replayArtifact.ID != planArtifact.ID || replayArtifact.Digest != planArtifact.Digest {
		t.Fatalf("replay plan = %#v %#v, %v", replay, replayArtifact, err)
	}
	planReviews := []domain.AgentAudit{
		serviceAudit(t, "plan-evidence", "plan-review", domain.ReviewOpinion{ReviewerID: "plan-evidence", Role: "evidence", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"}),
		serviceAudit(t, "plan-value", "plan-review", domain.ReviewOpinion{ReviewerID: "plan-value", Role: "value", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"}),
	}
	planBundle := ReviewBundle{ArtifactID: planArtifact.ID, ArtifactDigest: planArtifact.Digest, Opinions: []domain.ReviewOpinion{
		{ReviewerID: "plan-evidence", Role: "evidence", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"},
		{ReviewerID: "plan-value", Role: "value", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"},
	}, CreatedAt: now}
	workflow, planGate, err := service.GatePlan(ctx, workflow.ID, planAudit.RunID, planArtifact, planBundle, planReviews)
	if err != nil || !planGate.Approved() || workflow.State != domain.Generating {
		t.Fatalf("gate plan = %#v %#v, %v", workflow, planGate, err)
	}
	candidateAudit := serviceAudit(t, "generator-run", "generator", candidate)
	workflow, candidateArtifact, err := service.SubmitCandidate(ctx, workflow.ID, plan, candidate, archive, candidateAudit)
	if err != nil || workflow.State != domain.ArtifactReviewing {
		t.Fatalf("submit candidate = %#v, %v", workflow, err)
	}
	specDigest, err := candidate.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	candidateReviews := []domain.AgentAudit{
		serviceAudit(t, "artifact-safety", "artifact-review", domain.ReviewOpinion{ReviewerID: "artifact-safety", Role: "safety", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"}),
		serviceAudit(t, "artifact-consistency", "artifact-review", domain.ReviewOpinion{ReviewerID: "artifact-consistency", Role: "consistency", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"}),
	}
	candidateBundle := ArtifactReviewBundle{CandidateID: candidate.ID, CandidateDigest: candidate.Source.Digest, SpecDigest: specDigest, PlanID: plan.ID, PlanRevision: plan.Revision, Opinions: []domain.ReviewOpinion{
		{ReviewerID: "artifact-safety", Role: "safety", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"},
		{ReviewerID: "artifact-consistency", Role: "consistency", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"},
	}, CreatedAt: now}
	workflow, artifactGate, err := service.GateCandidate(ctx, workflow.ID, candidateAudit.RunID, plan, candidate, candidateArtifact, candidateBundle, candidateReviews)
	if err != nil || !artifactGate.Approved() || workflow.State != domain.MaterializingArtifact {
		t.Fatalf("gate candidate = %#v %#v, %v", workflow, artifactGate, err)
	}
	materialize, err := service.ScheduleMaterialization(ctx, workflow.ID, candidate, archive)
	if err != nil || materialize.Phase != runnable.ActionMaterializeArtifact {
		t.Fatalf("schedule materialization = %#v, %v", materialize, err)
	}
	if workflowID, found, err := service.WorkflowForRunnableAction(ctx, materialize); err != nil || !found || workflowID != workflow.ID {
		t.Fatalf("materialization workflow binding = %q %t %v", workflowID, found, err)
	}
	if replayAction, err := service.ScheduleMaterialization(ctx, workflow.ID, candidate, archive); err != nil || replayAction != materialize {
		t.Fatalf("replay materialization = %#v, %v", replayAction, err)
	}
	workflow, revisionRef, err := service.Materialized(ctx, workflow.ID, materialize)
	if err != nil || workflow.State != domain.Verifying {
		t.Fatalf("materialized = %#v %#v, %v", workflow, revisionRef, err)
	}
	verification, err := service.ScheduleVerification(ctx, workflow.ID, revisionRef)
	if err != nil || verification.Phase != runnable.ActionVerify {
		t.Fatalf("schedule verification = %#v, %v", verification, err)
	}
	if workflowID, found, err := service.WorkflowForRunnableAction(ctx, verification); err != nil || !found || workflowID != workflow.ID {
		t.Fatalf("verification workflow binding = %q %t %v", workflowID, found, err)
	}
	workflow, storedReport, err := service.Verified(ctx, workflow.ID, verification)
	if err != nil || workflow.State != domain.VerificationReviewing || !storedReport.Report.Passed || len(storedReport.Report.Phases) != 3 {
		t.Fatalf("verified = %#v %#v, %v", workflow, storedReport, err)
	}
	verificationReview := VerificationReviewBundle{ArtifactID: "verification-review-input", ArtifactDigest: serviceDigest("a"), ReportDigest: storedReport.Reference.Digest, Opinions: []domain.ReviewOpinion{{ReviewerID: "verification-review", Role: "verification", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"}}, CreatedAt: now}
	verificationAudits := []domain.AgentAudit{serviceAudit(t, "verification-review", "verification-review", verificationReview.Opinions[0])}
	workflow, err = service.GateVerification(ctx, workflow.ID, "runtime-worker", storedReport, verificationReview, verificationAudits)
	if err != nil || workflow.State != domain.Publishing {
		t.Fatalf("gate verification = %#v, %v", workflow, err)
	}
	workflow, practice, err := service.Publish(ctx, workflow.ID, candidate, plan, planGate, artifactGate, revisionRef, storedReport, verificationReview)
	if err != nil || workflow.State != domain.Published || practice.CandidateID != candidate.ID || store.published == nil || len(candidate.UserSteps) != 0 {
		t.Fatalf("publish = %#v %#v, %v", workflow, practice, err)
	}
	projection := practice.ReaderProjection
	if projection == nil || projection.Title != plan.Title || projection.Objective != plan.Objective || projection.Boundary != plan.Boundary || len(projection.Steps) != 0 || len(projection.Observations) != 1 || projection.Observations[0] != "Pod reaches Running" {
		t.Fatalf("publish did not freeze the reader projection from the plan: %+v", projection)
	}
}

func TestServiceRejectsStaleActionsAndRecordsVerificationFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 14, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	archive := []byte("documentation practice archive")
	candidate := serviceCandidate(t, plan, archive, now)
	revision, failedReport := serviceRevisionAndReport(t, candidate, now, false)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{revision: revision, report: failedReport})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	workflow, err := service.Start(ctx, "document-service-failure", testPageIdentity(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// Set up just the public-runtime phase: this proves a stale Worker action
	// cannot append a report to a newer workflow, while a failed report becomes
	// an immutable terminal result rather than an unrecorded application error.
	workflow.State = domain.Verifying
	workflow.StateVersion = 6
	store.workflows[workflow.ID] = workflow
	stale := runnable.ActionIdentity{Content: candidate.Spec.Identity, SpecDigest: mustSpecDigest(t, candidate.Spec), Phase: runnable.ActionVerify, StateVersion: 5}
	if _, _, err := service.Verified(ctx, workflow.ID, stale); err == nil {
		t.Fatal("stale verification action was accepted")
	}
	current := stale
	current.StateVersion = workflow.StateVersion
	got, report, err := service.Verified(ctx, workflow.ID, current)
	if err != nil || got.State != domain.Failed || report.Report.Passed {
		t.Fatalf("failed verification = %#v %#v, %v", got, report, err)
	}
}

func TestRestartedWorkflowReplansIntoFreshLedgerEntries(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	workflow, err := service.Start(ctx, "document-restart-replan", testPageIdentity(), nil)
	if err != nil {
		t.Fatal(err)
	}
	plan := validPlan()
	plan.CreatedAt = now
	firstAudit := serviceAudit(t, "planner-first", "planner", plan)
	if _, _, err := service.SubmitPlan(ctx, workflow.ID, plan, firstAudit); err != nil {
		t.Fatalf("first plan submission: %v", err)
	}
	forceAction := audit.HumanAction{ID: "audit-force", UserID: "u-admin", Action: audit.ActionDocumentationWorkflowForceFail, TargetType: audit.TargetDocumentWorkflow, TargetID: workflow.ID, Detail: json.RawMessage(`{}`), CreatedAt: now}
	if _, err := service.ForceFail(ctx, workflow.ID, "stuck", &forceAction); err != nil {
		t.Fatalf("force fail: %v", err)
	}
	restartAction := audit.HumanAction{ID: "audit-restart", UserID: "u-admin", Action: audit.ActionDocumentationWorkflowRestart, TargetType: audit.TargetDocumentWorkflow, TargetID: workflow.ID, Detail: json.RawMessage(`{}`), CreatedAt: now}
	restarted, err := service.Restart(ctx, workflow.ID, "retry", &restartAction)
	if err != nil || restarted.Revision != 2 {
		t.Fatalf("restart = %#v, %v", restarted, err)
	}

	// A re-planning attempt produces new plan bytes (any planner output with a
	// timestamp differs) and must land in fresh ledger entries instead of
	// colliding with the immutable artifacts of the failed attempt.
	replan := plan
	replan.CreatedAt = now.Add(time.Minute)
	if err := replan.Validate(); err != nil {
		t.Fatal(err)
	}
	secondAudit := serviceAudit(t, "planner-second", "planner", replan)
	submitted, artifact, err := service.SubmitPlan(ctx, workflow.ID, replan, secondAudit)
	if err != nil {
		t.Fatalf("restarted plan submission = %v", err)
	}
	if submitted.State != domain.PlanReviewing || submitted.Revision != 2 {
		t.Fatalf("restarted workflow = %#v", submitted)
	}
	if artifact.ID != "plan-"+plan.ID+"-r1-a2" {
		t.Fatalf("re-planned artifact = %q, want the attempt-namespaced id", artifact.ID)
	}
	entries, err := store.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, entry := range entries.Artifacts {
		kinds[entry.Kind]++
	}
	if kinds["learning-unit-plan"] != 2 {
		t.Fatalf("ledger plans = %d, want the failed attempt's plan and the new one", kinds["learning-unit-plan"])
	}
}

func TestServicePlanRejectionAndCandidateDigestMismatchDoNotProgress(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 15, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	workflow, err := service.Start(ctx, "document-service-reject", testPageIdentity(), nil)
	if err != nil {
		t.Fatal(err)
	}
	audit := serviceAudit(t, "planner-reject", "planner", plan)
	workflow, artifact, err := service.SubmitPlan(ctx, workflow.ID, plan, audit)
	if err != nil {
		t.Fatal(err)
	}
	bundle := ReviewBundle{ArtifactID: artifact.ID, ArtifactDigest: artifact.Digest, Opinions: []domain.ReviewOpinion{
		{ReviewerID: "review-evidence", Role: "evidence", Decision: domain.ReviewReject, HardReject: true, PolicyVersion: "review-v1"},
		{ReviewerID: "review-value", Role: "value", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"},
	}, CreatedAt: now}
	audits := []domain.AgentAudit{
		serviceAudit(t, "review-evidence", "plan-review", bundle.Opinions[0]),
		serviceAudit(t, "review-value", "plan-review", bundle.Opinions[1]),
	}
	workflow, gate, err := service.GatePlan(ctx, workflow.ID, audit.RunID, artifact, bundle, audits)
	if err != nil || gate.Approved() || workflow.State != domain.Rejected {
		t.Fatalf("rejected plan = %#v %#v, %v", workflow, gate, err)
	}

	archive := []byte("correct archive")
	candidate := serviceCandidate(t, plan, archive, now)
	candidate.Source.Digest = serviceDigest("b")
	candidate.Spec.Source.Digest = candidate.Source.Digest
	if _, _, err := service.SubmitCandidate(ctx, workflow.ID, plan, candidate, archive, serviceAudit(t, "generator-bad", "generator", candidate)); err == nil {
		t.Fatal("candidate with an archive digest mismatch was accepted")
	}
}

type memoryDocumentStore struct {
	mu               sync.Mutex
	workflows        map[string]domain.Workflow
	identities       map[string]domain.WorkflowPageIdentity
	batches          map[string]domain.DocumentBatch
	batchItems       map[string][]domain.BatchItem
	publishedAnchors map[string]bool
	audits           map[string]domain.AgentAudit
	actions          map[string]memoryDocumentAction
	published        *domain.PracticeRevision
	humanActions     []audit.HumanAction
	// watchdogStatuses carries the public action status of bound actions for
	// the watchdog candidate listing; the map key is the action key.
	watchdogStatuses map[string]WatchdogActionStatus
	watchdogReasons  map[string]string

	adminTransitions     []memoryAdminTransition
	pipelineAutoRestarts []memoryPipelineAutoRestart
}

type memoryDocumentAction struct {
	workflowID string
	action     runnable.ActionIdentity
	reconciled bool
}

func newMemoryDocumentStore() *memoryDocumentStore {
	return &memoryDocumentStore{workflows: map[string]domain.Workflow{}, identities: map[string]domain.WorkflowPageIdentity{}, batches: map[string]domain.DocumentBatch{}, batchItems: map[string][]domain.BatchItem{}, publishedAnchors: map[string]bool{}, audits: map[string]domain.AgentAudit{}, actions: map[string]memoryDocumentAction{}, watchdogStatuses: map[string]WatchdogActionStatus{}, watchdogReasons: map[string]string{}}
}

func (s *memoryDocumentStore) CreateWorkflow(_ context.Context, workflow domain.Workflow, identity domain.WorkflowPageIdentity, action *audit.HumanAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workflows[workflow.ID]; exists {
		return errors.New("workflow already exists")
	}
	s.workflows[workflow.ID] = workflow
	s.identities[workflow.ID] = identity
	if action != nil {
		s.humanActions = append(s.humanActions, *action)
	}
	return nil
}

func (s *memoryDocumentStore) RecordHumanAction(_ context.Context, action audit.HumanAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.humanActions = append(s.humanActions, action)
	return nil
}

func (s *memoryDocumentStore) ForceFailWorkflow(_ context.Context, id, reason string, action *audit.HumanAction, now time.Time) (domain.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, err := s.getWorkflowUnlocked(id)
	if err != nil {
		return domain.Workflow{}, err
	}
	fromState := workflow.State
	if err := workflow.AdvanceAt(domain.Failed, now); err != nil {
		return domain.Workflow{}, err
	}
	s.workflows[id] = workflow
	s.adminTransitions = append(s.adminTransitions, memoryAdminTransition{kind: "admin.force_fail", workflowID: id, fromState: fromState, reason: reason, action: action})
	if action != nil {
		s.humanActions = append(s.humanActions, *action)
	}
	return workflow, nil
}

func (s *memoryDocumentStore) RestartWorkflow(_ context.Context, id, reason string, action *audit.HumanAction, now time.Time) (domain.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, err := s.getWorkflowUnlocked(id)
	if err != nil {
		return domain.Workflow{}, err
	}
	fromState := workflow.State
	if err := workflow.RestartAt(now); err != nil {
		return domain.Workflow{}, err
	}
	s.workflows[id] = workflow
	s.adminTransitions = append(s.adminTransitions, memoryAdminTransition{kind: "admin.restart", workflowID: id, fromState: fromState, reason: reason, action: action})
	if action != nil {
		s.humanActions = append(s.humanActions, *action)
	}
	return workflow, nil
}

func (s *memoryDocumentStore) AutoRestartWorkflow(_ context.Context, id, gateKind string, reasons []string, expectedStateVersion int64, now time.Time) (domain.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, err := s.getWorkflowUnlocked(id)
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.StateVersion != expectedStateVersion {
		return domain.Workflow{}, fmt.Errorf("%w (workflow moved to %s v%d)", domain.ErrWorkflowConflict, workflow.State, workflow.StateVersion)
	}
	fromState := workflow.State
	if err := workflow.RestartAt(now); err != nil {
		return domain.Workflow{}, fmt.Errorf("%w (%s)", domain.ErrWorkflowConflict, err.Error())
	}
	payload, err := json.Marshal(struct {
		System    string    `json:"system"`
		FromState string    `json:"from_state"`
		Gate      string    `json:"gate"`
		Reasons   []string  `json:"reasons"`
		At        time.Time `json:"at"`
	}{System: "documentation-pipeline", FromState: string(fromState), Gate: gateKind, Reasons: reasons, At: now.UTC()})
	if err != nil {
		return domain.Workflow{}, err
	}
	digest, err := domain.DigestAgentInput(payload)
	if err != nil {
		return domain.Workflow{}, err
	}
	entry := domain.ArtifactRecord{ID: "pipeline.auto_restart-" + id + "-v" + strconv.FormatInt(workflow.StateVersion, 10), Kind: "pipeline.auto_restart", ContentRevision: strconv.FormatInt(workflow.Revision, 10), Digest: digest, SchemaVersion: domain.FormatVersion, OwnerRole: "system", CreatedAt: now.UTC(), Payload: payload}
	if err := workflow.Append(entry, now); err != nil {
		return domain.Workflow{}, err
	}
	s.workflows[id] = workflow
	s.pipelineAutoRestarts = append(s.pipelineAutoRestarts, memoryPipelineAutoRestart{gateKind: gateKind, reasons: reasons, revision: workflow.Revision})
	return workflow, nil
}

type memoryPipelineAutoRestart struct {
	gateKind string
	reasons  []string
	revision int64
}

type memoryAdminTransition struct {
	kind       string
	workflowID string
	fromState  domain.WorkflowState
	reason     string
	action     *audit.HumanAction
}

func (s *memoryDocumentStore) GetWorkflow(_ context.Context, id string) (domain.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getWorkflowUnlocked(id)
}

func (s *memoryDocumentStore) getWorkflowUnlocked(id string) (domain.Workflow, error) {
	workflow, exists := s.workflows[id]
	if !exists {
		return domain.Workflow{}, errors.New("workflow not found")
	}
	return workflow, nil
}

func (s *memoryDocumentStore) AppendArtifact(_ context.Context, id string, artifact domain.ArtifactRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, err := s.getWorkflowUnlocked(id)
	if err != nil {
		return err
	}
	if err := workflow.Append(artifact, artifact.CreatedAt); err != nil {
		return err
	}
	s.workflows[id] = workflow
	return nil
}

func (s *memoryDocumentStore) AdvanceWorkflow(_ context.Context, id string, expected int64, next domain.WorkflowState, now time.Time, required ...string) (domain.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, err := s.getWorkflowUnlocked(id)
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.StateVersion != expected {
		return domain.Workflow{}, errors.New("stale state version")
	}
	if err := workflow.AdvanceAt(next, now, required...); err != nil {
		return domain.Workflow{}, err
	}
	s.workflows[id] = workflow
	return workflow, nil
}

func (s *memoryDocumentStore) SaveAgentAudit(_ context.Context, _ string, audit domain.AgentAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := audit.Validate(); err != nil {
		return err
	}
	if existing, exists := s.audits[audit.RunID]; exists && existing != audit {
		return errors.New("audit changed")
	}
	s.audits[audit.RunID] = audit
	return nil
}

func (s *memoryDocumentStore) BindRunnableAction(_ context.Context, workflowID string, action runnable.ActionIdentity, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := action.Validate(); err != nil {
		return err
	}
	if _, err := s.getWorkflowUnlocked(workflowID); err != nil {
		return err
	}
	if existing, ok := s.actions[action.Key()]; ok {
		if existing.workflowID != workflowID || existing.action != action {
			return errors.New("runnable action already belongs to another workflow")
		}
		return nil
	}
	s.actions[action.Key()] = memoryDocumentAction{workflowID: workflowID, action: action}
	return nil
}

func (s *memoryDocumentStore) setWatchdogStatus(action runnable.ActionIdentity, status WatchdogActionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchdogStatuses[action.Key()] = status
}

func (s *memoryDocumentStore) ListWorkflowWatchdogCandidates(_ context.Context, now time.Time) ([]WatchdogCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := []WatchdogCandidate{}
	for _, value := range s.actions {
		workflow, ok := s.workflows[value.workflowID]
		if !ok || workflow.State.Terminal() || workflow.StateVersion != value.action.StateVersion {
			continue
		}
		status, ok := s.watchdogStatuses[value.action.Key()]
		if !ok {
			continue
		}
		if status.State != "failed" && (status.Attempt < 5 || status.State != "queued" && (status.State != "running" || !status.LeaseExpired)) {
			continue
		}
		if status.UpdatedAt.IsZero() {
			status.UpdatedAt = now
		}
		candidates = append(candidates, WatchdogCandidate{WorkflowID: value.workflowID, WorkflowState: workflow.State, WorkflowStateVersion: workflow.StateVersion, Action: status})
	}
	return candidates, nil
}

func (s *memoryDocumentStore) WatchdogFailWorkflow(_ context.Context, id, reason string, expected int64, now time.Time) (domain.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, err := s.getWorkflowUnlocked(id)
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.StateVersion != expected {
		return domain.Workflow{}, errors.New("stale state version")
	}
	if err := workflow.AdvanceAt(domain.Failed, now); err != nil {
		return domain.Workflow{}, err
	}
	s.workflows[id] = workflow
	s.watchdogReasons[id] = reason
	artifact := domain.ArtifactRecord{ID: "watchdog.force_fail-" + id + "-v" + strconv.FormatInt(workflow.StateVersion, 10), Kind: "watchdog.force_fail", ContentRevision: strconv.FormatInt(workflow.Revision, 10), Digest: serviceDigest("7"), SchemaVersion: domain.FormatVersion, OwnerRole: "system", CreatedAt: now}
	if err := workflow.Append(artifact, now); err != nil {
		panic(err)
	}
	s.workflows[id] = workflow
	return workflow, nil
}

func (s *memoryDocumentStore) WorkflowForRunnableAction(_ context.Context, action runnable.ActionIdentity) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := action.Validate(); err != nil {
		return "", false, err
	}
	value, ok := s.actions[action.Key()]
	return value.workflowID, ok, nil
}

func (s *memoryDocumentStore) ListCompletedUnreconciledRunnableActions(_ context.Context) ([]runnable.ActionIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]runnable.ActionIdentity, 0, len(s.actions))
	for _, value := range s.actions {
		if !value.reconciled {
			result = append(result, value.action)
		}
	}
	return result, nil
}

func (s *memoryDocumentStore) MarkRunnableActionReconciled(_ context.Context, action runnable.ActionIdentity, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.actions[action.Key()]
	if !ok || value.action != action {
		return errors.New("runnable action binding not found")
	}
	value.reconciled = true
	s.actions[action.Key()] = value
	return nil
}

func (s *memoryDocumentStore) PublishPracticeRevision(_ context.Context, id string, expected int64, revision domain.PracticeRevision, _ domain.PublicationManifest, now time.Time) (domain.Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow, err := s.getWorkflowUnlocked(id)
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.State == domain.Published && s.published != nil && *s.published == revision {
		return workflow, nil
	}
	if workflow.State != domain.Publishing || workflow.StateVersion != expected {
		return domain.Workflow{}, errors.New("publication state is stale")
	}
	if err := workflow.AdvanceAt(domain.Published, now); err != nil {
		return domain.Workflow{}, err
	}
	s.published = &revision
	s.workflows[id] = workflow
	return workflow, nil
}

type memoryRunnableStore struct {
	source   []byte
	revision runnable.RunnableRevision
	report   runnable.StoredVerificationReport
}

func (s *memoryRunnableStore) StoreRunnableSource(_ context.Context, source runnable.SourceArchive, archive []byte, _ time.Time) error {
	sum := sha256.Sum256(archive)
	if source.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return errors.New("source digest mismatch")
	}
	s.source = append([]byte(nil), archive...)
	return nil
}

func (s *memoryRunnableStore) ScheduleMaterialization(_ context.Context, spec runnable.RunnableSpec, stateVersion int64, _ time.Time) (runnable.ActionIdentity, error) {
	digest, err := spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	return runnable.ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: runnable.ActionMaterializeArtifact, StateVersion: stateVersion}, nil
}

func (s *memoryRunnableStore) ResolveMaterializedRunnableRevision(_ context.Context, action runnable.ActionIdentity) (runnable.RevisionReference, error) {
	if action.Phase != runnable.ActionMaterializeArtifact {
		return runnable.RevisionReference{}, runnable.ErrMaterializationNotReady
	}
	digest, err := s.revision.Digest()
	if err != nil {
		return runnable.RevisionReference{}, err
	}
	return runnable.RevisionReference{ID: "runnable-document", Digest: digest}, nil
}

func (s *memoryRunnableStore) ScheduleVerification(_ context.Context, reference runnable.RevisionReference, stateVersion int64, _ time.Time) (runnable.ActionIdentity, error) {
	if err := reference.Validate(); err != nil {
		return runnable.ActionIdentity{}, err
	}
	specDigest, err := s.revision.Spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	return runnable.ActionIdentity{Content: s.revision.Spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: stateVersion}, nil
}

func (s *memoryRunnableStore) ResolveVerificationForAction(_ context.Context, action runnable.ActionIdentity) (runnable.StoredVerificationReport, error) {
	if action.Phase != runnable.ActionVerify {
		return runnable.StoredVerificationReport{}, errors.New("wrong action")
	}
	return s.report, nil
}

func (s *memoryRunnableStore) ResolveRunnableRevision(_ context.Context, id, digest string) (runnable.RunnableRevision, error) {
	actual, err := s.revision.Digest()
	if err != nil || id != "runnable-document" || digest != actual {
		return runnable.RunnableRevision{}, errors.New("revision not found")
	}
	return s.revision, nil
}

func testPageIdentity() domain.WorkflowPageIdentity {
	return domain.WorkflowPageIdentity{SourceID: "kubernetes", Commit: strings.Repeat("a", 40), Language: "en", PagePath: "docs/concepts/workloads/pods/pod-lifecycle", Anchor: "pod-lifetime"}
}

func serviceCandidate(t *testing.T, plan domain.LearningUnitPlan, archive []byte, now time.Time) domain.PracticeCandidate {
	t.Helper()
	sum := sha256.Sum256(archive)
	source := runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/document-practice.tar", Digest: "sha256:" + hex.EncodeToString(sum[:])}
	target := runnable.TargetLocation{Kind: "management", ID: "cluster"}
	profile := runnable.RuntimeProfile{Runtime: plan.Runtime.Runtime, ProfileRevision: "document-profile-01", BaseImage: plan.Runtime.BaseImage, SoftwareVersions: map[string]string{"kubernetes": "v1"}, Resources: plan.Runtime.Resources, Network: plan.Runtime.Network, Topology: plan.Runtime.Topology, ExecutionBoundaries: []runnable.ExecutionBoundary{
		{ID: "management-write", Target: target, Permission: runnable.PermissionReadWrite, Network: plan.Runtime.Network, MaxTimeout: 60},
		{ID: "management-read", Target: target, Permission: runnable.PermissionReadOnly, Network: plan.Runtime.Network, MaxTimeout: 60},
	}}
	candidate := domain.PracticeCandidate{FormatVersion: domain.FormatVersion, ID: "pod-lifecycle-practice", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, Context: plan.Context, Source: source, UserSteps: []domain.UserStep{{ID: "apply-pod", Instruction: "Apply the Pod manifest", EvidenceIDs: []string{"page"}}}, Observations: plan.Observations, CreatedAt: now}
	candidate.Spec = runnable.RunnableSpec{FormatVersion: runnable.FormatVersion, Identity: runnable.ContentIdentity{Kind: "documentation-practice", ID: domain.ContentID(plan.Context), Revision: candidate.ID}, RuntimeProfile: profile, Source: source, Initialization: []runnable.ActionSpec{{ID: "initialize", Entrypoint: "scripts/init.sh", Target: target, BoundaryID: "management-write", TimeoutSeconds: 60, ExpectedExitCodes: []int{0}}}, ValidationPlan: runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{{ID: "observe", TimeoutSeconds: 60, Execution: runnable.PhaseSequential, Actions: []runnable.ActionSpec{{ID: "apply", Entrypoint: "scripts/apply.sh", Target: target, BoundaryID: "management-write", TimeoutSeconds: 60, ExpectedExitCodes: []int{0}}}, Assertions: []runnable.AssertionSpec{{ID: "pod-running", Entrypoint: "scripts/assert.sh", Target: target, BoundaryID: "management-read", TimeoutSeconds: 60}}}}}, LifecyclePolicy: runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 300, MaxLifetimeSeconds: 600}}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("test candidate: %v", err)
	}
	return candidate
}

func serviceRevisionAndReport(t *testing.T, candidate domain.PracticeCandidate, now time.Time, passed bool) (runnable.RunnableRevision, runnable.StoredVerificationReport) {
	t.Helper()
	specDigest := mustSpecDigest(t, candidate.Spec)
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: candidate.Spec, Artifact: runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: candidate.Spec.RuntimeProfile.Runtime, ProviderReference: "registry.example/document-practice@" + serviceDigest("c"), ArtifactDigest: serviceDigest("c"), BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01"}}
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profileDigest, err := candidate.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	phases := []runnable.PhaseResult{{ID: "initialization"}}
	for _, action := range candidate.Spec.Initialization {
		phases[0].Actions = append(phases[0].Actions, runnable.ActionResult{ID: action.ID, ExitCode: 0, Summary: "initialized"})
	}
	for _, phase := range candidate.Spec.ValidationPlan.Phases {
		result := runnable.PhaseResult{ID: phase.ID}
		for _, action := range phase.Actions {
			result.Actions = append(result.Actions, runnable.ActionResult{ID: action.ID, ExitCode: 0, Summary: "completed"})
		}
		for _, assertion := range phase.Assertions {
			result.Assertions = append(result.Assertions, runnable.AssertionResult{ID: assertion.ID, Satisfied: passed, Summary: "observed"})
		}
		phases = append(phases, result)
	}
	report := runnable.VerificationReport{FormatVersion: runnable.FormatVersion, RunnableRevisionDigest: revisionDigest, Environment: runnable.EnvironmentIdentity{ID: "verification-environment", Provider: "k8s", ProfileDigest: profileDigest}, Attempt: 1, Passed: passed, CreatedAt: now, Phases: phases}
	reportDigest, err := report.Digest(revision)
	if err != nil {
		t.Fatal(err)
	}
	return revision, runnable.StoredVerificationReport{Reference: runnable.VerificationReportReference{ID: "verification-report", Digest: reportDigest}, Report: report, RunnableRevision: revision, CreatedAt: now}
}

func serviceAudit(t *testing.T, runID, role string, output any) domain.AgentAudit {
	t.Helper()
	_, digest, err := artifactPayload(output)
	if err != nil {
		t.Fatal(err)
	}
	return domain.AgentAudit{RunID: runID, Role: role, Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1", InputDigest: serviceDigest("d"), OutputDigest: digest, CreatedAt: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
}

func mustSpecDigest(t *testing.T, spec runnable.RunnableSpec) string {
	t.Helper()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func serviceDigest(value string) string { return "sha256:" + strings.Repeat(value, 64) }

var _ Store = (*memoryDocumentStore)(nil)
var _ RunnableStore = (*memoryRunnableStore)(nil)

func (s *memoryDocumentStore) CreateBatch(_ context.Context, batch domain.DocumentBatch, items []domain.BatchItem, action *audit.HumanAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := batch.Validate(); err != nil {
		return err
	}
	if _, exists := s.batches[batch.ID]; exists {
		return errors.New("batch already exists")
	}
	if action == nil {
		return errors.New("batch creation requires a human action audit")
	}
	if err := action.Validate(); err != nil {
		return err
	}
	s.batches[batch.ID] = batch
	s.batchItems[batch.ID] = append([]domain.BatchItem(nil), items...)
	s.humanActions = append(s.humanActions, *action)
	return nil
}

func (s *memoryDocumentStore) GetBatch(_ context.Context, batchID string) (domain.DocumentBatch, domain.BatchItemCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, exists := s.batches[batchID]
	if !exists {
		return domain.DocumentBatch{}, nil, errors.New("document batch not found")
	}
	counts := domain.BatchItemCounts{}
	for _, item := range s.batchItems[batchID] {
		counts[item.State]++
	}
	return batch, counts, nil
}

func (s *memoryDocumentStore) ListBatches(_ context.Context, limit int) ([]domain.DocumentBatch, []domain.BatchItemCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batches := make([]domain.DocumentBatch, 0, len(s.batches))
	for _, batch := range s.batches {
		batches = append(batches, batch)
	}
	sort.Slice(batches, func(i, j int) bool { return batches[i].CreatedAt.After(batches[j].CreatedAt) })
	if len(batches) > limit {
		batches = batches[:limit]
	}
	counts := make([]domain.BatchItemCounts, 0, len(batches))
	for _, batch := range batches {
		batchCounts := domain.BatchItemCounts{}
		for _, item := range s.batchItems[batch.ID] {
			batchCounts[item.State]++
		}
		counts = append(counts, batchCounts)
	}
	return batches, counts, nil
}

func (s *memoryDocumentStore) ListBatchItems(_ context.Context, filter domain.BatchItemFilter) ([]domain.BatchItem, *domain.BatchItemCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if filter.Limit < 1 || filter.BatchID == "" {
		return nil, nil, errors.New("invalid batch item filter")
	}
	items := []domain.BatchItem{}
	for _, item := range s.batchItems[filter.BatchID] {
		if filter.State != "" && item.State != filter.State {
			continue
		}
		if filter.Cursor != nil && item.Ordinal <= filter.Cursor.Ordinal {
			continue
		}
		items = append(items, item)
	}
	var next *domain.BatchItemCursor
	if len(items) > filter.Limit {
		last := items[filter.Limit-1]
		next = &domain.BatchItemCursor{Ordinal: last.Ordinal}
		items = items[:filter.Limit]
	}
	return items, next, nil
}

func (s *memoryDocumentStore) ListPublishedWorkflowAnchors(_ context.Context, identity domain.DocumentContext) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	published := map[string]bool{}
	for key := range s.publishedAnchors {
		published[key] = true
	}
	_ = identity
	return published, nil
}

func (s *memoryDocumentStore) setPublishedAnchor(pagePath, anchor string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishedAnchors[pagePath+"\x00"+anchor] = true
}

func (s *memoryDocumentStore) ListSchedulerBatches(_ context.Context, states []domain.BatchState) ([]domain.DocumentBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := map[domain.BatchState]bool{}
	for _, state := range states {
		wanted[state] = true
	}
	batches := []domain.DocumentBatch{}
	for _, batch := range s.batches {
		if wanted[batch.State] {
			batches = append(batches, batch)
		}
	}
	sort.Slice(batches, func(i, j int) bool { return batches[i].CreatedAt.Before(batches[j].CreatedAt) })
	return batches, nil
}

func (s *memoryDocumentStore) ListBatchItemsByStates(_ context.Context, batchID string, states []domain.BatchItemState) ([]domain.BatchItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := map[domain.BatchItemState]bool{}
	for _, state := range states {
		wanted[state] = true
	}
	items := []domain.BatchItem{}
	for _, item := range s.batchItems[batchID] {
		if wanted[item.State] {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *memoryDocumentStore) ActiveBatchItemWorkflowStates(_ context.Context, batchID string) ([]ActiveBatchItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := []ActiveBatchItem{}
	for _, item := range s.batchItems[batchID] {
		if item.State != domain.ItemScheduled && item.State != domain.ItemRunning {
			continue
		}
		workflow, ok := s.workflows[item.WorkflowID]
		if !ok {
			continue
		}
		result = append(result, ActiveBatchItem{Item: item, WorkflowState: workflow.State})
	}
	return result, nil
}

func (s *memoryDocumentStore) TransitionBatchItem(_ context.Context, itemID string, from, to domain.BatchItemState, detail string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for batchID, items := range s.batchItems {
		for index, item := range items {
			if item.ID != itemID || item.State != from {
				continue
			}
			items[index].State = to
			items[index].Detail = detail
			items[index].UpdatedAt = now
			s.batchItems[batchID] = items
			return true, nil
		}
	}
	return false, nil
}

func (s *memoryDocumentStore) TransitionBatchState(_ context.Context, batchID string, from, to domain.BatchState, action *audit.HumanAction, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, exists := s.batches[batchID]
	if !exists || batch.State != from {
		return false, nil
	}
	if err := domain.TransitionBatch(from, to); err != nil && from != to {
		return false, err
	}
	batch.State = to
	batch.UpdatedAt = now
	s.batches[batchID] = batch
	if action != nil {
		s.humanActions = append(s.humanActions, *action)
	}
	return true, nil
}

func (s *memoryDocumentStore) CancelBatch(_ context.Context, batchID string, from domain.BatchState, action *audit.HumanAction, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, exists := s.batches[batchID]
	if !exists || batch.State != from {
		return 0, nil
	}
	batch.State = domain.BatchCancelled
	batch.UpdatedAt = now
	s.batches[batchID] = batch
	var cancelled int64
	items := s.batchItems[batchID]
	for index, item := range items {
		if item.State == domain.ItemPending {
			items[index].State = domain.ItemCancelled
			items[index].Detail = "batch-cancelled"
			items[index].UpdatedAt = now
			cancelled++
		}
	}
	s.batchItems[batchID] = items
	if action != nil {
		s.humanActions = append(s.humanActions, *action)
	}
	return cancelled, nil
}
