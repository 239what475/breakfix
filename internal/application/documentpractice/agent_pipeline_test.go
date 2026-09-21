package documentpractice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestAgentPipelineRunsIndependentRolesAndPublishes(t *testing.T) {
	now := time.Date(2026, 9, 15, 17, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := pipelineBlueprint(plan, seed)
	compiled, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now)
	if err != nil {
		t.Fatal(err)
	}
	revision, report := serviceRevisionAndReport(t, compiled, now, true)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{revision: revision, report: report})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{page: page, metadata: domain.Metadata{Context: plan.Context, Path: page.Path, Title: plan.Title, Anchors: []string{"pod-lifecycle"}}}, fakePlanAgent{plan: plan}, []PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}}, pipelineGenerator{blueprint: blueprint}, []CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{profile: seed.Spec.RuntimeProfile}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newRunID = sequentialRunIDs()

	started, err := pipeline.Start(context.Background(), "agent-pipeline-full", page.Path, "", nil)
	if err != nil || started.Workflow.State != domain.MaterializingArtifact || started.MaterializationAction.Phase != runnable.ActionMaterializeArtifact {
		t.Fatalf("start pipeline = %#v, %v", started, err)
	}
	// The fixed page can be requested again while the Worker owns the queued
	// materialization. That retry must observe state, not invoke Agents again.
	replay, err := pipeline.Start(context.Background(), "agent-pipeline-full", page.Path, "", nil)
	if err != nil || replay.Workflow.State != domain.MaterializingArtifact || replay.MaterializationAction != (runnable.ActionIdentity{}) {
		t.Fatalf("replay pipeline start = %#v, %v", replay, err)
	}
	if len(store.actions) != 1 || len(store.audits) != 6 {
		t.Fatalf("replayed pipeline start changed durable work: actions=%d audits=%d", len(store.actions), len(store.audits))
	}
	if err := pipeline.ReconcileCompletedAction(context.Background(), started.MaterializationAction); err != nil {
		t.Fatalf("reconcile materialization = %v", err)
	}
	workflow, err := service.store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil || workflow.State != domain.Verifying || !store.actions[started.MaterializationAction.Key()].reconciled {
		t.Fatalf("materialization workflow = %#v, binding = %#v, %v", workflow, store.actions[started.MaterializationAction.Key()], err)
	}
	var verification runnable.ActionIdentity
	for _, action := range store.actions {
		if action.action.Phase == runnable.ActionVerify {
			verification = action.action
		}
	}
	if verification == (runnable.ActionIdentity{}) {
		t.Fatal("verification action was not durably bound")
	}
	if err := pipeline.ReconcileCompletedAction(context.Background(), verification); err != nil {
		t.Fatalf("reconcile verification = %v", err)
	}
	workflow, err = service.store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil || workflow.State != domain.Published || store.published == nil || !store.actions[verification.Key()].reconciled {
		t.Fatalf("verification workflow = %#v, binding = %#v, %v", workflow, store.actions[verification.Key()], err)
	}
	if len(store.audits) != 7 { // planner, two plan reviews, generator, two artifact reviews, verification review.
		t.Fatalf("Agent audits = %d, want 7", len(store.audits))
	}
}

func TestAgentPipelineEndsApprovedNoPracticeWithoutGeneration(t *testing.T) {
	now := time.Date(2026, 9, 15, 17, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.NoPractice = true
	plan.CreatedAt = now
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{page: page, metadata: domain.Metadata{Context: plan.Context, Path: page.Path, Title: plan.Title}}, fakePlanAgent{plan: plan}, []PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}}, pipelineGenerator{fail: true}, []CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newRunID = sequentialRunIDs()
	got, err := pipeline.Start(context.Background(), "agent-pipeline-no-practice", page.Path, "", nil)
	if err != nil || got.Workflow.State != domain.NoPractice || got.MaterializationAction != (runnable.ActionIdentity{}) {
		t.Fatalf("no-practice pipeline = %#v, %v", got, err)
	}
}

func TestAgentPipelineRecoverySchedulesVerificationAfterMaterializationCommit(t *testing.T) {
	now := time.Date(2026, 9, 15, 18, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := pipelineBlueprint(plan, seed)
	compiled, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now)
	if err != nil {
		t.Fatal(err)
	}
	revision, report := serviceRevisionAndReport(t, compiled, now, true)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{revision: revision, report: report})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{page: page, metadata: domain.Metadata{Context: plan.Context, Path: page.Path, Title: plan.Title, Anchors: []string{"pod-lifecycle"}}}, fakePlanAgent{plan: plan}, []PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}}, pipelineGenerator{blueprint: blueprint}, []CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{profile: seed.Spec.RuntimeProfile}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newRunID = sequentialRunIDs()

	started, err := pipeline.Start(context.Background(), "agent-pipeline-materialization-recovery", page.Path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// This is the durable state after a Worker result has been stored but the
	// Server stopped before it could queue verification.
	if _, _, err := service.Materialized(context.Background(), started.Workflow.ID, started.MaterializationAction); err != nil {
		t.Fatalf("persist materialization result: %v", err)
	}
	if err := pipeline.Recover(context.Background()); err != nil {
		t.Fatalf("recover materialization completion: %v", err)
	}
	workflow, err := service.store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil || workflow.State != domain.Verifying || !store.actions[started.MaterializationAction.Key()].reconciled {
		t.Fatalf("recovered materialization workflow = %#v, binding = %#v, %v", workflow, store.actions[started.MaterializationAction.Key()], err)
	}
	verificationActions := 0
	for _, action := range store.actions {
		if action.action.Phase == runnable.ActionVerify {
			verificationActions++
		}
	}
	if verificationActions != 1 {
		t.Fatalf("verification actions after recovery = %d, want 1", verificationActions)
	}
}

func TestAgentPipelineRecoveryPublishesAfterVerificationReviewCommit(t *testing.T) {
	now := time.Date(2026, 9, 15, 19, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := pipelineBlueprint(plan, seed)
	compiled, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now)
	if err != nil {
		t.Fatal(err)
	}
	revision, report := serviceRevisionAndReport(t, compiled, now, true)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{revision: revision, report: report})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{page: page, metadata: domain.Metadata{Context: plan.Context, Path: page.Path, Title: plan.Title, Anchors: []string{"pod-lifecycle"}}}, fakePlanAgent{plan: plan}, []PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}}, pipelineGenerator{blueprint: blueprint}, []CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{profile: seed.Spec.RuntimeProfile}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newRunID = sequentialRunIDs()

	started, err := pipeline.Start(context.Background(), "agent-pipeline-publication-recovery", page.Path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	materialized, revisionRef, err := service.Materialized(context.Background(), started.Workflow.ID, started.MaterializationAction)
	if err != nil || materialized.State != domain.Verifying {
		t.Fatalf("persist materialization result = %#v %#v, %v", materialized, revisionRef, err)
	}
	verification, err := service.ScheduleVerification(context.Background(), started.Workflow.ID, revisionRef)
	if err != nil {
		t.Fatal(err)
	}
	verified, stored, err := service.Verified(context.Background(), started.Workflow.ID, verification)
	if err != nil || verified.State != domain.VerificationReviewing {
		t.Fatalf("persist verification result = %#v %#v, %v", verified, stored, err)
	}
	workflow, err := service.store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	workflowPlan, _, _, _, err := workflowPublicationInputs(workflow)
	if err != nil {
		t.Fatal(err)
	}
	reportArtifact, found := artifactFor(workflow, "verification-report-"+stored.Reference.ID)
	if !found {
		t.Fatal("verification report artifact is missing")
	}
	opinion := domain.ReviewOpinion{ReviewerID: "recovery-verification-review", Role: "verification", Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}
	review := domain.VerificationReviewBundle{ArtifactID: reportArtifact.ID, ArtifactDigest: reportArtifact.Digest, ReportDigest: stored.Reference.Digest, Opinions: []domain.ReviewOpinion{opinion}, CreatedAt: now}
	audit, err := pipeline.audit(opinion.ReviewerID, "verification-review", workflowPlan, opinion)
	if err != nil {
		t.Fatal(err)
	}
	publishing, err := service.GateVerification(context.Background(), started.Workflow.ID, "runtime-worker", stored, review, []domain.AgentAudit{audit})
	if err != nil || publishing.State != domain.Publishing {
		t.Fatalf("persist verification review = %#v, %v", publishing, err)
	}
	if err := pipeline.Recover(context.Background()); err != nil {
		t.Fatalf("recover publication: %v", err)
	}
	workflow, err = service.store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil || workflow.State != domain.Published || store.published == nil || !store.actions[verification.Key()].reconciled {
		t.Fatalf("recovered publication workflow = %#v, published = %#v, binding = %#v, %v", workflow, store.published, store.actions[verification.Key()], err)
	}
}

func TestMetadataContainsAnchorRequiresThePinnedHeading(t *testing.T) {
	metadata := domain.Metadata{Anchors: []string{"pod-lifetime", "pod-phase"}}
	if !metadataContainsAnchor(metadata, "pod-lifetime") {
		t.Fatal("existing heading anchor was rejected")
	}
	if metadataContainsAnchor(metadata, "pod-lifecycle") {
		t.Fatal("missing heading anchor was accepted")
	}
	if !metadataContainsAnchor(metadata, "") {
		t.Fatal("page scope without an anchor was rejected")
	}
}

type pipelineProfiles struct{ profile runnable.RuntimeProfile }

func (p pipelineProfiles) DocumentationRuntimeConstraints() []domain.RuntimeConstraint {
	return []domain.RuntimeConstraint{{Runtime: p.profile.Runtime, BaseImage: p.profile.BaseImage, Resources: p.profile.Resources, Network: p.profile.Network, Topology: p.profile.Topology}}
}

func (p pipelineProfiles) DocumentationLifecyclePolicy() runnable.LifecyclePolicy {
	return runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 300, MaxLifetimeSeconds: 600}
}

func (p pipelineProfiles) ResolveDocumentationRuntimeProfile(domain.RuntimeConstraint) (runnable.RuntimeProfile, error) {
	return p.profile, nil
}

type pipelineGenerator struct {
	blueprint CandidateBlueprint
	fail      bool
}

func (g pipelineGenerator) Generate(context.Context, domain.LearningUnitPlan, runnable.RuntimeProfile, runnable.LifecyclePolicy) (CandidateBlueprint, error) {
	if g.fail {
		return CandidateBlueprint{}, errPipelineGeneratorCalled
	}
	return g.blueprint, nil
}

type pipelineReviewer struct{ role string }

func (r pipelineReviewer) ReviewPlan(_ context.Context, runID string, _ domain.LearningUnitPlan) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}, nil
}

func (r pipelineReviewer) ReviewCandidate(_ context.Context, runID string, _ domain.LearningUnitPlan, _ domain.PracticeCandidate, _ []GeneratedFile) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}, nil
}

func (r pipelineReviewer) ReviewVerification(_ context.Context, runID string, _ domain.LearningUnitPlan, _ runnable.VerificationReport) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}, nil
}

func pipelineBlueprint(plan domain.LearningUnitPlan, seed domain.PracticeCandidate) CandidateBlueprint {
	return CandidateBlueprint{ID: "pod-lifecycle-generated", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, UserSteps: plan.UserSteps, Observations: plan.Observations, Initialization: seed.Spec.Initialization, ValidationPlan: seed.Spec.ValidationPlan, Files: []GeneratedFile{
		{Path: "scripts/init.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/apply.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/assert.sh", Content: "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"pod-running\",\"satisfied\":true,\"summary\":\"running\"}]}'\n", Executable: true},
	}}
}

func sequentialRunIDs() func(string) (string, error) {
	next := 0
	return func(prefix string) (string, error) {
		next++
		return prefix + "-run-" + string(rune('0'+next)), nil
	}
}

var errPipelineGeneratorCalled = &pipelineError{"generator should not run"}

type pipelineError struct{ message string }

func (e *pipelineError) Error() string { return e.message }

func TestLateOrStaleCompletionIsAcknowledgedWithoutAdvancing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{}, fakePlanAgent{}, []PlanReviewRole{pipelineReviewer{role: "evidence"}}, pipelineGenerator{}, []CandidateReviewRole{pipelineReviewer{role: "safety"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := service.Start(ctx, "document-late-completion", testPageIdentity(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A materialization completion belonging to the failed lineage is late.
	stale := runnable.ActionIdentity{
		Content:      runnable.ContentIdentity{Kind: "practice", ID: "page-1", Revision: "1"},
		SpecDigest:   serviceDigest("a"),
		Phase:        runnable.ActionMaterializeArtifact,
		StateVersion: workflow.StateVersion,
	}
	if err := store.BindRunnableAction(ctx, workflow.ID, stale, now); err != nil {
		t.Fatal(err)
	}
	// The workflow left the runnable phase behind: force-fail it first so a
	// terminal workflow must never advance from a late completion.
	if _, err := service.ForceFail(ctx, workflow.ID, "operator unblocked it", &audit.HumanAction{ID: "audit-1", UserID: "u-admin", Action: audit.ActionDocumentationWorkflowForceFail, TargetType: audit.TargetDocumentWorkflow, TargetID: workflow.ID, Detail: json.RawMessage(`{}`), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.ReconcileCompletedAction(ctx, stale); err != nil {
		t.Fatalf("late completion after force-fail: %v", err)
	}
	final, err := store.GetWorkflow(ctx, workflow.ID)
	if err != nil || final.State != domain.Failed {
		t.Fatalf("workflow after late completion = %#v, %v", final, err)
	}
	if !store.actions[stale.Key()].reconciled {
		t.Fatalf("late completion binding was not marked reconciled")
	}

	// A stale state version after a restart is likewise only acknowledged.
	restartAction := audit.HumanAction{ID: "audit-2", UserID: "u-admin", Action: audit.ActionDocumentationWorkflowRestart, TargetType: audit.TargetDocumentWorkflow, TargetID: workflow.ID, Detail: json.RawMessage(`{}`), CreatedAt: now}
	restarted, err := service.Restart(ctx, workflow.ID, "retry", &restartAction)
	if err != nil || restarted.State != domain.Planning {
		t.Fatalf("restart = %#v, %v", restarted, err)
	}
	if err := pipeline.ReconcileCompletedAction(ctx, stale); err != nil {
		t.Fatalf("stale completion after restart: %v", err)
	}
	final, err = store.GetWorkflow(ctx, workflow.ID)
	if err != nil || final.State != domain.Planning {
		t.Fatalf("workflow after stale completion = %#v, %v", final, err)
	}
	if !store.actions[stale.Key()].reconciled {
		t.Fatalf("stale completion binding was not marked reconciled")
	}
}

// rejectingReviewer soft-rejects its first rejections reviews with concrete
// reasons - the shape a live evidence reviewer produces - then approves. With
// hard set, its rejections carry the hard-reject flag instead.
type rejectingReviewer struct {
	role       string
	rejections int
	hard       bool
	reasons    []string
}

func (r *rejectingReviewer) ReviewPlan(_ context.Context, runID string, _ domain.LearningUnitPlan) (domain.ReviewOpinion, error) {
	return r.opinion(runID), nil
}

func (r *rejectingReviewer) ReviewCandidate(_ context.Context, runID string, _ domain.LearningUnitPlan, _ domain.PracticeCandidate, _ []GeneratedFile) (domain.ReviewOpinion, error) {
	return r.opinion(runID), nil
}

func (r *rejectingReviewer) opinion(runID string) domain.ReviewOpinion {
	if r.rejections > 0 {
		r.rejections--
		return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewReject, HardReject: r.hard, Reasons: r.reasons, PolicyVersion: "policy-v1"}
	}
	return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}
}

// newRetryPipeline assembles the full publication chain with injectable
// reviewers so a test can reject a bounded number of attempts.
func newRetryPipeline(t *testing.T, now time.Time, planReviewers []PlanReviewRole, artifactReviewers []CandidateReviewRole) (*AgentPipeline, *memoryDocumentStore, domain.Page) {
	t.Helper()
	plan := validPlan()
	plan.CreatedAt = now
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := pipelineBlueprint(plan, seed)
	compiled, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now)
	if err != nil {
		t.Fatal(err)
	}
	revision, report := serviceRevisionAndReport(t, compiled, now, true)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{revision: revision, report: report})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{page: page, metadata: domain.Metadata{Context: plan.Context, Path: page.Path, Title: plan.Title, Anchors: []string{"pod-lifecycle"}}}, fakePlanAgent{plan: plan}, planReviewers, pipelineGenerator{blueprint: blueprint}, artifactReviewers, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{profile: seed.Spec.RuntimeProfile}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newRunID = sequentialRunIDs()
	return pipeline, store, page
}

func countLedgerKinds(workflow domain.Workflow) map[string]int {
	kinds := map[string]int{}
	for _, artifact := range workflow.Artifacts {
		kinds[artifact.Kind]++
	}
	return kinds
}

func TestAgentPipelineAutoRestartsPlanGateRejectionAndRetriesToMaterialization(t *testing.T) {
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	pipeline, store, page := newRetryPipeline(t, now,
		[]PlanReviewRole{
			&rejectingReviewer{role: "evidence", rejections: 1, reasons: []string{"startup recreates the observed file on every boot; the disappearance assertion can never hold"}},
			pipelineReviewer{role: "value"},
		},
		[]CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}})

	started, err := pipeline.Start(context.Background(), "agent-pipeline-plan-retry", page.Path, "", nil)
	if err != nil || started.Workflow.State != domain.MaterializingArtifact || started.Workflow.Revision != 2 {
		t.Fatalf("retrying start = %#v, %v", started.Workflow, err)
	}
	entries, err := store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := countLedgerKinds(entries)
	// Each attempt lands in independent, attempt-namespaced ledger entries.
	if kinds["learning-unit-plan"] != 2 || kinds["plan-gate"] != 2 || kinds["pipeline.auto_restart"] != 1 {
		t.Fatalf("retry ledger kinds = %#v", kinds)
	}
	planArtifacts := []string{}
	for _, artifact := range entries.Artifacts {
		if artifact.Kind == "learning-unit-plan" {
			planArtifacts = append(planArtifacts, artifact.ID)
		}
	}
	if !strings.HasSuffix(planArtifacts[0], "-a1") || !strings.HasSuffix(planArtifacts[1], "-a2") {
		t.Fatalf("plan artifacts are not attempt-namespaced: %#v", planArtifacts)
	}
	if len(store.pipelineAutoRestarts) != 1 || store.pipelineAutoRestarts[0].gateKind != "plan-gate" || len(store.pipelineAutoRestarts[0].reasons) == 0 {
		t.Fatalf("auto restarts = %#v", store.pipelineAutoRestarts)
	}
	// The internal restart is a pipeline decision, not an administrator's.
	if len(store.humanActions) != 0 {
		t.Fatalf("auto restart recorded a human action: %#v", store.humanActions)
	}
	gates := []domain.GateResult{}
	for _, artifact := range entries.Artifacts {
		if artifact.Kind == "plan-gate" {
			var gate domain.GateResult
			if err := json.Unmarshal(artifact.Payload, &gate); err != nil {
				t.Fatal(err)
			}
			gates = append(gates, gate)
		}
	}
	if gates[0].Approved() || len(gates[0].Reasons) == 0 || !gates[1].Approved() {
		t.Fatalf("gate outcomes = %#v", gates)
	}
}

func TestAgentPipelineAutoRestartsCandidateGateRejectionAndRetriesToMaterialization(t *testing.T) {
	now := time.Date(2026, 9, 21, 11, 0, 0, 0, time.UTC)
	pipeline, store, page := newRetryPipeline(t, now,
		[]PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}},
		[]CandidateReviewRole{
			&rejectingReviewer{role: "safety", rejections: 1, reasons: []string{"assertion writes outside the read-only boundary"}},
			pipelineReviewer{role: "consistency"},
		})

	started, err := pipeline.Start(context.Background(), "agent-pipeline-candidate-retry", page.Path, "", nil)
	if err != nil || started.Workflow.State != domain.MaterializingArtifact || started.Workflow.Revision != 2 {
		t.Fatalf("retrying start = %#v, %v", started.Workflow, err)
	}
	entries, err := store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := countLedgerKinds(entries)
	if kinds["practice-candidate"] != 2 || kinds["artifact-gate"] != 2 || kinds["pipeline.auto_restart"] != 1 {
		t.Fatalf("retry ledger kinds = %#v", kinds)
	}
	if len(store.pipelineAutoRestarts) != 1 || store.pipelineAutoRestarts[0].gateKind != "artifact-gate" {
		t.Fatalf("auto restarts = %#v", store.pipelineAutoRestarts)
	}
}

func TestAgentPipelineGateRejectionExhaustsRetryBudgetIntoTerminalRejected(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	pipeline, store, page := newRetryPipeline(t, now,
		[]PlanReviewRole{
			&rejectingReviewer{role: "evidence", rejections: 100, reasons: []string{"observation remains ungrounded"}},
			pipelineReviewer{role: "value"},
		},
		[]CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}})

	// Default budget: revision starts at 1 with max_revisions 3, so the chain
	// runs three attempts (two automatic restarts) before the third rejection
	// lands in the terminal the administrator rescues.
	started, err := pipeline.Start(context.Background(), "agent-pipeline-budget", page.Path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if started.Workflow.State != domain.Rejected || started.Workflow.Revision != 3 {
		t.Fatalf("exhausted start = %#v", started.Workflow)
	}
	entries, err := store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := countLedgerKinds(entries)
	if kinds["learning-unit-plan"] != 3 || kinds["plan-gate"] != 3 || kinds["pipeline.auto_restart"] != 2 {
		t.Fatalf("exhausted ledger kinds = %#v", kinds)
	}
	if !entries.State.Terminal() {
		t.Fatalf("exhausted workflow is not terminal: %s", entries.State)
	}
}

func TestAgentPipelineHardGateRejectionNeverAutoRestarts(t *testing.T) {
	now := time.Date(2026, 9, 21, 13, 0, 0, 0, time.UTC)
	pipeline, store, page := newRetryPipeline(t, now,
		[]PlanReviewRole{
			&rejectingReviewer{role: "evidence", rejections: 1, hard: true, reasons: []string{"fabricated evidence reference"}},
			pipelineReviewer{role: "value"},
		},
		[]CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}})

	started, err := pipeline.Start(context.Background(), "agent-pipeline-hard-reject", page.Path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if started.Workflow.State != domain.Rejected || started.Workflow.Revision != 1 {
		t.Fatalf("hard-rejected start = %#v", started.Workflow)
	}
	entries, err := store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if kinds := countLedgerKinds(entries); kinds["pipeline.auto_restart"] != 0 {
		t.Fatalf("hard rejection auto restarted: %#v", kinds)
	}
	for _, artifact := range entries.Artifacts {
		if artifact.Kind != "plan-gate" {
			continue
		}
		var gate domain.GateResult
		if err := json.Unmarshal(artifact.Payload, &gate); err != nil {
			t.Fatal(err)
		}
		if !gate.HardReject {
			t.Fatalf("gate result lost the hard-reject flag: %#v", gate)
		}
	}
}
